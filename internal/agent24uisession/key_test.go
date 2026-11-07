// Agent24 fork product patch A24-KB-31 (KB-C2-01a, WeKnora#133, CONTRACT-UI-BRIDGE-v1 U2–U3).

package agent24uisession

import (
	"bytes"
	"encoding/base64"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestKeyFromEnv(t *testing.T) {
	const tagEnv = "AGENT24_KNOWLEDGE_TAG_KEY"
	k32 := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32))
	other := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{2}, 32))
	k31 := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{3}, 31))

	t.Run("unset", func(t *testing.T) {
		t.Setenv(KeyEnv, "placeholder")
		require.NoError(t, os.Unsetenv(KeyEnv))
		t.Setenv(tagEnv, other)
		key, configured, err := KeyFromEnv()
		require.NoError(t, err)
		require.False(t, configured)
		require.Nil(t, key)
	})
	t.Run("blank", func(t *testing.T) {
		t.Setenv(KeyEnv, " \n\t ")
		t.Setenv(tagEnv, other)
		key, configured, err := KeyFromEnv()
		require.NoError(t, err)
		require.False(t, configured)
		require.Nil(t, key)
	})
	t.Run("not base64", func(t *testing.T) {
		t.Setenv(KeyEnv, "!!!!")
		t.Setenv(tagEnv, "")
		_, configured, err := KeyFromEnv()
		require.Error(t, err)
		require.True(t, configured)
		require.NotContains(t, err.Error(), "!!!!")
	})
	t.Run("31 bytes", func(t *testing.T) {
		t.Setenv(KeyEnv, k31)
		t.Setenv(tagEnv, "")
		_, configured, err := KeyFromEnv()
		require.Error(t, err)
		require.True(t, configured)
		require.NotContains(t, err.Error(), k31)
	})
	t.Run("same as tag key", func(t *testing.T) {
		t.Setenv(KeyEnv, k32)
		t.Setenv(tagEnv, k32)
		_, configured, err := KeyFromEnv()
		require.Error(t, err)
		require.True(t, configured)
		require.NotContains(t, err.Error(), k32)
	})
	t.Run("32 bytes and different", func(t *testing.T) {
		t.Setenv(KeyEnv, "  "+k32+"\n")
		t.Setenv(tagEnv, other)
		key, configured, err := KeyFromEnv()
		require.NoError(t, err)
		require.True(t, configured)
		require.Equal(t, bytes.Repeat([]byte{1}, 32), key)
	})
	t.Run("tag key unset", func(t *testing.T) {
		t.Setenv(KeyEnv, k32)
		t.Setenv(tagEnv, "placeholder")
		require.NoError(t, os.Unsetenv(tagEnv))
		key, configured, err := KeyFromEnv()
		require.NoError(t, err)
		require.True(t, configured)
		require.Equal(t, bytes.Repeat([]byte{1}, 32), key)
	})
	t.Run("tag key invalid", func(t *testing.T) {
		t.Setenv(KeyEnv, k32)
		t.Setenv(tagEnv, "!!!!")
		key, configured, err := KeyFromEnv()
		require.NoError(t, err)
		require.True(t, configured)
		require.Equal(t, bytes.Repeat([]byte{1}, 32), key)
	})
}

func TestIssuerFromEnv(t *testing.T) {
	t.Run("unset", func(t *testing.T) {
		t.Setenv(IssuerEnv, "placeholder")
		require.NoError(t, os.Unsetenv(IssuerEnv))
		require.Equal(t, DefaultIssuer, IssuerFromEnv())
	})
	t.Run("blank", func(t *testing.T) {
		t.Setenv(IssuerEnv, " \n")
		require.Equal(t, DefaultIssuer, IssuerFromEnv())
	})
	t.Run("set", func(t *testing.T) {
		t.Setenv(IssuerEnv, "  example-issuer  ")
		require.Equal(t, "example-issuer", IssuerFromEnv())
	})
}
