package chatpipeline

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

func TestSearchErrorLogOmitsBackendErrorText(t *testing.T) {
	const sentinel = "S3NT1NEL"
	cases := []struct {
		name   string
		target *types.SearchTarget
		action string
	}{
		{
			name:   "combined knowledge bases",
			target: &types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-1"},
			action: "action=combined_kb_search_error",
		},
		{
			name: "single knowledge target",
			target: &types.SearchTarget{
				Type: types.SearchTargetTypeKnowledge, KnowledgeBaseID: "kb-1", KnowledgeIDs: []string{"k-1"},
			},
			action: "action=kb_search_error",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger.SetOutput(&buf)
			logger.SetLogLevel(logger.LevelDebug)
			t.Cleanup(logger.ConfigureFromEnv)

			plugin := &PluginSearch{
				knowledgeBaseService: &failingSearchKnowledgeBaseService{err: errors.New("backend echoed " + sentinel)},
			}
			chatManage := &types.ChatManage{
				PipelineRequest: types.PipelineRequest{
					SearchTargets: types.SearchTargets{tc.target},
					EmbeddingTopK: 10,
				},
				PipelineState: types.PipelineState{RewriteQuery: "q"},
			}

			_ = plugin.OnEvent(context.Background(), types.CHUNK_SEARCH, chatManage, func() *PluginError { return nil })

			out := buf.String()
			if !strings.Contains(out, tc.action+" ") || !strings.Contains(out, "error_len=") {
				t.Fatalf("expected %s with error_len, got: %s", tc.action, out)
			}
			if strings.Contains(out, sentinel) {
				t.Fatalf("log carries the backend error text: %s", out)
			}
		})
	}
}

// SearchParallel logs each branch's error again after the branches finish; the chunk branch's error is the
// search backend's, which can echo the query.
func TestSearchParallelErrorLogOmitsBackendErrorText(t *testing.T) {
	const sentinel = "S3NT1NEL"
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	logger.SetLogLevel(logger.LevelDebug)
	t.Cleanup(logger.ConfigureFromEnv)

	plugin := &PluginSearchParallel{
		searchPlugin: &PluginSearch{
			knowledgeBaseService: &failingSearchKnowledgeBaseService{err: errors.New("backend echoed " + sentinel)},
		},
		searchEntityPlugin: &PluginSearchEntity{},
	}
	chatManage := &types.ChatManage{
		PipelineRequest: types.PipelineRequest{
			SearchTargets: types.SearchTargets{
				&types.SearchTarget{Type: types.SearchTargetTypeKnowledgeBase, KnowledgeBaseID: "kb-1"},
			},
			EmbeddingTopK: 10,
		},
		PipelineState: types.PipelineState{RewriteQuery: "q"},
	}

	noop := func() *PluginError { return nil }
	_ = plugin.OnEvent(context.Background(), types.CHUNK_SEARCH_PARALLEL, chatManage, noop)

	out := buf.String()
	if !strings.Contains(out, "[SearchParallel] chunk_search error: error_len=") {
		t.Fatalf("expected the chunk_search error line with error_len, got: %s", out)
	}
	if strings.Contains(out, sentinel) {
		t.Fatalf("log carries the backend error text: %s", out)
	}
}

type failingEntityGraphRepo struct {
	interfaces.RetrieveGraphRepository
	err error
}

func (r *failingEntityGraphRepo) SearchNode(context.Context, types.NameSpace, []string) (*types.GraphData, error) {
	return nil, r.err
}

func TestSearchEntityFailureLogOmitsErrorText(t *testing.T) {
	const sentinel = "S3NT1NEL"
	cases := []struct {
		name    string
		manage  *types.ChatManage
		wantLog string
	}{
		{
			name: "knowledge file",
			manage: &types.ChatManage{PipelineState: types.PipelineState{
				Entity:          []string{"Docker"},
				EntityKnowledge: map[string]string{"doc-1": "kb-1"},
			}},
			wantLog: "Failed to search entity in Knowledge doc-1: error_len=",
		},
		{
			name: "knowledge base",
			manage: &types.ChatManage{PipelineState: types.PipelineState{
				Entity:      []string{"Docker"},
				EntityKBIDs: []string{"kb-1"},
			}},
			wantLog: "Failed to search entity in KB kb-1: error_len=",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger.SetOutput(&buf)
			logger.SetLogLevel(logger.LevelDebug)
			t.Cleanup(logger.ConfigureFromEnv)

			plugin := &PluginSearchEntity{graphRepo: &failingEntityGraphRepo{err: errors.New(sentinel)}}
			err := plugin.OnEvent(
				context.Background(), types.ENTITY_SEARCH, tc.manage, func() *PluginError { return nil },
			)
			if err != nil {
				t.Fatalf("OnEvent: %v", err)
			}
			out := buf.String()
			if strings.Contains(out, sentinel) {
				t.Fatalf("log carries the graph error text: %s", out)
			}
			if !strings.Contains(out, tc.wantLog) {
				t.Fatalf("log = %s, want %s", out, tc.wantLog)
			}
		})
	}
}
