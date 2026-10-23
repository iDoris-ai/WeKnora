package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

func TestAgent24KnowledgeRoutePolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	g := &rbacGuards{}
	engine := gin.New()
	RegisterAgent24KnowledgeRoutes(engine.Group("/api/v1"), &handler.Agent24KnowledgeReadyHandler{},
		&handler.Agent24KnowledgeSearchHandler{}, g)

	for _, path := range []string{"/api/v1/agent24/knowledge/ready", "/api/v1/agent24/knowledge/search"} {
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
	if len(engine.Routes()) != 2 {
		t.Fatalf("routes = %#v, want exactly ready and search", engine.Routes())
	}
}

func TestAgent24KnowledgeRouteNilHandlers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ready, search := &handler.Agent24KnowledgeReadyHandler{}, &handler.Agent24KnowledgeSearchHandler{}
	for _, tc := range []struct {
		name  string
		ready *handler.Agent24KnowledgeReadyHandler
		srch  *handler.Agent24KnowledgeSearchHandler
		want  []string
	}{
		{"both nil", nil, nil, nil},
		{"ready only", ready, nil, []string{"/api/v1/agent24/knowledge/ready"}},
		{"search only", nil, search, []string{"/api/v1/agent24/knowledge/search"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := &rbacGuards{}
			engine := gin.New()
			RegisterAgent24KnowledgeRoutes(engine.Group("/api/v1"), tc.ready, tc.srch, g)
			var got []string
			for _, rt := range engine.Routes() {
				got = append(got, rt.Path)
			}
			if len(got) != len(tc.want) || (len(got) == 1 && got[0] != tc.want[0]) {
				t.Fatalf("routes = %v, want %v", got, tc.want)
			}
			for _, path := range []string{"/api/v1/agent24/knowledge/ready", "/api/v1/agent24/knowledge/search"} {
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
// without the revision key) on the search route, and not a ready request (invalid_request) on the ready route.
func TestAgent24KnowledgeRouteHandlers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	RegisterAgent24KnowledgeRoutes(engine.Group("/api/v1"), &handler.Agent24KnowledgeReadyHandler{},
		&handler.Agent24KnowledgeSearchHandler{}, &rbacGuards{})
	body := `{"protocol_version":1,"request_id":"r1","generation":7,"query":"q",` +
		`"scope":{"knowledge_base_ids":["a"]},"budget":{"deadline_ms":800,"max_results":5,"max_concurrency":1}}`
	for _, tc := range []struct {
		op, want string
		status   int
	}{
		{"search", `"reason":"operation_disabled"`, http.StatusForbidden},
		{"ready", `"code":"invalid_request"`, http.StatusBadRequest},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/agent24/knowledge/"+tc.op, strings.NewReader(body))
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.want) {
			t.Fatalf("%s: status %d body %s, want %d with %s", tc.op, w.Code, w.Body.String(), tc.status, tc.want)
		}
	}
}
