package handler

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/config"
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
			`{"version":"v","destinations":[{"category":"retrieval","endpoint":"https://a.example"}]}`,
			"",
		},
		{
			"bad endpoint",
			`{"version":"v","destinations":[{"category":"embedding","endpoint":"S3NT1NEL"}]}`,
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
		`"endpoint":"https://api.example.com/v1","model":"m"}]}`)
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
	]}`)
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
