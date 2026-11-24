// Agent24 fork product patch A24-KB-52 (KB-C4-02a mock, Loop 7 first round F2a).
package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAgent24MockActivationLoadAndLookup(t *testing.T) {
	dir := t.TempDir()
	code := "activation-secret"
	hash := sha256Hex(code)
	path := writeMockActivationFile(t, dir, "ok.json", 0o600, mockActivationJSON(hash, 10001, ""))
	loaded, err := LoadAgent24MockActivations(path)
	require.NoError(t, err)
	require.NotNil(t, loaded)

	got, ok := loaded.Lookup(code)
	require.True(t, ok)
	require.Equal(t, uint64(10001), got.TenantID)
	require.Equal(t, "tmpl-kb", got.TemplateKBID)
	require.Equal(t, "cfg-1", got.ConfigVersion)

	_, ok = loaded.Lookup("wrong-code")
	require.False(t, ok)
}

func TestAgent24MockActivationEmptyPath(t *testing.T) {
	loaded, err := LoadAgent24MockActivations("")
	require.NoError(t, err)
	require.Nil(t, loaded)
	_, ok := loaded.Lookup("anything")
	require.False(t, ok)
	_, ok = loaded.Lookup("")
	require.False(t, ok)
}

func TestAgent24MockActivationRejectsGroupReadable(t *testing.T) {
	dir := t.TempDir()
	hash := sha256Hex("activation-secret")
	path := writeMockActivationFile(t, dir, "open.json", 0o640, mockActivationJSON(hash, 10001, ""))
	_, err := LoadAgent24MockActivations(path)
	require.Error(t, err)
}

func TestAgent24MockActivationRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	hash := sha256Hex("activation-secret")
	target := writeMockActivationFile(t, dir, "real.json", 0o600, mockActivationJSON(hash, 10001, ""))
	link := filepath.Join(dir, "link.json")
	require.NoError(t, os.Symlink(target, link))
	_, err := LoadAgent24MockActivations(link)
	require.Error(t, err)
}

func TestAgent24MockActivationRejectsUnknownField(t *testing.T) {
	dir := t.TempDir()
	hash := sha256Hex("activation-secret")
	body := fmt.Sprintf(
		`{"activations":[{"code_sha256":%q,"tenant_id":10001,"template_kb_id":"tmpl-kb","config_version":"cfg-1"}],`+
			`"extra":true}`,
		hash,
	)
	path := writeMockActivationFile(t, dir, "extra.json", 0o600, body)
	_, err := LoadAgent24MockActivations(path)
	require.Error(t, err)
}

func TestAgent24MockActivationRejectsBadHash(t *testing.T) {
	dir := t.TempDir()
	good := sha256Hex("activation-secret")
	cases := []struct {
		name, hash string
	}{
		{"uppercase", toUpperASCII(good)},
		{"short", good[:63]},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := writeMockActivationFile(t, dir, c.name+".json", 0o600, mockActivationJSON(c.hash, 10001, ""))
			_, err := LoadAgent24MockActivations(path)
			require.Error(t, err)
		})
	}
}

func TestAgent24MockActivationRejectsDuplicate(t *testing.T) {
	dir := t.TempDir()
	hash := sha256Hex("activation-secret")
	body := fmt.Sprintf(
		`{"activations":[`+
			`{"code_sha256":%q,"tenant_id":10001,"template_kb_id":"a","config_version":"v1"},`+
			`{"code_sha256":%q,"tenant_id":10002,"template_kb_id":"b","config_version":"v2"}]}`,
		hash, hash,
	)
	path := writeMockActivationFile(t, dir, "dup.json", 0o600, body)
	_, err := LoadAgent24MockActivations(path)
	require.Error(t, err)
}

func TestAgent24MockActivationRejectsZeroTenant(t *testing.T) {
	dir := t.TempDir()
	hash := sha256Hex("activation-secret")
	path := writeMockActivationFile(t, dir, "zero.json", 0o600, mockActivationJSON(hash, 0, ""))
	_, err := LoadAgent24MockActivations(path)
	require.Error(t, err)
}

func TestAgent24MockActivationRejectsEmptyList(t *testing.T) {
	dir := t.TempDir()
	path := writeMockActivationFile(t, dir, "empty.json", 0o600, `{"activations":[]}`)
	_, err := LoadAgent24MockActivations(path)
	require.Error(t, err)
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func toUpperASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - ('a' - 'A')
		}
	}
	return string(b)
}

func mockActivationJSON(hash string, tenant uint64, extra string) string {
	return fmt.Sprintf(
		`{"activations":[{"code_sha256":%q,"tenant_id":%d,"template_kb_id":"tmpl-kb","config_version":"cfg-1"%s}]}`,
		hash, tenant, extra,
	)
}

func writeMockActivationFile(t *testing.T, dir, name string, perm os.FileMode, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(body), perm))
	require.NoError(t, os.Chmod(path, perm))
	return path
}
