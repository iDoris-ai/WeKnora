package router

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
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

type agent24FakeKeySvc struct {
	interfaces.TenantAPIKeyService
	key *types.TenantAPIKey
}

func (f agent24FakeKeySvc) AuthenticateAPIKey(context.Context, string) (*types.TenantAPIKey, error) {
	return f.key, nil
}

type agent24FakeTenantSvc struct {
	interfaces.TenantService
	tenant *types.Tenant
}

func (f agent24FakeTenantSvc) GetTenantByID(_ context.Context, id uint64) (*types.Tenant, error) {
	if id != f.tenant.ID {
		return nil, errors.New("not found")
	}
	return f.tenant, nil
}

type agent24FakeUserSvc struct{ interfaces.UserService }

func (agent24FakeUserSvc) GetUserByTenantID(context.Context, uint64) (*types.User, error) {
	return nil, errors.New("not found")
}

// TestAgent24IntegrationCredentialGateNewRouter guards the A24-KB-05 wiring in NewRouter:
// platform keys and direct-principal tenant keys are rejected after middleware.Auth.
func TestAgent24IntegrationCredentialGateNewRouter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("LOCAL_STORAGE_BASE_DIR", t.TempDir())
	t.Setenv("AGENT24_KNOWLEDGE_MODE", "integration")
	tenantID := uint64(1)
	cases := map[string]struct {
		key  *types.TenantAPIKey
		mode types.APIPrincipalMode
	}{
		"platform key": {
			&types.TenantAPIKey{ID: 1, ScopeType: types.APIKeyScopePlatform}, types.APIPrincipalModeTenant,
		},
		"direct principal": {
			&types.TenantAPIKey{ID: 2, ScopeType: types.APIKeyScopeTenant, TenantID: &tenantID, FullAccess: true},
			types.APIPrincipalModeDirect,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tenant := &types.Tenant{ID: tenantID, APIPrincipalConfig: &types.APIPrincipalConfig{Mode: tc.mode}}
			r := NewRouter(RouterParams{
				Config: &config.Config{Tenant: &config.TenantConfig{}}, SystemHandler: &handler.SystemHandler{},
				TenantAPIKeyService: agent24FakeKeySvc{key: tc.key},
				TenantService:       agent24FakeTenantSvc{tenant: tenant},
				UserService:         agent24FakeUserSvc{},

				Agent24KnowledgeReadyHandler: &handler.Agent24KnowledgeReadyHandler{},
			})
			req := httptest.NewRequest(http.MethodPost, "/api/v1/agent24/knowledge/ready", strings.NewReader("{}"))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-API-Key", "sk-test")
			req.Header.Set("X-Tenant-ID", "1")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
			var body struct {
				Error struct {
					Details map[string]any `json:"details"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), w.Body.String())
			require.Equal(t, "credential_invalid", body.Error.Details["reason"])
		})
	}
}
