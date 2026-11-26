package agent24managed

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// G2: a conflicting env value is refused before anything is written, so a refused start leaves no
// secrets file behind (the old code wrote the file first, then compared the env).
func TestSecretsCheckEnvBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	clearSecretEnv(t)
	t.Setenv("JWT_SECRET", "preset-jwt-secret-from-the-environment-xx")
	if err := PrepareSecrets(dir); err == nil {
		t.Fatal("a preset JWT_SECRET on a fresh state dir must be refused")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), managedSecretsFilename) {
			t.Fatalf("refused start left %s behind", e.Name())
		}
	}
}

// G2: a write killed halfway leaves only a temp file. The next start ignores and removes it and
// writes a complete secrets file.
func TestSecretsRecoverFromInterruptedWrite(t *testing.T) {
	dir := t.TempDir()
	clearSecretEnv(t)
	stale := filepath.Join(dir, managedSecretsFilename+".tmp-123456")
	if err := os.WriteFile(stale, []byte(`{"version":1,"jwt_sec`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := PrepareSecrets(dir); err != nil {
		t.Fatalf("start after an interrupted write: %v", err)
	}
	readSecretFile(t, dir)
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale temp file still there: %v", err)
	}
	assertSecretModes(t, dir)
}

func TestParseArgsRequiresHostFDs(t *testing.T) {
	base := []string{"--managed", "--state-dir", "x"}
	cases := map[string][]string{
		"no handshake fd": append(append([]string{}, base...), "--lifeline-fd", "4"),
		"no lifeline fd":  append(append([]string{}, base...), "--handshake-fd", "3"),
		"same fd":         append(append([]string{}, base...), "--handshake-fd", "3", "--lifeline-fd", "3"),
		"lifeline fd 0":   append(append([]string{}, base...), "--handshake-fd", "3", "--lifeline-fd", "0"),
	}
	for name, args := range cases {
		if _, err := ParseArgs(args); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	opts, err := ParseArgs(append(base, "--handshake-fd=5", "--lifeline-fd", "6"))
	if err != nil {
		t.Fatal(err)
	}
	if opts.HandshakeFD != 5 || opts.LifelineFD != 6 {
		t.Fatalf("options = %+v", opts)
	}
}

// G3: storage is pinned to the state dir; a conflicting process value is refused by name only.
func TestApplyManagedEnvPinsStorage(t *testing.T) {
	dir := t.TempDir()
	for _, k := range []string{
		"DB_DRIVER", "DB_PATH", "STORAGE_TYPE", "LOCAL_STORAGE_BASE_DIR",
		"RETRIEVE_DRIVER", "STREAM_MANAGER_TYPE", "NEO4J_ENABLE", "WEKNORA_SANDBOX_MODE",
		"ENABLE_GRAPH_RAG", "GIN_MODE",
	} {
		t.Setenv(k, "")
	}
	if err := ApplyManagedEnv(dir); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"DB_DRIVER":              "sqlite",
		"DB_PATH":                filepath.Join(dir, ManagedDBFilename),
		"STORAGE_TYPE":           "local",
		"LOCAL_STORAGE_BASE_DIR": filepath.Join(dir, ManagedFilesDirname),
		"RETRIEVE_DRIVER":        "sqlite",
		"STREAM_MANAGER_TYPE":    "memory",
		"NEO4J_ENABLE":           "false",
		"WEKNORA_SANDBOX_MODE":   "disabled",
		"ENABLE_GRAPH_RAG":       "false",
		"GIN_MODE":               "release",
	}
	for k, v := range want {
		if got := os.Getenv(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	// Same values again: fine.
	if err := ApplyManagedEnv(dir); err != nil {
		t.Fatalf("idempotent apply: %v", err)
	}
	for k, v := range map[string]string{
		"DB_PATH":                "/somewhere/else.db",
		"LOCAL_STORAGE_BASE_DIR": "/somewhere/else",
		"DB_DRIVER":              "postgres",
		"STORAGE_TYPE":           "minio",
	} {
		t.Run(k, func(t *testing.T) {
			if err := ApplyManagedEnv(dir); err != nil {
				t.Fatal(err)
			}
			t.Setenv(k, v)
			err := ApplyManagedEnv(dir)
			if err == nil {
				t.Fatalf("%s=%s accepted", k, v)
			}
			if strings.Contains(err.Error(), v) {
				t.Fatal("error text contains the value")
			}
		})
	}
}

// G5: only 127.0.0.1:<port> and localhost:<port> as Host, and the same two as Origin when present.
func TestLoopbackGuard(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusTeapot) })
	h := LoopbackGuard(ok, 4321)
	cases := []struct {
		host, origin string
		pass         bool
	}{
		{"127.0.0.1:4321", "", true},
		{"localhost:4321", "", true},
		{"LOCALHOST:4321", "", true},
		{"127.0.0.1:4321", "http://127.0.0.1:4321", true},
		{"127.0.0.1:4321", "http://localhost:4321", true},
		{"evil.example", "", false},
		{"evil.example:4321", "", false},
		{"127.0.0.1", "", false},
		{"127.0.0.1:9999", "", false},
		{"[::1]:4321", "", false},
		{"127.0.0.1:4321", "http://evil.example", false},
		{"127.0.0.1:4321", "http://127.0.0.1:9999", false},
		{"127.0.0.1:4321", "null", false},
		{"127.0.0.1:4321", "https://127.0.0.1:4321", false},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		req.Host = c.host
		if c.origin != "" {
			req.Header.Set("Origin", c.origin)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		passed := rec.Code == http.StatusTeapot
		if passed != c.pass {
			t.Errorf("Host %q Origin %q: status %d, pass want %v", c.host, c.origin, rec.Code, c.pass)
		}
		if !c.pass && rec.Code != http.StatusForbidden {
			t.Errorf("Host %q Origin %q: status %d, want 403", c.host, c.origin, rec.Code)
		}
	}
}
