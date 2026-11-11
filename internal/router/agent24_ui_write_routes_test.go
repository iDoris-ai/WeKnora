// Agent24 fork product patch A24-KB-50 (KB-SOP-02 S5b, WeKnora#146, PR-Daemon #168 N2).

package router

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/agent24uisession"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/types"
)

func agent24UIWriteRouter(t *testing.T, read, candidates bool) *gin.Engine {
	t.Helper()
	agent24SetTagKey(t, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	gin.SetMode(gin.TestMode)
	t.Setenv("LOCAL_STORAGE_BASE_DIR", t.TempDir())
	t.Setenv("AGENT24_KNOWLEDGE_MODE", "integration")
	tenantID := uint64(1)
	p := RouterParams{
		Config: &config.Config{Tenant: &config.TenantConfig{}}, SystemHandler: &handler.SystemHandler{},
		TenantAPIKeyService: agent24FakeKeySvc{key: &types.TenantAPIKey{
			ID: 2, ScopeType: types.APIKeyScopeTenant, TenantID: &tenantID, FullAccess: true,
		}},
		TenantService:                    agent24FakeTenantSvc{tenant: &types.Tenant{ID: tenantID}},
		UserService:                      agent24RejectTokenUserSvc{},
		Agent24KnowledgeUISessionHandler: &handler.Agent24KnowledgeUISessionHandler{},
	}
	if read {
		p.Agent24KnowledgeUIReadHandler = &handler.Agent24KnowledgeUIReadHandler{}
	}
	if candidates {
		p.Agent24KnowledgeCandidatesHandler = &handler.Agent24KnowledgeCandidatesHandler{}
	}
	return NewRouter(p)
}

func agent24UIWriteEngineSet(r *gin.Engine) map[string]bool {
	seen := map[string]bool{}
	for _, rt := range r.Routes() {
		seen[rt.Method+" "+rt.Path] = true
	}
	return seen
}

// The five write routes are registered, allowlisted and exactly the table, only with both the UI read handler
// (session key) and the candidate handler (integration mode); without either, none of them, and the gate's 404.
func TestAgent24UIWriteRoutesRegisteredExactly(t *testing.T) {
	for _, rt := range agent24uisession.UIWriteRoutes {
		_, ok := agent24IntegrationAllowlist[rt.Method+" "+rt.Path]
		require.True(t, ok, "%s %s is not on the integration allowlist", rt.Method, rt.Path)
	}
	on := agent24UIWriteRouter(t, true, true)
	seen := agent24UIWriteEngineSet(on)
	for _, rt := range agent24uisession.UIWriteRoutes {
		require.True(t, seen[rt.Method+" "+rt.Path], "%s %s not registered", rt.Method, rt.Path)
	}
	// nothing else under the write paths: no other method, no OPTIONS without origins
	writePaths := map[string]bool{}
	for _, rt := range agent24uisession.UIWriteRoutes {
		writePaths[rt.Path] = true
	}
	var under []string
	for _, rt := range on.Routes() {
		if writePaths[rt.Path] {
			under = append(under, rt.Method+" "+rt.Path)
		}
	}
	var want []string
	for _, rt := range agent24uisession.UIWriteRoutes {
		want = append(want, rt.Method+" "+rt.Path)
	}
	slices.Sort(under)
	slices.Sort(want)
	require.Equal(t, want, under)

	for _, tc := range []struct {
		name             string
		read, candidates bool
	}{{"no UI read handler", false, true}, {"no candidate handler", true, false}, {"neither", false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			off := agent24UIWriteRouter(t, tc.read, tc.candidates)
			seen := agent24UIWriteEngineSet(off)
			for _, rt := range agent24uisession.UIWriteRoutes {
				require.False(t, seen[rt.Method+" "+rt.Path], "%s %s registered", rt.Method, rt.Path)
				w := httptest.NewRecorder()
				off.ServeHTTP(w, httptest.NewRequest(rt.Method, agent24ConcretePath(rt.Path), strings.NewReader("{}")))
				require.Equal(t, http.StatusNotFound, w.Code, "%s %s: %s", rt.Method, rt.Path, w.Body.String())
				require.Empty(t, w.Body.String())
			}
		})
	}
}

// N2 through the production stack: with no credential at all each write route reaches WriteGuard (its
// credential_invalid, with no-store), not Auth (credential_missing). Another method on the same path is the gate's
// bare 404, and a session-style bearer on C's propose route is Auth's credential_invalid before the handler (U1.3).
func TestAgent24UIWriteRoutesReachGuard(t *testing.T) {
	r := agent24UIWriteRouter(t, true, true)
	for _, rt := range agent24uisession.UIWriteRoutes {
		path := agent24ConcretePath(rt.Path)
		var body *strings.Reader
		if rt.Method == http.MethodPost {
			body = strings.NewReader(`{"request_id":"r1"}`)
		} else {
			body = strings.NewReader("")
		}
		req := httptest.NewRequest(rt.Method, path, body)
		if rt.Method == http.MethodPost {
			req.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusUnauthorized, w.Code, "%s %s: %s", rt.Method, rt.Path, w.Body.String())
		agent24RequireKnowledgeEnvelope(t, w.Body.Bytes(), "unauthorized", "credential_invalid", false)
		require.Equal(t, "no-store", w.Header().Get("Cache-Control"), rt.Path)

		for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
			if m == rt.Method {
				continue
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(m, path, strings.NewReader("{}")))
			require.Equal(t, http.StatusNotFound, w.Code, "%s %s: %s", m, rt.Path, w.Body.String())
			require.Empty(t, w.Body.String(), "%s %s", m, rt.Path)
		}
		for _, extra := range []string{path + "/x", path + "/", strings.ToUpper(path)} {
			if rt.Path == agent24uisession.UIReadPrefix+"/candidates" && extra == path+"/x" {
				continue // GET /candidates/x is GET /candidates/:candidate_id, a write entry of its own
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(rt.Method, extra, strings.NewReader("{}")))
			require.Equal(t, http.StatusNotFound, w.Code, "%s %s: %s", rt.Method, extra, w.Body.String())
		}
	}

	for _, path := range []string{
		"/api/v1/agent24/knowledge/candidates",
		"/api/v1/agent24/knowledge/candidates/withdraw",
		"/api/v1/agent24/knowledge/candidates/get",
	} {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer session-style.jwt.value")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		require.Equal(t, http.StatusUnauthorized, w.Code, "%s: %s", path, w.Body.String())
		agent24RequireKnowledgeEnvelope(t, w.Body.Bytes(), "unauthorized", "credential_invalid", false)
	}
}
