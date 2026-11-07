// Agent24 fork product patch A24-KB-33 (KB-C2-01b2, WeKnora#133, CONTRACT-UI-BRIDGE-v1 U2.3).

package agent24uisession

import "time"

// processStart is when this process loaded the package, which is at startup.
var processStart = time.Now()

// Epoch is the start of this process, truncated to the second because a JWT's iat is.
//
// U2.3: a revoked ticket or family must stay refused across a restart, or else everything issued before
// the restart must stop working. A leak revocation whose write failed lives only in memory, so tickets
// created and session JWTs issued before Epoch are refused (David 2026-11-08: option B). A restart
// therefore ends every UI session; the host rebuilds the view (U3.8).
func Epoch() time.Time { return processStart.Truncate(time.Second) }

// IssuedBefore reports whether a session JWT was issued before epoch. A JWT without iat counts as before.
func IssuedBefore(c *Claims, epoch time.Time) bool {
	return c == nil || c.IssuedAt == nil || c.IssuedAt.Before(epoch)
}
