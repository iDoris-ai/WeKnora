// Agent24 fork product patch A24-KB-52 (KB-C4-02a mock, Loop 7 first round F2a).
//
// IssueAgent24OnboardKey creates a tenant-scope retrieve+ingest key for the owner's default KB
// and revokes an earlier key of the same name. The token is never logged.

package service

import (
	"context"
	"time"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

const (
	agent24OnboardKeyPrefix  = "agent24-onboard "
	agent24OnboardKeyNameMax = 128
)

// Agent24OnboardKey is the issued credential. Token is shown once to the caller.
type Agent24OnboardKey struct {
	Token     string
	KeyID     uint64
	ExpiresAt *time.Time
}

func agent24OnboardKeyName(owner string) string {
	name := agent24OnboardKeyPrefix + owner
	if len(name) <= agent24OnboardKeyNameMax {
		return name
	}
	n := agent24OnboardKeyNameMax
	for n > 0 && !utf8.ValidString(name[:n]) {
		n--
	}
	return name[:n]
}

// IssueAgent24OnboardKey creates a retrieve+ingest key for kbID and revokes an earlier key of the same name.
func IssueAgent24OnboardKey(
	ctx context.Context, keys interfaces.TenantAPIKeyService, tenantID uint64, owner, kbID string,
	ttl time.Duration, now time.Time,
) (Agent24OnboardKey, error) {
	var expiresAt *time.Time
	if ttl > 0 {
		exp := now.Add(ttl)
		expiresAt = &exp
	}
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
		if k == nil || k.ID == created.APIKey.ID || k.Name != name || k.RevokedAt != nil {
			continue
		}
		if err := keys.RevokeAPIKey(ctx, tenantID, k.ID); err != nil {
			// The new key stays valid; the caller fails the request.
			return Agent24OnboardKey{}, err
		}
	}
	return Agent24OnboardKey{
		Token: created.Token, KeyID: created.APIKey.ID, ExpiresAt: created.APIKey.ExpiresAt,
	}, nil
}
