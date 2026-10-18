package main

// Agent24 fork product patch A24-KB-01: `--version` prints the
// ldflags-injected build metadata and exits before any config, database or
// container initialization.

import (
	"fmt"
	"io"

	"github.com/Tencent/WeKnora/internal/handler"
)

// handleVersionFlag prints one version line to w and returns true when args
// contain --version or -version.
func handleVersionFlag(args []string, w io.Writer) bool {
	for _, arg := range args {
		if arg == "--version" || arg == "-version" {
			fmt.Fprintf(w, "WeKnora %s (commit %s, edition %s, built %s, %s)\n",
				handler.Version, handler.CommitID, handler.Edition, handler.BuildTime, handler.GoVersion)
			return true
		}
	}
	return false
}
