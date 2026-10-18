package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/application/repository"
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
	require.Error(t, p.RetrieveEngine(context.Background()))
	require.NoError(t, db.Create(&types.VectorStore{Name: "vs", EngineType: "qdrant"}).Error)
	require.NoError(t, p.RetrieveEngine(context.Background()))
}

type fakeAgent24Scope func([]string) ([]*types.KnowledgeBase, error)

func (f fakeAgent24Scope) Resolve(_ context.Context, ids []string) ([]*types.KnowledgeBase, error) {
	return f(ids)
}

type fakeAgent24Index map[string]agent24KBIndexFacts

func (f fakeAgent24Index) Facts(_ context.Context, kb *types.KnowledgeBase) (agent24KBIndexFacts, error) {
	return f[kb.ID], nil
}

func TestAgent24KnowledgeReadyScope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := `{"protocol_version":1,"request_id":"r1","generation":7,"scope":{"knowledge_base_ids":["b","a","b","x"]}}`
	kbs := []*types.KnowledgeBase{{ID: "b"}, {ID: "a", EmbeddingModelID: "m"}} // E, in Q order
	r, dep := map[string]any{"state": "ready"}, map[string]any{"state": "not_ready", "reason": "dependency"}
	bld := map[string]any{"state": "not_ready", "reason": "index_building"}
	kb := func(id, space string, search, read any) any {
		return map[string]any{"knowledge_base_id": id, "score_space": space, "search": search, "read": read}
	}
	check := func(name string, in []*types.KnowledgeBase, err, engErr error, status int, want ...any) {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
			var got []string
			scope := fakeAgent24Scope(func(ids []string) ([]*types.KnowledgeBase, error) { got = ids; return in, err })
			index := fakeAgent24Index{"a": {Docs: 2, Building: 1}}
			(&Agent24KnowledgeReadyHandler{fakeAgent24ReadyProbe{engine: engErr}, scope, index}).Ready(c)
			require.Equal(t, status, w.Code)
			assert.Equal(t, []string{"b", "a", "x"}, got)
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
	check("ok", kbs, nil, nil, 200, kb("b", "kb:b", r, r), kb("a", "emb:m", bld, bld)) // want: knowledge_bases
	check("search down", kbs, nil, errors.New("x"), 200, kb("b", "kb:b", dep, r), kb("a", "emb:m", dep, bld))
	check("empty E", nil, nil, nil, 403, "forbidden", "empty_scope") // want: [code, reason]
	check("resolver error", nil, errors.New("SECRET"), nil, 503, "unavailable", "dependency")
}

func TestAgent24KBState(t *testing.T) {
	ready, down := agent24State(true), agent24State(false)
	nr := func(r string) agent24OperationState { return agent24OperationState{State: "not_ready", Reason: r} }
	indexed := agent24KBIndexFacts{Docs: 3, Active: 1, Building: 1, HasTextChunks: true}
	eq := func(op, w agent24OperationState, f agent24KBIndexFacts) { assert.Equal(t, w, agent24KBState(op, f), f) }
	eq(ready, ready, agent24KBIndexFacts{})
	eq(ready, ready, indexed)
	eq(ready, nr("index_building"), agent24KBIndexFacts{Docs: 2, Building: 1})
	eq(ready, nr("index_failed"), agent24KBIndexFacts{Docs: 2})
	eq(ready, nr("unsupported_kb_type"), agent24KBIndexFacts{Docs: 2, Active: 2})
	eq(ready, ready, agent24KBIndexFacts{Docs: 1, HasTextChunks: true}) // cancelled only, with chunks
	eq(ready, nr("index_failed"), agent24KBIndexFacts{Docs: 1})         // cancelled only, no chunks
	eq(agent24OperationState{State: "disabled"}, nr("operation_disabled"), indexed)
	eq(down, nr("dependency"), agent24KBIndexFacts{Docs: 2})
	sp := agent24ScoreSpace(&types.KnowledgeBase{EmbeddingModelID: "m", VectorStoreID: new("v")})
	assert.Equal(t, "emb:m|vs:v", sp)
}

type fakeAgent24KBService struct {
	interfaces.KnowledgeBaseService
	kbs map[string]*types.KnowledgeBase
}

func (f fakeAgent24KBService) GetKnowledgeBaseByID(_ context.Context, id string) (*types.KnowledgeBase, error) {
	if kb, ok := f.kbs[id]; ok {
		return kb, nil
	}
	return nil, repository.ErrKnowledgeBaseNotFound
}

func TestAgent24DefaultScopeAndIndex(t *testing.T) {
	own, other := &types.KnowledgeBase{ID: "kb", TenantID: 1}, &types.KnowledgeBase{ID: "o", TenantID: 2}
	r := agent24DefaultScopeResolver{fakeAgent24KBService{kbs: map[string]*types.KnowledgeBase{"kb": own, "o": other}}}
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 1})
	got, err := r.Resolve(ctx, []string{"o", "missing", "kb"})
	require.NoError(t, err)
	assert.Equal(t, []*types.KnowledgeBase{own}, got)
	got, _ = r.Resolve(context.Background(), []string{"kb"}) // tenant 0: not in E
	assert.Empty(t, got)
	db, err := gorm.Open(sqlite.Open("file:agent24idx?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.Knowledge{}, &types.Chunk{}))
	rows := []any{ // a cancelled doc, then its enabled text chunk
		&types.Knowledge{ID: "k", TenantID: 1, KnowledgeBaseID: "kb", ParseStatus: types.ParseStatusCancelled},
		&types.Chunk{ID: "c", TenantID: 1, KnowledgeBaseID: "kb", KnowledgeID: "k", ChunkType: "text", IsEnabled: true},
	}
	for i, want := range []agent24KBIndexFacts{{Docs: 1}, {Docs: 1, HasTextChunks: true}} {
		require.NoError(t, db.Create(rows[i]).Error)
		f, err := agent24DBIndexReader{db}.Facts(ctx, own)
		require.NoError(t, err)
		assert.Equal(t, want, f)
	}
}
