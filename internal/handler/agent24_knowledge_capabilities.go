// Agent24 fork product patch A24-KB-01 (CONTRACT-READONLY-v1 §9).
//
// Declares the Agent24 knowledge integration capability under
// data.integration.agent24_knowledge of GET /system/capabilities. New code
// lives in fork-only agent24_* files; the upstream files touched are the
// field and return value in deployment_capabilities.go and the first lines of
// cmd/server/main.go (--version). The declaration states what is supported,
// never health or readiness.

package handler

import (
	"strings"
	"unicode/utf8"
)

const (
	agent24KnowledgeProtocolVersion = 1
	// Maximum server_version length in bytes (CONTRACT-READONLY-v1 §9 R9.2).
	agent24ServerVersionMaxBytes = 128
)

// Declared limits only: they are not enforced until KB-C1-01c implements the
// search/read endpoints. While supported_operations is empty, clients do not
// connect, so these values cannot be relied upon yet.
const (
	agent24KnowledgeMaxResults      = 50
	agent24KnowledgeMaxConcurrency  = 4
	agent24KnowledgeMaxDeadlineMS   = 30000
	agent24KnowledgeMaxReadBytes    = 262144
	agent24KnowledgeMaxPageLimit    = 100
	agent24KnowledgeMaxSnippetBytes = 2048
)

// Agent24KnowledgeLimits holds exactly the six positive-integer limits of R9.2.
type Agent24KnowledgeLimits struct {
	MaxResults      int `json:"max_results"`
	MaxConcurrency  int `json:"max_concurrency"`
	MaxDeadlineMS   int `json:"max_deadline_ms"`
	MaxReadBytes    int `json:"max_read_bytes"`
	MaxPageLimit    int `json:"max_page_limit"`
	MaxSnippetBytes int `json:"max_snippet_bytes"`
}

// Agent24KnowledgeCapability holds exactly the five keys of R9.2.
type Agent24KnowledgeCapability struct {
	ProtocolVersions    []int                  `json:"protocol_versions"`
	Mode                string                 `json:"mode"`
	SupportedOperations []string               `json:"supported_operations"`
	ServerVersion       string                 `json:"server_version"`
	Limits              Agent24KnowledgeLimits `json:"limits"`
}

// DeploymentIntegration is data.integration of GET /system/capabilities.
type DeploymentIntegration struct {
	Agent24Knowledge Agent24KnowledgeCapability `json:"agent24_knowledge"`
}

// BuildAgent24KnowledgeCapability returns the current truthful declaration.
func BuildAgent24KnowledgeCapability() Agent24KnowledgeCapability {
	// The mode is validated at startup (router), so an error cannot occur here;
	// fall back to standalone anyway.
	mode, err := Agent24KnowledgeModeFromEnv()
	if err != nil {
		mode = Agent24KnowledgeModeStandalone
	}
	return Agent24KnowledgeCapability{
		ProtocolVersions: []int{agent24KnowledgeProtocolVersion},
		Mode:             mode,
		// search/read integration endpoints (KB-C1-01c) are not implemented.
		// Non-nil so it serializes as [] rather than null.
		SupportedOperations: []string{},
		ServerVersion:       agent24ServerVersion(Version, CommitID),
		Limits: Agent24KnowledgeLimits{
			MaxResults:      agent24KnowledgeMaxResults,
			MaxConcurrency:  agent24KnowledgeMaxConcurrency,
			MaxDeadlineMS:   agent24KnowledgeMaxDeadlineMS,
			MaxReadBytes:    agent24KnowledgeMaxReadBytes,
			MaxPageLimit:    agent24KnowledgeMaxPageLimit,
			MaxSnippetBytes: agent24KnowledgeMaxSnippetBytes,
		},
	}
}

// agent24ServerVersion formats the ldflags-injected version as
// "<version>+<commit>" (commit omitted when empty or "unknown"), truncated to
// at most 128 bytes on a UTF-8 boundary. An empty version yields "unknown".
func agent24ServerVersion(version, commit string) string {
	version = strings.TrimSpace(version)
	commit = strings.TrimSpace(commit)
	if version == "" {
		return "unknown"
	}
	s := version
	if commit != "" && commit != "unknown" {
		s = version + "+" + commit
	}
	s = strings.ToValidUTF8(s, "")
	if len(s) > agent24ServerVersionMaxBytes {
		cut := agent24ServerVersionMaxBytes
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = s[:cut]
	}
	if s == "" {
		return "unknown"
	}
	return s
}
