// Package router registers the HTTP API routes.
package router

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterAgent24KnowledgeRoutes registers the Agent24 knowledge integration
// endpoints (fork patches A24-KB-02, A24-KB-10); a nil handler skips its route.
func RegisterAgent24KnowledgeRoutes(r *gin.RouterGroup, h *handler.Agent24KnowledgeReadyHandler,
	s *handler.Agent24KnowledgeSearchHandler, g *rbacGuards,
) {
	if h != nil {
		g.apiKeyRoute(r, http.MethodPost, "/agent24/knowledge/ready", apiKeyRetrieve(apiKeyAny()), g.Viewer(), h.Ready)
	}
	if s != nil {
		g.apiKeyRoute(r, http.MethodPost, "/agent24/knowledge/search", apiKeyRetrieve(apiKeyAny()), g.Viewer(),
			s.Search)
	}
}
