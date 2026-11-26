// Agent24 fork product patch A24-KB-61 (KB-LITE-02a, G3): managed storage lives in the state dir.

package agent24managed

import (
	"fmt"
	"os"
	"path/filepath"
)

// ManagedDBFilename and ManagedFilesDirname are the database file and the local storage directory
// inside the state directory.
const (
	ManagedDBFilename   = "weknora.db"
	ManagedFilesDirname = "files"
)

// ApplyManagedEnv pins storage to stateDir and fills the Lite defaults that .env.lite used to
// supply (a managed host never loads a dotenv file):
//   - forced: DB_DRIVER=sqlite, DB_PATH=<stateDir>/weknora.db, STORAGE_TYPE=local,
//     LOCAL_STORAGE_BASE_DIR=<stateDir>/files, AGENT24_KNOWLEDGE_MODE=integration (G6: only the
//     allowlisted integration routes are served; /auth/auto-setup and the rest answer 404). A non-empty process value that differs is an error
//     (the error names the variable, never its value).
//   - default when unset: RETRIEVE_DRIVER=sqlite, STREAM_MANAGER_TYPE=memory, NEO4J_ENABLE=false,
//     WEKNORA_SANDBOX_MODE=disabled, ENABLE_GRAPH_RAG=false, GIN_MODE=release.
//
// stateDir must already be absolute (ParseArgs makes it so).
func ApplyManagedEnv(stateDir string) error {
	forced := [][2]string{
		{"DB_DRIVER", "sqlite"},
		{"DB_PATH", filepath.Join(stateDir, ManagedDBFilename)},
		{"STORAGE_TYPE", "local"},
		{"LOCAL_STORAGE_BASE_DIR", filepath.Join(stateDir, ManagedFilesDirname)},
		{"AGENT24_KNOWLEDGE_MODE", "integration"},
	}
	for _, kv := range forced {
		if got := os.Getenv(kv[0]); got != "" && got != kv[1] {
			return fmt.Errorf("%s does not match the managed state directory", kv[0])
		}
	}
	for _, kv := range forced {
		if err := os.Setenv(kv[0], kv[1]); err != nil {
			return err
		}
	}
	defaults := [][2]string{
		{"RETRIEVE_DRIVER", "sqlite"},
		{"STREAM_MANAGER_TYPE", "memory"},
		{"NEO4J_ENABLE", "false"},
		{"WEKNORA_SANDBOX_MODE", "disabled"},
		{"ENABLE_GRAPH_RAG", "false"},
		{"GIN_MODE", "release"},
	}
	for _, kv := range defaults {
		if os.Getenv(kv[0]) != "" {
			continue
		}
		if err := os.Setenv(kv[0], kv[1]); err != nil {
			return err
		}
	}
	return nil
}
