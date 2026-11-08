// Agent24 fork product patch A24-KB-41 (WeKnora#136).
package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAgent24IngestFormatAndSize(t *testing.T) {
	for _, ok := range []string{"a.md", "A.MD", "notes.markdown", "x.txt", "dir.v2/a.md"} {
		require.True(t, Agent24IngestFormatAllowed(ok), ok)
	}
	for _, bad := range []string{"a.pdf", "a.docx", "a", "a.md.exe", "a.", ".md", "dir/.md"} {
		require.False(t, Agent24IngestFormatAllowed(bad), bad)
	}
	require.False(t, Agent24IngestTooLarge(Agent24IngestMaxBytes))
	require.True(t, Agent24IngestTooLarge(Agent24IngestMaxBytes+1))
}
