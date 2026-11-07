// Agent24 fork product patch A24-KB-32 (KB-C2-01b1, WeKnora#133, CONTRACT-UI-BRIDGE-v1 U1.3).

package service

import (
	"context"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/agent24uisession"
)

// U1.3: a UI session JWT is never a user access token. Even when it is signed with the user JWT secret
// itself (a key misconfiguration), the real ValidateToken refuses it before any repository lookup: it has
// no user_id claim. The zero userService has nil repositories, so reaching a lookup would panic.
func TestAgent24UISessionJWTIsNotAUserToken(t *testing.T) {
	gen := int64(1)
	now := time.Now()
	claims := agent24uisession.Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "api_tenant:9",
			Issuer:    agent24uisession.DefaultIssuer,
			Audience:  jwt.ClaimStrings{agent24uisession.Audience},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Minute)),
			ID:        "jti-1",
		},
		TenantID:           9,
		Scope:              agent24uisession.Scope{KnowledgeBaseIDs: []string{"kb-a"}},
		Ops:                []string{"kb.list"},
		TicketID:           "6ba7b810-9dad-11d1-80b4-00c04fd430c8",
		ParentCredentialID: "key:7",
		BindingTag:         "bind",
		PartitionTag:       "part",
		Cnf:                agent24uisession.Confirmation{VPH: "vph"},
		Gen:                &gen,
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(getJwtSecret()))
	require.NoError(t, err)
	user, tenantID, err := (&userService{}).ValidateToken(context.Background(), token)
	require.Error(t, err)
	require.Nil(t, user)
	require.Zero(t, tenantID)
}
