package web_search

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/logger"
)

func TestTavilySearchLogOmitsQueryAndResponseBody(t *testing.T) {
	const sentinel = "S3NT1NEL"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"S3NT1NEL"}`))
	}))
	defer srv.Close()

	var buf bytes.Buffer
	logger.SetOutput(&buf)
	logger.SetLogLevel(logger.LevelDebug)
	t.Cleanup(logger.ConfigureFromEnv)

	p := &TavilyProvider{client: srv.Client(), baseURL: srv.URL, apiKey: "test-key"}
	_, err := p.Search(context.Background(), "q-"+sentinel, 3, false)
	if err == nil {
		t.Fatal("expected an error for status 500")
	}
	logs := buf.String()
	if strings.Contains(logs, sentinel) {
		t.Fatalf("log contains sentinel: %s", logs)
	}
	if !strings.Contains(logs, "query_len=") || !strings.Contains(logs, "body_bytes=") {
		t.Fatalf("log missing length fields: %s", logs)
	}
}

func TestBingSearchFailureLogOmitsResponseBody(t *testing.T) {
	const sentinel = "S3NT1NEL"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"S3NT1NEL"}`))
	}))
	defer srv.Close()

	var buf bytes.Buffer
	logger.SetOutput(&buf)
	logger.SetLogLevel(logger.LevelDebug)
	t.Cleanup(logger.ConfigureFromEnv)

	p := &BingProvider{client: srv.Client(), baseURL: srv.URL, apiKey: "test-key"}
	_, err := p.Search(context.Background(), "q-"+sentinel, 3, false)
	if err == nil || !strings.Contains(err.Error(), sentinel) {
		t.Fatalf("expected the returned error to keep the body, got %v", err)
	}
	logs := buf.String()
	if strings.Contains(logs, sentinel) {
		t.Fatalf("log contains sentinel: %s", logs)
	}
	if !strings.Contains(logs, "[WebSearch][Bing] failed: error_len=") {
		t.Fatalf("log missing the failure line: %s", logs)
	}
}
