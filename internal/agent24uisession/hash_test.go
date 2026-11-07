// Agent24 fork product patch A24-KB-31 (KB-C2-01a, WeKnora#133, CONTRACT-UI-BRIDGE-v1 U2–U3).

package agent24uisession

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"testing"

	"github.com/stretchr/testify/require"
)

// independentH is the UIB §1 formula written out again, so a change to H cannot agree with itself.
func independentH(tag string, parts ...[]byte) string {
	var buf []byte
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(tag)))
	buf = append(buf, n[:]...)
	buf = append(buf, tag...)
	for _, part := range parts {
		binary.BigEndian.PutUint32(n[:], uint32(len(part)))
		buf = append(buf, n[:]...)
		buf = append(buf, part...)
	}
	sum := sha256.Sum256(buf)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func TestH(t *testing.T) {
	// Hand-checked vector: H("kb-vph-v1", []byte{0x01, 0x02}).
	const hand = "_tAYfVtzSo-SV9hsh5hOiggx5KuS8DOY_qHLigrp51Q"
	got := H("kb-vph-v1", []byte{0x01, 0x02})
	require.Equal(t, hand, got)
	require.Equal(t, independentH("kb-vph-v1", []byte{0x01, 0x02}), got)

	empty := H("kb-vph-v1")
	require.Equal(t, independentH("kb-vph-v1"), empty)
	require.NotEqual(t, got, empty)

	left := H("kb-vph-v1", []byte("ab"), []byte("c"))
	right := H("kb-vph-v1", []byte("a"), []byte("bc"))
	require.Equal(t, independentH("kb-vph-v1", []byte("ab"), []byte("c")), left)
	require.Equal(t, independentH("kb-vph-v1", []byte("a"), []byte("bc")), right)
	require.NotEqual(t, left, right)
}

func TestVPH(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	require.Equal(t, H("kb-vph-v1", secret), VPH(secret))
	require.True(t, VPHEqual(VPH(secret), VPH(secret)))
	require.False(t, VPHEqual(VPH(secret), VPH(append(secret, 1))))
	require.False(t, VPHEqual("short", "longer"))
}

func TestParseViewProof(t *testing.T) {
	raw := []byte("0123456789abcdef0123456789abcdef")
	enc := base64.RawURLEncoding.EncodeToString(raw)
	got, err := ParseViewProof("  " + enc + "\n")
	require.NoError(t, err)
	require.Equal(t, raw, got)

	short := base64.RawURLEncoding.EncodeToString(raw[:31])
	_, err = ParseViewProof(short)
	require.Error(t, err)

	padded := base64.URLEncoding.EncodeToString(raw)
	require.Contains(t, padded, "=")
	_, err = ParseViewProof(padded)
	require.Error(t, err)

	plus := []byte(enc)
	plus[0] = '+'
	_, err = ParseViewProof(string(plus))
	require.Error(t, err)
	slash := []byte(enc)
	slash[1] = '/'
	_, err = ParseViewProof(string(slash))
	require.Error(t, err)

	_, err = ParseViewProof("")
	require.Error(t, err)
	_, err = ParseViewProof("   ")
	require.Error(t, err)
}
