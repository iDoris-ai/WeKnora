// Agent24 fork product patch A24-KB-60 (KB-LITE-01a): persist managed-host JWT and AES secrets.

package agent24managed

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

const managedSecretsFilename = "managed-secrets.json"

type managedSecretsFile struct {
	Version      int    `json:"version"`
	JWTSecret    string `json:"jwt_secret"`
	SystemAESKey string `json:"system_aes_key"`
}

// PrepareSecrets creates or reads <stateDir>/managed-secrets.json and copies
// those values into JWT_SECRET and SYSTEM_AES_KEY. An existing file is never
// rewritten. A non-empty environment value that differs from the file is an
// error, and the error text does not include either secret.
func PrepareSecrets(stateDir string) error {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return fmt.Errorf("create managed state dir: %w", err)
	}
	// MkdirAll leaves an existing directory's mode unchanged. The state
	// directory must be owner-only even when the caller created it first.
	if err := os.Chmod(stateDir, 0o700); err != nil {
		return fmt.Errorf("create managed state dir: %w", err)
	}
	path := filepath.Join(stateDir, managedSecretsFilename)
	generated, err := newManagedSecrets()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(generated)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return useExistingSecrets(path)
		}
		return fmt.Errorf("create managed secrets: %w", err)
	}
	_, werr := f.Write(payload)
	cerr := f.Close()
	if werr != nil {
		return fmt.Errorf("write managed secrets: %w", werr)
	}
	if cerr != nil {
		return fmt.Errorf("write managed secrets: %w", cerr)
	}
	return applyManagedEnv(generated)
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

func applyManagedEnv(s managedSecretsFile) error {
	if err := ensureEnvMatches("JWT_SECRET", s.JWTSecret); err != nil {
		return err
	}
	if err := ensureEnvMatches("SYSTEM_AES_KEY", s.SystemAESKey); err != nil {
		return err
	}
	if err := os.Setenv("JWT_SECRET", s.JWTSecret); err != nil {
		return err
	}
	if err := os.Setenv("SYSTEM_AES_KEY", s.SystemAESKey); err != nil {
		return err
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
