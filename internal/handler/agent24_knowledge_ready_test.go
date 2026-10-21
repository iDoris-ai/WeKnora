package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type fakeAgent24ReadyProbe struct{ storage, engine error }

func (f fakeAgent24ReadyProbe) Storage(context.Context) error        { return f.storage }
func (f fakeAgent24ReadyProbe) RetrieveEngine(context.Context) error { return f.engine }

func TestAgent24KnowledgeReady(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const ok = `{"protocol_version":1,"request_id":"r1","generation":7`
	secret := errors.New("SECRET-dsn")
	ready := map[string]any{"state": "ready"}
	notReady := map[string]any{"state": "not_ready", "reason": "dependency"}
	allDown := fakeAgent24ReadyProbe{storage: secret, engine: secret}
	engDown := fakeAgent24ReadyProbe{engine: secret}
	const pv2 = `{"protocol_version":2,"request_id":"r1","generation":7}`
	cases := []struct {
		name, body       string
		probe            fakeAgent24ReadyProbe
		status           int
		search, read     map[string]any
		reason, rid, gen any
	}{
		{name: "all ready", body: ok + `}`, status: 200, search: ready, read: ready},
		{name: "storage down", body: ok + `}`, probe: allDown, status: 200, search: notReady, read: notReady},
		{name: "engine down", body: ok + `}`, probe: engDown, status: 200, search: notReady, read: ready},
		{name: "pv 2", body: pv2, status: 400, reason: "unsupported_protocol_version", rid: "r1", gen: 7.0},
		{name: "extra key", body: ok + `,"x":1}`, status: 400, rid: "r1", gen: 7.0},
		{name: "scope empty", body: ok + `,"scope":{}}`, status: 400, rid: "r1", gen: 7.0},
		{name: "scope no ids", body: ok + `,"scope":{"knowledge_base_ids":[]}}`, status: 400, rid: "r1", gen: 7.0},
		{name: "scope blank id", body: ok + `,"scope":{"knowledge_base_ids":[""]}}`, status: 400, rid: "r1", gen: 7.0},
		{name: "scope non-string", body: ok + `,"scope":{"knowledge_base_ids":[1]}}`, status: 400, rid: "r1", gen: 7.0},
		{name: "scope+x", body: ok + `,"scope":{"knowledge_base_ids":["a"],"x":1}}`, status: 400, rid: "r1", gen: 7.0},
		{name: "not json", body: `nope`, status: 400},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			(&Agent24KnowledgeReadyHandler{probe: tc.probe}).Ready(c)
			require.Equal(t, tc.status, w.Code)
			assert.NotContains(t, w.Body.String(), "SECRET")
			var got map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
			if tc.status != 200 {
				e := got["error"].(map[string]any)
				d := e["details"].(map[string]any)
				assert.Equal(t, []any{"invalid_request", tc.rid, tc.gen, tc.reason},
					[]any{e["code"], d["request_id"], d["generation"], d["reason"]})
				if tc.reason != nil {
					assert.Equal(t, []any{1.0}, d["supported"])
				}
				return
			}
			assert.Len(t, got, 5)
			assert.Equal(t, []any{1.0, "r1", 7.0, true},
				[]any{got["protocol_version"], got["request_id"], got["generation"], got["live"]})
			ingest := map[string]any{"state": "disabled", "reason": "read_only_v1"}
			assert.Equal(t, map[string]any{"search": tc.search, "read": tc.read, "ingest": ingest}, got["operations"])
		})
	}
}

type emptyAgent24Registry struct {
	interfaces.RetrieveEngineRegistry
}

func (emptyAgent24Registry) GetAllRetrieveEngineServices() []interfaces.RetrieveEngineService {
	return nil
}

func TestAgent24DefaultProbeRetrieveEngineDBStores(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "vs.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.VectorStore{}))
	p := agent24DefaultReadyProbe{db: db, registry: emptyAgent24Registry{}}
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 1})
	require.Error(t, p.RetrieveEngine(ctx)) // a: no stores
	require.NoError(t, db.Create(&types.VectorStore{Name: "other", EngineType: "qdrant", TenantID: 2}).Error)
	require.Error(t, p.RetrieveEngine(ctx)) // b: another tenant's store does not count
	vs := types.VectorStore{Name: "gone", EngineType: "qdrant", TenantID: 1}
	require.NoError(t, db.Create(&vs).Error)
	require.NoError(t, db.Delete(&vs).Error)
	require.Error(t, p.RetrieveEngine(ctx)) // c: own store soft-deleted
	require.NoError(t, db.Create(&types.VectorStore{Name: "vs", EngineType: "qdrant", TenantID: 1}).Error)
	require.NoError(t, p.RetrieveEngine(ctx))                // d: own store
	require.Error(t, p.RetrieveEngine(context.Background())) // e: no caller
}

type blockingAgent24ReadyProbe struct {
	blockStorage, blockEngine bool
	deadlines                 *[]time.Time
}

func (p blockingAgent24ReadyProbe) Storage(ctx context.Context) error {
	if p.deadlines != nil {
		d, _ := ctx.Deadline()
		*p.deadlines = append(*p.deadlines, d)
	}
	if p.blockStorage {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func (p blockingAgent24ReadyProbe) RetrieveEngine(ctx context.Context) error {
	if p.deadlines != nil {
		d, _ := ctx.Deadline()
		*p.deadlines = append(*p.deadlines, d)
	}
	if p.blockEngine {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func TestAgent24KnowledgeReadyProbeDeadline(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const ok = `{"protocol_version":1,"request_id":"r1","generation":7`
	ready := map[string]any{"state": "ready"}
	notReady := map[string]any{"state": "not_ready", "reason": "dependency"}
	cases := []struct {
		name         string
		blockStorage bool
		blockEngine  bool
		search, read map[string]any
		sameDeadline bool
	}{
		{name: "engine blocks", blockEngine: true, search: notReady, read: ready},
		{name: "storage blocks", blockStorage: true, search: notReady, read: notReady},
		{name: "neither blocks", search: ready, read: ready, sameDeadline: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			prev := agent24ReadyProbeTimeout
			agent24ReadyProbeTimeout = 50 * time.Millisecond
			t.Cleanup(func() { agent24ReadyProbeTimeout = prev })
			var deadlines []time.Time
			probe := blockingAgent24ReadyProbe{
				blockStorage: tc.blockStorage, blockEngine: tc.blockEngine, deadlines: &deadlines,
			}
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(ok+"}"))
			done := make(chan struct{})
			go func() {
				(&Agent24KnowledgeReadyHandler{probe: probe}).Ready(c)
				close(done)
			}()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				t.Fatal("ready handler did not honor the probe deadline")
			}
			require.Equal(t, 200, w.Code)
			var got map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
			ops := got["operations"].(map[string]any)
			assert.Equal(t, tc.search, ops["search"])
			assert.Equal(t, tc.read, ops["read"])
			if tc.sameDeadline {
				require.Len(t, deadlines, 2)
				assert.False(t, deadlines[0].IsZero())
				assert.False(t, deadlines[1].IsZero())
				assert.Equal(t, deadlines[0], deadlines[1])
			}
		})
	}
}

type blockingAgent24Scope struct{}

func (blockingAgent24Scope) Resolve(ctx context.Context, _ []string) ([]*types.KnowledgeBase, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// A stuck scope lookup is bounded by the same deadline as the probes.
func TestAgent24KnowledgeReadyScopeDeadline(t *testing.T) {
	gin.SetMode(gin.TestMode)
	prev := agent24ReadyProbeTimeout
	agent24ReadyProbeTimeout = 50 * time.Millisecond
	t.Cleanup(func() { agent24ReadyProbeTimeout = prev })
	body := `{"protocol_version":1,"request_id":"r1","generation":7,"scope":{"knowledge_base_ids":["kb-a"]}}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	h := &Agent24KnowledgeReadyHandler{probe: fakeAgent24ReadyProbe{}, scope: blockingAgent24Scope{}}
	done := make(chan struct{})
	go func() { h.Ready(c); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ready handler did not bound the scope lookup")
	}
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	var got map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	details := got["error"].(map[string]any)["details"].(map[string]any)
	assert.Equal(t, "unavailable", got["error"].(map[string]any)["code"])
	assert.Equal(t, "dependency", details["reason"])
}

type fakeAgent24Index map[string]agent24KBIndexFacts

func (f fakeAgent24Index) Facts(_ context.Context, kb *types.KnowledgeBase) (agent24KBIndexFacts, error) {
	return f[kb.ID], nil
}

func TestAgent24KnowledgeReadyScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := `{"protocol_version":1,"request_id":"r1","generation":7,"scope":{"knowledge_base_ids":["b","a","b","x"]}}`
	kbs := map[string]*types.KnowledgeBase{"b": {ID: "b", TenantID: 1}} // E = [b, a], in Q order; x is missing
	kbs["a"] = &types.KnowledgeBase{ID: "a", TenantID: 1, EmbeddingModelID: "m"}
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 1})
	r, dep := map[string]any{"state": "ready"}, map[string]any{"state": "not_ready", "reason": "dependency"}
	bld := map[string]any{"state": "not_ready", "reason": "index_building"}
	kb := func(id, space string, search, read any) any {
		return map[string]any{"knowledge_base_id": id, "score_space": space, "search": search, "read": read}
	}
	check := func(name string, in map[string]*types.KnowledgeBase, engErr error, status int, want ...any) {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequestWithContext(ctx, http.MethodPost, "/", strings.NewReader(body))
			var q [][]string // the real resolver over the fake KB service: one deduped batch lookup
			scope := agent24DefaultScopeResolver{fakeAgent24KB{kbs: in, queries: &q}}
			index := fakeAgent24Index{"a": {Docs: 2, Building: 1}}
			(&Agent24KnowledgeReadyHandler{fakeAgent24ReadyProbe{engine: engErr}, scope, index}).Ready(c)
			require.Equal(t, []any{status, [][]string{{"b", "a", "x"}}}, []any{w.Code, q})
			assert.NotContains(t, w.Body.String(), "SECRET")
			var resp map[string]any
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			if status == 200 {
				assert.Equal(t, any(want), resp["knowledge_bases"])
				return
			}
			e := resp["error"].(map[string]any)
			d := e["details"].(map[string]any)
			assert.Equal(t, append(want, "r1", 7.0), []any{e["code"], d["reason"], d["request_id"], d["generation"]})
		})
	}
	check("ok", kbs, nil, 200, kb("b", "kb:b", r, r), kb("a", "emb:m", bld, bld)) // want: knowledge_bases
	check("search down", kbs, errors.New("x"), 200, kb("b", "kb:b", dep, r), kb("a", "emb:m", dep, bld))
	check("empty E", map[string]*types.KnowledgeBase{}, nil, 403, "forbidden", "empty_scope") // want: [code, reason]
	check("resolver error", nil, nil, 503, "unavailable", "dependency")                       // nil kbs: lookup fails
}

func TestAgent24KBState(t *testing.T) {
	ready, down := agent24State(true), agent24State(false)
	nr := func(r string) agent24OperationState { return agent24OperationState{State: "not_ready", Reason: r} }
	eq := func(op, w agent24OperationState, f agent24KBIndexFacts) { assert.Equal(t, w, agent24KBState(op, f), f) }
	eq(ready, ready, agent24KBIndexFacts{})
	eq(ready, ready, agent24KBIndexFacts{Docs: 3, Active: 1, Building: 1, HasTextChunks: true})
	eq(ready, nr("index_building"), agent24KBIndexFacts{Docs: 2, Building: 1})
	eq(ready, nr("index_failed"), agent24KBIndexFacts{Docs: 2})
	eq(ready, nr("unsupported_kb_type"), agent24KBIndexFacts{Docs: 2, Active: 1, Building: 1})
	eq(ready, ready, agent24KBIndexFacts{Docs: 1, Active: 1, HasTextChunks: true}) // one servable doc
	// cancelled only: its chunks are not servable (r9)
	eq(ready, nr("index_failed"), agent24KBIndexFacts{Docs: 1})
	eq(agent24OperationState{State: "disabled"}, nr("operation_disabled"), agent24KBIndexFacts{HasTextChunks: true})
	eq(down, nr("dependency"), agent24KBIndexFacts{Docs: 2})
	sp := agent24ScoreSpace(&types.KnowledgeBase{EmbeddingModelID: "m", VectorStoreID: new("v")})
	assert.Equal(t, "emb:m|vs:v", sp)
}

type fakeAgent24KB struct { // records each batch lookup; returns hits in reverse order; nil kbs = error
	interfaces.KnowledgeBaseService
	kbs     map[string]*types.KnowledgeBase
	queries *[][]string
}

func (f fakeAgent24KB) GetKnowledgeBasesByIDsOnly(_ context.Context, q []string) (out []*types.KnowledgeBase, _ error) {
	if *f.queries = append(*f.queries, q); f.kbs == nil {
		return nil, errors.New("SECRET-db")
	}
	for _, id := range q {
		if kb, ok := f.kbs[id]; ok {
			out = append([]*types.KnowledgeBase{kb}, out...)
		}
	}
	return out, nil
}

func TestAgent24DefaultScopeAndIndex(t *testing.T) {
	own, own2 := &types.KnowledgeBase{ID: "kb", TenantID: 1}, &types.KnowledgeBase{ID: "kb2", TenantID: 1}
	kbs := map[string]*types.KnowledgeBase{"kb": own, "kb2": own2, "o": {ID: "o", TenantID: 2}}
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 1})
	keyA := types.WithTenantAPIKeyScope(ctx, types.TenantAPIKeyScope{KnowledgeBaseIDs: types.StringArray{"kb"}})
	resolve := func(ctx context.Context, kbs map[string]*types.KnowledgeBase, ids ...string) []any {
		var q [][]string
		got, err := agent24DefaultScopeResolver{fakeAgent24KB{kbs: kbs, queries: &q}}.Resolve(ctx, ids)
		widest := 0
		if len(got) == 0 {
			got = nil
		}
		for _, b := range q {
			widest = max(widest, len(b))
		}
		return []any{got, len(q), widest, err != nil} // E, batch lookups, widest batch, error
	}
	e := func(kbs ...*types.KnowledgeBase) []*types.KnowledgeBase { return kbs }
	big := make([]string, 0, 20000) // 19999 missing ids, then the one own KB
	for i := range 19999 {
		big = append(big, fmt.Sprint("m", i))
	}
	assert.Equal(t, []any{e(own, own2), 1, 4, false}, resolve(ctx, kbs, "o", "missing", "kb", "kb2"))
	assert.Equal(t, []any{e(own), 40, 500, false}, resolve(ctx, kbs, append(big, "kb")...)) // bind-param limits
	assert.Equal(t, []any{e(own), 1, 1, false}, resolve(keyA, kbs, "kb", "kb2"))            // key allows kb only
	assert.Equal(t, []any{e(), 0, 0, false}, resolve(context.Background(), kbs, "kb"))      // tenant 0: not in E
	assert.Equal(t, []any{e(), 1, 1, true}, resolve(ctx, nil, "kb"))
	ci := map[string]*types.KnowledgeBase{"KB": own} // DB collation matches "KB" to row "kb": not in Q
	assert.Equal(t, []any{e(), 1, 1, false}, resolve(ctx, ci, "KB"))
	db, err := gorm.Open(sqlite.Open("file:agent24idx?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.Knowledge{}, &types.Chunk{}))
	rows := []any{ // r9: deleting and draft are not counted; a cancelled doc's chunks are not servable
		&types.Knowledge{ID: "d", TenantID: 1, KnowledgeBaseID: "kb", ParseStatus: types.ParseStatusDeleting},
		&types.Knowledge{ID: "k", TenantID: 1, KnowledgeBaseID: "kb", ParseStatus: types.ParseStatusCancelled},
		&types.Chunk{ID: "c0", TenantID: 1, KnowledgeBaseID: "kb", KnowledgeID: "k", ChunkType: "text"},
		&types.Chunk{ID: "c", TenantID: 1, KnowledgeBaseID: "kb", KnowledgeID: "k", ChunkType: "text", IsEnabled: true},
		&types.Knowledge{
			ID: "dr", TenantID: 1, KnowledgeBaseID: "kb", ParseStatus: types.ManualKnowledgeStatusDraft,
		},
		&types.Knowledge{ID: "k2", TenantID: 1, KnowledgeBaseID: "kb", ParseStatus: types.ParseStatusCompleted},
		&types.Chunk{
			ID: "c2", TenantID: 1, KnowledgeBaseID: "kb", KnowledgeID: "k2", ChunkType: "text", IsEnabled: true,
		},
	}
	wants := []agent24KBIndexFacts{
		{},
		{Docs: 1},
		{Docs: 1},
		{Docs: 1},
		{Docs: 1},
		{Docs: 2, Active: 1},
		{Docs: 2, Active: 1, HasTextChunks: true},
	}
	for i, want := range wants {
		require.NoError(t, db.Create(rows[i]).Error)
		if i == 2 { // gorm default:true overrides a zero IsEnabled on insert
			require.NoError(t, db.Model(rows[i]).Update("is_enabled", false).Error)
		}
		f, err := agent24DBIndexReader{db}.Facts(ctx, own)
		assert.Equal(t, []any{want, nil}, []any{f, err})
	}
}

// The status lists behind Facts agree with the document classifier (contract §5, r9).
func TestAgent24ReadyStatusListsMatchClassifier(t *testing.T) {
	for _, st := range []string{
		types.ParseStatusPending, types.ParseStatusProcessing, types.ParseStatusFailed, types.ParseStatusCancelled,
		types.ParseStatusCompleted, types.ParseStatusFinalizing, types.ParseStatusDeleting,
		types.ManualKnowledgeStatusDraft, "unprocessed",
	} {
		class := agent24ClassifyDoc(st, 0)
		assert.Equal(t, class != agent24DocExcluded, slices.Contains(agent24CountedParseStatuses, st), "%s counted", st)
		assert.Equal(t, agent24HasBuild(st), slices.Contains(agent24ActiveParseStatuses, st), "%s active", st)
		assert.Equal(t, class == agent24DocBuilding, slices.Contains(agent24BuildingParseStatuses, st),
			"%s building", st)
	}
}
