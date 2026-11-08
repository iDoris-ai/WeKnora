// Agent24 fork product patch A24-KB-37 (KB-C2-01c4, WeKnora#133, UI-BRIDGE-v1 U4.4).

package router

import (
	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// installAgent24CORS installs the upstream CORS middleware except in integration mode.
// In integration mode no response carries Access-Control-* headers: the embed page is served
// from the API's own origin (WeKnora#134) and every other caller is the Agent24 main process,
// so no browser needs cross-origin access. Preflights stay 404 at the integration gate.
// An invalid mode never reaches here: installAgent24IntegrationMode already stopped startup on it.
func installAgent24CORS(r *gin.Engine, cors gin.HandlerFunc) {
	mode, err := handler.Agent24KnowledgeModeFromEnv()
	if err == nil && mode == handler.Agent24KnowledgeModeIntegration {
		return
	}
	r.Use(cors)
}
