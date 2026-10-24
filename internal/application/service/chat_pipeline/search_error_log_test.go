package chatpipeline

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
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
