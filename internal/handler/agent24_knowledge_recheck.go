// Agent24 fork product patch A24-KB-23 (KB-C1-03 C3-d, WeKnora#41, CONTRACT-READONLY-v1 R7.6): the
// credential is checked again before a search or read response is assembled.

package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/Tencent/WeKnora/internal/middleware"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// agent24CredentialRechecker checks the request's credential again before a Knowledge response is
// assembled (R7.6). ctx carries the request's deadline; header is the request's. It returns the context
// to resolve E again with (it carries the key's scope as it is now), the unauthorized reason when the
// credential no longer stands, or an infrastructure error the caller answers as internal.
type agent24CredentialRechecker interface {
	Recheck(ctx context.Context, header http.Header) (context.Context, string, error)
}

// errAgent24RecheckUnwired: the handler was built without the services the recheck needs. The request is
// refused as internal rather than answered unchecked.
var errAgent24RecheckUnwired = errors.New("agent24 credential recheck is not wired")

type agent24DefaultRechecker struct {
	tenants     interfaces.TenantService
	keys        interfaces.TenantAPIKeyService
	revocations interfaces.Agent24CredentialRevocationStore // nil outside integration mode: no jti list
}

// newAgent24Rechecker consults the jti list only in integration mode: the table exists only there
// (A24-KB-22), and only there does the credential gate require it.
func newAgent24Rechecker(
	tenants interfaces.TenantService, keys interfaces.TenantAPIKeyService,
	revocations interfaces.Agent24CredentialRevocationStore,
) agent24DefaultRechecker {
	if mode, err := Agent24KnowledgeModeFromEnv(); err != nil || mode != Agent24KnowledgeModeIntegration {
		revocations = nil
	}
	return agent24DefaultRechecker{tenants: tenants, keys: keys, revocations: revocations}
}

// Recheck covers X-API-Key credentials: tenant keys with the tenant's principal mode, and platform keys. A request
// without an API key scope (a session JWT) is passed with its context unchanged; E is still resolved
// again by the caller. The key's grant is checked as the route's policy does (retrieve, or full access):
// a key that lost it is credential_invalid, as Auth's 403 is under the integration envelope.
func (r agent24DefaultRechecker) Recheck(ctx context.Context, header http.Header) (context.Context, string, error) {
	scope, ok := types.TenantAPIKeyScopeFromContext(ctx)
	if !ok {
		return ctx, "", nil
	}
	if r.tenants == nil || r.keys == nil {
		return ctx, "", errAgent24RecheckUnwired
	}
	entry := middleware.Agent24RecheckEntry{
		KeyID:    scope.KeyID,
		Platform: scope.IsPlatform(),
		TenantID: types.CallerFromContext(ctx).TenantID,
	}
	if t, _ := ctx.Value(types.TenantInfoContextKey).(*types.Tenant); t != nil {
		entry.Mode = middleware.Agent24PrincipalMode(t)
	}
	if p, ok := types.PrincipalFromContext(ctx); ok {
		entry.Principal = p
	}
	if jti, ok := types.Agent24TokenJTIFromContext(ctx); ok {
		entry.JTI = jti
	}
	res, err := middleware.Agent24RecheckAPIKey(ctx, r.tenants, r.keys, r.revocations, header, entry)
	if err != nil {
		return ctx, "", err
	}
	if res.Reason != "" {
		return ctx, res.Reason, nil
	}
	fresh := res.Scope
	fresh.FullAccess = fresh.FullAccess && !fresh.IsPlatform()
	if !fresh.FullAccess && !fresh.HasCapability(types.APIKeyCapabilityRetrieve) {
		return ctx, "credential_invalid", nil
	}
	return types.WithTenantAPIKeyScope(ctx, fresh), "", nil
}
