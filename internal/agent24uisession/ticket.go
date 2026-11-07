// Agent24 fork product patch A24-KB-31 (KB-C2-01a, WeKnora#133, CONTRACT-UI-BRIDGE-v1 U2–U3).

package agent24uisession

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"github.com/google/uuid"
)

const (
	// TicketDefaultTTL is the default one-time ticket lifetime (U2.3).
	TicketDefaultTTL = 30 * time.Second
	// TicketMaxTTL is the longest one-time ticket lifetime (U2.3).
	TicketMaxTTL = 60 * time.Second
)

// NewTicket draws a 32-byte ticket. raw is unpadded base64url (43 characters), hash is the lowercase hex
// SHA-256 of those 32 bytes, and ticketID is a random UUID v4.
func NewTicket() (raw string, hash string, ticketID string, err error) {
	buf := make([]byte, 32)
	if _, err = rand.Read(buf); err != nil {
		return "", "", "", err
	}
	sum := sha256.Sum256(buf)
	raw = base64.RawURLEncoding.EncodeToString(buf)
	hash = hex.EncodeToString(sum[:])
	ticketID = uuid.NewString()
	return raw, hash, ticketID, nil
}

// TicketHash checks that raw is 43 characters of unpadded base64url decoding to 32 bytes, and returns the
// lowercase hex SHA-256 of those bytes. The error text does not include raw.
func TicketHash(raw string) (string, error) {
	if len(raw) != 43 {
		return "", errors.New("ticket is invalid")
	}
	buf, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil || len(buf) != 32 {
		return "", errors.New("ticket is invalid")
	}
	sum := sha256.Sum256(buf)
	return hex.EncodeToString(sum[:]), nil
}
