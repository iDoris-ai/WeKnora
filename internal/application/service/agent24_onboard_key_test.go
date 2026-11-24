// Agent24 fork product patch A24-KB-52 (KB-C4-02a mock, Loop 7 first round F2a).
package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type fakeOnboardKeys struct {
	interfaces.TenantAPIKeyService
	nextID    uint64
	keys      []*types.TenantAPIKey
	revokeErr error
}

func (f *fakeOnboardKeys) CreateAPIKey(
	_ context.Context, req interfaces.TenantAPIKeyCreateRequest,
) (*interfaces.TenantAPIKeyCreateResult, error) {
	f.nextID++
	var tenantID *uint64
	if req.TenantID != 0 {
		id := req.TenantID
		tenantID = &id
	}
	var exp *time.Time
	if req.ExpiresAt != nil {
		t := *req.ExpiresAt
		exp = &t
	}
	key := &types.TenantAPIKey{
		ID:               f.nextID,
		TenantID:         tenantID,
		ScopeType:        req.ScopeType,
		Name:             req.Name,
		FullAccess:       req.FullAccess,
		KnowledgeBaseIDs: append(types.StringArray(nil), req.KnowledgeBaseIDs...),
		Capabilities:     append(types.StringArray(nil), req.Capabilities...),
		ExpiresAt:        exp,
	}
	f.keys = append(f.keys, key)
	return &interfaces.TenantAPIKeyCreateResult{
		APIKey: key, Token: fmt.Sprintf("tok-%d", key.ID),
	}, nil
}

func (f *fakeOnboardKeys) ListAPIKeys(_ context.Context, tenantID uint64) ([]*types.TenantAPIKey, error) {
	out := make([]*types.TenantAPIKey, 0, len(f.keys))
	for _, k := range f.keys {
		if k.TenantIDValue() == tenantID {
			out = append(out, k)
		}
	}
	return out, nil
}

func (f *fakeOnboardKeys) RevokeAPIKey(_ context.Context, _ uint64, id uint64) error {
	if f.revokeErr != nil {
		return f.revokeErr
	}
	now := time.Now().UTC()
	for _, k := range f.keys {
		if k.ID == id {
			k.RevokedAt = &now
			return nil
		}
	}
	return errors.New("missing key")
}

func TestAgent24OnboardKeyIssueScopedRetrieveIngest(t *testing.T) {
	keys := &fakeOnboardKeys{}
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	ttl := time.Hour
	got, err := IssueAgent24OnboardKey(context.Background(), keys, 7, "api_tenant:1", "kb-1", ttl, now)
	require.NoError(t, err)
	require.Equal(t, uint64(1), got.KeyID)
	require.NotEmpty(t, got.Token)
	require.NotNil(t, got.ExpiresAt)
	require.Equal(t, now.Add(ttl), *got.ExpiresAt)
	require.Len(t, keys.keys, 1)
	k := keys.keys[0]
	require.False(t, k.FullAccess)
	require.Equal(t, types.StringArray{"kb-1"}, k.KnowledgeBaseIDs)
	require.Equal(t, types.StringArray{
		string(types.APIKeyCapabilityRetrieve),
		string(types.APIKeyCapabilityIngest),
	}, k.Capabilities)
	require.Equal(t, agent24OnboardKeyName("api_tenant:1"), k.Name)
	require.Equal(t, types.APIKeyScopeTenant, k.ScopeType)
}

func TestAgent24OnboardKeyIssueRevokesEarlierSameOwner(t *testing.T) {
	keys := &fakeOnboardKeys{}
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	first, err := IssueAgent24OnboardKey(context.Background(), keys, 7, "api_tenant:1", "kb-1", time.Hour, now)
	require.NoError(t, err)
	second, err := IssueAgent24OnboardKey(
		context.Background(), keys, 7, "api_tenant:1", "kb-1", time.Hour, now.Add(time.Minute),
	)
	require.NoError(t, err)
	require.NotEqual(t, first.KeyID, second.KeyID)
	require.NotNil(t, keys.keys[0].RevokedAt)
	require.Nil(t, keys.keys[1].RevokedAt)
	require.Equal(t, second.KeyID, keys.keys[1].ID)
}

func TestAgent24OnboardKeyLeavesOtherOwner(t *testing.T) {
	keys := &fakeOnboardKeys{}
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	first, err := IssueAgent24OnboardKey(context.Background(), keys, 7, "api_tenant:1", "kb-1", time.Hour, now)
	require.NoError(t, err)
	_, err = IssueAgent24OnboardKey(context.Background(), keys, 7, "api_tenant:2", "kb-1", time.Hour, now)
	require.NoError(t, err)
	require.Nil(t, keys.keys[0].RevokedAt)
	require.Equal(t, first.KeyID, keys.keys[0].ID)
	require.Nil(t, keys.keys[1].RevokedAt)
}

func TestAgent24OnboardKeyRevokeError(t *testing.T) {
	keys := &fakeOnboardKeys{}
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	_, err := IssueAgent24OnboardKey(context.Background(), keys, 7, "api_tenant:1", "kb-1", time.Hour, now)
	require.NoError(t, err)
	keys.revokeErr = errors.New("revoke down")
	_, err = IssueAgent24OnboardKey(context.Background(), keys, 7, "api_tenant:1", "kb-1", time.Hour, now)
	require.Error(t, err)
	require.Len(t, keys.keys, 2)
	require.Nil(t, keys.keys[0].RevokedAt)
	require.Nil(t, keys.keys[1].RevokedAt)
}

// lockedKeyRepo is an in-memory TenantAPIKeyRepository safe for concurrent use, under the real key service.
type lockedKeyRepo struct {
	interfaces.TenantAPIKeyRepository
	mu   sync.Mutex
	keys []*types.TenantAPIKey
	next uint64
	// listAfter, when set, makes ListAPIKeys wait until that many keys were created (a deterministic interleave).
	listAfter int
	created   chan struct{}
	// listers, when set, makes each ListAPIKeys wait until that many lists have read (both see the same keys).
	listers int
	listed  int
	read    chan struct{}
}

func (r *lockedKeyRepo) CreateAPIKey(_ context.Context, key *types.TenantAPIKey) error {
	r.mu.Lock()
	r.next++
	key.ID = r.next
	cp := *key
	r.keys = append(r.keys, &cp)
	n := len(r.keys)
	r.mu.Unlock()
	if r.created != nil && n == r.listAfter {
		close(r.created)
	}
	return nil
}

func (r *lockedKeyRepo) ListAPIKeys(_ context.Context, tenantID uint64) ([]*types.TenantAPIKey, error) {
	if r.created != nil {
		<-r.created
	}
	r.mu.Lock()
	var out []*types.TenantAPIKey
	for i := len(r.keys) - 1; i >= 0; i-- { // newest first, as the real repository orders by created_at DESC
		k := r.keys[i]
		if k.TenantID != nil && *k.TenantID == tenantID {
			cp := *k
			out = append(out, &cp)
		}
	}
	if r.read != nil {
		r.listed++
		if r.listed == r.listers {
			close(r.read)
		}
	}
	read := r.read
	r.mu.Unlock()
	if read != nil {
		<-read // both callers saw the same keys before either revokes
	}
	return out, nil
}

func (r *lockedKeyRepo) RevokeAPIKey(_ context.Context, tenantID uint64, id uint64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, k := range r.keys {
		if k.ID == id && k.TenantID != nil && *k.TenantID == tenantID && k.RevokedAt == nil {
			now := time.Now()
			k.RevokedAt = &now
			return nil
		}
	}
	// as the real repository: zero rows is not found
	return repository.ErrTenantAPIKeyNotFound
}

func (r *lockedKeyRepo) live() []*types.TenantAPIKey {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*types.TenantAPIKey
	for _, k := range r.keys {
		if k.RevokedAt == nil {
			out = append(out, k)
		}
	}
	return out
}

// #173 G1: owners that share a long prefix, or end in a space, keep their own keys under the real key service.
func TestAgent24OnboardKeyNamesNeverCollideUnderTheRealService(t *testing.T) {
	repo := &lockedKeyRepo{}
	svc := NewTenantAPIKeyService(repo)
	ctx, now := context.Background(), time.Now()
	long := strings.Repeat("x", 120)
	a, b, spaced := long+"-a", long+"-b", "owner-with-space "
	for _, owner := range []string{a, b, spaced, spaced} {
		_, err := IssueAgent24OnboardKey(ctx, svc, 7, owner, "kb-1", time.Hour, now)
		require.NoError(t, err)
	}
	live := repo.live()
	require.Len(t, live, 3, "one live key per owner: a, b, and the second key of the spaced owner")
	names := map[string]bool{}
	for _, k := range live {
		names[k.Name] = true
	}
	require.Len(t, names, 3)
}

// #173 G2 / #174: two concurrent issues for one owner, both seeing the same older key, both succeed and leave the
// newest key live (deterministic: both list only after both created; a second revoke of the old key is not found).
func TestAgent24OnboardKeyConcurrentIssuesKeepTheNewest(t *testing.T) {
	repo := &lockedKeyRepo{}
	svc := NewTenantAPIKeyService(repo)
	ctx, now := context.Background(), time.Now()
	_, err := IssueAgent24OnboardKey(ctx, svc, 7, "api_tenant:7", "kb-1", time.Hour, now) // the older key X
	require.NoError(t, err)
	repo.listAfter, repo.created = 3, make(chan struct{})
	repo.listers, repo.read = 2, make(chan struct{})
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = IssueAgent24OnboardKey(ctx, svc, 7, "api_tenant:7", "kb-1", time.Hour, now)
		}(i)
	}
	wg.Wait()
	require.NoError(t, errs[0])
	require.NoError(t, errs[1])
	live := repo.live()
	require.Len(t, live, 1)
	require.Equal(t, uint64(3), live[0].ID, "the newest key stays live")
}

// #173 G3: a key that never expires is refused.
func TestAgent24OnboardKeyNeedsAPositiveLifetime(t *testing.T) {
	_, err := IssueAgent24OnboardKey(context.Background(), NewTenantAPIKeyService(&lockedKeyRepo{}), 7,
		"api_tenant:7", "kb-1", 0, time.Now())
	require.Error(t, err)
}
