package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/golang-jwt/jwt/v5"
)

func signedExternalUserToken(t *testing.T, secret string, claims jwt.MapClaims) string {
	t.Helper()

	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign token: %v", err)
	}
	return token
}

func TestResolveAPIPrincipalDefaultsToTenant(t *testing.T) {
	p, _, err := resolveAPIPrincipal(context.Background(), &types.Tenant{ID: 7}, http.Header{})
	if err != nil {
		t.Fatalf("resolveAPIPrincipal error = %v", err)
	}

	if p.Type != types.PrincipalAPITenant || p.ID != "7" {
		t.Fatalf("principal = %#v", p)
	}
}

func TestResolveAPIPrincipalDirectHeader(t *testing.T) {
	header := http.Header{}
	header.Set("X-External-User-ID", "external-u1")

	p, _, err := resolveAPIPrincipal(context.Background(), &types.Tenant{
		ID: 7,
		APIPrincipalConfig: &types.APIPrincipalConfig{
			Mode: types.APIPrincipalModeDirect,
		},
	}, header)
	if err != nil {
		t.Fatalf("resolveAPIPrincipal error = %v", err)
	}

	if p.Type != types.PrincipalAPIExternalUser || p.ID != "7:external-u1" {
		t.Fatalf("principal = %#v", p)
	}
}

func TestResolveAPIPrincipalSignedToken(t *testing.T) {
	secret := "test-secret"
	header := http.Header{}
	header.Set("X-External-User-Token", signedExternalUserToken(t, secret, jwt.MapClaims{
		"sub":       "external-u1",
		"tenant_id": float64(7),
		"aud":       "weknora",
		"exp":       time.Now().Add(time.Minute).Unix(),
	}))

	p, _, err := resolveAPIPrincipal(context.Background(), &types.Tenant{
		ID: 7,
		APIPrincipalConfig: &types.APIPrincipalConfig{
			Mode:       types.APIPrincipalModeSignedToken,
			HMACSecret: secret,
		},
	}, header)
	if err != nil {
		t.Fatalf("resolveAPIPrincipal error = %v", err)
	}

	if p.Type != types.PrincipalAPIExternalUser || p.ID != "7:external-u1" {
		t.Fatalf("principal = %#v", p)
	}
}

func TestResolveAPIPrincipalSignedTokenRejectsWrongTenant(t *testing.T) {
	secret := "test-secret"
	header := http.Header{}
	header.Set("X-External-User-Token", signedExternalUserToken(t, secret, jwt.MapClaims{
		"sub":       "external-u1",
		"tenant_id": float64(8),
		"aud":       "weknora",
		"exp":       time.Now().Add(time.Minute).Unix(),
	}))

	p, _, err := resolveAPIPrincipal(context.Background(), &types.Tenant{
		ID: 7,
		APIPrincipalConfig: &types.APIPrincipalConfig{
			Mode:       types.APIPrincipalModeSignedToken,
			HMACSecret: secret,
		},
	}, header)
	if err == nil {
		t.Fatalf("resolveAPIPrincipal error = nil, want error")
	}
	_ = p
}

func TestResolveAPIPrincipalSignedTokenRejectsExpired(t *testing.T) {
	secret := "test-secret"
	header := http.Header{}
	header.Set("X-External-User-Token", signedExternalUserToken(t, secret, jwt.MapClaims{
		"sub":       "external-u1",
		"tenant_id": float64(7),
		"aud":       "weknora",
		"exp":       time.Now().Add(-time.Minute).Unix(),
	}))

	p, _, err := resolveAPIPrincipal(context.Background(), &types.Tenant{
		ID: 7,
		APIPrincipalConfig: &types.APIPrincipalConfig{
			Mode:       types.APIPrincipalModeSignedToken,
			HMACSecret: secret,
		},
	}, header)
	if err == nil {
		t.Fatalf("resolveAPIPrincipal error = nil, want error")
	}
	_ = p
}

func TestResolveAPIPrincipalDirectHeaderRequired(t *testing.T) {
	_, _, err := resolveAPIPrincipal(context.Background(), &types.Tenant{
		ID: 7,
		APIPrincipalConfig: &types.APIPrincipalConfig{
			Mode:                types.APIPrincipalModeDirect,
			RequireDirectHeader: true,
		},
	}, http.Header{})
	if !errors.Is(err, errMissingDirectHeader) {
		t.Fatalf("resolveAPIPrincipal error = %v, want errMissingDirectHeader", err)
	}
}

func TestResolveAPIPrincipalDirectHeaderRejectsInvalidUserID(t *testing.T) {
	header := http.Header{}
	header.Set("X-External-User-ID", strings.Repeat("a", maxExternalUserIDLen+1))

	_, _, err := resolveAPIPrincipal(context.Background(), &types.Tenant{
		ID: 7,
		APIPrincipalConfig: &types.APIPrincipalConfig{
			Mode: types.APIPrincipalModeDirect,
		},
	}, header)
	if !errors.Is(err, errInvalidExternalUserID) {
		t.Fatalf("resolveAPIPrincipal error = %v, want errInvalidExternalUserID", err)
	}
}

func TestResolveAPIPrincipalSignedTokenRejectsLongLifetime(t *testing.T) {
	secret := "test-secret"
	header := http.Header{}
	header.Set("X-External-User-Token", signedExternalUserToken(t, secret, jwt.MapClaims{
		"sub":       "external-u1",
		"tenant_id": float64(7),
		"aud":       "weknora",
		"exp":       time.Now().Add(48 * time.Hour).Unix(),
	}))

	_, _, err := resolveAPIPrincipal(context.Background(), &types.Tenant{
		ID: 7,
		APIPrincipalConfig: &types.APIPrincipalConfig{
			Mode:       types.APIPrincipalModeSignedToken,
			HMACSecret: secret,
		},
	}, header)
	if err == nil {
		t.Fatalf("resolveAPIPrincipal error = nil, want error")
	}
}

// verifyExternalUserJWT's errors are fixed strings with numbers, so none can
// carry a sentinel. The test pins the exact error text instead: the rejection
// log keeps error_len and error_type and drops the text.
func TestResolveAPIPrincipalSignedTokenRejectionLogOmitsErrorText(t *testing.T) {
	buf := captureAuthLogs(t)
	secret := "test-secret"
	header := http.Header{}
	header.Set("X-External-User-Token", signedExternalUserToken(t, secret, jwt.MapClaims{
		"sub":       "external-u1",
		"tenant_id": float64(8),
		"aud":       "weknora",
		"exp":       time.Now().Add(time.Minute).Unix(),
	}))

	_, _, err := resolveAPIPrincipal(context.Background(), &types.Tenant{
		ID: 7,
		APIPrincipalConfig: &types.APIPrincipalConfig{
			Mode:       types.APIPrincipalModeSignedToken,
			HMACSecret: secret,
		},
	}, header)
	if err == nil {
		t.Fatal("resolveAPIPrincipal error = nil, want workspace mismatch")
	}
	logs := buf.String()
	if strings.Contains(logs, "workspace mismatch") {
		t.Fatalf("log contains the token error text:\n%s", logs)
	}
	if !strings.Contains(logs, "invalid external user token for tenant=7: error_len=") {
		t.Fatalf("log missing the rejection line:\n%s", logs)
	}
}

func TestResolveAPIPrincipalSignedTokenJTI(t *testing.T) {
	secret := "test-secret"
	tenant := &types.Tenant{
		ID: 7,
		APIPrincipalConfig: &types.APIPrincipalConfig{
			Mode:       types.APIPrincipalModeSignedToken,
			HMACSecret: secret,
		},
	}
	base := func() jwt.MapClaims {
		return jwt.MapClaims{
			"sub":       "external-u1",
			"tenant_id": float64(7),
			"aud":       "weknora",
			"exp":       time.Now().Add(time.Minute).Unix(),
		}
	}
	cases := []struct {
		name string
		set  func(jwt.MapClaims)
		want string
	}{
		{name: "present", set: func(c jwt.MapClaims) { c["jti"] = "token-jti-1" }, want: "token-jti-1"},
		{name: "absent"},
		{name: "not a string", set: func(c jwt.MapClaims) { c["jti"] = float64(12) }},
		{name: "too long", set: func(c jwt.MapClaims) { c["jti"] = strings.Repeat("j", 129) }},
		{
			name: "max runes", set: func(c jwt.MapClaims) { c["jti"] = strings.Repeat("j", 128) },
			want: strings.Repeat("j", 128),
		},
		{name: "blank", set: func(c jwt.MapClaims) { c["jti"] = "   " }},
		{name: "trimmed", set: func(c jwt.MapClaims) { c["jti"] = "  kept  " }, want: "kept"},
		{name: "control", set: func(c jwt.MapClaims) { c["jti"] = "bad\njti" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claims := base()
			if tc.set != nil {
				tc.set(claims)
			}
			header := http.Header{}
			header.Set("X-External-User-Token", signedExternalUserToken(t, secret, claims))
			p, jti, err := resolveAPIPrincipal(context.Background(), tenant, header)
			if err != nil {
				t.Fatalf("resolveAPIPrincipal error = %v", err)
			}
			if jti != tc.want {
				t.Fatalf("jti = %q, want %q", jti, tc.want)
			}
			if p.Type != types.PrincipalAPIExternalUser || p.ID != "7:external-u1" {
				t.Fatalf("principal = %#v", p)
			}
		})
	}
}

func TestResolveAPIPrincipalSignedTokenExpiredKeepsPrincipalAndJTI(t *testing.T) {
	secret := "test-secret"
	header := http.Header{}
	header.Set("X-External-User-Token", signedExternalUserToken(t, secret, jwt.MapClaims{
		"sub":       "external-u1",
		"tenant_id": float64(7),
		"aud":       "weknora",
		"exp":       time.Now().Add(-time.Minute).Unix(),
		"jti":       "expired-jti",
	}))

	p, jti, err := resolveAPIPrincipal(context.Background(), &types.Tenant{
		ID: 7,
		APIPrincipalConfig: &types.APIPrincipalConfig{
			Mode:       types.APIPrincipalModeSignedToken,
			HMACSecret: secret,
		},
	}, header)
	if !errors.Is(err, errExternalUserTokenExpired) || !errors.Is(err, errInvalidExternalUserToken) {
		t.Fatalf("resolveAPIPrincipal error = %v, want expired and invalid token", err)
	}
	if jti != "expired-jti" {
		t.Fatalf("jti = %q, want expired-jti", jti)
	}
	if p.Type != types.PrincipalAPIExternalUser || p.ID != "7:external-u1" {
		t.Fatalf("principal = %#v", p)
	}
}

func TestResolveAPIPrincipalSignedTokenExpiredBadSignature(t *testing.T) {
	header := http.Header{}
	header.Set("X-External-User-Token", signedExternalUserToken(t, "other-secret", jwt.MapClaims{
		"sub":       "external-u1",
		"tenant_id": float64(7),
		"aud":       "weknora",
		"exp":       time.Now().Add(-time.Minute).Unix(),
		"jti":       "expired-jti",
	}))

	_, _, err := resolveAPIPrincipal(context.Background(), &types.Tenant{
		ID: 7,
		APIPrincipalConfig: &types.APIPrincipalConfig{
			Mode:       types.APIPrincipalModeSignedToken,
			HMACSecret: "test-secret",
		},
	}, header)
	if err == nil {
		t.Fatal("resolveAPIPrincipal error = nil, want error")
	}
	if errors.Is(err, errExternalUserTokenExpired) {
		t.Fatalf("resolveAPIPrincipal error = %v, want it not to be expired", err)
	}
}

func TestResolveAPIPrincipalSignedTokenExpiredWrongTenant(t *testing.T) {
	secret := "test-secret"
	header := http.Header{}
	header.Set("X-External-User-Token", signedExternalUserToken(t, secret, jwt.MapClaims{
		"sub":       "external-u1",
		"tenant_id": float64(8),
		"aud":       "weknora",
		"exp":       time.Now().Add(-time.Minute).Unix(),
		"jti":       "expired-jti",
	}))

	_, _, err := resolveAPIPrincipal(context.Background(), &types.Tenant{
		ID: 7,
		APIPrincipalConfig: &types.APIPrincipalConfig{
			Mode:       types.APIPrincipalModeSignedToken,
			HMACSecret: secret,
		},
	}, header)
	if err == nil {
		t.Fatal("resolveAPIPrincipal error = nil, want error")
	}
	if errors.Is(err, errExternalUserTokenExpired) {
		t.Fatalf("resolveAPIPrincipal error = %v, want it not to be expired", err)
	}
}

// Expiry is the only claim allowed to fail for credential_expired: an expired token for another audience, or one
// not yet valid, stays invalid.
func TestResolveAPIPrincipalSignedTokenExpiredOtherClaimFails(t *testing.T) {
	secret := "test-secret"
	for name, claims := range map[string]jwt.MapClaims{
		"audience": {"aud": "elsewhere"},
		"nbf":      {"aud": "weknora", "nbf": time.Now().Add(time.Hour).Unix()},
	} {
		claims["sub"] = "external-u1"
		claims["tenant_id"] = float64(7)
		claims["exp"] = time.Now().Add(-time.Minute).Unix()
		claims["jti"] = "expired-jti"
		header := http.Header{}
		header.Set("X-External-User-Token", signedExternalUserToken(t, secret, claims))
		_, _, err := resolveAPIPrincipal(context.Background(), &types.Tenant{
			ID: 7,
			APIPrincipalConfig: &types.APIPrincipalConfig{
				Mode:       types.APIPrincipalModeSignedToken,
				HMACSecret: secret,
			},
		}, header)
		if err == nil || errors.Is(err, errExternalUserTokenExpired) {
			t.Fatalf("%s: err = %v, want an invalid token that is not credential_expired", name, err)
		}
	}
}

// An expired token with no sub is invalid, not credential_expired: there is no principal to record.
func TestResolveAPIPrincipalSignedTokenExpiredWithoutSubject(t *testing.T) {
	secret := "test-secret"
	header := http.Header{}
	header.Set("X-External-User-Token", signedExternalUserToken(t, secret, jwt.MapClaims{
		"sub":       "  ",
		"tenant_id": float64(7),
		"aud":       "weknora",
		"exp":       time.Now().Add(-time.Minute).Unix(),
		"jti":       "expired-jti",
	}))
	_, _, err := resolveAPIPrincipal(context.Background(), &types.Tenant{
		ID: 7,
		APIPrincipalConfig: &types.APIPrincipalConfig{
			Mode:       types.APIPrincipalModeSignedToken,
			HMACSecret: secret,
		},
	}, header)
	if err == nil || errors.Is(err, errExternalUserTokenExpired) {
		t.Fatalf("err = %v, want an invalid token that is not credential_expired", err)
	}
}

// verifyExternalUserJWT itself keeps an expired token with an empty sub on its original error.
func TestVerifyExternalUserJWTExpiredWithoutSubject(t *testing.T) {
	secret := "test-secret"
	token := signedExternalUserToken(t, secret, jwt.MapClaims{
		"sub": " ", "tenant_id": float64(7), "aud": "weknora", "exp": time.Now().Add(-time.Minute).Unix(),
	})
	_, err := verifyExternalUserJWT(token, 7, secret)
	if err == nil || errors.Is(err, errExternalUserTokenExpired) || !errors.Is(err, jwt.ErrTokenExpired) {
		t.Fatalf("err = %v, want the parser's expiry error, not errExternalUserTokenExpired", err)
	}
}

// An expired token whose sub fails validateExternalUserID keeps the upstream "invalid external user token"
// answer: it is neither credential_expired nor an invalid external user id.
func TestResolveAPIPrincipalSignedTokenExpiredInvalidSubject(t *testing.T) {
	secret := "test-secret"
	for name, sub := range map[string]string{
		"control character": "a\nb",
		"too long":          strings.Repeat("u", maxExternalUserIDLen+1),
	} {
		header := http.Header{}
		header.Set("X-External-User-Token", signedExternalUserToken(t, secret, jwt.MapClaims{
			"sub": sub, "tenant_id": float64(7), "aud": "weknora",
			"exp": time.Now().Add(-time.Minute).Unix(), "jti": "expired-jti",
		}))
		_, _, err := resolveAPIPrincipal(context.Background(), &types.Tenant{
			ID: 7,
			APIPrincipalConfig: &types.APIPrincipalConfig{
				Mode:       types.APIPrincipalModeSignedToken,
				HMACSecret: secret,
			},
		}, header)
		if err == nil || errors.Is(err, errExternalUserTokenExpired) || errors.Is(err, errInvalidExternalUserID) {
			t.Fatalf("%s: err = %v, want a plain invalid external user token", name, err)
		}
		if got := apiPrincipalAuthErrorMessage(err); got != "Unauthorized: invalid external user token" {
			t.Fatalf("%s: message = %q", name, got)
		}
	}
}
