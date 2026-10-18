// Package router hosts Agent24 fork product patch A24-KB-04 (KB-C1-01). In integration
// mode every route outside the allowlist answers 404, indistinguishable from a
// missing route.
package router

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/logger"
)

// agent24IntegrationAllowlist keys are method + " " + c.FullPath().
var agent24IntegrationAllowlist = map[string]struct{}{
	"GET /health":                          {},
	"GET /api/v1/system/capabilities":      {},
	"POST /api/v1/agent24/knowledge/ready": {},
}

func agent24IntegrationGate() gin.HandlerFunc {
	return func(c *gin.Context) {
		if p := c.FullPath(); p != "" {
			if _, ok := agent24IntegrationAllowlist[c.Request.Method+" "+p]; ok {
				c.Next()
				return
			}
		}
		c.AbortWithStatus(http.StatusNotFound)
	}
}

// applyAgent24IntegrationGate configures r for integration mode: no automatic
// path redirects, and the allowlist gate as a global middleware.
func applyAgent24IntegrationGate(r *gin.Engine) {
	r.RedirectTrailingSlash = false
	r.RedirectFixedPath = false
	r.Use(agent24IntegrationGate())
}

// installAgent24IntegrationMode must run before any route is registered.
func installAgent24IntegrationMode(r *gin.Engine, cfg *config.Config) {
	ctx := context.Background()
	mode, err := handler.Agent24KnowledgeModeFromEnv()
	if err != nil {
		logger.Fatalf(ctx, "%v", err)
	}
	if mode != handler.Agent24KnowledgeModeIntegration {
		return
	}
	if err := handler.ValidateAgent24IntegrationConfig(cfg); err != nil {
		logger.Fatalf(ctx, "%v", err)
	}
	applyAgent24IntegrationGate(r)
	logger.Infof(ctx, "Agent24 knowledge integration mode: only %d allowlisted routes are served",
		len(agent24IntegrationAllowlist))
}
