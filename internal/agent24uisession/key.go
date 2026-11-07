// Agent24 fork product patch A24-KB-31 (KB-C2-01a, WeKnora#133, CONTRACT-UI-BRIDGE-v1 U2–U3).

// Package agent24uisession is the UI session key, one-time ticket and session JWT library (A24-KB-31).
package agent24uisession

import (
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"os"
	"strings"
)

const (
	// KeyEnv is the standard-base64 UI session signing key (U3.1). Unset leaves UI sessions off.
	KeyEnv = "AGENT24_KNOWLEDGE_UI_SESSION_KEY"
	// IssuerEnv is the optional session JWT issuer. Blank means DefaultIssuer.
	IssuerEnv = "AGENT24_KNOWLEDGE_UI_ISSUER"
	// DefaultIssuer is the issuer used when IssuerEnv is unset or blank.
	DefaultIssuer = "weknora-agent24"
)

// agent24KnowledgeTagKeyEnv is handler.Agent24KnowledgeTagKeyEnv. This package does not import handler.
const agent24KnowledgeTagKeyEnv = "AGENT24_KNOWLEDGE_TAG_KEY"

var (
	errAgent24UISessionKeyInvalid = errors.New(KeyEnv + " must be standard base64 of at least 32 bytes")
	errAgent24UISessionKeyShared  = errors.New(KeyEnv + " must not equal AGENT24_KNOWLEDGE_TAG_KEY")
)

// KeyFromEnv reads the UI session key. Unset or blank is (nil, false, nil): UI sessions stay off, and an
// integration process still starts. A value that is set but is not standard base64, or that decodes to fewer
// than 32 bytes, is an error with configured true. The same bytes as AGENT24_KNOWLEDGE_TAG_KEY are an error
// (R1.2). A tag key that is unset or not valid standard base64 is not compared. On error the key bytes are
// not returned.
func KeyFromEnv() (key []byte, configured bool, err error) {
	raw := strings.TrimSpace(os.Getenv(KeyEnv))
	if raw == "" {
		return nil, false, nil
	}
	decoded, decErr := base64.StdEncoding.DecodeString(raw)
	if decErr != nil || len(decoded) < minKeyBytes {
		return nil, true, errAgent24UISessionKeyInvalid
	}
	if agent24UISessionKeyMatchesTag(decoded) {
		return nil, true, errAgent24UISessionKeyShared
	}
	return decoded, true, nil
}

// IssuerFromEnv returns the session JWT issuer. Blank or unset means DefaultIssuer.
func IssuerFromEnv() string {
	if v := strings.TrimSpace(os.Getenv(IssuerEnv)); v != "" {
		return v
	}
	return DefaultIssuer
}

func agent24UISessionKeyMatchesTag(key []byte) bool {
	raw := strings.TrimSpace(os.Getenv(agent24KnowledgeTagKeyEnv))
	if raw == "" {
		return false
	}
	tag, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(key, tag) == 1
}
