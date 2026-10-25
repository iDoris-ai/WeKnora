// Package ollama: Agent24 A24-KB-17 tests for the processing guard on model pulls.
package ollama

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

type pullGuard struct {
	allow bool
	dest  types.ProcessingDestination
}

func (g *pullGuard) Allow(_ context.Context, d types.ProcessingDestination) error {
	g.dest = d
	if g.allow {
		return nil
	}
	return fmt.Errorf("%w", types.ErrProcessingDenied)
}

// ollamaStub answers the calls PullModel makes and counts every request, whatever the path.
func ollamaStub(t *testing.T) (*httptest.Server, *int) {
	t.Helper()
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch r.URL.Path {
		case "/":
			w.WriteHeader(http.StatusOK)
		case "/api/tags":
			_, _ = w.Write([]byte(`{"models":[]}`))
		case "/api/pull":
			_, _ = w.Write([]byte(`{"status":"success"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OLLAMA_BASE_URL", srv.URL)
	t.Setenv("OLLAMA_OPTIONAL", "")
	return srv, &requests
}

func TestPullModelDeniedDoesNotRequest(t *testing.T) {
	srv, requests := ollamaStub(t)
	svc, err := GetOllamaService()
	if err != nil {
		t.Fatal(err)
	}
	*requests = 0 // construction may probe the service; count only what PullModel sends
	guard := &pullGuard{}
	err = svc.PullModel(types.WithProcessingGuard(context.Background(), guard), "embed-test")
	if !errors.Is(err, types.ErrProcessingDenied) {
		t.Fatalf("PullModel = %v", err)
	}
	if *requests != 0 {
		t.Fatalf("requests = %d, want 0", *requests)
	}
	want, err := types.NormalizeProcessingEndpoint(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if guard.dest.Category != types.ProcessingModelDownload || guard.dest.Endpoint != want ||
		guard.dest.Model != "embed-test" {
		t.Fatalf("dest = %+v, want %s embed-test", guard.dest, want)
	}
}

// The control: an allowing guard lets PullModel reach the server, so the zero above is not vacuous.
func TestPullModelAllowedRequests(t *testing.T) {
	_, requests := ollamaStub(t)
	svc, err := GetOllamaService()
	if err != nil {
		t.Fatal(err)
	}
	*requests = 0
	if err := svc.PullModel(types.WithProcessingGuard(context.Background(), &pullGuard{allow: true}), "m"); err != nil {
		t.Fatalf("PullModel = %v", err)
	}
	if *requests == 0 {
		t.Fatal("no request reached the server")
	}
}

// Through the production client (GetOllamaService): with a guard on the request, a redirect to another
// origin is refused and the other origin sees nothing; without a guard (standalone) it is followed as before.
func TestOllamaServiceRefusesCrossOriginRedirectUnderGuard(t *testing.T) {
	other := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		other++
		_, _ = w.Write([]byte(`{"models":[]}`))
	}))
	t.Cleanup(target.Close)
	allowed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The target path and query carry a sentinel: a refusal must not quote them (PR #98 B2).
		http.Redirect(w, r, target.URL+"/S3NT1NEL?token=S3NT1NEL", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(allowed.Close)
	t.Setenv("OLLAMA_BASE_URL", allowed.URL)
	t.Setenv("OLLAMA_OPTIONAL", "")
	svc, err := GetOllamaService()
	if err != nil {
		t.Fatal(err)
	}
	other = 0
	logger.SetLogLevel(logger.LevelDebug)
	var logs bytes.Buffer
	logger.SetOutput(&logs)
	t.Cleanup(logger.ConfigureFromEnv)
	guarded := types.WithProcessingGuard(context.Background(), &pullGuard{allow: true})
	_, err = svc.ListModels(guarded)
	if !errors.Is(err, types.ErrProcessingDenied) || other != 0 {
		t.Fatalf("guarded: err = %v, other origin requests = %d", err, other)
	}
	if strings.Contains(err.Error(), "S3NT1NEL") {
		t.Fatalf("refusal quotes the redirect target: %s", err.Error())
	}
	// StartService logs the heartbeat failure at WARN; the refusal must not put the URL there either.
	if err := svc.StartService(guarded); !errors.Is(err, types.ErrProcessingDenied) {
		t.Fatalf("heartbeat: err = %v", err)
	}
	if !strings.Contains(logs.String(), "ollama service unavailable") || strings.Contains(logs.String(), "S3NT1NEL") {
		t.Fatalf("heartbeat log: %s", logs.String())
	}
	if _, err := svc.ListModels(context.Background()); err != nil || other != 1 {
		t.Fatalf("standalone: err = %v, other origin requests = %d", err, other)
	}
}

// Same origin written two ways (default port spelled out or not) is followed; an IPv6 zone is refused.
func TestGuardedRedirectNormalizesOrigin(t *testing.T) {
	ctx := types.WithProcessingGuard(context.Background(), &pullGuard{allow: true})
	req := func(raw string) *http.Request {
		r, err := http.NewRequestWithContext(ctx, http.MethodPost, raw, nil)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if err := guardedRedirect(req("https://ollama.example/api/embed"),
		[]*http.Request{req("https://ollama.example:443/api/embed")}); err != nil {
		t.Fatalf("same origin refused: %v", err)
	}
	if err := guardedRedirect(req("http://[fe80::1%25ETH0]:11434/api/embed"),
		[]*http.Request{req("http://[fe80::1%25eth0]:11434/api/embed")}); !errors.Is(err, types.ErrProcessingDenied) {
		t.Fatalf("zone redirect: err = %v", err)
	}
}

// Under a guard an error that is not a refusal keeps its cause but not the URL net/http quotes: a same-origin
// redirect loop stops at the hop limit with the Location in a *url.Error (PR #98 review increment C1).
func TestOllamaServiceErrorsOmitURLUnderGuard(t *testing.T) {
	loop := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/S3NT1NEL?token=S3NT1NEL", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(loop.Close)
	t.Setenv("OLLAMA_BASE_URL", loop.URL)
	t.Setenv("OLLAMA_OPTIONAL", "")
	svc, err := GetOllamaService()
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.ListModels(types.WithProcessingGuard(context.Background(), &pullGuard{allow: true}))
	if err == nil || strings.Contains(err.Error(), "S3NT1NEL") {
		t.Fatalf("guarded: err = %v", err)
	}
	if !strings.Contains(err.Error(), "request failed") {
		t.Fatalf("guarded: err = %v", err)
	}
	if _, err := svc.ListModels(context.Background()); err == nil || !strings.Contains(err.Error(), "S3NT1NEL") {
		t.Fatalf("standalone keeps the error as it was: %v", err)
	}
}

// A reply body that echoes the request (an Ollama StatusError carries it) is not quoted under a guard either.
func TestOllamaServiceStatusErrorOmitsBodyUnderGuard(t *testing.T) {
	echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"unknown S3NT1NEL `+r.URL.Path+`"}`, http.StatusNotFound)
	}))
	t.Cleanup(echo.Close)
	t.Setenv("OLLAMA_BASE_URL", echo.URL)
	t.Setenv("OLLAMA_OPTIONAL", "")
	svc, err := GetOllamaService()
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.ListModels(types.WithProcessingGuard(context.Background(), &pullGuard{allow: true}))
	if err == nil || strings.Contains(err.Error(), "S3NT1NEL") {
		t.Fatalf("guarded: err = %v", err)
	}
	if _, err := svc.ListModels(context.Background()); err == nil || !strings.Contains(err.Error(), "S3NT1NEL") {
		t.Fatalf("standalone keeps the error as it was: %v", err)
	}
}
