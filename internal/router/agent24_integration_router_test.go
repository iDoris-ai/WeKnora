package router

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"sync"
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
	agent24SetTagKey(t, base64.StdEncoding.EncodeToString(make([]byte, 32)))
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
	agent24SetTagKey(t, base64.StdEncoding.EncodeToString(make([]byte, 32)))
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

				Agent24KnowledgeReadyHandler:  &handler.Agent24KnowledgeReadyHandler{},
				Agent24KnowledgeSearchHandler: &handler.Agent24KnowledgeSearchHandler{},
				Agent24KnowledgeReadHandler:   &handler.Agent24KnowledgeReadHandler{},
			})
			for _, op := range []string{"ready", "search", "read"} {
				req := httptest.NewRequest(http.MethodPost, "/api/v1/agent24/knowledge/"+op, strings.NewReader("{}"))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-API-Key", "sk-test")
				req.Header.Set("X-Tenant-ID", "1")
				w := httptest.NewRecorder()
				r.ServeHTTP(w, req)
				require.Equal(t, http.StatusUnauthorized, w.Code, "%s: %s", op, w.Body.String())
				var body struct {
					Error struct {
						Details map[string]any `json:"details"`
					} `json:"error"`
				}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), w.Body.String())
				require.Equal(t, "credential_invalid", body.Error.Details["reason"], op)
			}
		})
	}
}

// agent24RejectTokenUserSvc fails ValidateToken so Auth takes the bearer-rejection path.
type agent24RejectTokenUserSvc struct{ agent24FakeUserSvc }

func (agent24RejectTokenUserSvc) ValidateToken(context.Context, string) (*types.User, uint64, error) {
	return nil, 0, errors.New("invalid or expired token")
}

// agent24RejectKeySvc fails AuthenticateAPIKey.
type agent24RejectKeySvc struct{ interfaces.TenantAPIKeyService }

func (agent24RejectKeySvc) AuthenticateAPIKey(context.Context, string) (*types.TenantAPIKey, error) {
	return nil, errors.New("invalid API key")
}

// agent24TokenUserSvc accepts any bearer token as user, with the JWT tenant claim.
type agent24TokenUserSvc struct {
	agent24FakeUserSvc
	user        *types.User
	jwtTenantID uint64
}

func (s agent24TokenUserSvc) ValidateToken(context.Context, string) (*types.User, uint64, error) {
	return s.user, s.jwtTenantID, nil
}

// agent24NoMemberSvc has no membership rows. GetMembership misses and ListByUser is empty.
type agent24NoMemberSvc struct{ interfaces.TenantMemberService }

func (agent24NoMemberSvc) GetMembership(context.Context, string, uint64) (*types.TenantMember, error) {
	return nil, nil
}

func (agent24NoMemberSvc) ListByUser(context.Context, string) ([]*types.TenantMember, error) {
	return nil, nil
}

// agent24OrphanHomeSvc has no membership rows and lets the orphan home-workspace self-heal succeed
// (auth.go:798-809): a user reaching it becomes Owner, so Auth lets the request through.
type agent24OrphanHomeSvc struct{ agent24NoMemberSvc }

func (agent24OrphanHomeSvc) HasAnyMembers(context.Context, uint64) (bool, error) { return false, nil }

func (agent24OrphanHomeSvc) AddMember(
	context.Context, string, uint64, types.TenantRole, *string,
) (*types.TenantMember, error) {
	return &types.TenantMember{}, nil
}

func agent24RequireKnowledgeEnvelope(t *testing.T, body []byte, code, reason string, noReason bool) {
	t.Helper()
	var parsed struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(body, &parsed), string(body))
	require.Equal(t, code, parsed.Error.Code, string(body))
	if noReason {
		_, ok := parsed.Error.Details["reason"]
		require.False(t, ok, string(body))
		return
	}
	require.Equal(t, reason, parsed.Error.Details["reason"], string(body))
}

// TestAgent24IntegrationAuthEnvelopeNewRouter guards A24-KB-11: Auth's early rejections on the
// knowledge endpoints are contract envelopes.
func TestAgent24IntegrationAuthEnvelopeNewRouter(t *testing.T) {
	agent24SetTagKey(t, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	gin.SetMode(gin.TestMode)
	t.Setenv("LOCAL_STORAGE_BASE_DIR", t.TempDir())
	t.Setenv("AGENT24_KNOWLEDGE_MODE", "integration")

	tenantID := uint64(1)
	platform := &types.TenantAPIKey{ID: 1, ScopeType: types.APIKeyScopePlatform}
	tenantKey := &types.TenantAPIKey{ID: 2, ScopeType: types.APIKeyScopeTenant, TenantID: &tenantID}
	plain := &types.Tenant{ID: tenantID}
	// RequireDirectHeader and no X-External-User-ID is auth.go's 401 (the direct-header path).
	direct := &types.Tenant{ID: tenantID, APIPrincipalConfig: &types.APIPrincipalConfig{
		Mode: types.APIPrincipalModeDirect, RequireDirectHeader: true,
	}}
	// Workspace 2 is never the caller's home. auth.go:316 returns before tenant lookup, so a
	// fixture for workspace 1 makes a missed 316 fall through to 400 instead of another 403.
	otherID := uint64(2)
	noMember := agent24NoMemberSvc{}
	memberUser := &types.User{ID: "user-1", TenantID: tenantID}
	tenantlessUser := &types.User{ID: "user-2"}
	otherTenant := &types.Tenant{ID: otherID}
	tenantModeKey := &types.TenantAPIKey{ID: 3, ScopeType: types.APIKeyScopeTenant, TenantID: &tenantID}
	tenantMode := &types.Tenant{ID: tenantID, APIPrincipalConfig: &types.APIPrincipalConfig{
		Mode: types.APIPrincipalModeTenant,
	}}

	cases := []struct {
		name       string
		keys       interfaces.TenantAPIKeyService
		users      interfaces.UserService
		members    interfaces.TenantMemberService
		tenant     *types.Tenant
		bearer     string
		apiKey     string
		tenantHdr  string
		wantStatus int
		wantCode   string
		wantReason string
		noReason   bool
	}{
		{
			name: "no credential", keys: agent24FakeKeySvc{key: platform}, users: agent24FakeUserSvc{},
			tenant: plain, wantStatus: http.StatusUnauthorized, wantCode: "unauthorized",
			wantReason: "credential_missing",
		},
		{
			name: "bearer token", keys: agent24FakeKeySvc{key: platform}, users: agent24RejectTokenUserSvc{},
			tenant: plain, bearer: "Bearer bad", wantStatus: http.StatusUnauthorized,
			wantCode: "unauthorized", wantReason: "credential_invalid",
		},
		{
			name: "api key rejected", keys: agent24RejectKeySvc{}, users: agent24FakeUserSvc{},
			tenant: plain, apiKey: "sk-test", wantStatus: http.StatusUnauthorized,
			wantCode: "unauthorized", wantReason: "credential_invalid",
		},
		{
			name: "platform key missing tenant", keys: agent24FakeKeySvc{key: platform}, users: agent24FakeUserSvc{},
			tenant: plain, apiKey: "sk-test", wantStatus: http.StatusUnauthorized,
			wantCode: "unauthorized", wantReason: "credential_invalid",
		},
		{
			name: "platform key bad tenant id", keys: agent24FakeKeySvc{key: platform}, users: agent24FakeUserSvc{},
			tenant: plain, apiKey: "sk-test", tenantHdr: "abc", wantStatus: http.StatusBadRequest,
			wantCode: "invalid_request", noReason: true,
		},
		{
			name: "direct header missing", keys: agent24FakeKeySvc{key: tenantKey}, users: agent24FakeUserSvc{},
			tenant: direct, apiKey: "sk-test", tenantHdr: "1", wantStatus: http.StatusUnauthorized,
			wantCode: "unauthorized", wantReason: "credential_invalid",
		},
		{
			// auth.go:248 — JWT tenant is not the caller's home, and there is no membership.
			name: "jwt not workspace member", keys: agent24FakeKeySvc{key: platform},
			users:   agent24TokenUserSvc{user: memberUser, jwtTenantID: otherID},
			members: noMember, tenant: otherTenant, bearer: "Bearer good",
			wantStatus: http.StatusUnauthorized, wantCode: "unauthorized",
			wantReason: "credential_invalid",
		},
		{
			// auth.go:316 — X-Tenant-ID names a workspace the caller cannot access.
			name: "jwt forbidden tenant header", keys: agent24FakeKeySvc{key: platform},
			users:   agent24TokenUserSvc{user: memberUser, jwtTenantID: tenantID},
			members: noMember, tenant: plain, bearer: "Bearer good", tenantHdr: "2",
			wantStatus: http.StatusUnauthorized, wantCode: "unauthorized",
			wantReason: "credential_invalid",
		},
		{
			// auth.go:424 — a tenant-mode tenant key cannot name another workspace.
			name: "tenant key other workspace", keys: agent24FakeKeySvc{key: tenantModeKey},
			users: agent24FakeUserSvc{}, tenant: tenantMode, apiKey: "sk-test", tenantHdr: "2",
			wantStatus: http.StatusUnauthorized, wantCode: "unauthorized",
			wantReason: "credential_invalid",
		},
		{
			// auth.go:219 — valid JWT, no home workspace and no membership. Were :219 skipped, workspace 0 exists
			// (so not :234's 401) and is the user's home with no members, so the self-heal makes the user Owner
			// (not :248's 403): Auth would let the request through, never this envelope.
			name: "jwt no workspace", keys: agent24FakeKeySvc{key: platform},
			users:   agent24TokenUserSvc{user: tenantlessUser},
			members: agent24OrphanHomeSvc{}, tenant: &types.Tenant{}, bearer: "Bearer good",
			wantStatus: http.StatusUnauthorized, wantCode: "unauthorized",
			wantReason: "credential_invalid",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRouter(RouterParams{
				Config: &config.Config{Tenant: &config.TenantConfig{}}, SystemHandler: &handler.SystemHandler{},
				TenantAPIKeyService:           tc.keys,
				TenantService:                 agent24FakeTenantSvc{tenant: tc.tenant},
				UserService:                   tc.users,
				TenantMemberService:           tc.members,
				Agent24KnowledgeReadyHandler:  &handler.Agent24KnowledgeReadyHandler{},
				Agent24KnowledgeSearchHandler: &handler.Agent24KnowledgeSearchHandler{},
				Agent24KnowledgeReadHandler:   &handler.Agent24KnowledgeReadHandler{},
			})
			for _, op := range []string{"ready", "search", "read"} {
				t.Run(op, func(t *testing.T) {
					req := httptest.NewRequest(
						http.MethodPost, "/api/v1/agent24/knowledge/"+op, strings.NewReader("{}"),
					)
					req.Header.Set("Content-Type", "application/json")
					if tc.bearer != "" {
						req.Header.Set("Authorization", tc.bearer)
					}
					if tc.apiKey != "" {
						req.Header.Set("X-API-Key", tc.apiKey)
					}
					if tc.tenantHdr != "" {
						req.Header.Set("X-Tenant-ID", tc.tenantHdr)
					}
					w := httptest.NewRecorder()
					r.ServeHTTP(w, req)
					require.Equal(t, tc.wantStatus, w.Code, "%s: %s", op, w.Body.String())
					agent24RequireKnowledgeEnvelope(t, w.Body.Bytes(), tc.wantCode, tc.wantReason, tc.noReason)
				})
			}
		})
	}
}

// TestAgent24StandaloneAuthEnvelopeNewRouter guards L1: standalone does not install the envelope
// or the credential gate.
func TestAgent24StandaloneAuthEnvelopeNewRouter(t *testing.T) {
	agent24SetTagKey(t, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	gin.SetMode(gin.TestMode)
	t.Setenv("LOCAL_STORAGE_BASE_DIR", t.TempDir())
	t.Setenv(handler.Agent24KnowledgeModeEnv, "")
	require.NoError(t, os.Unsetenv(handler.Agent24KnowledgeModeEnv))

	tenantID := uint64(1)
	r := NewRouter(RouterParams{
		Config: &config.Config{Tenant: &config.TenantConfig{}}, SystemHandler: &handler.SystemHandler{},
		TenantAPIKeyService: agent24FakeKeySvc{key: &types.TenantAPIKey{
			ID: 1, ScopeType: types.APIKeyScopePlatform,
		}},
		TenantService:                 agent24FakeTenantSvc{tenant: &types.Tenant{ID: tenantID}},
		UserService:                   agent24FakeUserSvc{},
		Agent24KnowledgeReadyHandler:  &handler.Agent24KnowledgeReadyHandler{},
		Agent24KnowledgeSearchHandler: &handler.Agent24KnowledgeSearchHandler{},
		Agent24KnowledgeReadHandler:   &handler.Agent24KnowledgeReadHandler{},
	})

	t.Run("no credential", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/agent24/knowledge/ready", strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
		var body map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), w.Body.String())
		require.Equal(t, "Unauthorized: missing authentication", body["error"])
	})

	t.Run("platform key", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/agent24/knowledge/ready", strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-API-Key", "sk-test")
		req.Header.Set("X-Tenant-ID", "1")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
		var body map[string]any
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), w.Body.String())
		errText, isString := body["error"].(string)
		require.True(t, isString, w.Body.String())
		require.Equal(t, "Forbidden: API key scope does not allow this operation", errText)
	})
}

// agent24KnowledgeAuditStub records Log entries. Other AuditLogService methods panic if called.
type agent24KnowledgeAuditStub struct {
	interfaces.AuditLogService
	mu      sync.Mutex
	entries []*types.AuditLog
}

func (s *agent24KnowledgeAuditStub) Log(_ context.Context, entry *types.AuditLog) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *entry
	cp.Details = append(types.JSON(nil), entry.Details...)
	s.entries = append(s.entries, &cp)
	return nil
}

// TestAgent24IntegrationKnowledgeAuditBeforeAuth guards A24-KB-16: an invalid key is rejected by Auth, and the
// audit middleware (mounted before Auth) still records one unauthorized row.
func TestAgent24IntegrationKnowledgeAuditBeforeAuth(t *testing.T) {
	agent24SetTagKey(t, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	gin.SetMode(gin.TestMode)
	t.Setenv("LOCAL_STORAGE_BASE_DIR", t.TempDir())
	t.Setenv("AGENT24_KNOWLEDGE_MODE", "integration")
	audit := &agent24KnowledgeAuditStub{}
	r := NewRouter(RouterParams{
		Config: &config.Config{Tenant: &config.TenantConfig{}}, SystemHandler: &handler.SystemHandler{},
		TenantAPIKeyService:           agent24RejectKeySvc{},
		TenantService:                 agent24FakeTenantSvc{tenant: &types.Tenant{ID: 1}},
		UserService:                   agent24FakeUserSvc{},
		AuditLogService:               audit,
		Agent24KnowledgeSearchHandler: &handler.Agent24KnowledgeSearchHandler{},
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent24/knowledge/search", strings.NewReader("{}"))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "sk-test")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
	require.Len(t, audit.entries, 1)
	var details struct {
		Outcome string `json:"outcome"`
	}
	require.NoError(t, json.Unmarshal(audit.entries[0].Details, &details))
	require.Equal(t, "unauthorized", details.Outcome)
	require.Equal(t, "", audit.entries[0].ActorUserID)
	require.Equal(t, "agent24_knowledge", audit.entries[0].ScopeType)
}

// TestAgent24KnowledgeAuditMountedOutsideRecovery guards A24-KB-16: the audit middleware runs before
// Recovery, so a panic reaches it as Recovery's 500 (status and size as written), not as a bare panic.
func TestAgent24KnowledgeAuditMountedOutsideRecovery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("LOCAL_STORAGE_BASE_DIR", t.TempDir())
	r := NewRouter(RouterParams{
		Config: &config.Config{Tenant: &config.TenantConfig{}}, SystemHandler: &handler.SystemHandler{},
		AuditLogService: &agent24KnowledgeAuditStub{},
	})
	audit, recovery := -1, -1
	for i, h := range r.Handlers {
		name := runtime.FuncForPC(reflect.ValueOf(h).Pointer()).Name()
		switch {
		// Inlining may report the closure under NewRouter (router.NewRouter.Recovery.func6), so match the tail.
		// The audit closure is built by the unexported agent24KnowledgeAudit.
		case strings.Contains(name, ".agent24KnowledgeAudit.func"):
			audit = i
		case strings.Contains(name, ".Recovery.func"):
			recovery = i
		}
	}
	require.GreaterOrEqual(t, audit, 0)
	require.GreaterOrEqual(t, recovery, 0)
	require.Less(t, audit, recovery)
}
