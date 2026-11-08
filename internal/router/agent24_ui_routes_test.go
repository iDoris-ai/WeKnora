// Agent24 fork product patch A24-KB-34 (KB-C2-01c1, WeKnora#133, READONLY-v1 R2.8, UI-BRIDGE-v1 U3.4).

package router

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/agent24uisession"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/gin-gonic/gin"
)

func TestAgent24UIRoutesJSONMatchesTable(t *testing.T) {
	raw, err := os.ReadFile("../../docs/agent24/ui-routes.json")
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatal(err)
	}
	if _, ok := top["source"]; ok {
		t.Fatal("ui-routes.json must not carry source")
	}
	if _, ok := top["routes"]; !ok || len(top) != 1 {
		t.Fatalf("ui-routes.json keys = %v", keysOf(top))
	}
	var doc struct {
		Routes []struct {
			Method string `json:"method"`
			Path   string `json:"path"`
			Op     string `json:"op"`
		} `json:"routes"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Routes) != len(agent24uisession.UIReadRoutes) {
		t.Fatalf("file has %d routes, table has %d", len(doc.Routes), len(agent24uisession.UIReadRoutes))
	}
	for i, rt := range agent24uisession.UIReadRoutes {
		got := doc.Routes[i]
		if got.Method != rt.Method || got.Path != rt.Path || got.Op != rt.Op {
			t.Fatalf("file[%d] = %+v, table = %+v", i, got, rt)
		}
		if !agent24UIRouteShapeOK(rt.Path) {
			t.Fatalf("table path %s is not :param segments with *slug only at the end", rt.Path)
		}
		if _, ok := agent24IntegrationAllowlist[rt.Method+" "+rt.Path]; !ok {
			t.Fatalf("%s %s is not on the integration allowlist", rt.Method, rt.Path)
		}
	}

	gin.SetMode(gin.TestMode)
	agent24SetTagKey(t, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("LOCAL_STORAGE_BASE_DIR", t.TempDir())
	t.Setenv("AGENT24_KNOWLEDGE_MODE", "integration")
	engine := NewRouter(RouterParams{
		Config:                           &config.Config{Tenant: &config.TenantConfig{}},
		SystemHandler:                    &handler.SystemHandler{},
		Agent24KnowledgeUISessionHandler: &handler.Agent24KnowledgeUISessionHandler{},
		Agent24KnowledgeUIReadHandler:    &handler.Agent24KnowledgeUIReadHandler{},
	})
	var got []string
	for _, rt := range engine.Routes() {
		if rt.Method != http.MethodGet || !strings.HasPrefix(rt.Path, agent24uisession.UIReadPrefix+"/") {
			continue
		}
		if rt.Path == agent24uisession.UIReadPrefix+"/session" {
			continue
		}
		got = append(got, rt.Method+" "+rt.Path)
		if !agent24UIRouteShapeOK(rt.Path) {
			t.Fatalf("engine path %s is not :param segments with *slug only at the end", rt.Path)
		}
		if _, ok := agent24IntegrationAllowlist[rt.Method+" "+rt.Path]; !ok {
			t.Fatalf("engine route %s %s is not on the integration allowlist", rt.Method, rt.Path)
		}
	}
	var want []string
	for _, rt := range agent24uisession.UIReadRoutes {
		want = append(want, rt.Method+" "+rt.Path)
	}
	// A24-KB-36: gin walks children by priority, not registration order.
	// Both wiki routes sit under one node registered last, so that node's
	// priority rises above tags and documents and Routes() lists wiki first.
	// JSON already matches the table in order. Here the route sets must match.
	slices.Sort(got)
	slices.Sort(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("engine GET routes under the UI prefix (except session):\n%s\nwant:\n%s",
			strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestAgent24UIReadRoutesAbsentWhenHandlerNil(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("LOCAL_STORAGE_BASE_DIR", t.TempDir())
	t.Setenv("AGENT24_KNOWLEDGE_MODE", "standalone")
	engine := NewRouter(RouterParams{
		Config:        &config.Config{Tenant: &config.TenantConfig{}},
		SystemHandler: &handler.SystemHandler{},
	})
	seen := map[string]bool{}
	for _, rt := range engine.Routes() {
		seen[rt.Method+" "+rt.Path] = true
	}
	for _, rt := range agent24uisession.UIReadRoutes {
		if seen[rt.Method+" "+rt.Path] {
			t.Fatalf("standalone registered %s %s", rt.Method, rt.Path)
		}
		// Standalone has no integration gate, so an unregistered path is not a 404 from that gate.
		// Auth runs (FullPath is empty, so the UI read skip does not apply) and answers 401.
		// A registered read route would skip Auth and answer the contract envelope instead.
		w := httptest.NewRecorder()
		path := strings.ReplaceAll(rt.Path, ":kb_id", "kb1")
		path = strings.ReplaceAll(path, ":knowledge_id", "doc1") // A24-KB-35
		engine.ServeHTTP(w, httptest.NewRequest(rt.Method, path, nil))
		if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), "missing authentication") {
			t.Fatalf("standalone %s %s status %d body %s, want 401 missing authentication",
				rt.Method, path, w.Code, w.Body.String())
		}
	}
}

func agent24UIRouteShapeOK(path string) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for i, part := range parts {
		if part == "" {
			return false
		}
		if strings.HasPrefix(part, "*") {
			return i == len(parts)-1 && len(part) > 1 &&
				!strings.Contains(part[1:], "*") && !strings.Contains(part, ":")
		}
		if strings.Contains(part, "*") {
			return false
		}
		if strings.HasPrefix(part, ":") {
			if len(part) == 1 || strings.Contains(part[1:], ":") {
				return false
			}
			continue
		}
		if strings.Contains(part, ":") {
			return false
		}
	}
	return true
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
