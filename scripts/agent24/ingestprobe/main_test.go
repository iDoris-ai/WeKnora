package main

import (
	"bytes"
	"errors"
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

// The refusals come in the order an ingest route answers (W1.9): the credential, its scope, then format and size.
func TestProbeAdmissionOrder(t *testing.T) {
	tenant := uint64(7)
	good := &types.TenantAPIKey{
		ID: 1, TenantID: &tenant, ScopeType: types.APIKeyScopeTenant,
		KnowledgeBaseIDs: types.StringArray{"k2"}, Capabilities: types.StringArray{"ingest"},
	}
	noIngest := *good
	noIngest.Capabilities = types.StringArray{"retrieve"}
	big := 10<<20 + 1
	for _, tc := range []struct {
		key     *types.TenantAPIKey
		authErr error
		name    string
		size    int
		want    string
	}{
		{nil, errors.New("revoked"), "a.pdf", 1, "unauthorized/credential_invalid"},
		{&noIngest, nil, "a.md", big, "forbidden/operation_not_allowed"},
		{good, nil, "a.pdf", big, "invalid_request/unsupported_format"},
		{good, nil, "a.md", big, "invalid_request/too_large"},
		{good, nil, "a.md", 10 << 20, ""},
	} {
		require.Equal(t, tc.want, probeAdmission(tc.key, tc.authErr, "k2", tc.name, tc.size), tc)
	}
}

func TestReadKeyFileRefusesANonRegularFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keydir")
	require.NoError(t, os.Mkdir(dir, 0o700))
	_, err := readKeyFile(dir)
	require.ErrorContains(t, err, "not a regular file")
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
