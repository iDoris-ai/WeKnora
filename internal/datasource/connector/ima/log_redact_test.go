package ima

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/logger"
)

func TestClientLogOmitsAPIKey(t *testing.T) {
	fake := newFakeIMA(t)
	cfg := fake.config()
	cfg.Credentials["api_key"] = "S3NT1NEL-key"
	parsed, err := parseIMAConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	logger.SetOutput(&buf)
	logger.SetLogLevel(logger.LevelDebug)
	t.Cleanup(logger.ConfigureFromEnv)

	c := newClient(parsed)
	if _, err := c.GetAddableKnowledgeBaseList(context.Background(), "", 1); err != nil {
		t.Fatalf("request: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, "S3NT1NEL-key") {
		t.Fatalf("log contains api key:\n%s", out)
	}
	if !strings.Contains(out, "api_key_set=true") {
		t.Fatalf("log = %s, want api_key_set=true", out)
	}
}

// A configured base_url may carry userinfo; the client log keeps only scheme and host.
func TestClientLogOmitsBaseURLUserinfo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")

	var buf bytes.Buffer
	logger.SetOutput(&buf)
	logger.SetLogLevel(logger.LevelDebug)
	t.Cleanup(logger.ConfigureFromEnv)

	c := newClient(&Config{ClientID: "cid", APIKey: "cak", BaseURL: "http://user:S3NT1NEL@" + host})
	_, _ = c.GetAddableKnowledgeBaseList(context.Background(), "", 1)
	out := buf.String()
	if strings.Contains(out, "S3NT1NEL") {
		t.Fatalf("log contains base_url userinfo:\n%s", out)
	}
	if !strings.Contains(out, "base="+srv.URL) {
		t.Fatalf("log = %s, want base=%s", out, srv.URL)
	}
}
