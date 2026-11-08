// Agent24 fork product patch A24-KB-33 (KB-C2-01b2, WeKnora#133, CONTRACT-UI-BRIDGE-v1 U2.5).

package middleware

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/logger"
)

// With body logging on, UI session bodies are still neither read nor buffered: the exchange response
// carries the session JWT under session_token, and a request can spell "ticket" with JSON escapes.
func TestAgent24UISessionBodiesNeverLogged(t *testing.T) {
	t.Setenv(agent24LogHTTPBodyEnv, "true")
	logger.SetLogLevel(logger.LevelDebug)
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	t.Cleanup(logger.ConfigureFromEnv)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestID(), Logger())
	var got string
	r.POST("/api/v1/agent24/knowledge/ui/exchange", func(c *gin.Context) {
		body, _ := io.ReadAll(c.Request.Body)
		got = string(body)
		c.JSON(http.StatusOK, gin.H{"session_token": "S3NT1NEL-jwt"})
	})
	r.POST("/api/v1/x", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"note": "visible-note"}) })

	payload := `{"ticket":"S3NT1NEL-ticket"}`
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent24/knowledge/ui/exchange", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "S3NT1NEL-jwt") || got != payload {
		t.Fatalf("handler did not see the body or write the response: body=%q got=%q", rec.Body.String(), got)
	}
	other := httptest.NewRequest(http.MethodPost, "/api/v1/x", strings.NewReader(`{}`))
	other.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(httptest.NewRecorder(), other)

	logText := buf.String()
	if !strings.Contains(logText, "visible-note") {
		t.Fatalf("body logging is not on for other paths, the test proves nothing:\n%s", logText)
	}
	if strings.Contains(logText, "S3NT1NEL") {
		t.Fatalf("UI session body reached the access log:\n%s", logText)
	}
}
