// Agent24 fork product patch A24-KB-50 (KB-SOP-02 S5b, WeKnora#146, PR-Daemon #168 N2).

package agent24uisession

import (
	"net/http"
	"strings"
	"testing"
)

// IsUIWriteRoute is the open/closed decision for the UI write entries (#168 N2): exactly the five method+FullPath
// pairs of UIWriteRoutes, nothing near them.
func TestIsUIWriteRouteExact(t *testing.T) {
	if len(UIWriteRoutes) != 5 {
		t.Fatalf("UIWriteRoutes has %d entries, want 5", len(UIWriteRoutes))
	}
	for _, rt := range UIWriteRoutes {
		if !IsUIWriteRoute(rt.Method, rt.Path) {
			t.Fatalf("IsUIWriteRoute(%s, %s) = false", rt.Method, rt.Path)
		}
		got, ok := UIWriteRouteFor(rt.Method, rt.Path)
		if !ok || got != rt {
			t.Fatalf("UIWriteRouteFor(%s, %s) = %+v, %v; want %+v", rt.Method, rt.Path, got, ok, rt)
		}
		if IsUIReadRoute(rt.Method, rt.Path) {
			t.Fatalf("write route %s %s is a read route", rt.Method, rt.Path)
		}
	}
	for _, rt := range UIReadRoutes {
		if IsUIWriteRoute(rt.Method, rt.Path) {
			t.Fatalf("read route %s %s is a write route", rt.Method, rt.Path)
		}
	}
}

func TestIsUIWriteRouteRejectsNearMisses(t *testing.T) {
	var cases []struct{ method, path string }
	inTable := func(method, path string) bool {
		for _, rt := range UIWriteRoutes {
			if rt.Method == method && rt.Path == path {
				return true
			}
		}
		return false
	}
	add := func(method, path string) {
		if !inTable(method, path) { // GET /candidates/:candidate_id minus a segment is GET /candidates, a real entry
			cases = append(cases, struct{ method, path string }{method, path})
		}
	}
	for _, rt := range UIWriteRoutes {
		// another method on the same template
		for _, m := range []string{
			http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete,
			http.MethodPatch, http.MethodOptions, http.MethodHead,
		} {
			if m != rt.Method {
				add(m, rt.Path)
			}
		}
		add(rt.Method, rt.Path+"/extra")                          // one more segment
		add(rt.Method, rt.Path[:strings.LastIndex(rt.Path, "/")]) // one segment fewer
		add(rt.Method, strings.ToUpper(rt.Path))                  // case
		add(rt.Method, rt.Path+"/")                               // trailing slash
		add(rt.Method, strings.TrimPrefix(rt.Path, "/api/v1"))    // relative to the group
		add(strings.ToLower(rt.Method), rt.Path)                  // method case
		add(rt.Method, "")                                        // unmatched route (empty FullPath)
	}
	// prefixes of the write paths, and paths a prefix match would admit
	add(http.MethodGet, UIReadPrefix+"/candidates/:candidate_id/approve/x")
	add(http.MethodPost, UIReadPrefix+"/candidates")
	add(http.MethodPost, UIReadPrefix+"/candidates/:candidate_id")
	add(http.MethodPost, UIReadPrefix+"/candidates/:candidate_id/returns")
	add(http.MethodPost, UIReadPrefix+"/candidates/:candidate_id/approved")
	add(http.MethodPost, UIReadPrefix+"/documents/:knowledge_id")
	add(http.MethodPost, UIReadPrefix+"/documents/:knowledge_id/withdrawal")
	add(http.MethodPost, UIReadPrefix+"/documents/:id/withdraw") // other parameter name
	add(http.MethodGet, UIReadPrefix+"/candidates/:id")
	add(http.MethodPost, "/api/v1/agent24/knowledge/candidates/withdraw") // the C route, not a UI route
	add(http.MethodPost, UIReadPrefix)
	add(http.MethodPost, UIReadPrefix+"/")
	for _, tc := range cases {
		if IsUIWriteRoute(tc.method, tc.path) {
			t.Fatalf("IsUIWriteRoute(%q, %q) = true", tc.method, tc.path)
		}
		if _, ok := UIWriteRouteFor(tc.method, tc.path); ok {
			t.Fatalf("UIWriteRouteFor(%q, %q) matched", tc.method, tc.path)
		}
	}
}
