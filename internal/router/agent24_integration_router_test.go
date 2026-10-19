package router

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/handler"
)

var agent24RouteParam = regexp.MustCompile(`[:*][^/]+`)

// TestAgent24IntegrationModeNewRouter guards the production wiring in NewRouter:
// every registered non-allowlisted route, and its CORS preflight, must answer 404.
func TestAgent24IntegrationModeNewRouter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("LOCAL_STORAGE_BASE_DIR", t.TempDir())
	t.Setenv("AGENT24_KNOWLEDGE_MODE", "integration")
	r := NewRouter(RouterParams{
		Config: &config.Config{Tenant: &config.TenantConfig{}}, SystemHandler: &handler.SystemHandler{},
	})
	checked := 0
	for _, rt := range r.Routes() {
		if _, ok := agent24IntegrationAllowlist[rt.Method+" "+rt.Path]; ok {
			continue
		}
		path := agent24RouteParam.ReplaceAllString(rt.Path, "x")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(rt.Method, path, nil))
		require.Equal(t, http.StatusNotFound, w.Code, "%s %s", rt.Method, rt.Path)
		require.Empty(t, w.Body.String(), "%s %s", rt.Method, rt.Path)

		req := httptest.NewRequest(http.MethodOptions, path, nil)
		req.Header.Set("Origin", "https://example.org")
		req.Header.Set("Access-Control-Request-Method", http.MethodPost)
		w = httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusNotFound, w.Code, "OPTIONS %s", rt.Path)
		require.Empty(t, w.Header().Get("Access-Control-Allow-Origin"), "OPTIONS %s", rt.Path)
		checked++
	}
	require.Greater(t, checked, 50)
}

func TestInstallAgent24IntegrationModeErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	off := false
	cfg := &config.Config{Tenant: &config.TenantConfig{EnableRBAC: &off}}

	t.Setenv("AGENT24_KNOWLEDGE_MODE", "integration")
	require.Error(t, installAgent24IntegrationMode(gin.New(), cfg))

	t.Setenv("AGENT24_KNOWLEDGE_MODE", "open")
	require.Error(t, installAgent24IntegrationMode(gin.New(), cfg))

	t.Setenv("AGENT24_KNOWLEDGE_MODE", "standalone")
	r := gin.New()
	require.NoError(t, installAgent24IntegrationMode(r, cfg))
	require.True(t, r.RedirectTrailingSlash)
	require.Empty(t, r.Handlers)
}
