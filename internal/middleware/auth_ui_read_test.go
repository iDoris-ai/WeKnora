// Agent24 fork product patch A24-KB-34 (KB-C2-01c1, WeKnora#133, READONLY-v1 R2.8, UI-BRIDGE-v1 U3.4).

package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// UI read entries skip Auth by exact method and FullPath. Other GETs, and POST on the same paths, do not.
func TestAuthSkipsUIReadRoutesOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Auth(nil, rejectTokenUserService{}, nil, nil, nil))
	reached := map[string]int{}
	ok := func(c *gin.Context) {
		reached[c.Request.Method+" "+c.FullPath()]++
		c.Status(http.StatusNoContent)
	}
	r.GET("/api/v1/agent24/knowledge/ui/knowledge-bases", ok)
	r.GET("/api/v1/agent24/knowledge/ui/knowledge-bases/:kb_id", ok)
	r.GET("/api/v1/agent24/knowledge/ui/knowledge-bases/:kb_id/tags", ok)
	r.POST("/api/v1/agent24/knowledge/ui/knowledge-bases", ok)
	r.POST("/api/v1/agent24/knowledge/ui/knowledge-bases/:kb_id", ok)
	r.POST("/api/v1/agent24/knowledge/ui/knowledge-bases/:kb_id/tags", ok)
	r.GET("/api/v1/knowledge-bases", ok)
	r.POST("/api/v1/agent24/knowledge/search", ok)
	r.GET("/api/v1/agent24/knowledge/ready", ok)

	skip := []struct{ method, path, auth, key string }{
		{http.MethodGet, "/api/v1/agent24/knowledge/ui/knowledge-bases", "Bearer user-jwt", ""},
		{http.MethodGet, "/api/v1/agent24/knowledge/ui/knowledge-bases/kb1", "", "sk-live"},
		{http.MethodGet, "/api/v1/agent24/knowledge/ui/knowledge-bases/kb1/tags", "Bearer user-jwt", "sk-live"},
	}
	for _, tc := range skip {
		w := authUIReadDo(r, tc.method, tc.path, tc.auth, tc.key)
		if w.Code != http.StatusNoContent {
			t.Fatalf("%s %s status %d body %s, want 204 (Auth skipped)", tc.method, tc.path, w.Code, w.Body.String())
		}
	}
	keep := []struct{ method, path, auth, key, want string }{
		{http.MethodGet, "/api/v1/knowledge-bases", "Bearer user-jwt", "", "invalid or expired token"},
		{http.MethodGet, "/api/v1/knowledge-bases", "", "sk-live", "API key service is not configured"},
		{
			http.MethodPost, "/api/v1/agent24/knowledge/ui/knowledge-bases",
			"Bearer user-jwt", "", "invalid or expired token",
		},
		{
			http.MethodPost, "/api/v1/agent24/knowledge/ui/knowledge-bases/kb1",
			"", "sk-live", "API key service is not configured",
		},
		{http.MethodPost, "/api/v1/agent24/knowledge/ui/knowledge-bases/kb1/tags", "", "", "missing authentication"},
		// A failed bearer falls through to X-API-Key. Both headers therefore surface the API-key error.
		{
			http.MethodPost, "/api/v1/agent24/knowledge/search",
			"Bearer user-jwt", "sk-live", "API key service is not configured",
		},
		{http.MethodPost, "/api/v1/agent24/knowledge/search", "Bearer user-jwt", "", "invalid or expired token"},
		{http.MethodGet, "/api/v1/agent24/knowledge/ready", "", "", "missing authentication"},
	}
	for _, tc := range keep {
		before := reached[tc.method+" "+authUIReadFullPath(tc.path)]
		w := authUIReadDo(r, tc.method, tc.path, tc.auth, tc.key)
		if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), tc.want) {
			t.Fatalf("%s %s status %d body %s, want 401 with %q", tc.method, tc.path, w.Code, w.Body.String(), tc.want)
		}
		if reached[tc.method+" "+authUIReadFullPath(tc.path)] != before {
			t.Fatalf("%s %s reached the handler", tc.method, tc.path)
		}
	}
}

func authUIReadDo(r *gin.Engine, method, path, auth, key string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if key != "" {
		req.Header.Set("X-API-Key", key)
	}
	r.ServeHTTP(w, req)
	return w
}

// authUIReadFullPath is the gin template the handler records. POST uses the same template as GET.
func authUIReadFullPath(path string) string {
	switch {
	case path == "/api/v1/agent24/knowledge/ui/knowledge-bases":
		return path
	case strings.HasPrefix(path, "/api/v1/agent24/knowledge/ui/knowledge-bases/") && strings.HasSuffix(path, "/tags"):
		return "/api/v1/agent24/knowledge/ui/knowledge-bases/:kb_id/tags"
	case strings.HasPrefix(path, "/api/v1/agent24/knowledge/ui/knowledge-bases/"):
		return "/api/v1/agent24/knowledge/ui/knowledge-bases/:kb_id"
	default:
		return path
	}
}
