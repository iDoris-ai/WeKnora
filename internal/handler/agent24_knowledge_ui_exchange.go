// Agent24 fork product patch A24-KB-33 (KB-C2-01b2, WeKnora#133, CONTRACT-UI-BRIDGE-v1 U2.6–U2.8, U3.1–U3.4).

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/time/rate"

	"github.com/Tencent/WeKnora/internal/agent24uisession"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

const (
	// A24-KB-33: the exchange has its own local token bucket (U2.6). Five per second, burst ten.
	agent24UIExchangeRate  = 5
	agent24UIExchangeBurst = 10
	// A full table drops an idle bucket. When none is idle the request is allowed, as the refused-key
	// audit limiter does (A24-KB-26): a new bucket would start the same way, and refusing would be a
	// different rule.
	agent24UIExchangeBuckets = 1024

	agent24UIViewProofHeader = "X-Agent24-View-Proof"
)

// Agent24KnowledgeUIExchangeHandler serves POST /api/v1/agent24/knowledge/ui/exchange.
type Agent24KnowledgeUIExchangeHandler struct {
	scope  agent24ScopeResolver
	store  interfaces.Agent24UISessionStore
	stored agent24UIStoredDeps
	key    []byte
	issuer string
	limit  *agent24UIExchangeLimiter
}

// NewAgent24KnowledgeUIExchangeHandler builds the exchange handler.
// An unset UI session key, or standalone mode, returns a nil handler so the route is not registered.
// A key that is set but invalid returns an error so the container fails closed.
// The signing key and issuer are read once here.
func NewAgent24KnowledgeUIExchangeHandler(
	kbService interfaces.KnowledgeBaseService,
	tenants interfaces.TenantService, keys interfaces.TenantAPIKeyService,
	revocations interfaces.Agent24CredentialRevocationStore,
	store interfaces.Agent24UISessionStore,
) (*Agent24KnowledgeUIExchangeHandler, error) {
	on, err := agent24UISessionKeyGate()
	if err != nil || !on {
		return nil, err
	}
	key, configured, err := agent24uisession.KeyFromEnv()
	if err != nil {
		return nil, err
	}
	if !configured || len(key) == 0 {
		return nil, errors.New("ui session key is not configured")
	}
	copied := make([]byte, len(key))
	copy(copied, key)
	return &Agent24KnowledgeUIExchangeHandler{
		scope:  agent24DefaultScopeResolver{kbs: kbService},
		store:  store,
		stored: agent24UIStoredDeps{tenants: tenants, keys: keys, revocations: revocations},
		key:    copied,
		issuer: agent24uisession.IssuerFromEnv(),
		limit:  newAgent24UIExchangeLimiter(),
	}, nil
}

// agent24UIExchangeLimiter is one token bucket per client IP. now is replaceable in tests.
type agent24UIExchangeLimiter struct {
	mu      sync.Mutex
	burst   int
	max     int
	now     func() time.Time
	buckets map[string]*rate.Limiter
}

func newAgent24UIExchangeLimiter() *agent24UIExchangeLimiter {
	return &agent24UIExchangeLimiter{
		burst: agent24UIExchangeBurst, max: agent24UIExchangeBuckets,
		now: time.Now, buckets: map[string]*rate.Limiter{},
	}
}

// allow reports whether ip may exchange now. A nil limiter refuses (fail closed).
func (l *agent24UIExchangeLimiter) allow(ip string) bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	bucket, ok := l.buckets[ip]
	if !ok {
		if len(l.buckets) >= l.max && !l.removeIdle(now) {
			return true
		}
		bucket = rate.NewLimiter(rate.Limit(agent24UIExchangeRate), l.burst)
		l.buckets[ip] = bucket
	}
	return bucket.AllowN(now, 1)
}

func (l *agent24UIExchangeLimiter) removeIdle(now time.Time) bool {
	for ip, bucket := range l.buckets {
		if bucket.TokensAt(now) >= float64(l.burst) {
			delete(l.buckets, ip)
			return true
		}
	}
	return false
}

type agent24UIExchangeScope struct {
	KnowledgeBaseIDs []string `json:"knowledge_base_ids"`
}

type agent24UIExchangeResponse struct {
	SessionToken   string                 `json:"session_token"`
	ExpiresIn      int                    `json:"expires_in"`
	Ops            []string               `json:"ops"`
	EffectiveScope agent24UIExchangeScope `json:"effective_scope"`
}

// Exchange redeems one ticket. The decision order is the task's: rate limit, body, hash, consume, expiry,
// revocation, family, view proof, stored C, E, then the session JWT. Steps that fail as credential_invalid
// before a session exists share one response body; the audit row carries the internal reason.
func (h *Agent24KnowledgeUIExchangeHandler) Exchange(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if h.limit == nil || !h.limit.allow(c.ClientIP()) {
		writeAgent24KnowledgeError(c, agent24Unavailable("overloaded"), agent24KnowledgeEcho{})
		return
	}
	body, ok := agent24ReadBody(c)
	if !ok {
		writeAgent24KnowledgeError(c, agent24InvalidRequest(), agent24KnowledgeEcho{})
		return
	}
	raw, kerr := parseAgent24UIExchangeRequest(body)
	if kerr != nil {
		writeAgent24KnowledgeError(c, kerr, agent24KnowledgeEcho{})
		return
	}
	hash, err := agent24uisession.TicketHash(raw)
	if err != nil {
		agent24UIRefuseTicket(c, nil, "ticket_malformed", false)
		return
	}
	now := time.Now()
	ctx := c.Request.Context()
	if h.store == nil {
		writeAgent24KnowledgeError(c, agent24Internal(), agent24KnowledgeEcho{})
		return
	}
	ticket, first, err := h.store.ConsumeTicket(ctx, hash, now)
	if err != nil {
		agent24UILogErr(ctx, "ui exchange consume", err)
		writeAgent24KnowledgeError(c, agent24Internal(), agent24KnowledgeEcho{})
		return
	}
	if ticket == nil {
		agent24UIRefuseTicket(c, nil, "ticket_unknown", false)
		return
	}
	if !first {
		h.agent24UILeakFamily(c, ticket, now, "ticket_replayed")
		return
	}
	if !now.Before(ticket.ExpiresAt) {
		agent24UIRefuseTicket(c, ticket, "ticket_expired", false)
		return
	}
	if ticket.RevokedAt != nil {
		agent24UIRefuseTicket(c, ticket, "ticket_revoked", false)
		return
	}
	revoked, err := h.store.FamilyRevoked(ctx, ticket.VPH)
	if err != nil {
		agent24UILogErr(ctx, "ui exchange family", err)
		agent24UINoteTicket(c, ticket, "", false)
		writeAgent24KnowledgeError(c, agent24Internal(), agent24KnowledgeEcho{})
		return
	}
	if revoked {
		agent24UIRefuseTicket(c, ticket, "family_revoked", false)
		return
	}
	secret, err := agent24uisession.ParseViewProof(c.GetHeader(agent24UIViewProofHeader))
	if err != nil || !agent24uisession.VPHEqual(agent24uisession.VPH(secret), ticket.VPH) {
		h.agent24UILeakFamily(c, ticket, now, "view_proof_mismatch")
		return
	}
	scope, reason, keyExp, err := agent24UIRecheckStoredKey(ctx, h.stored, ticket, now)
	if err != nil {
		agent24UILogErr(ctx, "ui exchange recheck", err)
		agent24UINoteTicket(c, ticket, "", false)
		writeAgent24KnowledgeError(c, agent24Internal(), agent24KnowledgeEcho{})
		return
	}
	if reason != "" {
		agent24UINoteTicket(c, ticket, "", false)
		writeAgent24KnowledgeError(c, agent24Unauthorized(reason), agent24KnowledgeEcho{})
		return
	}
	effective, kerr := h.agent24UIEffectiveScope(c, ticket, scope)
	if kerr != nil {
		writeAgent24KnowledgeError(c, kerr, agent24KnowledgeEcho{})
		return
	}
	if effective == nil {
		return // client cancel: the response is already decided
	}
	lifetime := agent24uisession.Lifetime(now, agent24EarlierExpiry(ticket.CredentialExpiresAt, keyExp))
	// Less than one second would encode as expires_in 0. Treat that the same as an already expired C.
	if lifetime < time.Second {
		agent24UINoteTicket(c, ticket, "", false)
		writeAgent24KnowledgeError(c, agent24Unauthorized("credential_expired"), agent24KnowledgeEcho{})
		return
	}
	token, err := h.agent24UIIssueSession(ticket, effective, now, lifetime)
	if err != nil {
		agent24UILogErr(ctx, "ui exchange issue", err)
		agent24UINoteTicket(c, ticket, "", false)
		writeAgent24KnowledgeError(c, agent24Internal(), agent24KnowledgeEcho{})
		return
	}
	agent24UINoteTicket(c, ticket, "", false)
	agent24NoteAudit(c, func(f *agent24AuditFacts) {
		f.Outcome = agent24AuditOK
		f.EffectiveScope = effective
	})
	c.JSON(http.StatusOK, agent24UIExchangeResponse{
		SessionToken:   token,
		ExpiresIn:      int(lifetime / time.Second),
		Ops:            append([]string(nil), agent24uisession.UIOps...),
		EffectiveScope: agent24UIExchangeScope{KnowledgeBaseIDs: effective},
	})
}

// agent24UIEffectiveScope is E = A ∩ C_now ∩ the ticket scope. Full access and an empty allow-list are
// empty_scope (R2.8): an empty allow-list is never "all". nil, nil means the client cancelled.
func (h *Agent24KnowledgeUIExchangeHandler) agent24UIEffectiveScope(
	c *gin.Context, ticket *types.Agent24UITicket, scope types.TenantAPIKeyScope,
) ([]string, *agent24KnowledgeError) {
	agent24UINoteTicket(c, ticket, "", false)
	if scope.FullAccess || len(scope.KnowledgeBaseIDs) == 0 {
		return nil, agent24Forbidden("empty_scope")
	}
	if h.scope == nil {
		return nil, agent24Internal()
	}
	ids, err := ticket.ScopeKnowledgeBaseIDs()
	if err != nil {
		agent24UILogErr(c.Request.Context(), "ui exchange scope", err)
		return nil, agent24Internal()
	}
	ctx := types.WithCaller(c.Request.Context(), types.Caller{TenantID: ticket.TenantID})
	ctx = types.WithTenantAPIKeyScope(ctx, scope)
	kbs, err := h.scope.Resolve(ctx, ids)
	if err != nil {
		e, gone := agent24StorageError(ctx, err)
		if gone {
			agent24NoteCancelled(c, agent24KnowledgeEcho{}, nil)
			return nil, nil
		}
		return nil, e
	}
	if len(kbs) == 0 {
		return nil, agent24Forbidden("empty_scope")
	}
	return agent24CollectedKBIDs(kbs), nil
}

func (h *Agent24KnowledgeUIExchangeHandler) agent24UIIssueSession(
	ticket *types.Agent24UITicket, effective []string, now time.Time, lifetime time.Duration,
) (string, error) {
	gen := ticket.Generation
	return agent24uisession.Issue(h.key, h.issuer, agent24uisession.Claims{
		RegisteredClaims:   jwt.RegisteredClaims{Subject: ticket.Principal},
		TenantID:           ticket.TenantID,
		Scope:              agent24uisession.Scope{KnowledgeBaseIDs: append([]string(nil), effective...)},
		Ops:                append([]string(nil), agent24uisession.UIOps...),
		TicketID:           ticket.TicketID,
		ParentCredentialID: ticket.CredentialID,
		BindingTag:         ticket.BindingTag,
		PartitionTag:       ticket.PartitionTag,
		Cnf:                agent24uisession.Confirmation{VPH: ticket.VPH},
		Gen:                &gen,
	}, now, lifetime)
}

// agent24EarlierExpiry is the earlier of two expiries. A nil side does not expire.
func agent24EarlierExpiry(a, b *time.Time) *time.Time {
	switch {
	case a == nil:
		return b
	case b == nil:
		return a
	case b.Before(*a):
		return b
	default:
		return a
	}
}

func (h *Agent24KnowledgeUIExchangeHandler) agent24UILeakFamily(
	c *gin.Context, ticket *types.Agent24UITicket, now time.Time, internalReason string,
) {
	agent24UINoteTicket(c, ticket, internalReason, true)
	if h.store != nil && ticket != nil {
		err := h.store.RevokeFamily(c.Request.Context(), ticket.TenantID, ticket.Principal, ticket.VPH, now)
		if err != nil {
			agent24UILogErr(c.Request.Context(), "ui exchange revoke family", err)
		}
	}
	writeAgent24KnowledgeError(c, agent24Unauthorized("credential_invalid"), agent24KnowledgeEcho{})
}

func agent24UIRefuseTicket(c *gin.Context, ticket *types.Agent24UITicket, internalReason string, leak bool) {
	agent24UINoteTicket(c, ticket, internalReason, leak)
	writeAgent24KnowledgeError(c, agent24Unauthorized("credential_invalid"), agent24KnowledgeEcho{})
}

// agent24UINoteTicket records the ticket's identity and the audit resource. The raw ticket, the session
// JWT and the view proof are not recorded. internal_reason is omitted when empty. leak marks a U2.8 row:
// the audit middleware writes one row per request, so the leak fields live on this exchange row.
func agent24UINoteTicket(c *gin.Context, ticket *types.Agent24UITicket, internalReason string, leak bool) {
	agent24NoteAudit(c, func(f *agent24AuditFacts) {
		resource := map[string]any{}
		if internalReason != "" {
			resource["internal_reason"] = internalReason
		}
		if ticket != nil {
			resource["ticket_id_hash"] = agent24uisession.H("kb-ticket-id-v1", []byte(ticket.TicketID))
			resource["binding_tag"] = ticket.BindingTag
			if p, ok := agent24PrincipalFromStorageID(ticket.Principal); ok {
				copied := p
				f.Principal = &copied
			}
			f.CredentialID = ticket.CredentialID
			f.TenantID = ticket.TenantID
		}
		if leak && ticket != nil {
			resource["leak"] = true
			resource["vph_hash"] = agent24uisession.H("kb-vph-audit-v1", []byte(ticket.VPH))
		}
		f.Resource = resource
	})
}

func agent24PrincipalFromStorageID(storage string) (types.Principal, bool) {
	typ, id, ok := strings.Cut(storage, ":")
	if !ok {
		return types.Principal{}, false
	}
	p := types.Principal{Type: typ, ID: id}
	if !p.Valid() {
		return types.Principal{}, false
	}
	return p, true
}

// agent24UILogErr records an infrastructure failure without the ticket, the JWT, the view proof, or the
// error text. The length and the type are enough to tell a store failure from a recheck failure.
func agent24UILogErr(ctx context.Context, where string, err error) {
	logger.Warnf(ctx, "agent24 knowledge: %s: error_len=%d error_type=%T", where, len(fmt.Sprint(err)), err)
}

// parseAgent24UIExchangeRequest accepts one JSON object whose only field is the string "ticket".
func parseAgent24UIExchangeRequest(body []byte) (string, *agent24KnowledgeError) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var fields map[string]json.RawMessage
	if err := dec.Decode(&fields); err != nil || fields == nil {
		return "", agent24InvalidRequest()
	}
	if _, err := dec.Token(); err != io.EOF {
		return "", agent24InvalidRequest()
	}
	if len(fields) != 1 {
		return "", agent24InvalidRequest()
	}
	raw, ok := fields["ticket"]
	if !ok {
		return "", agent24InvalidRequest()
	}
	// json.Unmarshal turns null into "", which would reach the ticket checks as credential_invalid.
	// A ticket that is not a JSON string is a malformed request, not a bad credential.
	var ticket string
	if !bytes.HasPrefix(bytes.TrimSpace(raw), []byte(`"`)) || json.Unmarshal(raw, &ticket) != nil {
		return "", agent24InvalidRequest()
	}
	return ticket, nil
}
