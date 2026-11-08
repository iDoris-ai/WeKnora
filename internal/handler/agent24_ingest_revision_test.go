// Agent24 fork product patch A24-KB-41 (WeKnora#136).
package handler

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/types"
)

// The revision a job records is the one search and read derive for the same row.
func TestAgent24IngestSourceRevisionMatchesTheReadDerivation(t *testing.T) {
	key := make([]byte, 32)
	k := &types.Knowledge{ID: "k-1", Type: "file", FileHash: "h", FilePath: "7/k-1/a.md", Source: ""}
	want := agent24SourceRevision(key, "k-1", agent24SourceFingerprint{FileHash: "h", FilePath: "7/k-1/a.md"})
	require.Equal(t, want, Agent24IngestSourceRevision(key)(k))
	other := *k
	other.FilePath = "7/k-1/b.md"
	require.NotEqual(t, want, Agent24IngestSourceRevision(key)(&other), "a new original is a new revision")
}
