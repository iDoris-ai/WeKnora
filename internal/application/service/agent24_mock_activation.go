// Agent24 fork product patch A24-KB-52 (KB-C4-02a mock, Loop 7 first round F2a).
//
// LoadAgent24MockActivations reads an operator-supplied JSON file that maps an activation code's SHA-256
// to a tenant and template KB. An empty path turns the mock off. The file must be a regular file that group
// and others cannot access. Errors never include the code or its hash.

package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

const agent24MockActivationMaxBytes = 64 * 1024

// Agent24MockActivation is the tenant and template a matching activation code unlocks.
type Agent24MockActivation struct {
	TenantID      uint64
	TemplateKBID  string
	ConfigVersion string
}

// Agent24MockActivations is the loaded mock table, keyed by lowercase SHA-256 hex of the code.
type Agent24MockActivations struct {
	byHash map[string]Agent24MockActivation
}

type agent24MockActivationFile struct {
	Activations []agent24MockActivationEntry `json:"activations"`
}

type agent24MockActivationEntry struct {
	CodeSHA256    string `json:"code_sha256"`
	TenantID      uint64 `json:"tenant_id"`
	TemplateKBID  string `json:"template_kb_id"`
	ConfigVersion string `json:"config_version"`
}

// LoadAgent24MockActivations loads the mock table. path empty returns (nil, nil).
func LoadAgent24MockActivations(path string) (*Agent24MockActivations, error) {
	if path == "" {
		return nil, nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("agent24 mock activations: %s is not a regular file", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("agent24 mock activations: %s must not be group or world accessible", path)
	}
	if info.Size() > agent24MockActivationMaxBytes {
		return nil, fmt.Errorf("agent24 mock activations: %s is larger than 64 KiB", path)
	}
	f, err := os.Open(path) //nolint:gosec // operator-supplied path; Lstat already rejected a symlink
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	dec := json.NewDecoder(io.LimitReader(f, agent24MockActivationMaxBytes))
	dec.DisallowUnknownFields()
	var body agent24MockActivationFile
	if err := dec.Decode(&body); err != nil {
		return nil, fmt.Errorf("agent24 mock activations: %w", err)
	}
	if len(body.Activations) == 0 {
		return nil, errors.New("agent24 mock activations: activations must not be empty")
	}
	byHash := make(map[string]Agent24MockActivation, len(body.Activations))
	for _, e := range body.Activations {
		if !agent24MockCodeSHA256OK(e.CodeSHA256) {
			return nil, errors.New("agent24 mock activations: invalid code_sha256")
		}
		if e.TenantID == 0 {
			return nil, errors.New("agent24 mock activations: tenant_id must be greater than 0")
		}
		if e.TemplateKBID == "" {
			return nil, errors.New("agent24 mock activations: template_kb_id must not be empty")
		}
		if e.ConfigVersion == "" {
			return nil, errors.New("agent24 mock activations: config_version must not be empty")
		}
		if _, dup := byHash[e.CodeSHA256]; dup {
			return nil, errors.New("agent24 mock activations: duplicate code_sha256")
		}
		byHash[e.CodeSHA256] = Agent24MockActivation{
			TenantID: e.TenantID, TemplateKBID: e.TemplateKBID, ConfigVersion: e.ConfigVersion,
		}
	}
	return &Agent24MockActivations{byHash: byHash}, nil
}

// Lookup returns the activation for code. A nil receiver or empty code misses. The code and its hash are not logged.
func (a *Agent24MockActivations) Lookup(code string) (Agent24MockActivation, bool) {
	if a == nil || code == "" {
		return Agent24MockActivation{}, false
	}
	sum := sha256.Sum256([]byte(code))
	got, ok := a.byHash[hex.EncodeToString(sum[:])]
	return got, ok
}

func agent24MockCodeSHA256OK(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= '0' && c <= '9' {
			continue
		}
		if c >= 'a' && c <= 'f' {
			continue
		}
		return false
	}
	return true
}
