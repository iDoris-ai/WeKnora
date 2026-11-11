// Agent24 fork product patch A24-KB-49 (KB-SOP-02 S5a): the UI write guard. The guard is mounted in front of a
// recorder, not the candidate handlers: S5a only proves what the guard refuses and what it hands on. S5b mounts the
// real handlers.

package handler

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/agent24uisession"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

const agent24UIWriteCand = "cand-1"
const agent24UIWriteDoc = "doc-1"

// agent24UIWriteSeen is what the recorder behind the guard saw.
type agent24UIWriteSeen struct {
	calls      int
	body       []byte
	principal  types.Principal
	hasPrinc   bool
	tenant     uint64
	scope      types.TenantAPIKeyScope
	hasScope   bool
	credential string
}

type agent24UIWriteRig struct {
	t     *testing.T
	r     *gin.Engine
	stub  *agent24AuditStub
	seen  *agent24UIWriteSeen
	token string
	proof string
}

func agent24UIWriteMount(t *testing.T, h *Agent24KnowledgeUIReadHandler) agent24UIWriteRig {
	t.Helper()
	gin.SetMode(gin.TestMode)
	stub := &agent24AuditStub{}
	seen := &agent24UIWriteSeen{}
	r := gin.New()
	r.Use(Agent24KnowledgeUINoStore())
	r.Use(Agent24KnowledgeAudit(stub))
	// S5a: no Auth and no credential gate in front. Letting the write routes past them (as IsUIReadRoute does
	// for the read entries) is S5b, together with registering the routes.
	record := func(c *gin.Context) {
		seen.calls++
		body, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		seen.body = body
		ctx := c.Request.Context()
		seen.principal, seen.hasPrinc = types.PrincipalFromContext(ctx)
		seen.tenant = types.CallerFromContext(ctx).TenantID
		seen.scope, seen.hasScope = types.TenantAPIKeyScopeFromContext(ctx)
		seen.credential, _ = types.CredentialIDFromContext(ctx)
		c.Status(http.StatusNoContent)
	}
	for _, rt := range agent24uisession.UIWriteRoutes {
		r.Handle(rt.Method, rt.Path, h.WriteGuard(rt), record)
	}
	return agent24UIWriteRig{t: t, r: r, stub: stub, seen: seen}
}

func (g agent24UIWriteRig) do(method, target, body string) *httptest.ResponseRecorder {
	g.t.Helper()
	w := httptest.NewRecorder()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, target, rd)
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set(agent24UIViewProofHeader, g.proof)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	g.r.ServeHTTP(w, req)
	return w
}

func (g agent24UIWriteRig) reset() { *g.seen = agent24UIWriteSeen{} }

// agent24UIWriteFixture: tenant 9, the ticket's credential is key 7 with keys' KB list, the session JWT carries ops.
func agent24UIWriteFixture(
	t *testing.T, keys []*types.TenantAPIKey, ops []string, at time.Time,
) (agent24UIWriteRig, *types.Agent24UITicket, interfaces.Agent24UISessionStore) {
	t.Helper()
	_, store := agent24UIHandlerDB(t)
	proof, vph := agent24UIViewSecret()
	ticket := agent24UISeedTicket(t, store, agent24UIRawTicket(97), vph, nil)
	if keys == nil {
		keys = []*types.TenantAPIKey{agent24UILiveKey([]string{"kb-b", "kb-a"}, false, []string{
			string(types.APIKeyCapabilityRetrieve), string(types.APIKeyCapabilityKBCandidateReview),
		}, nil)}
	}
	h := agent24UIReadHandler(nil, store, keys, agent24UIResolver(agent24UIKBs(9, "kb-a", "kb-b")...))
	rig := agent24UIWriteMount(t, h)
	rig.token = agent24UIReadIssue(t, ticket, ops, at, time.Minute)
	rig.proof = proof
	return rig, ticket, store
}

// agent24UIWriteCalls is one valid call per write route.
func agent24UIWriteCalls() []struct{ method, target, body string } {
	p := agent24uisession.UIReadPrefix
	return []struct{ method, target, body string }{
		{http.MethodGet, p + "/candidates?request_id=r1", ""},
		{http.MethodGet, p + "/candidates/" + agent24UIWriteCand + "?request_id=r1", ""},
		{http.MethodPost, p + "/candidates/" + agent24UIWriteCand + "/return",
			`{"request_id":"r1","candidate_id":"` + agent24UIWriteCand + `"}`},
		{http.MethodPost, p + "/candidates/" + agent24UIWriteCand + "/approve",
			`{"request_id":"r1","candidate_id":"` + agent24UIWriteCand + `","content_sha256":"` +
				strings.Repeat("a", 64) + `"}`},
		{http.MethodPost, p + "/documents/" + agent24UIWriteDoc + "/withdraw",
			`{"request_id":"r1","knowledge_id":"` + agent24UIWriteDoc + `"}`},
	}
}

func agent24UIWriteRequireRefusal(t *testing.T, w *httptest.ResponseRecorder, status int, code, reason string) {
	t.Helper()
	require.Equal(t, status, w.Code, w.Body.String())
	m := agent24UIJSONMap(t, w.Body.Bytes())
	e := m["error"].(map[string]any)
	require.Equal(t, code, e["code"], w.Body.String())
	if reason != "" {
		require.Equal(t, reason, e["details"].(map[string]any)["reason"], w.Body.String())
	}
	agent24UIRequireNullEcho(t, w.Body.Bytes())
	agent24UIRequireNoStore(t, w)
}

func TestAgent24UIWriteRoutesTable(t *testing.T) {
	require.Len(t, agent24uisession.UIWriteRoutes, 5)
	require.Equal(t, []string{"candidate.review", "candidate.approve", "entry.withdraw"}, agent24uisession.UIWriteOps())
	for _, rt := range agent24uisession.UIWriteRoutes {
		require.False(t, agent24uisession.IsUIReadRoute(rt.Method, rt.Path), rt.Path)
		require.Contains(t, agent24uisession.UIOps, rt.Op)
		require.NotContains(t, agent24uisession.UIRouteOps(), rt.Op, "S5a must not release a write op at exchange")
	}
	want := map[string]string{
		"/candidates": "", "/candidates/:candidate_id": "candidate_id",
		"/candidates/:candidate_id/return": "candidate_id", "/candidates/:candidate_id/approve": "candidate_id",
		"/documents/:knowledge_id/withdraw": "knowledge_id",
	}
	for _, rt := range agent24uisession.UIWriteRoutes {
		require.Equal(t, want[strings.TrimPrefix(rt.Path, agent24uisession.UIReadPrefix)], agent24UIWriteIDParam(rt), rt.Path)
	}
}

// P3 (s5-preconditions): a session whose JWT Ops lacks the route's operation is refused with 403
// operation_not_allowed at every write entry, and nothing behind the guard runs.
func TestAgent24UIWriteGuardOpNotInSession(t *testing.T) {
	rig, _, _ := agent24UIWriteFixture(t, nil, agent24uisession.UIRouteOps(), time.Now())
	for _, call := range agent24UIWriteCalls() {
		rig.reset()
		w := rig.do(call.method, call.target, call.body)
		agent24UIWriteRequireRefusal(t, w, http.StatusForbidden, "forbidden", "operation_not_allowed")
		require.Equal(t, 0, rig.seen.calls, call.target)
	}

	// Only candidate.review: approve and withdraw are refused, the review entries pass.
	rig, _, _ = agent24UIWriteFixture(t, nil, append(agent24uisession.UIRouteOps(), "candidate.review"), time.Now())
	for i, call := range agent24UIWriteCalls() {
		rig.reset()
		w := rig.do(call.method, call.target, call.body)
		if i < 3 {
			require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
			require.Equal(t, 1, rig.seen.calls)
			continue
		}
		agent24UIWriteRequireRefusal(t, w, http.StatusForbidden, "forbidden", "operation_not_allowed")
		require.Equal(t, 0, rig.seen.calls, call.target)
	}
}

// What the handler behind the guard reads: the session's principal and tenant, and the ticket credential's scope
// with full access off and the knowledge-base list replaced by E.
func TestAgent24UIWriteGuardContext(t *testing.T) {
	rig, ticket, _ := agent24UIWriteFixture(t, nil, agent24uisession.UIOps, time.Now())
	for _, call := range agent24UIWriteCalls() {
		rig.reset()
		w := rig.do(call.method, call.target, call.body)
		require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
		require.Equal(t, 1, rig.seen.calls)
		require.True(t, rig.seen.hasPrinc)
		require.Equal(t, ticket.Principal, rig.seen.principal.StorageID())
		require.Equal(t, uint64(9), rig.seen.tenant)
		require.True(t, rig.seen.hasScope)
		require.False(t, rig.seen.scope.FullAccess)
		require.ElementsMatch(t, []string{"kb-a", "kb-b"}, []string(rig.seen.scope.KnowledgeBaseIDs))
		require.True(t, rig.seen.scope.HasCapability(types.APIKeyCapabilityKBCandidateReview))
		require.Equal(t, "key:7", rig.seen.credential)
	}
}

// Q2 = B: an E that became empty after the exchange is handed on as an empty, non-full-access scope; the guard
// does not answer empty_scope.
func TestAgent24UIWriteGuardEmptyE(t *testing.T) {
	caps := []string{string(types.APIKeyCapabilityKBCandidateReview)}
	for name, key := range map[string]*types.TenantAPIKey{
		"allowlist disjoint": agent24UILiveKey([]string{"kb-nope"}, false, caps, nil),
		"allowlist empty":    agent24UILiveKey([]string{}, false, caps, nil),
		"full access":        agent24UILiveKey(nil, true, caps, nil),
	} {
		t.Run(name, func(t *testing.T) {
			rig, _, _ := agent24UIWriteFixture(t, []*types.TenantAPIKey{key}, agent24uisession.UIOps, time.Now())
			for _, call := range agent24UIWriteCalls() {
				rig.reset()
				w := rig.do(call.method, call.target, call.body)
				require.Equal(t, http.StatusNoContent, w.Code, w.Body.String())
				require.Equal(t, 1, rig.seen.calls)
				require.True(t, rig.seen.hasScope)
				require.False(t, rig.seen.scope.FullAccess)
				require.Empty(t, rig.seen.scope.KnowledgeBaseIDs)
			}
		})
	}
}

// Revocation and expiry: the same 401s as a read entry, and nothing behind the guard runs.
func TestAgent24UIWriteGuardRevoked(t *testing.T) {
	past := time.Now().Add(-time.Hour)
	cases := []struct {
		name   string
		keys   []*types.TenantAPIKey
		at     time.Time
		revoke bool
		reason string
	}{
		{"view family revoked", nil, time.Now(), true, "credential_invalid"},
		{"credential revoked", []*types.TenantAPIKey{}, time.Now(), false, "credential_revoked"},
		{"credential expired", []*types.TenantAPIKey{agent24UILiveKey([]string{"kb-a"}, false, nil, &past)},
			time.Now(), false, "credential_expired"},
		{"session expired", nil, time.Now().Add(-2 * time.Minute), false, "credential_expired"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig, ticket, store := agent24UIWriteFixture(t, tc.keys, agent24uisession.UIOps, tc.at)
			if tc.revoke {
				_, vph := agent24UIViewSecret() // the fixture's view secret is fixed
				require.NoError(t, store.RevokeFamily(context.Background(), 9, ticket.Principal, vph, time.Now()))
			}
			for _, call := range agent24UIWriteCalls() {
				rig.reset()
				w := rig.do(call.method, call.target, call.body)
				agent24UIWriteRequireRefusal(t, w, http.StatusUnauthorized, "unauthorized", tc.reason)
				require.Equal(t, 0, rig.seen.calls, call.target)
			}
		})
	}
}

// Q1 = A, GET: the guard builds the JSON object body from the query and the path.
func TestAgent24UIWriteGuardGetBody(t *testing.T) {
	rig, _, _ := agent24UIWriteFixture(t, nil, agent24uisession.UIOps, time.Now())
	p := agent24uisession.UIReadPrefix
	ok := []struct{ target, want string }{
		{p + "/candidates?request_id=r1", `{"request_id":"r1"}`},
		{p + "/candidates?request_id=r1&after=c%22x&limit=5", `{"request_id":"r1","after":"c\"x","limit":5}`},
		{p + "/candidates?request_id=r1&limit=1.0", `{"request_id":"r1","limit":1.0}`}, // refused by the handler
		{p + "/candidates?request_id=r1&limit=1e0", `{"request_id":"r1","limit":1e0}`},
		{p + "/candidates", `{}`}, // missing request_id is the handler's invalid_request
		{p + "/candidates/" + agent24UIWriteCand + "?request_id=r1",
			`{"request_id":"r1","candidate_id":"` + agent24UIWriteCand + `"}`},
	}
	for _, tc := range ok {
		rig.reset()
		w := rig.do(http.MethodGet, tc.target, "")
		require.Equal(t, http.StatusNoContent, w.Code, tc.target+" "+w.Body.String())
		require.JSONEq(t, tc.want, string(rig.seen.body), tc.target)
		if strings.Contains(tc.target, "limit=1") && !strings.Contains(tc.target, "limit=5") {
			// written verbatim, so the handler sees the client's number token
			require.Contains(t, string(rig.seen.body), strings.SplitN(tc.target, "limit=", 2)[1])
		}
	}
	bad := []struct{ target, body string }{
		{p + "/candidates?request_id=r1&limit=five", ""},
		{p + "/candidates?request_id=r1&limit=", ""},
		{p + "/candidates?request_id=r1&limit=5,6", ""},
		{p + "/candidates?request_id=r1&sort=asc", ""},
		{p + "/candidates?request_id=r1&request_id=r2", ""},
		{p + "/candidates?request_id=r1&after=a&after=b", ""},
		{p + "/candidates/" + agent24UIWriteCand + "?request_id=r1&after=x", ""}, // after is the list's only
		{p + "/candidates/" + agent24UIWriteCand + "?request_id=r1&candidate_id=" + agent24UIWriteCand, ""},
		{p + "/candidates?request_id=r1", `{"request_id":"r1"}`},
	}
	for _, tc := range bad {
		rig.reset()
		w := rig.do(http.MethodGet, tc.target, tc.body)
		agent24UIWriteRequireRefusal(t, w, http.StatusBadRequest, "invalid_request", "")
		require.Equal(t, 0, rig.seen.calls, tc.target)
	}
}

// Q1 = A, POST: the body reaches the handler byte for byte; its id must match the path.
func TestAgent24UIWriteGuardPostBody(t *testing.T) {
	rig, _, _ := agent24UIWriteFixture(t, nil, agent24uisession.UIOps, time.Now())
	p := agent24uisession.UIReadPrefix
	ret := p + "/candidates/" + agent24UIWriteCand + "/return"
	wd := p + "/documents/" + agent24UIWriteDoc + "/withdraw"
	passed := []struct{ target, body string }{
		{ret, `{"request_id":"r1","candidate_id":"` + agent24UIWriteCand + `"}`},
		// spacing, key order, stripped fields and a duplicate key all reach the handler untouched
		{ret, "{ \"candidate_id\" : \"" + agent24UIWriteCand + "\",\n\"approved\":true,\"request_id\":\"r1\"}"},
		{ret, `{"request_id":"r1","note":"a","note":"b","candidate_id":"` + agent24UIWriteCand + `"}`},
		{wd, `{"request_id":"r1","knowledge_id":"` + agent24UIWriteDoc + `"}`},
		// not a JSON object: passed through for the handler to refuse
		{ret, `not json`},
		{ret, `["candidate_id"]`},
	}
	for _, tc := range passed {
		rig.reset()
		w := rig.do(http.MethodPost, tc.target, tc.body)
		require.Equal(t, http.StatusNoContent, w.Code, tc.body+" "+w.Body.String())
		require.True(t, bytes.Equal([]byte(tc.body), rig.seen.body), "body changed: %q", rig.seen.body)
	}
	refused := []struct{ target, body string }{
		{ret, `{"request_id":"r1"}`},
		{ret, `{"request_id":"r1","candidate_id":"other"}`},
		{ret, `{"request_id":"r1","candidate_id":1}`},
		{ret, `{"request_id":"r1","candidate_id":null}`},
		{ret, `{"request_id":"r1","knowledge_id":"` + agent24UIWriteCand + `"}`},
		{wd, `{"request_id":"r1","knowledge_id":"other"}`},
		{wd, `{"request_id":"r1","candidate_id":"` + agent24UIWriteDoc + `"}`},
	}
	for _, tc := range refused {
		rig.reset()
		w := rig.do(http.MethodPost, tc.target, tc.body)
		agent24UIWriteRequireRefusal(t, w, http.StatusBadRequest, "invalid_request", "")
		require.Equal(t, 0, rig.seen.calls, tc.body)
	}
}

// W1.9: ② (transport shape) before ③(a) (the operation is not in the session).
func TestAgent24UIWriteGuardShapeBeforeOp(t *testing.T) {
	rig, _, _ := agent24UIWriteFixture(t, nil, agent24uisession.UIRouteOps(), time.Now())
	p := agent24uisession.UIReadPrefix
	w := rig.do(http.MethodPost, p+"/documents/"+agent24UIWriteDoc+"/withdraw", `{"request_id":"r1"}`)
	agent24UIWriteRequireRefusal(t, w, http.StatusBadRequest, "invalid_request", "")
	w = rig.do(http.MethodGet, p+"/candidates?request_id=r1&sort=asc", "")
	agent24UIWriteRequireRefusal(t, w, http.StatusBadRequest, "invalid_request", "")
	require.Equal(t, 0, rig.seen.calls)
}
