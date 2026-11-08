// Agent24 fork product patch A24-KB-40 (WeKnora#135).
package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
)

var agent24AllWriteOps = []Agent24WriteOp{
	Agent24WriteCandidatePropose, Agent24WriteCandidateReview, Agent24WriteCandidateApprove, Agent24WriteEntryWithdraw,
}

type agent24AllWriteGrants struct{} // A as if every organisation role were held

func (agent24AllWriteGrants) HoldsAnywhere(context.Context, Agent24WriteOp) (bool, error) {
	return true, nil
}

type agent24FailingWriteGrants struct{}

func (agent24FailingWriteGrants) HoldsAnywhere(context.Context, Agent24WriteOp) (bool, error) {
	return false, errors.New("SECRET-db")
}

// agent24WriteCtx is a tenant-1 request whose credential is a tenant API key with these capabilities and KBs.
func agent24WriteCtx(scope types.TenantAPIKeyScope) context.Context {
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 1})
	return types.WithTenantAPIKeyScope(ctx, scope)
}

func agent24WriteAuth(grants agent24WriteGrants) (agent24WriteAuthorizer, *[][]string) {
	kbs := map[string]*types.KnowledgeBase{
		"kbA": {ID: "kbA", TenantID: 1}, "kbB": {ID: "kbB", TenantID: 1}, "other": {ID: "other", TenantID: 2},
	}
	var q [][]string
	scope := agent24DefaultScopeResolver{fakeAgent24KB{kbs: kbs, queries: &q}}
	return agent24WriteAuthorizer{grants: grants, scope: scope}, &q
}

func agent24WriteOutcome(e *agent24KnowledgeError) string {
	if e == nil {
		return "ok"
	}
	return string(e.Code) + "/" + e.Reason
}

func TestAgent24AuthorizeWriteCredentialScope(t *testing.T) {
	all := types.StringArray{
		"retrieve", "kb_candidate_propose", "kb_candidate_review", "kb_candidate_approve",
		"kb_entry_withdraw",
	}
	cases := []struct {
		name  string
		scope types.TenantAPIKeyScope
		op    Agent24WriteOp
		kb    string
		want  string
	}{
		// no write scope: a read-only key holds none of the four
		{"read-only propose", types.TenantAPIKeyScope{
			Capabilities:     types.StringArray{"retrieve"},
			KnowledgeBaseIDs: types.StringArray{"kbA"},
		}, Agent24WriteCandidatePropose, "kbA", "forbidden/operation_not_allowed"},
		{"ingest is not propose", types.TenantAPIKeyScope{
			Capabilities:     types.StringArray{"retrieve", "ingest"},
			KnowledgeBaseIDs: types.StringArray{"kbA"},
		}, Agent24WriteCandidatePropose, "kbA", "forbidden/operation_not_allowed"},
		{"propose in E", types.TenantAPIKeyScope{
			Capabilities:     types.StringArray{"retrieve", "kb_candidate_propose"},
			KnowledgeBaseIDs: types.StringArray{"kbA"},
		}, Agent24WriteCandidatePropose, "kbA", "ok"},
		{"propose names no KB", types.TenantAPIKeyScope{
			Capabilities:     types.StringArray{"kb_candidate_propose"},
			KnowledgeBaseIDs: types.StringArray{"kbA"},
		}, Agent24WriteCandidatePropose, "", "ok"},
		// cross-KB: the target is outside the key's allow-list, in another tenant, or does not exist
		{
			"cross KB",
			types.TenantAPIKeyScope{Capabilities: all, KnowledgeBaseIDs: types.StringArray{"kbA"}},
			Agent24WriteCandidatePropose, "kbB", "forbidden/empty_scope",
		},
		{
			"other tenant",
			types.TenantAPIKeyScope{Capabilities: all, KnowledgeBaseIDs: types.StringArray{"kbA", "other"}},
			Agent24WriteCandidatePropose, "other", "forbidden/empty_scope",
		},
		{
			"missing KB",
			types.TenantAPIKeyScope{Capabilities: all, KnowledgeBaseIDs: types.StringArray{"kbA", "gone"}},
			Agent24WriteCandidatePropose, "gone", "forbidden/empty_scope",
		},
		// an empty allow-list is an empty E, never "all" (R2.2–R2.4)
		{
			"empty allow-list",
			types.TenantAPIKeyScope{Capabilities: all},
			Agent24WriteCandidatePropose, "kbA",
			"forbidden/empty_scope",
		},
		// FullAccess does not imply a write class, and with one granted it still has no write E
		{
			"full access",
			types.TenantAPIKeyScope{FullAccess: true},
			Agent24WriteCandidatePropose, "kbA",
			"forbidden/operation_not_allowed",
		},
		{"full access with grant", types.TenantAPIKeyScope{
			FullAccess: true, Capabilities: all,
			KnowledgeBaseIDs: types.StringArray{"kbA"},
		}, Agent24WriteCandidatePropose, "kbA", "forbidden/empty_scope"},
		{"platform key", types.TenantAPIKeyScope{
			ScopeType: types.APIKeyScopePlatform, Capabilities: all,
			KnowledgeBaseIDs: types.StringArray{"kbA"},
		}, Agent24WriteCandidatePropose, "kbA", "forbidden/operation_not_allowed"},
		// W1.9 ③(a): a missing capability is judged before the target, so a cross-KB request without it is not
		// told whether the KB is in E
		{"order: capability before target", types.TenantAPIKeyScope{
			Capabilities:     types.StringArray{"retrieve"},
			KnowledgeBaseIDs: types.StringArray{"kbA"},
		}, Agent24WriteCandidatePropose, "kbB", "forbidden/operation_not_allowed"},
		{
			"unknown op",
			types.TenantAPIKeyScope{Capabilities: all, KnowledgeBaseIDs: types.StringArray{"kbA"}},
			Agent24WriteOp("ingest.submit"), "kbA", "forbidden/operation_not_allowed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			auth, q := agent24WriteAuth(agent24AllWriteGrants{})
			kerr, gone := auth.Authorize(agent24WriteCtx(tc.scope), tc.op, tc.kb)
			assert.False(t, gone)
			assert.Equal(t, tc.want, agent24WriteOutcome(kerr))
			if tc.want == "forbidden/operation_not_allowed" {
				assert.Empty(t, *q, "no target lookup before the capability check")
			}
		})
	}
	t.Run("no tenant API key", func(t *testing.T) { // a JWT user carries no write capability
		auth, _ := agent24WriteAuth(agent24AllWriteGrants{})
		ctx := types.WithCaller(context.Background(), types.Caller{TenantID: 1})
		kerr, _ := auth.Authorize(ctx, Agent24WriteCandidatePropose, "kbA")
		assert.Equal(t, "forbidden/operation_not_allowed", agent24WriteOutcome(kerr))
	})
}

// Until WeKnora#146 wires the organisation roles (W2.5), review, approve and withdraw are held by nobody,
// even with the capability on the key; propose needs only the target in E.
func TestAgent24AuthorizeWriteDefaultGrants(t *testing.T) {
	scope := types.TenantAPIKeyScope{KnowledgeBaseIDs: types.StringArray{"kbA"}, Capabilities: types.StringArray{
		"retrieve", "kb_candidate_propose", "kb_candidate_review", "kb_candidate_approve", "kb_entry_withdraw",
	}}
	auth, _ := agent24WriteAuth(agent24DefaultWriteGrants{})
	got := map[Agent24WriteOp]string{}
	for _, op := range agent24AllWriteOps {
		kerr, _ := auth.Authorize(agent24WriteCtx(scope), op, "kbA")
		got[op] = agent24WriteOutcome(kerr)
	}
	assert.Equal(t, map[Agent24WriteOp]string{
		Agent24WriteCandidatePropose: "ok",
		Agent24WriteCandidateReview:  "forbidden/operation_not_allowed",
		Agent24WriteCandidateApprove: "forbidden/operation_not_allowed",
		Agent24WriteEntryWithdraw:    "forbidden/operation_not_allowed",
	}, got)

	// each capability grants its own class only
	for _, op := range agent24AllWriteOps {
		one := types.TenantAPIKeyScope{
			KnowledgeBaseIDs: types.StringArray{"kbA"},
			Capabilities:     types.StringArray{"retrieve", string(agent24WriteCapabilities[op])},
		}
		for _, other := range agent24AllWriteOps {
			held := agent24CredentialHoldsWrite(agent24WriteCtx(one), other)
			assert.Equal(t, op == other, held, "%s grants %s", op, other)
		}
	}

	failing, _ := agent24WriteAuth(agent24FailingWriteGrants{})
	kerr, gone := failing.Authorize(agent24WriteCtx(scope), Agent24WriteCandidatePropose, "kbA")
	assert.Equal(t, []any{"unavailable/dependency", false}, []any{agent24WriteOutcome(kerr), gone})
}

func TestAgent24WriteInScope(t *testing.T) {
	auth, _ := agent24WriteAuth(agent24AllWriteGrants{})
	key := types.TenantAPIKeyScope{
		Capabilities: types.StringArray{"retrieve"}, KnowledgeBaseIDs: types.StringArray{"kbA"},
	}
	for _, tc := range []struct {
		scope types.TenantAPIKeyScope
		kb    string
		want  bool
	}{
		{key, "kbA", true},
		{key, "kbB", false},
		{key, "", false},
		{types.TenantAPIKeyScope{Capabilities: types.StringArray{"retrieve"}}, "kbA", false}, // empty allow-list
		{types.TenantAPIKeyScope{FullAccess: true}, "kbA", false},
	} {
		got, err := auth.InScope(agent24WriteCtx(tc.scope), tc.kb)
		require.NoError(t, err)
		assert.Equal(t, tc.want, got, "%+v %q", tc.scope, tc.kb)
	}
	broken := agent24WriteAuthorizer{grants: agent24AllWriteGrants{}, scope: agent24DefaultScopeResolver{
		fakeAgent24KB{queries: new([][]string)},
	}} // nil kbs: the lookup fails
	_, err := broken.InScope(agent24WriteCtx(key), "kbA")
	require.Error(t, err)
	kerr, _ := broken.Authorize(agent24WriteCtx(types.TenantAPIKeyScope{
		KnowledgeBaseIDs: types.StringArray{"kbA"},
		Capabilities:     types.StringArray{"kb_candidate_propose"},
	}), Agent24WriteCandidatePropose, "kbA")
	assert.Equal(t, "unavailable/dependency", agent24WriteOutcome(kerr))
}

// Reads are untouched: a key that also carries every write capability gets the same ready answer, byte for
// byte, as a read-only key; the wire gains no key (Agent24 clients decode operations strictly, design §4).
func TestAgent24WriteCapabilitiesLeaveReadyUnchanged(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := `{"protocol_version":1,"request_id":"r1","generation":7,"scope":{"knowledge_base_ids":["kbA"]}}`
	ready := func(caps ...string) string {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		ctx := agent24WriteCtx(types.TenantAPIKeyScope{Capabilities: caps, KnowledgeBaseIDs: types.StringArray{"kbA"}})
		c.Request = httptest.NewRequestWithContext(ctx, http.MethodPost, "/", strings.NewReader(body))
		kbs := map[string]*types.KnowledgeBase{"kbA": {ID: "kbA", TenantID: 1}}
		h := &Agent24KnowledgeReadyHandler{
			probe: fakeAgent24ReadyProbe{},
			scope: agent24DefaultScopeResolver{fakeAgent24KB{kbs: kbs, queries: new([][]string)}},
			index: fakeAgent24Index{},
		}
		h.Ready(c)
		require.Equal(t, http.StatusOK, w.Code)
		return w.Body.String()
	}
	readOnly := ready("retrieve")
	assert.Equal(t, readOnly, ready("retrieve", "kb_candidate_propose", "kb_candidate_review", "kb_candidate_approve",
		"kb_entry_withdraw"))
	assert.Contains(t, readOnly, `"ingest":{"state":"disabled","reason":"read_only_v1"}`)
	assert.NotContains(t, readOnly, "candidate")
}

// Key issuance accepts the four write capabilities (the unknown-capability check uses the same switch), and
// still rejects a misspelt one.
func TestAgent24WriteCapabilitiesAcceptedAtIssuance(t *testing.T) {
	ok := tenantAPIKeyCreateRequest{Name: "n", Capabilities: []string{
		"retrieve", "kb_candidate_propose",
		"kb_candidate_review", "kb_candidate_approve", "kb_entry_withdraw",
	}}
	assert.Nil(t, validateTenantAPIKeyRequest(context.Background(), nil, 1, ok))
	bad := tenantAPIKeyCreateRequest{Name: "n", Capabilities: []string{"retrieve", "kb_candidate_publish"}}
	assert.NotNil(t, validateTenantAPIKeyRequest(context.Background(), nil, 1, bad))
}
