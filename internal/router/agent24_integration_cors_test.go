// Agent24 fork product patch A24-KB-37 (KB-C2-01c4, WeKnora#133, UI-BRIDGE-v1 U4.4).

package router

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/agent24uisession"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/handler"
)

func agent24CORSRouter(t *testing.T, mode string) *gin.Engine {
	t.Helper()
	agent24SetTagKey(t, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	gin.SetMode(gin.TestMode)
	t.Setenv("LOCAL_STORAGE_BASE_DIR", t.TempDir())
	t.Setenv("AGENT24_KNOWLEDGE_MODE", mode)
	// Every Agent24 handler is set so all allowlisted routes are registered. They are zero values:
	// a request may fail inside the handler, which does not change the headers checked here.
	return NewRouter(RouterParams{
		Config: &config.Config{Tenant: &config.TenantConfig{}}, SystemHandler: &handler.SystemHandler{},
		AuditLogHandler:                      handler.NewAuditLogHandler(nil),
		Agent24KnowledgeReadyHandler:         &handler.Agent24KnowledgeReadyHandler{},
		Agent24KnowledgeSearchHandler:        &handler.Agent24KnowledgeSearchHandler{},
		Agent24KnowledgeReadHandler:          &handler.Agent24KnowledgeReadHandler{},
		Agent24KnowledgeUITicketsHandler:     &handler.Agent24KnowledgeUITicketsHandler{},
		Agent24KnowledgeUIRevocationsHandler: &handler.Agent24KnowledgeUIRevocationsHandler{},
		Agent24KnowledgeUIExchangeHandler:    &handler.Agent24KnowledgeUIExchangeHandler{},
		Agent24KnowledgeUISessionHandler:     &handler.Agent24KnowledgeUISessionHandler{},
		Agent24KnowledgeUIReadHandler:        &handler.Agent24KnowledgeUIReadHandler{},
	})
}

func agent24RequireNoCORSHeaders(t *testing.T, h http.Header, what string) {
	t.Helper()
	for k := range h {
		require.False(t, strings.HasPrefix(strings.ToLower(k), "access-control-"), "%s: header %s", what, k)
	}
}

// TestAgent24IntegrationModeSendsNoCORSHeaders runs the production NewRouter in integration mode.
// Every allowlisted route, the eight UI read routes included, answers a cross-origin request without
// any Access-Control-* header, and its preflight is 404 without one either.
func TestAgent24IntegrationModeSendsNoCORSHeaders(t *testing.T) {
	r := agent24CORSRouter(t, "integration")
	checked := map[string]bool{}
	for _, rt := range r.Routes() {
		key := rt.Method + " " + rt.Path
		if _, ok := agent24IntegrationAllowlist[key]; !ok {
			continue
		}
		path := agent24RouteParam.ReplaceAllString(rt.Path, "x")
		req := httptest.NewRequest(rt.Method, path, nil)
		req.Header.Set("Origin", "https://example.org")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.NotEqual(t, http.StatusNotFound, w.Code, key)
		agent24RequireNoCORSHeaders(t, w.Header(), key)

		pre := httptest.NewRequest(http.MethodOptions, path, nil)
		pre.Header.Set("Origin", "https://example.org")
		pre.Header.Set("Access-Control-Request-Method", rt.Method)
		pre.Header.Set("Access-Control-Request-Headers", "authorization, x-agent24-view-proof")
		w = httptest.NewRecorder()
		r.ServeHTTP(w, pre)
		require.Equal(t, http.StatusNotFound, w.Code, "OPTIONS "+key)
		agent24RequireNoCORSHeaders(t, w.Header(), "OPTIONS "+key)
		checked[key] = true
	}
	for _, rt := range agent24uisession.UIReadRoutes {
		require.True(t, checked[rt.Method+" "+rt.Path], rt.Path)
	}
	require.True(t, checked["GET /health"])
	require.Len(t, checked, len(agent24IntegrationAllowlist))
}

// TestAgent24StandaloneModeKeepsCORS: standalone keeps the upstream CORS middleware unchanged.
func TestAgent24StandaloneModeKeepsCORS(t *testing.T) {
	r := agent24CORSRouter(t, "standalone")
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", "https://example.org")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "*", w.Header().Get("Access-Control-Allow-Origin"))

	pre := httptest.NewRequest(http.MethodOptions, "/health", nil)
	pre.Header.Set("Origin", "https://example.org")
	pre.Header.Set("Access-Control-Request-Method", http.MethodGet)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, pre)
	require.Equal(t, http.StatusNoContent, w.Code)
	require.Equal(t, "*", w.Header().Get("Access-Control-Allow-Origin"))
}
