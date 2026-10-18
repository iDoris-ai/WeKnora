// Package router registers the HTTP API routes.
package router

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// RegisterAgent24KnowledgeRoutes registers the Agent24 knowledge integration
// endpoints (fork patch A24-KB-02).
func RegisterAgent24KnowledgeRoutes(r *gin.RouterGroup, h *handler.Agent24KnowledgeReadyHandler, g *rbacGuards) {
	if h == nil {
		return
	}
	g.apiKeyRoute(r, http.MethodPost, "/agent24/knowledge/ready", apiKeyRetrieve(apiKeyAny()), g.Viewer(), h.Ready)
}
