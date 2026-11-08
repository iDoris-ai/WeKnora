// Agent24 fork product patch A24-KB-41 (WeKnora#136).
package handler

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/types"
)

type agent24FailingModels struct{ agent24ModelRows }

func (agent24FailingModels) GetByID(context.Context, uint64, string) (*types.Model, error) {
	return nil, errors.New("database unavailable")
}

func TestAgent24IngestPolicy(t *testing.T) {
	model := &types.Model{
		ID: "m1", Name: "text-embedding-3-small", Source: types.ModelSourceRemote, Status: types.ModelStatusActive,
	}
	model.Parameters.Provider = "openai"
	model.Parameters.BaseURL = "https://agent24-unresolvable.invalid/v1"
	rows := &agent24ModelRows{rows: map[string]*types.Model{"m1": model}}
	kb := &types.KnowledgeBase{ID: "kb", TenantID: 42, EmbeddingModelID: "m1"}
	kb.IndexingStrategy.VectorEnabled = true
	ctx := context.Background()

	set := func(raw string) {
		t.Setenv(Agent24KnowledgePolicyEnv, raw)
		t.Setenv(Agent24KnowledgePolicyFileEnv, "")
		ResetAgent24ProcessingPolicyForTest()
	}
	t.Cleanup(ResetAgent24ProcessingPolicyForTest)
	p := NewAgent24IngestPolicy(rows)

	set(`{"version":"v","destinations":[{"category":"embedding",` +
		`"endpoint":"https://agent24-unresolvable.invalid:443",` +
		`"model":"text-embedding-3-small"}],"budget":{"max_external_calls":4,"max_input_bytes":65536,` +
		`"max_inflight_external":8}}`)
	require.NoError(t, p.AllowIngest(ctx, kb))

	// the KB is bound to a tenant vector store: denied before any lookup (A24-KB-29)
	vs := "vs-1"
	own := *kb
	own.VectorStoreID = &vs
	require.ErrorIs(t, p.AllowIngest(ctx, &own), service.ErrAgent24PolicyDenied)
	require.ErrorIs(t, p.AllowIngest(ctx, nil), service.ErrAgent24PolicyDenied)

	// a destination the policy does not list
	set(`{"version":"v","destinations":[{"category":"embedding","endpoint":"https://other.example:443"}],` +
		`"budget":{"max_external_calls":4,"max_input_bytes":65536,"max_inflight_external":8}}`)
	require.ErrorIs(t, p.AllowIngest(ctx, kb), service.ErrAgent24PolicyDenied)

	// no policy configured: the default denies everything (standalone without the policy file)
	set("")
	require.ErrorIs(t, p.AllowIngest(ctx, kb), service.ErrAgent24PolicyDenied)

	// an unreadable policy denies too
	set(`{"version":`)
	require.ErrorIs(t, p.AllowIngest(ctx, kb), service.ErrAgent24PolicyDenied)

	// a KB that indexes neither vectors nor keywords calls no embedding destination
	set("")
	require.NoError(t, p.AllowIngest(ctx, &types.KnowledgeBase{ID: "kb2", TenantID: 42, EmbeddingModelID: "m1"}))

	// keyword indexing embeds on the ingest side too: its destination is checked like a vector KB's
	keyword := *kb
	keyword.IndexingStrategy.VectorEnabled = false
	keyword.IndexingStrategy.KeywordEnabled = true
	require.ErrorIs(t, p.AllowIngest(ctx, &keyword), service.ErrAgent24PolicyDenied)

	// an indexing KB without a model has no destination to allow
	noModel := *kb
	noModel.EmbeddingModelID = ""
	set(`{"version":"v","destinations":[{"category":"embedding",` +
		`"endpoint":"https://agent24-unresolvable.invalid:443",` +
		`"model":"text-embedding-3-small"}],"budget":{"max_external_calls":4,"max_input_bytes":65536,` +
		`"max_inflight_external":8}}`)
	require.NoError(t, p.AllowIngest(ctx, &keyword), "the listed destination allows a keyword KB")
	require.ErrorIs(t, p.AllowIngest(ctx, &noModel), service.ErrAgent24PolicyDenied)

	// a model row that cannot be read is retried, not a denial
	failing := NewAgent24IngestPolicy(agent24FailingModels{})
	err := failing.AllowIngest(ctx, kb)
	require.Error(t, err)
	require.NotErrorIs(t, err, service.ErrAgent24PolicyDenied)
}
