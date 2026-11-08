// Agent24 fork product patch A24-KB-32 (KB-C2-01b1, WeKnora#133, CONTRACT-UI-BRIDGE-v1 U2.1, U2.13, U3.7).

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/Tencent/WeKnora/internal/agent24uisession"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// Agent24KnowledgeUIRevocationsHandler serves POST /api/v1/agent24/knowledge/ui/revocations.
type Agent24KnowledgeUIRevocationsHandler struct {
	recheck agent24CredentialRechecker
	store   interfaces.Agent24UISessionStore
}

// NewAgent24KnowledgeUIRevocationsHandler builds the revocation handler.
// An unset UI session key returns a nil handler so the route is not registered.
// A key that is set but invalid returns an error so the container fails closed.
func NewAgent24KnowledgeUIRevocationsHandler(
	tenants interfaces.TenantService, keys interfaces.TenantAPIKeyService,
	revocations interfaces.Agent24CredentialRevocationStore,
	users interfaces.UserService, members interfaces.TenantMemberService, cfg *config.Config,
	store interfaces.Agent24UISessionStore,
) (*Agent24KnowledgeUIRevocationsHandler, error) {
	on, err := agent24UISessionKeyGate()
	if err != nil || !on {
		return nil, err
	}
	return &Agent24KnowledgeUIRevocationsHandler{
		recheck: newAgent24Rechecker(tenants, keys, revocations, users, members, cfg),
		store:   store,
	}, nil
}

// Revoke revokes one ticket or one view family. E may be empty: a narrowed credential must still be able
// to revoke what it issued. Success, a missing target, an already-revoked target and someone else's target
// are all 204 with an empty body. The row is written only after the store returns nil.
func (h *Agent24KnowledgeUIRevocationsHandler) Revoke(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	body, ok := agent24ReadBody(c)
	if !ok {
		writeAgent24KnowledgeError(c, agent24InvalidRequest(), agent24KnowledgeEcho{})
		return
	}
	ticketID, vph, kerr := parseAgent24UIRevokeRequest(body)
	if kerr != nil {
		writeAgent24KnowledgeError(c, kerr, agent24KnowledgeEcho{})
		return
	}
	ctx, ok := agent24UIRecheck(c, h.recheck, agent24KnowledgeEcho{})
	if !ok {
		return
	}
	principal, tenantID, ok := agent24UIRevoker(ctx)
	if !ok {
		writeAgent24KnowledgeError(c, agent24Unauthorized("credential_invalid"), agent24KnowledgeEcho{})
		return
	}
	if h.store == nil {
		writeAgent24KnowledgeError(c, agent24Internal(), agent24KnowledgeEcho{})
		return
	}
	now := time.Now()
	var err error
	resource := map[string]any{}
	if ticketID != "" {
		err = h.store.RevokeTicket(ctx, tenantID, principal.StorageID(), ticketID, now)
		resource["target"] = "ticket"
		resource["ticket_id_hash"] = agent24uisession.H("kb-ticket-id-v1", []byte(ticketID))
	} else {
		err = h.store.RevokeFamily(ctx, tenantID, principal.StorageID(), vph, now)
		resource["target"] = "family"
		resource["vph_hash"] = agent24uisession.H("kb-vph-audit-v1", []byte(vph))
	}
	if err != nil {
		logger.Warnf(ctx, "agent24 knowledge: ui revoke store: error_len=%d error_type=%T", len(fmt.Sprint(err)), err)
		writeAgent24KnowledgeError(c, agent24Internal(), agent24KnowledgeEcho{})
		return
	}
	agent24NoteAudit(c, func(f *agent24AuditFacts) {
		f.Outcome = agent24AuditOK
		f.Resource = resource
	})
	c.Status(http.StatusNoContent)
}

// agent24UIRevoker reads the principal and tenant revocation requires. Credential id and E are not required.
func agent24UIRevoker(ctx context.Context) (types.Principal, uint64, bool) {
	principal, ok := types.PrincipalFromContext(ctx)
	if !ok || !principal.Valid() {
		return types.Principal{}, 0, false
	}
	tenantID := types.CallerFromContext(ctx).TenantID
	if tenantID == 0 {
		return types.Principal{}, 0, false
	}
	return principal, tenantID, true
}

// parseAgent24UIRevokeRequest accepts exactly one of ticket_id (a UUID) or vph (a 43-character tag).
// ticket_id must be the canonical lowercase hyphenated UUID that issuance returns.
func parseAgent24UIRevokeRequest(body []byte) (ticketID, vph string, kerr *agent24KnowledgeError) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var fields map[string]json.RawMessage
	if err := dec.Decode(&fields); err != nil || fields == nil || len(fields) != 1 {
		return "", "", agent24InvalidRequest()
	}
	if _, err := dec.Token(); err != io.EOF {
		return "", "", agent24InvalidRequest()
	}
	if raw, ok := fields["ticket_id"]; ok {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", "", agent24InvalidRequest()
		}
		// Only the canonical form issuance returns (lowercase, hyphenated). uuid.Parse also takes urn:uuid:,
		// braces, no hyphens and upper case; those never match a stored ticket_id.
		if id, err := uuid.Parse(s); err != nil || id.String() != s {
			return "", "", agent24InvalidRequest()
		}
		return s, "", nil
	}
	raw, ok := fields["vph"]
	if !ok {
		return "", "", agent24InvalidRequest()
	}
	vph, ok = agent24UITag(raw)
	if !ok {
		return "", "", agent24InvalidRequest()
	}
	return "", vph, nil
}
