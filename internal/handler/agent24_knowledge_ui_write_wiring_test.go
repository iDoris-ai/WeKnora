// Agent24 fork product patch A24-KB-50 (KB-SOP-02 S5b, WeKnora#146, s5-preconditions P1, PR-Daemon #168 N2).

package handler

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/agent24uisession"
	"github.com/Tencent/WeKnora/internal/types"
)

var agent24UIReadOpsWant = []string{
	"kb.list", "kb.get", "doc.list", "doc.get", "chunk.list", "wiki.list", "wiki.get", "tag.list",
}

// P1: the session carries the eight read ops plus exactly the write ops whose capability C_now holds, in UIOps
// order, judged as the write handlers judge it (HasCapability; a platform key holds none). D1 (the administrator
// grant) is not consulted here; full access does not stand in for a capability.
func TestAgent24UISessionOpsByCapability(t *testing.T) {
	require.Equal(t, agent24UIReadOpsWant, agent24uisession.UIRouteOps())
	with := func(extra ...string) []string { return append(slices.Clone(agent24UIReadOpsWant), extra...) }
	for _, tc := range []struct {
		name     string
		caps     []string
		full     bool
		platform bool
		want     []string
	}{
		{"retrieve only", []string{"retrieve"}, false, false, with()},
		{"propose only", []string{"retrieve", "kb_candidate_propose"}, false, false, with()},
		{"review", []string{"retrieve", "kb_candidate_review"}, false, false, with("candidate.review")},
		{"approve", []string{"retrieve", "kb_candidate_approve"}, false, false, with("candidate.approve")},
		{"withdraw", []string{"retrieve", "kb_entry_withdraw"}, false, false, with("entry.withdraw")},
		{
			"approve and review, listed backwards",
			[]string{"kb_candidate_approve", "retrieve", "kb_candidate_review"},
			false, false, with("candidate.review", "candidate.approve"),
		},
		{"all three", []string{
			"retrieve", "kb_candidate_propose", "kb_candidate_review", "kb_candidate_approve", "kb_entry_withdraw",
		}, false, false, with("candidate.review", "candidate.approve", "entry.withdraw")},
		{"full access, no write capability", []string{"retrieve"}, true, false, with()},
		{
			"platform key with write capabilities",
			[]string{"retrieve", "kb_candidate_review", "kb_entry_withdraw"},
			false, true, with(),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scope := types.TenantAPIKeyScope{
				FullAccess: tc.full, KnowledgeBaseIDs: types.StringArray{"kb-a"}, Capabilities: tc.caps,
			}
			if tc.platform {
				scope.ScopeType = types.APIKeyScopePlatform
			}
			got := agent24UISessionOps(scope)
			assert.Equal(t, tc.want, got)
			got[0] = "mutated" // a fresh slice every call
			assert.Equal(t, agent24UIReadOpsWant, agent24uisession.UIRouteOps())
		})
	}
}

// W8.12 (decision 2026-11-12-s5b-audit-ready Q1=A): action by exact method and FullPath, never by op alone.
func TestAgent24UIWriteAuditAction(t *testing.T) {
	const p = agent24uisession.UIReadPrefix
	want := map[string]types.AuditAction{
		http.MethodGet + " " + p + "/candidates":                        agent24AuditActionUIView,
		http.MethodGet + " " + p + "/candidates/:candidate_id":          agent24AuditActionUIView,
		http.MethodPost + " " + p + "/candidates/:candidate_id/return":  agent24AuditActionCandidateReturn,
		http.MethodPost + " " + p + "/candidates/:candidate_id/approve": agent24AuditActionCandidateApprove,
		http.MethodPost + " " + p + "/documents/:knowledge_id/withdraw": agent24AuditActionEntryWithdraw,
	}
	require.Len(t, agent24uisession.UIWriteRoutes, len(want))
	for _, rt := range agent24uisession.UIWriteRoutes {
		got, ok := agent24UIWriteAuditAction(rt.Method, rt.Path)
		require.True(t, ok, "%s %s", rt.Method, rt.Path)
		assert.Equal(t, want[rt.Method+" "+rt.Path], got, "%s %s", rt.Method, rt.Path)
	}
	assert.Equal(t, types.AuditAction("knowledge.candidate.approve"), agent24AuditActionCandidateApprove)
	assert.Equal(t, types.AuditAction("knowledge.candidate.return"), agent24AuditActionCandidateReturn)
	assert.Equal(t, types.AuditAction("knowledge.entry.withdraw"), agent24AuditActionEntryWithdraw)
	for _, rt := range agent24uisession.UIReadRoutes {
		_, ok := agent24UIWriteAuditAction(rt.Method, rt.Path)
		assert.False(t, ok, "read route %s %s", rt.Method, rt.Path)
	}
	for _, miss := range [][2]string{
		{http.MethodGet, agent24uisession.UIReadPrefix + "/candidates/:candidate_id/approve"},
		{http.MethodPost, agent24uisession.UIReadPrefix + "/candidates/:candidate_id"},
		{http.MethodPost, agent24uisession.UIReadPrefix + "/candidates/:candidate_id/approve/x"},
		{http.MethodPost, agent24AuditPathCandidateWithdraw},
		{http.MethodPost, ""},
	} {
		_, ok := agent24UIWriteAuditAction(miss[0], miss[1])
		assert.False(t, ok, "%s %s", miss[0], miss[1])
	}
}

// #168 N2: the credential gate lets exactly the five write routes through, by method and FullPath; the concrete
// URL path alone never does.
func TestAgent24UIBypassesCredentialWriteRoutes(t *testing.T) {
	for _, rt := range agent24uisession.UIWriteRoutes {
		concrete := strings.NewReplacer(":candidate_id", "cand-1", ":knowledge_id", "doc-1").Replace(rt.Path)
		assert.True(t, agent24UIBypassesCredential(rt.Method, concrete, rt.Path), "%s %s", rt.Method, rt.Path)
		assert.False(t, agent24UIBypassesCredential(rt.Method, concrete, ""), "unmatched %s %s", rt.Method, concrete)
		assert.False(t, agent24UIBypassesCredential(rt.Method, rt.Path, rt.Path+"/x"), "%s %s/x", rt.Method, rt.Path)
		other := http.MethodPost
		if rt.Method == http.MethodPost {
			other = http.MethodGet
		}
		assert.False(t, agent24UIBypassesCredential(other, concrete, rt.Path), "%s %s", other, rt.Path)
		assert.False(t, agent24UIBypassesCredential(rt.Method, concrete, strings.ToUpper(rt.Path)),
			"case %s %s", rt.Method, rt.Path)
	}
	// the C routes stay behind the gate
	for _, p := range []string{
		agent24AuditPathCandidates, agent24AuditPathCandidateWithdraw, agent24AuditPathCandidateGet,
	} {
		assert.False(t, agent24UIBypassesCredential(http.MethodPost, p, p), p)
	}
}

// capabilities declares review, approve and entry.withdraw exactly when the UI write routes are registered:
// integration mode, the revision key, and the UI session key.
func TestAgent24KnowledgeCapabilityUIWriteOps(t *testing.T) {
	tag := base64.StdEncoding.EncodeToString(make([]byte, 32))
	session := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("s", 32)))
	writes := []string{"candidate.review", "candidate.approve", "entry.withdraw"}
	for _, tc := range []struct {
		name, mode, tag, session string
		want                     []string
	}{
		{
			"integration with both keys", Agent24KnowledgeModeIntegration, tag, session,
			append([]string{"search", "read", "candidate.propose"}, writes...),
		},
		{
			"integration without session key", Agent24KnowledgeModeIntegration, tag, "",
			[]string{"search", "read", "candidate.propose"},
		},
		{"integration without revision key", Agent24KnowledgeModeIntegration, "", session, []string{}},
		{"standalone with both keys", "", tag, session, []string{"search", "read"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(Agent24KnowledgeModeEnv, tc.mode)
			agent24SetTagKey(t, tc.tag)
			t.Setenv(agent24uisession.KeyEnv, tc.session)
			assert.Equal(t, tc.want, BuildAgent24KnowledgeCapability().SupportedOperations)
			assert.Equal(t, slices.Contains(tc.want, "entry.withdraw"), Agent24UIWriteRoutesEnabled())
			// decision 2026-11-12-s5b-audit-ready Q2=A: ready's operations.candidate keeps S2's rule (the propose
			// routes), with or without the UI write routes.
			assert.Equal(t, slices.Contains(tc.want, "candidate.propose"),
				NewAgent24KnowledgeReadyHandler(nil, nil, nil).candidates)
		})
	}
}

// P1 through the real Exchange (Codex S5b r1 C1): the response's ops and the signed JWT's Ops are both the read ops
// plus exactly the write ops C_now holds a capability for, the same list in UIOps order.
func TestAgent24UIExchangeOpsFollowCredential(t *testing.T) {
	with := func(extra ...string) []string { return append(slices.Clone(agent24UIReadOpsWant), extra...) }
	for i, tc := range []struct {
		name string
		full bool
		caps []string
		want []string
	}{
		{"retrieve only", false, []string{"retrieve"}, with()},
		{"review", false, []string{"retrieve", "kb_candidate_review"}, with("candidate.review")},
		{"approve", false, []string{"retrieve", "kb_candidate_approve"}, with("candidate.approve")},
		{"withdraw", false, []string{"retrieve", "kb_entry_withdraw"}, with("entry.withdraw")},
		{
			"all three, listed backwards", false,
			[]string{"kb_entry_withdraw", "kb_candidate_approve", "kb_candidate_review", "retrieve"},
			with("candidate.review", "candidate.approve", "entry.withdraw"),
		},
		{"propose only", false, []string{"retrieve", "kb_candidate_propose"}, with()},
		// full access never reaches the ops: Exchange refuses it as empty_scope (R2.8, agent24UIEffectiveScope)
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, store := agent24UIHandlerDB(t)
			proof, vph := agent24UIViewSecret()
			raw := agent24UIRawTicket(byte(40 + i))
			agent24UISeedTicket(t, store, raw, vph, nil)
			key := agent24UILiveKey([]string{"kb-b", "kb-a"}, tc.full, tc.caps, nil)
			h := agent24UIExchangeHandler(store, []*types.TenantAPIKey{key}, nil, nil, nil)
			got, _ := agent24UIExchangeCall(h, raw, proof, "")
			require.Equal(t, http.StatusOK, got.Code, got.Body.String())
			var resp struct {
				SessionToken string   `json:"session_token"`
				Ops          []string `json:"ops"`
			}
			require.NoError(t, json.Unmarshal(got.Body.Bytes(), &resp))
			assert.Equal(t, tc.want, resp.Ops, "response ops")
			claims, err := agent24uisession.Verify(
				agent24UIExchangeKey(), agent24uisession.DefaultIssuer, resp.SessionToken, time.Now())
			require.NoError(t, err)
			assert.Equal(t, tc.want, claims.Ops, "JWT ops")
		})
	}
}
