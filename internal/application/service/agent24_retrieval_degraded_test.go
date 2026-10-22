package service

import (
	"context"
	stderrors "errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/application/service/retriever"
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
