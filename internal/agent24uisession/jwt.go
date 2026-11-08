// Agent24 fork product patch A24-KB-31 (KB-C2-01a, WeKnora#133, CONTRACT-UI-BRIDGE-v1 U2–U3).

package agent24uisession

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/Tencent/WeKnora/internal/types"
)

const (
	// Audience is the only audience a UI session JWT carries (U3.2).
	Audience = "agent24-knowledge-view"
	// DefaultLifetime is the session JWT lifetime when the parent credential does not expire sooner (U3.3).
	DefaultLifetime = 10 * time.Minute
	// MaxLifetime is the longest session JWT lifetime (U3.3). It is types.Agent24UISessionMaxLifetime.
	MaxLifetime = types.Agent24UISessionMaxLifetime
	// ClockLeeway is the exp and nbf skew allowed when a session JWT is checked (U3.4).
	ClockLeeway = 30 * time.Second

	// minKeyBytes is the shortest signing key Issue and Verify accept; KeyFromEnv enforces the same.
	minKeyBytes = 32
)

// ErrExpired means the signature was valid and the only problem is that exp has passed.
var ErrExpired = errors.New("agent24 ui session token expired")

// ErrInvalid means the session JWT is not usable. Signature, algorithm, audience, issuer, nbf, shape and
// missing claims all use this error.
var ErrInvalid = errors.New("agent24 ui session token invalid")

// UIOps is the first UI operation list (A24-KB-33, U3.2). C2-01c narrows it to the routes in
// ui-routes.json. The exchange copies it into the session JWT.
var UIOps = []string{
	"kb.list", "kb.get", "doc.list", "doc.get", "chunk.list", "wiki.list", "wiki.get", "tag.list",
}

// Scope is the knowledge-base allowlist carried in a session JWT (U3.2).
type Scope struct {
	KnowledgeBaseIDs []string `json:"knowledge_base_ids"`
}

// Confirmation binds a session JWT to one view family (U3.2).
type Confirmation struct {
	VPH string `json:"vph"`
}

// Claims is a UI session JWT (U3.2).
type Claims struct {
	jwt.RegisteredClaims
	TenantID           uint64       `json:"tenant_id"`
	Scope              Scope        `json:"scope"`
	Ops                []string     `json:"ops"`
	TicketID           string       `json:"ticket_id"`
	ParentCredentialID string       `json:"parent_credential_id"`
	BindingTag         string       `json:"binding_tag"`
	PartitionTag       string       `json:"partition_tag"`
	Cnf                Confirmation `json:"cnf"`
	// Gen is recorded, never checked (U3.9). It is a pointer so a missing claim is told apart from 0.
	Gen *int64 `json:"gen"`
}

// Lifetime is the session lifetime at now. It starts at DefaultLifetime, shortens so exp is not after the
// parent credential, and is 0 when that credential has already expired. A nil credentialExpiresAt means the
// credential does not expire.
func Lifetime(now time.Time, credentialExpiresAt *time.Time) time.Duration {
	lifetime := DefaultLifetime
	if credentialExpiresAt != nil && credentialExpiresAt.Before(now.Add(DefaultLifetime)) {
		lifetime = credentialExpiresAt.Sub(now)
	}
	if lifetime <= 0 {
		return 0
	}
	return lifetime
}

// Issue signs a session JWT with HS256. It sets iss, aud (exactly Audience), iat, nbf, exp and jti itself.
// The header has no kid. lifetime must be positive and at most MaxLifetime. Subject, TenantID, TicketID,
// ParentCredentialID, BindingTag, PartitionTag, Gen, Cnf.VPH, Scope and Ops are required (U3.2).
func Issue(key []byte, issuer string, c Claims, now time.Time, lifetime time.Duration) (string, error) {
	if len(key) < minKeyBytes || issuer == "" {
		return "", errors.New("session key or issuer is not configured")
	}
	if lifetime <= 0 || lifetime > MaxLifetime {
		return "", errors.New("session lifetime is outside the allowed range")
	}
	if sessionClaimsIncomplete(c) {
		return "", errors.New("session claims are incomplete")
	}
	c.Issuer = issuer
	c.Audience = jwt.ClaimStrings{Audience}
	c.IssuedAt = jwt.NewNumericDate(now)
	c.NotBefore = jwt.NewNumericDate(now)
	c.ExpiresAt = jwt.NewNumericDate(now.Add(lifetime))
	c.ID = bootID + "." + uuid.NewString() // A24-KB-33: the boot id lets a restart refuse it (U2.3)
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, c)
	signed, err := token.SignedString(key)
	if err != nil {
		return "", err
	}
	return signed, nil
}

// Verify checks a session JWT. The key function returns key and does not read kid or alg. nbf must be
// present, aud must be exactly Audience, exp minus iat must be at most MaxLifetime, and the claims Issue
// requires must be present. ErrExpired is returned only when the signature is valid and exp is the only
// problem. Every other failure is ErrInvalid. The error text does not include the token.
func Verify(key []byte, issuer, token string, now time.Time) (*Claims, error) {
	if len(key) < minKeyBytes || issuer == "" {
		// An empty issuer would turn the iss check off (jwt.WithIssuer("") checks nothing).
		return nil, fmt.Errorf("%w: session key or issuer is not configured", ErrInvalid)
	}
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithAudience(Audience),
		jwt.WithIssuer(issuer),
		jwt.WithLeeway(ClockLeeway),
		jwt.WithTimeFunc(func() time.Time { return now }),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
	)
	claims := &Claims{}
	_, err := parser.ParseWithClaims(token, claims, func(*jwt.Token) (any, error) {
		return key, nil
	})
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) && !sessionOtherJWTError(err) && sessionClaimsWellFormed(claims) {
			return nil, fmt.Errorf("%w: session token expired", ErrExpired)
		}
		return nil, fmt.Errorf("%w: session token invalid", ErrInvalid)
	}
	if !sessionClaimsWellFormed(claims) {
		return nil, fmt.Errorf("%w: session token invalid", ErrInvalid)
	}
	return claims, nil
}

func sessionClaimsIncomplete(c Claims) bool {
	return c.Subject == "" || c.TenantID == 0 || c.TicketID == "" || c.ParentCredentialID == "" ||
		c.BindingTag == "" || c.PartitionTag == "" || c.Gen == nil ||
		c.Cnf.VPH == "" || len(c.Scope.KnowledgeBaseIDs) == 0 || len(c.Ops) == 0
}

func sessionClaimsWellFormed(c *Claims) bool {
	if c == nil || sessionClaimsIncomplete(*c) {
		return false
	}
	if c.ID == "" || c.NotBefore == nil || c.IssuedAt == nil || c.ExpiresAt == nil {
		return false
	}
	if len(c.Audience) != 1 || c.Audience[0] != Audience {
		return false
	}
	if c.ExpiresAt.Sub(c.IssuedAt.Time) > MaxLifetime {
		return false
	}
	return true
}

// sessionOtherJWTError reports a validation failure other than expiry. ErrTokenInvalidClaims wraps every
// claim failure, including a lone expiry, so it is not itself another problem.
func sessionOtherJWTError(err error) bool {
	others := []error{
		jwt.ErrTokenMalformed,
		jwt.ErrTokenUnverifiable,
		jwt.ErrTokenSignatureInvalid,
		jwt.ErrTokenNotValidYet,
		jwt.ErrTokenUsedBeforeIssued,
		jwt.ErrTokenInvalidAudience,
		jwt.ErrTokenInvalidIssuer,
		jwt.ErrTokenInvalidSubject,
		jwt.ErrTokenRequiredClaimMissing,
		jwt.ErrTokenInvalidId,
		jwt.ErrInvalidKey,
		jwt.ErrInvalidKeyType,
	}
	for _, sentinel := range others {
		if errors.Is(err, sentinel) {
			return true
		}
	}
	return false
}
