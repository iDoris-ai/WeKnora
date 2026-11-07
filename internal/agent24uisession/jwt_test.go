// Agent24 fork product patch A24-KB-31 (KB-C2-01a, WeKnora#133, CONTRACT-UI-BRIDGE-v1 U2–U3).

package agent24uisession

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

const testIssuer = "weknora-agent24"

func genPtr(v int64) *int64 { return &v }

func testKey() []byte { return []byte("0123456789abcdef0123456789abcdef") }

func fullClaims() Claims {
	return Claims{
		RegisteredClaims:   jwt.RegisteredClaims{Subject: "principal-a"},
		TenantID:           7,
		Scope:              Scope{KnowledgeBaseIDs: []string{"kb-1", "kb-2"}},
		Ops:                []string{"search", "read"},
		TicketID:           "ticket-1",
		ParentCredentialID: "cred-1",
		BindingTag:         "bind",
		PartitionTag:       "part",
		Cnf:                Confirmation{VPH: "vph-1"},
		Gen:                genPtr(4),
	}
}

func issuedClaims(now time.Time, lifetime time.Duration) Claims {
	c := fullClaims()
	c.Issuer = testIssuer
	c.Audience = jwt.ClaimStrings{Audience}
	c.IssuedAt = jwt.NewNumericDate(now)
	c.NotBefore = jwt.NewNumericDate(now)
	c.ExpiresAt = jwt.NewNumericDate(now.Add(lifetime))
	c.ID = "jti-fixed"
	return c
}

func signMethod(t *testing.T, key []byte, method jwt.SigningMethod, c Claims) string {
	t.Helper()
	tok := jwt.NewWithClaims(method, c)
	signed, err := tok.SignedString(key)
	require.NoError(t, err)
	return signed
}

func claimsMap(t *testing.T, c Claims) map[string]any {
	t.Helper()
	raw, err := json.Marshal(c)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	return m
}

func signMap(t *testing.T, key []byte, m map[string]any) string {
	t.Helper()
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims(m))
	signed, err := tok.SignedString(key)
	require.NoError(t, err)
	return signed
}

func TestLifetime(t *testing.T) {
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	require.Equal(t, DefaultLifetime, Lifetime(now, nil))
	in5 := now.Add(5 * time.Minute)
	require.Equal(t, 5*time.Minute, Lifetime(now, &in5))
	past := now.Add(-time.Second)
	require.Equal(t, time.Duration(0), Lifetime(now, &past))
	exact := now
	require.Equal(t, time.Duration(0), Lifetime(now, &exact))
	in20 := now.Add(20 * time.Minute)
	require.Equal(t, DefaultLifetime, Lifetime(now, &in20))
	atDefault := now.Add(DefaultLifetime)
	require.Equal(t, DefaultLifetime, Lifetime(now, &atDefault))
}

func TestIssueRejects(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	key := testKey()
	ok := fullClaims()
	_, err := Issue(key, testIssuer, ok, now, 0)
	require.Error(t, err)
	_, err = Issue(key, testIssuer, ok, now, -time.Second)
	require.Error(t, err)
	_, err = Issue(key, testIssuer, ok, now, MaxLifetime+time.Second)
	require.Error(t, err)
	_, err = Issue(key, testIssuer, ok, now, MaxLifetime)
	require.NoError(t, err)

	cases := []struct {
		name string
		edit func(*Claims)
	}{
		{"subject", func(c *Claims) { c.Subject = "" }},
		{"tenant", func(c *Claims) { c.TenantID = 0 }},
		{"ticket", func(c *Claims) { c.TicketID = "" }},
		{"parent", func(c *Claims) { c.ParentCredentialID = "" }},
		{"vph", func(c *Claims) { c.Cnf.VPH = "" }},
		{"scope", func(c *Claims) { c.Scope.KnowledgeBaseIDs = nil }},
		{"ops", func(c *Claims) { c.Ops = []string{} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := fullClaims()
			tc.edit(&c)
			_, err := Issue(key, testIssuer, c, now, DefaultLifetime)
			require.Error(t, err)
		})
	}
}

func TestIssueVerifyRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	key := testKey()
	in := fullClaims()
	tok, err := Issue(key, testIssuer, in, now, DefaultLifetime)
	require.NoError(t, err)

	parts := strings.Split(tok, ".")
	require.Len(t, parts, 3)
	headerRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
	require.NoError(t, err)
	var header map[string]any
	require.NoError(t, json.Unmarshal(headerRaw, &header))
	require.Equal(t, "HS256", header["alg"])
	_, hasKid := header["kid"]
	require.False(t, hasKid)

	payloadRaw, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(payloadRaw, &payload))
	for _, key := range []string{
		"iss", "sub", "aud", "exp", "nbf", "iat", "jti",
		"tenant_id", "scope", "ops", "ticket_id", "parent_credential_id",
		"binding_tag", "partition_tag", "cnf", "gen",
	} {
		require.Contains(t, payload, key)
	}
	require.Equal(t, []any{Audience}, payload["aud"])
	scope, ok := payload["scope"].(map[string]any)
	require.True(t, ok)
	require.Contains(t, scope, "knowledge_base_ids")
	cnf, ok := payload["cnf"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "vph-1", cnf["vph"])

	got, err := Verify(key, testIssuer, tok, now)
	require.NoError(t, err)
	require.Equal(t, testIssuer, got.Issuer)
	require.Equal(t, jwt.ClaimStrings{Audience}, got.Audience)
	require.Equal(t, in.Subject, got.Subject)
	require.True(t, got.IssuedAt.Equal(now))
	require.True(t, got.NotBefore.Equal(now))
	require.True(t, got.ExpiresAt.Equal(now.Add(DefaultLifetime)))
	_, err = uuid.Parse(got.ID)
	require.NoError(t, err)
	require.Equal(t, in.TenantID, got.TenantID)
	require.Equal(t, in.Scope, got.Scope)
	require.Equal(t, in.Ops, got.Ops)
	require.Equal(t, in.TicketID, got.TicketID)
	require.Equal(t, in.ParentCredentialID, got.ParentCredentialID)
	require.Equal(t, in.BindingTag, got.BindingTag)
	require.Equal(t, in.PartitionTag, got.PartitionTag)
	require.Equal(t, in.Cnf, got.Cnf)
	require.NotNil(t, got.Gen)
	require.Equal(t, *in.Gen, *got.Gen)
}

func TestVerifyRejects(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	key := testKey()
	valid := issuedClaims(now, DefaultLifetime)

	noneHeader := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	nonePayload, err := json.Marshal(valid)
	require.NoError(t, err)
	noneTok := noneHeader + "." + base64.RawURLEncoding.EncodeToString(nonePayload) + "."

	rsHeader := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	rsPayload, err := json.Marshal(valid)
	require.NoError(t, err)
	rsInput := rsHeader + "." + base64.RawURLEncoding.EncodeToString(rsPayload)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(rsInput))
	rsTok := rsInput + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))

	good, err := Issue(key, testIssuer, fullClaims(), now, DefaultLifetime)
	require.NoError(t, err)

	missingNBF := claimsMap(t, valid)
	delete(missingNBF, "nbf")
	missingTicket := claimsMap(t, valid)
	delete(missingTicket, "ticket_id")
	missingVPH := claimsMap(t, valid)
	cnf, ok := missingVPH["cnf"].(map[string]any)
	require.True(t, ok)
	delete(cnf, "vph")
	emptyScope := claimsMap(t, valid)
	emptyScope["scope"] = map[string]any{"knowledge_base_ids": []any{}}
	emptyOps := claimsMap(t, valid)
	emptyOps["ops"] = []any{}
	badAud := claimsMap(t, valid)
	badAud["aud"] = []any{"other"}
	multiAud := claimsMap(t, valid)
	multiAud["aud"] = []any{Audience, "other"}

	span := issuedClaims(now, DefaultLifetime)
	span.ExpiresAt = jwt.NewNumericDate(now.Add(16 * time.Minute))
	// Only nbf is in the future: iat stays at now, so the iat check cannot be what rejects it.
	nbfOnly := issuedClaims(now, DefaultLifetime)
	nbfOnly.NotBefore = jwt.NewNumericDate(now.Add(31 * time.Second))
	missingJTI := claimsMap(t, valid)
	delete(missingJTI, "jti")
	missingBinding := claimsMap(t, valid)
	delete(missingBinding, "binding_tag")
	missingPartition := claimsMap(t, valid)
	delete(missingPartition, "partition_tag")
	missingGen := claimsMap(t, valid)
	delete(missingGen, "gen")
	nullGen := claimsMap(t, valid)
	nullGen["gen"] = nil
	// Expired and missing a required claim is invalid, not expired.
	expiredNoBinding := claimsMap(t, issuedClaims(now.Add(-time.Hour), DefaultLifetime))
	delete(expiredNoBinding, "binding_tag")

	cases := []struct {
		name  string
		token string
	}{
		{"alg none", noneTok},
		{"hs512", signMethod(t, key, jwt.SigningMethodHS512, valid)},
		{"rs256 header hmac signature", rsTok},
		{"wrong key", good},
		{"bad aud", signMap(t, key, badAud)},
		{"multi aud", signMap(t, key, multiAud)},
		{"bad iss", signMethod(t, key, jwt.SigningMethodHS256, func() Claims {
			c := valid
			c.Issuer = "other-issuer"
			return c
		}())},
		{"nbf too far", func() string {
			tok, err := Issue(key, testIssuer, fullClaims(), now.Add(31*time.Second), DefaultLifetime)
			require.NoError(t, err)
			return tok
		}()},
		{"nbf only too far", signMethod(t, key, jwt.SigningMethodHS256, nbfOnly)},
		{"missing nbf", signMap(t, key, missingNBF)},
		{"missing jti", signMap(t, key, missingJTI)},
		{"missing binding_tag", signMap(t, key, missingBinding)},
		{"missing partition_tag", signMap(t, key, missingPartition)},
		{"missing gen", signMap(t, key, missingGen)},
		{"null gen", signMap(t, key, nullGen)},
		{"expired and missing binding_tag", signMap(t, key, expiredNoBinding)},
		{"missing ticket_id", signMap(t, key, missingTicket)},
		{"missing cnf.vph", signMap(t, key, missingVPH)},
		{"empty scope", signMap(t, key, emptyScope)},
		{"empty ops", signMap(t, key, emptyOps)},
		{"exp-iat 16m", signMethod(t, key, jwt.SigningMethodHS256, span)},
		{"truncated", good[:len(good)-4]},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verifyKey := key
			if tc.name == "wrong key" {
				verifyKey = []byte("fedcba9876543210fedcba9876543210")
			}
			_, err := Verify(verifyKey, testIssuer, tc.token, now)
			require.Error(t, err)
			require.ErrorIs(t, err, ErrInvalid)
			require.NotErrorIs(t, err, ErrExpired)
			require.NotContains(t, err.Error(), tc.token)
		})
	}

	// An empty issuer or a short key is refused before parsing: jwt.WithIssuer("") would check nothing.
	_, err = Verify(key, "", good, now)
	require.ErrorIs(t, err, ErrInvalid)
	_, err = Verify(key[:31], testIssuer, good, now)
	require.ErrorIs(t, err, ErrInvalid)
	_, err = Issue(key, "", fullClaims(), now, DefaultLifetime)
	require.Error(t, err)
	_, err = Issue(key[:31], testIssuer, fullClaims(), now, DefaultLifetime)
	require.Error(t, err)

	// gen 0 is a legal value, not a missing claim.
	zeroGen := fullClaims()
	zeroGen.Gen = genPtr(0)
	zeroTok, err := Issue(key, testIssuer, zeroGen, now, DefaultLifetime)
	require.NoError(t, err)
	got, err := Verify(key, testIssuer, zeroTok, now)
	require.NoError(t, err)
	require.Equal(t, int64(0), *got.Gen)
	for _, mutate := range []func(*Claims){
		func(c *Claims) { c.BindingTag = "" },
		func(c *Claims) { c.PartitionTag = "" },
		func(c *Claims) { c.Gen = nil },
	} {
		c := fullClaims()
		mutate(&c)
		_, err = Issue(key, testIssuer, c, now, DefaultLifetime)
		require.Error(t, err)
	}

	// exp-iat of exactly MaxLifetime is still allowed.
	exact := issuedClaims(now, MaxLifetime)
	_, err = Verify(key, testIssuer, signMethod(t, key, jwt.SigningMethodHS256, exact), now)
	require.NoError(t, err)
}

func TestVerifyExpiry(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	key := testKey()

	expiredAt := now.Add(-31*time.Second - DefaultLifetime)
	expired, err := Issue(key, testIssuer, fullClaims(), expiredAt, DefaultLifetime)
	require.NoError(t, err)
	_, err = Verify(key, testIssuer, expired, now)
	require.ErrorIs(t, err, ErrExpired)
	require.NotErrorIs(t, err, ErrInvalid)
	require.NotContains(t, err.Error(), expired)

	within := now.Add(-29*time.Second - DefaultLifetime)
	tok, err := Issue(key, testIssuer, fullClaims(), within, DefaultLifetime)
	require.NoError(t, err)
	_, err = Verify(key, testIssuer, tok, now)
	require.NoError(t, err)

	soon, err := Issue(key, testIssuer, fullClaims(), now.Add(29*time.Second), DefaultLifetime)
	require.NoError(t, err)
	_, err = Verify(key, testIssuer, soon, now)
	require.NoError(t, err)

	// Flip a real signature byte. Changing the last base64url character can touch only the unused tail
	// bits and leave the decoded signature unchanged.
	dot := strings.LastIndexByte(expired, '.')
	sig, err := base64.RawURLEncoding.DecodeString(expired[dot+1:])
	require.NoError(t, err)
	sig[0] ^= 0x01
	bad := expired[:dot+1] + base64.RawURLEncoding.EncodeToString(sig)
	_, err = Verify(key, testIssuer, bad, now)
	require.ErrorIs(t, err, ErrInvalid)
	require.NotErrorIs(t, err, ErrExpired)
}
