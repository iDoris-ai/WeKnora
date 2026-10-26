package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

func TestTenantOptionalAPISurface(t *testing.T) {
	tests := []struct {
		method string
		path   string
		want   bool
	}{
		{http.MethodGet, "/api/v1/auth/me", true},
		{http.MethodPut, "/api/v1/auth/me", true},
		{http.MethodPut, "/api/v1/auth/me/preferences", true},
		{http.MethodPost, "/api/v1/tenants", true},
		{http.MethodGet, "/api/v1/me/invitations", true},
		{http.MethodPost, "/api/v1/me/invitations/12/accept", true},
		{http.MethodGet, "/api/v1/knowledge-bases", false},
		{http.MethodGet, "/api/v1/tenants", false},
	}
	for _, tt := range tests {
		if got := isTenantOptionalAPI(tt.path, tt.method); got != tt.want {
			t.Errorf("isTenantOptionalAPI(%s %s) = %v, want %v", tt.method, tt.path, got, tt.want)
		}
	}
}

func TestResolveFirstMembershipTarget(t *testing.T) {
	members := newFakeMemberService()
	members.seedActive("tenantless-user", 42, types.TenantRoleViewer)
	tenants := &fakeTenantService{tenant: &types.Tenant{ID: 42}}

	got := resolveFirstMembershipTarget(
		context.Background(),
		&types.User{ID: "tenantless-user"},
		members,
		tenants,
	)
	if got != 42 {
		t.Fatalf("resolved tenant = %d, want 42", got)
	}
}

// errTenantService makes GetTenantByID fail. Other methods delegate to the embedded fake.
type errTenantService struct {
	*fakeTenantService
	err error
}

func (s errTenantService) GetTenantByID(ctx context.Context, id uint64) (*types.Tenant, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.fakeTenantService.GetTenantByID(ctx, id)
}

// listErrMemberService makes ListByUser fail. Other methods delegate to the embedded fake.
type listErrMemberService struct {
	*fakeMemberService
	err error
}

func (s listErrMemberService) ListByUser(context.Context, string) ([]*types.TenantMember, error) {
	return nil, s.err
}

func TestAuthInvalidTenantHeaderLogOmitsSentinel(t *testing.T) {
	buf := captureAuthLogs(t)
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/knowledge-bases", nil)
	c.Request.Header.Set("X-Tenant-ID", "S3NT1NEL")

	_, _, _, ok := resolveTargetTenant(
		c, &fakeTenantService{}, newFakeMemberService(), cfgWithRBAC(true),
		&types.User{ID: "user-1", TenantID: 7}, 7,
	)
	if ok {
		t.Fatal("invalid X-Tenant-ID was accepted")
	}
	body := rec.Body.String()
	if rec.Code != http.StatusBadRequest || !strings.Contains(body, "Invalid X-Tenant-ID header") {
		t.Fatalf("response = %d %s", rec.Code, body)
	}
	if strings.Contains(body, "S3NT1NEL") {
		t.Fatalf("response contains sentinel: %s", body)
	}
	logs := buf.String()
	assertAuthLogOmitsSentinel(t, logs, "Invalid X-Tenant-ID header from user=user-1")
	if !strings.Contains(logs, "header_len=") || !strings.Contains(logs, "error_len=") {
		t.Fatalf("log missing header_len or error_len:\n%s", logs)
	}
}

func TestAuthTargetTenantLookupLogOmitsSentinel(t *testing.T) {
	buf := captureAuthLogs(t)
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/knowledge-bases", nil)
	c.Request.Header.Set("X-Tenant-ID", "42")
	tenants := errTenantService{
		fakeTenantService: &fakeTenantService{},
		err:               errors.New("S3NT1NEL"),
	}

	_, _, _, ok := resolveTargetTenant(
		c, tenants, newFakeMemberService(), cfgWithRBAC(true),
		&types.User{ID: "user-1", TenantID: 42}, 7,
	)
	if ok {
		t.Fatal("missing target tenant was accepted")
	}
	body := rec.Body.String()
	if rec.Code != http.StatusBadRequest || !strings.Contains(body, "Invalid target workspace ID") {
		t.Fatalf("response = %d %s", rec.Code, body)
	}
	if strings.Contains(body, "S3NT1NEL") {
		t.Fatalf("response contains sentinel: %s", body)
	}
	logs := buf.String()
	assertAuthLogOmitsSentinel(t, logs, "Error getting target tenant by ID")
	if !strings.Contains(logs, "tenantID: 42") {
		t.Fatalf("log missing tenant id:\n%s", logs)
	}
}

func TestAuthJWTTenantLookupLogOmitsSentinel(t *testing.T) {
	buf := captureAuthLogs(t)
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/knowledge-bases", nil)
	tenants := errTenantService{
		fakeTenantService: &fakeTenantService{},
		err:               errors.New("S3NT1NEL"),
	}

	ok := authenticateJWTUser(
		c, tenants, newFakeMemberService(), cfgWithRBAC(true),
		&types.User{ID: "user-1", TenantID: 7}, 7,
	)
	if ok {
		t.Fatal("tenant lookup failure was accepted")
	}
	body := rec.Body.String()
	if rec.Code != http.StatusUnauthorized || !strings.Contains(body, "Unauthorized: invalid workspace") {
		t.Fatalf("response = %d %s", rec.Code, body)
	}
	if strings.Contains(body, "S3NT1NEL") {
		t.Fatalf("response contains sentinel: %s", body)
	}
	assertAuthLogOmitsSentinel(t, buf.String(), "[auth] tenant lookup failed: tenant=7 user=user-1")
}

func TestAuthMembershipListLogOmitsSentinel(t *testing.T) {
	buf := captureAuthLogs(t)
	members := listErrMemberService{
		fakeMemberService: newFakeMemberService(),
		err:               errors.New("S3NT1NEL"),
	}
	got := resolveFirstMembershipTarget(
		context.Background(),
		&types.User{ID: "user-1"},
		members,
		&fakeTenantService{},
	)
	if got != 0 {
		t.Fatalf("resolved tenant = %d, want 0", got)
	}
	assertAuthLogOmitsSentinel(t, buf.String(), "Failed to list memberships for tenantless user user-1")
}
