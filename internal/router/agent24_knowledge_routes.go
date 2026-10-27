// Package router registers the HTTP API routes.
package router

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/types"
)

// RegisterAgent24KnowledgeRoutes registers the Agent24 knowledge integration
// endpoints (fork patches A24-KB-02, A24-KB-10, A24-KB-15); a nil handler skips its route.
func RegisterAgent24KnowledgeRoutes(r *gin.RouterGroup, h *handler.Agent24KnowledgeReadyHandler,
	s *handler.Agent24KnowledgeSearchHandler, rd *handler.Agent24KnowledgeReadHandler, g *rbacGuards,
) {
	if h != nil {
		g.apiKeyRoute(r, http.MethodPost, "/agent24/knowledge/ready", apiKeyRetrieve(apiKeyAny()), g.Viewer(), h.Ready)
	}
	if s != nil {
		g.apiKeyRoute(r, http.MethodPost, "/agent24/knowledge/search", apiKeyRetrieve(apiKeyAny()), g.Viewer(),
			s.Search)
	}
	if rd != nil {
		g.apiKeyRoute(r, http.MethodPost, "/agent24/knowledge/read", apiKeyRetrieve(apiKeyAny()), g.Viewer(), rd.Read)
	}
}

// RegisterAgent24KnowledgeAuditRoutes registers the read paths for the agent24_knowledge audit rows (fork patch
// A24-KB-19); a nil handler skips them. The tenant route repeats the per-tenant group guard (PathTenantMatch) and
// is undeclared for API keys; the system route sits behind SystemAdmin and admits platform keys holding
// system_audit_read. Both handlers check the caller again.
func RegisterAgent24KnowledgeAuditRoutes(r *gin.RouterGroup, h *handler.AuditLogHandler, g *rbacGuards) {
	if h == nil {
		return
	}
	tenantByID := r.Group("/tenants/:id", g.PathTenantMatch())
	tenantByID.GET("/agent24/knowledge/audit", g.Admin(), h.ListAgent24KnowledgeAuditTenant)
	admin := r.Group("/system/admin", g.SystemAdmin())
	g.apiKeyRoute(admin, http.MethodGet, "/agent24/knowledge/audit",
		apiKeyPlatform(types.APIKeyCapabilitySystemAuditRead), h.ListAgent24KnowledgeAuditSystem)
}
