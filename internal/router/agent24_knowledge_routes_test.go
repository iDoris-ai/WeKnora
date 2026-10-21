package router

import (
	"net/http"
	"testing"

	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/gin-gonic/gin"
)

func TestAgent24KnowledgeReadyRoutePolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	g := &rbacGuards{}
	engine := gin.New()
	RegisterAgent24KnowledgeRoutes(engine.Group("/api/v1"), &handler.Agent24KnowledgeReadyHandler{}, g)

	n := 0
	for _, rt := range engine.Routes() {
		if rt.Method == http.MethodPost && rt.Path == "/api/v1/agent24/knowledge/ready" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("POST /api/v1/agent24/knowledge/ready count = %d, routes = %#v", n, engine.Routes())
	}
	policy := mustLookupAPIKeyPolicy(t, g, http.MethodPost, "/api/v1/agent24/knowledge/ready")
	want := []types.APIKeyCapability{types.APIKeyCapabilityRetrieve}
	if len(policy.Capabilities) != len(want) || policy.Capabilities[0] != want[0] {
		t.Fatalf("capabilities = %#v, want %#v", policy.Capabilities, want)
	}
	if policy.RequireFullAccess {
		t.Fatal("RequireFullAccess = true, want false")
	}
	if policy.PlatformOnly {
		t.Fatal("PlatformOnly = true, want false")
	}
}

func TestAgent24KnowledgeReadyRouteNilHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	g := &rbacGuards{}
	engine := gin.New()
	RegisterAgent24KnowledgeRoutes(engine.Group("/api/v1"), nil, g)
	if len(engine.Routes()) != 0 {
		t.Fatalf("routes = %#v, want none", engine.Routes())
	}
	if g.apiKeyAuthorizer != nil {
		if _, ok := g.apiKeyAuthorizer.Lookup(http.MethodPost, "/api/v1/agent24/knowledge/ready"); ok {
			t.Fatal("nil handler registered the ready route policy")
		}
	}
}
