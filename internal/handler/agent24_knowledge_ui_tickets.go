// Agent24 fork product patch A24-KB-32 (KB-C2-01b1, WeKnora#133, CONTRACT-UI-BRIDGE-v1 U2.1, U2.13, U3.7).

package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/agent24uisession"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// agent24CredentialNearExpiry is the remaining life of C below which issuance is refused (U2.1 ③).
const agent24CredentialNearExpiry = 60 * time.Second

// Agent24KnowledgeUITicketsHandler serves POST /api/v1/agent24/knowledge/ui/tickets.
type Agent24KnowledgeUITicketsHandler struct {
	scope   agent24ScopeResolver
	recheck agent24CredentialRechecker
	store   interfaces.Agent24UISessionStore
}

// NewAgent24KnowledgeUITicketsHandler builds the issuance handler.
// An unset UI session key returns a nil handler so the route is not registered.
// A key that is set but invalid returns an error so the container fails closed.
func NewAgent24KnowledgeUITicketsHandler(
	kbService interfaces.KnowledgeBaseService,
	tenants interfaces.TenantService, keys interfaces.TenantAPIKeyService,
	revocations interfaces.Agent24CredentialRevocationStore,
	users interfaces.UserService, members interfaces.TenantMemberService, cfg *config.Config,
	store interfaces.Agent24UISessionStore,
) (*Agent24KnowledgeUITicketsHandler, error) {
	on, err := agent24UISessionKeyGate()
	if err != nil || !on {
		return nil, err
	}
	return &Agent24KnowledgeUITicketsHandler{
		scope:   agent24DefaultScopeResolver{kbs: kbService},
		recheck: newAgent24Rechecker(tenants, keys, revocations, users, members, cfg),
		store:   store,
	}, nil
}

// agent24UISessionKeyGate reports whether UI session routes should be built (A24-KB-32).
// Unset or blank is (false, nil): the constructor returns a nil handler and the route stays off.
// A value that is set but invalid is an error: the container fails rather than serving the routes
// without a usable key. Integration startup already refuses that key.
func agent24UISessionKeyGate() (bool, error) {
	_, configured, err := agent24uisession.KeyFromEnv()
	if err != nil {
		return false, err
	}
	return configured, nil
}

// agent24UITicketResponse is the issuance body. The raw ticket appears only here.
type agent24UITicketResponse struct {
	Ticket    string `json:"ticket"`
	TicketID  string `json:"ticket_id"`
	ExpiresIn int    `json:"expires_in"`
}

// Issue serves one ticket issuance. The decision order is shape, recheck, principal, E, near-expiry, then
// the store. Every response, including errors, is Cache-Control: no-store.
func (h *Agent24KnowledgeUITicketsHandler) Issue(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	body, ok := agent24ReadBody(c)
	if !ok {
		writeAgent24KnowledgeError(c, agent24InvalidRequest(), agent24KnowledgeEcho{})
		return
	}
	binding, partition, vph, generation, echo, kerr := parseAgent24UITicketRequest(body)
	if kerr != nil {
		writeAgent24KnowledgeError(c, kerr, echo)
		return
	}
	// U2.13: once the request parses, every outcome, refusals included, records the binding; the ticket id
	// hash is added as soon as an id exists.
	resource := map[string]any{"binding_tag": binding}
	agent24NoteAudit(c, func(f *agent24AuditFacts) { f.Resource = resource })
	ctx, ok := agent24UIRecheck(c, h.recheck, echo)
	if !ok {
		return
	}
	principal, tenantID, credID, ok := agent24UIIssuer(ctx)
	if !ok {
		writeAgent24KnowledgeError(c, agent24Unauthorized("credential_invalid"), echo)
		return
	}
	// E = A ∩ C. The request carries no Q. FullAccess, an empty allow-list, and a credential with no API
	// key scope are empty E (R2.8): an empty allow-list is never "all". AllowsKnowledgeBase treats an
	// empty list as every KB, so it is not used here.
	apiScope, scopeOK := types.TenantAPIKeyScopeFromContext(ctx)
	if !scopeOK || apiScope.FullAccess || len(apiScope.KnowledgeBaseIDs) == 0 {
		writeAgent24KnowledgeError(c, agent24Forbidden("empty_scope"), echo)
		return
	}
	if h.scope == nil {
		writeAgent24KnowledgeError(c, agent24Internal(), echo)
		return
	}
	kbs, err := h.scope.Resolve(ctx, []string(apiScope.KnowledgeBaseIDs))
	if err != nil {
		if e, gone := agent24StorageError(ctx, err); gone {
			agent24NoteCancelled(c, echo, nil)
		} else {
			writeAgent24KnowledgeError(c, e, echo)
		}
		return
	}
	if len(kbs) == 0 {
		writeAgent24KnowledgeError(c, agent24Forbidden("empty_scope"), echo)
		return
	}
	scope := agent24CollectedKBIDs(kbs)
	now := time.Now()
	var credExp *time.Time
	if exp, expOK := types.Agent24CredentialExpiryFromContext(ctx); expOK {
		if exp.Sub(now) < agent24CredentialNearExpiry {
			writeAgent24KnowledgeError(c, agent24Unavailable("credential_near_expiry"), echo)
			return
		}
		copied := exp
		credExp = &copied
	}
	raw, hash, ticketID, err := agent24uisession.NewTicket()
	if err != nil {
		logger.Warnf(ctx, "agent24 knowledge: ui ticket: error_len=%d error_type=%T", len(fmt.Sprint(err)), err)
		writeAgent24KnowledgeError(c, agent24Internal(), echo)
		return
	}
	resource["ticket_id_hash"] = agent24uisession.H("kb-ticket-id-v1", []byte(ticketID))
	if h.store == nil {
		writeAgent24KnowledgeError(c, agent24Internal(), echo)
		return
	}
	ticket := &types.Agent24UITicket{
		TicketHash:          hash,
		TicketID:            ticketID,
		TenantID:            tenantID,
		Principal:           principal.StorageID(),
		CredentialID:        credID,
		CredentialExpiresAt: credExp,
		BindingTag:          binding,
		PartitionTag:        partition,
		VPH:                 vph,
		Generation:          generation,
		ExpiresAt:           now.Add(agent24uisession.TicketDefaultTTL),
	}
	if err = ticket.SetScopeKnowledgeBaseIDs(scope); err != nil {
		writeAgent24KnowledgeError(c, agent24Internal(), echo)
		return
	}
	if err = h.store.CreateTicket(ctx, ticket); err != nil {
		if errors.Is(err, interfaces.ErrAgent24UIFamilyOwner) ||
			errors.Is(err, interfaces.ErrAgent24UIFamilyRevoked) {
			writeAgent24KnowledgeError(c, agent24Unauthorized("credential_invalid"), echo)
			return
		}
		logger.Warnf(ctx, "agent24 knowledge: ui ticket store: error_len=%d error_type=%T", len(fmt.Sprint(err)), err)
		writeAgent24KnowledgeError(c, agent24Internal(), echo)
		return
	}
	// The raw ticket is written only in the response below. Audit records its id hash, never the raw value.
	agent24NoteAudit(c, func(f *agent24AuditFacts) {
		f.Outcome = agent24AuditOK
		f.Echo = echo
		f.EffectiveScope = scope
	})
	c.JSON(http.StatusOK, agent24UITicketResponse{
		Ticket:    raw,
		TicketID:  ticketID,
		ExpiresIn: int(agent24uisession.TicketDefaultTTL / time.Second),
	})
}

// agent24UIIssuer reads the principal, tenant and credential id issuance requires.
// Principal is stored as StorageID: Principal has no String method, and that is the canonical form.
func agent24UIIssuer(ctx context.Context) (types.Principal, uint64, string, bool) {
	principal, ok := types.PrincipalFromContext(ctx)
	if !ok || !principal.Valid() {
		return types.Principal{}, 0, "", false
	}
	tenantID := types.CallerFromContext(ctx).TenantID
	if tenantID == 0 {
		return types.Principal{}, 0, "", false
	}
	credID, ok := types.CredentialIDFromContext(ctx)
	if !ok || credID == "" {
		return types.Principal{}, 0, "", false
	}
	return principal, tenantID, credID, true
}

// agent24UIRecheck runs the credential recheck. false means the response is already decided:
// a reason is unauthorized/<reason>, a client cancel is noted and not written, and any other error is internal.
func agent24UIRecheck(
	c *gin.Context, recheck agent24CredentialRechecker, echo agent24KnowledgeEcho,
) (context.Context, bool) {
	if recheck == nil {
		logger.Warnf(c.Request.Context(), "[agent24] credential recheck is not wired")
		writeAgent24KnowledgeError(c, agent24Internal(), echo)
		return nil, false
	}
	ctx, reason, err := recheck.Recheck(c.Request.Context(), c.Request.Header)
	if err != nil {
		if c.Request.Context().Err() != nil {
			e, gone := agent24StorageError(c.Request.Context(), err)
			if gone {
				agent24NoteCancelled(c, echo, nil)
				return nil, false
			}
			writeAgent24KnowledgeError(c, e, echo)
			return nil, false
		}
		logger.Warnf(c.Request.Context(),
			"agent24 knowledge: credential recheck failed: error_len=%d error_type=%T", len(fmt.Sprint(err)), err)
		writeAgent24KnowledgeError(c, agent24Internal(), echo)
		return nil, false
	}
	if reason != "" {
		writeAgent24KnowledgeError(c, agent24Unauthorized(reason), echo)
		return nil, false
	}
	if ctx == nil {
		ctx = c.Request.Context()
	}
	return ctx, true
}

// agent24UITag accepts a JSON string that is 43 characters of unpadded base64url decoding to 32 bytes.
func agent24UITag(raw json.RawMessage) (string, bool) {
	var s string
	if json.Unmarshal(raw, &s) != nil || len(s) != 43 {
		return "", false
	}
	buf, err := base64.RawURLEncoding.Strict().DecodeString(s)
	if err != nil || len(buf) != 32 {
		return "", false
	}
	return s, true
}

// parseAgent24UITicketRequest decodes an issuance body. Unknown fields, a bad tag, or a generation that
// is missing, not an integer, or negative is invalid_request. A valid generation is echoed; request_id
// stays null. parseAgent24KnowledgeCommon is not used: it requires protocol_version and request_id.
func parseAgent24UITicketRequest(
	body []byte,
) (binding, partition, vph string, generation int64, echo agent24KnowledgeEcho, kerr *agent24KnowledgeError) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var fields map[string]json.RawMessage
	if err := dec.Decode(&fields); err != nil || fields == nil {
		return "", "", "", 0, echo, agent24InvalidRequest()
	}
	if _, err := dec.Token(); err != io.EOF {
		return "", "", "", 0, echo, agent24InvalidRequest()
	}
	if raw, ok := fields["generation"]; ok {
		if g, ok := agent24JSONInt64(raw); ok && g >= 0 {
			echo.Generation = &g
		}
	}
	for key := range fields {
		switch key {
		case "binding_tag", "partition_tag", "vph", "generation":
		default:
			return "", "", "", 0, echo, agent24InvalidRequest()
		}
	}
	if echo.Generation == nil {
		return "", "", "", 0, echo, agent24InvalidRequest()
	}
	var tagOK bool
	if binding, tagOK = agent24UITag(fields["binding_tag"]); !tagOK {
		return "", "", "", 0, echo, agent24InvalidRequest()
	}
	if partition, tagOK = agent24UITag(fields["partition_tag"]); !tagOK {
		return "", "", "", 0, echo, agent24InvalidRequest()
	}
	if vph, tagOK = agent24UITag(fields["vph"]); !tagOK {
		return "", "", "", 0, echo, agent24InvalidRequest()
	}
	return binding, partition, vph, *echo.Generation, echo, nil
}
