package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
	"github.com/Tencent/WeKnora/internal/types"
)

var agent24CandidateTestKey = []byte("0123456789abcdef0123456789abcdef")

type agent24CandidateEnv struct {
	db    *gorm.DB
	store *repository.Agent24CandidateStore
	h     *Agent24KnowledgeCandidatesHandler
	kbs   map[string]*types.KnowledgeBase
	clock time.Time
}

func newAgent24CandidateEnv(t *testing.T) *agent24CandidateEnv {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dsn := filepath.Join(t.TempDir(), "cand.db") + "?_journal_mode=WAL&_busy_timeout=5000"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&types.Knowledge{}, &types.Agent24IngestJob{}))
	require.NoError(t, repository.EnsureAgent24CandidateTables(db))
	e := &agent24CandidateEnv{
		db: db, store: repository.NewAgent24CandidateStore(db),
		kbs: map[string]*types.KnowledgeBase{
			"kbA": {ID: "kbA", TenantID: 1}, "kbB": {ID: "kbB", TenantID: 1}, "kbO": {ID: "kbO", TenantID: 2},
		},
		clock: time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC),
	}
	var q [][]string
	e.h = newAgent24KnowledgeCandidatesHandler(agent24CandidateTestKey, e.store,
		agent24DefaultScopeResolver{fakeAgent24KB{kbs: e.kbs, queries: &q}})
	n := 0
	e.h.now = func() time.Time { n++; return e.clock.Add(time.Duration(n) * time.Second) }
	return e
}

// agent24CandidateCaller builds a request context: tenant 1, the principal, and a key with scope.
type agent24CandidateAs struct {
	principal types.Principal
	scope     types.TenantAPIKeyScope
	tenant    uint64
}

var (
	agent24Alice = types.Principal{Type: "api_external_user", ID: "1:alice"}
	agent24Bob   = types.Principal{Type: "api_external_user", ID: "1:bob"}
)

func agent24Proposer(p types.Principal) agent24CandidateAs {
	return agent24CandidateAs{principal: p, tenant: 1, scope: types.TenantAPIKeyScope{
		KeyID: 9, KnowledgeBaseIDs: types.StringArray{"kbA", "kbB"},
		Capabilities: types.StringArray{"retrieve", "kb_candidate_propose"},
	}}
}

func (as agent24CandidateAs) ctx() context.Context {
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: as.tenant})
	ctx = types.WithTenantAPIKeyScope(ctx, as.scope)
	if as.principal != (types.Principal{}) {
		ctx = types.WithPrincipal(ctx, as.principal)
	}
	return ctx
}

type agent24CandidateReply struct {
	code  int
	raw   string
	body  map[string]any
	facts *agent24AuditFacts
}

func (r agent24CandidateReply) outcome() string {
	if r.code == http.StatusOK {
		return "ok"
	}
	e, _ := r.body["error"].(map[string]any)
	d, _ := e["details"].(map[string]any)
	if reason, ok := d["reason"].(string); ok {
		return e["code"].(string) + "/" + reason
	}
	code, _ := e["code"].(string)
	return code
}

func (r agent24CandidateReply) details() map[string]any {
	if r.code == http.StatusOK {
		d, _ := r.body["details"].(map[string]any)
		return d
	}
	d, _ := r.body["error"].(map[string]any)["details"].(map[string]any)
	return d
}

func (r agent24CandidateReply) audit() *agent24CandidateAudit {
	if r.facts == nil {
		return nil
	}
	a, _ := r.facts.Resource.(*agent24CandidateAudit)
	return a
}

func agent24CandidateCall(
	t *testing.T, serve func(*gin.Context), as agent24CandidateAs, body string,
) agent24CandidateReply {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequestWithContext(as.ctx(), http.MethodPost, "/", strings.NewReader(body))
	serve(c)
	r := agent24CandidateReply{code: w.Code, raw: w.Body.String()}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &r.body), w.Body.String())
	if v, ok := c.Get(agent24AuditFactsKey); ok {
		r.facts, _ = v.(*agent24AuditFacts)
	}
	return r
}

// agent24Submit is a submission body: the fixture content by default, the hash recomputed after mut.
func agent24Submit(mut func(map[string]any)) string {
	return string(agent24SubmitDoc(func(m map[string]any) {
		if mut != nil {
			mut(m)
		}
		if _, ok := m["content_sha256"].(string); ok && m["content_sha256"] != "keep" {
			kind, _ := m["kind"].(string)
			title, _ := m["title"].(string)
			body, _ := m["body"].(string)
			m["content_sha256"] = agent24EntryHash(kind, title, body)
		}
	}))
}

func agent24KeyN(n int) string {
	return "idem-key-0000000000000-" + string(rune('a'+n%26)) + strings.Repeat("x", n/26)
}

func (e *agent24CandidateEnv) count(t *testing.T, where string, args ...any) int64 {
	t.Helper()
	var n int64
	require.NoError(t, e.db.Model(&types.Agent24KnowledgeCandidate{}).Where(where, args...).Count(&n).Error)
	return n
}

func TestAgent24CandidateSubmitCreate(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	sch := agent24CandidateSchema(t, "submit_response")
	r := agent24CandidateCall(t, e.h.Submit, agent24Proposer(agent24Alice), agent24Submit(nil))
	require.Equal(t, http.StatusOK, r.code, r.raw)
	require.NoError(t, agent24Validate(sch, []byte(r.raw)), r.raw)
	assert.Equal(t, "req-1", r.body["request_id"])
	assert.Equal(t, 3.0, r.body["generation"])
	assert.Equal(t, "submitted", r.body["status"])
	assert.Nil(t, r.body["details"])
	id, _ := r.body["candidate_id"].(string)
	assert.Len(t, id, 64)
	assert.Equal(t, "2026-11-09T10:00:01Z", r.body["submitted_at"])
	assert.True(t, strings.HasPrefix(r.body["authz_version"].(string), "az1_"))

	cand, err := e.store.GetCandidate(context.Background(), 1, id)
	require.NoError(t, err)
	require.NotNil(t, cand)
	assert.Equal(t, "api_external_user:1:alice", cand.Author)
	assert.Equal(t, types.Agent24CandidateSubmitted, cand.Status)
	assert.Equal(t, "请假流程 🌿", cand.Title)
	assert.Equal(t, []any{strings.Repeat("a", 64), 2, 7, "knowledge-entry/1", int64(1)},
		[]any{cand.SessionRef, cand.MessageFrom, cand.MessageTo, cand.SkillVersion, cand.RedactionRulesVersion})
	// W8.2: no document, no job, no upstream row
	var docs, jobs int64
	require.NoError(t, e.db.Model(&types.Knowledge{}).Count(&docs).Error)
	require.NoError(t, e.db.Model(&types.Agent24IngestJob{}).Count(&jobs).Error)
	assert.Zero(t, docs+jobs)

	a := r.audit()
	require.NotNil(t, a)
	assert.Equal(t, agent24AuditOK, r.facts.Outcome)
	assert.Equal(t, []any{id, "created", "sop", "kbA", "create", false},
		[]any{a.CandidateID, a.Outcome, a.Kind, a.TargetKnowledgeBaseID, a.Mode, a.Replayed})
	assert.Equal(t, agent24SHA256Hex("k7Qx2mN4pR8sT1vW3yZ5bA"), a.IdempotencyKeySHA256)
	raw, _ := json.Marshal(a)
	assert.NotContains(t, string(raw), "请假", "W8.12: no title or body in the audit")
	assert.NotContains(t, string(raw), "k7Qx2mN4pR8sT1vW3yZ5bA")
}

// W3.2: same key and fingerprint replays (replayed: true, same candidate); same key, other content is reuse;
// a new key with the same content is the W8.2 existing candidate, without replayed (N6a, N6c).
func TestAgent24CandidateSubmitIdempotencyAndDedup(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	sch := agent24CandidateSchema(t, "submit_response")
	alice := agent24Proposer(agent24Alice)
	first := agent24CandidateCall(t, e.h.Submit, alice, agent24Submit(nil))
	require.Equal(t, http.StatusOK, first.code)
	id := first.body["candidate_id"]

	retry := agent24CandidateCall(t, e.h.Submit, alice, agent24Submit(func(m map[string]any) {
		m["request_id"], m["generation"] = "req-2", 4
	}))
	require.Equal(t, http.StatusOK, retry.code)
	require.NoError(t, agent24Validate(sch, []byte(retry.raw)), retry.raw)
	assert.Equal(t, map[string]any{"replayed": true}, retry.details())
	assert.Equal(t, []any{id, "req-2", 4.0, first.body["submitted_at"], first.body["authz_version"]},
		[]any{
			retry.body["candidate_id"], retry.body["request_id"], retry.body["generation"],
			retry.body["submitted_at"], retry.body["authz_version"],
		})
	assert.True(t, retry.audit().Replayed)

	reused := agent24CandidateCall(t, e.h.Submit, alice, agent24Submit(func(m map[string]any) {
		m["body"] = "另一份正文"
	}))
	assert.Equal(t, "invalid_request/idempotency_key_reused", reused.outcome())

	again := agent24CandidateCall(t, e.h.Submit, alice, agent24Submit(func(m map[string]any) {
		m["idempotency_key"] = agent24KeyN(1)
	}))
	require.Equal(t, http.StatusOK, again.code)
	require.NoError(t, agent24Validate(sch, []byte(again.raw)), again.raw)
	assert.Equal(t, map[string]any{"outcome": "existing"}, again.details())
	assert.Equal(t, id, again.body["candidate_id"])
	assert.Equal(t, "existing", again.audit().Outcome)
	// and that answer is itself replayable under its own key
	again2 := agent24CandidateCall(t, e.h.Submit, alice, agent24Submit(func(m map[string]any) {
		m["idempotency_key"] = agent24KeyN(1)
	}))
	assert.Equal(t, map[string]any{"outcome": "existing", "replayed": true}, again2.details())
	assert.EqualValues(t, 1, e.count(t, "1 = 1"))

	// another author's same content and key are their own (W3.2: principals do not see each other's keys)
	bob := agent24CandidateCall(t, e.h.Submit, agent24Proposer(agent24Bob), agent24Submit(nil))
	require.Equal(t, http.StatusOK, bob.code)
	assert.Nil(t, bob.body["details"])
	assert.NotEqual(t, id, bob.body["candidate_id"])
	// a different target is a different candidate
	other := agent24CandidateCall(t, e.h.Submit, alice, agent24Submit(func(m map[string]any) {
		m["idempotency_key"], m["target_knowledge_base_id"] = agent24KeyN(2), "kbB"
	}))
	require.Equal(t, http.StatusOK, other.code)
	assert.Nil(t, other.body["details"])
	// a withdrawn candidate is not a duplicate
	wd := agent24CandidateCall(t, e.h.Withdraw, alice,
		`{"request_id":"w","generation":1,"candidate_id":"`+id.(string)+`"}`)
	require.Equal(t, http.StatusOK, wd.code)
	fresh := agent24CandidateCall(t, e.h.Submit, alice, agent24Submit(func(m map[string]any) {
		m["idempotency_key"] = agent24KeyN(3)
	}))
	require.Equal(t, http.StatusOK, fresh.code)
	assert.Nil(t, fresh.body["details"])
	assert.NotEqual(t, id, fresh.body["candidate_id"])
}

// An expired record (W3.3) is a new request.
func TestAgent24CandidateSubmitExpiredKey(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	alice := agent24Proposer(agent24Alice)
	require.Equal(t, http.StatusOK, agent24CandidateCall(t, e.h.Submit, alice, agent24Submit(nil)).code)
	e.clock = e.clock.Add(agent24WriteIdempotencyRetention + time.Hour)
	r := agent24CandidateCall(t, e.h.Submit, alice, agent24Submit(func(m map[string]any) { m["body"] = "新内容" }))
	require.Equal(t, http.StatusOK, r.code, r.raw)
	assert.Nil(t, r.body["details"])
}

// W1.9: ② (shape and the restricted Markdown) before ③, and ③ before ④ and ⑤.
func TestAgent24CandidateSubmitDecisionOrder(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	alice := agent24Proposer(agent24Alice)
	noCap := alice
	noCap.scope.Capabilities = types.StringArray{"retrieve"}
	html := func(m map[string]any) { m["body"] = "<img src=x>" }

	// N18: on a visible knowledge base, and before authorization
	assert.Equal(t, "invalid_request/unsupported_format",
		agent24CandidateCall(t, e.h.Submit, alice, agent24Submit(html)).outcome())
	assert.Equal(t, "invalid_request/unsupported_format",
		agent24CandidateCall(t, e.h.Submit, noCap, agent24Submit(html)).outcome())
	assert.Equal(t, "invalid_request/idempotency_key_missing",
		agent24CandidateCall(t, e.h.Submit, noCap, agent24Submit(func(m map[string]any) {
			delete(m, "idempotency_key")
		})).outcome())

	// ③: C first, then E
	assert.Equal(t, "forbidden/operation_not_allowed",
		agent24CandidateCall(t, e.h.Submit, noCap, agent24Submit(nil)).outcome())
	for name, mut := range map[string]func(map[string]any){
		"not in the key":     func(m map[string]any) { m["target_knowledge_base_id"] = "kbZ" },
		"other tenant":       func(m map[string]any) { m["target_knowledge_base_id"] = "kbO" },
		"key lists it, gone": func(m map[string]any) { m["target_knowledge_base_id"] = "kbB"; delete(e.kbs, "kbB") },
	} {
		r := agent24CandidateCall(t, e.h.Submit, alice, agent24Submit(mut))
		assert.Equal(t, "forbidden/empty_scope", r.outcome(), name)
	}
	e.kbs["kbB"] = &types.KnowledgeBase{ID: "kbB", TenantID: 1}
	// P3: a key without a knowledge-base list, and a full-access key, have an empty write E
	empty := alice
	empty.scope.KnowledgeBaseIDs = nil
	full := alice
	full.scope.FullAccess = true
	assert.Equal(t, "forbidden/empty_scope", agent24CandidateCall(t, e.h.Submit, empty, agent24Submit(nil)).outcome())
	assert.Equal(t, "forbidden/empty_scope", agent24CandidateCall(t, e.h.Submit, full, agent24Submit(nil)).outcome())
	// no principal: never a write
	anon := alice
	anon.principal = types.Principal{}
	assert.Equal(t, "forbidden/operation_not_allowed",
		agent24CandidateCall(t, e.h.Submit, anon, agent24Submit(nil)).outcome())
	assert.Zero(t, e.count(t, "1 = 1"))

	// ③ before ④: a caller who lost the capability gets no replay
	require.Equal(t, http.StatusOK, agent24CandidateCall(t, e.h.Submit, alice, agent24Submit(nil)).code)
	assert.Equal(t, "forbidden/operation_not_allowed",
		agent24CandidateCall(t, e.h.Submit, noCap, agent24Submit(nil)).outcome())
	// ④ before ⑤: the same key and fingerprint replay even where ⑤ would now refuse (a body that no longer
	// matches the claimed hash, which the fingerprint carries)
	r := agent24CandidateCall(t, e.h.Submit, alice, string(agent24SubmitDoc(func(m map[string]any) {
		m["body"] = "改过的正文"
	})))
	assert.Equal(t, map[string]any{"replayed": true}, r.details())
}

// W8.2 ⑤1, ⑤2: nothing is saved.
func TestAgent24CandidateSubmitContentChecks(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	alice := agent24Proposer(agent24Alice)
	for _, tc := range []struct {
		name string
		mut  func(map[string]any)
		want string
	}{
		{"unknown kind", func(m map[string]any) { m["kind"] = "howto" }, "invalid_request"},
		{"title leading space", func(m map[string]any) { m["title"] = " 请假" }, "invalid_request"},
		{"empty title", func(m map[string]any) { m["title"] = "" }, "invalid_request"},
		{"title too long", func(m map[string]any) { m["title"] = strings.Repeat("t", 201) }, "invalid_request"},
		{"body over 64 KiB", func(m map[string]any) {
			m["body"] = strings.Repeat("字", 21846) // 65538 bytes, 21846 characters
		}, "invalid_request/too_large"},
		{"hash mismatch", func(m map[string]any) {
			m["content_sha256"] = "keep"
		}, "invalid_request/content_hash_mismatch"},
		{"kind before hash", func(m map[string]any) {
			m["kind"], m["content_sha256"] = "howto", "keep"
		}, "invalid_request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := agent24Submit(func(m map[string]any) {
				tc.mut(m)
				m["idempotency_key"] = agent24KeyN(len(tc.name))
			})
			body = strings.Replace(body, `"content_sha256":"keep"`,
				`"content_sha256":"`+strings.Repeat("0", 64)+`"`, 1)
			assert.Equal(t, tc.want, agent24CandidateCall(t, e.h.Submit, alice, body).outcome())
		})
	}
	// exactly 64 KiB passes
	ok := agent24CandidateCall(t, e.h.Submit, alice, agent24Submit(func(m map[string]any) {
		m["body"] = strings.Repeat("b", agent24EntryBodyMaxBytes)
	}))
	assert.Equal(t, http.StatusOK, ok.code, ok.raw)
	assert.EqualValues(t, 1, e.count(t, "1 = 1"))
	var recs int64
	require.NoError(t, e.db.Model(&types.Agent24WriteIdempotency{}).Count(&recs).Error)
	assert.EqualValues(t, 1, recs, "W3.2: refused requests write no record")
}

// W8.2 ⑤4: 20 submitted per principal; an existing candidate adds nothing and still passes; a withdrawal frees one.
func TestAgent24CandidateSubmitQuota(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	alice := agent24Proposer(agent24Alice)
	var ids []string
	for i := range agent24CandidateQuota {
		r := agent24CandidateCall(t, e.h.Submit, alice, agent24Submit(func(m map[string]any) {
			m["idempotency_key"], m["body"] = agent24KeyN(i), "正文 "+agent24KeyN(i)
		}))
		require.Equal(t, http.StatusOK, r.code, r.raw)
		ids = append(ids, r.body["candidate_id"].(string))
	}
	over := agent24CandidateCall(t, e.h.Submit, alice, agent24Submit(func(m map[string]any) {
		m["idempotency_key"], m["body"] = agent24KeyN(30), "第 21 份"
	}))
	assert.Equal(t, "forbidden/quota_exceeded", over.outcome())
	dup := agent24CandidateCall(t, e.h.Submit, alice, agent24Submit(func(m map[string]any) {
		m["idempotency_key"], m["body"] = agent24KeyN(31), "正文 "+agent24KeyN(0)
	}))
	assert.Equal(t, map[string]any{"outcome": "existing"}, dup.details())
	bob := agent24CandidateCall(t, e.h.Submit, agent24Proposer(agent24Bob), agent24Submit(nil))
	assert.Equal(t, http.StatusOK, bob.code, "the quota is per principal")
	require.Equal(t, http.StatusOK, agent24CandidateCall(t, e.h.Withdraw, alice,
		`{"request_id":"w","generation":1,"candidate_id":"`+ids[0]+`"}`).code)
	assert.Equal(t, http.StatusOK, agent24CandidateCall(t, e.h.Submit, alice, agent24Submit(func(m map[string]any) {
		m["idempotency_key"], m["body"] = agent24KeyN(32), "第 21 份"
	})).code)
}

// The per-author mutex serialises the quota count with the insert: concurrent submissions never pass 20.
func TestAgent24CandidateSubmitQuotaConcurrent(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	alice := agent24Proposer(agent24Alice)
	var mu sync.Mutex
	e.h.now = func() time.Time { mu.Lock(); defer mu.Unlock(); e.clock = e.clock.Add(time.Second); return e.clock }
	var wg sync.WaitGroup
	codes := make([]int, 30)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			body := agent24Submit(func(m map[string]any) {
				m["idempotency_key"], m["body"] = agent24KeyN(i), "并发 "+agent24KeyN(i)
			})
			c.Request = httptest.NewRequestWithContext(alice.ctx(), http.MethodPost, "/", strings.NewReader(body))
			e.h.Submit(c)
			codes[i] = w.Code
		}()
	}
	wg.Wait()
	ok := 0
	for _, c := range codes {
		if c == http.StatusOK {
			ok++
		} else {
			assert.Equal(t, http.StatusForbidden, c)
		}
	}
	assert.Equal(t, agent24CandidateQuota, ok)
	assert.EqualValues(t, agent24CandidateQuota, e.count(t, "status = ?", types.Agent24CandidateSubmitted))
}

// agent24EntryDoc puts a document in kb and, when entry, an approved candidate bound to it; it returns its current
// source revision.
func (e *agent24CandidateEnv) entryDoc(t *testing.T, id, kb, status string, entry bool) string {
	t.Helper()
	k := &types.Knowledge{
		ID: id, TenantID: 1, KnowledgeBaseID: kb, ParseStatus: status, Type: "file",
		FileHash: "h-" + id, FilePath: "p-" + id, Source: "s",
	}
	require.NoError(t, e.db.Create(k).Error)
	if entry {
		c := &types.Agent24KnowledgeCandidate{
			ID: "approved-" + id, TenantID: 1, Author: "api_external_user:1:carol", TargetKnowledgeBaseID: kb,
			Mode: types.Agent24CandidateCreate, ContentSHA256: strings.Repeat("e", 64), Kind: "sop",
			SessionRef: strings.Repeat("a", 64), SkillVersion: "s", RedactionRulesVersion: 1,
			Status: types.Agent24CandidateSubmitted, SubmittedAt: e.clock,
		}
		require.NoError(t, e.store.CreateCandidate(context.Background(), c))
		moved, err := e.store.TransitionCandidate(context.Background(), 1, c.ID, types.Agent24CandidateTransition{
			To: types.Agent24CandidateApproved, KnowledgeID: id, Approver: "api_external_user:1:admin",
		}, e.clock)
		require.NoError(t, err)
		require.True(t, moved)
	}
	return Agent24IngestSourceRevision(agent24CandidateTestKey)(k)
}

// W8.2 ⑤3 (F1 submission side): every way the target is unusable is the same not_found, byte for byte (N17);
// an ordinary document is forbidden; a moved revision is stale with the current one.
func TestAgent24CandidateSubmitRevise(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	alice := agent24Proposer(agent24Alice)
	rev := e.entryDoc(t, "docE", "kbA", types.ParseStatusCompleted, true)
	revB := e.entryDoc(t, "docB", "kbB", types.ParseStatusCompleted, true)
	revW := e.entryDoc(t, "docW", "kbA", types.ParseStatusCompleted, true)
	revD := e.entryDoc(t, "docD", "kbA", types.ParseStatusDeleting, true)
	revP := e.entryDoc(t, "docP", "kbA", types.ParseStatusCompleted, false)
	ok, err := e.store.Withdraw(context.Background(), &types.Agent24EntryWithdrawal{
		TenantID: 1, KnowledgeID: "docW", WithdrawnBy: "x", WithdrawnAt: e.clock,
	})
	require.NoError(t, err)
	require.True(t, ok)
	revise := func(doc, base string) string {
		return agent24Submit(func(m map[string]any) {
			m["mode"], m["knowledge_id"], m["expected_source_revision"] = "revise", doc, base
			m["idempotency_key"] = agent24KeyN(len(doc) + len(base))
		})
	}
	var notFound []string
	for _, tc := range []struct{ doc, base string }{
		{"nope", "r"}, {"docB", revB}, {"docW", revW}, {"docD", revD},
	} {
		r := agent24CandidateCall(t, e.h.Submit, alice, revise(tc.doc, tc.base))
		assert.Equal(t, "not_found", r.outcome(), tc.doc)
		notFound = append(notFound, r.raw)
	}
	for _, raw := range notFound[1:] {
		assert.Equal(t, notFound[0], raw)
	}
	assert.Equal(t, "forbidden/operation_not_allowed",
		agent24CandidateCall(t, e.h.Submit, alice, revise("docP", revP)).outcome())
	stale := agent24CandidateCall(t, e.h.Submit, alice, revise("docE", "old-revision"))
	assert.Equal(t, "stale/base_revision_changed", stale.outcome())
	assert.Equal(t, rev, stale.details()["current_revision"])
	assert.Zero(t, e.count(t, "status = ?", types.Agent24CandidateSubmitted))

	r := agent24CandidateCall(t, e.h.Submit, alice, revise("docE", rev))
	require.Equal(t, http.StatusOK, r.code, r.raw)
	cand, err := e.store.GetCandidate(context.Background(), 1, r.body["candidate_id"].(string))
	require.NoError(t, err)
	assert.Equal(t, []any{types.Agent24CandidateRevise, "docE", rev},
		[]any{cand.Mode, cand.KnowledgeID, cand.BaseRevision})
	assert.Equal(t, "docE", r.audit().KnowledgeID)
}

// F2: a forged approval is stripped, recorded, and changes nothing.
func TestAgent24CandidateSubmitStripsForgedApproval(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	r := agent24CandidateCall(t, e.h.Submit, agent24Proposer(agent24Alice), agent24Submit(func(m map[string]any) {
		m["approved"], m["approver"] = true, "api_external_user:1:admin"
	}))
	require.Equal(t, http.StatusOK, r.code, r.raw)
	assert.Equal(t, "submitted", r.body["status"])
	assert.Equal(t, []string{"approved", "approver"}, r.audit().StrippedFields)
	refused := agent24CandidateCall(t, e.h.Submit, agent24Proposer(agent24Alice), agent24Submit(func(m map[string]any) {
		m["author"], m["kind"], m["idempotency_key"] = "x", "howto", agent24KeyN(5)
	}))
	assert.Equal(t, "invalid_request", refused.outcome())
	assert.Equal(t, []string{"author"}, refused.audit().StrippedFields)
	assert.Empty(t, refused.audit().Kind, "an unknown kind is not recorded")
}

func TestAgent24CandidateSubmitDisabled(t *testing.T) {
	h := newAgent24KnowledgeCandidatesHandler(agent24CandidateTestKey, nil, agent24EveryKB{})
	alice := agent24Proposer(agent24Alice)
	assert.Equal(t, "forbidden/operation_disabled",
		agent24CandidateCall(t, h.Submit, alice, agent24Submit(nil)).outcome())
	assert.Equal(t, "invalid_request", agent24CandidateCall(t, h.Submit, alice, `{}`).outcome(), "② first")
	ref := `{"request_id":"r","generation":0,"candidate_id":"c"}`
	assert.Equal(t, "forbidden/operation_disabled", agent24CandidateCall(t, h.Withdraw, alice, ref).outcome())
	assert.Equal(t, "forbidden/operation_disabled", agent24CandidateCall(t, h.Get, alice, ref).outcome())
	assert.Nil(t, NewAgent24KnowledgeCandidatesHandler(nil, nil), "outside integration mode the routes stay off")
}

// A record another writer committed between this request's read and its write replays; none visible is
// request_in_progress; another fingerprint is reuse.
func TestAgent24CandidateSubmitIdempotencyRace(t *testing.T) {
	alice := agent24Proposer(agent24Alice)
	sub, _, kerr := parseAgent24CandidateSubmit([]byte(agent24Submit(nil)))
	require.Nil(t, kerr)
	for _, tc := range []struct {
		name        string
		fingerprint string
		write       bool
		want        string
	}{
		{"replay", agent24SubmitFingerprint(sub), true, "ok"},
		{"in progress", "", false, "unavailable/request_in_progress"},
		{"reuse", "other", true, "invalid_request/idempotency_key_reused"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newAgent24CandidateEnv(t)
			e.h.commit = func(ctx context.Context, _ func(*repository.Agent24CandidateStore) error) error {
				if tc.write {
					require.NoError(t, e.store.PutIdempotency(ctx, &types.Agent24WriteIdempotency{
						TenantID: 1, Principal: agent24Alice.StorageID(), Op: "candidate.propose",
						Key: sub.IdempotencyKey, Fingerprint: tc.fingerprint, StatusCode: 200,
						Response:  `{"candidate_id":"c-first","status":"submitted"}`,
						CreatedAt: e.clock, ExpiresAt: e.clock.Add(time.Hour),
					}))
				}
				return errAgent24IdempotencyRace
			}
			r := agent24CandidateCall(t, e.h.Submit, alice, agent24Submit(nil))
			assert.Equal(t, tc.want, r.outcome())
			switch tc.want {
			case "ok":
				assert.Equal(t, []any{"c-first", map[string]any{"replayed": true}},
					[]any{r.body["candidate_id"], r.details()})
			case "unavailable/request_in_progress":
				assert.Equal(t, []any{true, float64(agent24RequestInProgressRetryMS)},
					[]any{r.details()["retryable"], r.details()["retry_after_ms"]})
			}
		})
	}
}

// Another process inserted the same content between the dedup lookup and the insert: the answer is that candidate.
func TestAgent24CandidateSubmitDuplicateInsertRace(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	var once sync.Once
	rival := &types.Agent24KnowledgeCandidate{
		ID: "rival", TenantID: 1, Author: agent24Alice.StorageID(), TargetKnowledgeBaseID: "kbA",
		Mode:          types.Agent24CandidateCreate,
		ContentSHA256: "cd192d152a12cfe33ea1ae7687e0653d911e24971edfc4bca2146cf8bbfbf30f",
		Kind:          "sop", Title: "t", SessionRef: strings.Repeat("b", 64), SkillVersion: "s",
		RedactionRulesVersion: 1, Status: types.Agent24CandidateSubmitted, SubmittedAt: e.clock,
	}
	// right after the dedup lookup, in the same transaction
	require.NoError(t, e.db.Callback().Query().After("gorm:query").Register("agent24_test_race", func(db *gorm.DB) {
		if db.Statement.Table != "agent24_knowledge_candidates" {
			return
		}
		once.Do(func() {
			tx := db.Session(&gorm.Session{NewDB: true})
			tx.Error = nil // the lookup's own "record not found" is copied into the session
			require.NoError(t, tx.Create(rival).Error)
		})
	}))
	r := agent24CandidateCall(t, e.h.Submit, agent24Proposer(agent24Alice), agent24Submit(nil))
	require.Equal(t, http.StatusOK, r.code, r.raw)
	assert.Equal(t, []any{"rival", map[string]any{"outcome": "existing"}}, []any{r.body["candidate_id"], r.details()})
}

func TestAgent24CandidateSubmitStorageErrors(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	e.h.commit = func(context.Context, func(*repository.Agent24CandidateStore) error) error {
		return errors.New("SECRET-db")
	}
	r := agent24CandidateCall(t, e.h.Submit, agent24Proposer(agent24Alice), agent24Submit(nil))
	assert.Equal(t, "unavailable/dependency", r.outcome())
	assert.NotContains(t, r.raw, "SECRET")
	e.h.newID = func() (string, error) { return "", errors.New("SECRET-rand") }
	e.h.commit = nil
	r = agent24CandidateCall(t, e.h.Submit, agent24Proposer(agent24Alice), agent24Submit(nil))
	assert.Equal(t, "unavailable/dependency", r.outcome())
	assert.Zero(t, e.count(t, "1 = 1"))
}

func (e *agent24CandidateEnv) submit(t *testing.T, as agent24CandidateAs, mut func(map[string]any)) string {
	t.Helper()
	r := agent24CandidateCall(t, e.h.Submit, as, agent24Submit(mut))
	require.Equal(t, http.StatusOK, r.code, r.raw)
	return r.body["candidate_id"].(string)
}

func agent24Ref(id string) string {
	return `{"request_id":"r9","generation":5,"candidate_id":"` + id + `"}`
}

// W8.8: the author withdraws their own submitted candidate; again is already: true; anything not submitted is
// stale/candidate_not_submitted; someone else's, a missing one, and one whose knowledge base left E are not_found.
func TestAgent24CandidateWithdraw(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	sch := agent24CandidateSchema(t, "withdraw_response")
	alice := agent24Proposer(agent24Alice)
	id := e.submit(t, alice, nil)

	var notFound []string
	for name, as := range map[string]agent24CandidateAs{
		"another author": agent24Proposer(agent24Bob),
		"target left E": func() agent24CandidateAs {
			a := alice
			a.scope.KnowledgeBaseIDs = types.StringArray{"kbB"}
			return a
		}(),
	} {
		r := agent24CandidateCall(t, e.h.Withdraw, as, agent24Ref(id))
		assert.Equal(t, "not_found", r.outcome(), name)
		notFound = append(notFound, r.raw)
	}
	missing := agent24CandidateCall(t, e.h.Withdraw, alice, agent24Ref("missing"))
	assert.Equal(t, "not_found", missing.outcome())
	for _, raw := range notFound {
		assert.Equal(t, missing.raw, raw)
	}
	noCap := alice
	noCap.scope.Capabilities = types.StringArray{"retrieve"}
	assert.Equal(t, "forbidden/operation_not_allowed",
		agent24CandidateCall(t, e.h.Withdraw, noCap, agent24Ref(id)).outcome())

	r := agent24CandidateCall(t, e.h.Withdraw, alice, agent24Ref(id))
	require.Equal(t, http.StatusOK, r.code, r.raw)
	require.NoError(t, agent24Validate(sch, []byte(r.raw)), r.raw)
	assert.Equal(t, []any{"withdrawn", id, "r9", 5.0, nil}, []any{
		r.body["status"], r.body["candidate_id"],
		r.body["request_id"], r.body["generation"], r.body["details"],
	})
	a := r.audit()
	assert.Equal(t, []any{id, "kbA", false}, []any{a.CandidateID, a.TargetKnowledgeBaseID, a.Already})
	again := agent24CandidateCall(t, e.h.Withdraw, alice, agent24Ref(id))
	require.NoError(t, agent24Validate(sch, []byte(again.raw)), again.raw)
	assert.Equal(t, map[string]any{"already": true}, again.details())
	assert.True(t, again.audit().Already)
	cand, err := e.store.GetCandidate(context.Background(), 1, id)
	require.NoError(t, err)
	assert.Equal(t, types.Agent24CandidateWithdrawn, cand.Status)

	for i, to := range []types.Agent24CandidateTransition{
		{To: types.Agent24CandidateApproved, KnowledgeID: "d", Approver: "x"},
		{To: types.Agent24CandidateReturned, ReturnReason: types.Agent24ReturnReviewer},
	} {
		other := e.submit(t, alice, func(m map[string]any) {
			m["idempotency_key"], m["body"] = agent24KeyN(10+i), string(to.To)
		})
		moved, err := e.store.TransitionCandidate(context.Background(), 1, other, to, e.clock)
		require.NoError(t, err)
		require.True(t, moved)
		assert.Equal(t, "stale/candidate_not_submitted",
			agent24CandidateCall(t, e.h.Withdraw, alice, agent24Ref(other)).outcome(), to.To)
	}
}

// W8.3: the author reads their own candidate as the ops-v2 view; nobody else can tell it exists.
func TestAgent24CandidateGet(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	view := agent24CandidateSchema(t, "view")
	alice := agent24Proposer(agent24Alice)
	id := e.submit(t, alice, nil)
	r := agent24CandidateCall(t, e.h.Get, alice, agent24Ref(id))
	require.Equal(t, http.StatusOK, r.code, r.raw)
	assert.Equal(t, []any{"r9", 5.0}, []any{r.body["request_id"], r.body["generation"]})
	assert.True(t, strings.HasPrefix(r.body["authz_version"].(string), "az1_"))
	v := r.body["candidate"].(map[string]any)
	raw, _ := json.Marshal(v)
	require.NoError(t, agent24Validate(view, raw), string(raw))
	assert.Equal(t, map[string]any{
		"candidate_id": id, "status": "submitted", "kind": "sop", "title": "请假流程 🌿",
		"body":                     "## 步骤\n\n1. 打开「人事」系统\n2. 提交申请 🙂\n\n> 审批人：直属主管",
		"content_sha256":           "cd192d152a12cfe33ea1ae7687e0653d911e24971edfc4bca2146cf8bbfbf30f",
		"target_knowledge_base_id": "kbA", "mode": "create",
		"submitted_at": "2026-11-09T10:00:01Z", "updated_at": "2026-11-09T10:00:01Z",
	}, v)
	bob := agent24CandidateCall(t, e.h.Get, agent24Proposer(agent24Bob), agent24Ref(id))
	assert.Equal(t, "not_found", bob.outcome())
	assert.Equal(t, "not_found", agent24CandidateCall(t, e.h.Get, alice, agent24Ref("missing")).outcome())

	// returned, then purged (W8.9): the view drops title, body and note and keeps the rest
	moved, err := e.store.TransitionCandidate(context.Background(), 1, id, types.Agent24CandidateTransition{
		To: types.Agent24CandidateReturned, ReturnReason: types.Agent24ReturnReviewer, Note: "请补充步骤",
	}, e.clock.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, moved)
	r = agent24CandidateCall(t, e.h.Get, alice, agent24Ref(id))
	v = r.body["candidate"].(map[string]any)
	raw, _ = json.Marshal(v)
	require.NoError(t, agent24Validate(view, raw), string(raw))
	assert.Equal(t, []any{"returned", "reviewer", "请补充步骤", "2026-11-09T10:01:00Z"},
		[]any{v["status"], v["return_reason"], v["note"], v["updated_at"]})
	_, err = e.store.PurgeCandidateContent(context.Background(), e.clock.Add(2*time.Hour), e.clock.Add(2*time.Hour))
	require.NoError(t, err)
	r = agent24CandidateCall(t, e.h.Get, alice, agent24Ref(id))
	v = r.body["candidate"].(map[string]any)
	raw, _ = json.Marshal(v)
	require.NoError(t, agent24Validate(view, raw), string(raw))
	for _, k := range []string{"title", "body", "note"} {
		assert.NotContains(t, v, k)
	}
	assert.Equal(t, "reviewer", v["return_reason"])
}

func TestAgent24CandidateViewShapes(t *testing.T) {
	view := agent24CandidateSchema(t, "view")
	at := time.Date(2026, 11, 9, 10, 0, 0, 123000000, time.UTC)
	later := at.Add(time.Hour)
	base := types.Agent24KnowledgeCandidate{
		ID: "c", TargetKnowledgeBaseID: "kb", Mode: types.Agent24CandidateRevise, KnowledgeID: "d", BaseRevision: "r",
		ContentSHA256: strings.Repeat("a", 64), Kind: "faq", Title: "t", Body: "", SubmittedAt: at,
	}
	approved := base
	approved.Status, approved.SourceRevision = types.Agent24CandidateApproved, "r2"
	approved.AuthorIsApprover, approved.TerminalAt = true, &later
	approved.Note = "ignored"
	withdrawn := base
	withdrawn.Mode, withdrawn.KnowledgeID, withdrawn.BaseRevision = types.Agent24CandidateCreate, "", ""
	withdrawn.Status, withdrawn.TerminalAt = types.Agent24CandidateWithdrawn, &later
	submitted := base
	submitted.Status = types.Agent24CandidateSubmitted
	for _, c := range []types.Agent24KnowledgeCandidate{approved, withdrawn, submitted} {
		v := agent24CandidateView(&c)
		raw, _ := json.Marshal(v)
		require.NoError(t, agent24Validate(view, raw), string(raw))
	}
	v := agent24CandidateView(&approved)
	assert.Equal(t, []any{"r2", true, "d", "r", "2026-11-09T11:00:00.123Z", "", nil},
		[]any{
			v["source_revision"], v["author_is_approver"], v["knowledge_id"], v["base_source_revision"],
			v["updated_at"], v["body"], v["note"],
		})
	v = agent24CandidateView(&withdrawn)
	assert.NotContains(t, v, "knowledge_id")
	assert.NotContains(t, v, "base_source_revision")
	assert.NotContains(t, v, "source_revision")
	assert.Equal(t, "2026-11-09T10:00:00.123Z", agent24CandidateView(&submitted)["updated_at"])
}

// W1.2: authz_version follows the credential and the principal, and nothing else.
func TestAgent24AuthzVersion(t *testing.T) {
	alice := agent24Proposer(agent24Alice)
	ver := func(as agent24CandidateAs, extra ...string) string {
		caller, ok := agent24CandidateCallerOf(as.ctx())
		require.True(t, ok)
		return agent24AuthzVersion(as.ctx(), caller, extra...)
	}
	v := ver(alice)
	assert.Equal(t, v, ver(alice))
	reordered := alice
	reordered.scope.KnowledgeBaseIDs = types.StringArray{"kbB", "kbA"}
	reordered.scope.Capabilities = types.StringArray{"kb_candidate_propose", "retrieve"}
	assert.Equal(t, v, ver(reordered), "order is not a change")
	for name, as := range map[string]agent24CandidateAs{
		"principal": agent24Proposer(agent24Bob),
		"key":       func() agent24CandidateAs { a := alice; a.scope.KeyID = 10; return a }(),
		"kbs": func() agent24CandidateAs {
			a := alice
			a.scope.KnowledgeBaseIDs = types.StringArray{"kbA"}
			return a
		}(),
		"caps": func() agent24CandidateAs {
			a := alice
			a.scope.Capabilities = types.StringArray{"retrieve", "kb_candidate_propose", "kb_candidate_review"}
			return a
		}(),
		"full":   func() agent24CandidateAs { a := alice; a.scope.FullAccess = true; return a }(),
		"tenant": func() agent24CandidateAs { a := alice; a.tenant = 2; return a }(),
	} {
		assert.NotEqual(t, v, ver(as), name)
	}
	assert.NotEqual(t, v, ver(alice, "grant"))
	assert.LessOrEqual(t, len(v), 128)
}

func TestAgent24KeyedMutex(t *testing.T) {
	k := newAgent24KeyedMutex()
	unlock := k.Lock("a")
	got := make(chan struct{})
	go func() { u := k.Lock("a"); close(got); u() }()
	other := k.Lock("b") // another key is not blocked
	other()
	select {
	case <-got:
		t.Fatal("the same key was taken twice")
	case <-time.After(20 * time.Millisecond):
	}
	unlock()
	<-got
	k.mu.Lock()
	defer k.mu.Unlock()
	assert.Empty(t, k.locks, "no lock is kept once released")
}

// A24-KB-43: the three routes are audited, each under its own action.
func TestAgent24CandidateAuditActions(t *testing.T) {
	for path, want := range map[string]types.AuditAction{
		"/api/v1/agent24/knowledge/candidates":          "knowledge.candidate.submit",
		"/api/v1/agent24/knowledge/candidates/withdraw": "knowledge.candidate.withdraw",
		"/api/v1/agent24/knowledge/candidates/get":      "knowledge.candidate.read",
	} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, path, nil)
		assert.True(t, agent24KnowledgeAuditPath(c), path)
		assert.Equal(t, want, agent24AuditAction(c), path)
		c.Request = httptest.NewRequest(http.MethodGet, path, nil)
		assert.False(t, agent24KnowledgeAuditPath(c), "GET "+path)
	}
}

// The conditional write lost to a concurrent withdrawal of the same candidate: the state is read again, so the
// answer is already: true, not stale.
func TestAgent24CandidateWithdrawRace(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	alice := agent24Proposer(agent24Alice)
	id := e.submit(t, alice, nil)
	var once sync.Once
	require.NoError(t, e.db.Callback().Update().Before("gorm:update").Register("agent24_test_withdraw_race",
		func(db *gorm.DB) {
			if db.Statement.Table != "agent24_knowledge_candidates" {
				return
			}
			once.Do(func() {
				tx := db.Session(&gorm.Session{NewDB: true})
				tx.Error = nil
				require.NoError(t, tx.Exec("UPDATE agent24_knowledge_candidates SET status = ? WHERE id = ?",
					types.Agent24CandidateWithdrawn, id).Error)
			})
		}))
	r := agent24CandidateCall(t, e.h.Withdraw, alice, agent24Ref(id))
	require.Equal(t, http.StatusOK, r.code, r.raw)
	assert.Equal(t, map[string]any{"already": true}, r.details())
}
