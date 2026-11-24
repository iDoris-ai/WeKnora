// Agent24 fork product patch A24-KB-52 (KB-C4-02a mock, Loop 7 first round F2a).
//
// IssueAgent24OnboardKey creates a tenant-scope retrieve+ingest key for the owner's default KB
// and revokes an earlier key of the same name. The token is never logged.

package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

const agent24OnboardKeyPrefix = "agent24-onboard "

// Agent24OnboardKey is the issued credential. Token is shown once to the caller.
type Agent24OnboardKey struct {
	Token     string
	KeyID     uint64
	ExpiresAt *time.Time
}

// agent24OnboardKeyName names an owner's onboarding keys by a hash of the owner, so the name never needs trimming
// or truncation (the key service stores the name trimmed, at most 128 bytes): two owners can never share a name.
func agent24OnboardKeyName(owner string) string {
	sum := sha256.Sum256([]byte(owner))
	return agent24OnboardKeyPrefix + hex.EncodeToString(sum[:16])
}

// IssueAgent24OnboardKey creates a retrieve+ingest key for kbID and revokes an earlier key of the same name.
func IssueAgent24OnboardKey(
	ctx context.Context, keys interfaces.TenantAPIKeyService, tenantID uint64, owner, kbID string,
	ttl time.Duration, now time.Time,
) (Agent24OnboardKey, error) {
	if ttl <= 0 {
		return Agent24OnboardKey{}, errors.New("agent24 onboard key: a positive lifetime is required")
	}
	exp := now.Add(ttl)
	expiresAt := &exp
	name := agent24OnboardKeyName(owner)
	created, err := keys.CreateAPIKey(ctx, interfaces.TenantAPIKeyCreateRequest{
		TenantID:         tenantID,
		ScopeType:        types.APIKeyScopeTenant,
		Name:             name,
		FullAccess:       false,
		KnowledgeBaseIDs: []string{kbID},
		Capabilities: []string{
			string(types.APIKeyCapabilityRetrieve),
			string(types.APIKeyCapabilityIngest),
		},
		ExpiresAt: expiresAt,
	})
	if err != nil {
		return Agent24OnboardKey{}, err
	}
	listed, err := keys.ListAPIKeys(ctx, tenantID)
	if err != nil {
		return Agent24OnboardKey{}, err
	}
	for _, k := range listed {
		// Only keys older than this one: a concurrent issue for the same owner never revokes a newer key, so
		// the newest issued key always stays live.
		if k == nil || k.ID >= created.APIKey.ID || k.Name != name || k.RevokedAt != nil {
			continue
		}
		// Already revoked by a concurrent issue for the same owner: not an error.
		err := keys.RevokeAPIKey(ctx, tenantID, k.ID)
		if err != nil && !errors.Is(err, repository.ErrTenantAPIKeyNotFound) {
			// The new key stays valid; the caller fails the request.
			return Agent24OnboardKey{}, err
		}
	}
	return Agent24OnboardKey{
		Token: created.Token, KeyID: created.APIKey.ID, ExpiresAt: created.APIKey.ExpiresAt,
	}, nil
}
