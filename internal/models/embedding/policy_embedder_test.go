package embedding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/models/providers"
	"github.com/Tencent/WeKnora/internal/types"
)

type countEmbedder struct{ n int }

func (e *countEmbedder) Embed(context.Context, string) ([]float32, error) {
	e.n++
	return []float32{1}, nil
}

func (e *countEmbedder) BatchEmbed(context.Context, []string) ([][]float32, error) {
	e.n++
	return [][]float32{{1}}, nil
}

func (e *countEmbedder) BatchEmbedWithPool(ctx context.Context, model Embedder, texts []string) ([][]float32, error) {
	return model.BatchEmbed(ctx, texts)
}

func (e *countEmbedder) GetModelName() string { return "m" }
func (e *countEmbedder) GetDimensions() int   { return 1 }
func (e *countEmbedder) GetModelID() string   { return "id" }
func (e *countEmbedder) AcceptsImages() bool  { return true }
func (e *countEmbedder) ImageLimits() ImageLimits {
	return ImageLimits{}
}

func (e *countEmbedder) BatchEmbedImages(context.Context, []Image) ([][]float32, error) {
	e.n++
	return [][]float32{{1}}, nil
}

// passSelfEmbedder stands in for concurrencyEmbedder: it threads itself down as the pool model and counts the
// per-sub-batch BatchEmbed calls that come back through it.
type passSelfEmbedder struct {
	Embedder
	batches int
}

func (w *passSelfEmbedder) BatchEmbed(ctx context.Context, texts []string) ([][]float32, error) {
	w.batches++
	return w.Embedder.BatchEmbed(ctx, texts)
}

func (w *passSelfEmbedder) BatchEmbedWithPool(ctx context.Context, _ Embedder, texts []string) ([][]float32, error) {
	return w.Embedder.BatchEmbedWithPool(ctx, w, texts)
}

// The pool's sub-batches still come back through the decorator above (the concurrency gate) and then through
// the policy check: policyEmbedder passes the caller's model down instead of itself.
func TestPolicyEmbedderPoolKeepsOuterModel(t *testing.T) {
	dest := types.ProcessingDestination{Category: types.ProcessingEmbedding, Endpoint: "https://a.example:443"}
	inner := &countEmbedder{}
	outer := &passSelfEmbedder{Embedder: &policyEmbedder{inner: inner, dest: dest}}
	if _, err := outer.BatchEmbedWithPool(context.Background(), nil, []string{"q"}); err != nil {
		t.Fatal(err)
	}
	if outer.batches != 1 || inner.n != 1 {
		t.Fatalf("outer batches = %d, provider calls = %d; want 1, 1", outer.batches, inner.n)
	}
	ctx := types.WithProcessingGuard(context.Background(), denyGuard{})
	_, err := outer.BatchEmbedWithPool(ctx, nil, []string{"q"})
	if !errors.Is(err, types.ErrProcessingDenied) {
		t.Fatalf("err = %v, want ErrProcessingDenied", err)
	}
	if outer.batches != 2 || inner.n != 1 {
		t.Fatalf("outer batches = %d, provider calls = %d; want 2, 1", outer.batches, inner.n)
	}
}

type denyGuard struct{}

func (denyGuard) Allow(context.Context, types.ProcessingDestination) error {
	return fmt.Errorf("%w", types.ErrProcessingDenied)
}

func TestPolicyEmbedderGuard(t *testing.T) {
	dest := types.ProcessingDestination{
		Category: types.ProcessingEmbedding, Endpoint: "https://api.example.com:443", Model: "m",
	}
	entries := []struct {
		name string
		call func(*policyEmbedder, context.Context) error
	}{
		{"Embed", func(p *policyEmbedder, ctx context.Context) error {
			_, err := p.Embed(ctx, "q")
			return err
		}},
		{"BatchEmbed", func(p *policyEmbedder, ctx context.Context) error {
			_, err := p.BatchEmbed(ctx, []string{"q"})
			return err
		}},
		{"BatchEmbedImages", func(p *policyEmbedder, ctx context.Context) error {
			_, err := p.BatchEmbedImages(ctx, []Image{{MIMEType: "image/png", Data: []byte{1}}})
			return err
		}},
	}
	for _, entry := range entries {
		t.Run(entry.name, func(t *testing.T) {
			t.Run("no guard", func(t *testing.T) {
				inner := &countEmbedder{}
				p := &policyEmbedder{inner: inner, dest: dest}
				if err := entry.call(p, context.Background()); err != nil {
					t.Fatal(err)
				}
				if inner.n != 1 {
					t.Fatalf("calls = %d, want 1", inner.n)
				}
			})
			t.Run("denied", func(t *testing.T) {
				inner := &countEmbedder{}
				p := &policyEmbedder{inner: inner, dest: dest}
				ctx := types.WithProcessingGuard(context.Background(), denyGuard{})
				err := entry.call(p, ctx)
				if !errors.Is(err, types.ErrProcessingDenied) || inner.n != 0 {
					t.Fatalf("err = %v, calls = %d", err, inner.n)
				}
			})
			t.Run("destination error", func(t *testing.T) {
				// A guard that allows everything: only the destErr check can refuse.
				inner := &countEmbedder{}
				guard := &allowCountGuard{}
				p := &policyEmbedder{inner: inner, destErr: errors.New("unresolved")}
				ctx := types.WithProcessingGuard(context.Background(), guard)
				err := entry.call(p, ctx)
				if !errors.Is(err, types.ErrProcessingDenied) || inner.n != 0 || guard.n != 0 {
					t.Fatalf("err = %v, calls = %d, guard calls = %d", err, inner.n, guard.n)
				}
			})
		})
	}
}

type allowCountGuard struct{ n int }

func (g *allowCountGuard) Allow(context.Context, types.ProcessingDestination) error {
	g.n++
	return nil
}

// wireRecorder is an embeddings endpoint that records what actually arrived: the host dialed, the path and
// the model named in the body.
type wireRecorder struct {
	host, path, model string
}

func newWireRecorder(t *testing.T) (*httptest.Server, *wireRecorder) {
	t.Helper()
	rec := &wireRecorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		rec.host, rec.path, rec.model = r.Host, r.URL.Path, body.Model
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[1]}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

// The destination the policy judges is the one the request reaches: the host dialed and the model sent,
// including a remote_model_name rewrite and a vendor Endpoint hook (Azure's legacy deployments path).
func TestProcessingDestinationForConfigMatchesWire(t *testing.T) {
	allowLoopback(t)
	cases := []struct {
		name string
		cfg  func(base string) Config
		// model checks the recorded request against the destination's model.
		model func(rec *wireRecorder, model string) bool
	}{
		{"generic with remote_model_name", func(base string) Config {
			return Config{
				Source: types.ModelSourceRemote, Provider: "generic", ModelName: "row-name", BaseURL: base + "/v1",
				APIKey: "k", ExtraConfig: map[string]string{"remote_model_name": "wire-model"},
			}
		}, func(rec *wireRecorder, model string) bool { return model == "wire-model" && rec.model == model }},
		{"azure deployments hook", func(base string) Config {
			return Config{
				Source: types.ModelSourceRemote, Provider: providers.AzureOpenaiID, ModelName: "deploy-1",
				BaseURL: base, APIKey: "k", ExtraConfig: map[string]string{"api_version": "2024-02-01"},
			}
		}, func(rec *wireRecorder, model string) bool {
			return strings.Contains(rec.path, "/openai/deployments/"+model+"/")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, rec := newWireRecorder(t)
			cfg := tc.cfg(srv.URL)
			dest, err := ProcessingDestinationForConfig(cfg, nil)
			if err != nil {
				t.Fatal(err)
			}
			e, err := NewEmbedder(cfg, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := e.Embed(types.WithProcessingGuard(context.Background(), &allowCountGuard{}), "q"); err != nil {
				t.Fatalf("Embed: %v", err)
			}
			if got, err := types.NormalizeProcessingEndpoint("http://" + rec.host); err != nil || got != dest.Endpoint {
				t.Fatalf("dialed %q (%v), destination %q", rec.host, err, dest.Endpoint)
			}
			if !tc.model(rec, dest.Model) {
				t.Fatalf("request path %q model %q, destination model %q", rec.path, rec.model, dest.Model)
			}
		})
	}
}

func TestProcessingDestinationForConfigUsesProviderDefault(t *testing.T) {
	dest, err := ProcessingDestinationForConfig(Config{
		Source: types.ModelSourceRemote, Provider: "openai", ModelName: "text-embedding-3-small", APIKey: "k",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want, err := types.NormalizeProcessingEndpoint(providers.OpenaiBaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if dest.Endpoint != want || dest.Endpoint == "" {
		t.Fatalf("dest endpoint %q, want %q", dest.Endpoint, want)
	}
	if dest.Model != "text-embedding-3-small" {
		t.Fatalf("model %q", dest.Model)
	}
}

// remote_model_name changes the model the client sends; the destination names that model, not the row name.
func TestProcessingDestinationForConfigUsesRemoteModelName(t *testing.T) {
	dest, err := ProcessingDestinationForConfig(Config{
		Source: types.ModelSourceRemote, Provider: "openai", ModelName: "allowed-model", APIKey: "k",
		ExtraConfig: map[string]string{"remote_model_name": "denied-model"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if dest.Model != "denied-model" {
		t.Fatalf("model %q, want denied-model", dest.Model)
	}
}

// Computing the destination reaches no network: a host that does not resolve still yields a destination
// (building the embedder would fail its SSRF check, which resolves the host).
func TestProcessingDestinationForConfigNoLookup(t *testing.T) {
	dest, err := ProcessingDestinationForConfig(Config{
		Source: types.ModelSourceRemote, Provider: "generic", ModelName: "m",
		BaseURL: "https://agent24-unresolvable.invalid/v1", APIKey: "k",
	}, nil)
	if err != nil || dest.Endpoint != "https://agent24-unresolvable.invalid:443" {
		t.Fatalf("dest = %+v, err = %v", dest, err)
	}
}

// The local destination is the Ollama base URL and the model the Ollama embedder will use.
func TestProcessingDestinationForConfigLocalDefaults(t *testing.T) {
	t.Setenv("OLLAMA_BASE_URL", "http://ollama.internal:11434")
	dest, err := ProcessingDestinationForConfig(Config{Source: types.ModelSourceLocal}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if dest.Endpoint != "http://ollama.internal:11434" || dest.Model != defaultOllamaEmbeddingModel {
		t.Fatalf("dest = %+v", dest)
	}
}

// The URL actually sent is checked against the approved endpoint (PR #98 B1): a configured path that moves the
// request to another host, and a redirect to another origin, are refused before anything reaches that host,
// and the error quotes neither the host nor the path and query.
func TestPolicyEmbedderChecksTheURLActuallySent(t *testing.T) {
	allowLoopback(t)
	const sentinel = "S3NT1NEL"
	other := 0
	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		other++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[1]}]}`))
	}))
	t.Cleanup(denied.Close)
	deniedHost := strings.TrimPrefix(denied.URL, "http://")
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, denied.URL+"/"+sentinel+"?token="+sentinel, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirector.Close)
	allowed, _ := newWireRecorder(t)

	cfg := func(base, path string) Config {
		c := Config{Source: types.ModelSourceRemote, Provider: "generic", ModelName: "m", BaseURL: base, APIKey: "k"}
		if path != "" {
			c.Spec = &types.ModelSpecOverride{Compat: map[string]any{"path": path}}
		}
		return c
	}
	cases := []struct {
		name string
		cfg  Config
		// reaches is whether the config reaches the other host without a guard. The shared client already
		// refuses to replay a POST across origins, so only the configured path does.
		reaches bool
	}{
		{"configured path moves the host", cfg(allowed.URL, "@"+deniedHost+"/"+sentinel), true},
		{"redirect to another origin", cfg(redirector.URL, ""), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, err := NewEmbedder(tc.cfg, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			other = 0
			_, err = e.Embed(types.WithProcessingGuard(context.Background(), &allowCountGuard{}), "q")
			if !errors.Is(err, types.ErrProcessingDenied) || other != 0 {
				t.Fatalf("guarded: err = %v, requests to the other host = %d", err, other)
			}
			if msg := err.Error(); strings.Contains(msg, sentinel) || strings.Contains(msg, deniedHost) {
				t.Fatalf("error quotes the URL: %s", msg)
			}
			if !tc.reaches {
				return
			}
			// Control: without a guard the same config does reach the other host.
			if _, err := e.Embed(context.Background(), "q"); err != nil || other != 1 {
				t.Fatalf("standalone: err = %v, requests to the other host = %d", err, other)
			}
		})
	}
}

// A compat field named "model" would overwrite the judged model in the body, so no destination is computed.
func TestProcessingDestinationForConfigRefusesModelFieldOverride(t *testing.T) {
	for _, field := range []string{"input_type_field", "dimensions_field", "truncate_field"} {
		cfg := Config{
			Source: types.ModelSourceRemote, Provider: "generic", ModelName: "m",
			BaseURL: "https://api.example.com/v1", APIKey: "k",
			Spec: &types.ModelSpecOverride{Compat: map[string]any{field: "model"}},
		}
		if _, err := ProcessingDestinationForConfig(cfg, nil); err == nil {
			t.Fatalf("%s=model: destination computed", field)
		}
	}
	ok := Config{
		Source: types.ModelSourceRemote, Provider: "generic", ModelName: "m",
		BaseURL: "https://api.example.com/v1", APIKey: "k",
		Spec: &types.ModelSpecOverride{Compat: map[string]any{"input_type_field": "input_type"}},
	}
	if _, err := ProcessingDestinationForConfig(ok, nil); err != nil {
		t.Fatalf("input_type_field=input_type: %v", err)
	}
}
