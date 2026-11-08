// Agent24 fork product patch A24-KB-41 (WeKnora#136): an Agent24 ingest's embedding calls run under the worker's
// embedding-only guard, so a redirect or a configured path away from the approved endpoint sends nothing.
package embedding

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

type agent24DenyGuard struct{}

func (agent24DenyGuard) Allow(context.Context, types.ProcessingDestination) error {
	return types.ErrProcessingDenied
}

func TestAgent24IngestEmbeddingGuardRefusesRedirectsAndOtherHosts(t *testing.T) {
	allowLoopback(t)
	other := 0
	var body strings.Builder
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		other++
		buf := make([]byte, 512)
		n, _ := r.Body.Read(buf)
		body.Write(buf[:n])
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[1]}]}`))
	}))
	t.Cleanup(elsewhere.Close)
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+"/v1/embeddings", http.StatusPermanentRedirect)
	}))
	t.Cleanup(redirector.Close)
	allowed, _ := newWireRecorder(t)
	elsewhereHost := strings.TrimPrefix(elsewhere.URL, "http://")

	cfg := func(base, path string) Config {
		c := Config{Source: types.ModelSourceRemote, Provider: "generic", ModelName: "m", BaseURL: base, APIKey: "k"}
		if path != "" {
			c.Spec = &types.ModelSpecOverride{Compat: map[string]any{"path": path}}
		}
		return c
	}
	const text = "AGENT24-INGEST-BODY"
	for _, tc := range []struct {
		name  string
		cfg   Config
		guard types.ProcessingGuard
	}{
		{"allowed endpoint answers 308 to another host", cfg(redirector.URL, ""), &allowCountGuard{}},
		{"configured path moves the host", cfg(allowed.URL, "@"+elsewhereHost+"/x"), &allowCountGuard{}},
		{"destination the policy does not list", cfg(allowed.URL, ""), agent24DenyGuard{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, err := NewEmbedder(tc.cfg, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			other = 0
			body.Reset()
			ctx := types.WithProcessingEmbeddingGuard(context.Background(), tc.guard)
			_, err = e.BatchEmbed(ctx, []string{text})
			if !errors.Is(err, types.ErrProcessingDenied) {
				t.Fatalf("err = %v, want ErrProcessingDenied", err)
			}
			if other != 0 || strings.Contains(body.String(), text) {
				t.Fatalf("the other host got %d requests, body sent: %v", other, strings.Contains(body.String(), text))
			}
			// upstream records "<what>: <err>"; the reconciler maps the denial text to policy_denied
			if !strings.Contains(err.Error(), types.ErrProcessingDenied.Error()) {
				t.Fatalf("error text %q does not carry the denial", err.Error())
			}
		})
	}

	// the same embedder without the embedding guard (an upstream document) is not affected
	e, err := NewEmbedder(cfg(allowed.URL, ""), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.BatchEmbed(context.Background(), []string{"q"}); err != nil {
		t.Fatalf("unguarded call: %v", err)
	}
}
