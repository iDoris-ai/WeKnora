// Agent24 fork product patch A24-KB-25 (KB-C1-03 C3-d2, WeKnora#41, CONTRACT-READONLY-v1 R7.6).

package middleware

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// Agent24RecheckBearerEntry is what Auth established at entry for a Bearer request: the user and the
// tenant the request operates in.
type Agent24RecheckBearerEntry struct {
	UserID   string
	TenantID uint64
}

// Agent24RecheckBearer runs again, before a Knowledge response is assembled (R7.6), the checks Auth made
// for a Bearer user JWT: ValidateToken (signature, token record, user row), the target tenant
// (resolveTargetTenant), the role in it (resolveTenantRole) and the route's Viewer floor (RequireRole). It
// writes no response, does not touch the request context and writes nothing to the store: the orphan home
// tenant's AddMember is not repeated, because when Auth made it the row is there now (the last Owner
// cannot be removed), and when it failed Auth fell through to the same RBAC step this does.
//
// The entry identity is the baseline: a different user or target tenant is credential_invalid. Every
// credential failure is credential_invalid, as Auth's Bearer refusal is under the integration envelope;
// the user JWT has no credential_id, so R10.2 has no expired or revoked row for it.
//
// The returned error is an infrastructure failure (a token, user, membership or tenant lookup could not be
// answered); the caller refuses the request as internal. Auth treats a failed membership lookup as no
// membership and goes on; here it is an error, as every other lookup the recheck makes.
func Agent24RecheckBearer(
	ctx context.Context,
	users interfaces.UserService,
	members interfaces.TenantMemberService,
	tenants interfaces.TenantService,
	cfg *config.Config,
	header http.Header,
	entry Agent24RecheckBearerEntry,
) (Agent24RecheckResult, error) {
	invalid := Agent24RecheckResult{Reason: "credential_invalid"}
	auth := header.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return invalid, nil
	}
	user, jwtTenantID, err := users.ValidateToken(ctx, strings.TrimPrefix(auth, "Bearer "))
	if err != nil {
		return agent24BearerTokenFailure(ctx, users, entry.UserID, err)
	}
	if user == nil || user.ID != entry.UserID {
		return invalid, nil
	}
	target, crossSwitch, ok, err := agent24RecheckTargetTenant(ctx, members, tenants, cfg, user, jwtTenantID,
		header.Get("X-Tenant-ID"))
	if err != nil {
		return Agent24RecheckResult{}, err
	}
	if !ok || target != entry.TenantID {
		return invalid, nil
	}
	role, ok, err := agent24RecheckTenantRole(ctx, members, cfg, user, target, crossSwitch)
	if err != nil {
		return Agent24RecheckResult{}, err
	}
	if !ok || !agent24RecheckViewer(cfg, user, role) {
		return invalid, nil
	}
	return Agent24RecheckResult{}, nil
}

// agent24BearerTokenFailure classifies a ValidateToken error. A missing token record or user is a
// credential failure; the token lookup's wrapped error is infrastructure. ValidateToken's own refusals and
// the user lookup's raw error are both unwrapped, so the user row is asked again: when that cannot be
// answered either, the failure was the lookup.
func agent24BearerTokenFailure(
	ctx context.Context, users interfaces.UserService, userID string, err error,
) (Agent24RecheckResult, error) {
	invalid := Agent24RecheckResult{Reason: "credential_invalid"}
	if errors.Is(err, apprepo.ErrTokenNotFound) || errors.Is(err, apprepo.ErrUserNotFound) {
		return invalid, nil
	}
	if errors.Unwrap(err) != nil {
		return Agent24RecheckResult{}, err
	}
	if _, uerr := users.GetUserByID(ctx, userID); uerr != nil && !errors.Is(uerr, apprepo.ErrUserNotFound) {
		return Agent24RecheckResult{}, uerr
	}
	return invalid, nil
}

// agent24RecheckTargetTenant is resolveTargetTenant without writing a response: the X-Tenant-ID header
// (accessible as IsTenantAccessible decides, and existing), else the JWT claim or the user's home tenant,
// else the first active membership. ok is false when the header is malformed, the tenant is not
// accessible or does not exist, or no tenant is left (Auth answered those, so the entry did not reach
// here with them). Lookup errors are returned, not read as a refusal.
func agent24RecheckTargetTenant(
	ctx context.Context, members interfaces.TenantMemberService, tenants interfaces.TenantService,
	cfg *config.Config, user *types.User, jwtTenantID uint64, tenantHeader string,
) (target uint64, crossSwitch, ok bool, err error) {
	if tenantHeader != "" {
		parsed, perr := strconv.ParseUint(tenantHeader, 10, 64)
		if perr != nil || parsed == 0 {
			return 0, false, false, nil
		}
		accessible, err := agent24RecheckTenantAccessible(ctx, members, cfg, user, parsed)
		if err != nil || !accessible {
			return 0, false, false, err
		}
		exists, err := agent24RecheckTenantExists(ctx, tenants, parsed)
		if err != nil || !exists {
			return 0, false, false, err
		}
		return parsed, parsed != user.TenantID, true, nil
	}
	target = jwtTenantID
	if target == 0 {
		target = user.TenantID
	}
	if target == 0 {
		if target, err = agent24RecheckFirstMembership(ctx, members, tenants, user); err != nil || target == 0 {
			return 0, false, false, err
		}
	}
	exists, err := agent24RecheckTenantExists(ctx, tenants, target)
	if err != nil || !exists {
		return 0, false, false, err
	}
	return target, target != user.TenantID, true, nil
}

// agent24RecheckTenantAccessible is IsTenantAccessible with the membership lookup's error returned.
func agent24RecheckTenantAccessible(
	ctx context.Context, members interfaces.TenantMemberService, cfg *config.Config, user *types.User,
	tenantID uint64,
) (bool, error) {
	if user.TenantID == tenantID {
		return true, nil
	}
	if cfg != nil && cfg.Tenant != nil && cfg.Tenant.EnableCrossTenantAccess && user.CanAccessAllTenants {
		return true, nil
	}
	if members == nil {
		return false, nil
	}
	m, err := members.GetMembership(ctx, user.ID, tenantID)
	if err != nil {
		return false, err
	}
	return m != nil && m.Status == types.TenantMemberStatusActive, nil
}

// agent24RecheckFirstMembership is resolveFirstMembershipTarget with lookup errors returned: a tenant
// that no longer exists is skipped, as there.
func agent24RecheckFirstMembership(
	ctx context.Context, members interfaces.TenantMemberService, tenants interfaces.TenantService,
	user *types.User,
) (uint64, error) {
	if members == nil {
		return 0, nil
	}
	list, err := members.ListByUser(ctx, user.ID)
	if err != nil {
		return 0, err
	}
	for _, m := range list {
		if m == nil || m.TenantID == 0 || m.Status != types.TenantMemberStatusActive {
			continue
		}
		exists, err := agent24RecheckTenantExists(ctx, tenants, m.TenantID)
		if err != nil {
			return 0, err
		}
		if exists {
			return m.TenantID, nil
		}
	}
	return 0, nil
}

func agent24RecheckTenantExists(ctx context.Context, tenants interfaces.TenantService, id uint64) (bool, error) {
	t, err := tenants.GetTenantByID(ctx, id)
	if errors.Is(err, apprepo.ErrTenantNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return t != nil, nil
}

// agent24RecheckTenantRole is resolveTenantRole without the orphan home tenant's AddMember (see
// Agent24RecheckBearer): an active membership row, else the cross-tenant superuser switch (Auth checks
// CanAccessAllTenants alone here, not EnableCrossTenantAccess), else refused under RBAC, else fail-open
// as Admin. ok is false when refused.
func agent24RecheckTenantRole(
	ctx context.Context, members interfaces.TenantMemberService, cfg *config.Config, user *types.User,
	tenantID uint64, crossSwitch bool,
) (types.TenantRole, bool, error) {
	m, err := members.GetMembership(ctx, user.ID, tenantID)
	if err != nil {
		return "", false, err
	}
	if m != nil && m.Status == types.TenantMemberStatusActive {
		return m.Role, true, nil
	}
	if crossSwitch && user.CanAccessAllTenants {
		return types.TenantRoleAdmin, true, nil
	}
	if cfg != nil && cfg.Tenant.IsRBACEnforced() {
		return "", false, nil
	}
	return types.TenantRoleAdmin, true, nil
}

// agent24RecheckViewer is RequireRole(Viewer) for the rechecked user: the role, else the cross-tenant
// superuser (IsCrossTenantSuperuser, which does check EnableCrossTenantAccess), else passed when RBAC is
// not enforced.
func agent24RecheckViewer(cfg *config.Config, user *types.User, role types.TenantRole) bool {
	if role.HasPermission(types.TenantRoleViewer) {
		return true
	}
	if cfg != nil && cfg.Tenant != nil && cfg.Tenant.EnableCrossTenantAccess && user.CanAccessAllTenants {
		return true
	}
	return !rbacEnforcementEnabled(cfg)
}
