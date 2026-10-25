// Agent24 fork product patch A24-KB-17 (CONTRACT-READONLY-v1 r10 R3.11,
// §6 policy_denied, WeKnora#42).
//
// The integration-mode processing policy. It is read once per process.
// Standalone mode does not read it. An unreadable or illegal policy fails
// startup from ValidateAgent24IntegrationConfig; error text never includes
// the policy body.

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

const (
	// Agent24KnowledgePolicyEnv is the JSON text of the processing policy.
	Agent24KnowledgePolicyEnv = "AGENT24_KNOWLEDGE_POLICY"
	// Agent24KnowledgePolicyFileEnv is a path to that JSON. Setting both is an error.
	Agent24KnowledgePolicyFileEnv = "AGENT24_KNOWLEDGE_POLICY_FILE"

	agent24ProcessingPolicyVersionMax = 128
	agent24DefaultProcessingVersion   = "agent24-default-deny"
)

var errAgent24ProcessingPolicyInvalid = errors.New("agent24 knowledge policy is invalid")

// agent24ProcessingPolicy is the parsed allow-list. Endpoints are normalized.
type agent24ProcessingPolicy struct {
	Version      string
	Destinations []agent24ProcessingDestination
}

type agent24ProcessingDestination struct {
	Category string
	Endpoint string
	Model    string
}

type agent24ProcessingPolicyJSON struct {
	Version      string                             `json:"version"`
	Destinations []agent24ProcessingDestinationJSON `json:"destinations"`
}

type agent24ProcessingDestinationJSON struct {
	Category string `json:"category"`
	Endpoint string `json:"endpoint"`
	Model    string `json:"model"`
}

// agent24DefaultProcessingPolicy is the only definition of the default policy:
// an empty allow-list. Callers that have not set a policy deny every destination.
func agent24DefaultProcessingPolicy() *agent24ProcessingPolicy {
	return &agent24ProcessingPolicy{Version: agent24DefaultProcessingVersion}
}

var agent24ProcessingPolicyOnce = sync.OnceValues(agent24ReadProcessingPolicy)

// ResetAgent24ProcessingPolicyForTest makes the next read reload the environment.
// For tests; the swap is unsynchronized, so call it while no read is running.
func ResetAgent24ProcessingPolicyForTest() {
	agent24ProcessingPolicyOnce = sync.OnceValues(agent24ReadProcessingPolicy)
}

// agent24ProcessingPolicyFromEnv returns the process-wide policy.
func agent24ProcessingPolicyFromEnv() (*agent24ProcessingPolicy, error) {
	return agent24ProcessingPolicyOnce()
}

// Agent24ProcessingPolicyVersion is the loaded policy version, or empty when
// the policy could not be read. Only the startup log carries it; the wire field and the audit row wait for
// contract r11 (D-B, 2026-11-06).
func Agent24ProcessingPolicyVersion() string {
	p, err := agent24ProcessingPolicyFromEnv()
	if err != nil || p == nil {
		return ""
	}
	return p.Version
}

func agent24ReadProcessingPolicy() (*agent24ProcessingPolicy, error) {
	text := strings.TrimSpace(os.Getenv(Agent24KnowledgePolicyEnv))
	file := strings.TrimSpace(os.Getenv(Agent24KnowledgePolicyFileEnv))
	switch {
	case text != "" && file != "":
		return nil, errors.New("agent24 knowledge policy: set only one of " +
			Agent24KnowledgePolicyEnv + " and " + Agent24KnowledgePolicyFileEnv)
	case text == "" && file == "":
		return agent24DefaultProcessingPolicy(), nil
	case file != "":
		raw, err := os.ReadFile(file)
		if err != nil {
			return nil, errAgent24ProcessingPolicyInvalid
		}
		return agent24ParseProcessingPolicy(raw)
	default:
		return agent24ParseProcessingPolicy([]byte(text))
	}
}

func agent24ParseProcessingPolicy(raw []byte) (*agent24ProcessingPolicy, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var body agent24ProcessingPolicyJSON
	if err := dec.Decode(&body); err != nil {
		return nil, errAgent24ProcessingPolicyInvalid
	}
	// Exactly one value: a second Decode must hit EOF (More alone accepts a stray "]" or "}" after the object).
	if err := dec.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		return nil, errAgent24ProcessingPolicyInvalid
	}
	if body.Version == "" || utf8.RuneCountInString(body.Version) > agent24ProcessingPolicyVersionMax ||
		agent24PolicyHasControl(body.Version) {
		return nil, errAgent24ProcessingPolicyInvalid
	}
	out := &agent24ProcessingPolicy{Version: body.Version}
	for _, d := range body.Destinations {
		if d.Category != types.ProcessingEmbedding && d.Category != types.ProcessingModelDownload {
			return nil, errAgent24ProcessingPolicyInvalid
		}
		endpoint, err := types.NormalizeProcessingEndpoint(d.Endpoint)
		if err != nil {
			return nil, errAgent24ProcessingPolicyInvalid
		}
		out.Destinations = append(out.Destinations, agent24ProcessingDestination{
			Category: d.Category, Endpoint: endpoint, Model: d.Model,
		})
	}
	return out, nil
}

func agent24PolicyHasControl(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// Allow implements types.ProcessingGuard. A destination matches when the
// category and the normalized endpoint are equal and the rule's model is
// empty or equal. A refusal wraps ErrProcessingDenied and does not include
// the endpoint or the model; the log records the category, the endpoint
// length and the policy version.
func (p *agent24ProcessingPolicy) Allow(ctx context.Context, d types.ProcessingDestination) error {
	if p != nil {
		for _, allowed := range p.Destinations {
			if allowed.Category == d.Category && allowed.Endpoint == d.Endpoint &&
				(allowed.Model == "" || allowed.Model == d.Model) {
				return nil
			}
		}
	}
	version := ""
	if p != nil {
		version = p.Version
	}
	logger.Infof(ctx, "agent24 knowledge policy denied category=%s endpoint_len=%d version=%s",
		d.Category, len(d.Endpoint), version)
	return fmt.Errorf("%w", types.ErrProcessingDenied)
}
