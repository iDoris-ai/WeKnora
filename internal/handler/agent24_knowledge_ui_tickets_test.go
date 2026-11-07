// Agent24 fork product patch A24-KB-32 (KB-C2-01b1, WeKnora#133, CONTRACT-UI-BRIDGE-v1 U2.1, U2.13, U3.7).

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

func agent24UITestTag(fill byte) string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{fill}, 32))
}

func agent24UITicketJSON(binding, partition, vph, generation string) string {
	return `{"binding_tag":"` + binding + `","partition_tag":"` + partition + `","vph":"` + vph +
		`","generation":` + generation + `}`
}

func agent24UIHandlerDB(t *testing.T) (*gorm.DB, interfaces.Agent24UISessionStore) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, apprepo.EnsureAgent24UISessionTables(db))
	return db, apprepo.NewAgent24UISessionStore(db)
}

type agent24UIKBStub struct {
	interfaces.KnowledgeBaseService
	rows []*types.KnowledgeBase
}

func (s agent24UIKBStub) GetKnowledgeBasesByIDsOnly(_ context.Context, ids []string) ([]*types.KnowledgeBase, error) {
	want := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		want[id] = struct{}{}
	}
	var out []*types.KnowledgeBase
	for _, kb := range s.rows {
		if kb == nil {
			continue
		}
		if _, ok := want[kb.ID]; ok {
			out = append(out, kb)
		}
	}
	return out, nil
}

func agent24UIResolver(rows ...*types.KnowledgeBase) agent24DefaultScopeResolver {
	return agent24DefaultScopeResolver{kbs: agent24UIKBStub{rows: rows}}
}

func agent24UIKBs(tenant uint64, ids ...string) []*types.KnowledgeBase {
	out := make([]*types.KnowledgeBase, len(ids))
	for i, id := range ids {
		out[i] = &types.KnowledgeBase{ID: id, TenantID: tenant}
	}
	return out
}

type agent24UIScopeCalls struct {
	inner agent24ScopeResolver
	calls int
}

func (s *agent24UIScopeCalls) Resolve(ctx context.Context, ids []string) ([]*types.KnowledgeBase, error) {
	s.calls++
	if s.inner == nil {
		return nil, nil
	}
	return s.inner.Resolve(ctx, ids)
}

type agent24UIErrScope struct{ err error }

func (s agent24UIErrScope) Resolve(context.Context, []string) ([]*types.KnowledgeBase, error) {
	return nil, s.err
}

type agent24UIFailStore struct {
	interfaces.Agent24UISessionStore
	err error
}

func (s agent24UIFailStore) CreateTicket(context.Context, *types.Agent24UITicket) error { return s.err }
func (s agent24UIFailStore) RevokeTicket(context.Context, uint64, string, string, time.Time) error {
	return s.err
}

func (s agent24UIFailStore) RevokeFamily(context.Context, uint64, string, string, time.Time) error {
	return s.err
}

type agent24UIIdentity struct {
	principal types.Principal
	tenant    uint64
	scope     *types.TenantAPIKeyScope
	exp       *time.Time
	jti       string
}

func agent24UIHappyIdentity(exp *time.Time) agent24UIIdentity {
	return agent24UIIdentity{
		principal: types.Principal{Type: types.PrincipalAPITenant, ID: "principal-7"},
		tenant:    9,
		scope: &types.TenantAPIKeyScope{
			KeyID:            7,
			KnowledgeBaseIDs: types.StringArray{"kb-b", "kb-a"},
		},
		exp: exp,
	}
}

func (id agent24UIIdentity) context() context.Context {
	ctx := types.WithCaller(context.Background(), types.Caller{TenantID: id.tenant})
	if id.principal.Valid() {
		ctx = types.WithPrincipal(ctx, id.principal)
	}
	if id.scope != nil {
		ctx = types.WithTenantAPIKeyScope(ctx, *id.scope)
	}
	if id.jti != "" {
		ctx = types.WithAgent24TokenJTI(ctx, id.jti)
	}
	if id.exp != nil {
		ctx = types.WithAgent24CredentialExpiry(ctx, *id.exp)
	}
	return ctx
}

func agent24UITicketsFor(
	store interfaces.Agent24UISessionStore, scope agent24ScopeResolver, recheck agent24CredentialRechecker,
) *Agent24KnowledgeUITicketsHandler {
	if recheck == nil {
		recheck = agent24FixedRecheck{}
	}
	return &Agent24KnowledgeUITicketsHandler{scope: scope, recheck: recheck, store: store}
}

func agent24UIServe(ctx context.Context, h gin.HandlerFunc, path, body string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if ctx != nil {
			c.Request = c.Request.WithContext(ctx)
		}
		c.Next()
	})
	r.POST(path, h)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	return w
}

func agent24UIAuditServe(
	t *testing.T, stub *agent24AuditStub, id agent24UIIdentity, path, body string, h gin.HandlerFunc,
) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(Agent24KnowledgeAudit(stub))
	r.Use(func(c *gin.Context) {
		ctx := id.context()
		c.Request = c.Request.WithContext(ctx)
		if p, ok := types.PrincipalFromContext(ctx); ok {
			c.Set(types.PrincipalContextKey.String(), p)
		}
		c.Next()
	})
	r.POST(path, h)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	return w
}

func agent24UITokens(s string) []string {
	var out []string
	run, start := 0, 0
	flush := func(i int) {
		if run == 43 {
			out = append(out, s[start:i])
		}
		run = 0
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_'
		if ok {
			if run == 0 {
				start = i
			}
			run++
			continue
		}
		flush(i)
	}
	flush(len(s))
	return out
}

func agent24UIUnexpectedTokens(s string, allowed ...string) []string {
	allow := make(map[string]struct{}, len(allowed))
	for _, a := range allowed {
		allow[a] = struct{}{}
	}
	var bad []string
	for _, tok := range agent24UITokens(s) {
		if _, ok := allow[tok]; !ok {
			bad = append(bad, tok)
		}
	}
	return bad
}

func agent24UIError(t *testing.T, w *httptest.ResponseRecorder) (string, string, map[string]any) {
	t.Helper()
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"), w.Body.String())
	var body struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body), w.Body.String())
	reason, _ := body.Error.Details["reason"].(string)
	return body.Error.Code, reason, body.Error.Details
}

func agent24UITicketRow(t *testing.T, db *gorm.DB, id string) types.Agent24UITicket {
	t.Helper()
	var row types.Agent24UITicket
	require.NoError(t, db.Where("ticket_id = ?", id).Take(&row).Error)
	return row
}

func agent24UICount(t *testing.T, db *gorm.DB, model any) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Model(model).Count(&n).Error)
	return n
}

func TestAgent24UIHandlerKeyGate(t *testing.T) {
	t.Setenv(Agent24KnowledgeModeEnv, Agent24KnowledgeModeIntegration)
	t.Setenv(agent24uisession.KeyEnv, "")
	t.Setenv(Agent24KnowledgeTagKeyEnv, "")
	h, err := NewAgent24KnowledgeUITicketsHandler(nil, nil, nil, nil, nil, nil, nil, nil)
	require.NoError(t, err)
	require.Nil(t, h)
	rev, err := NewAgent24KnowledgeUIRevocationsHandler(nil, nil, nil, nil, nil, nil, nil)
	require.NoError(t, err)
	require.Nil(t, rev)

	t.Setenv(agent24uisession.KeyEnv, "!!!")
	h, err = NewAgent24KnowledgeUITicketsHandler(nil, nil, nil, nil, nil, nil, nil, nil)
	require.Error(t, err)
	require.Nil(t, h)
	rev, err = NewAgent24KnowledgeUIRevocationsHandler(nil, nil, nil, nil, nil, nil, nil)
	require.Error(t, err)
	require.Nil(t, rev)

	t.Setenv(agent24uisession.KeyEnv, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32)))
	h, err = NewAgent24KnowledgeUITicketsHandler(nil, nil, nil, nil, nil, nil, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, h)
	rev, err = NewAgent24KnowledgeUIRevocationsHandler(nil, nil, nil, nil, nil, nil, nil)
	require.NoError(t, err)
	require.NotNil(t, rev)

	// Standalone never builds them, even with a valid key: no integration or credential gate guards them.
	t.Setenv(Agent24KnowledgeModeEnv, Agent24KnowledgeModeStandalone)
	h, err = NewAgent24KnowledgeUITicketsHandler(nil, nil, nil, nil, nil, nil, nil, nil)
	require.NoError(t, err)
	require.Nil(t, h)
	rev, err = NewAgent24KnowledgeUIRevocationsHandler(nil, nil, nil, nil, nil, nil, nil)
	require.NoError(t, err)
	require.Nil(t, rev)
}

func TestAgent24UITicketIssue(t *testing.T) {
	db, store := agent24UIHandlerDB(t)
	binding, partition, vph := agent24UITestTag(1), agent24UITestTag(2), agent24UITestTag(3)
	body := agent24UITicketJSON(binding, partition, vph, "7")
	id := agent24UIHappyIdentity(nil)
	scope := &agent24UIScopeCalls{inner: agent24UIResolver(agent24UIKBs(id.tenant, "kb-a", "kb-b")...)}
	h := agent24UITicketsFor(store, scope, nil)
	logs := agent24CaptureCredentialLogs(t)
	before := time.Now()
	w := agent24UIServe(id.context(), h.Issue, agent24AuditPathUITickets, body)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	var resp struct {
		Ticket    string `json:"ticket"`
		TicketID  string `json:"ticket_id"`
		ExpiresIn int    `json:"expires_in"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), w.Body.String())
	var keys []string
	var raw map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &raw))
	for k := range raw {
		keys = append(keys, k)
	}
	require.ElementsMatch(t, []string{"ticket", "ticket_id", "expires_in"}, keys)
	require.Len(t, resp.Ticket, 43)
	require.Equal(t, 30, resp.ExpiresIn)
	require.Equal(t, 1, scope.calls)
	row := agent24UITicketRow(t, db, resp.TicketID)
	hash, err := agent24uisession.TicketHash(resp.Ticket)
	require.NoError(t, err)
	require.Equal(t, hash, row.TicketHash)
	require.Equal(t, id.principal.StorageID(), row.Principal)
	require.Equal(t, "key:7", row.CredentialID)
	require.Equal(t, binding, row.BindingTag)
	require.Equal(t, partition, row.PartitionTag)
	require.Equal(t, vph, row.VPH)
	require.Equal(t, int64(7), row.Generation)
	require.Nil(t, row.CredentialExpiresAt)
	gotScope, err := row.ScopeKnowledgeBaseIDs()
	require.NoError(t, err)
	require.Equal(t, []string{"kb-b", "kb-a"}, gotScope)
	require.WithinDuration(t, before.Add(agent24uisession.TicketDefaultTTL), row.ExpiresAt, 3*time.Second)
	require.NotContains(t, logs.String(), resp.Ticket)
	require.Empty(t, agent24UIUnexpectedTokens(logs.String()))
}

func TestAgent24UITicketGenerationOnePointZero(t *testing.T) {
	db, store := agent24UIHandlerDB(t)
	binding, partition, vph := agent24UITestTag(4), agent24UITestTag(5), agent24UITestTag(6)
	id := agent24UIHappyIdentity(nil)
	h := agent24UITicketsFor(store, agent24UIResolver(agent24UIKBs(id.tenant, "kb-a", "kb-b")...), nil)
	body := agent24UITicketJSON(binding, partition, vph, "1.0")
	w := agent24UIServe(id.context(), h.Issue, agent24AuditPathUITickets, body)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp struct {
		TicketID string `json:"ticket_id"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, int64(1), agent24UITicketRow(t, db, resp.TicketID).Generation)
}

func TestAgent24UITicketEmptyScope(t *testing.T) {
	db, store := agent24UIHandlerDB(t)
	binding, partition, vph := agent24UITestTag(1), agent24UITestTag(2), agent24UITestTag(3)
	body := agent24UITicketJSON(binding, partition, vph, "7")
	inner := agent24UIResolver(agent24UIKBs(9, "kb-a", "kb-b")...)
	cases := []struct {
		name  string
		id    agent24UIIdentity
		inner agent24ScopeResolver
	}{
		{
			name: "full access",
			id: func() agent24UIIdentity {
				id := agent24UIHappyIdentity(nil)
				id.scope.FullAccess = true
				return id
			}(),
		},
		{
			name: "empty allow-list",
			id: func() agent24UIIdentity {
				id := agent24UIHappyIdentity(nil)
				id.scope.KnowledgeBaseIDs = nil
				return id
			}(),
		},
		{
			name: "no api key scope",
			id: func() agent24UIIdentity {
				id := agent24UIHappyIdentity(nil)
				id.scope = nil
				id.jti = "jti-bearer"
				return id
			}(),
		},
		{
			name:  "other tenant",
			id:    agent24UIHappyIdentity(nil),
			inner: agent24UIResolver(agent24UIKBs(8, "kb-a", "kb-b")...),
		},
		{
			name:  "missing knowledge bases",
			id:    agent24UIHappyIdentity(nil),
			inner: agent24UIResolver(),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolver := tc.inner
			if resolver == nil {
				resolver = inner
			}
			calls := &agent24UIScopeCalls{inner: resolver}
			h := agent24UITicketsFor(store, calls, nil)
			w := agent24UIServe(tc.id.context(), h.Issue, agent24AuditPathUITickets, body)
			require.Equal(t, http.StatusForbidden, w.Code, w.Body.String())
			code, reason, details := agent24UIError(t, w)
			require.Equal(t, "forbidden", code)
			require.Equal(t, "empty_scope", reason)
			require.Equal(t, false, details["retryable"])
			require.NotContains(t, w.Body.String(), `"ticket"`)
			if tc.inner == nil {
				require.Zero(t, calls.calls)
			} else {
				require.Equal(t, 1, calls.calls)
			}
			require.Zero(t, agent24UICount(t, db, &types.Agent24UITicket{}))
		})
	}
}

func TestAgent24UITicketNearExpiryAndCredential(t *testing.T) {
	db, store := agent24UIHandlerDB(t)
	binding, partition, vph := agent24UITestTag(1), agent24UITestTag(2), agent24UITestTag(3)
	body := agent24UITicketJSON(binding, partition, vph, "7")
	scope := agent24UIResolver(agent24UIKBs(9, "kb-a", "kb-b")...)

	soon := time.Now().Add(59 * time.Second)
	id := agent24UIHappyIdentity(&soon)
	h := agent24UITicketsFor(store, scope, nil)
	w := agent24UIServe(id.context(), h.Issue, agent24AuditPathUITickets, body)
	require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
	code, reason, details := agent24UIError(t, w)
	require.Equal(t, "unavailable", code)
	require.Equal(t, "credential_near_expiry", reason)
	require.Equal(t, true, details["retryable"])
	require.Zero(t, agent24UICount(t, db, &types.Agent24UITicket{}))

	later := time.Now().Add(61 * time.Second).Truncate(time.Second)
	id = agent24UIHappyIdentity(&later)
	w = agent24UIServe(id.context(), h.Issue, agent24AuditPathUITickets, body)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp struct {
		TicketID string `json:"ticket_id"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	row := agent24UITicketRow(t, db, resp.TicketID)
	require.NotNil(t, row.CredentialExpiresAt)
	require.True(t, row.CredentialExpiresAt.Equal(later))

	missing := []agent24UIIdentity{
		func() agent24UIIdentity {
			id := agent24UIHappyIdentity(nil)
			id.principal = types.Principal{}
			return id
		}(),
		func() agent24UIIdentity {
			id := agent24UIHappyIdentity(nil)
			id.tenant = 0
			return id
		}(),
		func() agent24UIIdentity {
			id := agent24UIHappyIdentity(nil)
			id.scope.KeyID = 0
			return id
		}(),
	}
	for _, id := range missing {
		w = agent24UIServe(id.context(), h.Issue, agent24AuditPathUITickets, body)
		require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
		code, reason, _ = agent24UIError(t, w)
		require.Equal(t, "unauthorized", code)
		require.Equal(t, "credential_invalid", reason)
	}
}

func TestAgent24UITicketRecheckAndFamily(t *testing.T) {
	db, store := agent24UIHandlerDB(t)
	binding, partition, vph := agent24UITestTag(7), agent24UITestTag(8), agent24UITestTag(9)
	body := agent24UITicketJSON(binding, partition, vph, "7")
	scope := agent24UIResolver(agent24UIKBs(9, "kb-a", "kb-b")...)
	id := agent24UIHappyIdentity(nil)

	h := agent24UITicketsFor(store, scope, agent24FixedRecheck{reason: "credential_revoked"})
	w := agent24UIServe(id.context(), h.Issue, agent24AuditPathUITickets, body)
	require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
	code, reason, _ := agent24UIError(t, w)
	require.Equal(t, "unauthorized", code)
	require.Equal(t, "credential_revoked", reason)
	require.Zero(t, agent24UICount(t, db, &types.Agent24UITicket{}))

	h = agent24UITicketsFor(store, scope, nil)
	w = agent24UIServe(id.context(), h.Issue, agent24AuditPathUITickets, body)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var first struct {
		Ticket string `json:"ticket"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &first))

	other := id
	other.principal.ID = "principal-8"
	logs := agent24CaptureCredentialLogs(t)
	w = agent24UIServe(other.context(), h.Issue, agent24AuditPathUITickets, body)
	require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
	code, reason, _ = agent24UIError(t, w)
	require.Equal(t, "unauthorized", code)
	require.Equal(t, "credential_invalid", reason)
	require.NotContains(t, w.Body.String(), "family")
	require.NotContains(t, w.Body.String(), first.Ticket)
	require.Empty(t, agent24UIUnexpectedTokens(w.Body.String()+logs.String(), binding, partition, vph))
	require.Equal(t, int64(1), agent24UICount(t, db, &types.Agent24UITicket{}))

	require.NoError(t, store.RevokeFamily(context.Background(), id.tenant, id.principal.StorageID(), vph, time.Now()))
	w = agent24UIServe(id.context(), h.Issue, agent24AuditPathUITickets, body)
	require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
	code, reason, _ = agent24UIError(t, w)
	require.Equal(t, "unauthorized", code)
	require.Equal(t, "credential_invalid", reason)
	require.NotContains(t, w.Body.String(), "family")
	require.Equal(t, int64(1), agent24UICount(t, db, &types.Agent24UITicket{}))
}

func TestAgent24UITicketStoreError(t *testing.T) {
	sentinel := errors.New("S3NT1NEL-store")
	id := agent24UIHappyIdentity(nil)
	h := agent24UITicketsFor(agent24UIFailStore{err: sentinel},
		agent24UIResolver(agent24UIKBs(id.tenant, "kb-a", "kb-b")...), nil)
	binding, partition, vph := agent24UITestTag(1), agent24UITestTag(2), agent24UITestTag(3)
	logs := agent24CaptureCredentialLogs(t)
	w := agent24UIServe(id.context(), h.Issue, agent24AuditPathUITickets,
		agent24UITicketJSON(binding, partition, vph, "7"))
	require.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
	code, reason, _ := agent24UIError(t, w)
	require.Equal(t, "internal", code)
	require.Empty(t, reason)
	require.NotContains(t, w.Body.String(), "S3NT1NEL")
	require.NotContains(t, logs.String(), "S3NT1NEL")
	require.Empty(t, agent24UIUnexpectedTokens(w.Body.String()+logs.String(), binding, partition, vph))
}

func TestAgent24UITicketResolveError(t *testing.T) {
	id := agent24UIHappyIdentity(nil)
	h := agent24UITicketsFor(agent24UIFailStore{err: errors.New("unused")},
		agent24UIErrScope{err: errors.New("S3NT1NEL-db")}, nil)
	w := agent24UIServe(id.context(), h.Issue, agent24AuditPathUITickets,
		agent24UITicketJSON(agent24UITestTag(1), agent24UITestTag(2), agent24UITestTag(3), "7"))
	require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
	code, reason, details := agent24UIError(t, w)
	require.Equal(t, "unavailable", code)
	require.Equal(t, "dependency", reason)
	require.Equal(t, true, details["retryable"])
	require.NotContains(t, w.Body.String(), "S3NT1NEL")
}

func TestAgent24UITicketShape(t *testing.T) {
	binding, partition, vph := agent24UITestTag(1), agent24UITestTag(2), agent24UITestTag(3)
	valid := agent24UITicketJSON(binding, partition, vph, "7")
	h := agent24UITicketsFor(agent24UIFailStore{err: errors.New("shape reached the store")}, nil, nil)
	id := agent24UIHappyIdentity(nil)
	gen := float64(7)
	cases := []struct {
		name string
		body string
		gen  *float64
	}{
		{"missing binding", `{"partition_tag":"` + partition + `","vph":"` + vph + `","generation":7}`, &gen},
		{"extra field", `{"binding_tag":"` + binding + `","partition_tag":"` + partition + `","vph":"` + vph +
			`","generation":7,"extra":true}`, &gen},
		{"tag 42", agent24UITicketJSON(strings.Repeat("A", 42), partition, vph, "7"), &gen},
		{"tag 44", agent24UITicketJSON(strings.Repeat("A", 44), partition, vph, "7"), &gen},
		{"tag padded", agent24UITicketJSON(strings.Repeat("A", 42)+"=", partition, vph, "7"), &gen},
		{"generation negative", agent24UITicketJSON(binding, partition, vph, "-1"), nil},
		{"generation string", agent24UITicketJSON(binding, partition, vph, `"7"`), nil},
		{"generation fraction", agent24UITicketJSON(binding, partition, vph, "1.5"), nil},
		{"generation missing", `{"binding_tag":"` + binding + `","partition_tag":"` + partition +
			`","vph":"` + vph + `"}`, nil},
		{"array", `[]`, nil},
		{"string", `"x"`, nil},
		{"null", `null`, nil},
		{"two objects", valid + `{"a":1}`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := agent24UIServe(id.context(), h.Issue, agent24AuditPathUITickets, tc.body)
			require.Equal(t, http.StatusBadRequest, w.Code, w.Body.String())
			code, reason, details := agent24UIError(t, w)
			require.Equal(t, "invalid_request", code)
			require.Empty(t, reason)
			require.Nil(t, details["request_id"])
			if tc.gen == nil {
				require.Nil(t, details["generation"])
			} else {
				require.Equal(t, *tc.gen, details["generation"])
			}
			require.Empty(t, agent24UIUnexpectedTokens(w.Body.String(), binding, partition, vph))
		})
	}
}

func TestAgent24UITicketAudit(t *testing.T) {
	_, store := agent24UIHandlerDB(t)
	binding, partition, vph := agent24UITestTag(1), agent24UITestTag(2), agent24UITestTag(3)
	body := agent24UITicketJSON(binding, partition, vph, "7")
	id := agent24UIHappyIdentity(nil)
	h := agent24UITicketsFor(store, agent24UIResolver(agent24UIKBs(id.tenant, "kb-a", "kb-b")...), nil)
	stub := &agent24AuditStub{}
	logs := agent24CaptureCredentialLogs(t)
	w := agent24UIAuditServe(t, stub, id, agent24AuditPathUITickets, body, h.Issue)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, "no-store", w.Header().Get("Cache-Control"))
	var resp struct {
		Ticket   string `json:"ticket"`
		TicketID string `json:"ticket_id"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Len(t, stub.entries, 1)
	entry := stub.entries[0]
	require.Equal(t, types.AuditAction("knowledge.ui.ticket"), entry.Action)
	require.Equal(t, types.AuditOutcomeSuccess, entry.Outcome)
	var details struct {
		Outcome        string         `json:"outcome"`
		EffectiveScope []string       `json:"effective_scope"`
		Resource       map[string]any `json:"resource"`
	}
	require.NoError(t, json.Unmarshal(entry.Details, &details))
	require.Equal(t, "ok", details.Outcome)
	require.Equal(t, []string{"kb-b", "kb-a"}, details.EffectiveScope)
	require.Len(t, details.Resource, 2)
	require.Equal(t, binding, details.Resource["binding_tag"])
	idHash := agent24uisession.H("kb-ticket-id-v1", []byte(resp.TicketID))
	require.Equal(t, idHash, details.Resource["ticket_id_hash"])
	blob := agent24AuditBlob(t, entry)
	require.NotContains(t, blob, resp.Ticket)
	require.NotContains(t, blob, vph)
	require.NotContains(t, blob, partition)
	require.NotContains(t, logs.String(), resp.Ticket)
	require.Empty(t, agent24UIUnexpectedTokens(blob, binding, idHash))
}

// agent24UIShortenRecheck stands in for a recheck that finds the credential's expiry shortened after Auth.
type agent24UIShortenRecheck struct{ exp time.Time }

func (r agent24UIShortenRecheck) Recheck(ctx context.Context, _ http.Header) (context.Context, string, error) {
	return types.WithAgent24CredentialExpiry(ctx, r.exp), "", nil
}

// The near-expiry check uses the expiry the recheck returns, not the one Auth saw.
func TestAgent24UITicketNearExpiryAfterRecheck(t *testing.T) {
	_, store := agent24UIHandlerDB(t)
	id := agent24UIHappyIdentity(nil) // Auth saw a credential that does not expire
	h := agent24UITicketsFor(store, agent24UIResolver(agent24UIKBs(id.tenant, "kb-a", "kb-b")...),
		agent24UIShortenRecheck{exp: time.Now().Add(30 * time.Second)})
	body := agent24UITicketJSON(agent24UITestTag(1), agent24UITestTag(2), agent24UITestTag(3), "1")
	w := agent24UIServe(id.context(), h.Issue, agent24AuditPathUITickets, body)
	require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
	_, reason, _ := agent24UIError(t, w)
	require.Equal(t, "credential_near_expiry", reason)
}

// U2.13: refusals after the request parses still carry the binding, and the ticket id hash once an id
// exists. Neither the raw ticket nor any tag other than the binding is recorded.
func TestAgent24UITicketRefusalAudit(t *testing.T) {
	binding, partition, vph := agent24UITestTag(1), agent24UITestTag(2), agent24UITestTag(3)
	body := agent24UITicketJSON(binding, partition, vph, "1")
	resource := func(t *testing.T, stub *agent24AuditStub) map[string]any {
		t.Helper()
		require.Len(t, stub.entries, 1)
		var details struct {
			Outcome  string         `json:"outcome"`
			Resource map[string]any `json:"resource"`
		}
		require.NoError(t, json.Unmarshal(stub.entries[0].Details, &details))
		require.NotEqual(t, "ok", details.Outcome)
		return details.Resource
	}

	soon := time.Now().Add(30 * time.Second)
	near := agent24UIHappyIdentity(&soon)
	_, store := agent24UIHandlerDB(t)
	h := agent24UITicketsFor(store, agent24UIResolver(agent24UIKBs(near.tenant, "kb-a", "kb-b")...), nil)
	stub := &agent24AuditStub{}
	w := agent24UIAuditServe(t, stub, near, agent24AuditPathUITickets, body, h.Issue)
	require.Equal(t, http.StatusServiceUnavailable, w.Code, w.Body.String())
	res := resource(t, stub)
	require.Equal(t, binding, res["binding_tag"])
	require.NotContains(t, res, "ticket_id_hash")

	id := agent24UIHappyIdentity(nil)
	failing := agent24UITicketsFor(agent24UIFailStore{err: errors.New("store down")},
		agent24UIResolver(agent24UIKBs(id.tenant, "kb-a", "kb-b")...), nil)
	stub = &agent24AuditStub{}
	w = agent24UIAuditServe(t, stub, id, agent24AuditPathUITickets, body, failing.Issue)
	require.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
	res = resource(t, stub)
	require.Equal(t, binding, res["binding_tag"])
	hash, ok := res["ticket_id_hash"].(string)
	require.True(t, ok)
	require.Len(t, hash, 43)
	blob := string(stub.entries[0].Details)
	require.NotContains(t, blob, partition)
	require.NotContains(t, blob, vph)
}

// A24-KB-33: issuance records the key id and the principal mode. KeyID 0 is credential_invalid once a
// credential id exists (a jti), and it does not insert a row.
func TestAgent24UITicketRecordsKeyID(t *testing.T) {
	db, store := agent24UIHandlerDB(t)
	h := agent24UITicketsFor(store, agent24UIResolver(agent24UIKBs(9, "kb-a", "kb-b")...), nil)
	body := agent24UITicketJSON(agent24UITestTag(1), agent24UITestTag(2), agent24UITestTag(3), "1")
	id := agent24UIHappyIdentity(nil)
	ctx := context.WithValue(id.context(), types.TenantInfoContextKey, &types.Tenant{
		ID: 9, APIPrincipalConfig: &types.APIPrincipalConfig{Mode: types.APIPrincipalModeSignedToken},
	})
	w := agent24UIServe(ctx, h.Issue, agent24AuditPathUITickets, body)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp struct {
		TicketID string `json:"ticket_id"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	row := agent24UITicketRow(t, db, resp.TicketID)
	require.Equal(t, uint64(7), row.KeyID)
	require.Equal(t, string(types.APIPrincipalModeSignedToken), row.PrincipalMode)

	zero := agent24UIHappyIdentity(nil)
	zero.scope.KeyID = 0
	zero.jti = "jti-zero-key"
	w = agent24UIServe(zero.context(), h.Issue, agent24AuditPathUITickets, body)
	require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
	_, reason, _ := agent24UIError(t, w)
	require.Equal(t, "credential_invalid", reason)
	require.Equal(t, int64(1), agent24UICount(t, db, &types.Agent24UITicket{}))
}
