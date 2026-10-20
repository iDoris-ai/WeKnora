package handler

import (
	"encoding/json"
	"math"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/sandbox"
	"github.com/stretchr/testify/require"
)

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestAgent24KnowledgeCapabilityWireShape(t *testing.T) {
	t.Setenv(Agent24KnowledgeModeEnv, "")
	data := BuildDeploymentCapabilities("standard", DeploymentFeatureAvailability{
		Agents:  true,
		API:     true,
		Sandbox: true,
	})
	raw, err := json.Marshal(data)
	require.NoError(t, err)

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded))
	require.Contains(t, decoded, "edition")
	require.Contains(t, decoded, "capabilities")

	integration, ok := decoded["integration"].(map[string]any)
	require.True(t, ok, "integration must be an object: %s", raw)
	require.Equal(t, []string{"agent24_knowledge"}, sortedKeys(integration))

	knowledge, ok := integration["agent24_knowledge"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, []string{
		"limits", "mode", "protocol_versions", "server_version", "supported_operations",
	}, sortedKeys(knowledge))

	require.Equal(t, "standalone", knowledge["mode"])
	require.Equal(t, []any{float64(1)}, knowledge["protocol_versions"])

	ops, ok := knowledge["supported_operations"].([]any)
	require.True(t, ok, "supported_operations must be an array, not null: %s", raw)
	require.Empty(t, ops)
	require.Contains(t, string(raw), `"supported_operations":[]`)

	serverVersion, ok := knowledge["server_version"].(string)
	require.True(t, ok)
	require.NotEmpty(t, serverVersion)
	require.LessOrEqual(t, len(serverVersion), 128)

	limits, ok := knowledge["limits"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, []string{
		"max_concurrency", "max_deadline_ms", "max_page_limit",
		"max_read_bytes", "max_results", "max_snippet_bytes",
	}, sortedKeys(limits))
	for key, value := range limits {
		n, ok := value.(float64)
		require.True(t, ok, "limit %s must be a number", key)
		require.Greater(t, n, float64(0), "limit %s must be positive", key)
		require.Equal(t, math.Trunc(n), n, "limit %s must be an integer", key)
	}
}

func TestAgent24ServerVersion(t *testing.T) {
	require.Equal(t, "v0.8.3+3e22822", agent24ServerVersion("v0.8.3", "3e22822"))
	require.Equal(t, "v0.8.3", agent24ServerVersion("v0.8.3", "unknown"))
	require.Equal(t, "v0.8.3", agent24ServerVersion("v0.8.3", ""))
	require.Equal(t, "unknown", agent24ServerVersion("", ""))
	require.Equal(t, "unknown", agent24ServerVersion("", "3e22822"))

	// Multi-byte runes straddling the 128-byte boundary must not be split.
	long := strings.Repeat("a", 127) + strings.Repeat("版", 10)
	got := agent24ServerVersion(long, "3e22822")
	require.LessOrEqual(t, len(got), 128)
	require.NotEmpty(t, got)
	require.True(t, utf8.ValidString(got))
	require.Equal(t, strings.Repeat("a", 127), got)

	got = agent24ServerVersion(strings.Repeat("版", 100), "")
	require.LessOrEqual(t, len(got), 128)
	require.True(t, utf8.ValidString(got))
}

func TestOverlayLiveDockerSandboxCapabilityPreservesIntegration(t *testing.T) {
	sandbox.ClearDockerBackendEnabledOverride()
	t.Cleanup(sandbox.ClearDockerBackendEnabledOverride)

	data := BuildDeploymentCapabilities("standard", DeploymentFeatureAvailability{Sandbox: true})
	before := data.Integration
	require.Equal(t, BuildAgent24KnowledgeCapability(), before.Agent24Knowledge)

	for _, enabled := range []bool{true, false} {
		sandbox.SetDockerBackendEnabled(enabled)
		overlaid := overlayLiveDockerSandboxCapability(data)
		require.Equal(t, before, overlaid.Integration)
		require.Equal(t, before, data.Integration)
	}
}
