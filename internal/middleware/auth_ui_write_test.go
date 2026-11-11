// Agent24 fork product patch A24-KB-50 (KB-SOP-02 S5b, WeKnora#146, PR-Daemon #168 N2).

package middleware

import (
	"net/http"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// The five UI write entries skip Auth by exact method and FullPath (WriteGuard verifies the session). Every
// neighbour stays behind Auth: another method on the same template, one segment more, an unmatched path, and the C
// propose route called with a session-style bearer (U1.3: a session JWT does not open C's entries).
func TestAuthSkipsUIWriteRoutesOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Auth(nil, rejectTokenUserService{}, nil, nil, nil))
	reached := map[string]int{}
	ok := func(c *gin.Context) {
		reached[c.Request.Method+" "+c.FullPath()]++
		c.Status(http.StatusNoContent)
	}
	const p = "/api/v1/agent24/knowledge/ui"
	// the five
	r.GET(p+"/candidates", ok)
	r.GET(p+"/candidates/:candidate_id", ok)
	r.POST(p+"/candidates/:candidate_id/return", ok)
	r.POST(p+"/candidates/:candidate_id/approve", ok)
	r.POST(p+"/documents/:knowledge_id/withdraw", ok)
	// neighbours registered so that they match a route (non-empty FullPath) and only Auth can stop them
	r.POST(p+"/candidates", ok)
	r.POST(p+"/candidates/:candidate_id", ok)
	r.GET(p+"/candidates/:candidate_id/return", ok)
	r.GET(p+"/candidates/:candidate_id/approve", ok)
	r.PUT(p+"/candidates/:candidate_id/approve", ok)
	r.DELETE(p+"/candidates/:candidate_id/approve", ok)
	r.GET(p+"/documents/:knowledge_id/withdraw", ok)
	r.POST(p+"/candidates/:candidate_id/approve/x", ok)
	r.POST(p+"/documents/:knowledge_id/withdraw/x", ok)
	r.POST("/api/v1/agent24/knowledge/candidates", ok)
	r.POST("/api/v1/agent24/knowledge/candidates/withdraw", ok)

	skip := []struct{ method, path, auth, key string }{
		{http.MethodGet, p + "/candidates", "Bearer session-jwt", ""},
		{http.MethodGet, p + "/candidates/cand-1", "", "sk-live"},
		{http.MethodPost, p + "/candidates/cand-1/return", "Bearer session-jwt", ""},
		{http.MethodPost, p + "/candidates/cand-1/approve", "", ""},
		{http.MethodPost, p + "/documents/doc-1/withdraw", "Bearer session-jwt", "sk-live"},
	}
	for _, tc := range skip {
		w := authUIReadDo(r, tc.method, tc.path, tc.auth, tc.key)
		if w.Code != http.StatusNoContent {
			t.Fatalf("%s %s status %d body %s, want 204 (Auth skipped)", tc.method, tc.path, w.Code, w.Body.String())
		}
	}

	keep := []struct{ method, path, template, auth, key, want string }{
		{http.MethodPost, p + "/candidates", p + "/candidates", "Bearer session-jwt", "", "invalid or expired token"},
		{
			http.MethodPost, p + "/candidates/cand-1", p + "/candidates/:candidate_id",
			"", "sk-live", "API key service is not configured",
		},
		{
			http.MethodGet, p + "/candidates/cand-1/return", p + "/candidates/:candidate_id/return",
			"", "", "missing authentication",
		},
		{
			http.MethodGet, p + "/candidates/cand-1/approve", p + "/candidates/:candidate_id/approve",
			"Bearer session-jwt", "", "invalid or expired token",
		},
		{
			http.MethodPut, p + "/candidates/cand-1/approve", p + "/candidates/:candidate_id/approve",
			"", "", "missing authentication",
		},
		{
			http.MethodDelete, p + "/candidates/cand-1/approve", p + "/candidates/:candidate_id/approve",
			"", "", "missing authentication",
		},
		{
			http.MethodGet, p + "/documents/doc-1/withdraw", p + "/documents/:knowledge_id/withdraw",
			"", "sk-live", "API key service is not configured",
		},
		{
			http.MethodPost, p + "/candidates/cand-1/approve/x", p + "/candidates/:candidate_id/approve/x",
			"Bearer session-jwt", "", "invalid or expired token",
		},
		{
			http.MethodPost, p + "/documents/doc-1/withdraw/x", p + "/documents/:knowledge_id/withdraw/x",
			"", "", "missing authentication",
		},
		// U1.3, the second direction: a session JWT on C's entries is a failed user token.
		{
			http.MethodPost, "/api/v1/agent24/knowledge/candidates", "/api/v1/agent24/knowledge/candidates",
			"Bearer session-jwt", "", "invalid or expired token",
		},
		{
			http.MethodPost, "/api/v1/agent24/knowledge/candidates/withdraw",
			"/api/v1/agent24/knowledge/candidates/withdraw", "Bearer session-jwt", "", "invalid or expired token",
		},
		// unmatched paths (empty FullPath): case and a missing segment
		{http.MethodPost, strings.ToUpper(p) + "/candidates/cand-1/approve", "", "", "", "missing authentication"},
		{http.MethodPost, p + "/documents/withdraw", "", "", "", "missing authentication"},
	}
	for _, tc := range keep {
		before := reached[tc.method+" "+tc.template]
		w := authUIReadDo(r, tc.method, tc.path, tc.auth, tc.key)
		if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), tc.want) {
			t.Fatalf("%s %s status %d body %s, want 401 with %q", tc.method, tc.path, w.Code, w.Body.String(), tc.want)
		}
		if tc.template != "" && reached[tc.method+" "+tc.template] != before {
			t.Fatalf("%s %s reached the handler", tc.method, tc.path)
		}
	}
}
