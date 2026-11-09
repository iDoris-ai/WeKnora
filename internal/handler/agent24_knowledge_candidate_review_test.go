package handler

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
)

func agent24Reviewer(p types.Principal) agent24CandidateAs {
	return agent24CandidateAs{principal: p, tenant: 1, scope: types.TenantAPIKeyScope{
		KeyID: 11, KnowledgeBaseIDs: types.StringArray{"kbA", "kbB"},
		Capabilities: types.StringArray{"retrieve", "kb_candidate_review"},
	}}
}

func agent24ReviewJSON(t *testing.T, m map[string]any) string {
	t.Helper()
	b, err := json.Marshal(m)
	require.NoError(t, err)
	return string(b)
}

func agent24ReviewGetBody(id string) string {
	return `{"request_id":"same","candidate_id":"` + id + `"}`
}

func (e *agent24CandidateEnv) grantAdmin(t *testing.T, p types.Principal, kb string, revoked bool) {
	t.Helper()
	row := &types.Agent24KnowledgeAdmin{
		TenantID: 1, Principal: p.StorageID(), KnowledgeBaseID: kb, CreatedAt: e.clock,
	}
	if revoked {
		at := e.clock
		row.RevokedAt = &at
	}
	require.NoError(t, e.db.Create(row).Error)
}

func (e *agent24CandidateEnv) clearAdmins(t *testing.T) {
	t.Helper()
	require.NoError(t, e.db.Where("tenant_id = ?", 1).Delete(&types.Agent24KnowledgeAdmin{}).Error)
}

func (e *agent24CandidateEnv) idempotencyRows(t *testing.T) int64 {
	t.Helper()
	var n int64
	require.NoError(t, e.db.Model(&types.Agent24WriteIdempotency{}).Count(&n).Error)
	return n
}

func agent24ReviewIDs(t *testing.T, sch *jsonschema.Schema, r agent24CandidateReply) []string {
	t.Helper()
	items, ok := r.body["candidates"].([]any)
	require.True(t, ok, r.raw)
	ids := make([]string, len(items))
	for i, item := range items {
		raw, err := json.Marshal(item)
		require.NoError(t, err)
		require.NoError(t, agent24Validate(sch, raw), string(raw))
		v := item.(map[string]any)
		assert.NotContains(t, v, "approver")
		ids[i] = v["candidate_id"].(string)
	}
	return ids
}

func agent24AssertNoGeneration(t *testing.T, r agent24CandidateReply) {
	t.Helper()
	_, ok := r.body["generation"]
	assert.False(t, ok, r.raw)
}

// D1 and P3: review is the credential capability intersected with an unrevoked organisation-administrator grant,
// and only on knowledge bases inside E.
func TestAgent24CandidateReviewGrants(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	view := agent24CandidateSchema(t, "view")
	bob := agent24Proposer(agent24Bob)
	idA := e.submit(t, bob, nil)
	idB := e.submit(t, bob, func(m map[string]any) {
		m["idempotency_key"] = agent24KeyN(2)
		m["target_knowledge_base_id"] = "kbB"
		m["body"] = "kbB 的正文"
	})
	h := e.h
	forbid := func(as agent24CandidateAs) {
		t.Helper()
		assert.Equal(t, "forbidden/operation_not_allowed",
			agent24CandidateCall(t, h.ReviewList, as, `{"request_id":"r-list"}`).outcome())
		assert.Equal(t, "forbidden/operation_not_allowed",
			agent24CandidateCall(t, h.ReviewGet, as, agent24ReviewGetBody(idA)).outcome())
		assert.Equal(t, "forbidden/operation_not_allowed",
			agent24CandidateCall(t, h.ReviewReturn, as, agent24ReviewJSON(t, map[string]any{
				"request_id": "r-ret", "candidate_id": idA, "note": "n",
			})).outcome())
	}
	forbid(agent24Reviewer(agent24Bob)) // review capability, no administrator row

	e.grantAdmin(t, agent24Alice, "kbA", false)
	noCap := agent24Reviewer(agent24Alice)
	noCap.scope.Capabilities = types.StringArray{"retrieve", "kb_candidate_propose"}
	forbid(noCap)

	alice := agent24Reviewer(agent24Alice)
	listed := agent24CandidateCall(t, h.ReviewList, alice, `{"request_id":"r-list"}`)
	require.Equal(t, http.StatusOK, listed.code, listed.raw)
	agent24AssertNoGeneration(t, listed)
	assert.Equal(t, []string{idA}, agent24ReviewIDs(t, view, listed))
	assert.Nil(t, listed.body["next"])
	assert.Equal(t, "请假流程 🌿", listed.body["candidates"].([]any)[0].(map[string]any)["title"])
	assert.Contains(t, listed.body["candidates"].([]any)[0].(map[string]any)["body"], "直属主管")

	dup := alice
	dup.scope.KnowledgeBaseIDs = types.StringArray{"kbA", "kbA", "kbB"}
	duped := agent24CandidateCall(t, h.ReviewList, dup, `{"request_id":"r-list"}`)
	require.Equal(t, http.StatusOK, duped.code, duped.raw)
	assert.Equal(t, []string{idA}, agent24ReviewIDs(t, view, duped))

	hidden := agent24CandidateCall(t, h.ReviewGet, alice, agent24ReviewGetBody(idB))
	missing := agent24CandidateCall(t, h.ReviewGet, alice, agent24ReviewGetBody("missing-id"))
	assert.Equal(t, "not_found", hidden.outcome())
	assert.Equal(t, "not_found", missing.outcome())
	assert.Equal(t, missing.raw, hidden.raw)

	got := agent24CandidateCall(t, h.ReviewGet, alice, agent24ReviewGetBody(idA))
	require.Equal(t, http.StatusOK, got.code, got.raw)
	agent24AssertNoGeneration(t, got)
	cand := got.body["candidate"].(map[string]any)
	raw, err := json.Marshal(cand)
	require.NoError(t, err)
	require.NoError(t, agent24Validate(view, raw), string(raw))
	assert.Equal(t, idA, cand["candidate_id"])

	full := alice
	full.scope.FullAccess = true
	fullList := agent24CandidateCall(t, h.ReviewList, full, `{"request_id":"r-list"}`)
	require.Equal(t, http.StatusOK, fullList.code, fullList.raw)
	assert.Empty(t, agent24ReviewIDs(t, view, fullList))
	assert.Equal(t, "not_found", agent24CandidateCall(t, h.ReviewGet, full, agent24ReviewGetBody(idA)).outcome())

	emptyE := alice
	emptyE.scope.KnowledgeBaseIDs = nil
	// a malformed cursor is a shape error even when nothing is reviewable
	assert.Equal(t, "invalid_request", agent24CandidateCall(t, h.ReviewList, emptyE,
		`{"request_id":"r-list","after":"not-a-cursor!"}`).outcome())
	emptyList := agent24CandidateCall(t, h.ReviewList, emptyE, `{"request_id":"r-list"}`)
	require.Equal(t, http.StatusOK, emptyList.code, emptyList.raw)
	assert.Empty(t, agent24ReviewIDs(t, view, emptyList))
	assert.Nil(t, emptyList.body["next"])
	assert.Equal(t, "not_found", agent24CandidateCall(t, h.ReviewGet, emptyE, agent24ReviewGetBody(idA)).outcome())

	e.clearAdmins(t)
	e.grantAdmin(t, agent24Alice, types.Agent24KnowledgeAdminAnyKB, false)
	star := agent24CandidateCall(t, h.ReviewList, dup, `{"request_id":"r-list"}`)
	require.Equal(t, http.StatusOK, star.code, star.raw)
	assert.Equal(t, []string{idA, idB}, agent24ReviewIDs(t, view, star))
	require.Equal(t, http.StatusOK, agent24CandidateCall(t, h.ReviewGet, alice, agent24ReviewGetBody(idB)).code)

	at := e.clock
	require.NoError(t, e.db.Model(&types.Agent24KnowledgeAdmin{}).Where("tenant_id = ?", 1).
		Update("revoked_at", at).Error)
	forbid(alice)
}

func TestAgent24CandidateReviewListPage(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	view := agent24CandidateSchema(t, "view")
	e.grantAdmin(t, agent24Alice, "kbA", false)
	alice := agent24Reviewer(agent24Alice)
	bob := agent24Proposer(agent24Bob)
	first := e.submit(t, bob, func(m map[string]any) { m["idempotency_key"] = agent24KeyN(3) })
	second := e.submit(t, bob, func(m map[string]any) {
		m["idempotency_key"] = agent24KeyN(4)
		m["body"] = "第二份"
	})
	all := agent24CandidateCall(t, e.h.ReviewList, alice, `{"request_id":"r-list"}`)
	require.Equal(t, http.StatusOK, all.code, all.raw)
	agent24AssertNoGeneration(t, all)
	assert.Equal(t, []string{first, second}, agent24ReviewIDs(t, view, all))
	assert.Nil(t, all.body["next"])
	assert.True(t, strings.HasPrefix(all.body["authz_version"].(string), "az1_"))

	page := agent24CandidateCall(t, e.h.ReviewList, alice, `{"request_id":"r-page","limit":1}`)
	require.Equal(t, http.StatusOK, page.code, page.raw)
	assert.Equal(t, []string{first}, agent24ReviewIDs(t, view, page))
	next, ok := page.body["next"].(string)
	require.True(t, ok)
	require.NotEmpty(t, next)
	rest := agent24CandidateCall(t, e.h.ReviewList, alice, agent24ReviewJSON(t, map[string]any{
		"request_id": "r-rest", "after": next, "limit": 1,
	}))
	require.Equal(t, http.StatusOK, rest.code, rest.raw)
	assert.Equal(t, []string{second}, agent24ReviewIDs(t, view, rest))
	assert.Nil(t, rest.body["next"])

	for _, body := range []string{
		`{"request_id":"r","limit":1.0}`,
		`{"request_id":"r","limit":1e0}`,
	} {
		one := agent24CandidateCall(t, e.h.ReviewList, alice, body)
		require.Equal(t, http.StatusOK, one.code, one.raw)
		assert.Equal(t, []string{first}, agent24ReviewIDs(t, view, one))
	}
	for _, body := range []string{
		`{"request_id":"r","after":"not-a-cursor!"}`,
		`{"request_id":"r","after":"` + base64.RawURLEncoding.EncodeToString([]byte("noseparator")) + `"}`,
		`{"request_id":"r","after":"` + base64.RawURLEncoding.EncodeToString([]byte("not-a-time|id")) + `"}`,
		`{"request_id":"r","limit":0}`,
		`{"request_id":"r","limit":101}`,
		`{"request_id":"r","limit":1.5}`,
		`{"request_id":"r","after":1}`,
		`{"request_id":"r","after":null}`,
	} {
		assert.Equal(t, "invalid_request", agent24CandidateCall(t, e.h.ReviewList, alice, body).outcome(), body)
	}
}

func TestAgent24CandidateReviewDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := newAgent24KnowledgeCandidatesHandler(agent24CandidateTestKey, nil, agent24EveryKB{})
	alice := agent24Reviewer(agent24Alice)
	assert.Equal(t, "invalid_request", agent24CandidateCall(t, h.ReviewList, alice, `{}`).outcome())
	assert.Equal(t, "invalid_request", agent24CandidateCall(t, h.ReviewGet, alice, `{}`).outcome())
	assert.Equal(t, "invalid_request", agent24CandidateCall(t, h.ReviewReturn, alice, `{}`).outcome())
	assert.Equal(t, "forbidden/operation_disabled",
		agent24CandidateCall(t, h.ReviewList, alice, `{"request_id":"r-list"}`).outcome())
	assert.Equal(t, "forbidden/operation_disabled",
		agent24CandidateCall(t, h.ReviewGet, alice, agent24ReviewGetBody("cand-1")).outcome())
	assert.Equal(t, "forbidden/operation_disabled",
		agent24CandidateCall(t, h.ReviewReturn, alice,
			`{"request_id":"r","candidate_id":"cand-1","note":"n"}`).outcome())
	assert.Equal(t, "invalid_request", agent24CandidateCall(t, h.ReviewReturn, alice,
		`{"request_id":"r","candidate_id":"cand-1","generation":3}`).outcome())
	nobody := alice
	nobody.principal = types.Principal{}
	// disabled is ② and comes before any caller check (W1.9), for all three
	assert.Equal(t, "forbidden/operation_disabled",
		agent24CandidateCall(t, h.ReviewList, nobody, `{"request_id":"r-list"}`).outcome())
	assert.Equal(t, "forbidden/operation_disabled",
		agent24CandidateCall(t, h.ReviewGet, nobody, agent24ReviewGetBody("cand-1")).outcome())
	assert.Equal(t, "forbidden/operation_disabled",
		agent24CandidateCall(t, h.ReviewReturn, nobody,
			`{"request_id":"r","candidate_id":"cand-1","note":"n"}`).outcome())
}

func TestAgent24CandidateReviewReturn(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	view := agent24CandidateSchema(t, "view")
	resp := agent24CandidateSchema(t, "return_response")
	e.grantAdmin(t, agent24Alice, types.Agent24KnowledgeAdminAnyKB, false)
	alice := agent24Reviewer(agent24Alice)
	bob := agent24Proposer(agent24Bob)
	id := e.submit(t, bob, nil)
	ret := func(note any, extra map[string]any) agent24CandidateReply {
		t.Helper()
		m := map[string]any{"request_id": "r-ret", "candidate_id": id}
		if note != nil || extra == nil {
			m["note"] = note
		}
		for k, v := range extra {
			m[k] = v
		}
		return agent24CandidateCall(t, e.h.ReviewReturn, alice, agent24ReviewJSON(t, m))
	}
	before := e.idempotencyRows(t)
	longHan := strings.Repeat("审", 400)
	require.Greater(t, len(longHan), agent24ReviewNoteMaxBytes)
	for _, note := range []any{longHan, strings.Repeat("a", agent24ReviewNoteMaxBytes+1), "", "a\rb", nil} {
		assert.Equal(t, "invalid_request", ret(note, nil).outcome(), note)
	}
	assert.Equal(t, "invalid_request", ret("ok", map[string]any{"generation": 3}).outcome())
	assert.Equal(t, before, e.idempotencyRows(t))
	cand, err := e.store.GetCandidate(context.Background(), 1, id)
	require.NoError(t, err)
	require.Equal(t, types.Agent24CandidateSubmitted, cand.Status)

	side := e.submit(t, bob, func(m map[string]any) {
		m["idempotency_key"] = agent24KeyN(5)
		m["body"] = "边界"
	})
	exact := agent24CandidateCall(t, e.h.ReviewReturn, alice, agent24ReviewJSON(t, map[string]any{
		"request_id": "r-exact", "candidate_id": side, "note": strings.Repeat("a", agent24ReviewNoteMaxBytes),
	}))
	require.Equal(t, http.StatusOK, exact.code, exact.raw)
	require.NoError(t, agent24Validate(resp, []byte(exact.raw)), exact.raw)

	note := "请补充审批人"
	before = e.idempotencyRows(t)
	ok := ret(note, map[string]any{"approved": true, "author": "x", "approver": "y"})
	require.Equal(t, http.StatusOK, ok.code, ok.raw)
	require.NoError(t, agent24Validate(resp, []byte(ok.raw)), ok.raw)
	agent24AssertNoGeneration(t, ok)
	assert.Nil(t, ok.body["details"])
	assert.Equal(t, []any{"returned", "reviewer", id, "r-ret"}, []any{
		ok.body["status"], ok.body["return_reason"], ok.body["candidate_id"], ok.body["request_id"],
	})
	assert.Equal(t, []string{"approved", "author", "approver"}, ok.audit().StrippedFields)
	cand, err = e.store.GetCandidate(context.Background(), 1, id)
	require.NoError(t, err)
	assert.Equal(t, []any{
		types.Agent24CandidateReturned, types.Agent24ReturnReviewer, note, agent24Alice.StorageID(),
	}, []any{cand.Status, cand.ReturnReason, cand.Note, cand.Approver})
	author := agent24CandidateCall(t, e.h.Get, bob, agent24Ref(id))
	require.Equal(t, http.StatusOK, author.code, author.raw)
	authorView := author.body["candidate"].(map[string]any)
	raw, err := json.Marshal(authorView)
	require.NoError(t, err)
	require.NoError(t, agent24Validate(view, raw), string(raw))
	assert.Equal(t, []any{"returned", "reviewer", note}, []any{
		authorView["status"], authorView["return_reason"], authorView["note"],
	})

	again := ret("另一条说明", nil)
	require.Equal(t, http.StatusOK, again.code, again.raw)
	require.NoError(t, agent24Validate(resp, []byte(again.raw)), again.raw)
	assert.Equal(t, map[string]any{"already": true}, again.details())
	assert.True(t, again.audit().Already)
	cand, err = e.store.GetCandidate(context.Background(), 1, id)
	require.NoError(t, err)
	assert.Equal(t, note, cand.Note)
	assert.Equal(t, before, e.idempotencyRows(t))

	carol := types.Principal{Type: "api_external_user", ID: "1:carol"}
	e.grantAdmin(t, carol, "kbA", false)
	other := agent24CandidateCall(t, e.h.ReviewReturn, agent24Reviewer(carol), agent24ReviewJSON(t, map[string]any{
		"request_id": "r-carol", "candidate_id": id, "note": "别人退回",
	}))
	assert.Equal(t, "stale/candidate_not_submitted", other.outcome())
	assert.Equal(t, before, e.idempotencyRows(t))

	withdrawn := e.submit(t, bob, func(m map[string]any) {
		m["idempotency_key"] = agent24KeyN(6)
		m["body"] = "将撤回"
	})
	require.Equal(t, http.StatusOK, agent24CandidateCall(t, e.h.Withdraw, bob, agent24Ref(withdrawn)).code)
	assert.Equal(t, "stale/candidate_not_submitted", agent24CandidateCall(t, e.h.ReviewReturn, alice,
		agent24ReviewJSON(t, map[string]any{
			"request_id": "r-w", "candidate_id": withdrawn, "note": "退回",
		})).outcome())

	system := e.submit(t, bob, func(m map[string]any) {
		m["idempotency_key"] = agent24KeyN(7)
		m["body"] = "系统退回"
	})
	moved, err := e.store.TransitionCandidate(context.Background(), 1, system, types.Agent24CandidateTransition{
		To: types.Agent24CandidateReturned, ReturnReason: types.Agent24ReturnConflicted,
		Note: "system", Approver: "system",
	}, e.clock)
	require.NoError(t, err)
	require.True(t, moved)
	assert.Equal(t, "stale/candidate_not_submitted", agent24CandidateCall(t, e.h.ReviewReturn, alice,
		agent24ReviewJSON(t, map[string]any{
			"request_id": "r-sys", "candidate_id": system,
		})).outcome())

	approved := e.submit(t, bob, func(m map[string]any) {
		m["idempotency_key"] = agent24KeyN(8)
		m["body"] = "已批准"
	})
	moved, err = e.store.TransitionCandidate(context.Background(), 1, approved, types.Agent24CandidateTransition{
		To: types.Agent24CandidateApproved, KnowledgeID: "doc-x", Approver: "x",
	}, e.clock)
	require.NoError(t, err)
	require.True(t, moved)
	assert.Equal(t, "stale/candidate_not_submitted", agent24CandidateCall(t, e.h.ReviewReturn, alice,
		agent24ReviewJSON(t, map[string]any{
			"request_id": "r-ap", "candidate_id": approved,
		})).outcome())
}

func TestAgent24CandidateReviewReturnConcurrent(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	e.grantAdmin(t, agent24Alice, "kbA", false)
	carol := types.Principal{Type: "api_external_user", ID: "1:carol"}
	e.grantAdmin(t, carol, "kbA", false)
	id := e.submit(t, agent24Proposer(agent24Bob), nil)
	sqlDB, err := e.db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	var mu sync.Mutex
	e.h.now = func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		e.clock = e.clock.Add(time.Second)
		return e.clock
	}
	callers := []agent24CandidateAs{agent24Reviewer(agent24Alice), agent24Reviewer(carol)}
	bodies := []string{
		agent24ReviewJSON(t, map[string]any{"request_id": "r-a", "candidate_id": id, "note": "同时退回"}),
		agent24ReviewJSON(t, map[string]any{"request_id": "r-c", "candidate_id": id, "note": "同时退回"}),
	}
	hits := make([]agent24ReviewHit, len(callers))
	var wg sync.WaitGroup
	for i, as := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			hits[i] = agent24ReviewSend(e.h.ReviewReturn, as, bodies[i])
		}()
	}
	wg.Wait()
	ok, stale := 0, 0
	for _, hit := range hits {
		switch agent24ReviewOutcome(hit.code, hit.body) {
		case "ok":
			ok++
			assert.False(t, agent24ReviewAlready(hit.body), hit.body)
		case "stale/candidate_not_submitted":
			stale++
		default:
			t.Errorf("outcome %s body %v", agent24ReviewOutcome(hit.code, hit.body), hit.body)
		}
	}
	assert.Equal(t, 1, ok)
	assert.Equal(t, 1, stale)
	cand, err := e.store.GetCandidate(context.Background(), 1, id)
	require.NoError(t, err)
	assert.Equal(t, types.Agent24CandidateReturned, cand.Status)
	assert.Contains(t, []string{agent24Alice.StorageID(), carol.StorageID()}, cand.Approver)
}

type agent24ReviewHit struct {
	code int
	body map[string]any
}

func agent24ReviewSend(serve func(*gin.Context), as agent24CandidateAs, body string) agent24ReviewHit {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequestWithContext(as.ctx(), http.MethodPost, "/", strings.NewReader(body))
	serve(c)
	hit := agent24ReviewHit{code: w.Code}
	_ = json.Unmarshal(w.Body.Bytes(), &hit.body)
	return hit
}

func agent24ReviewOutcome(code int, body map[string]any) string {
	if code == http.StatusOK {
		return "ok"
	}
	e, _ := body["error"].(map[string]any)
	if e == nil {
		return "unparsed"
	}
	d, _ := e["details"].(map[string]any)
	name, _ := e["code"].(string)
	if reason, ok := d["reason"].(string); ok {
		return name + "/" + reason
	}
	return name
}

func agent24ReviewAlready(body map[string]any) bool {
	d, _ := body["details"].(map[string]any)
	already, _ := d["already"].(bool)
	return already
}

func TestAgent24CandidateReviewReturnAudit(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	e.grantAdmin(t, agent24Alice, "kbA", false)
	alice := agent24Reviewer(agent24Alice)
	id := e.submit(t, agent24Proposer(agent24Bob), nil)
	listed := agent24CandidateCall(t, e.h.ReviewList, alice, `{"request_id":"r-list"}`)
	require.Equal(t, http.StatusOK, listed.code, listed.raw)
	listAudit, err := json.Marshal(listed.audit())
	require.NoError(t, err)
	assert.Empty(t, listed.audit().CandidateID)
	assert.NotContains(t, string(listAudit), "请假流程")
	assert.NotContains(t, string(listAudit), "直属主管")

	note := "审计不记这条说明"
	returned := agent24CandidateCall(t, e.h.ReviewReturn, alice, agent24ReviewJSON(t, map[string]any{
		"request_id": "r-ret", "candidate_id": id, "note": note,
	}))
	require.Equal(t, http.StatusOK, returned.code, returned.raw)
	a := returned.audit()
	require.NotNil(t, a)
	assert.Equal(t, types.Agent24ReturnReviewer, a.ReturnReason)
	raw, err := json.Marshal(a)
	require.NoError(t, err)
	text := string(raw)
	assert.Contains(t, text, `"return_reason"`)
	assert.NotContains(t, text, note)
	assert.NotContains(t, text, "请假流程")
	assert.NotContains(t, text, "直属主管")
	assert.NotContains(t, text, "title")
	assert.NotContains(t, text, "body")
}

func TestAgent24CandidateReturnSchemaCases(t *testing.T) {
	var f struct {
		Cases []struct {
			Name   string          `json:"name"`
			Schema string          `json:"schema"`
			Valid  bool            `json:"valid"`
			Doc    json.RawMessage `json:"doc"`
		} `json:"cases"`
	}
	require.NoError(t, json.Unmarshal(agent24OpsV2Fixture(t, "schema-cases.json"), &f))
	sch := agent24CandidateSchema(t, "return_request")
	e := newAgent24CandidateEnv(t)
	e.grantAdmin(t, agent24Alice, types.Agent24KnowledgeAdminAnyKB, false)
	alice := agent24Reviewer(agent24Alice)
	n := 0
	for _, c := range f.Cases {
		if c.Schema != "candidate.return_request" {
			continue
		}
		n++
		require.Equal(t, c.Valid, agent24Validate(sch, c.Doc) == nil, "fixture verdict %s", c.Name)
		r := agent24CandidateCall(t, e.h.ReviewReturn, alice, string(c.Doc))
		if c.Valid {
			assert.Equal(t, "not_found", r.outcome(), c.Name)
			continue
		}
		assert.Equal(t, "invalid_request", r.outcome(), "%s: %s", c.Name, r.raw)
	}
	assert.Equal(t, 3, n)
}

// W1.2: authz_version digests the organisation-administrator grants the decision read, so granting or revoking
// one changes it even when the credential and principal stay the same.
func TestAgent24CandidateReviewAuthzVersionFollowsGrants(t *testing.T) {
	e := newAgent24CandidateEnv(t)
	alice := agent24Reviewer(agent24Alice)
	version := func() string {
		t.Helper()
		r := agent24CandidateCall(t, e.h.ReviewList, alice, `{"request_id":"r-list"}`)
		require.Equal(t, http.StatusOK, r.code, r.raw)
		v, ok := r.body["authz_version"].(string)
		require.True(t, ok)
		return v
	}
	e.grantAdmin(t, agent24Alice, "kbA", false)
	onlyA := version()
	assert.Equal(t, onlyA, version(), "stable while nothing changes")
	e.grantAdmin(t, agent24Alice, "kbB", false)
	both := version()
	assert.NotEqual(t, onlyA, both)
	require.NoError(t, e.db.Model(&types.Agent24KnowledgeAdmin{}).
		Where("tenant_id = ? AND knowledge_base_id = ?", 1, "kbA").Update("revoked_at", e.clock).Error)
	onlyB := version()
	assert.NotEqual(t, both, onlyB)
	assert.NotEqual(t, onlyA, onlyB)
}
