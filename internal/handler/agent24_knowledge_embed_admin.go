// Agent24 fork product patch A24-KB-39 (KB-C2-02a, WeKnora#134, UI-BRIDGE-v1 U4.2).

package handler

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Agent24KnowledgeEmbedAdminDirEnv names the directory holding the embed knowledge page build
// (frontend `npm run build:embed-admin`, output dist-embed-admin/): embed-admin.html and assets/.
const Agent24KnowledgeEmbedAdminDirEnv = "AGENT24_KNOWLEDGE_EMBED_ADMIN_DIR"

// Agent24KnowledgeEmbedAdminPageFile is the page document inside that directory.
const Agent24KnowledgeEmbedAdminPageFile = "embed-admin.html"

// Agent24KnowledgeEmbedAdminDirFromEnv returns the configured directory, or "" when unset or blank.
// A configured value must be an absolute path to a directory that holds a regular embed-admin.html;
// anything else is an error, so integration mode fails at startup instead of serving 404 pages.
func Agent24KnowledgeEmbedAdminDirFromEnv() (string, error) {
	raw := strings.TrimSpace(os.Getenv(Agent24KnowledgeEmbedAdminDirEnv))
	if raw == "" {
		return "", nil
	}
	if !filepath.IsAbs(raw) {
		return "", fmt.Errorf("%s must be an absolute path", Agent24KnowledgeEmbedAdminDirEnv)
	}
	dir := filepath.Clean(raw)
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", Agent24KnowledgeEmbedAdminDirEnv)
	}
	page, err := os.Lstat(filepath.Join(dir, Agent24KnowledgeEmbedAdminPageFile))
	if err != nil || !page.Mode().IsRegular() {
		return "", errors.New(Agent24KnowledgeEmbedAdminDirEnv + " has no regular " +
			Agent24KnowledgeEmbedAdminPageFile)
	}
	return dir, nil
}
