// Agent24 fork product patch A24-KB-17, A24-KB-27, A24-KB-28 (CONTRACT-READONLY-v1 r10
// R3.11, R6.6, §6 policy_denied, WeKnora#42).
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
	// agent24ProcessingBudgetMax is the largest accepted budget field. Anything
	// larger is an illegal policy: the counter comparison must not overflow.
	agent24ProcessingBudgetMax int64 = 1<<31 - 1
	// agent24PolicyRetryAfterMS is how long a caller should wait after an
	// in-flight rejection. The slot is released when the external call returns,
	// and there is no better estimate.
	agent24PolicyRetryAfterMS = 1000
)

var errAgent24ProcessingPolicyInvalid = errors.New("agent24 knowledge policy is invalid")

// agent24ProcessingPolicy is the parsed allow-list. Endpoints are normalized.
// A configured policy has a budget and an in-flight semaphore. The default
// policy (nothing configured) has a zero budget and a nil inflight.
type agent24ProcessingPolicy struct {
	Version      string
	Destinations []agent24ProcessingDestination
	Budget       agent24ProcessingBudget
	inflight     chan struct{}
}

// agent24ProcessingBudget is one configured policy's limits. The three fields
// are integers; zero calls or bytes means send nothing.
type agent24ProcessingBudget struct {
	MaxExternalCalls    int
	MaxInputBytes       int
	MaxInflightExternal int
}

type agent24ProcessingDestination struct {
	Category string
	Endpoint string
	Model    string
}

type agent24ProcessingPolicyJSON struct {
	Version      string                             `json:"version"`
	Destinations []agent24ProcessingDestinationJSON `json:"destinations"`
	Budget       *agent24ProcessingBudgetJSON       `json:"budget"`
}

// Pointers distinguish a missing field from zero. The budget object itself is
// a pointer for the same reason: a configured policy must name it.
type agent24ProcessingBudgetJSON struct {
	MaxExternalCalls    *int64 `json:"max_external_calls"`
	MaxInputBytes       *int64 `json:"max_input_bytes"`
	MaxInflightExternal *int64 `json:"max_inflight_external"`
}

type agent24ProcessingDestinationJSON struct {
	Category string `json:"category"`
	Endpoint string `json:"endpoint"`
	Model    string `json:"model"`
}

// agent24DefaultProcessingPolicy is the only definition of the default policy:
// an empty allow-list and no budget. Callers that have not set a policy deny
// every destination. inflight stays nil.
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
	budget, err := agent24ParseProcessingBudget(body.Budget)
	if err != nil {
		return nil, err
	}
	out := &agent24ProcessingPolicy{Version: body.Version, Budget: budget}
	out.inflight = make(chan struct{}, budget.MaxInflightExternal)
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

// agent24ParseProcessingBudget requires the object and all three fields. Zero
// calls or bytes is legal (send nothing). The in-flight limit is at least one.
func agent24ParseProcessingBudget(b *agent24ProcessingBudgetJSON) (agent24ProcessingBudget, error) {
	if b == nil || b.MaxExternalCalls == nil || b.MaxInputBytes == nil || b.MaxInflightExternal == nil {
		return agent24ProcessingBudget{}, errAgent24ProcessingPolicyInvalid
	}
	calls, nbytes, inflight := *b.MaxExternalCalls, *b.MaxInputBytes, *b.MaxInflightExternal
	if calls < 0 || calls > agent24ProcessingBudgetMax || nbytes < 0 || nbytes > agent24ProcessingBudgetMax ||
		inflight < 1 || inflight > agent24ProcessingBudgetMax {
		return agent24ProcessingBudget{}, errAgent24ProcessingPolicyInvalid
	}
	return agent24ProcessingBudget{
		MaxExternalCalls: int(calls), MaxInputBytes: int(nbytes), MaxInflightExternal: int(inflight),
	}, nil
}

// agent24RequestGuard is one request's view of the policy: Allow checks the
// destination, Charge and Acquire spend this request's budget and the
// process-wide in-flight limit.
type agent24RequestGuard struct {
	policy *agent24ProcessingPolicy
	mu     sync.Mutex
	calls  int64
	bytes  int64
}

func (p *agent24ProcessingPolicy) forRequest() *agent24RequestGuard {
	return &agent24RequestGuard{policy: p}
}

func (g *agent24RequestGuard) Allow(ctx context.Context, d types.ProcessingDestination) error {
	if g == nil {
		return fmt.Errorf("%w", types.ErrProcessingDenied)
	}
	return g.policy.Allow(ctx, d)
}

// Charge checks both limits and spends them together under one lock: a charge
// that would cross either limit is refused and spends nothing, so concurrent
// charges behave as some serial order of them. A nil policy, or the default
// policy whose inflight is nil, has no budget: every charge is a refusal. The
// log names the dimension and the policy version, not the destination or the
// input.
func (g *agent24RequestGuard) Charge(ctx context.Context, calls, inputBytes int) error {
	if g == nil || g.policy == nil || g.policy.inflight == nil {
		logger.Infof(ctx, "agent24 knowledge policy has no budget; charge refused")
		return fmt.Errorf("%w", types.ErrProcessingDenied)
	}
	dimension := ""
	g.mu.Lock()
	switch {
	case calls < 0 || inputBytes < 0:
		dimension = "negative"
	case g.calls+int64(calls) > int64(g.policy.Budget.MaxExternalCalls):
		dimension = "calls"
	case g.bytes+int64(inputBytes) > int64(g.policy.Budget.MaxInputBytes):
		dimension = "input_bytes"
	default:
		g.calls += int64(calls)
		g.bytes += int64(inputBytes)
	}
	g.mu.Unlock()
	if dimension == "" {
		return nil
	}
	logger.Infof(ctx, "agent24 knowledge policy budget exhausted dimension=%s version=%s",
		dimension, g.policy.Version)
	return fmt.Errorf("%w", types.ErrProcessingDenied)
}

// Acquire takes one process-wide slot without waiting. A nil inflight (the
// default policy) has no budget and is a refusal, not a retryable overload;
// Allow has already refused, so a caller does not reach it. release gives the
// slot back once.
func (g *agent24RequestGuard) Acquire(ctx context.Context) (func(), error) {
	if g == nil || g.policy == nil || g.policy.inflight == nil {
		return nil, fmt.Errorf("%w", types.ErrProcessingDenied)
	}
	select {
	case g.policy.inflight <- struct{}{}:
		var once sync.Once
		inflight := g.policy.inflight
		return func() {
			once.Do(func() { <-inflight })
		}, nil
	default:
		logger.Infof(ctx, "agent24 knowledge policy inflight limit reached version=%s", g.policy.Version)
		return nil, fmt.Errorf("%w", types.ErrProcessingOverloaded)
	}
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
