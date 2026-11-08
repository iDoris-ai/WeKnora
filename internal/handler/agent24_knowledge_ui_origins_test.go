// Agent24 fork product patch A24-KB-38 (KB-C2-01c5, WeKnora#133).

package handler

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAgent24KnowledgeUIOriginsFromEnv(t *testing.T) {
	for _, unset := range []string{"", "  ", "\t"} {
		t.Setenv(Agent24KnowledgeUIOriginsEnv, unset)
		got, err := Agent24KnowledgeUIOriginsFromEnv()
		require.NoError(t, err, "%q", unset)
		require.Nil(t, got, "%q", unset)
	}

	t.Setenv(Agent24KnowledgeUIOriginsEnv,
		" https://ui.example.org , http://127.0.0.1:5173,http://[::1]:8443,"+
			"https://ui.example.org,https://10.0.0.2:8443")
	got, err := Agent24KnowledgeUIOriginsFromEnv()
	require.NoError(t, err)
	require.Equal(t, []string{
		"https://ui.example.org", "http://127.0.0.1:5173", "http://[::1]:8443", "https://10.0.0.2:8443",
	}, got)
}

func TestAgent24KnowledgeUIOriginsRejected(t *testing.T) {
	valid := []string{
		"https://ui.example.org", "https://ui.example.org:8443", "https://a-b.c1.example", "https://10.0.0.2",
		"https://[2001:db8::1]", "https://[2001:db8::1]:8443", "http://127.0.0.1:5173", "http://[::1]:1",
		"https://x:65535", "https://ui.0xg", "https://ui.example.org1",
	}
	for _, o := range valid {
		require.NoError(t, agent24ValidUIOrigin(o), o)
	}
	invalid := []string{
		"", "*", "null", "ui.example.org", "https://", "https://*.example.org", "https://ui.example.org/",
		"https://ui.example.org/app", "https://ui.example.org?x", "https://ui.example.org#x",
		"https://user@ui.example.org", "https://UI.example.org", "HTTPS://ui.example.org",
		"https://ui.example.org:443", "https://ui.example.org:0", "https://ui.example.org:65536",
		"https://ui.example.org:08443", "https://ui.example.org:", "https://ui.example.org:x",
		"https://ui..example.org", "https://.example.org", "https://ui_x.example.org", "https://bücher.example",
		"http://ui.example.org:8080", "http://localhost:5173", "http://127.0.0.1", "http://127.0.0.1:80",
		"http://127.0.0.2:5173", "http://[::1]", "http://[0:0:0:0:0:0:0:1]:5173", "https://[2001:DB8::1]",
		"https://[2001:db8::1", "https://[::ffff:10.0.0.2]", "https://[fe80::1%25en0]", "https://010.0.0.2",
		"https://ui.0x1f", "https://ui.0x", "https://1.2.3", "https://ui.example.08",
		"ws://127.0.0.1:5173", "file://", "https://ui.example.org:8443:1", "https://ui.example.org extra",
	}
	for _, o := range invalid {
		require.Error(t, agent24ValidUIOrigin(o), o)
	}

	t.Setenv(Agent24KnowledgeUIOriginsEnv, "https://ui.example.org,,http://127.0.0.1:5173")
	_, err := Agent24KnowledgeUIOriginsFromEnv()
	require.ErrorContains(t, err, Agent24KnowledgeUIOriginsEnv)
	t.Setenv(Agent24KnowledgeUIOriginsEnv, "https://ui.example.org,*")
	_, err = Agent24KnowledgeUIOriginsFromEnv()
	require.ErrorContains(t, err, Agent24KnowledgeUIOriginsEnv)
}
