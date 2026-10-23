package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
)

// TestAgent24KnowledgeAuthEnvelope guards A24-KB-11: on the knowledge path, Auth's early
// rejections become the contract envelope; a released writer, other paths, and panics do not.
func TestAgent24KnowledgeAuthEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, ids := agent24SchemaCompiler(t)
	errSchema, err := c.Compile(ids["error.schema.json"])
	require.NoError(t, err)

	reject := func(status int, payload gin.H) gin.HandlerFunc {
		return func(c *gin.Context) {
			c.JSON(status, payload)
			c.Abort()
		}
	}
	pass := func(c *gin.Context) { c.Next() }
	large := bytes.Repeat([]byte("x"), 64*1024+1)
	const readyPath = "/api/v1/agent24/knowledge/ready"
	const otherPath = "/api/v1/other"

	cases := []struct {
		name      string
		path      string
		authz     string
		apiKey    string
		auth      gin.HandlerFunc
		ready     string // "", "body", "panic"
		status    int
		code      string
		reason    string
		noReason  bool
		raw       string
		oneObject bool
	}{
		{
			name: "401 missing credential", path: readyPath,
			auth:   reject(http.StatusUnauthorized, gin.H{"error": "Unauthorized: missing authentication"}),
			status: http.StatusUnauthorized, code: "unauthorized", reason: "credential_missing",
		},
		{
			name: "401 bearer", path: readyPath, authz: "Bearer x",
			auth:   reject(http.StatusUnauthorized, gin.H{"error": "Unauthorized: invalid or expired token"}),
			status: http.StatusUnauthorized, code: "unauthorized", reason: "credential_invalid",
		},
		{
			name: "401 api key", path: readyPath, apiKey: "sk-test",
			auth:   reject(http.StatusUnauthorized, gin.H{"error": "Unauthorized: invalid API key"}),
			status: http.StatusUnauthorized, code: "unauthorized", reason: "credential_invalid",
		},
		{
			name: "403 with api key", path: readyPath, apiKey: "sk-test",
			auth:   reject(http.StatusForbidden, gin.H{"error": "Forbidden: not a member of the target workspace"}),
			status: http.StatusUnauthorized, code: "unauthorized", reason: "credential_invalid",
		},
		{
			name: "409 tenant required", path: readyPath, apiKey: "sk-test",
			auth: reject(http.StatusConflict, gin.H{
				"error": "Workspace required: platform API keys must send X-Tenant-ID",
				"code":  "TENANT_REQUIRED",
			}),
			status: http.StatusUnauthorized, code: "unauthorized", reason: "credential_invalid",
		},
		{
			name: "400 invalid request", path: readyPath,
			auth:   reject(http.StatusBadRequest, gin.H{"error": "Invalid X-Tenant-ID header"}),
			status: http.StatusBadRequest, code: "invalid_request", noReason: true,
		},
		{
			name: "abort without write", path: readyPath,
			auth:   func(c *gin.Context) { c.Abort() },
			status: http.StatusInternalServerError, code: "internal", noReason: true,
		},
		{
			name: "418", path: readyPath,
			auth:   reject(418, gin.H{"error": "teapot"}),
			status: http.StatusInternalServerError, code: "internal", noReason: true,
		},
		{
			name: "passthrough large body", path: readyPath, auth: pass, ready: "body",
			status: http.StatusOK,
		},
		{
			name: "credential gate one envelope", path: readyPath, ready: "",
			auth: func(c *gin.Context) {
				scope := types.TenantAPIKeyScope{KeyID: 1, ScopeType: types.APIKeyScopePlatform}
				ctx := types.WithTenantAPIKeyScope(c.Request.Context(), scope)
				c.Request = c.Request.WithContext(ctx)
				c.Next()
			},
			status: http.StatusUnauthorized, code: "unauthorized", reason: "credential_invalid",
			oneObject: true,
		},
		{
			name: "non-knowledge upstream", path: otherPath,
			auth: reject(http.StatusUnauthorized, gin.H{"error": "Unauthorized: missing authentication"}),
			raw:  `{"error":"Unauthorized: missing authentication"}`, status: http.StatusUnauthorized,
		},
		{
			name: "panic before write", path: readyPath,
			auth: func(*gin.Context) { panic("auth") }, status: http.StatusInternalServerError,
		},
		{
			name: "panic in handler", path: readyPath, auth: pass, ready: "panic",
			status: http.StatusInternalServerError,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var ready gin.HandlerFunc
			switch tc.ready {
			case "body":
				ready = func(c *gin.Context) {
					_, held := c.Writer.(*agent24HeldWriter)
					require.False(t, held)
					c.Data(http.StatusOK, "text/plain", large)
				}
			case "panic":
				ready = func(*gin.Context) { panic("handler") }
			default:
				ready = func(*gin.Context) {}
			}
			r := gin.New()
			r.Use(gin.Recovery())
			r.Use(Agent24KnowledgeAuthEnvelope())
			r.Use(tc.auth)
			r.Use(Agent24IntegrationCredentialGate())
			r.POST(readyPath, ready)
			r.POST(otherPath, func(*gin.Context) {})

			req := httptest.NewRequest(http.MethodPost, tc.path, nil)
			if tc.authz != "" {
				req.Header.Set("Authorization", tc.authz)
			}
			if tc.apiKey != "" {
				req.Header.Set("X-API-Key", tc.apiKey)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			switch {
			case tc.raw != "":
				require.Equal(t, tc.status, w.Code, w.Body.String())
				require.JSONEq(t, tc.raw, w.Body.String())
			case tc.ready == "body":
				require.Equal(t, http.StatusOK, w.Code, w.Body.String())
				require.Greater(t, w.Body.Len(), 64*1024)
				require.Equal(t, large, w.Body.Bytes())
			case tc.ready == "panic" || tc.name == "panic before write":
				require.Equal(t, http.StatusInternalServerError, w.Code, "status=%d body=%q", w.Code, w.Body.String())
			default:
				require.Equal(t, tc.status, w.Code, w.Body.String())
				requireAgent24RewrittenEnvelope(t, errSchema, w.Body.Bytes(), tc.code, tc.reason, tc.noReason)
				if tc.oneObject {
					var obj map[string]any
					require.NoError(t, json.Unmarshal(w.Body.Bytes(), &obj), w.Body.String())
				}
			}
		})
	}
}

func requireAgent24RewrittenEnvelope(
	t *testing.T, sch *jsonschema.Schema, body []byte, code, reason string, noReason bool,
) {
	t.Helper()
	require.NoError(t, agent24Validate(sch, body), string(body))
	var top map[string]any
	require.NoError(t, json.Unmarshal(body, &top), string(body))
	errObj, ok := top["error"].(map[string]any)
	require.True(t, ok, string(body))
	require.Equal(t, code, errObj["code"], string(body))
	details, ok := errObj["details"].(map[string]any)
	require.True(t, ok, string(body))
	require.Contains(t, details, "request_id")
	require.Nil(t, details["request_id"])
	require.Contains(t, details, "generation")
	require.Nil(t, details["generation"])
	if noReason {
		_, found := details["reason"]
		require.False(t, found, string(body))
	} else {
		require.Equal(t, reason, details["reason"], string(body))
	}
	require.NotContains(t, string(body), "Unauthorized: missing authentication")
	require.NotContains(t, string(body), "TENANT_REQUIRED")
}
