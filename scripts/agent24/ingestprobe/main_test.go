package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
)

func TestReadKeyFileRefusesAReadableFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "key")
	require.NoError(t, os.WriteFile(p, []byte(" sk-test \n"), 0o600))
	key, err := readKeyFile(p)
	require.NoError(t, err)
	require.Equal(t, "sk-test", key)
	require.NoError(t, os.Chmod(p, 0o640))
	_, err = readKeyFile(p)
	require.ErrorContains(t, err, "must not be readable")
	require.NotContains(t, err.Error(), "sk-test", "the key is never printed")
	empty := filepath.Join(dir, "empty")
	require.NoError(t, os.WriteFile(empty, []byte("\n"), 0o600))
	_, err = readKeyFile(empty)
	require.Error(t, err)
}

func TestProbeScopeRefusal(t *testing.T) {
	tenant := uint64(7)
	key := func(mut func(*types.TenantAPIKey)) *types.TenantAPIKey {
		k := &types.TenantAPIKey{
			ID: 1, TenantID: &tenant, ScopeType: types.APIKeyScopeTenant,
			KnowledgeBaseIDs: types.StringArray{"k2"}, Capabilities: types.StringArray{"retrieve", "ingest"},
		}
		if mut != nil {
			mut(k)
		}
		return k
	}
	require.Empty(t, probeScopeRefusal(key(nil), "k2"))
	require.Equal(t, "forbidden/empty_scope", probeScopeRefusal(key(nil), "k1"))
	require.Equal(t, "forbidden/empty_scope",
		probeScopeRefusal(key(func(k *types.TenantAPIKey) { k.KnowledgeBaseIDs = nil }), "k2"))
	require.Equal(t, "forbidden/operation_not_allowed",
		probeScopeRefusal(key(func(k *types.TenantAPIKey) { k.Capabilities = types.StringArray{"retrieve"} }), "k2"))
	require.Equal(t, "forbidden/operation_not_allowed",
		probeScopeRefusal(key(func(k *types.TenantAPIKey) { k.FullAccess = true }), "k2"))
	require.Equal(t, "unauthorized/credential_invalid",
		probeScopeRefusal(key(func(k *types.TenantAPIKey) { k.ScopeType = types.APIKeyScopePlatform }), "k2"))
	require.Equal(t, "unauthorized/credential_invalid", probeScopeRefusal(nil, "k2"))
}

func TestSubmitRefusesFormatAndSizeBeforeAnyWrite(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key")
	require.NoError(t, os.WriteFile(keyFile, []byte("sk-test"), 0o600))
	doc := filepath.Join(dir, "a.pdf")
	require.NoError(t, os.WriteFile(doc, []byte("%PDF"), 0o600))
	big := filepath.Join(dir, "big.md")
	require.NoError(t, os.WriteFile(big, bytes.Repeat([]byte("x"), 10<<20+1), 0o600))
	for file, want := range map[string]string{doc: "unsupported_format", big: "too_large"} {
		var out, errOut bytes.Buffer
		code := run([]string{"submit", "--key-file", keyFile, "--kb", "k2", "--file", file}, &out, &errOut)
		require.Equal(t, 2, code, errOut.String())
		require.Contains(t, out.String(), "invalid_request/"+want)
	}
}

func TestUsageAndReadOnlyCommandsTakeNoKey(t *testing.T) {
	var out, errOut bytes.Buffer
	require.Equal(t, 1, run(nil, &out, &errOut))
	require.Equal(t, 1, run([]string{"nope"}, &out, &errOut))
	require.Equal(t, 1, run([]string{"status"}, &out, &errOut), "status needs --tenant and --job")
	errOut.Reset()
	require.Equal(t, 1, run([]string{"status", "--key-file", "x", "--tenant", "7", "--job", "j"}, &out, &errOut))
	require.True(t, strings.Contains(errOut.String(), "flag provided but not defined"), "status has no key flag")
	t.Setenv("REDIS_ADDR", "")
	out.Reset()
	require.Equal(t, 0, run([]string{"workers"}, &out, &errOut))
	require.Contains(t, out.String(), `"redis": false`)
	require.Equal(t, 1, run([]string{"workers", "--key-file", "x"}, &out, &errOut))
	_, _, err := parseSubmitArgs([]string{"--kb", "k2"})
	require.Error(t, err)
}

func parseSubmitArgs(args []string) (submitFlags, string, error) {
	f, err := parseSubmit(args)
	return f, f.name, err
}
