// Agent24 fork product patch A24-KB-32 (KB-C2-01b1, WeKnora#133, CONTRACT-UI-BRIDGE-v1 U2.1, U2.13, U3.7).

package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// agent24ExpiryUserStub makes the tenant API-key path use the synthetic user.
type agent24ExpiryUserStub struct{ interfaces.UserService }

func (agent24ExpiryUserStub) GetUserByTenantID(context.Context, uint64) (*types.User, error) {
	return nil, errors.New("none")
}

func agent24AttachExpiry(
	t *testing.T, tenant *types.Tenant, key *types.TenantAPIKey, header http.Header,
) *gin.Context {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/agent24/knowledge/ui/tickets", nil)
	if header != nil {
		c.Request.Header = header
	}
	tenantID := uint64(7)
	if tenant != nil && tenant.ID != 0 {
		tenantID = tenant.ID
	}
	attachAPIKeyAuthContext(c, &fakeTenantService{tenant: tenant}, agent24ExpiryUserStub{}, tenantID, key)
	if c.IsAborted() {
		t.Fatalf("auth aborted: %d %s", w.Code, w.Body.String())
	}
	return c
}

func TestVerifyExternalUserJWTReadsExp(t *testing.T) {
	secret := "test-secret"
	expUnix := time.Now().Add(time.Hour).Unix()
	tok := signedExternalUserToken(t, secret, jwt.MapClaims{
		"sub": "external-u1", "tenant_id": float64(7), "aud": "weknora", "exp": expUnix, "jti": "j1",
	})
	got, err := verifyExternalUserJWT(tok, 7, secret)
	if err != nil {
		t.Fatalf("verifyExternalUserJWT error = %v", err)
	}
	if !got.ExpiresAt.Equal(time.Unix(expUnix, 0)) {
		t.Fatalf("ExpiresAt = %v, want %v", got.ExpiresAt, time.Unix(expUnix, 0))
	}

	past := time.Now().Add(-time.Minute).Unix()
	tok = signedExternalUserToken(t, secret, jwt.MapClaims{
		"sub": "external-u1", "tenant_id": float64(7), "aud": "weknora", "exp": past, "jti": "j2",
	})
	got, err = verifyExternalUserJWT(tok, 7, secret)
	if !errors.Is(err, errExternalUserTokenExpired) {
		t.Fatalf("verifyExternalUserJWT error = %v, want expired", err)
	}
	if !got.ExpiresAt.Equal(time.Unix(past, 0)) || got.JTI != "j2" {
		t.Fatalf("expired token = %#v", got)
	}

	header := http.Header{}
	header.Set("X-External-User-Token", tok)
	_, _, info, err := resolveAPIPrincipal(context.Background(), &types.Tenant{
		ID: 7,
		APIPrincipalConfig: &types.APIPrincipalConfig{
			Mode: types.APIPrincipalModeSignedToken, HMACSecret: secret,
		},
	}, header)
	if !errors.Is(err, errExternalUserTokenExpired) {
		t.Fatalf("resolveAPIPrincipal error = %v, want expired", err)
	}
	if info.JTI != "j2" || !info.ExpiresAt.Equal(time.Unix(past, 0)) {
		t.Fatalf("tokenInfo = %#v", info)
	}
}

func TestAgent24CredentialExpiryFromContext(t *testing.T) {
	tenantID := uint64(7)
	secret := "test-secret"
	signed := func(expUnix int64) http.Header {
		h := http.Header{}
		h.Set("X-External-User-Token", signedExternalUserToken(t, secret, jwt.MapClaims{
			"sub": "external-u1", "tenant_id": float64(tenantID), "aud": "weknora", "exp": expUnix, "jti": "jti-exp",
		}))
		return h
	}
	tenantMode := &types.Tenant{ID: tenantID}
	signedMode := &types.Tenant{ID: tenantID, APIPrincipalConfig: &types.APIPrincipalConfig{
		Mode: types.APIPrincipalModeSignedToken, HMACSecret: secret,
	}}
	keyExp := time.Now().Add(5 * time.Hour).Truncate(time.Second)
	tokenExp := time.Now().Add(2 * time.Hour).Unix()
	earlierKey := time.Now().Add(time.Hour).Truncate(time.Second)
	laterToken := time.Now().Add(3 * time.Hour).Unix()

	key := &types.TenantAPIKey{ID: 4, ScopeType: types.APIKeyScopeTenant, TenantID: &tenantID}
	cases := []struct {
		name      string
		tenant    *types.Tenant
		keyExp    *time.Time
		header    http.Header
		want      time.Time
		wantUnset bool
	}{
		{name: "api key expiry", tenant: tenantMode, keyExp: &keyExp, want: keyExp},
		{
			name: "signed token expiry", tenant: signedMode, header: signed(tokenExp),
			want: time.Unix(tokenExp, 0),
		},
		{
			name: "both, token earlier", tenant: signedMode, keyExp: &keyExp, header: signed(tokenExp),
			want: time.Unix(tokenExp, 0),
		},
		{
			name: "both, key earlier", tenant: signedMode, keyExp: &earlierKey, header: signed(laterToken),
			want: earlierKey,
		},
		{name: "neither", tenant: tenantMode, wantUnset: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			k := *key
			k.ExpiresAt = tc.keyExp
			c := agent24AttachExpiry(t, tc.tenant, &k, tc.header)
			got, ok := types.Agent24CredentialExpiryFromContext(c.Request.Context())
			if tc.wantUnset {
				if ok {
					t.Fatalf("expiry = %v, want unset", got)
				}
				return
			}
			if !ok || !got.Equal(tc.want) {
				t.Fatalf("expiry = %v ok=%v, want %v", got, ok, tc.want)
			}
		})
	}
}
