// Agent24 fork product patch A24-KB-52 (KB-C4-02a mock, Loop 7 first round F2a).
package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

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
	require.Equal(t, agent24OnboardKeyPrefix+"api_tenant:1", k.Name)
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
