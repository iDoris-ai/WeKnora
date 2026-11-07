// Agent24 fork product patch A24-KB-33 (KB-C2-01b2, WeKnora#133, CONTRACT-UI-BRIDGE-v1 U2.6–U2.8, U3.1–U3.4).

package handler

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/agent24uisession"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// agent24UISessionDeps is what agent24UIVerifySession needs. C2-01c's middleware reuses the function.
type agent24UISessionDeps struct {
	store  interfaces.Agent24UISessionStore
	key    []byte
	issuer string
	stored agent24UIStoredDeps
	// epoch refuses session JWTs issued before this process started (U2.3, A24-KB-33).
	epoch time.Time
}

// Agent24KnowledgeUISessionHandler serves GET /api/v1/agent24/knowledge/ui/session.
type Agent24KnowledgeUISessionHandler struct {
	deps agent24UISessionDeps
}

// NewAgent24KnowledgeUISessionHandler builds the session probe.
// An unset UI session key, or standalone mode, returns a nil handler so the route is not registered.
// A key that is set but invalid returns an error so the container fails closed.
// The signing key and issuer are read once here.
func NewAgent24KnowledgeUISessionHandler(
	tenants interfaces.TenantService, keys interfaces.TenantAPIKeyService,
	revocations interfaces.Agent24CredentialRevocationStore,
	store interfaces.Agent24UISessionStore,
) (*Agent24KnowledgeUISessionHandler, error) {
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
	return &Agent24KnowledgeUISessionHandler{deps: agent24UISessionDeps{
		store:  store,
		key:    copied,
		issuer: agent24uisession.IssuerFromEnv(),
		stored: agent24UIStoredDeps{tenants: tenants, keys: keys, revocations: revocations, key: copied},
		epoch:  agent24uisession.Epoch(),
	}}, nil
}

type agent24UISessionResponse struct {
	ExpiresIn int `json:"expires_in"`
}

// Probe reports the remaining life of a session JWT. It does not recompute E.
func (h *Agent24KnowledgeUISessionHandler) Probe(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	now := time.Now()
	claims, ticket, _, kerr := agent24UIVerifySession(c.Request.Context(), h.deps, c.Request.Header, now)
	if ticket != nil {
		agent24UINoteTicket(c, ticket, "", false)
	}
	if kerr != nil {
		writeAgent24KnowledgeError(c, kerr, agent24KnowledgeEcho{})
		return
	}
	secs := 0
	if claims != nil && claims.ExpiresAt != nil {
		secs = int(claims.ExpiresAt.Sub(now) / time.Second)
		if secs < 0 {
			secs = 0
		}
	}
	agent24NoteAudit(c, func(f *agent24AuditFacts) { f.Outcome = agent24AuditOK })
	c.JSON(http.StatusOK, agent24UISessionResponse{ExpiresIn: secs})
}

// agent24UIVerifySession checks a session JWT, the view proof, the ticket row, the view family and the
// credential the ticket recorded. A non-nil error is the response. The ticket is returned when a row was
// loaded, including on refusal, so the audit can name its principal. The probe does not compute E.
func agent24UIVerifySession(
	ctx context.Context, deps agent24UISessionDeps, header http.Header, now time.Time,
) (*agent24uisession.Claims, *types.Agent24UITicket, types.TenantAPIKeyScope, *agent24KnowledgeError) {
	token := agent24UIBearerToken(header.Get("Authorization"))
	if token == "" {
		return nil, nil, types.TenantAPIKeyScope{}, agent24Unauthorized("credential_invalid")
	}
	claims, err := agent24uisession.Verify(deps.key, deps.issuer, token, now)
	if err != nil {
		if errors.Is(err, agent24uisession.ErrExpired) {
			return nil, nil, types.TenantAPIKeyScope{}, agent24Unauthorized("credential_expired")
		}
		return nil, nil, types.TenantAPIKeyScope{}, agent24Unauthorized("credential_invalid")
	}
	// U2.3: a session issued before this process started is refused. A revocation that could not be
	// written lived only in the previous process.
	if agent24uisession.IssuedBefore(claims, deps.epoch) {
		return nil, agent24UITicketForAudit(ctx, deps.store, claims.TicketID), types.TenantAPIKeyScope{},
			agent24Unauthorized("credential_invalid")
	}
	secret, perr := agent24uisession.ParseViewProof(header.Get(agent24UIViewProofHeader))
	if perr != nil || !agent24uisession.VPHEqual(agent24uisession.VPH(secret), claims.Cnf.VPH) {
		return nil, agent24UITicketForAudit(ctx, deps.store, claims.TicketID), types.TenantAPIKeyScope{},
			agent24Unauthorized("credential_invalid")
	}
	if deps.store == nil {
		return nil, nil, types.TenantAPIKeyScope{}, agent24Internal()
	}
	revoked, err := deps.store.FamilyRevoked(ctx, claims.Cnf.VPH)
	if err != nil {
		agent24UILogErr(ctx, "ui session family", err)
		return nil, nil, types.TenantAPIKeyScope{}, agent24Internal()
	}
	ticket, err := deps.store.GetTicketByID(ctx, claims.TicketID)
	if err != nil {
		agent24UILogErr(ctx, "ui session ticket", err)
		return nil, nil, types.TenantAPIKeyScope{}, agent24Internal()
	}
	if revoked || ticket == nil || ticket.RevokedAt != nil ||
		ticket.TenantID != claims.TenantID || ticket.Principal != claims.Subject ||
		!agent24uisession.VPHEqual(ticket.VPH, claims.Cnf.VPH) {
		return nil, ticket, types.TenantAPIKeyScope{}, agent24Unauthorized("credential_invalid")
	}
	scope, reason, err := agent24UIRecheckStored(ctx, deps.stored, ticket, now)
	if err != nil {
		agent24UILogErr(ctx, "ui session recheck", err)
		return nil, ticket, types.TenantAPIKeyScope{}, agent24Internal()
	}
	if reason != "" {
		return nil, ticket, types.TenantAPIKeyScope{}, agent24Unauthorized(reason)
	}
	return claims, ticket, scope, nil
}

// agent24UITicketForAudit loads a ticket to name the audit row. A store error stays a view-proof refusal,
// not an internal error: the decision is already credential_invalid.
func agent24UITicketForAudit(
	ctx context.Context, store interfaces.Agent24UISessionStore, ticketID string,
) *types.Agent24UITicket {
	if store == nil || ticketID == "" {
		return nil
	}
	ticket, err := store.GetTicketByID(ctx, ticketID)
	if err != nil {
		return nil
	}
	return ticket
}

// agent24UIBearerToken accepts "Bearer <token>" with a case-insensitive scheme. Anything else is empty.
func agent24UIBearerToken(header string) string {
	fields := strings.Fields(header)
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
		return ""
	}
	return fields[1]
}
