// Agent24 fork product patch A24-KB-23 (KB-C1-03 C3-d, WeKnora#41, CONTRACT-READONLY-v1 R7.6).

package middleware

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

var (
	errAgent24RecheckKeyDown    = errors.New("key down")
	errAgent24RecheckTenantDown = errors.New("tenant down")
	errAgent24RecheckListDown   = errors.New("list down")
)

// agent24RecheckTenants embeds TenantService and answers only GetTenantByID.
type agent24RecheckTenants struct {
	interfaces.TenantService
	tenant *types.Tenant
	err    error
}

func (s agent24RecheckTenants) GetTenantByID(context.Context, uint64) (*types.Tenant, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.tenant, nil
}

// agent24RecheckKeys embeds TenantAPIKeyService and answers only AuthenticateAPIKey.
type agent24RecheckKeys struct {
	interfaces.TenantAPIKeyService
	key *types.TenantAPIKey
	err error
}

func (s agent24RecheckKeys) AuthenticateAPIKey(context.Context, string) (*types.TenantAPIKey, error) {
	if s.err != nil {
		return s.key, s.err
	}
	return s.key, nil
}

// agent24RecheckRevocations is a jti list. A nil store is not consulted.
type agent24RecheckRevocations struct {
	revoked bool
	err     error
	calls   int
	tenant  uint64
	jti     string
}

func (s *agent24RecheckRevocations) Revoke(context.Context, uint64, string, time.Time) error {
	return nil
}

func (s *agent24RecheckRevocations) IsRevoked(
	_ context.Context, tenantID uint64, jti string, _ time.Time,
) (bool, error) {
	s.calls++
	s.tenant, s.jti = tenantID, jti
	return s.revoked, s.err
}

func (s *agent24RecheckRevocations) PurgeExpired(context.Context, time.Time) (int64, error) {
	return 0, nil
}

func TestAgent24RecheckAPIKey(t *testing.T) {
	const secret = "test-secret"
	tenantID := uint64(7)
	passKey := &types.TenantAPIKey{
		ID: 4, Name: "rotated", ScopeType: types.APIKeyScopeTenant, TenantID: &tenantID,
		KnowledgeBaseIDs: types.StringArray{"kb-new", "kb-9"},
		Capabilities:     types.StringArray{"retrieve"},
	}
	passScope := types.TenantAPIKeyScope{
		KeyID: 4, Name: "rotated", ScopeType: types.APIKeyScopeTenant,
		KnowledgeBaseIDs: types.StringArray{"kb-new", "kb-9"},
		Capabilities:     types.StringArray{"retrieve"},
	}
	plainTenant := &types.Tenant{ID: tenantID}
	tenantEntry := Agent24RecheckEntry{
		KeyID: 4, TenantID: tenantID,
		Principal: types.Principal{Type: types.PrincipalAPITenant, ID: "7"},
	}
	signedTenant := &types.Tenant{ID: tenantID, APIPrincipalConfig: &types.APIPrincipalConfig{
		Mode: types.APIPrincipalModeSignedToken, HMACSecret: secret,
	}}
	signedEntry := Agent24RecheckEntry{
		KeyID: 4, TenantID: tenantID, Mode: types.APIPrincipalModeSignedToken, JTI: "jti-1",
		Principal: types.Principal{Type: types.PrincipalAPIExternalUser, ID: "7:external-u1"},
	}
	platformKey := &types.TenantAPIKey{
		ID: 5, ScopeType: types.APIKeyScopePlatform, Capabilities: types.StringArray{"retrieve"},
	}
	platformScope := types.TenantAPIKeyScope{
		KeyID: 5, ScopeType: types.APIKeyScopePlatform, Capabilities: types.StringArray{"retrieve"},
	}
	platformEntry := Agent24RecheckEntry{
		KeyID: 5, TenantID: tenantID,
		Principal: types.Principal{Type: types.PrincipalAPIPlatform, ID: "5"},
	}
	otherTenant := uint64(8)
	validAt := time.Now().Add(time.Minute)
	expiredAt := time.Now().Add(-time.Minute)
	claims := func(sub, jti string, exp time.Time) jwt.MapClaims {
		out := jwt.MapClaims{
			"sub": sub, "tenant_id": float64(tenantID), "aud": "weknora", "exp": exp.Unix(),
		}
		if jti != "" {
			out["jti"] = jti
		}
		return out
	}
	header := func(token string) http.Header {
		h := http.Header{}
		h.Set("X-API-Key", "sk-live")
		if token != "" {
			h.Set("X-External-User-Token", token)
		}
		return h
	}
	validToken := signedExternalUserToken(t, secret, claims("external-u1", "jti-1", validAt))
	revokedList := &agent24RecheckRevocations{revoked: true}
	errList := &agent24RecheckRevocations{err: errAgent24RecheckListDown}

	cases := []struct {
		name         string
		header       http.Header
		tenants      interfaces.TenantService
		keys         interfaces.TenantAPIKeyService
		revocations  interfaces.Agent24CredentialRevocationStore
		entry        Agent24RecheckEntry
		wantReason   string
		wantErr      error
		wantScope    types.TenantAPIKeyScope
		wantListCall *agent24RecheckRevocations
	}{
		{
			name:   "pass scope comes from the new key row",
			header: header(""), tenants: agent24RecheckTenants{tenant: plainTenant},
			keys: agent24RecheckKeys{key: passKey}, entry: tenantEntry, wantScope: passScope,
		},
		{
			name:   "missing X-API-Key",
			header: http.Header{}, tenants: agent24RecheckTenants{tenant: plainTenant},
			keys: agent24RecheckKeys{key: passKey}, entry: tenantEntry, wantReason: "credential_invalid",
		},
		{
			name:   "key revoked",
			header: header(""), tenants: agent24RecheckTenants{tenant: plainTenant},
			keys:  agent24RecheckKeys{err: &apprepo.APIKeyUnusableError{Key: passKey, Revoked: true}},
			entry: tenantEntry, wantReason: "credential_revoked",
		},
		{
			name:   "key expired",
			header: header(""), tenants: agent24RecheckTenants{tenant: plainTenant},
			keys:  agent24RecheckKeys{err: &apprepo.APIKeyUnusableError{Key: passKey, Revoked: false}},
			entry: tenantEntry, wantReason: "credential_expired",
		},
		{
			name:   "key not found",
			header: header(""), tenants: agent24RecheckTenants{tenant: plainTenant},
			keys:  agent24RecheckKeys{err: apprepo.ErrTenantAPIKeyNotFound},
			entry: tenantEntry, wantReason: "credential_invalid",
		},
		{
			name:   "authenticate returns another error",
			header: header(""), tenants: agent24RecheckTenants{tenant: plainTenant},
			keys:  agent24RecheckKeys{err: errAgent24RecheckKeyDown},
			entry: tenantEntry, wantErr: errAgent24RecheckKeyDown,
		},
		{
			name:   "key id differs",
			header: header(""), tenants: agent24RecheckTenants{tenant: plainTenant},
			keys: agent24RecheckKeys{key: &types.TenantAPIKey{
				ID: 99, ScopeType: types.APIKeyScopeTenant, TenantID: &tenantID,
			}},
			entry: tenantEntry, wantReason: "credential_invalid",
		},
		{
			name:   "tenant key became a platform key",
			header: header(""), tenants: agent24RecheckTenants{tenant: plainTenant},
			keys: agent24RecheckKeys{key: &types.TenantAPIKey{
				ID: 4, ScopeType: types.APIKeyScopePlatform, TenantID: &tenantID,
			}},
			entry: tenantEntry, wantReason: "credential_invalid",
		},
		{
			name:   "platform key passes",
			header: header(""), tenants: agent24RecheckTenants{tenant: plainTenant},
			keys: agent24RecheckKeys{key: platformKey}, entry: platformEntry, wantScope: platformScope,
		},
		{
			name:   "platform key ignores the tenant's principal mode",
			header: header(""), tenants: agent24RecheckTenants{tenant: signedTenant},
			keys: agent24RecheckKeys{key: platformKey}, entry: platformEntry, wantScope: platformScope,
		},
		{
			name:   "platform key principal differs",
			header: header(""), tenants: agent24RecheckTenants{tenant: plainTenant},
			keys: agent24RecheckKeys{key: platformKey},
			entry: Agent24RecheckEntry{
				KeyID: 5, TenantID: tenantID,
				Principal: types.Principal{Type: types.PrincipalAPIPlatform, ID: "6"},
			},
			wantReason: "credential_invalid",
		},
		{
			name:   "platform key became a tenant key",
			header: header(""), tenants: agent24RecheckTenants{tenant: plainTenant},
			keys: agent24RecheckKeys{key: &types.TenantAPIKey{
				ID: 5, ScopeType: types.APIKeyScopeTenant, TenantID: &tenantID,
			}},
			entry: platformEntry, wantReason: "credential_invalid",
		},
		{
			name:   "revoked key is another key",
			header: header(""), tenants: agent24RecheckTenants{tenant: plainTenant},
			keys: agent24RecheckKeys{err: &apprepo.APIKeyUnusableError{
				Key: &types.TenantAPIKey{ID: 9, ScopeType: types.APIKeyScopeTenant, TenantID: &tenantID}, Revoked: true,
			}},
			entry: tenantEntry, wantReason: "credential_invalid",
		},
		{
			// Without a direct header the direct mode falls back to the tenant principal, the entry's: only
			// the mode comparison refuses it, as the integration gate refuses direct mode on the next request.
			name:   "mode changed from tenant to direct",
			header: header(""),
			tenants: agent24RecheckTenants{tenant: &types.Tenant{
				ID: tenantID, APIPrincipalConfig: &types.APIPrincipalConfig{Mode: types.APIPrincipalModeDirect},
			}},
			keys: agent24RecheckKeys{key: passKey}, entry: tenantEntry, wantReason: "credential_invalid",
		},
		{
			// An entry without a bound principal or jti must not match the empty identity a failed
			// resolution returns.
			name:    "resolution fails against an empty entry identity",
			header:  header(signedExternalUserToken(t, "other-secret", claims("external-u1", "jti-1", validAt))),
			tenants: agent24RecheckTenants{tenant: signedTenant}, keys: agent24RecheckKeys{key: passKey},
			entry:      Agent24RecheckEntry{KeyID: 4, TenantID: tenantID, Mode: types.APIPrincipalModeSignedToken},
			wantReason: "credential_invalid",
		},
		{
			name:   "key tenant differs",
			header: header(""), tenants: agent24RecheckTenants{tenant: plainTenant},
			keys: agent24RecheckKeys{key: &types.TenantAPIKey{
				ID: 4, ScopeType: types.APIKeyScopeTenant, TenantID: &otherTenant,
			}},
			entry: tenantEntry, wantReason: "credential_invalid",
		},
		{
			name:   "tenant not found",
			header: header(""), tenants: agent24RecheckTenants{err: apprepo.ErrTenantNotFound},
			keys: agent24RecheckKeys{key: passKey}, entry: tenantEntry, wantReason: "credential_invalid",
		},
		{
			name:   "tenant lookup returns another error",
			header: header(""), tenants: agent24RecheckTenants{err: errAgent24RecheckTenantDown},
			keys: agent24RecheckKeys{key: passKey}, entry: tenantEntry, wantErr: errAgent24RecheckTenantDown,
		},
		{
			name:   "mode changed from signed_token to tenant",
			header: header(validToken),
			tenants: agent24RecheckTenants{tenant: &types.Tenant{
				ID: tenantID, APIPrincipalConfig: &types.APIPrincipalConfig{Mode: types.APIPrincipalModeTenant},
			}},
			keys: agent24RecheckKeys{key: passKey}, entry: signedEntry, wantReason: "credential_invalid",
		},
		{
			name:    "hmac rotated",
			header:  header(signedExternalUserToken(t, "other-secret", claims("external-u1", "jti-1", validAt))),
			tenants: agent24RecheckTenants{tenant: signedTenant}, keys: agent24RecheckKeys{key: passKey},
			entry: signedEntry, wantReason: "credential_invalid",
		},
		{
			name:    "token expired",
			header:  header(signedExternalUserToken(t, secret, claims("external-u1", "jti-1", expiredAt))),
			tenants: agent24RecheckTenants{tenant: signedTenant}, keys: agent24RecheckKeys{key: passKey},
			entry: signedEntry, wantReason: "credential_expired",
		},
		{
			name:    "token expired and sub differs",
			header:  header(signedExternalUserToken(t, secret, claims("other-user", "jti-1", expiredAt))),
			tenants: agent24RecheckTenants{tenant: signedTenant}, keys: agent24RecheckKeys{key: passKey},
			entry: signedEntry, wantReason: "credential_invalid",
		},
		{
			name:    "sub differs",
			header:  header(signedExternalUserToken(t, secret, claims("other-user", "jti-1", validAt))),
			tenants: agent24RecheckTenants{tenant: signedTenant}, keys: agent24RecheckKeys{key: passKey},
			entry: signedEntry, wantReason: "credential_invalid",
		},
		{
			name:    "jti differs",
			header:  header(signedExternalUserToken(t, secret, claims("external-u1", "jti-other", validAt))),
			tenants: agent24RecheckTenants{tenant: signedTenant}, keys: agent24RecheckKeys{key: passKey},
			entry: signedEntry, wantReason: "credential_invalid",
		},
		{
			name:   "jti revoked",
			header: header(validToken), tenants: agent24RecheckTenants{tenant: signedTenant},
			keys: agent24RecheckKeys{key: passKey}, revocations: revokedList, entry: signedEntry,
			wantReason: "credential_revoked", wantListCall: revokedList,
		},
		{
			name:   "revocation list errors",
			header: header(validToken), tenants: agent24RecheckTenants{tenant: signedTenant},
			keys: agent24RecheckKeys{key: passKey}, revocations: errList, entry: signedEntry,
			wantErr: errAgent24RecheckListDown, wantListCall: errList,
		},
		{
			name:   "nil revocations are not consulted",
			header: header(validToken), tenants: agent24RecheckTenants{tenant: signedTenant},
			keys: agent24RecheckKeys{key: passKey}, entry: signedEntry, wantScope: passScope,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Agent24RecheckAPIKey(
				context.Background(), tc.tenants, tc.keys, tc.revocations, tc.header, tc.entry,
			)
			if tc.wantListCall != nil {
				require.Equal(t, 1, tc.wantListCall.calls)
				require.Equal(t, tenantID, tc.wantListCall.tenant)
				require.Equal(t, "jti-1", tc.wantListCall.jti)
			}
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				require.Equal(t, Agent24RecheckResult{}, res)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantReason, res.Reason)
			if tc.wantReason == "" {
				require.Equal(t, tc.wantScope, res.Scope)
				return
			}
			require.Equal(t, types.TenantAPIKeyScope{}, res.Scope)
		})
	}
}
