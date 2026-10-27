// Agent24 fork product patch A24-KB-23 (KB-C1-03 C3-d, WeKnora#41, CONTRACT-READONLY-v1 R7.6).

package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// Agent24RecheckEntry is what Auth established at entry for an X-API-Key request: the key, its tenant,
// that tenant's principal mode, the bound principal and the signed_token jti ("" for none).
type Agent24RecheckEntry struct {
	KeyID     uint64
	TenantID  uint64
	Mode      types.APIPrincipalMode
	Principal types.Principal
	JTI       string
}

// Agent24RecheckResult is the outcome of Agent24RecheckAPIKey. Reason is "" when the credential still
// stands, otherwise credential_expired, credential_revoked or credential_invalid. Scope is the key's
// scope as it is now; it is set only when Reason is "".
type Agent24RecheckResult struct {
	Reason string
	Scope  types.TenantAPIKeyScope
}

// Agent24RecheckAPIKey runs again, before a Knowledge response is assembled (R7.6), the credential
// checks Auth made for an X-API-Key request: the key by its hash (revoked, expired), the tenant row as it
// is now, and the principal resolved from that row (for signed_token: the signature under the current
// HMAC secret, exp, nbf, TTL, tenant_id and sub), then the jti revocation list when revocations is
// non-nil. It writes no response and does not touch the request context.
//
// The entry identity is the baseline: a different key, tenant, principal mode, principal or jti is
// credential_invalid, never a re-resolved identity. A tenant switched from signed_token to tenant mode
// would otherwise answer the in-flight request as the tenant principal, with no jti left to check. A
// changed mode is not a revocation (R1.5); the next request is judged afresh.
//
// The returned error is an infrastructure failure (the key, tenant or revocation lookup could not be
// answered); the caller refuses the request as internal. A key or tenant that no longer exists is
// credential_invalid.
func Agent24RecheckAPIKey(
	ctx context.Context,
	tenants interfaces.TenantService,
	keys interfaces.TenantAPIKeyService,
	revocations interfaces.Agent24CredentialRevocationStore,
	header http.Header,
	entry Agent24RecheckEntry,
) (Agent24RecheckResult, error) {
	invalid := Agent24RecheckResult{Reason: "credential_invalid"}
	raw := strings.TrimSpace(header.Get("X-API-Key"))
	if raw == "" {
		return invalid, nil
	}
	key, err := keys.AuthenticateAPIKey(ctx, raw)
	var unusable *apprepo.APIKeyUnusableError
	switch {
	case errors.As(err, &unusable):
		if unusable.Key == nil || unusable.Key.ID != entry.KeyID {
			return invalid, nil
		}
		if unusable.Revoked {
			return Agent24RecheckResult{Reason: "credential_revoked"}, nil
		}
		return Agent24RecheckResult{Reason: "credential_expired"}, nil
	case errors.Is(err, apprepo.ErrTenantAPIKeyNotFound):
		return invalid, nil
	case err != nil:
		return Agent24RecheckResult{}, err
	}
	if key == nil || key.ID != entry.KeyID || key.IsPlatform() || key.TenantIDValue() != entry.TenantID {
		return invalid, nil
	}
	tenant, err := tenants.GetTenantByID(ctx, entry.TenantID)
	if errors.Is(err, apprepo.ErrTenantNotFound) {
		return invalid, nil
	}
	if err != nil {
		return Agent24RecheckResult{}, err
	}
	if tenant == nil || Agent24PrincipalMode(tenant) != entry.Mode {
		return invalid, nil
	}
	principal, jti, err := resolveAPIPrincipal(ctx, tenant, header)
	same := principal.Normalize() == entry.Principal.Normalize() && jti == entry.JTI
	if errors.Is(err, errExternalUserTokenExpired) {
		if !same {
			return invalid, nil
		}
		return Agent24RecheckResult{Reason: "credential_expired"}, nil
	}
	if err != nil || !same {
		return invalid, nil
	}
	if jti != "" && revocations != nil {
		revoked, err := revocations.IsRevoked(ctx, entry.TenantID, jti, time.Now())
		if err != nil {
			return Agent24RecheckResult{}, err
		}
		if revoked {
			return Agent24RecheckResult{Reason: "credential_revoked"}, nil
		}
	}
	return Agent24RecheckResult{Scope: types.TenantAPIKeyScope{
		KeyID:            key.ID,
		Name:             key.Name,
		ScopeType:        key.ScopeType,
		FullAccess:       key.FullAccess,
		KnowledgeBaseIDs: key.KnowledgeBaseIDs,
		Capabilities:     key.Capabilities,
	}}, nil
}

// Agent24PrincipalMode is the tenant's principal mode as resolveAPIPrincipal reads it: "" for no config.
// Callers record it at entry for Agent24RecheckEntry.Mode.
func Agent24PrincipalMode(t *types.Tenant) types.APIPrincipalMode {
	if t.APIPrincipalConfig == nil {
		return ""
	}
	return t.APIPrincipalConfig.Mode
}
