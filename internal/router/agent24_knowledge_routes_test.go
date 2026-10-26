package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/gin-gonic/gin"
)

func TestAgent24KnowledgeRoutePolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	g := &rbacGuards{}
	engine := gin.New()
	RegisterAgent24KnowledgeRoutes(engine.Group("/api/v1"), &handler.Agent24KnowledgeReadyHandler{},
		&handler.Agent24KnowledgeSearchHandler{}, &handler.Agent24KnowledgeReadHandler{}, g)

	for _, path := range agent24KnowledgeRoutePaths {
		n := 0
		for _, rt := range engine.Routes() {
			if rt.Method == http.MethodPost && rt.Path == path {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("POST %s count = %d, routes = %#v", path, n, engine.Routes())
		}
		policy := mustLookupAPIKeyPolicy(t, g, http.MethodPost, path)
		want := []types.APIKeyCapability{types.APIKeyCapabilityRetrieve}
		if len(policy.Capabilities) != len(want) || policy.Capabilities[0] != want[0] {
			t.Fatalf("%s capabilities = %#v, want %#v", path, policy.Capabilities, want)
		}
		if policy.RequireFullAccess {
			t.Fatalf("%s RequireFullAccess = true, want false", path)
		}
		if policy.PlatformOnly {
			t.Fatalf("%s PlatformOnly = true, want false", path)
		}
	}
	if len(engine.Routes()) != 3 {
		t.Fatalf("routes = %#v, want exactly ready, search and read", engine.Routes())
	}
}

var agent24KnowledgeRoutePaths = []string{
	"/api/v1/agent24/knowledge/ready", "/api/v1/agent24/knowledge/search", "/api/v1/agent24/knowledge/read",
}

func TestAgent24KnowledgeRouteNilHandlers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ready, search := &handler.Agent24KnowledgeReadyHandler{}, &handler.Agent24KnowledgeSearchHandler{}
	read := &handler.Agent24KnowledgeReadHandler{}
	for _, tc := range []struct {
		name  string
		ready *handler.Agent24KnowledgeReadyHandler
		srch  *handler.Agent24KnowledgeSearchHandler
		read  *handler.Agent24KnowledgeReadHandler
		want  []string
	}{
		{"all nil", nil, nil, nil, nil},
		{"ready only", ready, nil, nil, []string{"/api/v1/agent24/knowledge/ready"}},
		{"search only", nil, search, nil, []string{"/api/v1/agent24/knowledge/search"}},
		{"read only", nil, nil, read, []string{"/api/v1/agent24/knowledge/read"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &rbacGuards{}
			engine := gin.New()
			RegisterAgent24KnowledgeRoutes(engine.Group("/api/v1"), tc.ready, tc.srch, tc.read, g)
			var got []string
			for _, rt := range engine.Routes() {
				got = append(got, rt.Path)
			}
			if len(got) != len(tc.want) || (len(got) == 1 && got[0] != tc.want[0]) {
				t.Fatalf("routes = %v, want %v", got, tc.want)
			}
			for _, path := range agent24KnowledgeRoutePaths {
				registered := len(got) == 1 && got[0] == path
				if g.apiKeyAuthorizer == nil {
					continue
				}
				if _, ok := g.apiKeyAuthorizer.Lookup(http.MethodPost, path); ok != registered {
					t.Fatalf("%s policy registered = %v, want %v", path, ok, registered)
				}
			}
		})
	}
}

// Each route reaches its own handler: a valid search body is a search refused as operation_disabled (a handler
// without the revision key) on the search route, and not a ready or read request (invalid_request) on the
// others; a valid read body is refused as operation_disabled only on the read route.
func TestAgent24KnowledgeRouteHandlers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	RegisterAgent24KnowledgeRoutes(engine.Group("/api/v1"), &handler.Agent24KnowledgeReadyHandler{},
		&handler.Agent24KnowledgeSearchHandler{}, &handler.Agent24KnowledgeReadHandler{}, &rbacGuards{})
	search := `{"protocol_version":1,"request_id":"r1","generation":7,"query":"q",` +
		`"scope":{"knowledge_base_ids":["a"]},"budget":{"deadline_ms":800,"max_results":5,"max_concurrency":1}}`
	read := `{"protocol_version":1,"request_id":"r1","generation":7,"scope":{"knowledge_base_ids":["a"]},` +
		`"knowledge_id":"k"}`
	for _, tc := range []struct {
		op, body, want string
		status         int
	}{
		{"search", search, `"reason":"operation_disabled"`, http.StatusForbidden},
		{"ready", search, `"code":"invalid_request"`, http.StatusBadRequest},
		{"read", search, `"code":"invalid_request"`, http.StatusBadRequest},
		{"read", read, `"reason":"operation_disabled"`, http.StatusForbidden},
		{"search", read, `"code":"invalid_request"`, http.StatusBadRequest},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/agent24/knowledge/"+tc.op, strings.NewReader(tc.body))
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.want) {
			t.Fatalf("%s: status %d body %s, want %d with %s", tc.op, w.Code, w.Body.String(), tc.status, tc.want)
		}
	}
}

func TestAgent24KnowledgeAuditRoutePolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	g := &rbacGuards{}
	engine := gin.New()
	RegisterAgent24KnowledgeAuditRoutes(engine.Group("/api/v1"), handler.NewAuditLogHandler(nil), g)

	const (
		systemPath = "/api/v1/system/admin/agent24/knowledge/audit"
		tenantPath = "/api/v1/tenants/:id/agent24/knowledge/audit"
	)
	seen := map[string]int{}
	for _, rt := range engine.Routes() {
		if rt.Method == http.MethodGet {
			seen[rt.Path]++
		}
	}
	if seen[systemPath] != 1 || seen[tenantPath] != 1 || len(engine.Routes()) != 2 {
		t.Fatalf("routes = %#v, want the tenant and system audit reads", engine.Routes())
	}
	policy := mustLookupAPIKeyPolicy(t, g, http.MethodGet, systemPath)
	want := []types.APIKeyCapability{types.APIKeyCapabilitySystemAuditRead}
	if len(policy.Capabilities) != len(want) || policy.Capabilities[0] != want[0] {
		t.Fatalf("%s capabilities = %#v, want %#v", systemPath, policy.Capabilities, want)
	}
	if !policy.PlatformOnly {
		t.Fatalf("%s PlatformOnly = false, want true", systemPath)
	}
	if policy.RequireFullAccess {
		t.Fatalf("%s RequireFullAccess = true, want false", systemPath)
	}
	if _, ok := g.apiKeyAuthorizer.Lookup(http.MethodGet, tenantPath); ok {
		t.Fatalf("tenant route declared an API key policy")
	}
	for method, paths := range g.apiKeyAuthorizer.RegisteredRoutes() {
		for _, p := range paths {
			if p == tenantPath {
				t.Fatalf("tenant route declared %s %s", method, p)
			}
		}
	}
}

func TestAgent24KnowledgeAuditRouteNilHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	g := &rbacGuards{}
	engine := gin.New()
	RegisterAgent24KnowledgeAuditRoutes(engine.Group("/api/v1"), nil, g)
	if len(engine.Routes()) != 0 {
		t.Fatalf("routes = %#v, want none", engine.Routes())
	}
	if g.apiKeyAuthorizer != nil {
		if _, ok := g.apiKeyAuthorizer.Lookup(http.MethodGet, "/api/v1/system/admin/agent24/knowledge/audit"); ok {
			t.Fatal("nil handler registered a system API key policy")
		}
	}
}

// The tenant route reaches ListAgent24KnowledgeAuditTenant (List tenant 7) and the system route reaches
// ListAgent24KnowledgeAuditSystem (List tenant 0). Guards are satisfied so the handler itself runs.
func TestAgent24KnowledgeAuditRouteHandlers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := &agent24KnowledgeAuditReadStub{}
	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	engine.Use(func(c *gin.Context) {
		ctx := c.Request.Context()
		ctx = context.WithValue(ctx, types.TenantIDContextKey, uint64(7))
		ctx = context.WithValue(ctx, types.TenantRoleContextKey, types.TenantRoleAdmin)
		ctx = context.WithValue(ctx, types.SystemAdminContextKey, true)
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	})
	RegisterAgent24KnowledgeAuditRoutes(engine.Group("/api/v1"), handler.NewAuditLogHandler(stub), &rbacGuards{})

	got := map[string]string{}
	for _, rt := range engine.Routes() {
		got[rt.Method+" "+rt.Path] = rt.Handler
	}
	want := map[string]string{
		http.MethodGet + " /api/v1/tenants/:id/agent24/knowledge/audit":  "ListAgent24KnowledgeAuditTenant",
		http.MethodGet + " /api/v1/system/admin/agent24/knowledge/audit": "ListAgent24KnowledgeAuditSystem",
	}
	if len(got) != len(want) {
		t.Fatalf("routes = %#v", got)
	}
	for key, name := range want {
		handlerName, ok := got[key]
		if !ok || !strings.Contains(handlerName, name) {
			t.Fatalf("%s handler = %q, want %s", key, handlerName, name)
		}
	}

	for _, path := range []string{
		"/api/v1/tenants/7/agent24/knowledge/audit",
		"/api/v1/system/admin/agent24/knowledge/audit",
	} {
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"success":true`) {
			t.Fatalf("%s: status %d body %s", path, w.Code, w.Body.String())
		}
	}
	if len(stub.calls) != 2 || stub.calls[0] != 7 || stub.calls[1] != 0 || stub.bad {
		t.Fatalf("List calls = %#v bad=%v, want [7 0]", stub.calls, stub.bad)
	}
}

// agent24KnowledgeAuditReadStub records List. bad is set when the query is not the agent24_knowledge read.
type agent24KnowledgeAuditReadStub struct {
	interfaces.AuditLogService
	calls []uint64
	bad   bool
}

func (s *agent24KnowledgeAuditReadStub) List(
	_ context.Context, tenantID uint64, q *interfaces.AuditLogQuery,
) ([]*types.AuditLog, error) {
	s.calls = append(s.calls, tenantID)
	if q == nil || q.ScopeType != "agent24_knowledge" || q.UnscopedOnly || q.ActorUserID != "" {
		s.bad = true
	}
	scope := ""
	if q != nil {
		scope = q.ScopeType
	}
	return []*types.AuditLog{{ID: 1, TenantID: tenantID, ScopeType: scope}}, nil
}
