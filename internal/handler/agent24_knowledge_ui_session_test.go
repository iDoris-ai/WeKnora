// Agent24 fork product patch A24-KB-33 (KB-C2-01b2, WeKnora#133, CONTRACT-UI-BRIDGE-v1 U2.6–U2.8, U3.1–U3.4).

package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/agent24uisession"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

func agent24UISessionHandler(
	store interfaces.Agent24UISessionStore, keys []*types.TenantAPIKey, tenant *types.Tenant,
	revocations interfaces.Agent24CredentialRevocationStore,
) *Agent24KnowledgeUISessionHandler {
	if tenant == nil {
		tenant = &types.Tenant{ID: 9}
	}
	return &Agent24KnowledgeUISessionHandler{deps: agent24UISessionDeps{
		store:  store,
		key:    agent24UIExchangeKey(),
		issuer: agent24uisession.DefaultIssuer,
		stored: agent24UIStoredDeps{
			tenants:     agent24UITenantStub{tenant: tenant},
			keys:        agent24UIKeyStub{keys: keys},
			revocations: revocations,
			key:         agent24UIExchangeKey(),
		},
	}}
}

func agent24UISessionClaims(t *testing.T, ticket *types.Agent24UITicket) agent24uisession.Claims {
	t.Helper()
	ids, err := ticket.ScopeKnowledgeBaseIDs()
	require.NoError(t, err)
	gen := ticket.Generation
	return agent24uisession.Claims{
		RegisteredClaims:   jwt.RegisteredClaims{Subject: ticket.Principal},
		TenantID:           ticket.TenantID,
		Scope:              agent24uisession.Scope{KnowledgeBaseIDs: ids},
		Ops:                append([]string(nil), agent24uisession.UIOps...),
		TicketID:           ticket.TicketID,
		ParentCredentialID: ticket.CredentialID,
		BindingTag:         ticket.BindingTag,
		PartitionTag:       ticket.PartitionTag,
		Cnf:                agent24uisession.Confirmation{VPH: ticket.VPH},
		Gen:                &gen,
	}
}

func agent24UIIssueFor(
	t *testing.T, ticket *types.Agent24UITicket, key []byte, now time.Time, life time.Duration,
) string {
	t.Helper()
	claims := agent24UISessionClaims(t, ticket)
	token, err := agent24uisession.Issue(key, agent24uisession.DefaultIssuer, claims, now, life)
	require.NoError(t, err)
	return token
}

func agent24UIProbeCall(
	t *testing.T, h *Agent24KnowledgeUISessionHandler, authorization, proof string,
) (*httptest.ResponseRecorder, *agent24AuditStub, *bytes.Buffer) {
	t.Helper()
	logs := agent24CaptureCredentialLogs(t)
	gin.SetMode(gin.TestMode)
	stub := &agent24AuditStub{}
	r := gin.New()
	r.Use(Agent24KnowledgeAudit(stub))
	r.GET(agent24AuditPathUISession, h.Probe)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, agent24AuditPathUISession, nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	if proof != "" {
		req.Header.Set(agent24UIViewProofHeader, proof)
	}
	r.ServeHTTP(w, req)
	return w, stub, logs
}

func agent24UIAssertSessionSecrets(t *testing.T, body, blob, logs string, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		require.NotContains(t, body, secret)
		require.NotContains(t, blob, secret)
		require.NotContains(t, logs, secret)
	}
}

func TestAgent24UISessionProbe(t *testing.T) {
	proof, vph := agent24UIViewSecret()
	live := []*types.TenantAPIKey{agent24UILiveKey([]string{"kb-b", "kb-a"}, false, nil, nil)}

	t.Run("ok", func(t *testing.T) {
		db, store := agent24UIHandlerDB(t)
		raw := agent24UIRawTicket(70)
		ticket := agent24UISeedTicket(t, store, raw, vph, nil)
		token := agent24UIIssueFor(t, ticket, agent24UIExchangeKey(), time.Now(), time.Minute)
		w, stub, logs := agent24UIProbeCall(t, hSession(store, live), "bearer "+token, proof)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
		var resp struct {
			ExpiresIn int `json:"expires_in"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.Greater(t, resp.ExpiresIn, 0)
		require.LessOrEqual(t, resp.ExpiresIn, 60)
		entry, view := agent24UIAuditOf(t, stub)
		require.Equal(t, types.AuditAction("knowledge.ui.session"), entry.Action)
		require.Equal(t, types.AuditOutcomeSuccess, entry.Outcome)
		require.Equal(t, "ok", view.Outcome)
		agent24UIRequireIdentified(t, view)
		require.NotContains(t, view.Resource, "internal_reason")
		require.Equal(t, ticket.BindingTag, view.Resource["binding_tag"])
		require.Equal(t, agent24uisession.H("kb-ticket-id-v1", []byte(ticket.TicketID)),
			view.Resource["ticket_id_hash"])
		agent24UIAssertSessionSecrets(t, w.Body.String(), agent24AuditBlob(t, entry), logs.String(),
			raw, token, proof, vph)
		require.Nil(t, agent24UIFamilyRow(t, db, vph).RevokedAt)
	})

	t.Run("expired", func(t *testing.T) {
		_, store := agent24UIHandlerDB(t)
		ticket := agent24UISeedTicket(t, store, agent24UIRawTicket(71), vph, nil)
		// Probe uses time.Now. Leeway is 30s, so the token must be more than that past exp.
		issuedAt := time.Now().Add(-2 * time.Minute)
		token := agent24UIIssueFor(t, ticket, agent24UIExchangeKey(), issuedAt, time.Minute)
		w, stub, logs := agent24UIProbeCall(t, hSession(store, live), "Bearer "+token, proof)
		agent24UIRequireProbeRefusal(t, w, stub, logs, "credential_expired", false, token, proof)
	})

	t.Run("wrong key", func(t *testing.T) {
		_, store := agent24UIHandlerDB(t)
		ticket := agent24UISeedTicket(t, store, agent24UIRawTicket(72), vph, nil)
		other := bytesRepeat9()
		other[0] = 8
		token := agent24UIIssueFor(t, ticket, other, time.Now(), time.Minute)
		w, stub, logs := agent24UIProbeCall(t, hSession(store, live), "Bearer "+token, proof)
		agent24UIRequireProbeRefusal(t, w, stub, logs, "credential_invalid", false, token, proof)
	})

	t.Run("alg none", func(t *testing.T) {
		_, store := agent24UIHandlerDB(t)
		ticket := agent24UISeedTicket(t, store, agent24UIRawTicket(73), vph, nil)
		token := agent24UIAlgNone(t, ticket)
		w, stub, logs := agent24UIProbeCall(t, hSession(store, live), "Bearer "+token, proof)
		agent24UIRequireProbeRefusal(t, w, stub, logs, "credential_invalid", false, token, proof)
	})

	t.Run("bad aud", func(t *testing.T) {
		_, store := agent24UIHandlerDB(t)
		ticket := agent24UISeedTicket(t, store, agent24UIRawTicket(74), vph, nil)
		token := agent24UIBadAud(t, ticket)
		w, stub, logs := agent24UIProbeCall(t, hSession(store, live), "Bearer "+token, proof)
		agent24UIRequireProbeRefusal(t, w, stub, logs, "credential_invalid", false, token, proof)
	})

	t.Run("missing proof", func(t *testing.T) {
		db, store := agent24UIHandlerDB(t)
		ticket := agent24UISeedTicket(t, store, agent24UIRawTicket(75), vph, nil)
		token := agent24UIIssueFor(t, ticket, agent24UIExchangeKey(), time.Now(), time.Minute)
		w, stub, logs := agent24UIProbeCall(t, hSession(store, live), "Bearer "+token, "")
		agent24UIRequireProbeRefusal(t, w, stub, logs, "credential_invalid", true, token, proof)
		require.Nil(t, agent24UIFamilyRow(t, db, vph).RevokedAt)
	})

	t.Run("wrong proof", func(t *testing.T) {
		db, store := agent24UIHandlerDB(t)
		ticket := agent24UISeedTicket(t, store, agent24UIRawTicket(76), vph, nil)
		token := agent24UIIssueFor(t, ticket, agent24UIExchangeKey(), time.Now(), time.Minute)
		wrong := agent24UITestTag(8)
		w, stub, logs := agent24UIProbeCall(t, hSession(store, live), "Bearer "+token, wrong)
		agent24UIRequireProbeRefusal(t, w, stub, logs, "credential_invalid", true, token, wrong, proof)
		require.Nil(t, agent24UIFamilyRow(t, db, vph).RevokedAt)
	})

	t.Run("ticket revoked", func(t *testing.T) {
		_, store := agent24UIHandlerDB(t)
		ticket := agent24UISeedTicket(t, store, agent24UIRawTicket(77), vph, nil)
		require.NoError(t, store.RevokeTicket(context.Background(), 9, ticket.Principal, ticket.TicketID, time.Now()))
		token := agent24UIIssueFor(t, ticket, agent24UIExchangeKey(), time.Now(), time.Minute)
		w, stub, logs := agent24UIProbeCall(t, hSession(store, live), "Bearer "+token, proof)
		agent24UIRequireProbeRefusal(t, w, stub, logs, "credential_invalid", true, token, proof)
	})

	t.Run("family revoked", func(t *testing.T) {
		_, store := agent24UIHandlerDB(t)
		ticket := agent24UISeedTicket(t, store, agent24UIRawTicket(78), vph, nil)
		require.NoError(t, store.RevokeFamily(context.Background(), 9, ticket.Principal, vph, time.Now()))
		token := agent24UIIssueFor(t, ticket, agent24UIExchangeKey(), time.Now(), time.Minute)
		w, stub, logs := agent24UIProbeCall(t, hSession(store, live), "Bearer "+token, proof)
		agent24UIRequireProbeRefusal(t, w, stub, logs, "credential_invalid", true, token, proof)
	})

	t.Run("credential revoked", func(t *testing.T) {
		_, store := agent24UIHandlerDB(t)
		ticket := agent24UISeedTicket(t, store, agent24UIRawTicket(79), vph, nil)
		token := agent24UIIssueFor(t, ticket, agent24UIExchangeKey(), time.Now(), time.Minute)
		w, stub, logs := agent24UIProbeCall(t, hSession(store, nil), "Bearer "+token, proof)
		agent24UIRequireProbeRefusal(t, w, stub, logs, "credential_revoked", true, token, proof)
	})

	t.Run("ticket deleted", func(t *testing.T) {
		db, store := agent24UIHandlerDB(t)
		ticket := agent24UISeedTicket(t, store, agent24UIRawTicket(80), vph, nil)
		token := agent24UIIssueFor(t, ticket, agent24UIExchangeKey(), time.Now(), time.Minute)
		err := db.Where("ticket_id = ?", ticket.TicketID).Delete(&types.Agent24UITicket{}).Error
		require.NoError(t, err)
		w, stub, logs := agent24UIProbeCall(t, hSession(store, live), "Bearer "+token, proof)
		agent24UIRequireProbeRefusal(t, w, stub, logs, "credential_invalid", false, token, proof)
	})

	// A JWT whose sub or tenant no longer matches the ticket row it names is not that ticket's session.
	for _, tc := range []struct {
		name   string
		change func(*agent24uisession.Claims)
	}{
		{name: "subject mismatch", change: func(c *agent24uisession.Claims) { c.Subject = "api_tenant:principal-8" }},
		{name: "tenant mismatch", change: func(c *agent24uisession.Claims) { c.TenantID = 10 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, store := agent24UIHandlerDB(t)
			ticket := agent24UISeedTicket(t, store, agent24UIRawTicket(81), vph, nil)
			claims := agent24UISessionClaims(t, ticket)
			tc.change(&claims)
			token, err := agent24uisession.Issue(
				agent24UIExchangeKey(), agent24uisession.DefaultIssuer, claims, time.Now(), time.Minute)
			require.NoError(t, err)
			w, stub, logs := agent24UIProbeCall(t, hSession(store, live), "Bearer "+token, proof)
			agent24UIRequireProbeRefusal(t, w, stub, logs, "credential_invalid", true, token, proof)
		})
	}

	t.Run("missing authorization", func(t *testing.T) {
		_, store := agent24UIHandlerDB(t)
		w, stub, logs := agent24UIProbeCall(t, hSession(store, live), "", proof)
		agent24UIRequireProbeRefusal(t, w, stub, logs, "credential_invalid", false, proof)
	})
}

func hSession(store interfaces.Agent24UISessionStore, keys []*types.TenantAPIKey) *Agent24KnowledgeUISessionHandler {
	return agent24UISessionHandler(store, keys, nil, nil)
}

func bytesRepeat9() []byte {
	return bytes.Clone(agent24UIExchangeKey())
}

func agent24UIAlgNone(t *testing.T, ticket *types.Agent24UITicket) string {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	c := agent24UISessionClaims(t, ticket)
	c.Issuer = agent24uisession.DefaultIssuer
	c.Audience = jwt.ClaimStrings{agent24uisession.Audience}
	c.IssuedAt = jwt.NewNumericDate(now)
	c.NotBefore = jwt.NewNumericDate(now)
	c.ExpiresAt = jwt.NewNumericDate(now.Add(time.Minute))
	c.ID = "jti-none"
	payload, err := json.Marshal(c)
	require.NoError(t, err)
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + "."
}

func agent24UIBadAud(t *testing.T, ticket *types.Agent24UITicket) string {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	c := agent24UISessionClaims(t, ticket)
	c.Issuer = agent24uisession.DefaultIssuer
	c.Audience = jwt.ClaimStrings{"other"}
	c.IssuedAt = jwt.NewNumericDate(now)
	c.NotBefore = jwt.NewNumericDate(now)
	c.ExpiresAt = jwt.NewNumericDate(now.Add(time.Minute))
	c.ID = "jti-bad-aud"
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, c)
	signed, err := tok.SignedString(agent24UIExchangeKey())
	require.NoError(t, err)
	return signed
}

func agent24UIRequireIdentified(t *testing.T, view agent24UIAuditView) {
	t.Helper()
	require.NotNil(t, view.Principal)
	require.Equal(t, "api_tenant", view.Principal.Type)
	require.Equal(t, "principal-7", view.Principal.ID)
	require.NotNil(t, view.CredentialID)
	require.Equal(t, "key:7", *view.CredentialID)
	require.NotNil(t, view.TenantID)
	require.Equal(t, uint64(9), *view.TenantID)
}

func agent24UIRequireProbeRefusal(
	t *testing.T, w *httptest.ResponseRecorder, stub *agent24AuditStub, logs *bytes.Buffer,
	reason string, identified bool, secrets ...string,
) {
	t.Helper()
	require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
	code, got, _ := agent24UIError(t, w)
	require.Equal(t, "unauthorized", code)
	require.Equal(t, reason, got)
	entry, view := agent24UIAuditOf(t, stub)
	require.Equal(t, types.AuditAction("knowledge.ui.session"), entry.Action)
	require.Equal(t, types.AuditOutcomeDenied, entry.Outcome)
	require.Equal(t, "unauthorized", view.Outcome)
	require.Equal(t, reason, view.Reason)
	if identified {
		agent24UIRequireIdentified(t, view)
	} else {
		require.Nil(t, view.Principal)
		require.Nil(t, view.CredentialID)
		require.Nil(t, view.TenantID)
	}
	agent24UIAssertSessionSecrets(t, w.Body.String(), agent24AuditBlob(t, entry), logs.String(), secrets...)
}
