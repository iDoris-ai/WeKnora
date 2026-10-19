package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

func TestAgent24IntegrationGate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	applyAgent24IntegrationGate(r)
	ok := func(c *gin.Context) { c.Status(http.StatusOK) }
	for _, rt := range [][2]string{
		{"GET", "/health"},
		{"GET", "/api/v1/system/capabilities"},
		{"POST", "/api/v1/agent24/knowledge/ready"},
		{"POST", "/api/v1/auth/register"},
		{"POST", "/api/v1/auth/login"},
		{"GET", "/swagger/*any"},
		{"GET", "/embed/:channelId"},
		{"POST", "/mcp/:endpoint_id"},
		{"GET", "/files"},
		{"GET", "/api/v1/knowledge-bases/:id"},
		{"GET", "/api/v1/system/info"},
	} {
		r.Handle(rt[0], rt[1], ok)
	}
	cases := []struct {
		method, path string
		want         int
	}{
		{"GET", "/health", 200},
		{"GET", "/api/v1/system/capabilities", 200},
		{"POST", "/api/v1/agent24/knowledge/ready", 200},
		{"POST", "/api/v1/auth/register", 404},
		{"POST", "/api/v1/auth/login", 404},
		{"GET", "/swagger/index.html", 404},
		{"GET", "/embed/ch1", 404},
		{"POST", "/mcp/ep1", 404},
		{"GET", "/files", 404},
		{"GET", "/api/v1/knowledge-bases/kb1", 404},
		{"GET", "/api/v1/system/info", 404},
		{"POST", "/health", 404},
		{"GET", "/index.html", 404},
		// Trailing-slash variants must not be redirected around the gate.
		{"POST", "/api/v1/auth/login/", 404},
		{"GET", "/api/v1/knowledge-bases/kb1/", 404},
		{"GET", "/files/", 404},
		{"GET", "/health/", 404},
		{"GET", "/api/v1/system/capabilities/", 404},
	}
	for _, hdr := range [][2]string{{"Authorization", "Bearer x"}, {"X-API-Key", "sk-x"}} {
		for _, tc := range cases {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.Header.Set(hdr[0], hdr[1])
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Errorf("%s %s with %s: got %d, want %d", tc.method, tc.path, hdr[0], w.Code, tc.want)
			}
			if tc.want == 404 && (w.Body.Len() != 0 || w.Header().Get("Location") != "") {
				t.Errorf("%s %s with %s: 404 must have empty body and no Location, got %q %q",
					tc.method, tc.path, hdr[0], w.Body.String(), w.Header().Get("Location"))
			}
		}
	}
}

func TestAgent24IntegrationGatePrecedesCORS(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	applyAgent24IntegrationGate(r)
	r.Use(cors.New(cors.Config{
		AllowOrigins: []string{"*"},
		AllowMethods: []string{"GET", "POST", "OPTIONS"},
		AllowHeaders: []string{"Content-Type", "Authorization", "X-API-Key"},
	}))
	ok := func(c *gin.Context) { c.Status(http.StatusOK) }
	r.POST("/api/v1/auth/register", ok)
	r.GET("/health", ok)

	req := httptest.NewRequest(http.MethodOptions, "/api/v1/auth/register", nil)
	req.Header.Set("Origin", "https://example.org")
	req.Header.Set("Access-Control-Request-Method", "POST")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Errorf("preflight OPTIONS /api/v1/auth/register: got %d, want 404", w.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/health", nil)
	req.Header.Set("Origin", "https://example.org")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Header().Get("Access-Control-Allow-Origin") == "" {
		t.Errorf("GET /health: got %d, ACAO %q; want 200 with CORS header",
			w.Code, w.Header().Get("Access-Control-Allow-Origin"))
	}
}
