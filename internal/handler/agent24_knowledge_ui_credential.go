// Agent24 fork product patch A24-KB-33 (KB-C2-01b2, WeKnora#133, CONTRACT-UI-BRIDGE-v1 U2.6–U2.8, U3.1–U3.4).

package handler

import (
	"context"
	"errors"
	"strings"
	"time"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// agent24UIStoredDeps are the services that recheck a credential from what a ticket recorded.
// The request itself does not carry C.
type agent24UIStoredDeps struct {
	tenants     interfaces.TenantService
	keys        interfaces.TenantAPIKeyService
	revocations interfaces.Agent24CredentialRevocationStore
}

// errAgent24UIStoredUnwired means a service the stored-credential recheck needs is missing.
// The caller answers internal (fail closed).
var errAgent24UIStoredUnwired = errors.New("agent24 ui stored credential recheck is not wired")

// agent24UIRecheckStored reports whether the credential a ticket recorded is still valid.
// An empty reason means it is. err is only an infrastructure failure; the caller answers internal.
//
// A signed_token cannot be verified again here: the request does not carry it. The expiry recorded at
// issuance and the jti revocation list stand in for that check. This is UIB U2.6 "C valid" on a request
// that has no C.
func agent24UIRecheckStored(
	ctx context.Context, deps agent24UIStoredDeps, t *types.Agent24UITicket, now time.Time,
) (types.TenantAPIKeyScope, string, error) {
	scope, reason, _, err := agent24UIRecheckStoredKey(ctx, deps, t, now)
	return scope, reason, err
}

// agent24UIRecheckStoredKey is agent24UIRecheckStored plus the key's current expiry.
// The exchange uses that expiry to shorten the session JWT. The decision order is the task's.
func agent24UIRecheckStoredKey(
	ctx context.Context, deps agent24UIStoredDeps, t *types.Agent24UITicket, now time.Time,
) (types.TenantAPIKeyScope, string, *time.Time, error) {
	if t == nil {
		return types.TenantAPIKeyScope{}, "", nil, errAgent24UIStoredUnwired
	}
	if t.CredentialExpiresAt != nil && !now.Before(*t.CredentialExpiresAt) {
		return types.TenantAPIKeyScope{}, "credential_expired", nil, nil
	}
	if deps.keys == nil {
		return types.TenantAPIKeyScope{}, "", nil, errAgent24UIStoredUnwired
	}
	listed, err := deps.keys.ListAPIKeys(ctx, t.TenantID)
	if err != nil {
		return types.TenantAPIKeyScope{}, "", nil, err
	}
	var key *types.TenantAPIKey
	for _, item := range listed {
		if item != nil && item.ID == t.KeyID {
			key = item
			break
		}
	}
	if key == nil {
		return types.TenantAPIKeyScope{}, "credential_revoked", nil, nil
	}
	if key.ExpiresAt != nil && !now.Before(*key.ExpiresAt) {
		return types.TenantAPIKeyScope{}, "credential_expired", nil, nil
	}
	if key.IsPlatform() || key.TenantIDValue() != t.TenantID {
		return types.TenantAPIKeyScope{}, "credential_invalid", nil, nil
	}
	if deps.tenants == nil {
		return types.TenantAPIKeyScope{}, "", nil, errAgent24UIStoredUnwired
	}
	tenant, err := deps.tenants.GetTenantByID(ctx, t.TenantID)
	if err != nil {
		if errors.Is(err, apprepo.ErrTenantNotFound) {
			return types.TenantAPIKeyScope{}, "credential_invalid", nil, nil
		}
		return types.TenantAPIKeyScope{}, "", nil, err
	}
	if tenant == nil || string(middleware.Agent24PrincipalMode(tenant)) != t.PrincipalMode {
		return types.TenantAPIKeyScope{}, "credential_invalid", nil, nil
	}
	if strings.HasPrefix(t.CredentialID, "jti:") {
		if deps.revocations == nil {
			return types.TenantAPIKeyScope{}, "", nil, errAgent24UIStoredUnwired
		}
		revoked, rerr := deps.revocations.IsRevoked(ctx, t.TenantID, strings.TrimPrefix(t.CredentialID, "jti:"), now)
		if rerr != nil {
			return types.TenantAPIKeyScope{}, "", nil, rerr
		}
		if revoked {
			return types.TenantAPIKeyScope{}, "credential_revoked", nil, nil
		}
	}
	scope := types.TenantAPIKeyScope{
		KeyID:            key.ID,
		Name:             key.Name,
		ScopeType:        key.ScopeType,
		FullAccess:       key.FullAccess,
		KnowledgeBaseIDs: key.KnowledgeBaseIDs,
		Capabilities:     key.Capabilities,
	}
	if !scope.FullAccess && !scope.HasCapability(types.APIKeyCapabilityRetrieve) {
		return types.TenantAPIKeyScope{}, "credential_invalid", nil, nil
	}
	var keyExp *time.Time
	if key.ExpiresAt != nil {
		copied := *key.ExpiresAt
		keyExp = &copied
	}
	return scope, "", keyExp, nil
}
