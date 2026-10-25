package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

func TestAgent24ProcessingPolicyInvalid(t *testing.T) {
	cases := []struct {
		name, text, file string
	}{
		{"both", `{"version":"v"}`, "also-set"},
		{"trailing bracket", `{"version":"v"}]`, ""},
		{"trailing brace", `{"version":"v"}}`, ""},
		{"second object", `{"version":"v"} {"version":"w"}`, ""},
		{"unknown field", `{"version":"v","extra":1}`, ""},
		{"empty version", `{"version":""}`, ""},
		{
			"unknown category",
			`{"version":"v","budget":{"max_external_calls":1,"max_input_bytes":1,"max_inflight_external":1},` +
				`"destinations":[{"category":"retrieval","endpoint":"https://a.example"}]}`,
			"",
		},
		{
			"bad endpoint",
			`{"version":"v","budget":{"max_external_calls":1,"max_input_bytes":1,"max_inflight_external":1},` +
				`"destinations":[{"category":"embedding","endpoint":"S3NT1NEL"}]}`,
			"",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(Agent24KnowledgePolicyEnv, tc.text)
			t.Setenv(Agent24KnowledgePolicyFileEnv, tc.file)
			ResetAgent24ProcessingPolicyForTest()
			t.Cleanup(ResetAgent24ProcessingPolicyForTest)
			_, err := agent24ProcessingPolicyFromEnv()
			if err == nil {
				t.Fatal("expected error")
			}
			if strings.Contains(err.Error(), "S3NT1NEL") {
				t.Fatalf("error text includes the policy: %s", err.Error())
			}
		})
	}
}

func TestAgent24ProcessingPolicyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "policy.json")
	raw := []byte(`{"version":"v1-file","destinations":[{"category":"embedding",` +
		`"endpoint":"https://api.example.com/v1","model":"m"}],` +
		`"budget":{"max_external_calls":4,"max_input_bytes":65536,"max_inflight_external":8}}`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(Agent24KnowledgePolicyEnv, "")
	t.Setenv(Agent24KnowledgePolicyFileEnv, path)
	ResetAgent24ProcessingPolicyForTest()
	t.Cleanup(ResetAgent24ProcessingPolicyForTest)
	p, err := agent24ProcessingPolicyFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if p.Version != "v1-file" {
		t.Fatalf("version %q", p.Version)
	}
	err = p.Allow(context.Background(), types.ProcessingDestination{
		Category: types.ProcessingEmbedding, Endpoint: "https://api.example.com:443", Model: "m",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestAgent24DefaultProcessingPolicyDenies(t *testing.T) {
	t.Setenv(Agent24KnowledgePolicyEnv, "")
	t.Setenv(Agent24KnowledgePolicyFileEnv, "")
	ResetAgent24ProcessingPolicyForTest()
	t.Cleanup(ResetAgent24ProcessingPolicyForTest)
	p, err := agent24ProcessingPolicyFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if p.Version != "agent24-default-deny" || len(p.Destinations) != 0 {
		t.Fatalf("default policy %+v", p)
	}
	err = p.Allow(context.Background(), types.ProcessingDestination{
		Category: types.ProcessingEmbedding, Endpoint: "https://api.example.com:443", Model: "m",
	})
	if !errors.Is(err, types.ErrProcessingDenied) {
		t.Fatalf("default policy Allow = %v", err)
	}
}

func TestAgent24ProcessingPolicyAllow(t *testing.T) {
	raw := []byte(`{"version":"v1","destinations":[
		{"category":"embedding","endpoint":"HTTPS://API.Example.COM/v1"},
		{"category":"embedding","endpoint":"https://other.example/x","model":"text-embedding-3-small"},
		{"category":"model_download","endpoint":"http://localhost:11434","model":"nomic"}
	],"budget":{"max_external_calls":4,"max_input_bytes":65536,"max_inflight_external":8}}`)
	p, err := agent24ParseProcessingPolicy(raw)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		dest types.ProcessingDestination
		ok   bool
	}{
		{"empty model matches any", types.ProcessingDestination{
			Category: types.ProcessingEmbedding, Endpoint: "https://api.example.com:443", Model: "anything",
		}, true},
		{"equal model", types.ProcessingDestination{
			Category: types.ProcessingEmbedding, Endpoint: "https://other.example:443",
			Model: "text-embedding-3-small",
		}, true},
		{"different model", types.ProcessingDestination{
			Category: types.ProcessingEmbedding, Endpoint: "https://other.example:443", Model: "other",
		}, false},
		{"download", types.ProcessingDestination{
			Category: types.ProcessingModelDownload, Endpoint: "http://localhost:11434", Model: "nomic",
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := p.Allow(context.Background(), tc.dest)
			if tc.ok {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, types.ErrProcessingDenied) {
				t.Fatalf("Allow = %v, want denial", err)
			}
			if strings.Contains(err.Error(), tc.dest.Endpoint) || strings.Contains(err.Error(), tc.dest.Model) {
				t.Fatalf("denial error leaks destination: %s", err.Error())
			}
		})
	}
}

func TestValidateAgent24IntegrationConfigRejectsInvalidPolicy(t *testing.T) {
	tr := true
	cfg := &config.Config{Tenant: &config.TenantConfig{EnableRBAC: &tr}}
	t.Setenv(Agent24KnowledgeTagKeyEnv, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv(Agent24KnowledgePolicyEnv,
		`{"version":"v","destinations":[{"category":"embedding","endpoint":"S3NT1NEL"}]}`)
	t.Setenv(Agent24KnowledgePolicyFileEnv, "")
	ResetAgent24ProcessingPolicyForTest()
	t.Cleanup(ResetAgent24ProcessingPolicyForTest)
	err := ValidateAgent24IntegrationConfig(cfg)
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "S3NT1NEL") {
		t.Fatalf("error text includes the policy: %s", err.Error())
	}
}

// The pre-check names the KB whose embedding model HybridSearch calls: the primary when it embeds, otherwise
// the first KB that does (knowledgebase_search.go), and none when no KB embeds.
func TestAgent24QueryEmbeddingKBMatchesHybridSearch(t *testing.T) {
	vec := func(id, model string) *types.KnowledgeBase {
		kb := &types.KnowledgeBase{ID: id, EmbeddingModelID: model}
		kb.IndexingStrategy.VectorEnabled = true
		return kb
	}
	keywordOnly := &types.KnowledgeBase{ID: "kw", EmbeddingModelID: "m-kw"}
	if got := agent24QueryEmbeddingKB([]*types.KnowledgeBase{vec("a", "m1"), vec("b", "m2")}); got.ID != "a" {
		t.Fatalf("primary embeds: got %s", got.ID)
	}
	fallback := []*types.KnowledgeBase{keywordOnly, vec("b", ""), vec("c", "m3")}
	if got := agent24QueryEmbeddingKB(fallback); got.ID != "c" {
		t.Fatalf("fallback: got %s", got.ID)
	}
	if got := agent24QueryEmbeddingKB([]*types.KnowledgeBase{keywordOnly}); got != nil {
		t.Fatalf("none embeds: got %s", got.ID)
	}
}

// Integration mode with a policy the handler cannot read still guards: it denies every destination rather than
// running without a policy (startup validation normally stops the process first).
func TestAgent24SearchHandlerInvalidPolicyDeniesAll(t *testing.T) {
	agent24SetTagKey(t, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv(Agent24KnowledgeModeEnv, "integration")
	t.Setenv(Agent24KnowledgePolicyEnv, `{"version":`)
	t.Setenv(Agent24KnowledgePolicyFileEnv, "")
	ResetAgent24ProcessingPolicyForTest()
	t.Cleanup(ResetAgent24ProcessingPolicyForTest)
	h := NewAgent24KnowledgeSearchHandler(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	if h.policy == nil {
		t.Fatal("policy is nil in integration mode")
	}
	err := h.policy.Allow(context.Background(), types.ProcessingDestination{
		Category: types.ProcessingEmbedding, Endpoint: "https://api.example.com:443",
	})
	if !errors.Is(err, types.ErrProcessingDenied) {
		t.Fatalf("err = %v, want ErrProcessingDenied", err)
	}
}

type agent24ModelRows struct {
	interfaces.ModelRepository
	rows   map[string]*types.Model
	tenant []uint64
}

func (r *agent24ModelRows) GetByID(_ context.Context, tenantID uint64, id string) (*types.Model, error) {
	r.tenant = append(r.tenant, tenantID)
	return r.rows[id], nil
}

// The default pre-check reads the model row under the KB's tenant and computes the destination from it,
// without building the embedder; an inactive row is an error, not a pass.
func TestAgent24ModelEmbeddingDestinations(t *testing.T) {
	model := &types.Model{
		ID: "m1", Name: "text-embedding-3-small", Source: types.ModelSourceRemote, Status: types.ModelStatusActive,
	}
	model.Parameters.Provider = "openai"
	model.Parameters.BaseURL = "https://agent24-unresolvable.invalid/v1"
	rows := &agent24ModelRows{rows: map[string]*types.Model{"m1": model}}
	kb := &types.KnowledgeBase{ID: "kb", TenantID: 42, EmbeddingModelID: "m1"}
	kb.IndexingStrategy.VectorEnabled = true
	d := agent24ModelEmbeddingDestinations{models: rows}
	dest, ok, err := d.QueryEmbedding(context.Background(), []*types.KnowledgeBase{kb})
	if err != nil || !ok {
		t.Fatalf("ok = %v, err = %v", ok, err)
	}
	if dest.Endpoint != "https://agent24-unresolvable.invalid:443" || dest.Model != "text-embedding-3-small" {
		t.Fatalf("dest = %+v", dest)
	}
	if len(rows.tenant) != 1 || rows.tenant[0] != 42 {
		t.Fatalf("row read under tenants %v, want [42]", rows.tenant)
	}
	model.Status = types.ModelStatusDownloading
	if _, _, err := d.QueryEmbedding(context.Background(), []*types.KnowledgeBase{kb}); err == nil {
		t.Fatal("inactive model passed the pre-check")
	}
}

func TestAgent24ProcessingPolicyBudget(t *testing.T) {
	invalid := []struct{ name, raw string }{
		{"missing budget", `{"version":"v"}`},
		{"null budget", `{"version":"v","budget":null}`},
		{"missing calls", `{"version":"v","budget":{"max_input_bytes":1,"max_inflight_external":1}}`},
		{"missing bytes", `{"version":"v","budget":{"max_external_calls":1,"max_inflight_external":1}}`},
		{"missing inflight", `{"version":"v","budget":{"max_external_calls":1,"max_input_bytes":1}}`},
		{
			"negative calls",
			`{"version":"v","budget":{"max_external_calls":-1,"max_input_bytes":1,"max_inflight_external":1}}`,
		},
		{
			"negative bytes",
			`{"version":"v","budget":{"max_external_calls":1,"max_input_bytes":-1,"max_inflight_external":1}}`,
		},
		{
			"inflight zero",
			`{"version":"v","budget":{"max_external_calls":0,"max_input_bytes":0,"max_inflight_external":0}}`,
		},
		{
			"calls over max",
			`{"version":"v","budget":{"max_external_calls":2147483648,"max_input_bytes":1,"max_inflight_external":1}}`,
		},
		{
			"bytes over max",
			`{"version":"v","budget":{"max_external_calls":1,"max_input_bytes":2147483648,"max_inflight_external":1}}`,
		},
		{
			"inflight over max",
			`{"version":"v","budget":{"max_external_calls":1,"max_input_bytes":1,"max_inflight_external":2147483648}}`,
		},
		{
			"unknown field",
			`{"version":"v","budget":{"max_external_calls":1,"max_input_bytes":1,` +
				`"max_inflight_external":1,"extra":"S3NT1NEL"}}`,
		},
		{
			"sentinel with a bad budget",
			`{"version":"v","destinations":[{"category":"embedding","endpoint":"https://s3nt1nel.example",` +
				`"model":"S3NT1NEL"}],"budget":{"max_external_calls":-1,"max_input_bytes":1,` +
				`"max_inflight_external":1}}`,
		},
	}
	for _, tc := range invalid {
		t.Run(tc.name, func(t *testing.T) {
			_, err := agent24ParseProcessingPolicy([]byte(tc.raw))
			if !errors.Is(err, errAgent24ProcessingPolicyInvalid) {
				t.Fatalf("err = %v", err)
			}
			if strings.Contains(err.Error(), "S3NT1NEL") || strings.Contains(err.Error(), "s3nt1nel") {
				t.Fatalf("error text includes the policy: %s", err.Error())
			}
		})
	}

	raw := `{"version":"v1","destinations":[{"category":"embedding","endpoint":"https://s3nt1nel.example",` +
		`"model":"S3NT1NEL"}],"budget":{"max_external_calls":4,"max_input_bytes":65536,"max_inflight_external":8}}`
	p, err := agent24ParseProcessingPolicy([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if p.Budget.MaxExternalCalls != 4 || p.Budget.MaxInputBytes != 65536 || p.Budget.MaxInflightExternal != 8 {
		t.Fatalf("budget = %+v", p.Budget)
	}
	if cap(p.inflight) != 8 {
		t.Fatalf("inflight cap = %d, want 8", cap(p.inflight))
	}

	zero, err := agent24ParseProcessingPolicy([]byte(
		`{"version":"v0","budget":{"max_external_calls":0,"max_input_bytes":0,"max_inflight_external":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	if zero.Budget.MaxExternalCalls != 0 || zero.Budget.MaxInputBytes != 0 || zero.Budget.MaxInflightExternal != 1 {
		t.Fatalf("zero budget = %+v", zero.Budget)
	}
	if cap(zero.inflight) != 1 {
		t.Fatalf("inflight cap = %d, want 1", cap(zero.inflight))
	}

	top, err := agent24ParseProcessingPolicy([]byte(
		`{"version":"vmax","budget":{"max_external_calls":2147483647,"max_input_bytes":2147483647,` +
			`"max_inflight_external":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	if top.Budget.MaxExternalCalls != 2147483647 || top.Budget.MaxInputBytes != 2147483647 {
		t.Fatalf("top budget = %+v", top.Budget)
	}
}

func TestAgent24ProcessingPolicyForRequest(t *testing.T) {
	p, err := agent24ParseProcessingPolicy([]byte(
		`{"version":"v1","budget":{"max_external_calls":2,"max_input_bytes":100,"max_inflight_external":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	a := p.forRequest()
	b := p.forRequest()
	if a == b || a.policy.inflight != b.policy.inflight {
		t.Fatal("requests must be distinct guards over one inflight limit")
	}
	if err := a.Charge(context.Background(), 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := a.Charge(context.Background(), 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := a.Charge(context.Background(), 1, 1); !errors.Is(err, types.ErrProcessingDenied) {
		t.Fatalf("third charge = %v", err)
	}
	if err := b.Charge(context.Background(), 1, 1); err != nil {
		t.Fatalf("second request shares the first counter: %v", err)
	}
	release, err := a.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Acquire(context.Background()); !errors.Is(err, types.ErrProcessingOverloaded) {
		t.Fatalf("second request did not share inflight: %v", err)
	}
	release()
	releaseB, err := b.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	releaseB()
}

func agent24GuardPolicy(calls, nbytes, inflight int) *agent24ProcessingPolicy {
	return &agent24ProcessingPolicy{
		Version: "v1",
		Budget: agent24ProcessingBudget{
			MaxExternalCalls: calls, MaxInputBytes: nbytes, MaxInflightExternal: inflight,
		},
		inflight: make(chan struct{}, inflight),
	}
}

func TestAgent24RequestGuard(t *testing.T) {
	ctx := context.Background()
	t.Run("calls", func(t *testing.T) {
		g := agent24GuardPolicy(2, 100, 1).forRequest()
		if err := g.Charge(ctx, 1, 1); err != nil {
			t.Fatal(err)
		}
		if err := g.Charge(ctx, 1, 1); err != nil {
			t.Fatal(err)
		}
		err := g.Charge(ctx, 1, 1)
		if !errors.Is(err, types.ErrProcessingDenied) {
			t.Fatalf("third = %v", err)
		}
	})
	t.Run("bytes", func(t *testing.T) {
		g := agent24GuardPolicy(10, 10, 1).forRequest()
		if err := g.Charge(ctx, 1, 6); err != nil {
			t.Fatal(err)
		}
		if err := g.Charge(ctx, 1, 6); !errors.Is(err, types.ErrProcessingDenied) {
			t.Fatalf("second = %v", err)
		}
		// The refused charge spent nothing: 4 more bytes and a call still fit.
		if err := g.Charge(ctx, 1, 4); err != nil {
			t.Fatalf("third = %v", err)
		}
	})
	t.Run("zero calls", func(t *testing.T) {
		g := agent24GuardPolicy(0, 10, 1).forRequest()
		if err := g.Charge(ctx, 1, 0); !errors.Is(err, types.ErrProcessingDenied) {
			t.Fatalf("first = %v", err)
		}
	})
	t.Run("inflight", func(t *testing.T) {
		g := agent24GuardPolicy(10, 100, 1).forRequest()
		release, err := g.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := g.Acquire(ctx); !errors.Is(err, types.ErrProcessingOverloaded) {
			t.Fatalf("second = %v", err)
		}
		release()
		release()
		release2, err := g.Acquire(ctx)
		if err != nil {
			t.Fatalf("after a second release: %v", err)
		}
		release2()
		if len(g.policy.inflight) != 0 {
			t.Fatalf("len = %d", len(g.policy.inflight))
		}
	})
	t.Run("default policy", func(t *testing.T) {
		p := agent24DefaultProcessingPolicy()
		if p.inflight != nil {
			t.Fatal("default policy has an inflight limit")
		}
		g := p.forRequest()
		if err := g.Charge(ctx, 1, 1); !errors.Is(err, types.ErrProcessingDenied) {
			t.Fatalf("charge = %v", err)
		}
		// No budget is a refusal, never a retryable overload.
		if _, err := g.Acquire(ctx); !errors.Is(err, types.ErrProcessingDenied) {
			t.Fatalf("acquire = %v", err)
		}
	})
	t.Run("log hides destination", func(t *testing.T) {
		p, err := agent24ParseProcessingPolicy([]byte(
			`{"version":"v1","destinations":[{"category":"embedding","endpoint":"https://s3nt1nel.example",` +
				`"model":"S3NT1NEL"}],"budget":{"max_external_calls":1,"max_input_bytes":10,` +
				`"max_inflight_external":1}}`))
		if err != nil {
			t.Fatal(err)
		}
		var logs bytes.Buffer
		logger.SetOutput(&logs)
		logger.SetLogLevel(logger.LevelDebug)
		t.Cleanup(func() { logger.SetOutput(os.Stdout) })
		g := p.forRequest()
		if err := g.Charge(ctx, 1, 1); err != nil {
			t.Fatal(err)
		}
		err = g.Charge(ctx, 1, 1)
		if !errors.Is(err, types.ErrProcessingDenied) {
			t.Fatalf("charge = %v", err)
		}
		if _, err := g.Acquire(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := g.Acquire(ctx); !errors.Is(err, types.ErrProcessingOverloaded) {
			t.Fatalf("acquire = %v", err)
		}
		text := logs.String()
		if strings.Contains(err.Error(), "S3NT1NEL") || strings.Contains(err.Error(), "s3nt1nel") ||
			strings.Contains(text, "S3NT1NEL") || strings.Contains(text, "s3nt1nel") {
			t.Fatalf("leak: err=%s log=%s", err.Error(), text)
		}
		if !strings.Contains(text, "dimension=calls") || !strings.Contains(text, "version=v1") ||
			!strings.Contains(text, "inflight limit reached version=v1") {
			t.Fatalf("log = %s", text)
		}
	})
}

// Both dimensions at the same limit: a charge that loses on one must not
// spend the other, so exactly limit charges pass whatever the interleaving.
func TestAgent24RequestGuardChargeRace(t *testing.T) {
	const limit = 8
	g := agent24GuardPolicy(limit, limit, 4).forRequest()
	var passed, bad atomic.Int64
	var wg sync.WaitGroup
	const n = 64
	wg.Add(n)
	for range n {
		go func() {
			defer wg.Done()
			err := g.Charge(context.Background(), 1, 1)
			if err == nil {
				passed.Add(1)
				return
			}
			if !errors.Is(err, types.ErrProcessingDenied) {
				bad.Add(1)
			}
		}()
	}
	wg.Wait()
	if bad.Load() != 0 {
		t.Fatalf("unexpected errors: %d", bad.Load())
	}
	if passed.Load() != limit {
		t.Fatalf("passed %d, want %d", passed.Load(), limit)
	}
}
