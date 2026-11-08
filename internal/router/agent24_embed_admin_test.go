// Agent24 fork product patch A24-KB-39 (KB-C2-02a, WeKnora#134).

package router

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/handler"
)

const (
	agent24EmbedAdminFixturePage = "<!doctype html><title>embed</title>"
	agent24EmbedAdminFixtureJS   = "export const x = 1\n"
)

// agent24EmbedAdminFixture writes a page directory and points AGENT24_KNOWLEDGE_EMBED_ADMIN_DIR at it.
// Next to it sits outside.js, which a symlink inside assets/ points at.
func agent24EmbedAdminFixture(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	dir := filepath.Join(base, "dist")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "assets", "sub"), 0o755))
	write := func(name, body string) {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
	}
	write(handler.Agent24KnowledgeEmbedAdminPageFile, agent24EmbedAdminFixturePage)
	write("assets/x.js", agent24EmbedAdminFixtureJS)
	write("assets/x.css", "a{}")
	write("assets/f.woff2", "wOF2")
	write("assets/x.svg", "<svg/>")
	write("assets/x.js.map", "{}")
	write("assets/.hidden.js", "1")
	write("assets/sub/y.js", "1")
	write("secret.js", "1")
	require.NoError(t, os.WriteFile(filepath.Join(base, "outside.js"), []byte("1"), 0o644))
	require.NoError(t, os.Symlink(filepath.Join(base, "outside.js"), filepath.Join(dir, "assets", "out.js")))
	t.Setenv(handler.Agent24KnowledgeEmbedAdminDirEnv, dir)
	return dir
}

// agent24ConcretePath fills a route template with a value that reaches the handler.
func agent24ConcretePath(p string) string {
	if p == agent24EmbedAdminAssetPath {
		return "/embed-admin/assets/x.js"
	}
	return agent24RouteParam.ReplaceAllString(p, "x")
}

func agent24EmbedAdminRouter(t *testing.T, mode string, uiRead bool) *gin.Engine {
	t.Helper()
	agent24SetTagKey(t, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	gin.SetMode(gin.TestMode)
	t.Setenv("LOCAL_STORAGE_BASE_DIR", t.TempDir())
	t.Setenv("AGENT24_KNOWLEDGE_MODE", mode)
	params := RouterParams{
		Config: &config.Config{Tenant: &config.TenantConfig{}}, SystemHandler: &handler.SystemHandler{},
	}
	if uiRead {
		params.Agent24KnowledgeUIReadHandler = &handler.Agent24KnowledgeUIReadHandler{}
	}
	return NewRouter(params)
}

func agent24Serve(r *gin.Engine, method, target string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func agent24EmbedAdminRegistered(r *gin.Engine) []string {
	var out []string
	for _, rt := range r.Routes() {
		if strings.HasPrefix(rt.Path, "/embed-admin/") {
			out = append(out, rt.Method+" "+rt.Path)
		}
	}
	return out
}

// TestAgent24EmbedAdminPage: both page routes answer the page document with the design §4 CSP and no-store,
// with no credential and with a forged one alike: they sit before Auth and the credential gate.
func TestAgent24EmbedAdminPage(t *testing.T) {
	agent24EmbedAdminFixture(t)
	r := agent24EmbedAdminRouter(t, "integration", true)
	require.ElementsMatch(t, []string{
		"GET " + agent24EmbedAdminListPath, "GET " + agent24EmbedAdminDetailPath, "GET " + agent24EmbedAdminAssetPath,
	}, agent24EmbedAdminRegistered(r))

	for _, target := range []string{
		"/embed-admin/knowledge-bases", "/embed-admin/knowledge-bases/kb-1",
		"/embed-admin/knowledge-bases/kb-1?x=1",
	} {
		for _, hdr := range []map[string]string{nil, {"Authorization": "Bearer forged", "X-API-Key": "sk-forged"}} {
			w := agent24Serve(r, http.MethodGet, target, hdr)
			require.Equal(t, http.StatusOK, w.Code, target)
			require.Equal(t, agent24EmbedAdminFixturePage, w.Body.String(), target)
			require.Equal(t, "text/html; charset=utf-8", w.Header().Get("Content-Type"), target)
			require.Equal(t, "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; "+
				"img-src 'self'; font-src 'self'; connect-src 'self'; frame-src 'none'; worker-src 'none'; "+
				"object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'",
				w.Header().Get("Content-Security-Policy"), target)
			require.Equal(t, "no-store", w.Header().Get("Cache-Control"), target)
			require.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"), target)
			require.Equal(t, "no-referrer", w.Header().Get("Referrer-Policy"), target)
			agent24RequireNoCORSHeaders(t, w.Header(), target)
		}
	}

	// Only GET, and only these paths. Everything else is the gate's bare 404.
	for _, c := range []struct{ method, target string }{
		{http.MethodHead, "/embed-admin/knowledge-bases"},
		{http.MethodPost, "/embed-admin/knowledge-bases"},
		{http.MethodPut, "/embed-admin/knowledge-bases/kb-1"},
		{http.MethodOptions, "/embed-admin/knowledge-bases"},
		{http.MethodHead, "/embed-admin/assets/x.js"},
		{http.MethodGet, "/embed-admin/knowledge-bases/"},
		{http.MethodGet, "/embed-admin/knowledge-bases/kb-1/documents"},
		{http.MethodGet, "/embed-admin/"},
		{http.MethodGet, "/embed-admin"},
		{http.MethodGet, "/embed-admin/embed-admin.html"},
		{http.MethodGet, "/embed-admin.html"},
		{http.MethodGet, "/embed-admin/secret.js"},
		{http.MethodGet, "/embed-admin/assets"},
	} {
		w := agent24Serve(r, c.method, c.target, nil)
		require.Equal(t, http.StatusNotFound, w.Code, "%s %s", c.method, c.target)
		require.Zero(t, w.Body.Len(), "%s %s", c.method, c.target)
		require.Empty(t, w.Header().Get("Content-Security-Policy"), "%s %s", c.method, c.target)
	}
}

// TestAgent24EmbedAdminAssets: one plain file name directly under assets/, known extensions only, long cache.
func TestAgent24EmbedAdminAssets(t *testing.T) {
	agent24EmbedAdminFixture(t)
	r := agent24EmbedAdminRouter(t, "integration", true)
	for name, ctype := range map[string]string{
		"x.js": "text/javascript; charset=utf-8", "x.css": "text/css; charset=utf-8", "f.woff2": "font/woff2",
	} {
		forged := map[string]string{"Authorization": "Bearer forged"}
		w := agent24Serve(r, http.MethodGet, "/embed-admin/assets/"+name, forged)
		require.Equal(t, http.StatusOK, w.Code, name)
		require.Equal(t, ctype, w.Header().Get("Content-Type"), name)
		require.Equal(t, "public, max-age=31536000, immutable", w.Header().Get("Cache-Control"), name)
		require.Equal(t, "nosniff", w.Header().Get("X-Content-Type-Options"), name)
		require.Equal(t, "default-src 'none'; frame-ancestors 'none'", w.Header().Get("Content-Security-Policy"), name)
	}
	w := agent24Serve(r, http.MethodGet, "/embed-admin/assets/x.js", nil)
	require.Equal(t, agent24EmbedAdminFixtureJS, w.Body.String())

	for _, target := range []string{
		"/embed-admin/assets/",
		"/embed-admin/assets/missing.js",
		"/embed-admin/assets/sub",                // a directory
		"/embed-admin/assets/sub/y.js",           // not directly under assets/
		"/embed-admin/assets/sub%2Fy.js",         // encoded separator
		"/embed-admin/assets/sub%5Cy.js",         // encoded backslash
		"/embed-admin/assets/%2E%2E%2Fsecret.js", // encoded dot-dot
		"/embed-admin/assets/%78.js",             // any encoding at all
		"/embed-admin/assets/../secret.js",
		"/embed-admin/assets/..%2Fsecret.js",
		"/embed-admin/assets/.hidden.js",
		"/embed-admin/assets/x.svg",
		"/embed-admin/assets/x.js.map",
		"/embed-admin/assets/x",
		"/embed-admin/assets/out.js", // a symlink out of the directory
		"/embed-admin/assets/x.js;.css",
	} {
		w := agent24Serve(r, http.MethodGet, target, nil)
		require.Equal(t, http.StatusNotFound, w.Code, target)
		require.Zero(t, w.Body.Len(), target)
		require.Empty(t, w.Header().Get("Cache-Control"), target)
	}
}

// TestAgent24EmbedAdminNotRegistered: no page routes without the directory, without the UI read routes, or in
// standalone mode.
func TestAgent24EmbedAdminNotRegistered(t *testing.T) {
	t.Run("directory unset", func(t *testing.T) {
		t.Setenv(handler.Agent24KnowledgeEmbedAdminDirEnv, " ")
		r := agent24EmbedAdminRouter(t, "integration", true)
		require.Empty(t, agent24EmbedAdminRegistered(r))
		w := agent24Serve(r, http.MethodGet, "/embed-admin/knowledge-bases", nil)
		require.Equal(t, http.StatusNotFound, w.Code)
		require.Zero(t, w.Body.Len())
	})
	t.Run("no UI read routes", func(t *testing.T) {
		agent24EmbedAdminFixture(t)
		r := agent24EmbedAdminRouter(t, "integration", false)
		require.Empty(t, agent24EmbedAdminRegistered(r))
	})
	t.Run("standalone", func(t *testing.T) {
		agent24EmbedAdminFixture(t)
		r := agent24EmbedAdminRouter(t, "standalone", true)
		require.Empty(t, agent24EmbedAdminRegistered(r))
	})
}

// TestInstallAgent24IntegrationModeRejectsInvalidEmbedAdminDir: a configured directory must hold the page.
func TestInstallAgent24IntegrationModeRejectsInvalidEmbedAdminDir(t *testing.T) {
	gin.SetMode(gin.TestMode)
	agent24SetTagKey(t, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("AGENT24_KNOWLEDGE_MODE", "integration")
	cfg := &config.Config{Tenant: &config.TenantConfig{}}
	empty := t.TempDir()
	pageDir := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(pageDir, handler.Agent24KnowledgeEmbedAdminPageFile), 0o755))
	file := filepath.Join(t.TempDir(), "f")
	require.NoError(t, os.WriteFile(file, nil, 0o644))
	for _, v := range []string{"relative/dist", empty, pageDir, file, filepath.Join(empty, "missing")} {
		t.Setenv(handler.Agent24KnowledgeEmbedAdminDirEnv, v)
		err := installAgent24IntegrationMode(gin.New(), cfg)
		require.ErrorContains(t, err, handler.Agent24KnowledgeEmbedAdminDirEnv, v)
	}
	agent24EmbedAdminFixture(t)
	require.NoError(t, installAgent24IntegrationMode(gin.New(), cfg))
	t.Setenv(handler.Agent24KnowledgeEmbedAdminDirEnv, "")
	require.NoError(t, installAgent24IntegrationMode(gin.New(), cfg))
}

func TestAgent24EmbedAdminAssetName(t *testing.T) {
	for _, ok := range []string{"/index-B3x_9a.js", "/KaTeX_Main-Regular.woff2", "/a"} {
		_, got := agent24EmbedAdminAssetName("", ok)
		require.True(t, got, ok)
	}
	for _, bad := range []string{
		"", "/", "/.x.js", "/a/b.js", "/a\\b.js", "/a b.js", "/a\x00.js", "/a%2F.js",
		"/" + strings.Repeat("a", 129),
	} {
		_, got := agent24EmbedAdminAssetName("", bad)
		require.False(t, got, bad)
	}
	_, got := agent24EmbedAdminAssetName("/embed-admin/assets/%78.js", "/x.js")
	require.False(t, got)
}
