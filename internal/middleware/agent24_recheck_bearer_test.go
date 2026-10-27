// Agent24 fork product patch A24-KB-25 (KB-C1-03 C3-d2, WeKnora#41, CONTRACT-READONLY-v1 R7.6).

package middleware

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// agent24BearerUsers embeds UserService and answers only ValidateToken and GetUserByID.
type agent24BearerUsers struct {
	interfaces.UserService
	user        *types.User
	jwtTenantID uint64
	validateErr error
	getErr      error
}

func (s agent24BearerUsers) ValidateToken(context.Context, string) (*types.User, uint64, error) {
	if s.validateErr != nil {
		return nil, 0, s.validateErr
	}
	return s.user, s.jwtTenantID, nil
}

func (s agent24BearerUsers) GetUserByID(context.Context, string) (*types.User, error) {
	return nil, s.getErr
}

// agent24BearerMembers embeds TenantMemberService and answers only GetMembership and ListByUser.
// AddMember and HasAnyMembers are left on the nil embedded interface: a call panics and fails the test.
// Agent24RecheckBearer must not write (the orphan-home AddMember / HasAnyMembers path is not repeated).
type agent24BearerMembers struct {
	interfaces.TenantMemberService
	byTenant map[uint64]*types.TenantMember
	getErr   error
	list     []*types.TenantMember
	listErr  error
}

func (s agent24BearerMembers) GetMembership(_ context.Context, _ string, tenantID uint64) (*types.TenantMember, error) {
	if s.getErr != nil {
		return nil, s.getErr
	}
	if s.byTenant == nil {
		return nil, nil
	}
	return s.byTenant[tenantID], nil
}

func (s agent24BearerMembers) ListByUser(context.Context, string) ([]*types.TenantMember, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.list, nil
}

// agent24BearerTenants embeds TenantService and answers only GetTenantByID.
// An id in errByID returns that error. An id in present returns the tenant. Anything else is not found.
type agent24BearerTenants struct {
	interfaces.TenantService
	present []uint64
	errByID map[uint64]error
}

func (s agent24BearerTenants) GetTenantByID(_ context.Context, id uint64) (*types.Tenant, error) {
	if err, ok := s.errByID[id]; ok {
		return nil, err
	}
	for _, present := range s.present {
		if present == id {
			return &types.Tenant{ID: id}, nil
		}
	}
	return nil, apprepo.ErrTenantNotFound
}

func agent24BearerViewer(tenantID uint64) *types.TenantMember {
	return &types.TenantMember{
		UserID: "u1", TenantID: tenantID,
		Role: types.TenantRoleViewer, Status: types.TenantMemberStatusActive,
	}
}

func TestAgent24RecheckBearer(t *testing.T) {
	dbDown := errors.New("db down")
	tokenMissing := fmt.Errorf("look up token: %w", apprepo.ErrTokenNotFound)
	tokenDB := fmt.Errorf("look up token: %w", dbDown)
	revoked := errors.New("token is revoked")
	connReset := errors.New("conn reset")
	userLookup := errors.New("user lookup failed")
	disabled := errors.New("account is disabled")
	listDown := errors.New("list down")
	tenantDown := errors.New("tenant down")
	memberDown := errors.New("member down")

	claim0 := uint64(0)
	claim8 := uint64(8)
	claim9 := uint64(9)
	rbacOff := false
	noAuth := ""
	basic := "Basic x"
	entry0 := Agent24RecheckBearerEntry{UserID: "u1", TenantID: 0}
	entry9 := Agent24RecheckBearerEntry{UserID: "u1", TenantID: 9}
	home0 := &types.User{ID: "u1", TenantID: 0}
	otherUser := &types.User{ID: "u2", TenantID: 7}
	super := &types.User{ID: "u1", TenantID: 7, CanAccessAllTenants: true}
	viewer7 := map[uint64]*types.TenantMember{7: agent24BearerViewer(7)}
	invalid := "credential_invalid"

	cases := []struct {
		name          string
		authorization *string
		tenantHeader  string
		entry         *Agent24RecheckBearerEntry
		user          *types.User
		nilUser       bool
		claim         *uint64
		validateErr   error
		getUserErr    error
		members       map[uint64]*types.TenantMember
		memberErr     error
		list          []*types.TenantMember
		listErr       error
		present       []uint64
		tenantErr     map[uint64]error
		rbac          *bool
		cross         bool
		wantReason    string
		wantErr       error
		exactErr      bool
	}{
		{name: "active viewer passes", members: viewer7},

		// members: the rest of the recheck would pass, so only the Bearer prefix refuses.
		{name: "no authorization header", authorization: &noAuth, members: viewer7, wantReason: invalid},
		{name: "basic authorization", authorization: &basic, members: viewer7, wantReason: invalid},

		{name: "validate token not found", validateErr: tokenMissing, wantReason: invalid},
		{name: "validate token wrapped db error", validateErr: tokenDB, wantErr: dbDown},
		{
			// The sentinel decides: the user row is not asked again, so its failure does not matter.
			name: "validate token user not found", validateErr: apprepo.ErrUserNotFound, getUserErr: dbDown,
			wantReason: invalid,
		},
		{name: "token revoked", validateErr: revoked, wantReason: invalid},
		{
			name:        "validate token raw error and user lookup fails",
			validateErr: connReset, getUserErr: userLookup, wantErr: userLookup, exactErr: true,
		},
		{
			name:        "account disabled and user row missing",
			validateErr: disabled, getUserErr: apprepo.ErrUserNotFound, wantReason: invalid,
		},

		{name: "user id differs from entry", user: otherUser, members: viewer7, wantReason: invalid},
		{name: "nil user", nilUser: true, wantReason: invalid},

		{
			// Tenant 8 exists and the user is a member there: only the entry baseline refuses.
			name: "claim differs from entry tenant", claim: &claim8, present: []uint64{8},
			members: map[uint64]*types.TenantMember{8: agent24BearerViewer(8)}, wantReason: invalid,
		},
		{name: "claim zero falls back to user tenant", claim: &claim0, members: viewer7},
		{
			// Tenant 5 exists, so only its status keeps it from being chosen.
			name: "claim zero uses first active membership", user: home0, claim: &claim0, members: viewer7,
			present: []uint64{5, 7},
			list: []*types.TenantMember{
				{TenantID: 5, Status: types.TenantMemberStatusSuspended},
				{TenantID: 6, Status: types.TenantMemberStatusActive},
				{TenantID: 7, Role: types.TenantRoleViewer, Status: types.TenantMemberStatusActive},
			},
		},
		{name: "list memberships fails", user: home0, claim: &claim0, listErr: listDown, wantErr: listDown},
		{
			name: "first active membership tenant lookup fails", user: home0, claim: &claim0,
			list: []*types.TenantMember{
				{TenantID: 5, Status: types.TenantMemberStatusSuspended},
				{TenantID: 6, Status: types.TenantMemberStatusActive},
			},
			tenantErr: map[uint64]error{6: tenantDown}, wantErr: tenantDown,
		},
		{
			name: "no usable membership", user: home0, claim: &claim0,
			list:       []*types.TenantMember{{TenantID: 5, Status: types.TenantMemberStatusSuspended}},
			wantReason: invalid,
		},
		{name: "jwt tenant does not exist", present: []uint64{}, members: viewer7, wantReason: invalid},
		{
			name: "jwt tenant lookup fails", members: viewer7,
			tenantErr: map[uint64]error{7: tenantDown}, wantErr: tenantDown,
		},

		{name: "malformed tenant header", tenantHeader: "abc", entry: &entry0, wantReason: invalid},
		{
			// A home tenant 0 that exists, with a membership: only the header's zero check refuses.
			name: "zero tenant header", tenantHeader: "0", entry: &entry0, user: home0, present: []uint64{0},
			members: map[uint64]*types.TenantMember{0: agent24BearerViewer(0)}, wantReason: invalid,
		},
		{
			name: "header tenant not accessible", tenantHeader: "9", entry: &entry9,
			present: []uint64{9}, wantReason: invalid,
		},
		// With RBAC not enforced the role step would pass, so only the accessibility check refuses.
		{
			name: "header tenant not accessible fail open", tenantHeader: "9", entry: &entry9, rbac: &rbacOff,
			present: []uint64{9}, wantReason: invalid,
		},
		{
			name: "header tenant inactive member fail open", tenantHeader: "9", entry: &entry9, rbac: &rbacOff,
			members: map[uint64]*types.TenantMember{9: {
				UserID: "u1", TenantID: 9, Role: types.TenantRoleViewer, Status: types.TenantMemberStatusSuspended,
			}},
			present: []uint64{9}, wantReason: invalid,
		},
		{name: "header home tenant fail open", tenantHeader: "7", rbac: &rbacOff},
		// Each half of the cross-tenant accessibility condition on its own (PR-Daemon B1 on #122).
		{
			name: "header tenant superuser flag off", tenantHeader: "9", entry: &entry9, user: super,
			present: []uint64{9}, wantReason: invalid,
		},
		{
			name: "header tenant cross flag non-superuser fail open", tenantHeader: "9", entry: &entry9,
			cross: true, rbac: &rbacOff, present: []uint64{9}, wantReason: invalid,
		},
		{
			name: "header tenant active member", tenantHeader: "9", entry: &entry9,
			members: map[uint64]*types.TenantMember{9: agent24BearerViewer(9)}, present: []uint64{9},
		},
		{
			name: "header tenant cross superuser", tenantHeader: "9", entry: &entry9,
			user: super, cross: true, present: []uint64{9},
		},
		{
			name: "header tenant membership lookup fails", tenantHeader: "9", entry: &entry9,
			memberErr: memberDown, wantErr: memberDown,
		},
		{
			name: "header tenant does not exist", tenantHeader: "9", entry: &entry9,
			members: map[uint64]*types.TenantMember{9: agent24BearerViewer(9)},
			present: []uint64{}, wantReason: invalid,
		},
		{
			name: "header tenant lookup fails", tenantHeader: "9", entry: &entry9,
			members:   map[uint64]*types.TenantMember{9: agent24BearerViewer(9)},
			tenantErr: map[uint64]error{9: tenantDown}, wantErr: tenantDown,
		},

		{
			name: "inactive member enforced",
			members: map[uint64]*types.TenantMember{7: {
				UserID: "u1", TenantID: 7, Role: types.TenantRoleViewer, Status: types.TenantMemberStatusSuspended,
			}},
			wantReason: invalid,
		},
		{
			name: "inactive member fail open", rbac: &rbacOff,
			members: map[uint64]*types.TenantMember{7: {
				UserID: "u1", TenantID: 7, Role: types.TenantRoleViewer, Status: types.TenantMemberStatusSuspended,
			}},
		},
		{name: "role membership lookup fails", members: viewer7, memberErr: memberDown, wantErr: memberDown},
		{
			name: "cross switch ignores enable cross tenant access",
			user: super, claim: &claim9, entry: &entry9, present: []uint64{9},
		},

		{
			// A superuser without a membership in the home tenant is not switching: no Admin.
			name: "superuser home tenant no membership enforced", user: super, wantReason: invalid,
		},
		{
			// The cross-tenant switch grants Admin only to a superuser (PR-Daemon B1 on #122).
			name: "foreign claim non-superuser no membership enforced", claim: &claim9,
			entry: &entry9, present: []uint64{9}, wantReason: invalid,
		},

		{
			name: "ghost role enforced",
			members: map[uint64]*types.TenantMember{7: {
				UserID: "u1", TenantID: 7, Role: types.TenantRole("ghost"), Status: types.TenantMemberStatusActive,
			}},
			wantReason: invalid,
		},
		{
			name: "ghost role fail open", rbac: &rbacOff,
			members: map[uint64]*types.TenantMember{7: {
				UserID: "u1", TenantID: 7, Role: types.TenantRole("ghost"), Status: types.TenantMemberStatusActive,
			}},
		},
		// Each half of the Viewer floor's superuser condition on its own (PR-Daemon B1 on #122).
		{
			name: "ghost role superuser flag off", user: super,
			members: map[uint64]*types.TenantMember{7: {
				UserID: "u1", TenantID: 7, Role: types.TenantRole("ghost"), Status: types.TenantMemberStatusActive,
			}},
			wantReason: invalid,
		},
		{
			name: "ghost role cross flag non-superuser", cross: true,
			members: map[uint64]*types.TenantMember{7: {
				UserID: "u1", TenantID: 7, Role: types.TenantRole("ghost"), Status: types.TenantMemberStatusActive,
			}},
			wantReason: invalid,
		},
		{
			name: "ghost role cross superuser", user: super, cross: true,
			members: map[uint64]*types.TenantMember{7: {
				UserID: "u1", TenantID: 7, Role: types.TenantRole("ghost"), Status: types.TenantMemberStatusActive,
			}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			authz := "Bearer tok"
			if tc.authorization != nil {
				authz = *tc.authorization
			}
			header := http.Header{}
			if authz != "" {
				header.Set("Authorization", authz)
			}
			if tc.tenantHeader != "" {
				header.Set("X-Tenant-ID", tc.tenantHeader)
			}
			entry := Agent24RecheckBearerEntry{UserID: "u1", TenantID: 7}
			if tc.entry != nil {
				entry = *tc.entry
			}
			var user *types.User
			if !tc.nilUser {
				user = tc.user
				if user == nil {
					user = &types.User{ID: "u1", TenantID: 7}
				}
			}
			claim := uint64(7)
			if tc.claim != nil {
				claim = *tc.claim
			}
			rbac := true
			if tc.rbac != nil {
				rbac = *tc.rbac
			}
			present := tc.present
			if present == nil {
				present = []uint64{7}
			}
			res, err := Agent24RecheckBearer(
				context.Background(),
				agent24BearerUsers{
					user: user, jwtTenantID: claim, validateErr: tc.validateErr, getErr: tc.getUserErr,
				},
				agent24BearerMembers{
					byTenant: tc.members, getErr: tc.memberErr, list: tc.list, listErr: tc.listErr,
				},
				agent24BearerTenants{present: present, errByID: tc.tenantErr},
				&config.Config{Tenant: &config.TenantConfig{
					EnableRBAC: &rbac, EnableCrossTenantAccess: tc.cross,
				}},
				header, entry,
			)
			if tc.wantErr != nil {
				require.Empty(t, res.Reason)
				if tc.exactErr {
					require.True(t, err == tc.wantErr)
					return
				}
				require.ErrorIs(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantReason, res.Reason)
		})
	}
}

// TestAgent24RecheckBearerNilGuards covers what the table above never builds: a nil config, a config
// without Tenant settings, and no membership service (L3 on #122). Each guard keeps the recheck from
// panicking and decides as Auth does for the same input.
func TestAgent24RecheckBearerNilGuards(t *testing.T) {
	header := http.Header{}
	header.Set("Authorization", "Bearer tok")
	entry := Agent24RecheckBearerEntry{UserID: "u1", TenantID: 7}
	users := agent24BearerUsers{user: &types.User{ID: "u1", TenantID: 7}, jwtTenantID: 7}
	tenants := agent24BearerTenants{present: []uint64{0, 7, 9}}
	inactive := agent24BearerMembers{byTenant: map[uint64]*types.TenantMember{7: {
		UserID: "u1", TenantID: 7, Role: types.TenantRoleViewer, Status: types.TenantMemberStatusSuspended,
	}}}
	ghost := agent24BearerMembers{byTenant: map[uint64]*types.TenantMember{7: {
		UserID: "u1", TenantID: 7, Role: types.TenantRole("ghost"), Status: types.TenantMemberStatusActive,
	}}}

	run := func(t *testing.T, users agent24BearerUsers, members interfaces.TenantMemberService,
		cfg *config.Config, header http.Header, entry Agent24RecheckBearerEntry,
	) string {
		t.Helper()
		res, err := Agent24RecheckBearer(context.Background(), users, members, tenants, cfg, header, entry)
		require.NoError(t, err)
		return res.Reason
	}

	t.Run("nil config fails open on the role", func(t *testing.T) {
		require.Empty(t, run(t, users, inactive, nil, header, entry))
	})
	t.Run("nil config fails open on the viewer floor", func(t *testing.T) {
		require.Empty(t, run(t, users, ghost, nil, header, entry))
	})
	t.Run("no tenant settings enforce RBAC on the role", func(t *testing.T) {
		require.Equal(t, "credential_invalid", run(t, users, inactive, &config.Config{}, header, entry))
	})
	t.Run("no tenant settings enforce RBAC on the viewer floor", func(t *testing.T) {
		require.Equal(t, "credential_invalid", run(t, users, ghost, &config.Config{}, header, entry))
	})
	t.Run("no tenant settings with a header tenant", func(t *testing.T) {
		h := header.Clone()
		h.Set("X-Tenant-ID", "9")
		members := agent24BearerMembers{byTenant: map[uint64]*types.TenantMember{9: agent24BearerViewer(9)}}
		require.Empty(t, run(t, users, members, &config.Config{}, h,
			Agent24RecheckBearerEntry{UserID: "u1", TenantID: 9}))
	})
	t.Run("no membership service with a header tenant", func(t *testing.T) {
		h := header.Clone()
		h.Set("X-Tenant-ID", "9")
		require.Equal(t, "credential_invalid",
			run(t, users, nil, nil, h, Agent24RecheckBearerEntry{UserID: "u1", TenantID: 9}))
	})
	t.Run("no membership service without a tenant", func(t *testing.T) {
		homeless := agent24BearerUsers{user: &types.User{ID: "u1"}}
		require.Equal(t, "credential_invalid",
			run(t, homeless, nil, nil, header, Agent24RecheckBearerEntry{UserID: "u1"}))
	})
}
