package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDotenvManagedLoadsNothing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env.lite"), []byte("A24_DOTENV_PROBE_LITE=lite\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	explicit := filepath.Join(dir, "explicit.env")
	if err := os.WriteFile(explicit, []byte("A24_DOTENV_PROBE_EXPLICIT=explicit\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	// godotenv never overrides a variable that is present, even empty: unset them (t.Setenv restores).
	for _, k := range []string{"A24_DOTENV_PROBE_LITE", "A24_DOTENV_PROBE_EXPLICIT"} {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("ENV_FILE", explicit)
	if err := loadDotenv(true, true); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("A24_DOTENV_PROBE_LITE") != "" || os.Getenv("A24_DOTENV_PROBE_EXPLICIT") != "" {
		t.Fatal("managed mode loaded a dotenv file")
	}
	// Control: the same files are picked up by a standalone Lite start.
	if err := loadDotenv(false, true); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("A24_DOTENV_PROBE_EXPLICIT") != "explicit" {
		t.Fatal("control: standalone Lite did not load ENV_FILE")
	}
}
