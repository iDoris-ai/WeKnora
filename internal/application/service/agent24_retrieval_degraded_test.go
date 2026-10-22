package service

import (
	"context"
	stderrors "errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/application/service/retriever"
	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/models/embedding"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
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
		// Qdrant and Milvus carry a partial failure on RetrieveResult.Error, with or without hits.
		{"partial without hits", []*storeGroup{group(engine(nil, cause, nil))}, true, false},
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

// failingChunkRepo fails every chunk lookup.
type failingChunkRepo struct {
	interfaces.ChunkRepository
}

func (failingChunkRepo) ListChunksByID(context.Context, uint64, []string) ([]*types.Chunk, error) {
	return nil, stderrors.New("chunk storage unavailable")
}

// flakyChunkRepo fails the first failures lookups, then answers from answering.
type flakyChunkRepo struct {
	interfaces.ChunkRepository
	answering interfaces.ChunkRepository
	failures  int
	calls     int
}

func (r *flakyChunkRepo) ListChunksByID(ctx context.Context, tenantID uint64, ids []string) ([]*types.Chunk, error) {
	r.calls++
	if r.calls <= r.failures {
		return nil, stderrors.New("chunk storage unavailable")
	}
	return r.answering.ListChunksByID(ctx, tenantID, ids)
}

// The FAQ deepening loop refetches the chunks a failed lookup missed. When a later lookup answers, the result
// is checked against negative questions after all: not degraded. When the last lookup failed, it is.
func TestAgent24RetrievalDegradedFAQLookupRecovers(t *testing.T) {
	// 30 rows over 5 chunks: a full first page (depth 30) that dedups below matchCount 10, so the loop
	// goes one level deeper, where the same 30 rows exhaust the list. c0's negative question is the query:
	// a checked result leaves it out.
	var rows []*types.IndexWithScore
	chunks := map[string]*types.Chunk{}
	for i := range 30 {
		id := fmt.Sprintf("c%d", i%5)
		rows = append(rows, &types.IndexWithScore{ChunkID: id, KnowledgeBaseID: "kb", Score: 0.9 - float64(i)/100})
		chunks[id] = faqChunkWithNegative(t, id, 1, "kb", "gift cards")
	}
	chunks["c0"] = faqChunkWithNegative(t, "c0", 1, "kb", "refunds")
	for _, tc := range []struct {
		name     string
		failures int
		calls    int
		results  int
		degraded bool
	}{
		{"no failure", 0, 1, 4, false},
		{"first lookup fails, second answers", 1, 2, 4, false},
		{"both lookups fail", 2, 2, 5, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := &fakeRetrieveEngineService{
				engineType: types.PostgresRetrieverEngineType,
				support:    []types.RetrieverType{types.VectorRetrieverType},
				canned:     rows,
			}
			groups := []*storeGroup{{
				Engine: buildBoundComposite(t, engine), BaseParams: vectorParams("q"), TopK: 30, KBIDs: []string{"kb"},
			}}
			repo := &flakyChunkRepo{answering: &tenantScopedFAQChunkRepo{chunks: chunks}, failures: tc.failures}
			s := &knowledgeBaseService{chunkRepo: repo, kbShareService: &fakeKBShareService{}}
			ctx, degraded := types.WithRetrievalDegradedRecorder(newSharedAccessContext())
			out, err := s.iterativeRetrieveWithDeduplication(ctx, groups, 10, "refunds", 1)
			require.NoError(t, err)
			require.Len(t, out, tc.results, "c0 is filtered out once checked")
			assert.Equal(t, tc.calls, repo.calls, "lookups")
			assert.Equal(t, tc.degraded, degraded())
		})
	}
}

// A chunk lookup that fails leaves negative questions unchecked, in the FAQ deepening loop and in the
// filter alike; both searches are degraded. The same searches with the lookup answering are not.
func TestAgent24RetrievalDegradedFAQChunks(t *testing.T) {
	answering := func(t *testing.T) interfaces.ChunkRepository {
		return &tenantScopedFAQChunkRepo{chunks: map[string]*types.Chunk{
			"c1": faqChunkWithNegative(t, "c1", 1, "kb", "gift cards"),
		}}
	}
	for _, tc := range []struct {
		name     string
		repo     func(t *testing.T) interfaces.ChunkRepository
		degraded bool
	}{
		{"lookup answers", answering, false},
		{"lookup fails", func(*testing.T) interfaces.ChunkRepository { return failingChunkRepo{} }, true},
	} {
		t.Run("iteration, "+tc.name, func(t *testing.T) {
			engine := &fakeRetrieveEngineService{
				engineType: types.PostgresRetrieverEngineType,
				support:    []types.RetrieverType{types.VectorRetrieverType},
				canned:     []*types.IndexWithScore{{ChunkID: "c1", KnowledgeBaseID: "kb", Score: 0.5}},
			}
			groups := []*storeGroup{{
				Engine: buildBoundComposite(t, engine), BaseParams: vectorParams("q"), TopK: 50, KBIDs: []string{"kb"},
			}}
			s := &knowledgeBaseService{chunkRepo: tc.repo(t), kbShareService: &fakeKBShareService{}}
			ctx, degraded := types.WithRetrievalDegradedRecorder(newSharedAccessContext())
			out, err := s.iterativeRetrieveWithDeduplication(ctx, groups, 10, "refunds", 50)
			require.NoError(t, err)
			require.Len(t, out, 1)
			assert.Equal(t, tc.degraded, degraded())
		})
		t.Run("filter, "+tc.name, func(t *testing.T) {
			s := &knowledgeBaseService{chunkRepo: tc.repo(t), kbShareService: &fakeKBShareService{}}
			ctx, degraded := types.WithRetrievalDegradedRecorder(newSharedAccessContext())
			in := []*types.IndexWithScore{{ChunkID: "c1", KnowledgeBaseID: "kb", Score: 0.5}}
			out := s.filterByNegativeQuestions(ctx, in, "refunds", map[string]bool{"kb": true})
			require.Len(t, out, 1)
			assert.Equal(t, tc.degraded, degraded())
		})
	}
}

// failingModels fails every embedding model lookup.
type failingModels struct {
	interfaces.ModelService
}

func (failingModels) GetEmbeddingModel(context.Context, string) (embedding.Embedder, error) {
	return nil, stderrors.New("model registry unavailable")
}

// Image recall that cannot resolve the KB's embedding model falls back to text: a degraded search. A model
// that takes no images is the normal text-only case.
func TestAgent24RetrievalDegradedImageModel(t *testing.T) {
	kb := &types.KnowledgeBase{ID: "kb", TenantID: 1, EmbeddingModelID: "embed-1"}
	for _, tc := range []struct {
		name     string
		models   interfaces.ModelService
		degraded bool
	}{
		{"text-only model", &imageRecallModels{model: &textModel{}}, false},
		{"lookup fails", failingModels{}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.WithValue(context.Background(), types.TenantIDContextKey, uint64(1))
			ctx, degraded := types.WithRetrievalDegradedRecorder(ctx)
			assert.False(t, (&knowledgeBaseService{modelService: tc.models}).embeddingTakesImages(ctx, kb))
			assert.Equal(t, tc.degraded, degraded())
		})
	}
}

// failingShares fails every organization share lookup.
type failingShares struct {
	fakeKBShareService
}

func (*failingShares) CheckTenantKBPermission(context.Context, string, uint64, types.TenantRole) (
	types.OrgMemberRole, bool, error,
) {
	return "", false, stderrors.New("share storage unavailable")
}

// A chunk of a shared KB is dropped when its share lookup fails: the lookup answered for fewer chunks than it
// should, a degraded search. A chunk the caller may not read (here: outside the API key's KBs) is dropped too,
// and that is no failure.
func TestAgent24RetrievalDegradedSharedChunk(t *testing.T) {
	repo := &tenantScopedFAQChunkRepo{chunks: map[string]*types.Chunk{
		"own":    {ID: "own", TenantID: 1, KnowledgeBaseID: "kb-own"},
		"shared": {ID: "shared", TenantID: 2, KnowledgeBaseID: "kb-shared"},
	}}
	answering := &fakeKBShareService{allowedKBs: map[string]bool{"kb-shared": true}}
	for _, tc := range []struct {
		name     string
		shares   interfaces.KBShareService
		scope    []string
		want     []string
		degraded bool
	}{
		{"share lookup answers", answering, nil, []string{"own", "shared"}, false},
		{"share lookup fails", &failingShares{}, nil, []string{"own"}, true},
		{"outside the API key", answering, []string{"kb-own"}, []string{"own"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := newSharedAccessContext()
			if tc.scope != nil {
				ctx = types.WithTenantAPIKeyScope(ctx, types.TenantAPIKeyScope{KnowledgeBaseIDs: tc.scope})
			}
			ctx, degraded := types.WithRetrievalDegradedRecorder(ctx)
			s := &knowledgeBaseService{chunkRepo: repo, kbShareService: tc.shares}
			got, err := s.listChunksByIDWithShared(ctx, 1, []string{"own", "shared"})
			require.NoError(t, err)
			var ids []string
			for _, c := range got {
				ids = append(ids, c.ID)
			}
			assert.Equal(t, tc.want, ids)
			assert.Equal(t, tc.degraded, degraded())
		})
	}
}
