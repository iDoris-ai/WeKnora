// Agent24 fork product patch A24-KB-20 (KB-C1-03 C3-b2, WeKnora#41, CONTRACT-READONLY-v1 R1.6).

package interfaces

import (
	"context"
	"time"
)

// Agent24CredentialRevocationStore is the jti revocation list (A24-KB-20).
type Agent24CredentialRevocationStore interface {
	// Revoke records jti as revoked for tenantID until expiresAt. Revoking a jti that is already listed is not
	// an error and keeps the listed expiry. tenantID must be non-zero and jti valid (the same rule middleware.Auth
	// applies when it reads a jti: trimmed, non-empty, at most 128 runes, no control characters).
	Revoke(ctx context.Context, tenantID uint64, jti string, expiresAt time.Time) error
	// IsRevoked reports whether jti is listed for tenantID with an expiry after now.
	IsRevoked(ctx context.Context, tenantID uint64, jti string, now time.Time) (bool, error)
	// PurgeExpired deletes the entries whose expiry is at or before before, and returns how many it removed.
	PurgeExpired(ctx context.Context, before time.Time) (int64, error)
}
