// Agent24 fork product patch A24-KB-32 (KB-C2-01b1, WeKnora#133, CONTRACT-UI-BRIDGE-v1 U2.1).

package middleware

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
)

// The recheck reports the credential's expiry as it is now: the earlier of the key's expires_at and the
// signed token's exp, nil when there is neither.
func TestAgent24RecheckAPIKeyExpiry(t *testing.T) {
	const secret = "test-secret"
	tenantID := uint64(7)
	now := time.Now().Truncate(time.Second)
	keyExp := now.Add(10 * time.Minute)
	key := func(exp *time.Time) *types.TenantAPIKey {
		return &types.TenantAPIKey{
			ID: 4, ScopeType: types.APIKeyScopeTenant, TenantID: &tenantID, ExpiresAt: exp,
			KnowledgeBaseIDs: types.StringArray{"kb-1"}, Capabilities: types.StringArray{"retrieve"},
		}
	}
	tenantEntry := Agent24RecheckEntry{
		KeyID: 4, TenantID: tenantID, Principal: types.Principal{Type: types.PrincipalAPITenant, ID: "7"},
	}
	plainHeader := http.Header{}
	plainHeader.Set("X-API-Key", "sk-live")

	res, err := Agent24RecheckAPIKey(context.Background(), agent24RecheckTenants{tenant: &types.Tenant{ID: tenantID}},
		agent24RecheckKeys{key: key(&keyExp)}, nil, plainHeader, tenantEntry)
	require.NoError(t, err)
	require.Empty(t, res.Reason)
	require.NotNil(t, res.ExpiresAt)
	require.True(t, res.ExpiresAt.Equal(keyExp))

	res, err = Agent24RecheckAPIKey(context.Background(), agent24RecheckTenants{tenant: &types.Tenant{ID: tenantID}},
		agent24RecheckKeys{key: key(nil)}, nil, plainHeader, tenantEntry)
	require.NoError(t, err)
	require.Nil(t, res.ExpiresAt)

	tokenExp := now.Add(time.Minute)
	token := signedExternalUserToken(t, secret, jwt.MapClaims{
		"sub": "external-u1", "tenant_id": float64(tenantID), "aud": "weknora", "exp": tokenExp.Unix(), "jti": "jti-1",
	})
	signedHeader := http.Header{}
	signedHeader.Set("X-API-Key", "sk-live")
	signedHeader.Set("X-External-User-Token", token)
	signedTenant := &types.Tenant{ID: tenantID, APIPrincipalConfig: &types.APIPrincipalConfig{
		Mode: types.APIPrincipalModeSignedToken, HMACSecret: secret,
	}}
	signedEntry := Agent24RecheckEntry{
		KeyID: 4, TenantID: tenantID, Mode: types.APIPrincipalModeSignedToken, JTI: "jti-1",
		Principal: types.Principal{Type: types.PrincipalAPIExternalUser, ID: "7:external-u1"},
	}
	res, err = Agent24RecheckAPIKey(context.Background(), agent24RecheckTenants{tenant: signedTenant},
		agent24RecheckKeys{key: key(&keyExp)}, nil, signedHeader, signedEntry)
	require.NoError(t, err)
	require.Empty(t, res.Reason)
	require.NotNil(t, res.ExpiresAt)
	require.True(t, res.ExpiresAt.Equal(tokenExp), "token exp is earlier than the key's: %s", res.ExpiresAt)
}
