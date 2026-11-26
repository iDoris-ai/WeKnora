// Agent24 fork product patch A24-KB-60 (KB-LITE-01a), A24-KB-62 (KB-LITE-02a, G2): persist managed-host secrets.

package agent24managed

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	managedSecretsFilename = "managed-secrets.json"
	managedSecretsTempGlob = managedSecretsFilename + ".tmp-"
)

type managedSecretsFile struct {
	Version      int    `json:"version"`
	JWTSecret    string `json:"jwt_secret"`
	SystemAESKey string `json:"system_aes_key"`
}

// PrepareSecrets creates or reads <stateDir>/managed-secrets.json and copies
// those values into JWT_SECRET and SYSTEM_AES_KEY.
// An existing file is never rewritten. A non-empty environment value that
// differs from the file is an error, and the error text does not include
// any secret. The caller holds the instance lock (AcquireInstanceLock).
//
// A new file is checked against the environment first, then written to a
// temp file, fsynced and linked into place, so a crash leaves either no file
// or a complete one, never a partial one.
func PrepareSecrets(stateDir string) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return fmt.Errorf("create managed state dir: %w", err)
	}
	// MkdirAll leaves an existing directory's mode unchanged. The state
	// directory must be owner-only even when the caller created it first.
	if err := os.Chmod(stateDir, 0o700); err != nil {
		return fmt.Errorf("create managed state dir: %w", err)
	}
	removeStaleSecretTemps(stateDir)
	path := filepath.Join(stateDir, managedSecretsFilename)
	if _, err := os.Lstat(path); err == nil {
		return useExistingSecrets(path)
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("read managed secrets: %w", err)
	}
	generated, err := newManagedSecrets()
	if err != nil {
		return err
	}
	if err := checkManagedEnv(generated); err != nil {
		return err
	}
	payload, err := json.Marshal(generated)
	if err != nil {
		return err
	}
	if err := writeSecretsAtomically(stateDir, path, payload); err != nil {
		if errors.Is(err, os.ErrExist) {
			return useExistingSecrets(path)
		}
		return err
	}
	return applyManagedEnv(generated)
}

// removeStaleSecretTemps drops temp files a killed start left behind. Safe because the caller holds
// the instance lock, so no other start is writing one.
func removeStaleSecretTemps(stateDir string) {
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), managedSecretsTempGlob) {
			_ = os.Remove(filepath.Join(stateDir, e.Name()))
		}
	}
}

func writeSecretsAtomically(stateDir, path string, payload []byte) error {
	tmp, err := os.CreateTemp(stateDir, managedSecretsTempGlob)
	if err != nil {
		return fmt.Errorf("create managed secrets: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	werr := tmp.Chmod(0o600)
	if werr == nil {
		_, werr = tmp.Write(payload)
	}
	if werr == nil {
		werr = tmp.Sync()
	}
	cerr := tmp.Close()
	if werr != nil {
		return fmt.Errorf("write managed secrets: %w", werr)
	}
	if cerr != nil {
		return fmt.Errorf("write managed secrets: %w", cerr)
	}
	// Link, not rename: it never replaces a file that already exists.
	if err := os.Link(tmpPath, path); err != nil {
		if os.IsExist(err) {
			return os.ErrExist
		}
		return fmt.Errorf("write managed secrets: %w", err)
	}
	dir, err := os.Open(stateDir)
	if err != nil {
		return fmt.Errorf("sync managed state dir: %w", err)
	}
	serr := dir.Sync()
	_ = dir.Close()
	if serr != nil {
		return fmt.Errorf("sync managed state dir: %w", serr)
	}
	return nil
}

func newManagedSecrets() (managedSecretsFile, error) {
	jwtSecret, err := randomRawURL(32)
	if err != nil {
		return managedSecretsFile{}, err
	}
	aesKey, err := randomRawURL(24)
	if err != nil {
		return managedSecretsFile{}, err
	}
	return managedSecretsFile{Version: 1, JWTSecret: jwtSecret, SystemAESKey: aesKey}, nil
}

func randomRawURL(nBytes int) (string, error) {
	buf := make([]byte, nBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func useExistingSecrets(path string) error {
	parsed, err := loadManagedSecrets(path)
	if err != nil {
		return err
	}
	return applyManagedEnv(parsed)
}

func loadManagedSecrets(path string) (managedSecretsFile, error) {
	info, err := os.Stat(path)
	if err != nil {
		return managedSecretsFile{}, err
	}
	// Group or other permission bits would expose the host secrets.
	// A looser file is refused and left in place so encrypted rows stay readable.
	if info.Mode().Perm()&0o077 != 0 {
		return managedSecretsFile{}, fmt.Errorf("managed secrets file %s permissions are too open", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return managedSecretsFile{}, err
	}
	var parsed managedSecretsFile
	if err := json.Unmarshal(data, &parsed); err != nil {
		return managedSecretsFile{}, fmt.Errorf("invalid managed secrets file %s", path)
	}
	if parsed.Version != 1 || len(parsed.JWTSecret) < 32 || len(parsed.SystemAESKey) != 32 {
		return managedSecretsFile{}, fmt.Errorf("invalid managed secrets file %s", path)
	}
	return parsed, nil
}

func checkManagedEnv(s managedSecretsFile) error {
	if err := ensureEnvMatches("JWT_SECRET", s.JWTSecret); err != nil {
		return err
	}
	return ensureEnvMatches("SYSTEM_AES_KEY", s.SystemAESKey)
}

func applyManagedEnv(s managedSecretsFile) error {
	if err := checkManagedEnv(s); err != nil {
		return err
	}
	for _, kv := range [][2]string{
		{"JWT_SECRET", s.JWTSecret}, {"SYSTEM_AES_KEY", s.SystemAESKey},
	} {
		if err := os.Setenv(kv[0], kv[1]); err != nil {
			return err
		}
	}
	if err := os.Setenv("DISABLE_REGISTRATION", "true"); err != nil {
		return err
	}
	// OIDC first login provisions an account without consulting the registration mode
	// (service.LoginWithOIDC -> provisionOIDCUser), so a managed host turns OIDC off.
	// The env override wins over config.yaml (config.applyOIDCEnvOverrides).
	return os.Setenv("OIDC_AUTH_ENABLE", "false")
}

func ensureEnvMatches(key, want string) error {
	got := os.Getenv(key)
	if got == "" || got == want {
		return nil
	}
	return fmt.Errorf("%s does not match the managed state directory", key)
}
