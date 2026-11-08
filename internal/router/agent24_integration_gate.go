// Agent24 fork product patch A24-KB-04 (KB-C1-01). In integration
// mode every route outside the allowlist answers 404, indistinguishable from a
// missing route.

package router

import (
	"context"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/agent24uisession"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// agent24IntegrationAllowlist keys are method + " " + c.FullPath().
var agent24IntegrationAllowlist = map[string]struct{}{
	"GET /health":                           {},
	"GET /api/v1/system/capabilities":       {},
	"POST /api/v1/agent24/knowledge/ready":  {},
	"POST /api/v1/agent24/knowledge/search": {},
	"POST /api/v1/agent24/knowledge/read":   {},
	// A24-KB-32
	"POST /api/v1/agent24/knowledge/ui/tickets":     {},
	"POST /api/v1/agent24/knowledge/ui/revocations": {},
	// A24-KB-33: exact method. The other method on each path stays off the list.
	"POST /api/v1/agent24/knowledge/ui/exchange": {},
	"GET /api/v1/agent24/knowledge/ui/session":   {},
}

func init() {
	// A24-KB-34: UI read routes come from the single table. Do not hand-copy them.
	for _, rt := range agent24uisession.UIReadRoutes {
		agent24IntegrationAllowlist[rt.Method+" "+rt.Path] = struct{}{}
	}
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
	// The policy version goes to the log only (decided 2026-11-06, D-B): the wire field waits for contract r11.
	// Validation above refused a version with control characters or over 128 runes.
	logger.Infof(context.Background(), "Agent24 knowledge integration mode: only %d allowlisted routes are served; "+
		"processing policy version %q", len(agent24IntegrationAllowlist), handler.Agent24ProcessingPolicyVersion())
	return nil
}

// installAgent24IntegrationAuthEnvelope must run right before middleware.Auth (A24-KB-11); the credential gate
// after Auth releases what it holds. An invalid mode never reaches here: installAgent24IntegrationMode, at the
// top of NewRouter, already stopped startup on it, as ensureAgent24CredentialRevocations does in the container.
func installAgent24IntegrationAuthEnvelope(r *gin.Engine) {
	mode, err := handler.Agent24KnowledgeModeFromEnv()
	if err == nil && mode == handler.Agent24KnowledgeModeIntegration {
		r.Use(handler.Agent24KnowledgeAuthEnvelope())
	}
}

// installAgent24IntegrationCredentialGate must run right after middleware.Auth. As for the auth envelope, an
// invalid mode already stopped startup in installAgent24IntegrationMode.
func installAgent24IntegrationCredentialGate(r *gin.Engine, revocations interfaces.Agent24CredentialRevocationStore) {
	mode, err := handler.Agent24KnowledgeModeFromEnv()
	if err == nil && mode == handler.Agent24KnowledgeModeIntegration {
		r.Use(handler.Agent24IntegrationCredentialGate(revocations))
	}
}
