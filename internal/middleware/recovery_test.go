package middleware

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/gin-gonic/gin"
)

// A panic value can carry body text (A24-OBS-07): the client gets a fixed 500 message, and the log names
// the value's type and length but not the value.
func TestRecoveryOmitsPanicValue(t *testing.T) {
	gin.SetMode(gin.TestMode)
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	logger.SetLogLevel(logger.LevelDebug)
	t.Cleanup(logger.ConfigureFromEnv)

	r := gin.New()
	r.Use(Recovery())
	r.GET("/boom", func(*gin.Context) { panic(errors.New("S3NT1NEL")) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	if strings.Contains(w.Body.String(), "S3NT1NEL") {
		t.Fatalf("response contains the panic value: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"message":"Internal Server Error"`) {
		t.Fatalf("response = %s, want the fixed message", w.Body.String())
	}
	logText := buf.String()
	if strings.Contains(logText, "S3NT1NEL") {
		t.Fatalf("log contains the panic value:\n%s", logText)
	}
	if !strings.Contains(logText, "*errors.errorString (len=8)") {
		t.Fatalf("log missing the panic type and length:\n%s", logText)
	}
}
