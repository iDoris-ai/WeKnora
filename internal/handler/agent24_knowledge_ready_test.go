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
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/middleware"
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
			h := &Agent24KnowledgeReadyHandler{probe: fakeAgent24ReadyProbe{engine: engErr}, scope: scope, index: index}
			h.Ready(c)
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

// Without the revision key search is disabled (R6.4, R9.3): operations.search and every KB's search say so,
// whatever the dependencies; read is unaffected. The response stays schema-valid.
func TestAgent24KnowledgeReadySearchDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	_, respSchema := agent24ReadySchemas(t)
	down := errors.New("down")
	index := fakeAgent24Index{"chunks": {Docs: 1, Active: 1, HasTextChunks: true}, "building": {Docs: 1, Building: 1}}
	body := `{"protocol_version":1,"request_id":"r1","generation":7,` +
		`"scope":{"knowledge_base_ids":["chunks","building"]}}`
	disabled := map[string]any{"state": "disabled", "reason": "tag_key_missing"}
	kbOff := map[string]any{"state": "not_ready", "reason": "operation_disabled"}
	for _, tc := range []struct {
		name  string
		probe fakeAgent24ReadyProbe
	}{
		{"deps up", fakeAgent24ReadyProbe{}},
		{"engine down", fakeAgent24ReadyProbe{engine: down}},
		{"storage down", fakeAgent24ReadyProbe{storage: down}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
			h := &Agent24KnowledgeReadyHandler{
				probe: tc.probe, scope: agent24EveryKB{}, index: index, keyMissing: true,
			}
			h.Ready(c)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			require.NoError(t, agent24Validate(respSchema, w.Body.Bytes()), w.Body.String())
			var got struct {
				Operations     map[string]any   `json:"operations"`
				KnowledgeBases []map[string]any `json:"knowledge_bases"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
			// Without the key both search and read are disabled, whatever the dependencies (R6.4).
			assert.Equal(t, disabled, got.Operations["search"])
			assert.Equal(t, disabled, got.Operations["read"])
			require.Len(t, got.KnowledgeBases, 2)
			for _, kb := range got.KnowledgeBases {
				assert.Equal(t, kbOff, kb["search"], kb["knowledge_base_id"])
				assert.Equal(t, kbOff, kb["read"], kb["knowledge_base_id"])
			}
		})
	}
}

// A body over agent24KnowledgeMaxBodyBytes is invalid_request with nothing echoed, even when it starts as a
// valid request; one exactly at the limit is read.
func TestAgent24KnowledgeReadyBodyLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	head := `{"protocol_version":1,"request_id":"r1","generation":7}`
	for _, tc := range []struct {
		name   string
		size   int
		status int
	}{
		{"at limit", 1 << 20, http.StatusOK},
		{"one byte over", 1<<20 + 1, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := head + strings.Repeat(" ", tc.size-len(head))
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
			(&Agent24KnowledgeReadyHandler{probe: fakeAgent24ReadyProbe{}}).Ready(c)
			require.Equal(t, tc.status, w.Code, w.Body.String())
			if tc.status != http.StatusOK {
				assert.JSONEq(t, `{"error":{"code":"invalid_request","message":"invalid request",`+
					`"details":{"retryable":false,"request_id":null,"generation":null}}}`, w.Body.String())
			}
		})
	}
}

// agent24AuditStub records Log entries. Other AuditLogService methods panic if called.
type agent24AuditStub struct {
	interfaces.AuditLogService
	mu      sync.Mutex
	entries []*types.AuditLog
}

func (s *agent24AuditStub) Log(_ context.Context, entry *types.AuditLog) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *entry
	cp.Details = append(types.JSON(nil), entry.Details...)
	s.entries = append(s.entries, &cp)
	return nil
}

// agent24AuditIdentity stands in for middleware.Auth: caller, principal and a tenant API key on the request.
func agent24AuditIdentity(c *gin.Context) {
	ctx := types.WithCaller(c.Request.Context(), types.Caller{TenantID: 9, UserID: "user-should-not-leak"})
	p := types.Principal{Type: types.PrincipalAPITenant, ID: "principal-7"}
	ctx = types.WithPrincipal(ctx, p)
	ctx = types.WithTenantAPIKeyScope(ctx, types.TenantAPIKeyScope{KeyID: 7})
	c.Request = c.Request.WithContext(ctx)
	c.Set(types.PrincipalContextKey.String(), p)
	c.Next()
}

func agent24AuditServe(
	t *testing.T, svc interfaces.AuditLogService, identity bool, path, body string, h gin.HandlerFunc,
) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Agent24KnowledgeAudit(svc))
	if identity {
		r.Use(agent24AuditIdentity)
	}
	r.POST(path, h)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

func agent24AuditBlob(t *testing.T, entry *types.AuditLog) string {
	t.Helper()
	raw, err := json.Marshal(entry)
	require.NoError(t, err)
	return string(raw)
}

func TestAgent24KnowledgeAuditSearch(t *testing.T) {
	stub := &agent24AuditStub{}
	e := newAgent24SearchEnv()
	body := `{"protocol_version":1,"request_id":"r1","generation":7,"query":"S3NT1NEL",` +
		`"scope":{"knowledge_base_ids":["b","a"]},"budget":` + agent24Budget + `}`
	w := agent24AuditServe(t, stub, true, "/api/v1/agent24/knowledge/search", body, e.h.Search)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Len(t, stub.entries, 1)
	entry := stub.entries[0]
	require.Equal(t, types.AuditAction("knowledge.search"), entry.Action)
	require.Equal(t, "", entry.ActorUserID)
	require.Equal(t, "agent24_knowledge", entry.ScopeType)
	require.Equal(t, "", entry.ScopeID)
	require.Equal(t, types.AuditOutcomeSuccess, entry.Outcome)

	var details struct {
		OccurredAt string  `json:"occurred_at"`
		RequestID  *string `json:"request_id"`
		Generation *int64  `json:"generation"`
		Principal  *struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		} `json:"principal"`
		CredentialID   *string  `json:"credential_id"`
		TenantID       *uint64  `json:"tenant_id"`
		EffectiveScope []string `json:"effective_scope"`
		Resource       struct {
			ChunkIDs []string `json:"chunk_ids"`
			Count    int      `json:"count"`
		} `json:"resource"`
		Revision []struct {
			ChunkID        string `json:"chunk_id"`
			SourceRevision string `json:"source_revision"`
			IndexBuildID   string `json:"index_build_id"`
			ChunkRevision  int    `json:"chunk_revision"`
		} `json:"revision"`
		Outcome  string `json:"outcome"`
		Metering struct {
			ResultCount   int `json:"result_count"`
			BytesReturned int `json:"bytes_returned"`
		} `json:"metering"`
	}
	require.NoError(t, json.Unmarshal(entry.Details, &details))
	occurred, err := time.Parse(time.RFC3339Nano, details.OccurredAt)
	require.NoError(t, err)
	require.Equal(t, time.UTC, occurred.Location())
	require.Equal(t, "r1", *details.RequestID)
	require.Equal(t, int64(7), *details.Generation)
	require.Equal(t, "api_tenant", details.Principal.Type)
	require.Equal(t, "principal-7", details.Principal.ID)
	require.Equal(t, "key:7", *details.CredentialID)
	require.Equal(t, uint64(9), *details.TenantID)
	require.Equal(t, []string{"b", "a"}, details.EffectiveScope)
	require.Equal(t, []string{"c2", "c1"}, details.Resource.ChunkIDs)
	require.Equal(t, 2, details.Resource.Count)
	require.Equal(t, 2, details.Metering.ResultCount)
	require.Greater(t, details.Metering.BytesReturned, 0)
	require.Equal(t, "ok", details.Outcome)
	fp := agent24SourceFingerprint{}
	require.Equal(t, "c2", details.Revision[0].ChunkID)
	require.Equal(t, agent24SourceRevision(agent24TestKey, "k2", fp), details.Revision[0].SourceRevision)
	require.Equal(t, agent24IndexBuildID(agent24TestKey, "k2", []string{"c2", "c7"}), details.Revision[0].IndexBuildID)
	require.Equal(t, 0, details.Revision[0].ChunkRevision)
	require.Equal(t, "c1", details.Revision[1].ChunkID)
	require.Equal(t, agent24SourceRevision(agent24TestKey, "k1", fp), details.Revision[1].SourceRevision)
	require.Equal(t, agent24IndexBuildID(agent24TestKey, "k1", []string{"c1"}), details.Revision[1].IndexBuildID)
	require.Equal(t, 2, details.Revision[1].ChunkRevision)
	blob := agent24AuditBlob(t, entry)
	require.NotContains(t, blob, "S3NT1NEL")
	require.NotContains(t, blob, "content_tag")
	require.NotContains(t, blob, "user-should-not-leak")
}

func TestAgent24KnowledgeAuditRead(t *testing.T) {
	stub := &agent24AuditStub{}
	e := newAgent24ReadEnv()
	doc := e.reads.docs["doc-1"]
	doc.Chunks[1].Content = "S3NT1NEL"
	e.reads.docs["doc-1"] = doc
	tag := agent24ContentTag(agent24TestKey, "c2", "S3NT1NEL")
	body := agent24ReadBodyOf(`"scope":{"knowledge_base_ids":["a","b"]}`, `"knowledge_id":"doc-1"`, `"chunk_id":"c2"`)
	w := agent24AuditServe(t, stub, true, "/api/v1/agent24/knowledge/read", body, e.h.Read)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Contains(t, w.Body.String(), "S3NT1NEL")
	require.Len(t, stub.entries, 1)
	entry := stub.entries[0]
	require.Equal(t, types.AuditAction("knowledge.read"), entry.Action)
	require.Equal(t, "", entry.ActorUserID)
	require.Equal(t, "agent24_knowledge", entry.ScopeType)
	require.Equal(t, types.AuditOutcomeSuccess, entry.Outcome)

	var details struct {
		EffectiveScope []string `json:"effective_scope"`
		Resource       struct {
			KnowledgeID string   `json:"knowledge_id"`
			ChunkIDs    []string `json:"chunk_ids"`
		} `json:"resource"`
		Revision struct {
			SourceRevision string `json:"source_revision"`
			IndexBuildID   string `json:"index_build_id"`
			Segments       []struct {
				ChunkID       string `json:"chunk_id"`
				ChunkRevision int    `json:"chunk_revision"`
			} `json:"segments"`
		} `json:"revision"`
		Outcome  string `json:"outcome"`
		Metering struct {
			ResultCount int `json:"result_count"`
		} `json:"metering"`
	}
	require.NoError(t, json.Unmarshal(entry.Details, &details))
	require.Equal(t, "ok", details.Outcome)
	require.Equal(t, []string{"a", "b"}, details.EffectiveScope)
	require.Equal(t, "doc-1", details.Resource.KnowledgeID)
	require.Equal(t, []string{"c2"}, details.Resource.ChunkIDs)
	require.Equal(t, 1, details.Metering.ResultCount)
	fp := agent24SourceFingerprint{FileHash: "h"}
	require.Equal(t, agent24SourceRevision(agent24TestKey, "doc-1", fp), details.Revision.SourceRevision)
	require.Equal(t, agent24IndexBuildID(agent24TestKey, "doc-1", []string{"c1", "c2", "c3"}),
		details.Revision.IndexBuildID)
	require.Equal(t, []string{"c2"}, []string{details.Revision.Segments[0].ChunkID})
	require.Equal(t, 0, details.Revision.Segments[0].ChunkRevision)
	blob := agent24AuditBlob(t, entry)
	require.NotContains(t, blob, "S3NT1NEL")
	require.NotContains(t, blob, tag)
	require.NotContains(t, blob, "content_tag")
}

func TestAgent24KnowledgeAuditEmptyScope(t *testing.T) {
	stub := &agent24AuditStub{}
	e := newAgent24SearchEnv()
	body := agent24SearchBody(`"x"`, agent24Budget)
	w := agent24AuditServe(t, stub, true, "/api/v1/agent24/knowledge/search", body, e.h.Search)
	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	require.Len(t, stub.entries, 1)
	entry := stub.entries[0]
	require.Equal(t, "", entry.ActorUserID)
	require.Equal(t, types.AuditOutcomeDenied, entry.Outcome)
	var details struct {
		Outcome string `json:"outcome"`
		Reason  string `json:"reason"`
	}
	require.NoError(t, json.Unmarshal(entry.Details, &details))
	require.Equal(t, "forbidden", details.Outcome)
	require.Equal(t, "empty_scope", details.Reason)
}

func TestAgent24KnowledgeAuditUnauthenticated(t *testing.T) {
	stub := &agent24AuditStub{}
	w := agent24AuditServe(t, stub, false, "/api/v1/agent24/knowledge/search", "", func(c *gin.Context) {
		c.AbortWithStatus(http.StatusUnauthorized)
	})
	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Len(t, stub.entries, 1)
	entry := stub.entries[0]
	require.Equal(t, uint64(0), entry.TenantID)
	require.Equal(t, "", entry.ActorUserID)
	require.Equal(t, types.AuditOutcomeDenied, entry.Outcome)
	var details struct {
		Principal    *struct{ Type, ID string } `json:"principal"`
		CredentialID *string                    `json:"credential_id"`
		TenantID     *uint64                    `json:"tenant_id"`
		Outcome      string                     `json:"outcome"`
		Reason       string                     `json:"reason"`
	}
	require.NoError(t, json.Unmarshal(entry.Details, &details))
	require.Equal(t, "unauthorized", details.Outcome)
	require.Equal(t, "credential_invalid", details.Reason)
	require.Nil(t, details.Principal)
	require.Nil(t, details.CredentialID)
	require.Nil(t, details.TenantID)
	raw := string(entry.Details)
	require.Contains(t, raw, `"principal":null`)
	require.Contains(t, raw, `"credential_id":null`)
	require.Contains(t, raw, `"tenant_id":null`)
}

// Unauthenticated rows sit in tenant 0, the system scope. The system feed lists only unscoped rows, so a
// flood of anonymous requests cannot page a real system event out of its first page (PR #96 probe).
func TestAgent24KnowledgeAuditAnonymousRowsStayOutOfSystemFeed(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "audit.db")), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.AuditLog{}))
	svc := service.NewAuditLogService(repository.NewAuditLogRepository(db))
	require.NoError(t, svc.Log(context.Background(), &types.AuditLog{Action: types.AuditActionSystemSettingChanged}))

	const anonymous = 60 // more than the feed's default page of 50
	for range anonymous {
		w := agent24AuditServe(t, svc, false, "/api/v1/agent24/knowledge/search", "", func(c *gin.Context) {
			c.AbortWithStatus(http.StatusUnauthorized)
		})
		require.Equal(t, http.StatusUnauthorized, w.Code)
	}
	var written int64
	require.NoError(t, db.Model(&types.AuditLog{}).
		Where("tenant_id = 0 AND scope_type = ?", agent24AuditScopeType).Count(&written).Error)
	require.Positive(t, written, "the anonymous rows must reach tenant 0 for this test to mean anything")

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(middleware.ErrorHandler())
	r.GET("/system/audit-log", NewAuditLogHandler(svc).ListSystemAuditLog)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/system/audit-log", nil))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var page auditLogListResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &page))
	require.Len(t, page.Data, 1)
	require.Equal(t, types.AuditActionSystemSettingChanged, page.Data[0].Action)
}

// The integration credential gate rejects after Auth bound a caller (platform key, direct-mode tenant key):
// R10.2 still records the identity as null for credential_invalid.
func TestAgent24KnowledgeAuditCredentialInvalidAfterAuthIsAnonymous(t *testing.T) {
	stub := &agent24AuditStub{}
	w := agent24AuditServe(t, stub, true, "/api/v1/agent24/knowledge/search", "", func(c *gin.Context) {
		writeAgent24KnowledgeError(c, agent24Unauthorized("credential_invalid"), agent24KnowledgeEcho{})
	})
	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Len(t, stub.entries, 1)
	entry := stub.entries[0]
	require.Equal(t, uint64(0), entry.TenantID)
	require.Equal(t, types.AuditOutcomeDenied, entry.Outcome)
	raw := string(entry.Details)
	require.Contains(t, raw, `"principal":null`)
	require.Contains(t, raw, `"credential_id":null`)
	require.Contains(t, raw, `"tenant_id":null`)
	require.Contains(t, raw, `"reason":"credential_invalid"`)
	require.NotContains(t, raw, "principal-7")
}

// A panic in the handler leaves one row, as internal even after a success was noted, with the size of the
// 500 that Recovery (mounted inside the audit, as in router.go) wrote.
func TestAgent24KnowledgeAuditRecordsPanic(t *testing.T) {
	stub := &agent24AuditStub{}
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Agent24KnowledgeAudit(stub))
	r.Use(gin.CustomRecovery(func(c *gin.Context, _ any) {
		c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": "Internal Server Error"})
	}))
	r.Use(agent24AuditIdentity)
	r.POST("/api/v1/agent24/knowledge/read", func(c *gin.Context) {
		agent24NoteAudit(c, func(f *agent24AuditFacts) { f.Outcome = agent24AuditOK })
		panic("boom")
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/agent24/knowledge/read", strings.NewReader("{}")))
	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Len(t, stub.entries, 1)
	require.Equal(t, types.AuditOutcomeFailed, stub.entries[0].Outcome)
	var details struct {
		Outcome  string `json:"outcome"`
		Metering struct {
			BytesReturned int `json:"bytes_returned"`
		} `json:"metering"`
	}
	require.NoError(t, json.Unmarshal(stub.entries[0].Details, &details))
	require.Equal(t, "internal", details.Outcome)
	require.Equal(t, w.Body.Len(), details.Metering.BytesReturned)
	require.Positive(t, details.Metering.BytesReturned)
}

type agent24PanickingAudit struct{ interfaces.AuditLogService }

func (agent24PanickingAudit) Log(context.Context, *types.AuditLog) error {
	panic("audit store S3NT1NEL")
}

// The audit sits outside Recovery: a panic while writing the row is contained, and the response the handler
// already wrote reaches the client unchanged.
func TestAgent24KnowledgeAuditContainsOwnPanic(t *testing.T) {
	var w *httptest.ResponseRecorder
	require.NotPanics(t, func() {
		w = agent24AuditServe(t, agent24PanickingAudit{}, true, "/api/v1/agent24/knowledge/ready", "",
			func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	})
	require.Equal(t, http.StatusOK, w.Code)
	require.JSONEq(t, `{"ok":true}`, w.Body.String())
}

// A guard ahead of the handler (the API-key capability gate, the Viewer guard) writes its own 403: the row is
// forbidden and denied, not internal.
func TestAgent24KnowledgeAuditInfersForbiddenFromGuard(t *testing.T) {
	stub := &agent24AuditStub{}
	w := agent24AuditServe(t, stub, true, "/api/v1/agent24/knowledge/read", "", func(c *gin.Context) {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "forbidden"})
	})
	require.Equal(t, http.StatusForbidden, w.Code)
	require.Len(t, stub.entries, 1)
	require.Equal(t, types.AuditOutcomeDenied, stub.entries[0].Outcome)
	require.Contains(t, string(stub.entries[0].Details), `"outcome":"forbidden"`)
	require.Contains(t, string(stub.entries[0].Details), `"principal":{"type"`)
}

func TestAgent24KnowledgeAuditSkipsOtherPaths(t *testing.T) {
	stub := &agent24AuditStub{}
	w := agent24AuditServe(t, stub, true, "/api/v1/other", "", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	require.Equal(t, http.StatusNoContent, w.Code)
	require.Empty(t, stub.entries)
}

func TestAgent24KnowledgeAuditNilService(t *testing.T) {
	require.NotPanics(t, func() {
		w := agent24AuditServe(t, nil, false, "/api/v1/agent24/knowledge/ready", "", func(c *gin.Context) {
			c.Status(http.StatusNoContent)
		})
		require.Equal(t, http.StatusNoContent, w.Code)
	})
}
