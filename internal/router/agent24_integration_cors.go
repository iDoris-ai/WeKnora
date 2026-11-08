// Agent24 fork product patches A24-KB-37 (KB-C2-01c4) and A24-KB-38 (KB-C2-01c5), WeKnora#133,
// UI-BRIDGE-v1 U4.4.

package router

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
)

// agent24UIAllowHeaders are the only request headers a UI preflight may ask for (design §6).
var agent24UIAllowHeaders = map[string]struct{}{
	"authorization": {}, "content-type": {}, "x-agent24-view-proof": {}, "x-request-id": {},
}

const (
	agent24UIAllowHeadersValue = "Authorization, Content-Type, X-Agent24-View-Proof, X-Request-ID"
	agent24UIPreflightMaxAge   = "600"
)

// agent24UIOrigins returns the cross-origin allowlist for the UI read routes: nil outside integration mode
// or when AGENT24_KNOWLEDGE_UI_ORIGINS is unset. An invalid value never reaches here:
// installAgent24IntegrationMode already stopped startup on it.
func agent24UIOrigins() []string {
	mode, err := handler.Agent24KnowledgeModeFromEnv()
	if err != nil || mode != handler.Agent24KnowledgeModeIntegration {
		return nil
	}
	origins, err := handler.Agent24KnowledgeUIOriginsFromEnv()
	if err != nil {
		return nil
	}
	return origins
}

// installAgent24CORS installs the upstream CORS middleware except in integration mode.
// Integration mode with no UI origins configured installs nothing (A24-KB-37): no response carries an
// Access-Control-* header and every preflight stays 404 at the integration gate. The embed page is served
// from the API's own origin (WeKnora#134) and every other caller is the Agent24 main process.
// With UI origins configured (A24-KB-38) only the UI read routes answer cross-origin; see agent24UICORS.
// An invalid mode never reaches here: installAgent24IntegrationMode already stopped startup on it.
func installAgent24CORS(r *gin.Engine, cors gin.HandlerFunc) {
	mode, err := handler.Agent24KnowledgeModeFromEnv()
	if err == nil && mode == handler.Agent24KnowledgeModeIntegration {
		if origins := agent24UIOrigins(); len(origins) > 0 {
			r.Use(agent24UICORS(origins))
		}
		return
	}
	r.Use(cors)
}

// agent24UICORS serves CORS for the UI read routes only. Every other route passes through untouched.
//   - GET from an allowed origin gets Access-Control-Allow-Origin echoing it; from any other origin, none.
//     Both get Vary: Origin. No response allows credentials: the host injects the JWT, no cookie is used.
//   - A preflight from an allowed origin asking for GET and only the allowed headers gets 204 here, before
//     Auth. Any other OPTIONS on a UI path gets the gate's bare 404 with no CORS header.
func agent24UICORS(origins []string) gin.HandlerFunc {
	allowed := make(map[string]struct{}, len(origins))
	for _, o := range origins {
		allowed[o] = struct{}{}
	}
	return func(c *gin.Context) {
		if _, ui := agent24UIReadPaths[c.FullPath()]; !ui {
			c.Next()
			return
		}
		origin, ok := "", false
		if vs := c.Request.Header.Values("Origin"); len(vs) == 1 {
			origin = vs[0]
			_, ok = allowed[origin]
		}
		h := c.Writer.Header()
		if c.Request.Method == http.MethodOptions {
			if !ok || !agent24UIPreflightRequestOK(c.Request.Header) {
				c.AbortWithStatus(http.StatusNotFound)
				return
			}
			h.Set("Access-Control-Allow-Origin", origin)
			h.Set("Access-Control-Allow-Methods", http.MethodGet)
			h.Set("Access-Control-Allow-Headers", agent24UIAllowHeadersValue)
			h.Set("Access-Control-Max-Age", agent24UIPreflightMaxAge)
			h.Add("Vary", "Origin")
			h.Add("Vary", "Access-Control-Request-Method")
			h.Add("Vary", "Access-Control-Request-Headers")
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		h.Add("Vary", "Origin")
		if ok {
			h.Set("Access-Control-Allow-Origin", origin)
		}
		c.Next()
	}
}

// agent24UIPreflightRequestOK requires a CORS preflight for GET whose requested headers are all allowed.
// An OPTIONS without Access-Control-Request-Method is not a preflight.
func agent24UIPreflightRequestOK(h http.Header) bool {
	if ms := h.Values("Access-Control-Request-Method"); len(ms) != 1 || ms[0] != http.MethodGet {
		return false
	}
	for _, line := range h.Values("Access-Control-Request-Headers") {
		for _, name := range strings.Split(line, ",") {
			name = strings.ToLower(strings.TrimSpace(name))
			if name == "" {
				continue
			}
			if _, ok := agent24UIAllowHeaders[name]; !ok {
				return false
			}
		}
	}
	return true
}

// agent24UIPreflightUnreachable is the OPTIONS route handler on the UI read paths. agent24UICORS answers
// every OPTIONS there first, so reaching this means the middleware is missing: refuse like the gate.
func agent24UIPreflightUnreachable(c *gin.Context) {
	c.AbortWithStatus(http.StatusNotFound)
}
