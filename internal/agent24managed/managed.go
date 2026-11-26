// Agent24 fork product patch A24-KB-60 (KB-LITE-01a): managed-mode flags, loopback listen, and model probe.

// Package agent24managed is the headless managed host for WeKnora Lite.
package agent24managed

import (
	"context"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

const (
	modelStateReady       = "ready"
	modelStateMissing     = "missing"
	modelStateUnreachable = "unreachable"
	// modelStateConfigured: an active built-in chat row exists. Chat is not called, so it is not "ready".
	modelStateConfigured = "configured"
	defaultProbeTimeout  = 15 * time.Second
	embedProbeText       = "ping"
)

// Options is the managed-host command line. The zero value is standalone.
type Options struct {
	Managed     bool
	StateDir    string // required when Managed
	Port        int    // default 0 = OS picks
	HandshakeFD int    // required: write end of the host's handshake pipe
	LifelineFD  int    // required: read end of a pipe the host holds open; EOF means exit
}

// ParseArgs reads managed-mode flags. Without --managed it returns the zero
// Options and ignores every argument, so standalone flag handling stays unchanged.
func ParseArgs(args []string) (Options, error) {
	if !hasExactArg(args, "--managed") {
		return Options{}, nil
	}
	opts := Options{Managed: true}
	var stateDir string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--managed" || arg == "--version" {
			continue
		}
		name, inline, hasEq := splitLongFlag(arg)
		if name == "" {
			continue
		}
		switch name {
		case "--state-dir", "--port", "--handshake-fd", "--lifeline-fd":
			value, next, err := takeFlagValue(args, i, name, inline, hasEq)
			if err != nil {
				return Options{}, err
			}
			i = next
			if err := applyManagedFlag(&opts, &stateDir, name, value); err != nil {
				return Options{}, err
			}
		default:
			return Options{}, fmt.Errorf("unknown argument %s", arg)
		}
	}
	if stateDir == "" {
		return Options{}, fmt.Errorf("--state-dir is required in managed mode")
	}
	// G1: the host passes both fds explicitly; nothing is assumed about fd 3.
	if opts.HandshakeFD == 0 {
		return Options{}, fmt.Errorf("--handshake-fd is required in managed mode")
	}
	if opts.LifelineFD == 0 {
		return Options{}, fmt.Errorf("--lifeline-fd is required in managed mode")
	}
	if opts.HandshakeFD == opts.LifelineFD {
		return Options{}, fmt.Errorf("--lifeline-fd must differ from --handshake-fd")
	}
	abs, err := filepath.Abs(stateDir)
	if err != nil {
		return Options{}, fmt.Errorf("state dir: %w", err)
	}
	opts.StateDir = abs
	return opts, nil
}

func hasExactArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func splitLongFlag(arg string) (name, value string, hasEq bool) {
	if !strings.HasPrefix(arg, "--") {
		return "", "", false
	}
	if i := strings.IndexByte(arg, '='); i >= 0 {
		return arg[:i], arg[i+1:], true
	}
	return arg, "", false
}

func takeFlagValue(args []string, i int, name, inline string, hasEq bool) (string, int, error) {
	if hasEq {
		if inline == "" {
			return "", i, fmt.Errorf("%s requires a value", name)
		}
		return inline, i, nil
	}
	if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
		return "", i, fmt.Errorf("%s requires a value", name)
	}
	return args[i+1], i + 1, nil
}

func applyManagedFlag(opts *Options, stateDir *string, name, value string) error {
	switch name {
	case "--state-dir":
		*stateDir = value
	case "--port":
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 || n > 65535 {
			return fmt.Errorf("invalid --port %q", value)
		}
		opts.Port = n
	case "--handshake-fd":
		n, err := strconv.Atoi(value)
		if err != nil || n < 3 {
			return fmt.Errorf("invalid --handshake-fd %q", value)
		}
		opts.HandshakeFD = n
	case "--lifeline-fd":
		n, err := strconv.Atoi(value)
		if err != nil || n < 3 {
			return fmt.Errorf("invalid --lifeline-fd %q", value)
		}
		opts.LifelineFD = n
	default:
		return fmt.Errorf("unknown argument %s", name)
	}
	return nil
}

// Listen binds 127.0.0.1 only. Port 0 lets the OS pick a free port.
// The configured server host is not consulted.
func Listen(port int) (net.Listener, error) {
	return net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
}

// ModelProbe reports whether the built-in embedding and chat models can be used.
type ModelProbe interface {
	Probe(ctx context.Context) ModelsReady
}

// BuiltinModelProbe reads built-in rows from the models table and, for an
// active embedding model, calls the embedder once.
type BuiltinModelProbe struct {
	DB      *gorm.DB
	Models  interfaces.ModelService
	Timeout time.Duration // default 15s
}

// Probe returns one models handshake record. Embedding is ready only when a
// live built-in row is active and Embed("ping") returns a non-empty vector
// before Timeout. Chat is only "configured" when a live built-in KnowledgeQA row is
// active: it is not called, so a stopped chat backend is not detected here.
func (p *BuiltinModelProbe) Probe(ctx context.Context) ModelsReady {
	out := ModelsReady{
		Type:      "models",
		Protocol:  HandshakeProtocol,
		Embedding: ModelState{State: modelStateMissing},
		Chat:      ModelState{State: modelStateMissing},
	}
	if m, state, found := p.lookupBuiltin(ctx, types.ModelTypeEmbedding); found {
		out.Embedding = p.probeEmbedding(ctx, m)
	} else {
		out.Embedding = state
	}
	if m, state, found := p.lookupBuiltin(ctx, types.ModelTypeKnowledgeQA); found {
		out.Chat = chatModelState(m)
	} else {
		out.Chat = state
	}
	return out
}

func (p *BuiltinModelProbe) lookupBuiltin(
	ctx context.Context, kind types.ModelType,
) (types.Model, ModelState, bool) {
	var m types.Model
	err := p.DB.WithContext(ctx).
		Where("is_builtin = ? AND type = ?", true, kind).
		Order("created_at ASC").
		First(&m).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return types.Model{}, ModelState{State: modelStateMissing}, false
	}
	if err != nil {
		logger.Warnf(ctx, "managed model probe query failed: %v", err)
		return types.Model{}, ModelState{State: modelStateUnreachable}, false
	}
	return m, ModelState{}, true
}

func (p *BuiltinModelProbe) probeEmbedding(ctx context.Context, m types.Model) ModelState {
	state := ModelState{State: modelStateUnreachable, Model: m.Name}
	if m.Status != types.ModelStatusActive {
		return state
	}
	if p.Models == nil {
		logger.Warnf(ctx, "managed embedding probe has no model service")
		return state
	}
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = defaultProbeTimeout
	}
	ectx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	emb, err := p.Models.GetEmbeddingModelForTenant(ectx, m.ID, m.TenantID)
	if err != nil {
		logger.Warnf(ctx, "managed embedding probe failed to open model: %v", err)
		return state
	}
	if emb == nil {
		logger.Warnf(ctx, "managed embedding probe returned no embedder")
		return state
	}
	vec, err := emb.Embed(ectx, embedProbeText)
	if err != nil {
		logger.Warnf(ctx, "managed embedding probe failed: %v", err)
		return state
	}
	if len(vec) == 0 {
		logger.Warnf(ctx, "managed embedding probe returned an empty vector")
		return state
	}
	state.State = modelStateReady
	return state
}

func chatModelState(m types.Model) ModelState {
	state := ModelState{State: modelStateUnreachable, Model: m.Name}
	if m.Status == types.ModelStatusActive {
		state.State = modelStateConfigured
	}
	return state
}
