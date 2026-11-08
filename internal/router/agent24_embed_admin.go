// Agent24 fork product patch A24-KB-39 (KB-C2-02a, WeKnora#134, UI-BRIDGE-v1 U4.2, design §4/§8/§9).
// In integration mode, with the UI read routes and a page directory configured, the app serves the embed
// knowledge page itself, same origin as the API. The responses are build output only, never knowledge
// data, so they are registered before Auth and take no credential (design §9).

package router

import (
	"context"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/logger"
)

const (
	agent24EmbedAdminListPath   = "/embed-admin/knowledge-bases"
	agent24EmbedAdminDetailPath = "/embed-admin/knowledge-bases/:kb_id"
	agent24EmbedAdminAssetPath  = "/embed-admin/assets/*filepath"
	agent24EmbedAdminAssetDir   = "assets"
)

// agent24EmbedAdminCSP is design §4. The host adds its own policy on top (§9); this one is stricter.
const agent24EmbedAdminCSP = "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self'; font-src 'self'; connect-src 'self'; frame-src 'none'; worker-src 'none'; " +
	"object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

// agent24EmbedAdminAssetCSP goes on every asset: an asset opened as a document runs nothing and frames nothing.
const agent24EmbedAdminAssetCSP = "default-src 'none'; frame-ancestors 'none'"

// agent24EmbedAdminAssetTypes are the only extensions served. Anything else is 404, SVG included.
var agent24EmbedAdminAssetTypes = map[string]string{
	".js":    "text/javascript; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".woff2": "font/woff2",
	".woff":  "font/woff",
	".ttf":   "font/ttf",
	".png":   "image/png",
}

// installAgent24EmbedAdmin registers the page routes when integration mode is on, the UI read routes are
// registered (uiRead) and AGENT24_KNOWLEDGE_EMBED_ADMIN_DIR is set. It must run before Auth. Only GET is
// registered: HEAD and every other method answer the gate's 404. An invalid directory already stopped
// startup in installAgent24IntegrationMode; reading the page here can still fail, which also stops startup.
func installAgent24EmbedAdmin(r *gin.Engine, uiRead bool) {
	mode, err := handler.Agent24KnowledgeModeFromEnv()
	if err != nil || mode != handler.Agent24KnowledgeModeIntegration || !uiRead {
		return
	}
	dir, err := handler.Agent24KnowledgeEmbedAdminDirFromEnv()
	if err != nil || dir == "" {
		return
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		logger.Fatalf(context.Background(), "agent24 embed page: cannot open %s",
			handler.Agent24KnowledgeEmbedAdminDirEnv)
	}
	page, err := root.ReadFile(handler.Agent24KnowledgeEmbedAdminPageFile)
	if err != nil {
		logger.Fatalf(context.Background(), "agent24 embed page: cannot read %s",
			handler.Agent24KnowledgeEmbedAdminPageFile)
	}
	serve := agent24EmbedAdminPage(page)
	r.GET(agent24EmbedAdminListPath, serve)
	r.GET(agent24EmbedAdminDetailPath, serve)
	r.GET(agent24EmbedAdminAssetPath, agent24EmbedAdminAsset(root))
	logger.Infof(context.Background(), "Agent24 knowledge integration mode: serving the embed knowledge page")
}

// agent24EmbedAdminPage answers both page routes with the same document, read once at startup.
func agent24EmbedAdminPage(page []byte) gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("Content-Security-Policy", agent24EmbedAdminCSP)
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		c.Data(http.StatusOK, "text/html; charset=utf-8", page)
	}
}

// agent24EmbedAdminAsset serves one regular file directly under assets/. A name that is not a single plain
// segment, any percent-encoding in the request path, a missing file, a directory and an unknown extension are
// the gate's empty 404: no listing and no fallback to the page.
func agent24EmbedAdminAsset(root *os.Root) gin.HandlerFunc {
	return func(c *gin.Context) {
		name, ok := agent24EmbedAdminAssetName(c.Request.URL.RawPath, c.Param("filepath"))
		if !ok {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		ctype, ok := agent24EmbedAdminAssetTypes[path.Ext(name)]
		if !ok {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		f, err := root.Open(agent24EmbedAdminAssetDir + "/" + name)
		if err != nil {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		defer func() { _ = f.Close() }()
		info, err := f.Stat()
		if err != nil || !info.Mode().IsRegular() {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		h := c.Writer.Header()
		h.Set("Content-Type", ctype)
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Content-Security-Policy", agent24EmbedAdminAssetCSP)
		http.ServeContent(c.Writer, c.Request, "", info.ModTime(), f)
	}
}

// agent24EmbedAdminAssetName returns the file name from the *filepath parameter. rawPath is URL.RawPath: it is
// set only when the request spelled the path with an encoding Go would not produce, so any non-empty value is
// refused, which covers %2F, %5C and %2E. The name must be one segment of [A-Za-z0-9._-] not starting with ".".
func agent24EmbedAdminAssetName(rawPath, param string) (string, bool) {
	if rawPath != "" {
		return "", false
	}
	name := strings.TrimPrefix(param, "/")
	if name == "" || len(name) > 128 || name[0] == '.' {
		return "", false
	}
	for i := 0; i < len(name); i++ {
		b := name[i]
		switch {
		case b >= 'a' && b <= 'z', b >= 'A' && b <= 'Z', b >= '0' && b <= '9', b == '.', b == '_', b == '-':
		default:
			return "", false
		}
	}
	return name, true
}
