// Agent24 fork product patch A24-KB-31 (KB-C2-01a, WeKnora#133, CONTRACT-UI-BRIDGE-v1 U2–U3).

package agent24uisession

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestNewTicketAndTicketHash(t *testing.T) {
	raw, hash, id, err := NewTicket()
	require.NoError(t, err)
	require.Len(t, raw, 43)
	require.Len(t, hash, 64)
	parsed, err := uuid.Parse(id)
	require.NoError(t, err)
	require.Equal(t, uuid.Version(4), parsed.Version())

	again, err := TicketHash(raw)
	require.NoError(t, err)
	require.Equal(t, hash, again)

	buf, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	require.NoError(t, err)
	require.Len(t, buf, 32)
	sum := sha256.Sum256(buf)
	require.Equal(t, hex.EncodeToString(sum[:]), hash)

	_, err = TicketHash(raw[:42])
	require.Error(t, err)
	_, err = TicketHash(raw + "A")
	require.Error(t, err)
	bad := []byte(raw)
	bad[0] = '+'
	_, err = TicketHash(string(bad))
	require.Error(t, err)
	require.NotContains(t, err.Error(), string(bad))
}
