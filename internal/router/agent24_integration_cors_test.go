// Agent24 fork product patch A24-KB-37 (KB-C2-01c4, WeKnora#133, UI-BRIDGE-v1 U4.4).

package router

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"slices"
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
	agent24EmbedAdminFixture(t) // A24-KB-39: so the page routes are registered too
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
		Agent24KnowledgeCandidatesHandler:    &handler.Agent24KnowledgeCandidatesHandler{}, // A24-KB-43
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
		path := agent24ConcretePath(rt.Path)
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
	// A24-KB-50: the write routes are registered and allowlisted, and send no CORS header without origins.
	for _, rt := range agent24uisession.UIWriteRoutes {
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

// A24-KB-38 (KB-C2-01c5): AGENT24_KNOWLEDGE_UI_ORIGINS.

const (
	agent24UIOriginA = "https://ui.example.org"
	agent24UIOriginB = "http://127.0.0.1:5173"
)

func agent24UICORSRouter(t *testing.T, origins string) *gin.Engine {
	t.Helper()
	t.Setenv(handler.Agent24KnowledgeUIOriginsEnv, origins)
	return agent24CORSRouter(t, "integration")
}

func agent24Preflight(path, origin, method, headers string) *http.Request {
	req := httptest.NewRequest(http.MethodOptions, path, nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if method != "" {
		req.Header.Set("Access-Control-Request-Method", method)
	}
	if headers != "" {
		req.Header.Set("Access-Control-Request-Headers", headers)
	}
	return req
}

func agent24RequireBare404(t *testing.T, w *httptest.ResponseRecorder, what string) {
	t.Helper()
	require.Equal(t, http.StatusNotFound, w.Code, what)
	require.Zero(t, w.Body.Len(), what)
	agent24RequireNoCORSHeaders(t, w.Header(), what)
	require.Empty(t, w.Header().Values("Vary"), what)
}

// TestAgent24UIOriginsCORS: with origins configured, the eight UI read routes and (A24-KB-50) the five UI write
// routes answer cross-origin requests and preflights from those exact origins only, without credentials. A UI
// path's preflight allows exactly the one method that path is registered for (GET, or POST for return, approve
// and withdraw). Every other allowlisted route still sends no CORS header and its preflight stays 404.
func TestAgent24UIOriginsCORS(t *testing.T) {
	r := agent24UICORSRouter(t, " "+agent24UIOriginA+" ,"+agent24UIOriginB+","+agent24UIOriginA)
	ui := map[string]string{} // path -> its one method
	for _, rt := range append(slices.Clone(agent24uisession.UIReadRoutes), agent24uisession.UIWriteRoutes...) {
		require.NotContains(t, ui, rt.Path, "one method per UI path")
		ui[rt.Path] = rt.Method
	}
	options := 0
	for _, rt := range r.Routes() {
		if rt.Method == http.MethodOptions {
			require.Contains(t, ui, rt.Path, "OPTIONS route outside the UI paths: %s", rt.Path)
			options++
		}
	}
	require.Equal(t, len(ui), options)

	checked := map[string]bool{}
	for _, rt := range r.Routes() {
		key := rt.Method + " " + rt.Path
		if _, ok := agent24IntegrationAllowlist[key]; !ok {
			continue
		}
		path := agent24ConcretePath(rt.Path)
		checked[key] = true
		method, isUI := ui[rt.Path]
		if !isUI {
			req := httptest.NewRequest(rt.Method, path, nil)
			req.Header.Set("Origin", agent24UIOriginA)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			require.NotEqual(t, http.StatusNotFound, w.Code, key)
			agent24RequireNoCORSHeaders(t, w.Header(), key)
			w = httptest.NewRecorder()
			r.ServeHTTP(w, agent24Preflight(path, agent24UIOriginA, rt.Method, "authorization"))
			agent24RequireBare404(t, w, "OPTIONS "+key)
			continue
		}

		for _, origin := range []string{agent24UIOriginA, agent24UIOriginB} {
			req := httptest.NewRequest(method, path, nil)
			req.Header.Set("Origin", origin)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			require.NotEqual(t, http.StatusNotFound, w.Code, key)
			require.Equal(t, origin, w.Header().Get("Access-Control-Allow-Origin"), key)
			require.Empty(t, w.Header().Values("Access-Control-Allow-Credentials"), key)
			require.Contains(t, w.Header().Values("Vary"), "Origin", key)

			w = httptest.NewRecorder()
			r.ServeHTTP(w, agent24Preflight(path, origin, method,
				"Authorization, X-Agent24-View-Proof,x-request-id , content-type"))
			require.Equal(t, http.StatusNoContent, w.Code, "OPTIONS "+key)
			require.Zero(t, w.Body.Len(), "OPTIONS "+key)
			require.Equal(t, origin, w.Header().Get("Access-Control-Allow-Origin"), "OPTIONS "+key)
			require.Equal(t, method, w.Header().Get("Access-Control-Allow-Methods"), "OPTIONS "+key)
			require.Equal(t, "Authorization, Content-Type, X-Agent24-View-Proof, X-Request-ID",
				w.Header().Get("Access-Control-Allow-Headers"), "OPTIONS "+key)
			require.Equal(t, "600", w.Header().Get("Access-Control-Max-Age"), "OPTIONS "+key)
			require.Empty(t, w.Header().Values("Access-Control-Allow-Credentials"), "OPTIONS "+key)
			require.Equal(t, []string{"Origin", "Access-Control-Request-Method", "Access-Control-Request-Headers"},
				w.Header().Values("Vary"), "OPTIONS "+key)
		}

		// A preflight that asks for no header at all is still a preflight.
		w := httptest.NewRecorder()
		r.ServeHTTP(w, agent24Preflight(path, agent24UIOriginA, method, ""))
		require.Equal(t, http.StatusNoContent, w.Code, "OPTIONS "+key+" without request headers")

		others := []string{
			"", "https://evil.example", "null", agent24UIOriginA + "/",
			"https://UI.example.org", "http://ui.example.org", "https://ui.example.org:443",
		}
		for _, origin := range others {
			req := httptest.NewRequest(method, path, nil)
			if origin != "" {
				req.Header.Set("Origin", origin)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			require.NotEqual(t, http.StatusNotFound, w.Code, key)
			agent24RequireNoCORSHeaders(t, w.Header(), key+" from "+origin)
			require.Contains(t, w.Header().Values("Vary"), "Origin", key)

			w = httptest.NewRecorder()
			r.ServeHTTP(w, agent24Preflight(path, origin, method, "authorization"))
			agent24RequireBare404(t, w, "OPTIONS "+key+" from "+origin)
		}

		bad := map[string]*http.Request{
			"the other method":   agent24Preflight(path, agent24UIOriginA, agent24OtherMethod(method), "authorization"),
			"method PUT":         agent24Preflight(path, agent24UIOriginA, http.MethodPut, "authorization"),
			"method DELETE":      agent24Preflight(path, agent24UIOriginA, http.MethodDelete, "authorization"),
			"lower-case method":  agent24Preflight(path, agent24UIOriginA, strings.ToLower(method), "authorization"),
			"no request method":  agent24Preflight(path, agent24UIOriginA, "", "authorization"),
			"header x-api-key":   agent24Preflight(path, agent24UIOriginA, method, "authorization, x-api-key"),
			"header x-tenant-id": agent24Preflight(path, agent24UIOriginA, method, "x-tenant-id"),
			"header cookie":      agent24Preflight(path, agent24UIOriginA, method, "cookie"),
		}
		two := agent24Preflight(path, agent24UIOriginA, method, "authorization")
		two.Header.Add("Origin", agent24UIOriginB)
		bad["two origins"] = two
		twoHdr := agent24Preflight(path, agent24UIOriginA, method, "authorization")
		twoHdr.Header.Add("Access-Control-Request-Headers", "x-api-key")
		bad["second header line"] = twoHdr
		twoMethod := agent24Preflight(path, agent24UIOriginA, method, "authorization")
		twoMethod.Header.Add("Access-Control-Request-Method", method)
		bad["two request methods"] = twoMethod
		for what, req := range bad {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			agent24RequireBare404(t, w, "OPTIONS "+key+" "+what)
		}

		twoGet := httptest.NewRequest(method, path, nil)
		twoGet.Header.Add("Origin", agent24UIOriginA)
		twoGet.Header.Add("Origin", agent24UIOriginB)
		w = httptest.NewRecorder()
		r.ServeHTTP(w, twoGet)
		agent24RequireNoCORSHeaders(t, w.Header(), key+" with two origins")
	}
	for _, rt := range append(slices.Clone(agent24uisession.UIReadRoutes), agent24uisession.UIWriteRoutes...) {
		require.True(t, checked[rt.Method+" "+rt.Path], rt.Path)
	}
	require.Len(t, checked, len(agent24IntegrationAllowlist))

	// Paths outside the allowlist stay 404 for every method, OPTIONS included.
	for _, path := range []string{"/api/v1/knowledge-bases", "/api/v1/auth/register", "/nope"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, agent24Preflight(path, agent24UIOriginA, http.MethodGet, ""))
		agent24RequireBare404(t, w, "OPTIONS "+path)
	}
}

// TestAgent24UIOriginsBlankIsUnset: a blank value is the same as unset (A24-KB-37): no OPTIONS route, no CORS.
func TestAgent24UIOriginsBlankIsUnset(t *testing.T) {
	r := agent24UICORSRouter(t, "  ")
	for _, rt := range r.Routes() {
		require.NotEqual(t, http.MethodOptions, rt.Method, rt.Path)
	}
	path := agent24RouteParam.ReplaceAllString(agent24uisession.UIReadRoutes[0].Path, "x")
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Origin", agent24UIOriginA)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	agent24RequireNoCORSHeaders(t, w.Header(), path)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, agent24Preflight(path, agent24UIOriginA, http.MethodGet, "authorization"))
	agent24RequireBare404(t, w, "OPTIONS "+path)
}

// TestAgent24UIOriginsWithoutUIRoutes: origins configured but the UI session key unset, so the UI read
// routes are not registered. No OPTIONS route appears and the preflight is the gate's 404.
func TestAgent24UIOriginsWithoutUIRoutes(t *testing.T) {
	agent24SetTagKey(t, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	gin.SetMode(gin.TestMode)
	t.Setenv("LOCAL_STORAGE_BASE_DIR", t.TempDir())
	t.Setenv("AGENT24_KNOWLEDGE_MODE", "integration")
	t.Setenv(handler.Agent24KnowledgeUIOriginsEnv, agent24UIOriginA)
	r := NewRouter(RouterParams{
		Config: &config.Config{Tenant: &config.TenantConfig{}}, SystemHandler: &handler.SystemHandler{},
		AuditLogHandler: handler.NewAuditLogHandler(nil),
	})
	for _, rt := range r.Routes() {
		require.NotEqual(t, http.MethodOptions, rt.Method, rt.Path)
	}
	path := agent24RouteParam.ReplaceAllString(agent24uisession.UIReadRoutes[0].Path, "x")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, agent24Preflight(path, agent24UIOriginA, http.MethodGet, "authorization"))
	agent24RequireBare404(t, w, "OPTIONS "+path)
}

// TestAgent24UIOriginsIgnoredInStandalone: standalone keeps the upstream CORS and registers no OPTIONS route.
func TestAgent24UIOriginsIgnoredInStandalone(t *testing.T) {
	t.Setenv(handler.Agent24KnowledgeUIOriginsEnv, agent24UIOriginA)
	r := agent24CORSRouter(t, "standalone")
	for _, rt := range r.Routes() {
		require.NotEqual(t, http.MethodOptions, rt.Method, rt.Path)
	}
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, "*", w.Header().Get("Access-Control-Allow-Origin"))

	// An invalid value does not stop standalone either: the variable is not read there.
	t.Setenv(handler.Agent24KnowledgeUIOriginsEnv, "*")
	r = agent24CORSRouter(t, "standalone")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/health", nil))
	require.Equal(t, http.StatusOK, w.Code)
}

func TestInstallAgent24IntegrationModeRejectsInvalidUIOrigins(t *testing.T) {
	gin.SetMode(gin.TestMode)
	agent24SetTagKey(t, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("AGENT24_KNOWLEDGE_MODE", "integration")
	for _, v := range []string{"*", "https://ui.example.org/", "http://ui.example.org:8080", "https://a,,https://b"} {
		t.Setenv(handler.Agent24KnowledgeUIOriginsEnv, v)
		err := installAgent24IntegrationMode(gin.New(), &config.Config{Tenant: &config.TenantConfig{}})
		require.ErrorContains(t, err, handler.Agent24KnowledgeUIOriginsEnv, v)
	}
	t.Setenv(handler.Agent24KnowledgeUIOriginsEnv, agent24UIOriginA+","+agent24UIOriginB)
	require.NoError(t, installAgent24IntegrationMode(gin.New(), &config.Config{Tenant: &config.TenantConfig{}}))
}

func agent24OtherMethod(m string) string {
	if m == http.MethodGet {
		return http.MethodPost
	}
	return http.MethodGet
}
