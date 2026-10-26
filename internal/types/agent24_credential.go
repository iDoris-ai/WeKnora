// Agent24 fork product patch A24-KB-18 (KB-C1-03 C3-b, WeKnora#41, CONTRACT-READONLY-v1 R1.6/R10.2).

package types

import "context"

// agent24TokenJTIContextKey carries the jti of a signed_token whose signature verified.
// Unexported: only WithAgent24TokenJTI and Agent24TokenJTIFromContext read it.
const agent24TokenJTIContextKey ContextKey = "Agent24TokenJTI"

func init() {
	// Request-scoped, like EmbedChannelContextKey: audit and the access log read it on the request
	// that authenticated the token. A caller-chosen jti must not ride along on a detach.
	// The clone table lives in context_clone.go, which this slice does not edit, so the decision is
	// recorded here. false is what that table uses for a value that describes one request.
	contextCloneAcrossDetach[agent24TokenJTIContextKey] = false
}

// WithAgent24TokenJTI binds a signed_token jti to ctx.
func WithAgent24TokenJTI(ctx context.Context, jti string) context.Context {
	return context.WithValue(ctx, agent24TokenJTIContextKey, jti)
}

// Agent24TokenJTIFromContext returns the bound jti. An empty string is not a jti.
func Agent24TokenJTIFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	jti, ok := ctx.Value(agent24TokenJTIContextKey).(string)
	if !ok || jti == "" {
		return "", false
	}
	return jti, true
}

// Agent24CredentialRejection is what middleware.Auth notes, in the gin context, when it refuses a credential
// whose signature (signed_token) or hash (tenant API key) verified but that has expired or been revoked. The
// Agent24 knowledge envelope and audit read it; nothing else does.
type Agent24CredentialRejection struct {
	Reason       string    // "credential_expired" or "credential_revoked"
	Principal    Principal // zero for a tenant API key rejection: the key is refused before any principal is resolved
	CredentialID string    // "jti:<jti>" or "key:<id>"
	TenantID     uint64
	JTI          string // the token's jti for a signed_token rejection, "" otherwise
}

// Agent24CredentialRejectionKey is the gin context key for an Agent24CredentialRejection.
// The types package does not import gin; middleware and handler get and set it themselves.
const Agent24CredentialRejectionKey = "agent24.credential_rejection"
