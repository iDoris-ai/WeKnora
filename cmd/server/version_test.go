package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/handler"
)

func TestHandleVersionFlag(t *testing.T) {
	oldCommit := handler.CommitID
	handler.CommitID = "3e22822-test"
	t.Cleanup(func() { handler.CommitID = oldCommit })

	for _, flag := range []string{"--version", "-version"} {
		var buf bytes.Buffer
		if !handleVersionFlag([]string{"foo", flag}, &buf) {
			t.Fatalf("%s: expected true", flag)
		}
		out := buf.String()
		if !strings.HasPrefix(out, "WeKnora ") || !strings.Contains(out, "commit 3e22822-test") {
			t.Fatalf("%s: unexpected output %q", flag, out)
		}
		if strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") {
			t.Fatalf("%s: expected exactly one line, got %q", flag, out)
		}
	}

	for _, args := range [][]string{nil, {}, {"--versions"}, {"version"}, {"--config", "x"}} {
		var buf bytes.Buffer
		if handleVersionFlag(args, &buf) {
			t.Fatalf("args %q: expected false", args)
		}
		if buf.Len() != 0 {
			t.Fatalf("args %q: expected no output, got %q", args, buf.String())
		}
	}
}
