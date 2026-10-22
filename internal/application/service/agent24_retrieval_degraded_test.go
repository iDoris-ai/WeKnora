package service

import (
	"context"
	stderrors "errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/application/service/retriever"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
)

// Agent24 fork patch A24-KB-09: retainPartialResults marks the recorder on the context, and only then.
func TestAgent24RetrievalDegradedMark(t *testing.T) {
	cause := stderrors.New("1 of 2 matched collections failed")
	group := func(f *fakeRetrieveEngineService) *storeGroup {
		return &storeGroup{
			StoreID: "s", KBIDs: []string{"kb"}, Engine: buildBoundComposite(t, f),
			BaseParams: keywordsRetrieveParams("q"), TopK: 50,
		}
	}
	engine := func(canned []*types.IndexWithScore, resultErr, err error) *fakeRetrieveEngineService {
		return &fakeRetrieveEngineService{
			engineType: types.QdrantRetrieverEngineType, support: []types.RetrieverType{types.KeywordsRetrieverType},
			canned: canned, cannedResultErr: resultErr, cannedErr: err,
		}
	}
	hit := []*types.IndexWithScore{{ChunkID: "c1", Score: 0.5}}
	cases := []struct {
		name     string
		groups   []*storeGroup
		degraded bool
		fails    bool
	}{
		{"partial single group", []*storeGroup{group(engine(hit, cause, nil))}, true, false},
		{
			"partial one of two groups",
			[]*storeGroup{group(engine(hit, nil, nil)), group(engine(hit, cause, nil))},
			true, false,
		},
		{"complete", []*storeGroup{group(engine(hit, nil, nil))}, false, false},
		{"complete empty", []*storeGroup{group(engine(nil, nil, nil))}, false, false},
		{"engine failure", []*storeGroup{group(engine(hit, nil, cause))}, false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, degraded := types.WithRetrievalDegradedRecorder(context.Background())
			_, err := (&knowledgeBaseService{}).retrieveFromStores(ctx, tc.groups, retriever.EngineAwareNormalizer{})
			require.Equal(t, tc.fails, err != nil, "err = %v", err)
			assert.Equal(t, tc.degraded, degraded())
		})
	}
	t.Run("no recorder", func(t *testing.T) {
		res, err := (&knowledgeBaseService{}).retrieveFromStores(context.Background(),
			[]*storeGroup{group(engine(hit, cause, nil))}, retriever.EngineAwareNormalizer{})
		require.NoError(t, err)
		assert.Len(t, res, 1)
	})
	t.Run("recorders are independent", func(t *testing.T) {
		ctxA, a := types.WithRetrievalDegradedRecorder(context.Background())
		_, b := types.WithRetrievalDegradedRecorder(context.Background())
		types.MarkRetrievalDegraded(ctxA)
		assert.Equal(t, []bool{true, false}, []bool{a(), b()})
	})
}

// An image-row refill that fails or answers in part keeps the smaller pool; that is a degraded search too.
func TestAgent24RetrievalDegradedRefill(t *testing.T) {
	images := func(rows []*types.IndexWithScore) []*types.IndexWithScore {
		var out []*types.IndexWithScore
		for _, h := range rows {
			if h.SourceType == types.ImageSourceType {
				out = append(out, h)
			}
		}
		return out
	}
	cases := []struct {
		name     string
		refill   func(rows []*types.IndexWithScore) ([]*types.RetrieveResult, error)
		degraded bool
	}{
		{"refill complete", func(rows []*types.IndexWithScore) ([]*types.RetrieveResult, error) {
			more := append(rows, rankedHits("kb", "deep", 50, 0.1, types.ChunkSourceType)...)
			return []*types.RetrieveResult{{
				Results: more, RetrieverEngineType: types.PostgresRetrieverEngineType,
				RetrieverType: types.VectorRetrieverType,
			}}, nil
		}, false},
		{"refill answers in part", func(rows []*types.IndexWithScore) ([]*types.RetrieveResult, error) {
			return []*types.RetrieveResult{{
				Results: images(rows), RetrieverEngineType: types.PostgresRetrieverEngineType,
				RetrieverType: types.VectorRetrieverType, Error: stderrors.New("collection text: unavailable"),
			}}, nil
		}, true},
		{"refill fails", func([]*types.IndexWithScore) ([]*types.RetrieveResult, error) {
			return nil, stderrors.New("store unavailable")
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			engine := &flakyRefillEngine{
				rankedEngine: rankedEngine{
					fakeRetrieveEngineService: fakeRetrieveEngineService{
						engineType: types.PostgresRetrieverEngineType,
						support:    []types.RetrieverType{types.VectorRetrieverType},
					},
					ranked: map[types.RetrieverType][]*types.IndexWithScore{
						types.VectorRetrieverType: append(
							rankedHits("kb", "image", 40, 0.9, types.ImageSourceType),
							rankedHits("kb", "text", 10, 0.5, types.ChunkSourceType)...),
					},
				},
				refill: tc.refill,
			}
			g := &storeGroup{
				KBIDs: []string{"kb"}, Engine: buildBoundComposite(t, engine), TopK: 50,
				BaseParams: []types.RetrieveParams{{RetrieverType: types.VectorRetrieverType}},
			}
			ctx, degraded := types.WithRetrievalDegradedRecorder(context.Background())
			_, err := (&knowledgeBaseService{}).retrieveFromStores(ctx, []*storeGroup{g}, nil)
			require.NoError(t, err)
			assert.Equal(t, []int{50, 100}, engine.topKs[types.VectorRetrieverType], "the refill ran")
			assert.Equal(t, tc.degraded, degraded())
		})
	}
}

// The FAQ deepening loop keeps what earlier iterations found when a later retrieval fails with a plain
// error; the search is degraded. A typed failure still fails the search, unmarked; a loop that runs out of
// results normally is not degraded.
func TestAgent24RetrievalDegradedFAQIteration(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err      error
		degraded bool
		fails    bool
	}{
		{"runs out normally", nil, false, false},
		{"plain error", stderrors.New("engine timeout"), true, false},
		{"typed error", apperrors.NewVectorStoreUnavailableError("down"), false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bad := &fakeRetrieveEngineService{
				engineType: types.PostgresRetrieverEngineType,
				support:    []types.RetrieverType{types.VectorRetrieverType},
				cannedErr:  tc.err,
			}
			groups := []*storeGroup{
				{Engine: buildBoundComposite(t, bad), BaseParams: vectorParams("q"), TopK: 50, KBIDs: []string{"kb"}},
			}
			ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(1))
			ctx, degraded := types.WithRetrievalDegradedRecorder(ctx)
			_, err := (&knowledgeBaseService{}).iterativeRetrieveWithDeduplication(ctx, groups, 10, "q", 50)
			require.Equal(t, tc.fails, err != nil, "err = %v", err)
			assert.Equal(t, tc.degraded, degraded())
		})
	}
}
