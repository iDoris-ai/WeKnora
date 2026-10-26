package middleware

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

func TestPlatformTenantOptionalAPIs(t *testing.T) {
	cases := []struct {
		method string
		path   string
		want   bool
	}{
		{http.MethodGet, "/api/v1/system/admin/settings", true},
		{http.MethodGet, "/api/v1/tenants/all", true},
		{http.MethodGet, "/api/v1/tenants/search", true},
		{http.MethodPost, "/api/v1/tenants", true},
		{http.MethodGet, "/api/v1/knowledge-bases", false},
		{http.MethodGet, "/api/v1/tenants", false},
	}
	for _, tc := range cases {
		if got := isPlatformTenantOptionalAPI(tc.path, tc.method); got != tc.want {
			t.Fatalf("isPlatformTenantOptionalAPI(%s, %s) = %v, want %v", tc.method, tc.path, got, tc.want)
		}
	}
}

func TestAttachPlatformAPIKeyAuthContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/system/admin/settings", nil)
	attachPlatformAPIKeyAuthContext(c, &types.TenantAPIKey{
		ID: 9, ScopeType: types.APIKeyScopePlatform,
		Capabilities: types.StringArray{string(types.APIKeyCapabilitySystemSettingsRead)},
	})
	scope, ok := types.TenantAPIKeyScopeFromContext(c.Request.Context())
	if !ok || !scope.IsPlatform() || scope.KeyID != 9 {
		t.Fatalf("platform scope = %#v, ok=%v", scope, ok)
	}
	if _, ok := types.TenantIDFromContext(c.Request.Context()); ok {
		t.Fatal("tenantless platform control-plane request must not have tenant context")
	}
	principal, ok := types.PrincipalFromContext(c.Request.Context())
	if !ok || principal.Type != types.PrincipalAPIPlatform {
		t.Fatalf("principal = %#v, ok=%v", principal, ok)
	}
}

func TestPlatformAPIKeyIdentityIsStableAcrossTenants(t *testing.T) {
	principal, user := platformAPIKeyIdentity(&types.TenantAPIKey{ID: 27})
	if principal.Type != types.PrincipalAPIPlatform || principal.ID != "27" {
		t.Fatalf("principal = %#v", principal)
	}
	if user.ID != "api_platform:27" || user.Username != user.ID {
		t.Fatalf("user = %#v", user)
	}
}

func TestAttachTargetedPlatformAPIKeyKeepsPlatformPrincipal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/knowledge-bases", nil)
	key := &types.TenantAPIKey{
		ID:         27,
		ScopeType:  types.APIKeyScopePlatform,
		FullAccess: true, // Corrupt/legacy data must still be capability-only at runtime.
		Capabilities: types.StringArray{
			string(types.APIKeyCapabilityRetrieve),
		},
	}
	attachAPIKeyAuthContext(c, &fakeTenantService{tenant: &types.Tenant{ID: 42}}, nil, 42, key)
	if c.IsAborted() {
		t.Fatal("targeted platform API key context unexpectedly aborted")
	}
	principal, ok := types.PrincipalFromContext(c.Request.Context())
	if !ok || principal.Type != types.PrincipalAPIPlatform || principal.ID != "27" {
		t.Fatalf("principal = %#v, ok=%v", principal, ok)
	}
	tenantID, ok := types.TenantIDFromContext(c.Request.Context())
	if !ok || tenantID != 42 {
		t.Fatalf("tenant id = %d, ok=%v", tenantID, ok)
	}
	scope, ok := types.TenantAPIKeyScopeFromContext(c.Request.Context())
	if !ok || !scope.IsPlatform() || scope.FullAccess {
		t.Fatalf("scope = %#v, ok=%v", scope, ok)
	}
	user, ok := c.Request.Context().Value(types.UserContextKey).(*types.User)
	if !ok || user.ID != "api_platform:27" || user.TenantID != 42 {
		t.Fatalf("user = %#v, ok=%v", user, ok)
	}
}

func TestAuthAPIKeyTenantLookupLogOmitsSentinel(t *testing.T) {
	buf := captureAuthLogs(t)
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/knowledge-bases", nil)
	tenants := errTenantService{
		fakeTenantService: &fakeTenantService{},
		err:               errors.New("S3NT1NEL"),
	}

	attachAPIKeyAuthContext(c, tenants, nil, 42, &types.TenantAPIKey{ID: 7})
	if !c.IsAborted() {
		t.Fatal("API key tenant lookup failure did not abort")
	}
	body := rec.Body.String()
	if rec.Code != http.StatusUnauthorized || !strings.Contains(body, "Unauthorized: invalid API key") {
		t.Fatalf("response = %d %s", rec.Code, body)
	}
	if strings.Contains(body, "S3NT1NEL") {
		t.Fatalf("response contains sentinel: %s", body)
	}
	assertAuthLogOmitsSentinel(t, buf.String(), "[auth] API key tenant lookup failed: tenant=42")
}
