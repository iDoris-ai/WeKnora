package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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

	"github.com/Tencent/WeKnora/internal/types"
)

type agent24ReadCall struct {
	tenant      uint64
	kbIDs       []string
	knowledgeID string
	window      agent24ReadWindow
	deadline    time.Duration
}

// fakeAgent24Reads serves docs by id like agent24DBReadReader: only a document in kbIDs is found, and only the
// window's chunks are loaded.
type fakeAgent24Reads struct {
	docs   map[string]agent24ReadSnapshot
	kbOf   map[string]string
	err    error
	cancel context.CancelFunc // called during the snapshot, if set
	calls  []agent24ReadCall
}

func (f *fakeAgent24Reads) Snapshot(ctx context.Context, tenantID uint64, kbIDs []string, knowledgeID string,
	w agent24ReadWindow,
) (agent24ReadSnapshot, error) {
	d, _ := ctx.Deadline()
	f.calls = append(f.calls, agent24ReadCall{tenantID, kbIDs, knowledgeID, w, time.Until(d)})
	if f.cancel != nil {
		f.cancel()
	}
	if f.err != nil {
		return agent24ReadSnapshot{}, f.err
	}
	snap, ok := f.docs[knowledgeID]
	if !ok || !slices.Contains(kbIDs, f.kbOf[knowledgeID]) {
		return agent24ReadSnapshot{}, nil
	}
	if snap.Chunks != nil {
		snap.Chunks = slices.Clone(snap.Chunks)
	}
	for i := range snap.Chunks {
		in := snap.Chunks[i].ID == w.ChunkID
		if w.ChunkID == "" {
			in = i >= w.Position && i < w.Position+w.Limit
		}
		if snap.Chunks[i].Loaded = in; !in {
			snap.Chunks[i].Content = ""
		}
	}
	if snap.Found {
		snap.KnowledgeBaseID = f.kbOf[knowledgeID]
	}
	return snap, nil
}

type agent24ReadEnv struct {
	h     *Agent24KnowledgeReadHandler
	scope *fakeAgent24Scope
	reads *fakeAgent24Reads
}

// newAgent24ReadEnv: KBs a and b are readable; doc-1 (kb a) has three servable chunks, doc-2 (kb a) is still
// building, doc-z lies in kb z, outside every E.
func newAgent24ReadEnv() *agent24ReadEnv {
	scope := &fakeAgent24Scope{kbs: map[string]*types.KnowledgeBase{
		"a": {ID: "a", TenantID: 9}, "b": {ID: "b", TenantID: 9},
	}}
	doc1 := agent24ReadSnapshot{
		Found: true, ParseStatus: types.ParseStatusCompleted, Title: "T", FileName: "f.md",
		Fingerprint: agent24SourceFingerprint{FileHash: "h"},
		Chunks: []agent24ReadChunk{
			{ID: "c1", ContentRevision: 1, Content: "alpha"},
			{ID: "c2", Content: "beta"},
			{ID: "c3", Content: "gamma"},
		},
	}
	reads := &fakeAgent24Reads{
		docs: map[string]agent24ReadSnapshot{
			"doc-1": doc1,
			"doc-2": {Found: true, ParseStatus: types.ParseStatusProcessing},
			"doc-z": doc1,
		},
		kbOf: map[string]string{"doc-1": "a", "doc-2": "a", "doc-z": "z"},
	}
	return &agent24ReadEnv{
		h: &Agent24KnowledgeReadHandler{
			key: agent24TestKey, scope: scope, reads: reads, recheck: agent24FixedRecheck{},
		},
		scope: scope, reads: reads,
	}
}

func (e *agent24ReadEnv) run(ctx context.Context, t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequestWithContext(ctx, http.MethodPost, "/", strings.NewReader(body))
	e.h.Read(c)
	return w
}

func agent24ReadSchemas(t *testing.T) (request, response, errResp *jsonschema.Schema) {
	t.Helper()
	c, ids := agent24SchemaCompiler(t)
	var err error
	request, err = c.Compile(ids["read.schema.json"] + "#/$defs/request")
	require.NoError(t, err)
	response, err = c.Compile(ids["read.schema.json"] + "#/$defs/response")
	require.NoError(t, err)
	errResp, err = c.Compile(ids["error.schema.json"])
	require.NoError(t, err)
	return request, response, errResp
}

type agent24ReadResp struct {
	RequestID      string                `json:"request_id"`
	Generation     int64                 `json:"generation"`
	EffectiveScope agent24EffectiveScope `json:"effective_scope"`
	KnowledgeID    string                `json:"knowledge_id"`
	Revision       agent24DocRevision    `json:"revision"`
	Segments       []agent24ReadSegment  `json:"segments"`
	Truncated      bool                  `json:"truncated"`
	NextCursor     *string               `json:"next_cursor"`
}

func TestAgent24KnowledgeReadChunk(t *testing.T) {
	reqSchema, respSchema, _ := agent24ReadSchemas(t)
	e := newAgent24ReadEnv()
	tag := agent24ContentTag(agent24TestKey, "c2", "beta")
	body := agent24ReadBodyOf(`"scope":{"knowledge_base_ids":["x","a","b"]}`, `"knowledge_id":"doc-1"`,
		`"chunk_id":"c2"`, `"expected_revision":{"content_tag":"`+tag+`","chunk_revision":0}`, `"deadline_ms":800`)
	require.NoError(t, agent24Validate(reqSchema, []byte(body)))
	w := e.run(agent24TenantCtx(), t, body)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NoError(t, agent24Validate(respSchema, w.Body.Bytes()), w.Body.String())
	assert.Contains(t, w.Body.String(), `"next_cursor":null`)
	assert.Contains(t, w.Body.String(), `"display":{"title":"T","file_name":"f.md"}`)
	var got agent24ReadResp
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
	assert.Equal(t, []any{"r1", int64(3), "doc-1"}, []any{got.RequestID, got.Generation, got.KnowledgeID})
	assert.Equal(t, []string{"a", "b"}, got.EffectiveScope.KnowledgeBaseIDs) // x is outside E
	assert.Equal(t, []agent24ReadSegment{{ChunkID: "c2", ChunkType: "text", ContentTag: tag, Text: "beta"}},
		got.Segments)
	assert.Equal(t, agent24IndexBuildID(agent24TestKey, "doc-1", []string{"c1", "c2", "c3"}),
		got.Revision.IndexBuildID)

	require.Len(t, e.reads.calls, 1)
	call := e.reads.calls[0]
	assert.Equal(t, []any{uint64(9), []string{"a", "b"}, "doc-1", agent24ReadWindow{ChunkID: "c2", Limit: 100}},
		[]any{call.tenant, call.kbIDs, call.knowledgeID, call.window})
	assert.InDelta(t, 800*time.Millisecond, call.deadline, float64(200*time.Millisecond))
}

// Document pages follow next_cursor; the cursor's position is the next snapshot's window (R4.5).
func TestAgent24KnowledgeReadDocumentPages(t *testing.T) {
	_, respSchema, _ := agent24ReadSchemas(t)
	e := newAgent24ReadEnv()
	var texts []string
	cursor := "null"
	for range 4 {
		w := e.run(agent24TenantCtx(), t, agent24ReadBodyOf(`"scope":{"knowledge_base_ids":["a"]}`,
			`"knowledge_id":"doc-1"`, `"page":{"cursor":`+cursor+`,"limit":2}`))
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.NoError(t, agent24Validate(respSchema, w.Body.Bytes()), w.Body.String())
		var got agent24ReadResp
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
		for _, s := range got.Segments {
			texts = append(texts, s.Text)
		}
		if got.NextCursor == nil {
			break
		}
		cursor = `"` + *got.NextCursor + `"`
	}
	assert.Equal(t, []string{"alpha", "beta", "gamma"}, texts)
	require.Len(t, e.reads.calls, 2)
	assert.Equal(t, agent24ReadWindow{Position: 2, Limit: 2}, e.reads.calls[1].window)
}

// Every error the endpoint writes is a valid error envelope; out of scope and absent are indistinguishable
// (R2.6).
func TestAgent24KnowledgeReadErrors(t *testing.T) {
	_, _, errSchema := agent24ReadSchemas(t)
	scope := `"scope":{"knowledge_base_ids":["a"]}`
	read := func(fields ...string) *httptest.ResponseRecorder {
		w := newAgent24ReadEnv().run(agent24TenantCtx(), t, agent24ReadBodyOf(append([]string{scope}, fields...)...))
		require.NoError(t, agent24Validate(errSchema, w.Body.Bytes()), w.Body.String())
		return w
	}
	absent, outside := read(`"knowledge_id":"nope"`), read(`"knowledge_id":"doc-z"`)
	assert.Equal(t, http.StatusNotFound, absent.Code)
	assert.Equal(t, absent.Body.String(), outside.Body.String())

	for name, tc := range map[string]struct {
		fields []string
		status int
		want   string
	}{
		"anchor missing": {[]string{`"knowledge_id":"doc-1"`, `"chunk_id":"c9"`}, 409, `"reason":"anchor_missing"`},
		"tag differs": {
			[]string{`"knowledge_id":"doc-1"`, `"chunk_id":"c1"`, `"expected_revision":{"content_tag":"old"}`},
			409, `"current_content_tag":"` + agent24ContentTag(agent24TestKey, "c1", "alpha") + `"`,
		},
		"no build, anchor": {[]string{`"knowledge_id":"doc-2"`, `"chunk_id":"c1"`}, 409, `"index_build_id":null`},
		"no build, doc stale": {
			[]string{`"knowledge_id":"doc-2"`, `"expected_revision":{"source_revision":"s","index_build_id":"b"}`},
			409, `"index_build_id":null`,
		},
		"index pending": {[]string{`"knowledge_id":"doc-2"`}, 503, `"reason":"index_pending"`},
		"bad cursor":    {[]string{`"knowledge_id":"doc-1"`, `"page":{"cursor":"x.y"}`}, 400, `"invalid_request"`},
	} {
		w := read(tc.fields...)
		assert.Equal(t, tc.status, w.Code, name)
		assert.Contains(t, w.Body.String(), tc.want, name)
		assert.Contains(t, w.Body.String(), `"request_id":"r1"`, name)
	}
}

func TestAgent24KnowledgeReadDecisionOrder(t *testing.T) {
	ok := agent24ReadBodyOf(`"scope":{"knowledge_base_ids":["a"]}`, `"knowledge_id":"doc-1"`)

	// Shape before the disabled operation, which comes before E (R6.4).
	e := newAgent24ReadEnv()
	e.h.key = nil
	w := e.run(agent24TenantCtx(), t, agent24ReadBodyOf(`"knowledge_id":"doc-1"`))
	assert.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	w = e.run(agent24TenantCtx(), t, agent24ReadBodyOf(`"scope":{"knowledge_base_ids":["a"]}`,
		`"knowledge_id":"doc-1"`, `"page":{"cursor":"not-checked-without-a-key"}`))
	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"reason":"operation_disabled"`)
	assert.Equal(t, []int{0, 0}, []int{e.scope.calls, len(e.reads.calls)})

	// Empty E: forbidden/empty_scope, no snapshot.
	e = newAgent24ReadEnv()
	w = e.run(agent24TenantCtx(), t, agent24ReadBodyOf(`"scope":{"knowledge_base_ids":["x"]}`,
		`"knowledge_id":"doc-1"`))
	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"reason":"empty_scope"`)
	assert.Empty(t, e.reads.calls)

	// Storage failures: unavailable/dependency, raw text never in the body (R6.3).
	e = newAgent24ReadEnv()
	e.scope.err = errors.New("SECRET-dsn")
	w = e.run(agent24TenantCtx(), t, ok)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
	assert.NotContains(t, w.Body.String(), "SECRET")
	assert.Empty(t, e.reads.calls)
	e = newAgent24ReadEnv()
	e.reads.err = errors.New("SECRET-dsn")
	w = e.run(agent24TenantCtx(), t, ok)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"reason":"dependency"`)
	assert.NotContains(t, w.Body.String(), "SECRET")

	big := ok + strings.Repeat(" ", agent24KnowledgeMaxBodyBytes)
	w = newAgent24ReadEnv().run(agent24TenantCtx(), t, big)
	require.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), `"request_id":null`)
}

// The caller leaving means no response (R7.1); the deadline passing is a timeout, even when the snapshot
// answered anyway.
func TestAgent24KnowledgeReadEnded(t *testing.T) {
	body := agent24ReadBodyOf(`"scope":{"knowledge_base_ids":["a"]}`, `"knowledge_id":"doc-1"`,
		`"deadline_ms":1`)
	e := newAgent24ReadEnv()
	ctx, cancel := context.WithCancel(agent24TenantCtx())
	e.reads.cancel = cancel
	w := e.run(ctx, t, body)
	assert.Empty(t, w.Body.String())
	require.Len(t, e.reads.calls, 1)

	e = newAgent24ReadEnv()
	e.reads.cancel = func() { time.Sleep(20 * time.Millisecond) }
	w = e.run(agent24TenantCtx(), t, body)
	assert.Equal(t, http.StatusGatewayTimeout, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), `"code":"timeout"`)
}

// One read of the key drives the capability declaration, ready and read (R6.4, R9.3).
func TestAgent24KnowledgeReadKeyAgreement(t *testing.T) {
	t.Setenv(Agent24KnowledgeModeEnv, "")
	for _, tc := range []struct {
		name, env string
		enabled   bool
	}{
		{"unset", "", false},
		{"too short", base64.StdEncoding.EncodeToString(agent24TestKey[:31]), false},
		{"valid", base64.StdEncoding.EncodeToString(agent24TestKey), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agent24SetTagKey(t, tc.env)
			ops := BuildAgent24KnowledgeCapability().SupportedOperations
			ready := NewAgent24KnowledgeReadyHandler(nil, nil, nil)
			read := NewAgent24KnowledgeReadHandler(nil, nil, nil, nil, nil, nil, nil, nil)
			assert.Equal(t, []bool{tc.enabled, !tc.enabled, tc.enabled},
				[]bool{slices.Contains(ops, "read"), ready.keyMissing, read.key != nil})

			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/",
				strings.NewReader(`{"protocol_version":1,"request_id":"r","generation":0}`))
			(&Agent24KnowledgeReadyHandler{probe: fakeAgent24ReadyProbe{}, keyMissing: ready.keyMissing}).Ready(c)
			want := `"read":{"state":"ready"}`
			if !tc.enabled {
				want = `"read":{"state":"disabled","reason":"tag_key_missing"}`
			}
			assert.Contains(t, w.Body.String(), want)
		})
	}
}

func TestAgent24KnowledgeReadRecheck(t *testing.T) {
	body := agent24ReadBodyOf(`"scope":{"knowledge_base_ids":["a","b"]}`, `"knowledge_id":"doc-1"`)

	t.Run("document outside the new scope", func(t *testing.T) {
		e := newAgent24ReadEnv()
		wrap := &agent24ScopeThen{inner: e.scope, then: []*types.KnowledgeBase{{ID: "b"}}}
		e.h.scope = wrap
		w := e.run(agent24TenantCtx(), t, body)
		require.Equal(t, 2, wrap.calls)
		require.Len(t, e.reads.calls, 1)
		assert.Equal(t, []string{"a", "b"}, e.reads.calls[0].kbIDs)
		require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
		agent24RequireErrorCode(t, w.Body.Bytes(), "not_found", "")
	})
	t.Run("scope empty", func(t *testing.T) {
		e := newAgent24ReadEnv()
		wrap := &agent24ScopeThen{inner: e.scope}
		e.h.scope = wrap
		w := e.run(agent24TenantCtx(), t, body)
		require.Equal(t, 2, wrap.calls)
		require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
		agent24RequireErrorCode(t, w.Body.Bytes(), "forbidden", "empty_scope")
	})
	stale := agent24ReadBodyOf(`"scope":{"knowledge_base_ids":["a","b"]}`, `"knowledge_id":"doc-1"`,
		`"chunk_id":"c1"`, `"expected_revision":{"content_tag":"old"}`)
	t.Run("stale after the credential stopped", func(t *testing.T) {
		e := newAgent24ReadEnv()
		e.h.recheck = agent24FixedRecheck{reason: "credential_revoked"}
		w := e.run(agent24TenantCtx(), t, stale)
		require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
		agent24RequireErrorCode(t, w.Body.Bytes(), "unauthorized", "credential_revoked")
		assert.NotContains(t, w.Body.String(), "current_content_tag")
	})
	t.Run("stale for a document outside the new scope", func(t *testing.T) {
		e := newAgent24ReadEnv()
		e.h.scope = &agent24ScopeThen{inner: e.scope, then: []*types.KnowledgeBase{{ID: "b"}}}
		w := e.run(agent24TenantCtx(), t, stale)
		require.Equal(t, http.StatusNotFound, w.Code, w.Body.String())
		agent24RequireErrorCode(t, w.Body.Bytes(), "not_found", "")
		assert.NotContains(t, w.Body.String(), "current_content_tag")
	})
	t.Run("deadline passed while a stale read was rechecked", func(t *testing.T) {
		e := newAgent24ReadEnv()
		e.h.scope = &agent24ResolveWaitSecond{inner: e.scope}
		w := e.run(agent24TenantCtx(), t, agent24ReadBodyOf(`"scope":{"knowledge_base_ids":["a","b"]}`,
			`"knowledge_id":"doc-1"`, `"chunk_id":"c1"`, `"expected_revision":{"content_tag":"old"}`,
			`"deadline_ms":30`))
		require.Equal(t, http.StatusGatewayTimeout, w.Code, w.Body.String())
		agent24RequireErrorCode(t, w.Body.Bytes(), "timeout", "")
		assert.NotContains(t, w.Body.String(), "current_content_tag")
	})
	t.Run("deadline passed while the read was judged", func(t *testing.T) {
		e := newAgent24ReadEnv()
		e.h.judge = func(key []byte, req agent24ReadRequest, snap agent24ReadSnapshot) (
			agent24ReadPage, *agent24KnowledgeError,
		) {
			time.Sleep(50 * time.Millisecond)
			return agent24JudgeRead(key, req, snap)
		}
		w := e.run(agent24TenantCtx(), t, agent24ReadBodyOf(`"scope":{"knowledge_base_ids":["a","b"]}`,
			`"knowledge_id":"doc-1"`, `"deadline_ms":30`))
		require.Equal(t, http.StatusGatewayTimeout, w.Code, w.Body.String())
		agent24RequireErrorCode(t, w.Body.Bytes(), "timeout", "")
	})
	t.Run("stale inside the new scope", func(t *testing.T) {
		e := newAgent24ReadEnv()
		e.h.scope = &agent24ScopeThen{inner: e.scope, then: []*types.KnowledgeBase{{ID: "a"}}}
		w := e.run(agent24TenantCtx(), t, stale)
		require.Equal(t, http.StatusConflict, w.Code, w.Body.String())
		assert.Contains(t, w.Body.String(), "current_content_tag")
	})
	t.Run("credential expired", func(t *testing.T) {
		e := newAgent24ReadEnv()
		e.h.recheck = agent24FixedRecheck{reason: "credential_expired"}
		w := e.run(agent24TenantCtx(), t, body)
		require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
		agent24RequireErrorCode(t, w.Body.Bytes(), "unauthorized", "credential_expired")
	})
}

// agent24ResolveWaitSecond resolves through inner, and on the second call (the recheck) waits for the
// request's deadline first: the recheck is the last step before a read answers.
type agent24ResolveWaitSecond struct {
	inner agent24ScopeResolver
	calls int
}

func (s *agent24ResolveWaitSecond) Resolve(ctx context.Context, ids []string) ([]*types.KnowledgeBase, error) {
	s.calls++
	if s.calls == 2 {
		<-ctx.Done()
	}
	return s.inner.Resolve(ctx, ids)
}
