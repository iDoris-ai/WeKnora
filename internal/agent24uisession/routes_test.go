// Agent24 fork product patch A24-KB-34 (KB-C2-01c1, WeKnora#133, READONLY-v1 R2.8, UI-BRIDGE-v1 U3.4).

package agent24uisession

import (
	"net/http"
	"slices"
	"testing"
)

func TestUIReadRoutes(t *testing.T) {
	if len(UIReadRoutes) == 0 {
		t.Fatal("UIReadRoutes is empty")
	}
	for i, rt := range UIReadRoutes {
		if rt.Method != http.MethodGet {
			t.Fatalf("UIReadRoutes[%d].Method = %s, want GET", i, rt.Method)
		}
		if len(rt.Path) <= len(UIReadPrefix)+1 || rt.Path[:len(UIReadPrefix)+1] != UIReadPrefix+"/" {
			t.Fatalf("UIReadRoutes[%d].Path = %s, want a path under %s/", i, rt.Path, UIReadPrefix)
		}
		if !slices.Contains(UIOps, rt.Op) {
			t.Fatalf("UIReadRoutes[%d].Op = %s is not in UIOps", i, rt.Op)
		}
		if !IsUIReadRoute(rt.Method, rt.Path) {
			t.Fatalf("IsUIReadRoute missed %s %s", rt.Method, rt.Path)
		}
		op, ok := UIRouteOp(rt.Method, rt.Path)
		if !ok || op != rt.Op {
			t.Fatalf("UIRouteOp(%s, %s) = %q, %v", rt.Method, rt.Path, op, ok)
		}
	}
	if IsUIReadRoute(http.MethodPost, UIReadRoutes[0].Path) {
		t.Fatal("POST matches a GET read route")
	}
	if IsUIReadRoute(http.MethodGet, UIReadPrefix) || IsUIReadRoute(http.MethodGet, UIReadPrefix+"/session") {
		t.Fatal("prefix or the session probe matched a read route")
	}
	if IsUIReadRoute(http.MethodGet, UIReadRoutes[0].Path+"/extra") {
		t.Fatal("a longer path matched")
	}
	want := []string{ // A24-KB-36
		"kb.list", "kb.get", "doc.list", "doc.get", "chunk.list", "wiki.list", "wiki.get", "tag.list",
	}
	if !slices.Equal(UIRouteOps(), want) {
		t.Fatalf("UIRouteOps = %v, want %v", UIRouteOps(), want)
	}
	for _, op := range UIRouteOps() {
		if !slices.Contains(UIOps, op) {
			t.Fatalf("UIRouteOps contains %s, which is not in UIOps", op)
		}
	}
}
