// Agent24 fork product patch A24-KB-04 (KB-C1-01). In integration
// mode every route outside the allowlist answers 404, indistinguishable from a
// missing route.

package router

import (
	"context"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/logger"
)

// agent24IntegrationAllowlist keys are method + " " + c.FullPath().
var agent24IntegrationAllowlist = map[string]struct{}{
	"GET /health":                           {},
	"GET /api/v1/system/capabilities":       {},
	"POST /api/v1/agent24/knowledge/ready":  {},
	"POST /api/v1/agent24/knowledge/search": {},
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

// installAgent24IntegrationMode must run before any route is registered. It
// returns an error for an invalid mode or an unsafe integration config.
func installAgent24IntegrationMode(r *gin.Engine, cfg *config.Config) error {
	mode, err := handler.Agent24KnowledgeModeFromEnv()
	if err != nil {
		return fmt.Errorf("agent24 knowledge mode: %w", err)
	}
	if mode != handler.Agent24KnowledgeModeIntegration {
		return nil
	}
	if err := handler.ValidateAgent24IntegrationConfig(cfg); err != nil {
		return fmt.Errorf("agent24 integration config: %w", err)
	}
	applyAgent24IntegrationGate(r)
	logger.Infof(context.Background(), "Agent24 knowledge integration mode: only %d allowlisted routes are served",
		len(agent24IntegrationAllowlist))
	return nil
}

// installAgent24IntegrationAuthEnvelope must run right before middleware.Auth (A24-KB-11); the credential gate
// after Auth releases what it holds.
func installAgent24IntegrationAuthEnvelope(r *gin.Engine) {
	mode, err := handler.Agent24KnowledgeModeFromEnv()
	if err == nil && mode == handler.Agent24KnowledgeModeIntegration {
		r.Use(handler.Agent24KnowledgeAuthEnvelope())
	}
}

// installAgent24IntegrationCredentialGate must run right after middleware.Auth.
func installAgent24IntegrationCredentialGate(r *gin.Engine) {
	mode, err := handler.Agent24KnowledgeModeFromEnv()
	if err == nil && mode == handler.Agent24KnowledgeModeIntegration {
		r.Use(handler.Agent24IntegrationCredentialGate())
	}
}
