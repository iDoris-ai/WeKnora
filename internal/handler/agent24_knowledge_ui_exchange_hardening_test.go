// Agent24 fork product patch A24-KB-33 (KB-C2-01b2, WeKnora#133, CONTRACT-UI-BRIDGE-v1 U2.7, U2.8, U3.4).

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
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/agent24uisession"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

func agent24UISignedTenant(secret string) *types.Tenant {
	return &types.Tenant{ID: 9, APIPrincipalConfig: &types.APIPrincipalConfig{
		Mode: types.APIPrincipalModeSignedToken, HMACSecret: secret,
	}}
}

// A rotated signed-token secret invalidates the tokens signed with the old one. Exchange and the probe
// cannot verify C again, so the secret tag recorded at issuance refuses the ticket (U3.4 "C 有效").
func TestAgent24UISecretRotated(t *testing.T) {
	proof, vph := agent24UIViewSecret()
	live := agent24UILiveKeysAB()
	signedWith := func(secret string) func(*types.Agent24UITicket) {
		return func(ticket *types.Agent24UITicket) {
			ticket.PrincipalMode = string(types.APIPrincipalModeSignedToken)
			tenant := agent24UISignedTenant(secret)
			ticket.PrincipalSecretTag = agent24UIPrincipalSecretTag(agent24UIExchangeKey(), tenant)
		}
	}
	for _, tc := range []struct {
		name, secret string
		status       int
	}{
		{name: "same secret", secret: "s3cret-one", status: http.StatusOK},
		{name: "rotated", secret: "s3cret-two", status: http.StatusUnauthorized},
	} {
		t.Run("exchange "+tc.name, func(t *testing.T) {
			_, store := agent24UIHandlerDB(t)
			raw := agent24UIRawTicket(90)
			agent24UISeedTicket(t, store, raw, vph, signedWith("s3cret-one"))
			h := agent24UIExchangeHandler(store, live, agent24UISignedTenant(tc.secret), nil, nil)
			w, _ := agent24UIExchangeCall(h, raw, proof, "203.0.113.90:1")
			require.Equal(t, tc.status, w.Code, w.Body.String())
			if tc.status != http.StatusOK {
				_, reason, _ := agent24UIError(t, w)
				require.Equal(t, "credential_invalid", reason)
			}
		})
		t.Run("probe "+tc.name, func(t *testing.T) {
			_, store := agent24UIHandlerDB(t)
			ticket := agent24UISeedTicket(t, store, agent24UIRawTicket(91), vph, signedWith("s3cret-one"))
			token := agent24UIIssueFor(t, ticket, agent24UIExchangeKey(), time.Now(), time.Minute)
			h := agent24UISessionHandler(store, live, agent24UISignedTenant(tc.secret), nil)
			w, _, _ := agent24UIProbeCall(t, h, "Bearer "+token, proof)
			require.Equal(t, tc.status, w.Code, w.Body.String())
		})
	}
}

// agent24UIRevokeSpy wraps the real store. It cancels the request after the ticket is consumed and
// records whether RevokeLeakedFamily still had a live context; fail makes it return an error.
type agent24UIRevokeSpy struct {
	interfaces.Agent24UISessionStore
	cancel   context.CancelFunc
	fail     bool
	called   bool
	liveCtx  bool
	revokeOK bool
}

func (s *agent24UIRevokeSpy) ConsumeTicket(
	ctx context.Context, hash string, now time.Time,
) (*types.Agent24UITicket, bool, error) {
	ticket, first, err := s.Agent24UISessionStore.ConsumeTicket(ctx, hash, now)
	if s.cancel != nil {
		s.cancel()
	}
	return ticket, first, err
}

func (s *agent24UIRevokeSpy) RevokeLeakedFamily(
	ctx context.Context, tenantID uint64, principal, vph string, now time.Time,
) error {
	s.called = true
	s.liveCtx = ctx.Err() == nil
	if s.fail {
		return errors.New("store down")
	}
	err := s.Agent24UISessionStore.RevokeLeakedFamily(ctx, tenantID, principal, vph, now)
	s.revokeOK = err == nil
	return err
}

func agent24UIExchangeWithContext(
	ctx context.Context, h *Agent24KnowledgeUIExchangeHandler, raw, proof string,
) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST(agent24AuditPathUIExchange, h.Exchange)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, agent24AuditPathUIExchange,
		strings.NewReader(`{"ticket":"`+raw+`"}`)).WithContext(ctx)
	req.Header.Set(agent24UIViewProofHeader, proof)
	req.RemoteAddr = "203.0.113.92:1"
	r.ServeHTTP(w, req)
	return w
}

// U2.8: a client that disconnects after presenting a leaked ticket does not cancel the family revocation.
func TestAgent24UILeakRevocationIgnoresCancel(t *testing.T) {
	proof, vph := agent24UIViewSecret()
	db, store := agent24UIHandlerDB(t)
	raw := agent24UIRawTicket(92)
	agent24UISeedTicket(t, store, raw, vph, nil)
	hash, err := agent24uisession.TicketHash(raw)
	require.NoError(t, err)
	_, first, err := store.ConsumeTicket(context.Background(), hash, time.Now())
	require.NoError(t, err)
	require.True(t, first)
	// The replay is decided right after the consume, which cancels the request.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	spy := &agent24UIRevokeSpy{Agent24UISessionStore: store, cancel: cancel}
	h := agent24UIExchangeHandler(spy, agent24UILiveKeysAB(), nil, nil, nil)
	w := agent24UIExchangeWithContext(ctx, h, raw, proof)
	require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
	require.True(t, spy.called)
	require.True(t, spy.liveCtx, "the revocation ran on the cancelled request context")
	require.True(t, spy.revokeOK)
	require.NotNil(t, agent24UIFamilyRow(t, db, vph).RevokedAt)
}

// A family revocation that fails is not answered as a completed leak response.
func TestAgent24UILeakRevocationFailureIsInternal(t *testing.T) {
	_, vph := agent24UIViewSecret()
	_, store := agent24UIHandlerDB(t)
	raw := agent24UIRawTicket(93)
	agent24UISeedTicket(t, store, raw, vph, nil)
	spy := &agent24UIRevokeSpy{Agent24UISessionStore: store, fail: true}
	h := agent24UIExchangeHandler(spy, agent24UILiveKeysAB(), nil, nil, nil)
	w, stub := agent24UIExchangeCall(h, raw, agent24UITestTag(8), "203.0.113.93:1")
	require.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
	code, _, _ := agent24UIError(t, w)
	require.Equal(t, "internal", code)
	require.NotContains(t, w.Body.String(), "store down")
	_, view := agent24UIAuditOf(t, stub)
	require.Equal(t, true, view.Resource["leak"])
}

// A full bucket table with no idle bucket sends new addresses to one shared overflow bucket: the limit
// still holds for them, and an address that has its own bucket is unaffected.
func TestAgent24UIExchangeLimiterOverflow(t *testing.T) {
	l := newAgent24UIExchangeLimiter()
	l.max = 1
	clock := time.Now()
	l.now = func() time.Time { return clock }
	require.True(t, l.allow("198.51.100.1")) // the only bucket, now not idle
	allowed := 0
	for i := 0; i < 3*agent24UIExchangeBurst; i++ {
		if l.allow("198.51.100." + string(rune('a'+i%20))) {
			allowed++
		}
	}
	require.Equal(t, agent24UIExchangeBurst, allowed)
	require.False(t, l.allow("198.51.100.200"))
	require.True(t, l.allow("198.51.100.1"))
	require.Len(t, l.buckets, 1)
}

// U2.7: when the anonymous audit bucket suppresses a row, the exchange response is still flushed, as it
// is for a row that is written, so the transfer does not tell an unknown ticket from a known one.
func TestAgent24UIExchangeFlushesSuppressedRow(t *testing.T) {
	_, store := agent24UIHandlerDB(t)
	h := agent24UIExchangeHandler(store, agent24UILiveKeysAB(), nil, nil, nil)
	gin.SetMode(gin.TestMode)
	stub := &agent24AuditStub{}
	r := gin.New()
	r.Use(agent24KnowledgeAudit(stub, newAgent24AnonymousAuditLimiter(time.Hour, 1), nil))
	r.POST(agent24AuditPathUIExchange, h.Exchange)
	call := func(fill byte) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, agent24AuditPathUIExchange,
			strings.NewReader(`{"ticket":"`+agent24UIRawTicket(fill)+`"}`))
		req.RemoteAddr = "203.0.113.94:1"
		r.ServeHTTP(w, req)
		return w
	}
	first := call(94)
	second := call(95)
	require.Equal(t, http.StatusUnauthorized, second.Code, second.Body.String())
	require.Equal(t, first.Body.String(), second.Body.String())
	require.Len(t, stub.entries, 1, "the second unknown-ticket row should have been suppressed")
	require.True(t, first.Flushed)
	require.True(t, second.Flushed)
}

// U2.8 end to end: a session taken from a ticket is refused after a replay, even when the family
// revocation write failed once and the database then recovered, with the ticket never presented again.
func TestAgent24UILeakRevocationFailureStillRefusesSession(t *testing.T) {
	proof, vph := agent24UIViewSecret()
	db, store := agent24UIHandlerDB(t)
	raw := agent24UIRawTicket(96)
	agent24UISeedTicket(t, store, raw, vph, nil)
	h := agent24UIExchangeHandler(store, agent24UILiveKeysAB(), nil, nil, nil)
	w, _ := agent24UIExchangeCall(h, raw, proof, "203.0.113.96:1")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	var resp struct {
		SessionToken string `json:"session_token"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	probe := agent24UISessionHandler(store, agent24UILiveKeysAB(), nil, nil)
	w, _, _ = agent24UIProbeCall(t, probe, "Bearer "+resp.SessionToken, proof)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	const name = "agent24_test_fail_family_update"
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "agent24_ui_families" {
			_ = tx.AddError(errors.New("write down"))
		}
	}))
	w, _ = agent24UIExchangeCall(h, raw, proof, "203.0.113.96:2")
	require.Equal(t, http.StatusInternalServerError, w.Code, w.Body.String())
	require.NoError(t, db.Callback().Update().Remove(name))

	w, _, _ = agent24UIProbeCall(t, probe, "Bearer "+resp.SessionToken, proof)
	require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
	_, reason, _ := agent24UIError(t, w)
	require.Equal(t, "credential_invalid", reason)
	require.Eventually(t, func() bool {
		var row types.Agent24UIFamily
		return db.Where("vph = ?", vph).Take(&row).Error == nil && row.RevokedAt != nil
	}, 5*time.Second, 10*time.Millisecond)
}

// U2.3 (David 2026-11-08, option B): tickets created and session JWTs issued before this process started
// are refused, so a leak revocation that only lived in the previous process cannot be outlived.
func TestAgent24UIEpochRefusesPreStart(t *testing.T) {
	t.Run("constructors take the process epoch", func(t *testing.T) {
		t.Setenv(Agent24KnowledgeModeEnv, Agent24KnowledgeModeIntegration)
		t.Setenv(Agent24KnowledgeTagKeyEnv, "")
		t.Setenv(agent24uisession.KeyEnv, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32)))
		ex, err := NewAgent24KnowledgeUIExchangeHandler(nil, nil, nil, nil, nil)
		require.NoError(t, err)
		require.NotNil(t, ex)
		require.Equal(t, agent24uisession.Epoch(), ex.epoch)
		pr, err := NewAgent24KnowledgeUISessionHandler(nil, nil, nil, nil)
		require.NoError(t, err)
		require.NotNil(t, pr)
		require.Equal(t, agent24uisession.Epoch(), pr.deps.epoch)
		tk, err := NewAgent24KnowledgeUITicketsHandler(nil, nil, nil, nil, nil, nil, nil, nil)
		require.NoError(t, err)
		require.NotNil(t, tk)
		require.Len(t, tk.key, 32)
	})

	proof, vph := agent24UIViewSecret()
	epoch := time.Now().Add(-time.Minute).Truncate(time.Second)

	t.Run("exchange", func(t *testing.T) {
		_, store := agent24UIHandlerDB(t)
		before := agent24UIRawTicket(97)
		agent24UISeedTicket(t, store, before, vph, func(ticket *types.Agent24UITicket) {
			ticket.CreatedAt = epoch.Add(-time.Second)
		})
		after := agent24UIRawTicket(98)
		agent24UISeedTicket(t, store, after, vph, func(ticket *types.Agent24UITicket) {
			ticket.CreatedAt = epoch
		})
		h := agent24UIExchangeHandler(store, agent24UILiveKeysAB(), nil, nil, nil)
		h.epoch = epoch
		w, stub := agent24UIExchangeCall(h, before, proof, "203.0.113.97:1")
		require.Equal(t, http.StatusUnauthorized, w.Code, w.Body.String())
		_, view := agent24UIAuditOf(t, stub)
		require.Equal(t, "ticket_before_start", view.Resource["internal_reason"])
		require.NotContains(t, view.Resource, "leak")
		unknown, _ := agent24UIExchangeCall(h, agent24UIRawTicket(99), proof, "203.0.113.97:2")
		require.Equal(t, unknown.Body.String(), w.Body.String(), "U2.7: same body as the other ticket refusals")
		w, _ = agent24UIExchangeCall(h, after, proof, "203.0.113.97:3")
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	})

	t.Run("probe", func(t *testing.T) {
		_, store := agent24UIHandlerDB(t)
		ticket := agent24UISeedTicket(t, store, agent24UIRawTicket(100), vph, nil)
		h := agent24UISessionHandler(store, agent24UILiveKeysAB(), nil, nil)
		h.deps.epoch = epoch
		old := agent24UIIssueFor(t, ticket, agent24UIExchangeKey(), epoch.Add(-2*time.Second), 10*time.Minute)
		w, stub, logs := agent24UIProbeCall(t, h, "Bearer "+old, proof)
		agent24UIRequireProbeRefusal(t, w, stub, logs, "credential_invalid", true, old, proof)
		fresh := agent24UIIssueFor(t, ticket, agent24UIExchangeKey(), epoch, 10*time.Minute)
		w, _, _ = agent24UIProbeCall(t, h, "Bearer "+fresh, proof)
		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	})
}
