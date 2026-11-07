// Agent24 fork product patch A24-KB-31 (KB-C2-01a, WeKnora#133, CONTRACT-UI-BRIDGE-v1 U2–U3).

package agent24uisession

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"strings"
)

// H is the UI bridge length-prefixed hash (UIB §1). It is SHA-256 of
// be32(len(tag)) ‖ tag ‖ be32(len(x₁)) ‖ x₁ ‖ …, encoded as unpadded base64url. Lengths are byte lengths.
func H(tag string, parts ...[]byte) string {
	mac := sha256.New()
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(tag)))
	_, _ = mac.Write(n[:])
	_, _ = mac.Write([]byte(tag))
	for _, part := range parts {
		binary.BigEndian.PutUint32(n[:], uint32(len(part)))
		_, _ = mac.Write(n[:])
		_, _ = mac.Write(part)
	}
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// VPH is H("kb-vph-v1", viewSecret), the family id of one view instance (UIB §1).
func VPH(viewSecret []byte) string { return H("kb-vph-v1", viewSecret) }

// ParseViewProof decodes an X-Agent24-View-Proof value: the raw view_secret as unpadded base64url.
// Surrounding space is ignored. Padding, the standard base64 alphabet, a decode failure or fewer than
// 32 bytes is an error. The error text does not include the header.
func ParseViewProof(header string) ([]byte, error) {
	raw, err := base64.RawURLEncoding.Strict().DecodeString(strings.TrimSpace(header))
	if err != nil || len(raw) < 32 {
		return nil, errors.New("view proof is invalid")
	}
	return raw, nil
}

// VPHEqual reports whether two vph strings are equal, in constant time.
func VPHEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
