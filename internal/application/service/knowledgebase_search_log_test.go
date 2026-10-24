package service

import (
	"bytes"
	"context"
	stderrors "errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/models/embedding"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/stretchr/testify/require"
)

func TestHybridSearchLogsQueryLengthNotText(t *testing.T) {
	const sentinel = "S3NT1NEL"
	storeID := "00000000-0000-0000-0000-000000000001"
	kb := &types.KnowledgeBase{
		ID:               "kb-1",
		TenantID:         1,
		EmbeddingModelID: "embed-1",
		VectorStoreID:    &storeID,
		IndexingStrategy: types.IndexingStrategy{VectorEnabled: true, KeywordEnabled: true},
	}
	engine := &fakeRetrieveEngineService{
		engineType: types.PostgresRetrieverEngineType,
		support: []types.RetrieverType{
			types.VectorRetrieverType,
			types.KeywordsRetrieverType,
		},
	}
	svc := &knowledgeBaseService{
		repo:           hybridSearchLogRepo{kb: kb},
		retrieveEngine: &fakeFanoutRegistry{byStore: map[string]interfaces.RetrieveEngineService{storeID: engine}},
		ownership:      &fakeOwnership{owned: map[string]uint64{storeID: 1}},
		kbShareService: &fakeKBShareForAuth{},
	}
	ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(1))
	params := types.SearchParams{QueryText: "find " + sentinel, MatchCount: 3}

	var buf bytes.Buffer
	logger.SetOutput(&buf)
	logger.SetLogLevel(logger.LevelDebug)
	t.Cleanup(func() {
		logger.SetOutput(os.Stdout)
		logger.SetLogLevel(logLevelFromEnv())
	})

	svc.modelService = &imageRecallModels{model: &scriptedEmbedder{}}
	results, err := svc.HybridSearch(ctx, kb.ID, params)
	require.NoError(t, err)
	require.Empty(t, results)

	svc.modelService = &imageRecallModels{model: &scriptedEmbedder{failAfterEmpty: true}}
	_, err = svc.HybridSearch(ctx, kb.ID, params)
	require.Error(t, err)

	// A retrieve failure reaches the ErrorWithFields line after the fan-out.
	svc.modelService = &imageRecallModels{model: &scriptedEmbedder{}}
	engine.cannedErr = stderrors.New("simulated retrieve failure")
	_, err = svc.HybridSearch(ctx, kb.ID, params)
	require.Error(t, err)

	logs := buf.String()
	require.NotContains(t, logs, sentinel)
	require.Contains(t, logs, "query_len=")
	require.Contains(t, logs, "Failed to embed query text")
	require.Contains(t, logs, "simulated retrieve failure")
}

func TestRetrieveSpanInputOmitsQueryText(t *testing.T) {
	params := types.SearchParams{QueryText: "find S3NT1NEL", KnowledgeIDs: []string{"k-1"}}
	input := retrieveSpanInput(params, []string{"kb-1"}, 3, 1)
	require.Equal(t, len(params.QueryText), input["query_len"])
	require.NotContains(t, fmt.Sprint(input), "S3NT1NEL")
}

// hybridSearchLogRepo returns one KB. The package's fakeKBRepo answers
// GetKnowledgeBaseByIDs with an empty list, which stops HybridSearch before
// the embedding log.
type hybridSearchLogRepo struct {
	interfaces.KnowledgeBaseRepository
	kb *types.KnowledgeBase
}

func (r hybridSearchLogRepo) GetKnowledgeBaseByIDs(context.Context, []string) ([]*types.KnowledgeBase, error) {
	return []*types.KnowledgeBase{r.kb}, nil
}

func (r hybridSearchLogRepo) GetKnowledgeBaseByID(_ context.Context, id string) (*types.KnowledgeBase, error) {
	if r.kb != nil && r.kb.ID == id {
		return r.kb, nil
	}
	return nil, stderrors.New("knowledge base not found")
}

// scriptedEmbedder lets the first HybridSearch embed succeed. A second call
// with failAfterEmpty returns an empty vector once, so the pre-computed
// embedding is discarded, then fails inside resolveQueryEmbedding.
type scriptedEmbedder struct {
	embedding.Embedder
	failAfterEmpty bool
	calls          int
}

func (e *scriptedEmbedder) Embed(context.Context, string) ([]float32, error) {
	e.calls++
	if e.failAfterEmpty && e.calls == 1 {
		return []float32{}, nil
	}
	if e.failAfterEmpty {
		return nil, stderrors.New("embed failed")
	}
	return []float32{1, 0}, nil
}

func (e *scriptedEmbedder) GetModelName() string { return "scripted" }
func (e *scriptedEmbedder) GetDimensions() int   { return 0 }
func (e *scriptedEmbedder) GetModelID() string   { return "embed-1" }
func (e *scriptedEmbedder) BatchEmbed(context.Context, []string) ([][]float32, error) {
	return nil, stderrors.New("unused")
}

func (e *scriptedEmbedder) BatchEmbedWithPool(context.Context, embedding.Embedder, []string) ([][]float32, error) {
	return nil, stderrors.New("unused")
}

func logLevelFromEnv() logger.LogLevel {
	switch strings.ToLower(os.Getenv("LOG_LEVEL")) {
	case "info":
		return logger.LevelInfo
	case "warn", "warning":
		return logger.LevelWarn
	case "error":
		return logger.LevelError
	case "fatal":
		return logger.LevelFatal
	default:
		return logger.LevelDebug
	}
}
