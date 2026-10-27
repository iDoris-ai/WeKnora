// Agent24 fork product patch A24-KB-23 (KB-C1-03 C3-d, WeKnora#41, CONTRACT-READONLY-v1 R7.6).

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
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
	t.Run("signed_token entry", func(t *testing.T) {
		// The entry's mode, principal and jti come from the context Auth left: a signed_token request
		// passes only when all three are carried over.
		const secret = "test-secret"
		signed := &types.Tenant{ID: tid, APIPrincipalConfig: &types.APIPrincipalConfig{
			Mode: types.APIPrincipalModeSignedToken, HMACSecret: secret,
		}}
		token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
			"sub": "external-u1", "tenant_id": float64(tid), "aud": "weknora",
			"exp": time.Now().Add(time.Minute).Unix(), "jti": "jti-1",
		}).SignedString([]byte(secret))
		require.NoError(t, err)
		sctx := context.WithValue(ctx, types.TenantInfoContextKey, signed)
		sctx = types.WithPrincipal(sctx, types.Principal{Type: types.PrincipalAPIExternalUser, ID: "7:external-u1"})
		sctx = types.WithAgent24TokenJTI(sctx, "jti-1")
		header := agent24RecheckHeader()
		header.Set("X-External-User-Token", token)
		r := agent24DefaultRechecker{
			tenants: agent24RecheckTenantStub{tenant: signed},
			keys: agent24RecheckKeyStub{key: &types.TenantAPIKey{
				ID: 4, ScopeType: types.APIKeyScopeTenant, TenantID: &tid,
				Capabilities: types.StringArray{string(types.APIKeyCapabilityRetrieve)},
			}},
		}
		_, reason, err := r.Recheck(sctx, header)
		require.NoError(t, err)
		require.Empty(t, reason)
	})
	t.Run("platform key full access is not a grant", func(t *testing.T) {
		// Auth drops FullAccess for platform keys; the recheck must too, so retrieve is still required.
		pctx := types.WithCaller(context.Background(), types.Caller{TenantID: tid})
		pctx = types.WithPrincipal(pctx, types.Principal{Type: types.PrincipalAPIPlatform, ID: "5"})
		pctx = types.WithTenantAPIKeyScope(pctx, types.TenantAPIKeyScope{
			KeyID: 5, ScopeType: types.APIKeyScopePlatform,
			Capabilities: types.StringArray{string(types.APIKeyCapabilityRetrieve)},
		})
		r := agent24DefaultRechecker{tenants: matching, keys: agent24RecheckKeyStub{key: &types.TenantAPIKey{
			ID: 5, ScopeType: types.APIKeyScopePlatform, FullAccess: true,
		}}}
		_, reason, err := r.Recheck(pctx, agent24RecheckHeader())
		require.NoError(t, err)
		require.Equal(t, "credential_invalid", reason)
	})
}

// agent24RunWithContext runs h like the envs' run, and returns the gin context too, so a test can read
// the audit facts the handler noted.
func agent24RunWithContext(
	ctx context.Context, body string, h func(*gin.Context),
) (*httptest.ResponseRecorder, *gin.Context) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequestWithContext(ctx, http.MethodPost, "/", strings.NewReader(body))
	h(c)
	return w, c
}

func agent24NotedScope(t *testing.T, c *gin.Context) []string {
	t.Helper()
	v, ok := c.Get(agent24AuditFactsKey)
	require.True(t, ok)
	return v.(*agent24AuditFacts).EffectiveScope
}

func TestAgent24RecheckNarrowsAudit(t *testing.T) {
	t.Run("search", func(t *testing.T) {
		e := newAgent24SearchEnv()
		e.h.scope = &agent24ScopeThen{inner: e.scope, then: []*types.KnowledgeBase{{ID: "a"}}}
		w, c := agent24RunWithContext(agent24TenantCtx(), agent24SearchBody(`"b","a"`, agent24Budget), e.h.Search)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.Equal(t, []string{"a"}, agent24NotedScope(t, c))
	})
	t.Run("read keeps the document", func(t *testing.T) {
		e := newAgent24ReadEnv()
		e.h.scope = &agent24ScopeThen{inner: e.scope, then: []*types.KnowledgeBase{{ID: "a"}}}
		body := agent24ReadBodyOf(`"scope":{"knowledge_base_ids":["a","b"]}`, `"knowledge_id":"doc-1"`)
		w, c := agent24RunWithContext(agent24TenantCtx(), body, e.h.Read)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		var got struct {
			EffectiveScope struct {
				KnowledgeBaseIDs []string `json:"knowledge_base_ids"`
			} `json:"effective_scope"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &got))
		require.Equal(t, []string{"a"}, got.EffectiveScope.KnowledgeBaseIDs)
		require.Equal(t, []string{"a"}, agent24NotedScope(t, c))
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
	t.Run("recheck error after the caller left", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		got, kerr, gone := agent24RecheckScope(ctx, agent24FixedRecheck{err: errors.New("boom")},
			okResolver, header, ids, scope)
		require.True(t, gone)
		require.Nil(t, got)
		require.Nil(t, kerr)
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
