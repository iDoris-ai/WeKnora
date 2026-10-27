// Agent24 fork product patch A24-KB-23 (KB-C1-03 C3-d, WeKnora#41, CONTRACT-READONLY-v1 R7.6).

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// agent24FixedRecheck answers Recheck with a fixed reason and error. The zero value returns the
// context unchanged, which is a credential that still stands.
type agent24FixedRecheck struct {
	reason string
	err    error
}

func (r agent24FixedRecheck) Recheck(ctx context.Context, _ http.Header) (context.Context, string, error) {
	return ctx, r.reason, r.err
}

// agent24ScopeThen resolves through inner on the first call and returns then afterwards.
// A nil then is an empty E.
type agent24ScopeThen struct {
	inner agent24ScopeResolver
	then  []*types.KnowledgeBase
	calls int
}

func (s *agent24ScopeThen) Resolve(ctx context.Context, ids []string) ([]*types.KnowledgeBase, error) {
	s.calls++
	if s.calls == 1 && s.inner != nil {
		return s.inner.Resolve(ctx, ids)
	}
	return s.then, nil
}

// agent24RecheckKeyStub embeds TenantAPIKeyService and answers only AuthenticateAPIKey.
type agent24RecheckKeyStub struct {
	interfaces.TenantAPIKeyService
	key *types.TenantAPIKey
	err error
}

func (s agent24RecheckKeyStub) AuthenticateAPIKey(context.Context, string) (*types.TenantAPIKey, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.key, nil
}

// agent24RecheckTenantStub embeds TenantService and answers only GetTenantByID.
type agent24RecheckTenantStub struct {
	interfaces.TenantService
	tenant *types.Tenant
	err    error
}

func (s agent24RecheckTenantStub) GetTenantByID(context.Context, uint64) (*types.Tenant, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.tenant, nil
}

// agent24RecheckList is a revocation store kept only so newAgent24Rechecker can hold it.
type agent24RecheckList struct{}

func (agent24RecheckList) Revoke(context.Context, uint64, string, time.Time) error { return nil }

func (agent24RecheckList) IsRevoked(context.Context, uint64, string, time.Time) (bool, error) {
	return false, nil
}

func (agent24RecheckList) PurgeExpired(context.Context, time.Time) (int64, error) { return 0, nil }

func agent24RecheckTestCtx(tenantID, keyID uint64, kbIDs []string) context.Context {
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: tenantID})
	ctx = context.WithValue(ctx, types.TenantInfoContextKey, &types.Tenant{ID: tenantID})
	ctx = types.WithPrincipal(ctx, types.Principal{
		Type: types.PrincipalAPITenant, ID: strconv.FormatUint(tenantID, 10),
	})
	return types.WithTenantAPIKeyScope(ctx, types.TenantAPIKeyScope{
		KeyID: keyID, Name: "old", ScopeType: types.APIKeyScopeTenant,
		KnowledgeBaseIDs: types.StringArray(kbIDs),
		Capabilities:     types.StringArray{string(types.APIKeyCapabilityRetrieve)},
	})
}

func agent24RecheckHeader() http.Header {
	h := http.Header{}
	h.Set("X-API-Key", "sk-live")
	return h
}

func agent24RequireErrorCode(t *testing.T, body []byte, code, reason string) {
	t.Helper()
	var parsed struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(body, &parsed), string(body))
	require.Equal(t, code, parsed.Error.Code, string(body))
	if reason == "" {
		_, ok := parsed.Error.Details["reason"]
		require.False(t, ok, string(body))
		return
	}
	require.Equal(t, reason, parsed.Error.Details["reason"], string(body))
}

func TestAgent24DefaultRechecker(t *testing.T) {
	t.Run("no api key scope", func(t *testing.T) {
		ctx := context.Background()
		r := agent24DefaultRechecker{
			tenants: agent24RecheckTenantStub{err: errors.New("called")},
			keys:    agent24RecheckKeyStub{err: errors.New("called")},
		}
		got, reason, err := r.Recheck(ctx, agent24RecheckHeader())
		require.NoError(t, err)
		require.Empty(t, reason)
		require.True(t, got == ctx)
	})

	tid := uint64(7)
	ctx := agent24RecheckTestCtx(tid, 4, []string{"kb-old"})
	t.Run("tenants nil", func(t *testing.T) {
		r := agent24DefaultRechecker{keys: agent24RecheckKeyStub{}}
		_, reason, err := r.Recheck(ctx, agent24RecheckHeader())
		require.ErrorIs(t, err, errAgent24RecheckUnwired)
		require.Empty(t, reason)
	})
	t.Run("keys nil", func(t *testing.T) {
		r := agent24DefaultRechecker{tenants: agent24RecheckTenantStub{tenant: &types.Tenant{ID: tid}}}
		_, reason, err := r.Recheck(ctx, agent24RecheckHeader())
		require.ErrorIs(t, err, errAgent24RecheckUnwired)
		require.Empty(t, reason)
	})

	matching := agent24RecheckTenantStub{tenant: &types.Tenant{ID: tid}}
	t.Run("lost retrieve", func(t *testing.T) {
		r := agent24DefaultRechecker{tenants: matching, keys: agent24RecheckKeyStub{key: &types.TenantAPIKey{
			ID: 4, Name: "narrowed", ScopeType: types.APIKeyScopeTenant, TenantID: &tid,
			Capabilities: types.StringArray{string(types.APIKeyCapabilityChat)},
		}}}
		_, reason, err := r.Recheck(ctx, agent24RecheckHeader())
		require.NoError(t, err)
		require.Equal(t, "credential_invalid", reason)
	})
	t.Run("full access", func(t *testing.T) {
		r := agent24DefaultRechecker{tenants: matching, keys: agent24RecheckKeyStub{key: &types.TenantAPIKey{
			ID: 4, Name: "full", ScopeType: types.APIKeyScopeTenant, TenantID: &tid, FullAccess: true,
			KnowledgeBaseIDs: types.StringArray{"kb-new"},
		}}}
		got, reason, err := r.Recheck(ctx, agent24RecheckHeader())
		require.NoError(t, err)
		require.Empty(t, reason)
		fresh, ok := types.TenantAPIKeyScopeFromContext(got)
		require.True(t, ok)
		require.Equal(t, (types.TenantAPIKeyScope{
			KeyID: 4, Name: "full", ScopeType: types.APIKeyScopeTenant, FullAccess: true,
			KnowledgeBaseIDs: types.StringArray{"kb-new"},
		}).Normalize(), fresh)
	})
}

func TestNewAgent24Rechecker(t *testing.T) {
	store := &agent24RecheckList{}
	t.Setenv(Agent24KnowledgeModeEnv, Agent24KnowledgeModeStandalone)
	standalone := newAgent24Rechecker(nil, nil, store)
	require.Nil(t, standalone.revocations)

	t.Setenv(Agent24KnowledgeModeEnv, Agent24KnowledgeModeIntegration)
	integration := newAgent24Rechecker(nil, nil, store)
	if integration.revocations != store {
		t.Fatalf("revocations = %#v, want the store passed in", integration.revocations)
	}
}

func TestAgent24RecheckScope(t *testing.T) {
	header := http.Header{}
	ids := []string{"a"}
	scope := []string{"a", "b"}
	okResolver := &fakeAgent24Scope{kbs: map[string]*types.KnowledgeBase{"a": {ID: "a"}, "b": {ID: "b"}}}

	t.Run("recheck nil", func(t *testing.T) {
		got, kerr, gone := agent24RecheckScope(context.Background(), nil, okResolver, header, ids, scope)
		require.False(t, gone)
		require.Nil(t, got)
		require.NotNil(t, kerr)
		require.Equal(t, agent24CodeInternal, kerr.Code)
	})
	t.Run("recheck error", func(t *testing.T) {
		got, kerr, gone := agent24RecheckScope(context.Background(), agent24FixedRecheck{err: errors.New("boom")},
			okResolver, header, ids, scope)
		require.False(t, gone)
		require.Nil(t, got)
		require.NotNil(t, kerr)
		require.Equal(t, agent24CodeInternal, kerr.Code)
	})
	t.Run("reason", func(t *testing.T) {
		got, kerr, gone := agent24RecheckScope(context.Background(),
			agent24FixedRecheck{reason: "credential_revoked"}, okResolver, header, ids, scope)
		require.False(t, gone)
		require.Nil(t, got)
		require.NotNil(t, kerr)
		require.Equal(t, agent24CodeUnauthorized, kerr.Code)
		require.Equal(t, "credential_revoked", kerr.Reason)
	})
	t.Run("resolve error", func(t *testing.T) {
		resolver := &fakeAgent24Scope{err: errors.New("resolve down")}
		got, kerr, gone := agent24RecheckScope(
			context.Background(), agent24FixedRecheck{}, resolver, header, ids, scope,
		)
		require.False(t, gone)
		require.Nil(t, got)
		require.NotNil(t, kerr)
		require.Equal(t, agent24CodeUnavailable, kerr.Code)
		require.Equal(t, "dependency", kerr.Reason)
	})
	t.Run("empty", func(t *testing.T) {
		resolver := &fakeAgent24Scope{kbs: map[string]*types.KnowledgeBase{"z": {ID: "z"}}}
		got, kerr, gone := agent24RecheckScope(context.Background(), agent24FixedRecheck{}, resolver, header,
			[]string{"z"}, []string{"a", "b"})
		require.False(t, gone)
		require.Nil(t, got)
		require.NotNil(t, kerr)
		require.Equal(t, agent24CodeForbidden, kerr.Code)
		require.Equal(t, "empty_scope", kerr.Reason)
	})
	t.Run("intersection keeps scope order", func(t *testing.T) {
		resolver := &fakeAgent24Scope{kbs: map[string]*types.KnowledgeBase{
			"b": {ID: "b"}, "z": {ID: "z"}, "a": {ID: "a"},
		}}
		got, kerr, gone := agent24RecheckScope(context.Background(), agent24FixedRecheck{}, resolver, header,
			[]string{"b", "z", "a"}, []string{"c", "a", "b"})
		require.False(t, gone)
		require.Nil(t, kerr)
		require.Equal(t, []string{"a", "b"}, got)
	})
}

func TestAgent24NarrowSearch(t *testing.T) {
	hits := []agent24SearchHit{
		{Rank: 9, KnowledgeBaseID: "a", ChunkID: "c1"},
		{Rank: 4, KnowledgeBaseID: "b", ChunkID: "c2"},
		{Rank: 7, KnowledgeBaseID: "c", ChunkID: "c3"},
	}

	t.Run("filter and renumber", func(t *testing.T) {
		comp, got, failures := agent24NarrowSearch([]string{"c", "a"}, "partial", hits, nil)
		require.Equal(t, "partial", comp)
		require.Nil(t, failures)
		require.Equal(t, []agent24SearchHit{
			{Rank: 1, KnowledgeBaseID: "a", ChunkID: "c1"},
			{Rank: 2, KnowledgeBaseID: "c", ChunkID: "c3"},
		}, got)
	})
	t.Run("failures become empty", func(t *testing.T) {
		comp, _, failures := agent24NarrowSearch([]string{"a"}, "partial", hits, []agent24SearchFailure{
			{KnowledgeBaseID: "z", Reason: "drop"}, {KnowledgeBaseID: "y", Reason: "drop"},
		})
		require.Equal(t, "complete", comp)
		require.Nil(t, failures)
	})
	t.Run("failures remain", func(t *testing.T) {
		comp, _, failures := agent24NarrowSearch([]string{"a", "b"}, "partial", hits, []agent24SearchFailure{
			{KnowledgeBaseID: "z", Reason: "drop"},
			{KnowledgeBaseID: "a", Reason: "keep-a"},
			{KnowledgeBaseID: "b", Reason: "keep-b"},
		})
		require.Equal(t, "partial", comp)
		require.Equal(t, []agent24SearchFailure{
			{KnowledgeBaseID: "a", Reason: "keep-a"},
			{KnowledgeBaseID: "b", Reason: "keep-b"},
		}, failures)
	})
	t.Run("no failures", func(t *testing.T) {
		comp, _, failures := agent24NarrowSearch([]string{"a"}, "partial", hits, nil)
		require.Equal(t, "partial", comp)
		require.Nil(t, failures)
		comp, _, failures = agent24NarrowSearch([]string{"a"}, "complete", hits, []agent24SearchFailure{})
		require.Equal(t, "complete", comp)
		require.Nil(t, failures)
	})
	t.Run("hits all dropped", func(t *testing.T) {
		_, got, _ := agent24NarrowSearch([]string{"z"}, "complete", hits, nil)
		require.NotNil(t, got)
		require.Empty(t, got)
	})
}
