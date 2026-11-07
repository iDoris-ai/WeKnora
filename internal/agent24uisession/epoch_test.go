// Agent24 fork product patch A24-KB-33 (KB-C2-01b2, WeKnora#133, CONTRACT-UI-BRIDGE-v1 U2.3).

package agent24uisession

import (
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

func TestEpochIsProcessStartToTheSecond(t *testing.T) {
	e := Epoch()
	require.Equal(t, processStart.Truncate(time.Second), e)
	require.False(t, e.After(time.Now()))
	require.Equal(t, e, Epoch(), "the epoch does not move while the process runs")
}

func TestIssuedBefore(t *testing.T) {
	epoch := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	at := func(ts time.Time) *Claims {
		return &Claims{RegisteredClaims: jwt.RegisteredClaims{IssuedAt: jwt.NewNumericDate(ts)}}
	}
	require.True(t, IssuedBefore(nil, epoch))
	require.True(t, IssuedBefore(&Claims{}, epoch))
	require.True(t, IssuedBefore(at(epoch.Add(-time.Second)), epoch))
	require.False(t, IssuedBefore(at(epoch), epoch))
	// iat has second precision: a JWT signed in the start second, after the start, is not refused.
	require.False(t, IssuedBefore(at(epoch.Add(700*time.Millisecond)), epoch))
}
