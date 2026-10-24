package langfuse

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	secutils "github.com/Tencent/WeKnora/internal/utils"
	"github.com/gin-gonic/gin"
)

func TestGinMiddlewareRedactsHTTPQuery(t *testing.T) {
	_, exp := newTestManager(t)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(GinMiddleware())
	r.GET("/api/v1/knowledge-search", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	rawQuery := "q=S3NT1NEL-query&sig=S3NT1NEL-sig"
	req := httptest.NewRequest(http.MethodGet, "/api/v1/knowledge-search?"+rawQuery, nil)
	r.ServeHTTP(httptest.NewRecorder(), req)

	want := secutils.RedactQueryValues(rawQuery)
	found := false
	for _, span := range exp.GetSpans() {
		for _, kv := range span.Attributes {
			if string(kv.Key) != attrTraceMetadata {
				continue
			}
			raw, _ := kv.Value.AsInterface().(string)
			if raw == "" {
				continue
			}
			var meta map[string]any
			if err := json.Unmarshal([]byte(raw), &meta); err != nil {
				t.Fatalf("trace metadata: %v (%s)", err, raw)
			}
			got, _ := meta["http.query"].(string)
			found = true
			if got != want || strings.Contains(raw, "S3NT1NEL") {
				t.Fatalf("http.query = %q, want %q (raw %s)", got, want, raw)
			}
		}
	}
	if !found {
		t.Fatal("GinMiddleware did not export http.query metadata")
	}
}
