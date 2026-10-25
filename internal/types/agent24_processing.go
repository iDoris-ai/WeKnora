// Agent24 fork product patch A24-KB-17 (CONTRACT-READONLY-v1 r10 R3.11,
// §6 policy_denied, WeKnora#42).
//
// Processing destinations and the context guard shared by the embedding
// decorators and the Ollama client. Both packages can import types; neither
// types nor its dependencies import them, so the guard lives here.

package types

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"unicode/utf8"
)

const (
	// ProcessingEmbedding is a query or document embedding call.
	ProcessingEmbedding = "embedding"
	// ProcessingModelDownload is an Ollama model pull.
	ProcessingModelDownload = "model_download"
)

// ProcessingDestination is where one processing call will actually connect.
// Endpoint is the normalized scheme://host:port form.
type ProcessingDestination struct {
	Category string
	Endpoint string
	Model    string
}

// ErrProcessingDenied is the sentinel a guard returns when a destination is
// not allowed. Denial errors wrap it and carry neither the endpoint nor the
// model name.
var ErrProcessingDenied = errors.New("processing destination not allowed by policy")

// NormalizeProcessingEndpoint returns scheme://host:port. The scheme and host
// are lowercased, the default port is filled in (http 80, https 443), and the
// path, query, userinfo and fragment are dropped. An IPv6 host keeps its
// brackets. A missing scheme or host is an error, and so is a host with any
// non-ASCII byte: Unicode case folding would match hosts that IDNA dials as
// different names (strings.ToLower("İ") is "i"; IDNA gives xn--i-9bb). An IPv6
// zone is refused too: Go matches the interface name exactly, so folding its
// case would merge two links. The error text does not include raw.
func NormalizeProcessingEndpoint(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("processing endpoint requires a scheme and host")
	}
	scheme := strings.ToLower(u.Scheme)
	host := u.Hostname()
	if host == "" {
		return "", errors.New("processing endpoint requires a scheme and host")
	}
	for i := 0; i < len(host); i++ {
		if host[i] >= utf8.RuneSelf {
			return "", errors.New("processing endpoint host must be ASCII")
		}
	}
	if strings.Contains(host, "%") {
		return "", errors.New("processing endpoint host must not carry an IPv6 zone")
	}
	host = strings.ToLower(host) // ASCII only, so this is plain ASCII case folding
	port := u.Port()
	if port == "" {
		switch scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		default:
			return "", errors.New("processing endpoint requires a scheme and host")
		}
	}
	// JoinHostPort brackets an IPv6 host; Hostname() has already removed them.
	return scheme + "://" + net.JoinHostPort(host, port), nil
}

// ProcessingGuard allows or refuses one actual processing destination.
type ProcessingGuard interface {
	Allow(ctx context.Context, d ProcessingDestination) error
}

type processingGuardKey struct{}

// WithProcessingGuard returns a context that carries g. A nil g clears nothing
// by itself; ProcessingGuardFromContext reports it as absent.
func WithProcessingGuard(ctx context.Context, g ProcessingGuard) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, processingGuardKey{}, g)
}

type processingApprovedKey struct{}

// WithProcessingApprovedEndpoint records the normalized endpoint a guard has
// just allowed. The HTTP layer compares each request it sends under a guard
// with it, so a URL rewritten after the decision (a configured path, a vendor
// hook, a redirect) cannot reach another origin.
func WithProcessingApprovedEndpoint(ctx context.Context, endpoint string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, processingApprovedKey{}, endpoint)
}

// ProcessingApprovedEndpoint returns the endpoint recorded on ctx, if any.
func ProcessingApprovedEndpoint(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	endpoint, ok := ctx.Value(processingApprovedKey{}).(string)
	return endpoint, ok && endpoint != ""
}

// CheckProcessingRequestURL is the send-time check for a request under a
// guard: without a guard it passes; with one, rawURL must normalize to the
// approved endpoint on ctx. A guarded request with no approval (it did not go
// through a guarded caller) is refused. The error wraps ErrProcessingDenied
// and carries no URL.
func CheckProcessingRequestURL(ctx context.Context, rawURL string) error {
	if ProcessingGuardFromContext(ctx) == nil {
		return nil
	}
	approved, ok := ProcessingApprovedEndpoint(ctx)
	if !ok {
		return fmt.Errorf("%w", ErrProcessingDenied)
	}
	got, err := NormalizeProcessingEndpoint(rawURL)
	if err != nil || got != approved {
		return fmt.Errorf("%w", ErrProcessingDenied)
	}
	return nil
}

// ProcessingGuardFromContext returns the guard on ctx, or nil when ctx is nil
// or carries no guard.
func ProcessingGuardFromContext(ctx context.Context) ProcessingGuard {
	if ctx == nil {
		return nil
	}
	g, _ := ctx.Value(processingGuardKey{}).(ProcessingGuard)
	return g
}
