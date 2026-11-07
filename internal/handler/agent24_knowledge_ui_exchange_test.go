// Agent24 fork product patch A24-KB-33 (KB-C2-01b2, WeKnora#133, CONTRACT-UI-BRIDGE-v1 U2.6–U2.8, U3.1–U3.4).

package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/agent24uisession"
	apprepo "github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

func agent24UIExchangeKey() []byte { return bytes.Repeat([]byte{9}, 32) }

func agent24UIViewSecret() (header, vph string) {
	secret := bytes.Repeat([]byte{4}, 32)
	return base64.RawURLEncoding.EncodeToString(secret), agent24uisession.VPH(secret)
}

type agent24UIKeyStub struct {
	interfaces.TenantAPIKeyService
	keys []*types.TenantAPIKey
	err  error
	// unfiltered returns every key, as a list that ignored its tenant argument would.
	unfiltered bool
}

func (s agent24UIKeyStub) ListAPIKeys(_ context.Context, tenantID uint64) ([]*types.TenantAPIKey, error) {
	if s.err != nil {
		return nil, s.err
	}
	var out []*types.TenantAPIKey
	for _, key := range s.keys {
		if key != nil && (s.unfiltered || key.TenantIDValue() == tenantID) {
			out = append(out, key)
		}
	}
	return out, nil
}

type agent24UITenantStub struct {
	interfaces.TenantService
	tenant *types.Tenant
	err    error
}

func (s agent24UITenantStub) GetTenantByID(context.Context, uint64) (*types.Tenant, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.tenant == nil {
		return nil, apprepo.ErrTenantNotFound
	}
	return s.tenant, nil
}

func agent24UILiveKey(ids []string, full bool, caps []string, exp *time.Time) *types.TenantAPIKey {
	tenantID := uint64(9)
	if caps == nil && !full {
		caps = []string{string(types.APIKeyCapabilityRetrieve)}
	}
	return &types.TenantAPIKey{
		ID: 7, TenantID: &tenantID, ScopeType: types.APIKeyScopeTenant, Name: "k",
		FullAccess: full, KnowledgeBaseIDs: ids, Capabilities: caps, ExpiresAt: exp,
	}
}

func agent24UIExchangeHandler(
	store interfaces.Agent24UISessionStore, keys []*types.TenantAPIKey, tenant *types.Tenant,
	revocations interfaces.Agent24CredentialRevocationStore, scope agent24ScopeResolver,
) *Agent24KnowledgeUIExchangeHandler {
	if tenant == nil {
		tenant = &types.Tenant{ID: 9}
	}
	if scope == nil {
		scope = agent24UIResolver(agent24UIKBs(9, "kb-a", "kb-b", "kb-z")...)
	}
	return &Agent24KnowledgeUIExchangeHandler{
		scope: scope,
		store: store,
		stored: agent24UIStoredDeps{
			tenants: agent24UITenantStub{tenant: tenant}, keys: agent24UIKeyStub{keys: keys}, revocations: revocations,
			key: agent24UIExchangeKey(),
		},
		key:    agent24UIExchangeKey(),
		issuer: agent24uisession.DefaultIssuer,
		limit:  newAgent24UIExchangeLimiter(),
	}
}

// agent24UIKeyWith is the live kb-b, kb-a key with one change applied.
func agent24UIKeyWith(change func(*types.TenantAPIKey)) *types.TenantAPIKey {
	key := agent24UILiveKey([]string{"kb-b", "kb-a"}, false, nil, nil)
	change(key)
	return key
}

func agent24UILiveKeysAB() []*types.TenantAPIKey {
	return []*types.TenantAPIKey{agent24UILiveKey([]string{"kb-b", "kb-a"}, false, nil, nil)}
}

func agent24UIRawTicket(fill byte) string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 32))
}

func agent24UISeedTicket(
	t *testing.T, store interfaces.Agent24UISessionStore, raw, vph string, tweak func(*types.Agent24UITicket),
) *types.Agent24UITicket {
	t.Helper()
	hash, err := agent24uisession.TicketHash(raw)
	require.NoError(t, err)
	ticket := &types.Agent24UITicket{
		TicketHash: hash, TicketID: "tid-" + hash[:8], TenantID: 9,
		Principal: "api_tenant:principal-7", CredentialID: "key:7", KeyID: 7,
		BindingTag: agent24UITestTag(1), PartitionTag: agent24UITestTag(2),
		VPH: vph, Generation: 4, ExpiresAt: time.Now().Add(time.Minute),
		PrincipalSecretTag: agent24UIPrincipalSecretTag(agent24UIExchangeKey(), &types.Tenant{ID: 9}),
	}
	require.NoError(t, ticket.SetScopeKnowledgeBaseIDs([]string{"kb-b", "kb-a"}))
	if tweak != nil {
		tweak(ticket)
	}
	require.NoError(t, store.CreateTicket(context.Background(), ticket))
	return ticket
}

func agent24UIPostExchange(
	h *Agent24KnowledgeUIExchangeHandler, body, proof, remote string,
) (*httptest.ResponseRecorder, *agent24AuditStub) {
	gin.SetMode(gin.TestMode)
	stub := &agent24AuditStub{}
	r := gin.New()
	r.Use(Agent24KnowledgeAudit(stub))
	r.POST(agent24AuditPathUIExchange, h.Exchange)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, agent24AuditPathUIExchange, strings.NewReader(body))
	if proof != "" {
		req.Header.Set(agent24UIViewProofHeader, proof)
	}
	if remote == "" {
		remote = "203.0.113.10:10"
	}
	req.RemoteAddr = remote
	r.ServeHTTP(w, req)
	return w, stub
}

func agent24UIExchangeCall(
	h *Agent24KnowledgeUIExchangeHandler, raw, proof, remote string,
) (*httptest.ResponseRecorder, *agent24AuditStub) {
	return agent24UIPostExchange(h, `{"ticket":"`+raw+`"}`, proof, remote)
}

type agent24UIAuditView struct {
	Outcome      string
	Reason       string
	Principal    *agent24UIAuditPrincipal
	CredentialID *string
	TenantID     *uint64
	Resource     map[string]any
}

type agent24UIAuditPrincipal struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}

func agent24UIAuditOf(t *testing.T, stub *agent24AuditStub) (*types.AuditLog, agent24UIAuditView) {
	t.Helper()
	require.Len(t, stub.entries, 1)
	entry := stub.entries[0]
	var details struct {
		Outcome      string                   `json:"outcome"`
		Reason       string                   `json:"reason"`
		Principal    *agent24UIAuditPrincipal `json:"principal"`
		CredentialID *string                  `json:"credential_id"`
		TenantID     *uint64                  `json:"tenant_id"`
		Resource     map[string]any           `json:"resource"`
	}
	require.NoError(t, json.Unmarshal(entry.Details, &details))
	return entry, agent24UIAuditView{
		Outcome: details.Outcome, Reason: details.Reason, Principal: details.Principal,
		CredentialID: details.CredentialID, TenantID: details.TenantID, Resource: details.Resource,
	}
}

func TestAgent24UIExchangeHappy(t *testing.T) {
	db, store := agent24UIHandlerDB(t)
	proof, vph := agent24UIViewSecret()
	binding, partition := agent24UITestTag(1), agent24UITestTag(2)
	issuer := agent24UITicketsFor(store, agent24UIResolver(agent24UIKBs(9, "kb-a", "kb-b")...), nil)
	w := agent24UIServe(agent24UIHappyIdentity(nil).context(), issuer.Issue, agent24AuditPathUITickets,
		agent24UITicketJSON(binding, partition, vph, "4"))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var issued struct {
		Ticket   string `json:"ticket"`
		TicketID string `json:"ticket_id"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &issued))
	row := agent24UITicketRow(t, db, issued.TicketID)
	require.Equal(t, uint64(7), row.KeyID)
	require.Equal(t, "", row.PrincipalMode)

	h := agent24UIExchangeHandler(store, agent24UILiveKeysAB(), nil, nil, nil)
	logs := agent24CaptureCredentialLogs(t)
	got, stub := agent24UIExchangeCall(h, issued.Ticket, proof, "")
	require.Equal(t, http.StatusOK, got.Code, got.Body.String())
	require.Equal(t, "no-store", got.Header().Get("Cache-Control"))
	var resp struct {
		SessionToken   string   `json:"session_token"`
		ExpiresIn      int      `json:"expires_in"`
		Ops            []string `json:"ops"`
		EffectiveScope struct {
			KnowledgeBaseIDs []string `json:"knowledge_base_ids"`
		} `json:"effective_scope"`
	}
	require.NoError(t, json.Unmarshal(got.Body.Bytes(), &resp))
	require.LessOrEqual(t, resp.ExpiresIn, 600)
	require.Greater(t, resp.ExpiresIn, 0)
	require.Equal(t, []string{"kb-b", "kb-a"}, resp.EffectiveScope.KnowledgeBaseIDs)
	require.Equal(t, append([]string(nil), agent24uisession.UIOps...), resp.Ops)
	claims, err := agent24uisession.Verify(
		agent24UIExchangeKey(), agent24uisession.DefaultIssuer, resp.SessionToken, time.Now())
	require.NoError(t, err)
	require.Equal(t, "api_tenant:principal-7", claims.Subject)
	require.Equal(t, uint64(9), claims.TenantID)
	require.Equal(t, []string{"kb-b", "kb-a"}, claims.Scope.KnowledgeBaseIDs)
	require.Equal(t, agent24uisession.UIOps, claims.Ops)
	require.Equal(t, issued.TicketID, claims.TicketID)
	require.Equal(t, vph, claims.Cnf.VPH)
	require.NotNil(t, claims.Gen)
	require.Equal(t, int64(4), *claims.Gen)
	require.Equal(t, "key:7", claims.ParentCredentialID)
	require.Equal(t, binding, claims.BindingTag)
	require.Equal(t, partition, claims.PartitionTag)

	entry, view := agent24UIAuditOf(t, stub)
	require.Equal(t, types.AuditAction("knowledge.ui.exchange"), entry.Action)
	require.Equal(t, types.AuditOutcomeSuccess, entry.Outcome)
	require.Equal(t, "ok", view.Outcome)
	require.Equal(t, "api_tenant", view.Principal.Type)
	require.Equal(t, "principal-7", view.Principal.ID)
	require.Equal(t, "key:7", *view.CredentialID)
	require.Equal(t, uint64(9), *view.TenantID)
	require.NotContains(t, view.Resource, "internal_reason")
	require.NotContains(t, view.Resource, "leak")
	blob := agent24AuditBlob(t, entry)
	for _, secret := range []string{issued.Ticket, resp.SessionToken, proof, vph} {
		require.NotContains(t, blob, secret)
		require.NotContains(t, logs.String(), secret)
	}
}

func TestAgent24UIExchangeOrderAndIdenticalBodies(t *testing.T) {
	proof, vph := agent24UIViewSecret()
	type step struct {
		name, raw, body, proof string
		tweak                  func(*types.Agent24UITicket)
		before                 func(t *testing.T, db *gorm.DB, store interfaces.Agent24UISessionStore, raw string)
		reason                 string
	}
	bad := agent24UIRawTicket(1)
	steps := []step{
		{name: "body", body: `{"ticket":1}`, reason: ""},
		{name: "body_null", body: `{"ticket":null}`, reason: ""},
		{name: "ticket_malformed", body: `{"ticket":"short"}`, reason: "ticket_malformed"},
		{name: "ticket_unknown", raw: bad, proof: proof, reason: "ticket_unknown"},
		{
			name: "ticket_replayed", raw: agent24UIRawTicket(2), proof: proof, reason: "ticket_replayed",
			before: func(t *testing.T, _ *gorm.DB, store interfaces.Agent24UISessionStore, raw string) {
				hash, err := agent24uisession.TicketHash(raw)
				require.NoError(t, err)
				_, first, err := store.ConsumeTicket(context.Background(), hash, time.Now())
				require.NoError(t, err)
				require.True(t, first)
			},
		},
		{
			name: "ticket_expired", raw: agent24UIRawTicket(3), proof: proof, reason: "ticket_expired",
			tweak: func(ticket *types.Agent24UITicket) { ticket.ExpiresAt = time.Now().Add(-time.Second) },
		},
		{
			name: "ticket_revoked", raw: agent24UIRawTicket(4), proof: proof, reason: "ticket_revoked",
			tweak: func(ticket *types.Agent24UITicket) {
				revoked := time.Now().Add(-time.Second)
				ticket.RevokedAt = &revoked
			},
		},
		{
			name: "family_revoked", raw: agent24UIRawTicket(5), proof: proof, reason: "family_revoked",
			before: func(t *testing.T, _ *gorm.DB, store interfaces.Agent24UISessionStore, _ string) {
				require.NoError(t, store.RevokeFamily(
					context.Background(), 9, "api_tenant:principal-7", vph, time.Now()))
			},
		},
		{
			name: "view_proof_mismatch", raw: agent24UIRawTicket(6), proof: agent24UITestTag(8),
			reason: "view_proof_mismatch",
		},
	}
	var invalid [][]byte
	for _, tc := range steps {
		t.Run(tc.name, func(t *testing.T) {
			_, store := agent24UIHandlerDB(t)
			// ticket_unknown must stay absent: seeding it would consume a real row and
			// then fail later in recheck (nil keys → credential_revoked).
			if tc.raw != "" && tc.reason != "ticket_unknown" {
				agent24UISeedTicket(t, store, tc.raw, vph, tc.tweak)
			}
			if tc.before != nil {
				tc.before(t, nil, store, tc.raw)
			}
			h := agent24UIExchangeHandler(store, nil, nil, nil, nil)
			body := tc.body
			if body == "" {
				body = `{"ticket":"` + tc.raw + `"}`
			}
			w, stub := agent24UIPostExchange(h, body, tc.proof, "")
			if tc.name == "body" || tc.name == "body_null" {
				require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
				code, reason, _ := agent24UIError(t, w)
				require.Equal(t, "invalid_request", code)
				require.Empty(t, reason)
				return
			}
			require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
			code, reason, _ := agent24UIError(t, w)
			require.Equal(t, "unauthorized", code)
			require.Equal(t, "credential_invalid", reason)
			_, view := agent24UIAuditOf(t, stub)
			require.Equal(t, tc.reason, view.Resource["internal_reason"])
			invalid = append(invalid, append([]byte(nil), w.Body.Bytes()...))
			for _, secret := range []string{tc.raw, tc.proof, vph} {
				if secret != "" {
					require.NotContains(t, w.Body.String(), secret)
				}
			}
		})
	}
	require.Len(t, invalid, 7)
	for i := 1; i < len(invalid); i++ {
		require.Equal(t, string(invalid[0]), string(invalid[i]), "body %d differs", i)
	}
}

func TestAgent24UIExchangeFamilyRevokedOnReplayAndMismatch(t *testing.T) {
	proof, vph := agent24UIViewSecret()
	db, store := agent24UIHandlerDB(t)
	agent24UISeedTicket(t, store, agent24UIRawTicket(11), vph, nil)
	agent24UISeedTicket(t, store, agent24UIRawTicket(12), vph, nil)
	hash, err := agent24uisession.TicketHash(agent24UIRawTicket(11))
	require.NoError(t, err)
	_, consumed, err := store.ConsumeTicket(context.Background(), hash, time.Now())
	require.NoError(t, err)
	require.True(t, consumed)
	h := agent24UIExchangeHandler(store, agent24UILiveKeysAB(), nil, nil, nil)
	w, stub := agent24UIExchangeCall(h, agent24UIRawTicket(11), proof, "203.0.113.20:1")
	require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
	_, view := agent24UIAuditOf(t, stub)
	require.Equal(t, true, view.Resource["leak"])
	require.Equal(t, "ticket_replayed", view.Resource["internal_reason"])
	require.Equal(t, agent24uisession.H("kb-vph-audit-v1", []byte(vph)), view.Resource["vph_hash"])
	// A reused model keeps the previous primary key, and Gorm ANDs it into the next WHERE.
	fam := types.Agent24UIFamily{}
	require.NoError(t, db.Where("vph = ?", vph).Take(&fam).Error)
	require.NotNil(t, fam.RevokedAt)
	w, _ = agent24UIExchangeCall(h, agent24UIRawTicket(12), proof, "203.0.113.21:1")
	require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())

	db, store = agent24UIHandlerDB(t)
	secret2 := bytes.Repeat([]byte{5}, 32)
	proof2 := base64.RawURLEncoding.EncodeToString(secret2)
	vph2 := agent24uisession.VPH(secret2)
	agent24UISeedTicket(t, store, agent24UIRawTicket(13), vph2, nil)
	agent24UISeedTicket(t, store, agent24UIRawTicket(14), vph2, nil)
	h = agent24UIExchangeHandler(store, agent24UILiveKeysAB(), nil, nil, nil)
	w, stub = agent24UIExchangeCall(h, agent24UIRawTicket(13), agent24UITestTag(7), "203.0.113.22:1")
	require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
	_, view = agent24UIAuditOf(t, stub)
	require.Equal(t, true, view.Resource["leak"])
	require.Equal(t, "view_proof_mismatch", view.Resource["internal_reason"])
	fam2 := types.Agent24UIFamily{}
	require.NoError(t, db.Where("vph = ?", vph2).Take(&fam2).Error)
	require.NotNil(t, fam2.RevokedAt)
	w, _ = agent24UIExchangeCall(h, agent24UIRawTicket(14), proof2, "203.0.113.23:1")
	require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
}

func TestAgent24UIExchangeConcurrent(t *testing.T) {
	dir := t.TempDir()
	dsn := dir + "/ui.db?_busy_timeout=5000&_journal_mode=WAL"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(8)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, apprepo.EnsureAgent24UISessionTables(db))
	store := apprepo.NewAgent24UISessionStore(db)
	proof, vph := agent24UIViewSecret()
	raw := agent24UIRawTicket(21)
	agent24UISeedTicket(t, store, raw, vph, nil)
	h := agent24UIExchangeHandler(store, agent24UILiveKeysAB(), nil, nil, nil)

	const n = 8
	codes := make([]int, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			<-start
			w, _ := agent24UIExchangeCall(h, raw, proof, "203.0.113.30:1")
			codes[i] = w.Code
		}(i)
	}
	close(start)
	wg.Wait()
	// U2.8: one ticket presented twice is a leak, and the whole family is revoked. A host keeps one
	// exchange in flight per view (U2.9), so concurrent presentations are a replay. At most one caller
	// gets a session, and the family ends revoked whichever order the store saw them in.
	ok := 0
	for _, code := range codes {
		if code == http.StatusOK {
			ok++
		} else {
			require.Equal(t, http.StatusUnauthorized, code)
		}
	}
	require.LessOrEqual(t, ok, 1)
	fam := types.Agent24UIFamily{}
	require.NoError(t, db.Where("vph = ?", vph).Take(&fam).Error)
	require.NotNil(t, fam.RevokedAt)
}

func TestAgent24UIExchangeRecheck(t *testing.T) {
	proof, vph := agent24UIViewSecret()
	past := time.Now().Add(-time.Second)
	soon := time.Now().Add(time.Hour)
	cases := []struct {
		name       string
		key        *types.TenantAPIKey
		tenant     *types.Tenant
		rev        *agent24RevocationStub
		tweak      func(*types.Agent24UITicket)
		status     int
		reason     string
		scope      []string
		resolveHit int
	}{
		{name: "key revoked", status: http.StatusUnauthorized, reason: "credential_revoked"},
		{
			name: "key expired", key: agent24UILiveKey([]string{"kb-b", "kb-a"}, false, nil, &past),
			status: http.StatusUnauthorized, reason: "credential_expired",
		},
		{
			name: "recorded expiry", key: agent24UILiveKey([]string{"kb-b", "kb-a"}, false, nil, &soon),
			tweak:  func(ticket *types.Agent24UITicket) { ticket.CredentialExpiresAt = &past },
			status: http.StatusUnauthorized, reason: "credential_expired",
		},
		{
			name: "jti revoked", key: agent24UILiveKey([]string{"kb-b", "kb-a"}, false, nil, nil),
			rev:    &agent24RevocationStub{revoked: true},
			tweak:  func(ticket *types.Agent24UITicket) { ticket.CredentialID = "jti:abc" },
			status: http.StatusUnauthorized, reason: "credential_revoked",
		},
		{
			name: "revocation error", key: agent24UILiveKey([]string{"kb-b", "kb-a"}, false, nil, nil),
			rev:    &agent24RevocationStub{err: errors.New("list down")},
			tweak:  func(ticket *types.Agent24UITicket) { ticket.CredentialID = "jti:abc" },
			status: http.StatusInternalServerError,
		},
		{
			name: "mode changed", key: agent24UILiveKey([]string{"kb-b", "kb-a"}, false, nil, nil),
			tenant: &types.Tenant{
				ID: 9, APIPrincipalConfig: &types.APIPrincipalConfig{Mode: types.APIPrincipalModeSignedToken},
			},
			tweak:  func(ticket *types.Agent24UITicket) { ticket.PrincipalMode = "tenant" },
			status: http.StatusUnauthorized, reason: "credential_invalid",
		},
		{
			name: "retrieve lost", key: agent24UILiveKey([]string{"kb-b", "kb-a"}, false, []string{"chat"}, nil),
			status: http.StatusUnauthorized, reason: "credential_invalid",
		},
		{
			name:   "platform key",
			key:    agent24UIKeyWith(func(k *types.TenantAPIKey) { k.ScopeType = types.APIKeyScopePlatform }),
			status: http.StatusUnauthorized, reason: "credential_invalid",
		},
		{
			// The tenant's key list does not hold it, so the recorded key is gone.
			name: "other tenant key", key: agent24UIKeyWith(func(k *types.TenantAPIKey) {
				other := uint64(10)
				k.TenantID = &other
			}),
			status: http.StatusUnauthorized, reason: "credential_revoked",
		},
		{
			name: "narrowed", key: agent24UILiveKey([]string{"kb-a"}, false, nil, nil),
			status: http.StatusOK, scope: []string{"kb-a"}, resolveHit: 1,
		},
		{
			name: "narrowed empty", key: agent24UILiveKey([]string{"kb-z"}, false, nil, nil),
			status: http.StatusForbidden, reason: "empty_scope", resolveHit: 1,
		},
		{
			name: "full access", key: agent24UILiveKey([]string{"kb-a"}, true, nil, nil),
			status: http.StatusForbidden, reason: "empty_scope",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, store := agent24UIHandlerDB(t)
			agent24UISeedTicket(t, store, agent24UIRawTicket(40), vph, tc.tweak)
			calls := &agent24UIScopeCalls{inner: agent24UIResolver(agent24UIKBs(9, "kb-a", "kb-b", "kb-z")...)}
			var keys []*types.TenantAPIKey
			if tc.key != nil {
				keys = []*types.TenantAPIKey{tc.key}
			}
			var rev interfaces.Agent24CredentialRevocationStore
			if tc.rev != nil {
				rev = tc.rev
			}
			h := agent24UIExchangeHandler(store, keys, tc.tenant, rev, calls)
			w, _ := agent24UIExchangeCall(h, agent24UIRawTicket(40), proof, "")
			require.Equal(t, tc.status, w.Code, w.Body.String())
			switch tc.status {
			case http.StatusOK:
				var resp struct {
					EffectiveScope struct {
						KnowledgeBaseIDs []string `json:"knowledge_base_ids"`
					} `json:"effective_scope"`
				}
				require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
				require.Equal(t, tc.scope, resp.EffectiveScope.KnowledgeBaseIDs)
			case http.StatusInternalServerError:
				code, _, _ := agent24UIError(t, w)
				require.Equal(t, "internal", code)
				require.NotContains(t, w.Body.String(), "list down")
				require.NotContains(t, w.Body.String(), "jti:abc")
			default:
				_, reason, _ := agent24UIError(t, w)
				require.Equal(t, tc.reason, reason)
			}
			require.Equal(t, tc.resolveHit, calls.calls)
		})
	}
}

func TestAgent24UIExchangeLifetime(t *testing.T) {
	proof, vph := agent24UIViewSecret()
	_, store := agent24UIHandlerDB(t)
	agent24UISeedTicket(t, store, agent24UIRawTicket(50), vph, func(ticket *types.Agent24UITicket) {
		later := time.Now().Add(time.Hour)
		ticket.CredentialExpiresAt = &later
	})
	keyExp := time.Now().Add(5 * time.Minute)
	h := agent24UIExchangeHandler(store, []*types.TenantAPIKey{
		agent24UILiveKey([]string{"kb-b", "kb-a"}, false, nil, &keyExp),
	}, nil, nil, nil)
	w, _ := agent24UIExchangeCall(h, agent24UIRawTicket(50), proof, "203.0.113.50:1")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp struct {
		ExpiresIn int `json:"expires_in"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.GreaterOrEqual(t, resp.ExpiresIn, 298)
	require.LessOrEqual(t, resp.ExpiresIn, 300)

	_, store = agent24UIHandlerDB(t)
	agent24UISeedTicket(t, store, agent24UIRawTicket(51), vph, func(ticket *types.Agent24UITicket) {
		later := time.Now().Add(time.Hour)
		ticket.CredentialExpiresAt = &later
	})
	short := time.Now().Add(800 * time.Millisecond)
	h = agent24UIExchangeHandler(store, []*types.TenantAPIKey{
		agent24UILiveKey([]string{"kb-b", "kb-a"}, false, nil, &short),
	}, nil, nil, nil)
	w, _ = agent24UIExchangeCall(h, agent24UIRawTicket(51), proof, "203.0.113.51:1")
	require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
	_, reason, _ := agent24UIError(t, w)
	require.Equal(t, "credential_expired", reason)
}

func TestAgent24UIExchangeRateLimit(t *testing.T) {
	proof, vph := agent24UIViewSecret()
	db, store := agent24UIHandlerDB(t)
	raw := agent24UIRawTicket(60)
	seeded := agent24UISeedTicket(t, store, raw, vph, nil)
	h := agent24UIExchangeHandler(store, []*types.TenantAPIKey{
		agent24UILiveKey([]string{"kb-b", "kb-a"}, false, nil, nil),
	}, nil, nil, nil)
	clock := time.Now()
	h.limit.now = func() time.Time { return clock }
	const remote = "203.0.113.60:1"
	for i := 0; i < agent24UIExchangeBurst; i++ {
		w, _ := agent24UIPostExchange(h, `{"ticket":1}`, "", remote)
		require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
	}
	w, _ := agent24UIExchangeCall(h, raw, proof, remote)
	require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
	_, reason, details := agent24UIError(t, w)
	require.Equal(t, "overloaded", reason)
	require.Equal(t, true, details["retryable"])
	row := agent24UITicketRow(t, db, seeded.TicketID)
	require.Nil(t, row.ConsumedAt)

	clock = clock.Add(time.Second)
	w, _ = agent24UIExchangeCall(h, raw, proof, remote)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
}

func TestAgent24UICredentialBypassIsExact(t *testing.T) {
	gin.SetMode(gin.TestMode)
	platform := types.TenantAPIKeyScope{KeyID: 1, ScopeType: types.APIKeyScopePlatform}
	newEngine := func() *gin.Engine {
		r := gin.New()
		r.Use(func(c *gin.Context) {
			c.Request = c.Request.WithContext(types.WithTenantAPIKeyScope(c.Request.Context(), platform))
			c.Next()
		})
		r.Use(Agent24IntegrationCredentialGate(nil))
		r.POST(agent24AuditPathUIExchange, func(c *gin.Context) { c.Status(http.StatusNoContent) })
		r.GET(agent24AuditPathUISession, func(c *gin.Context) { c.Status(http.StatusNoContent) })
		r.POST(agent24AuditPathReady, func(c *gin.Context) { c.Status(http.StatusNoContent) })
		r.POST(agent24AuditPathUISession, func(c *gin.Context) { c.Status(http.StatusNoContent) })
		r.GET(agent24AuditPathUIExchange, func(c *gin.Context) { c.Status(http.StatusNoContent) })
		return r
	}
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodPost, agent24AuditPathUIExchange, http.StatusNoContent},
		{http.MethodGet, agent24AuditPathUISession, http.StatusNoContent},
		{http.MethodPost, agent24AuditPathReady, http.StatusUnauthorized},
		{http.MethodPost, agent24AuditPathUISession, http.StatusUnauthorized},
		{http.MethodGet, agent24AuditPathUIExchange, http.StatusUnauthorized},
	} {
		w := httptest.NewRecorder()
		newEngine().ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		require.Equal(t, tc.want, w.Code, "%s %s: %s", tc.method, tc.path, w.Body.String())
	}
}

// A key list that returned another tenant's key with the recorded id is still refused.
func TestAgent24UIRecheckStoredOtherTenantKey(t *testing.T) {
	other := uint64(10)
	key := agent24UIKeyWith(func(k *types.TenantAPIKey) { k.TenantID = &other })
	deps := agent24UIStoredDeps{
		tenants: agent24UITenantStub{tenant: &types.Tenant{ID: 9}},
		keys:    agent24UIKeyStub{keys: []*types.TenantAPIKey{key}, unfiltered: true},
		key:     agent24UIExchangeKey(),
	}
	ticket := &types.Agent24UITicket{TenantID: 9, KeyID: 7, CredentialID: "key:7"}
	_, reason, _, err := agent24UIRecheckStoredKey(context.Background(), deps, ticket, time.Now())
	require.NoError(t, err)
	require.Equal(t, "credential_invalid", reason)
}
