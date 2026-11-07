// Agent24 fork product patch A24-KB-33 (KB-C2-01b2, WeKnora#133, CONTRACT-UI-BRIDGE-v1 U2.3).

package agent24uisession

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

func TestBootIDIsRandomAndStable(t *testing.T) {
	require.Len(t, BootID(), 22)
	require.Equal(t, BootID(), BootID(), "the boot id does not change while the process runs")
	require.NotEqual(t, newBootID(), newBootID())
}

func TestIssuedByBoot(t *testing.T) {
	boot := BootID()
	require.False(t, IssuedByBoot(nil, boot))
	require.False(t, IssuedByBoot(&Claims{}, boot))
	require.False(t, IssuedByBoot(&Claims{RegisteredClaims: jwt.RegisteredClaims{ID: boot + ".x"}}, ""))
	// An empty boot matches nothing.
	require.False(t, IssuedByBoot(&Claims{RegisteredClaims: jwt.RegisteredClaims{ID: ".x"}}, ""))
	require.False(t, IssuedByBoot(&Claims{RegisteredClaims: jwt.RegisteredClaims{ID: boot + "x"}}, boot))
	require.False(t, IssuedByBoot(&Claims{RegisteredClaims: jwt.RegisteredClaims{ID: "other." + boot}}, boot))
	require.True(t, IssuedByBoot(&Claims{RegisteredClaims: jwt.RegisteredClaims{ID: boot + ".x"}}, boot))
}

// Issue prefixes the jti with this process's boot id, and Verify returns it, whatever the clock says.
func TestIssueCarriesBootID(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	gen := int64(1)
	c := Claims{
		RegisteredClaims: jwt.RegisteredClaims{Subject: "api_tenant:p"}, TenantID: 9,
		Scope: Scope{KnowledgeBaseIDs: []string{"kb"}}, Ops: []string{"kb.list"}, TicketID: "t",
		ParentCredentialID: "key:1", BindingTag: "b", PartitionTag: "p", Cnf: Confirmation{VPH: "v"}, Gen: &gen,
	}
	past := time.Now().Add(-time.Minute)
	token, err := Issue(key, DefaultIssuer, c, past, 10*time.Minute)
	require.NoError(t, err)
	got, err := Verify(key, DefaultIssuer, token, time.Now())
	require.NoError(t, err)
	require.True(t, IssuedByBoot(got, BootID()))
	rest := strings.TrimPrefix(got.ID, BootID()+".")
	require.NotEmpty(t, rest)
	_, err = base64.RawURLEncoding.DecodeString(BootID())
	require.NoError(t, err)
}
