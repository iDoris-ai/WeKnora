// Agent24 fork product patch A24-KB-50 (KB-SOP-02 S5b): the UI write entries through the real registration
// (RegisterAgent24UIWriteRoutes), WriteGuard in front of the S3/S4 handlers on their sqlite fixture.

package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/agent24uisession"
	"github.com/Tencent/WeKnora/internal/types"
)

// agent24UISessionAdmin is the ticket's principal (tenant mode: the key's principal is api_tenant). It is the
// approver and withdrawer; agent24Bob is the author.
var agent24UISessionAdmin = types.Principal{Type: types.PrincipalAPITenant, ID: "1"}

var agent24UIAllWriteCaps = []string{
	"retrieve", "kb_candidate_propose", "kb_candidate_review", "kb_candidate_approve", "kb_entry_withdraw",
}

type agent24UIWriteE2E struct {
	t     *testing.T
	e     *agent24CandidateEnv
	r     *gin.Engine
	stub  *agent24AuditStub
	token string
	proof string
}

// newAgent24UIWriteE2E: tenant 1, kbA and kbB; the ticket's credential is key 7 with caps; the session carries ops.
func newAgent24UIWriteE2E(t *testing.T, caps, ops []string) *agent24UIWriteE2E {
	t.Helper()
	e := newAgent24CandidateEnv(t)
	e.installApprove(t)
	_, store := agent24UIHandlerDB(t)
	proof, vph := agent24UIViewSecret()
	ticket := agent24UISeedTicket(t, store, agent24UIRawTicket(98), vph, func(tk *types.Agent24UITicket) {
		tk.TenantID = 1
		tk.Principal = agent24UISessionAdmin.StorageID()
		tk.PrincipalSecretTag = agent24UIPrincipalSecretTag(agent24UIExchangeKey(), &types.Tenant{ID: 1})
		require.NoError(t, tk.SetScopeKnowledgeBaseIDs([]string{"kbA", "kbB"}))
	})
	tenant := uint64(1)
	key := &types.TenantAPIKey{
		ID: 7, TenantID: &tenant, ScopeType: types.APIKeyScopeTenant, Name: "k",
		KnowledgeBaseIDs: []string{"kbA", "kbB"}, Capabilities: caps,
	}
	resolver := agent24UIResolver(agent24UIKBs(1, "kbA", "kbB")...)
	h := agent24UIReadHandler(nil, store, []*types.TenantAPIKey{key}, resolver)
	h.deps.stored.tenants = agent24UITenantStub{tenant: &types.Tenant{ID: 1}}
	gin.SetMode(gin.TestMode)
	stub := &agent24AuditStub{}
	r := gin.New()
	r.Use(Agent24KnowledgeUINoStore())
	r.Use(Agent24KnowledgeAudit(stub))
	RegisterAgent24UIWriteRoutes(r.Group("/api/v1"), h, e.h, false, nil)
	return &agent24UIWriteE2E{
		t: t, e: e, r: r, stub: stub, proof: proof,
		token: agent24UIReadIssue(t, ticket, ops, time.Now(), time.Minute),
	}
}

func agent24UIAllOps() []string { return append([]string(nil), agent24uisession.UIOps...) }

func (g *agent24UIWriteE2E) do(method, target, body string) *httptest.ResponseRecorder {
	g.t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
		req.Header.Set(agent24UIViewProofHeader, g.proof)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	g.r.ServeHTTP(w, req)
	return w
}

// submit is Bob's candidate on kbA with the given title, through the C handler.
func (g *agent24UIWriteE2E) submit(title string, n int) (id, sha string) {
	g.t.Helper()
	r := agent24CandidateCall(g.t, g.e.h.Submit, agent24Proposer(agent24Bob), agent24Submit(func(m map[string]any) {
		m["title"] = title
		m["target_knowledge_base_id"] = "kbA"
		m["idempotency_key"] = agent24KeyN(n)
	}))
	require.Equal(g.t, http.StatusOK, r.code, r.raw)
	id, _ = r.body["candidate_id"].(string)
	require.NotEmpty(g.t, id, r.raw)
	return id, g.e.candidate(g.t, id).ContentSHA256
}

func agent24UIJSON(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &m), w.Body.String())
	return m
}

// Registration is exactly the five routes, each method on its own FullPath, and nothing on the UI read paths.
func TestAgent24UIWriteRoutesRegistration(t *testing.T) {
	g := newAgent24UIWriteE2E(t, agent24UIAllWriteCaps, agent24UIAllOps())
	var got, want []string
	for _, rt := range g.r.Routes() {
		got = append(got, rt.Method+" "+rt.Path)
	}
	for _, rt := range agent24uisession.UIWriteRoutes {
		want = append(want, rt.Method+" "+rt.Path)
	}
	assert.ElementsMatch(t, want, got)

	pre := gin.New()
	unreachable := func(c *gin.Context) { c.AbortWithStatus(http.StatusTeapot) }
	RegisterAgent24UIWriteRoutes(pre.Group("/api/v1"), g.e.h2read(t), g.e.h, true, unreachable)
	got = nil
	for _, rt := range pre.Routes() {
		got = append(got, rt.Method+" "+rt.Path)
	}
	for _, rt := range agent24uisession.UIWriteRoutes {
		want = append(want, http.MethodOptions+" "+rt.Path)
	}
	assert.ElementsMatch(t, agent24Dedup(want), got, "with preflight: one OPTIONS per write path")
	w := httptest.NewRecorder()
	pre.ServeHTTP(w, httptest.NewRequest(http.MethodOptions, agent24uisession.UIReadPrefix+"/candidates", nil))
	assert.Equal(t, http.StatusTeapot, w.Code, "the OPTIONS route is the unreachable handler")
}

func agent24Dedup(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// h2read is a second UI read handler for a second engine (the handler holds no per-engine state).
func (e *agent24CandidateEnv) h2read(t *testing.T) *Agent24KnowledgeUIReadHandler {
	t.Helper()
	_, store := agent24UIHandlerDB(t)
	return agent24UIReadHandler(nil, store, nil, nil)
}

// One positive per entry, against the real handlers: the contract shapes come back, the store changes, and every
// row carries action, op, source=ui_session and the template request_path (decision 2026-11-12-s5b-audit-ready).
func TestAgent24UIWriteRoutesPositive(t *testing.T) {
	g := newAgent24UIWriteE2E(t, agent24UIAllWriteCaps, agent24UIAllOps())
	g.e.grantAdmin(t, agent24UISessionAdmin, "kbA", false)
	toApprove, sha := g.submit("待批准", 1)
	toReturn, _ := g.submit("待退回", 2)
	p := agent24uisession.UIReadPrefix

	w := g.do(http.MethodGet, p+"/candidates?request_id=r-list", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), toApprove)
	assert.Contains(t, w.Body.String(), toReturn)

	w = g.do(http.MethodGet, p+"/candidates/"+toApprove+"?request_id=r-get", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	cand, _ := agent24UIJSON(t, w)["candidate"].(map[string]any)
	assert.Equal(t, toApprove, cand["candidate_id"], w.Body.String())

	w = g.do(http.MethodPost, p+"/candidates/"+toReturn+"/return",
		`{"request_id":"r-ret","candidate_id":"`+toReturn+`","note":"请补充出处"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	assert.Equal(t, "returned", string(g.e.candidate(t, toReturn).Status))

	w = g.do(http.MethodPost, p+"/candidates/"+toApprove+"/approve",
		`{"request_id":"r-app","candidate_id":"`+toApprove+`","content_sha256":"`+sha+`"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	approved := g.e.candidate(t, toApprove)
	assert.Equal(t, "approved", string(approved.Status))
	assert.Equal(t, agent24UISessionAdmin.StorageID(), approved.Approver)
	assert.False(t, approved.AuthorIsApprover)

	const doc = "docUI"
	g.e.entryDoc(t, doc, "kbA", types.ParseStatusCompleted, true)
	w = g.do(http.MethodPost, p+"/documents/"+doc+"/withdraw", `{"request_id":"r-wd","knowledge_id":"`+doc+`"}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Len(t, g.e.withdrawalRows(t), 1)
	assert.Equal(t, agent24UISessionAdmin.StorageID(), g.e.withdrawalRows(t)[0].WithdrawnBy)

	type row struct{ action, op, source, path string }
	var rows []row
	for _, entry := range g.stub.entries {
		var d map[string]any
		require.NoError(t, json.Unmarshal(entry.Details, &d))
		op, _ := d["op"].(string)
		src, _ := d["source"].(string)
		rows = append(rows, row{string(entry.Action), op, src, entry.RequestPath})
	}
	assert.Equal(t, []row{
		{"knowledge.ui.view", "candidate.review", "ui_session", p + "/candidates"},
		{"knowledge.ui.view", "candidate.review", "ui_session", p + "/candidates/:candidate_id"},
		{"knowledge.candidate.return", "candidate.review", "ui_session", p + "/candidates/:candidate_id/return"},
		{"knowledge.candidate.approve", "candidate.approve", "ui_session", p + "/candidates/:candidate_id/approve"},
		{"knowledge.entry.withdraw", "entry.withdraw", "ui_session", p + "/documents/:knowledge_id/withdraw"},
	}, rows)
}

// Refusals are audited the same way: a session without the op (P3), and no session at all (a C key alone, U1.3:
// C cannot use a UI write entry). The handler is not reached either time.
func TestAgent24UIWriteRoutesRefusalsAudited(t *testing.T) {
	g := newAgent24UIWriteE2E(t, agent24UIAllWriteCaps, agent24uisession.UIRouteOps())
	g.e.grantAdmin(t, agent24UISessionAdmin, "kbA", false)
	id, sha := g.submit("会话没有写 op", 1)
	p := agent24uisession.UIReadPrefix
	w := g.do(http.MethodPost, p+"/candidates/"+id+"/approve",
		`{"request_id":"r1","candidate_id":"`+id+`","content_sha256":"`+sha+`"}`)
	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	assert.Equal(t, "submitted", string(g.e.candidate(t, id).Status))

	g.token = ""
	w = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, p+"/candidates/"+id+"/approve",
		strings.NewReader(`{"request_id":"r2","candidate_id":"`+id+`","content_sha256":"`+sha+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", "sk-c-key")
	g.r.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
	assert.Equal(t, "submitted", string(g.e.candidate(t, id).Status))
	assert.Zero(t, g.e.approveRecords(t))

	entries := g.stub.entries
	require.Len(t, entries, 2)
	for _, entry := range entries {
		var d map[string]any
		require.NoError(t, json.Unmarshal(entry.Details, &d))
		assert.Equal(t, "knowledge.candidate.approve", string(entry.Action))
		assert.Equal(t, "candidate.approve", d["op"])
		assert.Equal(t, "ui_session", d["source"])
		assert.Equal(t, p+"/candidates/:candidate_id/approve", entry.RequestPath)
		assert.NotEqual(t, "ok", d["outcome"])
	}
}

// P1 end to end: a session whose C holds only review can list and return but not approve or withdraw.
func TestAgent24UIWriteRoutesOpsFollowCredential(t *testing.T) {
	caps := []string{"retrieve", "kb_candidate_review"}
	ops := agent24UISessionOps(types.TenantAPIKeyScope{KnowledgeBaseIDs: types.StringArray{"kbA"}, Capabilities: caps})
	g := newAgent24UIWriteE2E(t, caps, ops)
	g.e.grantAdmin(t, agent24UISessionAdmin, "kbA", false)
	id, sha := g.submit("只能审阅", 1)
	p := agent24uisession.UIReadPrefix
	require.Equal(t, http.StatusOK, g.do(http.MethodGet, p+"/candidates?request_id=r1", "").Code)
	w := g.do(http.MethodPost, p+"/candidates/"+id+"/approve",
		`{"request_id":"r2","candidate_id":"`+id+`","content_sha256":"`+sha+`"}`)
	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "operation_not_allowed")
	w = g.do(http.MethodPost, p+"/documents/doc-x/withdraw", `{"request_id":"r3","knowledge_id":"doc-x"}`)
	require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
	assert.Contains(t, w.Body.String(), "operation_not_allowed")
}
