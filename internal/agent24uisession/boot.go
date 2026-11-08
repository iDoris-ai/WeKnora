// Agent24 fork product patch A24-KB-33 (KB-C2-01b2, WeKnora#133, CONTRACT-UI-BRIDGE-v1 U2.3, U3.4).

package agent24uisession

import (
	"crypto/rand"
	"encoding/base64"
	"strings"
)

// bootID is random per process, set when the package loads at startup.
var bootID = newBootID()

func newBootID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("agent24uisession: no randomness for the boot id: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// BootID identifies this process.
//
// U2.3: a revoked ticket or family must stay refused across a restart, or else everything issued before
// the restart must stop working. A leak revocation whose write failed lives only in memory, so tickets
// and session JWTs from another process are refused (David 2026-11-08: option B). A random id, not the
// start time, so a restart within the same second or a clock step back is still caught. Issue puts it in
// front of every jti; tickets record it.
func BootID() string { return bootID }

// IssuedByBoot reports whether a session JWT was issued by the process whose id is boot. An empty boot
// matches nothing.
func IssuedByBoot(c *Claims, boot string) bool {
	return c != nil && boot != "" && strings.HasPrefix(c.ID, boot+".")
}
