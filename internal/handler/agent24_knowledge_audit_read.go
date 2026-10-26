// Agent24 fork product patch A24-KB-19 (KB-C1-03, WeKnora#41): read paths for the agent24_knowledge audit rows
// written by A24-KB-16.
//
// These routes stay off agent24IntegrationAllowlist: integration mode's credential gate rejects platform keys,
// and there is no login route.

package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// ListAgent24KnowledgeAuditTenant returns this tenant's agent24_knowledge audit rows.
// The route guard is not sufficient: when RBAC is not enforced, RequireRole only logs and continues.
func (h *AuditLogHandler) ListAgent24KnowledgeAuditTenant(c *gin.Context) {
	ctx := c.Request.Context()
	tenantID, ok := parseTenantIDFromPath(c)
	if !ok {
		return
	}
	// Undeclared for API keys at the route. Reject here as well, including when the gate is absent.
	if _, ok := types.TenantAPIKeyScopeFromContext(ctx); ok {
		c.Error(errors.NewForbiddenError("API keys cannot read agent24 knowledge audit"))
		return
	}
	if types.CallerFromContext(ctx).TenantID != tenantID {
		c.Error(errors.NewForbiddenError("agent24 knowledge audit is only available in the caller's workspace"))
		return
	}
	if !types.TenantRoleFromContext(ctx).HasPermission(types.TenantRoleAdmin) {
		c.Error(errors.NewForbiddenError("agent24 knowledge audit requires admin access"))
		return
	}
	h.listAgent24KnowledgeAudit(c, tenantID)
}

// ListAgent24KnowledgeAuditSystem returns tenant 0's agent24_knowledge rows, the anonymous rows of
// unauthenticated requests. A platform key must carry system_audit_read; any other caller must be a
// system administrator.
func (h *AuditLogHandler) ListAgent24KnowledgeAuditSystem(c *gin.Context) {
	ctx := c.Request.Context()
	if scope, ok := types.TenantAPIKeyScopeFromContext(ctx); ok {
		if !scope.IsPlatform() || !scope.HasCapability(types.APIKeyCapabilitySystemAuditRead) {
			c.Error(errors.NewForbiddenError("agent24 knowledge audit requires a platform key with system_audit_read"))
			return
		}
	} else if !types.IsSystemAdminFromContext(ctx) {
		c.Error(errors.NewForbiddenError("agent24 knowledge audit requires a system administrator"))
		return
	}
	h.listAgent24KnowledgeAudit(c, 0)
}

// listAgent24KnowledgeAudit parses the shared cursor, action and outcome filters and writes the list.
// Actor is not a filter: these rows always store an empty actor.
func (h *AuditLogHandler) listAgent24KnowledgeAudit(c *gin.Context, tenantID uint64) {
	ctx := c.Request.Context()
	afterID, limit := parseAuditCursor(c)
	action := types.AuditAction(c.Query("action"))
	switch action {
	case "", agent24AuditActionSearch, agent24AuditActionRead, agent24AuditActionReady:
	default:
		c.Error(errors.NewValidationError("action must be knowledge.search, knowledge.read, or knowledge.ready"))
		return
	}
	q := &interfaces.AuditLogQuery{
		AfterID:      afterID,
		Limit:        limit,
		Action:       action,
		Outcome:      types.AuditOutcome(c.Query("outcome")),
		ScopeType:    agent24AuditScopeType,
		UnscopedOnly: false,
	}
	entries, err := h.auditService.List(ctx, tenantID, q)
	if err != nil {
		logger.ErrorWithFields(ctx, err, map[string]interface{}{"tenant_id": tenantID})
		c.Error(errors.NewInternalServerError("failed to list audit log"))
		return
	}
	var nextCursor uint64
	if n := len(entries); n > 0 {
		nextCursor = entries[n-1].ID
	}
	c.JSON(http.StatusOK, auditLogListResponse{
		Success:    true,
		Data:       entries,
		NextCursor: nextCursor,
	})
}
