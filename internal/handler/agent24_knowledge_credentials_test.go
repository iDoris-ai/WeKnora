package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
)

func TestAgent24IntegrationCredentialGate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tenantKey := &types.TenantAPIKeyScope{KeyID: 2, ScopeType: types.APIKeyScopeTenant}
	platformKey := &types.TenantAPIKeyScope{KeyID: 1, ScopeType: types.APIKeyScopePlatform}
	withMode := func(m types.APIPrincipalMode) *types.Tenant {
		return &types.Tenant{ID: 1, APIPrincipalConfig: &types.APIPrincipalConfig{Mode: m}}
	}
	cases := []struct {
		name       string
		scope      *types.TenantAPIKeyScope
		tenant     *types.Tenant
		externalID string
		jti        string
		allowed    bool
	}{
		{"platform key", platformKey, &types.Tenant{ID: 1}, "", "", false},
		{"direct without header", tenantKey, withMode(types.APIPrincipalModeDirect), "", "", false},
		{"direct with header", tenantKey, withMode(types.APIPrincipalModeDirect), "u1", "", false},
		{"signed token", tenantKey, withMode(types.APIPrincipalModeSignedToken), "", "j-1", true},
		// R1.6: integration mode requires a jti on a signed_token credential.
		{"signed token without jti", tenantKey, withMode(types.APIPrincipalModeSignedToken), "", "", false},
		{"default mode", tenantKey, &types.Tenant{ID: 1}, "", "", true},
		{"tenant key without tenant", tenantKey, nil, "", "", false},
		{"jwt user", nil, &types.Tenant{ID: 1}, "", "", true},
	}
	for _, tc := range cases {
		for _, path := range []string{"/api/v1/agent24/knowledge/ready", "/api/v1/system/capabilities"} {
			t.Run(tc.name+" "+path, func(t *testing.T) {
				w := httptest.NewRecorder()
				_, r := gin.CreateTestContext(w)
				r.Use(func(c *gin.Context) {
					ctx := c.Request.Context()
					if tc.scope != nil {
						ctx = types.WithTenantAPIKeyScope(ctx, *tc.scope)
					}
					if tc.tenant != nil {
						ctx = context.WithValue(ctx, types.TenantInfoContextKey, tc.tenant)
					}
					if tc.jti != "" {
						ctx = types.WithAgent24TokenJTI(ctx, tc.jti)
					}
					c.Request = c.Request.WithContext(ctx)
				}, Agent24IntegrationCredentialGate())
				called := false
				r.POST(path, func(*gin.Context) { called = true })
				req := httptest.NewRequest(http.MethodPost, path, nil)
				if tc.externalID != "" {
					req.Header.Set("X-External-User-ID", tc.externalID)
				}
				r.ServeHTTP(w, req)
				require.Equal(t, tc.allowed, called)
				if tc.allowed {
					return
				}
				require.Equal(t, http.StatusUnauthorized, w.Code)
				var body map[string]any
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
				if path == "/api/v1/system/capabilities" {
					require.IsType(t, "", body["error"])
					return
				}
				e := body["error"].(map[string]any)
				d := e["details"].(map[string]any)
				require.Equal(t, "unauthorized", e["code"])
				require.Equal(t, "credential_invalid", d["reason"])
				require.Equal(t, false, d["retryable"])
				require.Contains(t, d, "request_id")
				require.Nil(t, d["request_id"])
				require.Contains(t, d, "generation")
				require.Nil(t, d["generation"])
			})
		}
	}
}
