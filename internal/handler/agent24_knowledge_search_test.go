package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/types"
)

type agent24SearchCall struct {
	id       string
	params   types.SearchParams
	deadline time.Duration // until the ctx deadline, at the call
	tenant   uint64
}

type fakeAgent24Searcher struct {
	results []*types.SearchResult
	err     error
	degrade bool // mark the retrieval degraded, as upstream retainPartialResults does
	calls   []agent24SearchCall
}

func (f *fakeAgent24Searcher) HybridSearch(ctx context.Context, id string, p types.SearchParams) (
	[]*types.SearchResult, error,
) {
	d, _ := ctx.Deadline()
	f.calls = append(f.calls, agent24SearchCall{id, p, time.Until(d), types.CallerFromContext(ctx).TenantID})
	if f.degrade {
		types.MarkRetrievalDegraded(ctx)
	}
	return f.results, f.err
}

type agent24HitsCall struct {
	tenant          uint64
	kbIDs, chunkIDs []string
}

type fakeAgent24Hits struct {
	snap  agent24HitSnapshot
	err   error
	calls []agent24HitsCall
}

func (f *fakeAgent24Hits) Snapshot(_ context.Context, tenantID uint64, kbIDs, chunkIDs []string) (
	agent24HitSnapshot, error,
) {
	f.calls = append(f.calls, agent24HitsCall{tenantID, kbIDs, chunkIDs})
	if f.err != nil {
		return agent24HitSnapshot{}, f.err
	}
	// Like agent24DBHitReader: only rows in kbIDs, only chunks asked for.
	snap := agent24HitSnapshot{Chunks: map[string]agent24HitChunk{}, Docs: map[string]agent24HitDoc{}}
	for _, d := range f.snap.Docs {
		if slices.Contains(kbIDs, d.KnowledgeBaseID) {
			snap.Docs[d.ID] = d
		}
	}
	for _, c := range f.snap.Chunks {
		if slices.Contains(kbIDs, c.KnowledgeBaseID) && slices.Contains(chunkIDs, c.ID) {
			snap.Chunks[c.ID] = c
		}
	}
	return snap, nil
}

// fakeAgent24Scope resolves the ids it knows, in request order (E = Q ∩ known).
type fakeAgent24Scope struct {
	kbs   map[string]*types.KnowledgeBase
	err   error
	calls int
}

func (f *fakeAgent24Scope) Resolve(_ context.Context, ids []string) ([]*types.KnowledgeBase, error) {
	f.calls++
	var out []*types.KnowledgeBase
	for _, id := range ids {
		if kb, ok := f.kbs[id]; ok {
			out = append(out, kb)
		}
	}
	return out, f.err
}

var agent24TestKey = []byte("0123456789abcdef0123456789abcdef")

type agent24SearchEnv struct {
	h        *Agent24KnowledgeSearchHandler
	scope    *fakeAgent24Scope
	searcher *fakeAgent24Searcher
	hits     *fakeAgent24Hits
}

// newAgent24SearchEnv: KBs a, b, c, m share one score space ("m" is another); facts make a and b searchable,
// c empty, d building; the snapshot has c1 (k1, kb a), c2 (k2, kb b) and c3 (k3, kb d: it became servable
// after the facts were read, so it must not be a hit while d is a failed branch).
func newAgent24SearchEnv() *agent24SearchEnv {
	kb := func(id, model string) *types.KnowledgeBase {
		return &types.KnowledgeBase{ID: id, TenantID: 9, EmbeddingModelID: model}
	}
	scope := &fakeAgent24Scope{kbs: map[string]*types.KnowledgeBase{
		"a": kb("a", "e"), "b": kb("b", "e"), "c": kb("c", "e"), "d": kb("d", "e"), "m": kb("m", "other"),
	}}
	facts := fakeAgent24Index{
		"a": {Docs: 2, Active: 1, HasTextChunks: true}, "b": {Docs: 1, Active: 1, HasTextChunks: true},
		"d": {Docs: 1, Building: 1}, "m": {Docs: 1, Active: 1, HasTextChunks: true},
	}
	searcher := &fakeAgent24Searcher{results: []*types.SearchResult{
		{ID: "c2", Score: 0.3}, {ID: "gone", Score: 0.9}, nil, {ID: "c1", Score: 0.5}, {ID: "c3", Score: 0.7},
	}}
	hits := &fakeAgent24Hits{snap: agent24HitSnapshot{
		Chunks: map[string]agent24HitChunk{
			"c1": {ID: "c1", KnowledgeID: "k1", KnowledgeBaseID: "a", Content: "alpha", ContentRevision: 2},
			"c2": {ID: "c2", KnowledgeID: "k2", KnowledgeBaseID: "b", Content: "beta"},
			"c3": {ID: "c3", KnowledgeID: "k3", KnowledgeBaseID: "d", Content: "delta"},
		},
		Docs: map[string]agent24HitDoc{
			"k1": {ID: "k1", KnowledgeBaseID: "a", Title: "T1", ServableChunkIDs: []string{"c1"}},
			"k2": {ID: "k2", KnowledgeBaseID: "b", FileName: "f2.md", ServableChunkIDs: []string{"c2", "c7"}},
			"k3": {ID: "k3", KnowledgeBaseID: "d", ServableChunkIDs: []string{"c3"}},
		},
	}}
	return &agent24SearchEnv{
		h: &Agent24KnowledgeSearchHandler{
			key: agent24TestKey, probe: fakeAgent24ReadyProbe{}, scope: scope, index: facts,
			search: searcher, hits: hits,
		},
		scope: scope, searcher: searcher, hits: hits,
	}
}

func agent24SearchBody(ids string, budget string) string {
	return `{"protocol_version":1,"request_id":"r1","generation":7,"query":"q?",` +
		`"scope":{"knowledge_base_ids":[` + ids + `]},"budget":` + budget + `}`
}

const agent24Budget = `{"deadline_ms":8000,"max_results":10,"max_concurrency":2}`

func (e *agent24SearchEnv) run(ctx context.Context, t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequestWithContext(ctx, http.MethodPost, "/", strings.NewReader(body))
	e.h.Search(c)
	return w
}

func agent24TenantCtx() context.Context {
	return types.WithCaller(context.Background(), types.Caller{TenantID: 9})
}

func agent24SearchSchemas(t *testing.T) (request, response, errResp *jsonschema.Schema) {
	t.Helper()
	c, ids := agent24SchemaCompiler(t)
	var err error
	request, err = c.Compile(ids["search.schema.json"] + "#/$defs/request")
	require.NoError(t, err)
	response, err = c.Compile(ids["search.schema.json"] + "#/$defs/response")
	require.NoError(t, err)
	errResp, err = c.Compile(ids["error.schema.json"])
	require.NoError(t, err)
	return request, response, errResp
}

type agent24SearchResp struct {
	Completeness    string                 `json:"completeness"`
	EffectiveScope  agent24EffectiveScope  `json:"effective_scope"`
	AppliedBudget   agent24SearchBudget    `json:"applied_budget"`
	Hits            []agent24SearchHit     `json:"hits"`
	PartialFailures []agent24SearchFailure `json:"partial_failures"`
	RequestID       string                 `json:"request_id"`
	Generation      int64                  `json:"generation"`
}

func TestAgent24KnowledgeSearchComplete(t *testing.T) {
	reqSchema, respSchema, _ := agent24SearchSchemas(t)
	e := newAgent24SearchEnv()
	body := agent24SearchBody(`"b","x","a","b"`, `{"deadline_ms":9e9,"max_results":100,"max_concurrency":9}`)
	require.NoError(t, agent24Validate(reqSchema, []byte(body)))
	w := e.run(agent24TenantCtx(), t, body)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NoError(t, agent24Validate(respSchema, w.Body.Bytes()), w.Body.String())
	var got agent24SearchResp
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))

	assert.Equal(t, "complete", got.Completeness)
	assert.Equal(t, []string{"b", "a"}, got.EffectiveScope.KnowledgeBaseIDs) // x is outside E
	assert.Equal(t, agent24SearchBudget{DeadlineMS: 30000, MaxResults: 50, MaxConcurrency: 4}, got.AppliedBudget)
	assert.Equal(t, []any{"r1", int64(7)}, []any{got.RequestID, got.Generation})
	assert.NotContains(t, w.Body.String(), "partial_failures")

	require.Len(t, e.searcher.calls, 1)
	call := e.searcher.calls[0]
	assert.Equal(t, "b", call.id)
	assert.Equal(t, types.SearchParams{
		QueryText: "q?", MatchCount: 500, KnowledgeBaseIDs: []string{"b", "a"}, SkipContextEnrichment: true,
	}, call.params)
	assert.InDelta(t, 30*time.Second, call.deadline, float64(time.Second))
	require.Len(t, e.hits.calls, 1)
	assert.Equal(t, agent24HitsCall{9, []string{"b", "a"}, []string{"c2", "gone", "c1", "c3"}}, e.hits.calls[0])

	require.Len(t, got.Hits, 2) // retrieval order, gone dropped
	h1, h2 := got.Hits[0], got.Hits[1]
	row := func(h agent24SearchHit) []any {
		return []any{h.Rank, h.ChunkID, h.KnowledgeBaseID, h.KnowledgeID, h.Score}
	}
	assert.Equal(t, []any{1, "c2", "b", "k2", 0.3}, row(h1))
	assert.Equal(t, []any{2, "c1", "a", "k1", 0.5}, row(h2))
	assert.Equal(t, "emb:e", h1.ScoreSpace)
	assert.Equal(t, agent24ContentTag(agent24TestKey, "c2", "beta"), h1.Revision.ContentTag)
	assert.Equal(t, agent24IndexBuildID(agent24TestKey, "k2", []string{"c7", "c2"}), h1.Revision.IndexBuildID)
	assert.Equal(t, 2, h2.Revision.ChunkRevision)
	assert.Equal(t, agent24HitSnippet{Text: "alpha"}, h2.Snippet)
}

func TestAgent24KnowledgeSearchDeadlineAndLimits(t *testing.T) {
	e := newAgent24SearchEnv()
	tight := `{"deadline_ms":800,"max_results":1,"max_concurrency":1}`
	w := e.run(agent24TenantCtx(), t, agent24SearchBody(`"a","b"`, tight))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var got agent24SearchResp
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	require.Len(t, got.Hits, 1)
	assert.Equal(t, "c2", got.Hits[0].ChunkID)
	require.Len(t, e.searcher.calls, 1)
	assert.InDelta(t, 800*time.Millisecond, e.searcher.calls[0].deadline, float64(200*time.Millisecond))
	assert.Equal(t, uint64(9), e.searcher.calls[0].tenant)

	// At most agent24ScopeBatch candidates reach the snapshot, in retrieval order.
	e = newAgent24SearchEnv()
	e.searcher.results = nil
	for i := range 501 {
		e.searcher.results = append(e.searcher.results, &types.SearchResult{ID: fmt.Sprintf("x%d", i)})
	}
	w = e.run(agent24TenantCtx(), t, agent24SearchBody(`"a"`, agent24Budget))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Len(t, e.hits.calls, 1)
	ids := e.hits.calls[0].chunkIDs
	require.Len(t, ids, 500)
	assert.Equal(t, []string{"x0", "x499"}, []string{ids[0], ids[499]})
	assert.Contains(t, w.Body.String(), `"hits":[]`)
}

func TestAgent24KnowledgeSearchOutcomes(t *testing.T) {
	_, respSchema, errSchema := agent24SearchSchemas(t)
	down := errors.New("SECRET-dsn")
	fail := func(id, code, reason string, retry bool) agent24SearchFailure {
		return agent24SearchFailure{KnowledgeBaseID: id, Code: code, Reason: reason, Retryable: retry}
	}
	cases := []struct {
		name      string
		ids       string
		setup     func(*agent24SearchEnv)
		status    int
		code      string // error code, or completeness of a 200
		reason    any
		failures  []agent24SearchFailure
		hits      []string
		searches  int
		snapshots int
		searched  []string // KnowledgeBaseIDs of the retrieval call; its first is the primary id
	}{
		{
			name: "partial: d building", ids: `"d","a"`, status: 200, code: "partial",
			failures: []agent24SearchFailure{fail("d", "unavailable", "index_building", true)},
			hits:     []string{"c1"}, searches: 1, snapshots: 1, searched: []string{"a"},
		},
		{name: "only empty KB", ids: `"c"`, status: 200, code: "complete", hits: []string{}},
		{
			name: "degraded retrieval", ids: `"a","b"`, setup: func(e *agent24SearchEnv) { e.searcher.degrade = true },
			status: 503, code: "unavailable", reason: "dependency", searches: 1,
			failures: []agent24SearchFailure{
				fail("a", "unavailable", "dependency", true),
				fail("b", "unavailable", "dependency", true),
			},
		},
		{
			name: "degraded retrieval, empty KB succeeds", ids: `"a","c"`,
			setup: func(e *agent24SearchEnv) { e.searcher.degrade = true }, status: 200, code: "partial",
			failures: []agent24SearchFailure{fail("a", "unavailable", "dependency", true)},
			hits:     []string{}, searches: 1,
		},
		{
			name: "retrieval overloaded", ids: `"a"`,
			setup:  func(e *agent24SearchEnv) { e.searcher.err = apperrors.NewTooManyRequestsError("SECRET") },
			status: 503, code: "unavailable", reason: "overloaded", searches: 1,
			failures: []agent24SearchFailure{fail("a", "unavailable", "overloaded", true)},
		},
		{
			name: "retrieval internal", ids: `"a","c"`, setup: func(e *agent24SearchEnv) { e.searcher.err = down },
			status: 500, code: "internal", searches: 1,
		},
		{
			name: "snapshot fails", ids: `"a"`, setup: func(e *agent24SearchEnv) { e.hits.err = down },
			status: 503, code: "unavailable", reason: "dependency", searches: 1, snapshots: 1,
			failures: []agent24SearchFailure{fail("a", "unavailable", "dependency", true)},
		},
		{
			name: "non-finite score", ids: `"a"`,
			setup:  func(e *agent24SearchEnv) { e.searcher.results[3].Score = math.Inf(1) },
			status: 500, code: "internal", searches: 1,
		},
		{
			// "gone" is not in the snapshot: the score would never reach a hit, yet the ranking is broken.
			name: "non-finite score of a dropped candidate", ids: `"a"`,
			setup:  func(e *agent24SearchEnv) { e.searcher.results[1].Score = math.NaN() },
			status: 500, code: "internal", searches: 1,
		},
		{
			name: "engine down", ids: `"a","c"`,
			setup:  func(e *agent24SearchEnv) { e.h.probe = fakeAgent24ReadyProbe{engine: down} },
			status: 503, code: "unavailable", reason: "dependency",
			failures: []agent24SearchFailure{
				fail("a", "unavailable", "dependency", true),
				fail("c", "unavailable", "dependency", true),
			},
		},
		{name: "empty scope", ids: `"x"`, status: 403, code: "forbidden", reason: "empty_scope"},
		{name: "mixed score space", ids: `"a","m"`, status: 400, code: "invalid_request", reason: "mixed_score_space"},
		{
			name: "mixed score space, index facts fail", ids: `"a","m"`,
			setup:  func(e *agent24SearchEnv) { e.h.index = agent24FailingIndex{} },
			status: 400, code: "invalid_request", reason: "mixed_score_space",
		},
		{
			name: "index facts fail", ids: `"a"`,
			setup:  func(e *agent24SearchEnv) { e.h.index = agent24FailingIndex{} },
			status: 503, code: "unavailable", reason: "dependency",
		},
		{
			name: "scope fails", ids: `"a"`, setup: func(e *agent24SearchEnv) { e.scope.err = down },
			status: 503, code: "unavailable", reason: "dependency",
		},
		{
			name: "disabled", ids: `"x"`, setup: func(e *agent24SearchEnv) { e.h.key = nil },
			status: 403, code: "forbidden", reason: "operation_disabled",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newAgent24SearchEnv()
			if tc.setup != nil {
				tc.setup(e)
			}
			w := e.run(agent24TenantCtx(), t, agent24SearchBody(tc.ids, agent24Budget))
			require.Equal(t, tc.status, w.Code, w.Body.String())
			assert.NotContains(t, w.Body.String(), "SECRET")
			assert.Equal(t, []int{tc.searches, tc.snapshots}, []int{len(e.searcher.calls), len(e.hits.calls)})
			if tc.searched != nil {
				require.Len(t, e.searcher.calls, 1)
				call := e.searcher.calls[0]
				assert.Equal(t, tc.searched, call.params.KnowledgeBaseIDs)
				assert.Equal(t, tc.searched[0], call.id)
			}
			if tc.status == 200 {
				require.NoError(t, agent24Validate(respSchema, w.Body.Bytes()), w.Body.String())
				var got agent24SearchResp
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
				assert.Equal(t, tc.code, got.Completeness)
				assert.Equal(t, tc.failures, got.PartialFailures)
				ids := []string{}
				for _, h := range got.Hits {
					ids = append(ids, h.ChunkID)
				}
				assert.Equal(t, tc.hits, ids)
				return
			}
			require.NoError(t, agent24Validate(errSchema, w.Body.Bytes()), w.Body.String())
			var got struct {
				Error struct {
					Code    string         `json:"code"`
					Details map[string]any `json:"details"`
				} `json:"error"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
			assert.Equal(t, []any{tc.code, tc.reason, "r1", 7.0},
				[]any{
					got.Error.Code, got.Error.Details["reason"], got.Error.Details["request_id"],
					got.Error.Details["generation"],
				})
			var fs []agent24SearchFailure
			if raw, ok := got.Error.Details["partial_failures"]; ok {
				b, _ := json.Marshal(raw)
				require.NoError(t, json.Unmarshal(b, &fs))
			}
			assert.Equal(t, tc.failures, fs)
		})
	}
}

// Shape is judged before the disabled operation, and the disabled operation before E (R6.4).
func TestAgent24KnowledgeSearchDecisionOrder(t *testing.T) {
	e := newAgent24SearchEnv()
	e.h.key = nil
	w := e.run(agent24TenantCtx(), t, agent24SearchBody(`"a"`, `{"deadline_ms":1}`))
	require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"code":"invalid_request"`)

	w = e.run(agent24TenantCtx(), t, agent24SearchBody(`"a"`, agent24Budget))
	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"reason":"operation_disabled"`)
	assert.Equal(t, []int{0, 0, 0}, []int{e.scope.calls, len(e.searcher.calls), len(e.hits.calls)})

	big := agent24SearchBody(`"a"`, agent24Budget)
	big += strings.Repeat(" ", agent24KnowledgeMaxBodyBytes+1-len(big))
	w = newAgent24SearchEnv().run(agent24TenantCtx(), t, big)
	require.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), `"request_id":null`)
}

// agent24FailingIndex fails every index fact read.
type agent24FailingIndex struct{}

func (agent24FailingIndex) Facts(context.Context, *types.KnowledgeBase) (agent24KBIndexFacts, error) {
	return agent24KBIndexFacts{}, errors.New("SECRET-dsn")
}

// The caller aborting means no response at all (R7.1), also when upstream swallowed the cancellation as a
// degraded retrieval.
func TestAgent24KnowledgeSearchClientGone(t *testing.T) {
	for _, name := range []string{"retrieval", "degraded retrieval", "snapshot"} {
		t.Run(name, func(t *testing.T) {
			e := newAgent24SearchEnv()
			ctx, cancel := context.WithCancel(agent24TenantCtx())
			switch name {
			case "retrieval":
				e.searcher.err = context.Canceled
			case "degraded retrieval":
				e.searcher.degrade = true
			default:
				e.hits.err = context.Canceled
			}
			e.h.search = agent24CancelOnCall{e.searcher, cancel}
			w := e.run(ctx, t, agent24SearchBody(`"a"`, agent24Budget))
			assert.Empty(t, w.Body.String())
		})
	}
}

// A retrieval the deadline ended, swallowed upstream as degraded, is a timeout, not a dependency failure.
func TestAgent24KnowledgeSearchDegradedByDeadline(t *testing.T) {
	_, _, errSchema := agent24SearchSchemas(t)
	e := newAgent24SearchEnv()
	e.searcher.degrade = true
	e.h.search = agent24WaitForDeadline{e.searcher}
	budget := `{"deadline_ms":50,"max_results":10,"max_concurrency":1}`
	w := e.run(agent24TenantCtx(), t, agent24SearchBody(`"a"`, budget))
	require.Equal(t, http.StatusGatewayTimeout, w.Code, w.Body.String())
	require.NoError(t, agent24Validate(errSchema, w.Body.Bytes()), w.Body.String())
	assert.Contains(t, w.Body.String(), `"code":"timeout"`)
	assert.Contains(t, w.Body.String(), `"reason":"timeout"`)
	assert.Empty(t, e.hits.calls)
}

// agent24WaitForDeadline runs the retrieval once the request deadline has passed.
type agent24WaitForDeadline struct {
	inner *fakeAgent24Searcher
}

func (a agent24WaitForDeadline) HybridSearch(ctx context.Context, id string, p types.SearchParams) (
	[]*types.SearchResult, error,
) {
	<-ctx.Done()
	return a.inner.HybridSearch(ctx, id, p)
}

// agent24CancelOnCall cancels the request context while the retrieval runs.
type agent24CancelOnCall struct {
	inner  *fakeAgent24Searcher
	cancel context.CancelFunc
}

func (a agent24CancelOnCall) HybridSearch(ctx context.Context, id string, p types.SearchParams) (
	[]*types.SearchResult, error,
) {
	a.cancel()
	return a.inner.HybridSearch(ctx, id, p)
}

// One judgement drives ready and search (R6.4): the key read at startup.
func TestAgent24KnowledgeSearchKeyAgreement(t *testing.T) {
	for _, tc := range []struct {
		name, env string
		enabled   bool
	}{
		{"unset", "", false},
		{"too short", base64.StdEncoding.EncodeToString(agent24TestKey[:31]), false},
		{"valid", base64.StdEncoding.EncodeToString(agent24TestKey), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(Agent24KnowledgeTagKeyEnv, tc.env)
			ready := NewAgent24KnowledgeReadyHandler(nil, nil, nil)
			search := NewAgent24KnowledgeSearchHandler(nil, nil, nil)
			assert.Equal(t, !tc.enabled, ready.searchDisabled)
			if tc.enabled {
				assert.Equal(t, agent24TestKey, search.key)
			} else {
				assert.Nil(t, search.key)
			}
		})
	}
}
