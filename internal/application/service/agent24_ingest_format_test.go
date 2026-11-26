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

// A24-KB-62: csv and json only on the Lite managed host.
func TestAgent24IngestFormatLiteExtras(t *testing.T) {
	t.Cleanup(func() { agent24LiteFormatsOn.Store(false) })
	for _, name := range []string{"rows.csv", "data.JSON"} {
		if Agent24IngestFormatAllowed(name) {
			t.Fatalf("%s allowed outside the managed host", name)
		}
	}
	EnableAgent24LiteIngestFormats()
	for _, name := range []string{"rows.csv", "data.JSON", "note.md"} {
		if !Agent24IngestFormatAllowed(name) {
			t.Fatalf("%s refused on the managed host", name)
		}
	}
	if Agent24IngestFormatAllowed("doc.pdf") {
		t.Fatal("pdf allowed on the managed host")
	}
}
