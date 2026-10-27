package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
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
				var revocations interfaces.Agent24CredentialRevocationStore
				if tc.jti != "" && tc.allowed {
					revocations = &agent24RevocationStub{}
				}
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
				}, Agent24IntegrationCredentialGate(revocations))
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

type agent24RevocationCall struct {
	tenantID uint64
	jti      string
}

// agent24RevocationStub records the tenant and jti the gate asks about.
type agent24RevocationStub struct {
	revoked bool
	err     error
	calls   []agent24RevocationCall
}

func (s *agent24RevocationStub) Revoke(context.Context, uint64, string, time.Time) error { return nil }

func (s *agent24RevocationStub) IsRevoked(
	_ context.Context, tenantID uint64, jti string, _ time.Time,
) (bool, error) {
	s.calls = append(s.calls, agent24RevocationCall{tenantID: tenantID, jti: jti})
	return s.revoked, s.err
}

func (s *agent24RevocationStub) PurgeExpired(context.Context, time.Time) (int64, error) {
	return 0, nil
}

func TestAgent24IntegrationCredentialGateRevocation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const (
		tenantID = uint64(42)
		jti      = "jti-revoked-1"
		ready    = "/api/v1/agent24/knowledge/ready"
		caps     = "/api/v1/system/capabilities"
	)
	signed := &types.Tenant{ID: tenantID, APIPrincipalConfig: &types.APIPrincipalConfig{
		Mode: types.APIPrincipalModeSignedToken,
	}}
	scope := &types.TenantAPIKeyScope{KeyID: 7, ScopeType: types.APIKeyScopeTenant}

	serve := func(
		store interfaces.Agent24CredentialRevocationStore, path, tokenJTI string, tenant *types.Tenant, withScope bool,
	) (int, bool, map[string]any) {
		t.Helper()
		w := httptest.NewRecorder()
		_, r := gin.CreateTestContext(w)
		called := false
		r.Use(func(c *gin.Context) {
			ctx := types.WithCaller(c.Request.Context(), types.Caller{TenantID: tenantID})
			if withScope {
				ctx = types.WithTenantAPIKeyScope(ctx, *scope)
			}
			if tenant != nil {
				ctx = context.WithValue(ctx, types.TenantInfoContextKey, tenant)
			}
			if tokenJTI != "" {
				ctx = types.WithAgent24TokenJTI(ctx, tokenJTI)
			}
			c.Request = c.Request.WithContext(ctx)
		}, Agent24IntegrationCredentialGate(store))
		r.POST(path, func(c *gin.Context) {
			called = true
			c.JSON(http.StatusOK, gin.H{"ok": true})
		})
		req := httptest.NewRequest(http.MethodPost, path, nil)
		r.ServeHTTP(w, req)
		var body map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		return w.Code, called, body
	}

	t.Run("revoked knowledge", func(t *testing.T) {
		stub := &agent24RevocationStub{revoked: true}
		code, called, body := serve(stub, ready, jti, signed, true)
		require.False(t, called)
		require.Equal(t, http.StatusUnauthorized, code)
		e := body["error"].(map[string]any)
		d := e["details"].(map[string]any)
		require.Equal(t, "unauthorized", e["code"])
		require.Equal(t, "credential_revoked", d["reason"])
		require.Equal(t, []agent24RevocationCall{{tenantID: tenantID, jti: jti}}, stub.calls)
	})
	t.Run("revoked capabilities", func(t *testing.T) {
		stub := &agent24RevocationStub{revoked: true}
		code, called, body := serve(stub, caps, jti, signed, true)
		require.False(t, called)
		require.Equal(t, http.StatusUnauthorized, code)
		require.Equal(t, "Unauthorized: credential not accepted in integration mode", body["error"])
		require.Equal(t, []agent24RevocationCall{{tenantID: tenantID, jti: jti}}, stub.calls)
	})
	t.Run("list error", func(t *testing.T) {
		logs := agent24CaptureCredentialLogs(t)
		stub := &agent24RevocationStub{err: errors.New("db down")}
		code, called, body := serve(stub, ready, jti, signed, true)
		require.Contains(t, logs.String(), "error_len=7 error_type=*errors.errorString")
		require.NotContains(t, logs.String(), "db down")
		require.NotContains(t, logs.String(), jti)
		require.False(t, called)
		require.Equal(t, http.StatusInternalServerError, code)
		e := body["error"].(map[string]any)
		require.Equal(t, "internal", e["code"])
		require.Equal(t, []agent24RevocationCall{{tenantID: tenantID, jti: jti}}, stub.calls)
	})
	t.Run("nil store with jti", func(t *testing.T) {
		logs := agent24CaptureCredentialLogs(t)
		code, called, body := serve(nil, ready, jti, signed, true)
		require.Contains(t, logs.String(), "store=unwired")
		require.NotContains(t, logs.String(), "error_type")
		require.False(t, called)
		require.Equal(t, http.StatusInternalServerError, code)
		e := body["error"].(map[string]any)
		require.Equal(t, "internal", e["code"])
	})
	t.Run("tenant mode key without jti", func(t *testing.T) {
		tenant := &types.Tenant{ID: tenantID, APIPrincipalConfig: &types.APIPrincipalConfig{
			Mode: types.APIPrincipalModeTenant,
		}}
		code, called, body := serve(nil, ready, "", tenant, true)
		require.True(t, called)
		require.Equal(t, http.StatusOK, code)
		require.Equal(t, true, body["ok"])
	})
}

// agent24CaptureCredentialLogs sends the logger to a buffer for one test.
func agent24CaptureCredentialLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	t.Cleanup(logger.ConfigureFromEnv)
	return &buf
}
