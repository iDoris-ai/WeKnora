// Agent24 fork product patch A24-KB-26 (KB-C1-03 F3, WeKnora#41, CONTRACT-READONLY-v1 R10.2).

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"golang.org/x/time/rate"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// agent24TestClock is a settable clock for agent24RefusedKeyAuditLimiter.
type agent24TestClock struct{ t time.Time }

func (c *agent24TestClock) now() time.Time { return c.t }

func newAgent24TestRefusedLimiter(
	every time.Duration, burst, size int,
) (*agent24RefusedKeyAuditLimiter, *agent24TestClock) {
	clock := &agent24TestClock{t: time.Unix(1_800_000_000, 0)}
	l := newAgent24RefusedKeyAuditLimiter(every, burst, size)
	l.now = clock.now
	return l, clock
}

// agent24RefusedKeyRouter answers 401 on search after noting the rejection the X-Test-Credential header names,
// as Auth does: "key:<id>" is a revoked tenant key, "jti:<jti>" an expired signed_token.
func agent24RefusedKeyRouter(svc interfaces.AuditLogService, refused *agent24RefusedKeyAuditLimiter) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(agent24KnowledgeAudit(svc, nil, refused))
	r.POST("/api/v1/agent24/knowledge/search", func(c *gin.Context) {
		c.Set(types.Agent24CredentialRejectionKey, agent24TestRejection(c.GetHeader("X-Test-Credential")))
		c.AbortWithStatus(http.StatusUnauthorized)
	})
	return r
}

func agent24TestRejection(credential string) types.Agent24CredentialRejection {
	if jti, ok := strings.CutPrefix(credential, "jti:"); ok {
		return types.Agent24CredentialRejection{
			Reason:       "credential_expired",
			Principal:    types.Principal{Type: types.PrincipalAPIExternalUser, ID: "7:user-1"},
			CredentialID: credential,
			TenantID:     7,
			JTI:          jti,
		}
	}
	return types.Agent24CredentialRejection{Reason: "credential_revoked", CredentialID: credential, TenantID: 7}
}

func agent24RefusedKeyRequest(t *testing.T, r *gin.Engine, credential string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent24/knowledge/search", nil)
	req.Header.Set("X-Test-Credential", credential)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code)
}

// agent24RefusedRowCredential returns the row's credential_id and suppressed count.
func agent24RefusedRowCredential(t *testing.T, entry *types.AuditLog) (string, int64) {
	t.Helper()
	var details agent24AuditDetails
	require.NoError(t, json.Unmarshal(entry.Details, &details))
	require.NotNil(t, details.CredentialID)
	return *details.CredentialID, details.Suppressed
}

// F3 on #119: a revoked or expired tenant key's rows go through that key's own bucket, and the next written
// one carries how many were dropped. Another key has its own bucket; a signed_token's rows are not limited.
func TestAgent24KnowledgeAuditRateLimitsRefusedKeyRows(t *testing.T) {
	stub := &agent24AuditStub{}
	refused, clock := newAgent24TestRefusedLimiter(time.Hour, 2, 8)
	r := agent24RefusedKeyRouter(stub, refused)

	for range 5 {
		agent24RefusedKeyRequest(t, r, "key:1")
	}
	require.Len(t, stub.entries, 2, "key:1's bucket holds two rows")
	for range 3 {
		agent24RefusedKeyRequest(t, r, "key:2")
	}
	require.Len(t, stub.entries, 4, "key:2 has its own bucket of two")
	for range 5 {
		agent24RefusedKeyRequest(t, r, "jti:tok-1")
	}
	require.Len(t, stub.entries, 9, "signed_token rows are not limited")

	clock.t = clock.t.Add(time.Hour)
	agent24RefusedKeyRequest(t, r, "key:1")
	require.Len(t, stub.entries, 10)
	last := stub.entries[9]
	require.Equal(t, uint64(7), last.TenantID, "a limited row keeps its tenant")
	credential, suppressed := agent24RefusedRowCredential(t, last)
	require.Equal(t, "key:1", credential)
	require.Equal(t, int64(3), suppressed)
	for i, e := range stub.entries[:9] {
		_, n := agent24RefusedRowCredential(t, e)
		require.Zero(t, n, "row %d", i)
	}
}

// A refused key's row whose write fails gives its count back to that key's bucket.
func TestAgent24KnowledgeAuditRefusedKeyFailedWriteKeepsCount(t *testing.T) {
	stub := &agent24FlakyAudit{}
	refused, clock := newAgent24TestRefusedLimiter(time.Hour, 1, 8)
	r := agent24RefusedKeyRouter(stub, refused)
	for range 4 {
		agent24RefusedKeyRequest(t, r, "key:1")
	}
	require.Len(t, stub.entries, 1)

	clock.t = clock.t.Add(time.Hour)
	stub.fail = true
	agent24RefusedKeyRequest(t, r, "key:1") // admitted with suppressed 3, write fails
	agent24RefusedKeyRequest(t, r, "key:2")
	clock.t = clock.t.Add(time.Hour)
	agent24RefusedKeyRequest(t, r, "key:1")
	require.Len(t, stub.entries, 3)
	_, n := agent24RefusedRowCredential(t, stub.entries[1])
	require.Zero(t, n, "key:2 does not take key:1's count")
	credential, n := agent24RefusedRowCredential(t, stub.entries[2])
	require.Equal(t, "key:1", credential)
	require.Equal(t, int64(3), n)
}

// A full table removes only a bucket that has refilled and owes no count; until one can go, a new key's rows
// are written without a limit.
func TestAgent24KnowledgeAuditRefusedKeyTableFull(t *testing.T) {
	stub := &agent24AuditStub{}
	refused, clock := newAgent24TestRefusedLimiter(time.Hour, 1, 1)
	r := agent24RefusedKeyRouter(stub, refused)

	agent24RefusedKeyRequest(t, r, "key:1")
	for range 3 {
		agent24RefusedKeyRequest(t, r, "key:2")
	}
	require.Len(t, stub.entries, 4, "key:1's bucket is not full, so key:2 gets none and is not limited")
	agent24RefusedKeyRequest(t, r, "key:1")
	require.Len(t, stub.entries, 4, "key:1 is dropped and owes 1")

	clock.t = clock.t.Add(time.Hour)
	agent24RefusedKeyRequest(t, r, "key:2")
	require.Len(t, stub.entries, 5, "key:1 has refilled but owes a count, so it stays")
	require.Contains(t, refused.buckets, "key:1")

	agent24RefusedKeyRequest(t, r, "key:1")
	require.Len(t, stub.entries, 6)
	_, n := agent24RefusedRowCredential(t, stub.entries[5])
	require.Equal(t, int64(1), n)

	clock.t = clock.t.Add(time.Hour)
	agent24RefusedKeyRequest(t, r, "key:2")
	agent24RefusedKeyRequest(t, r, "key:2")
	require.Len(t, stub.entries, 7, "key:1 was idle and gave way; key:2 is now limited")
	require.Len(t, refused.buckets, 1)
	require.Contains(t, refused.buckets, "key:2")
}

// A bucket whose reported count is still being written is not removed for a new key, even once refilled, so a
// failed write still finds it and the table stays at its size (Codex on F3: a stalled flush outlasting a refill).
func TestAgent24RefusedKeyAuditLimiterKeepsPendingBucket(t *testing.T) {
	refused, clock := newAgent24TestRefusedLimiter(time.Hour, 1, 1)
	ok, _ := refused.admit("key:1")
	require.True(t, ok)
	ok, _ = refused.admit("key:1")
	require.False(t, ok)
	clock.t = clock.t.Add(time.Hour)
	ok, n := refused.admit("key:1") // carries 1; its write is still running
	require.True(t, ok)
	require.Equal(t, int64(1), n)

	clock.t = clock.t.Add(time.Hour) // key:1 has refilled and owes no suppressed count
	ok, n = refused.admit("key:2")
	require.True(t, ok, "no bucket could go, so key:2 is written without a limit")
	require.Zero(t, n)
	require.Len(t, refused.buckets, 1)
	require.Contains(t, refused.buckets, "key:1")

	refused.settle("key:1", 1, false)
	require.Len(t, refused.buckets, 1)
	ok, n = refused.admit("key:1")
	require.True(t, ok)
	require.Equal(t, int64(1), n, "the failed row's count went back to key:1")
	refused.settle("key:1", n, true)

	clock.t = clock.t.Add(time.Hour)
	ok, _ = refused.admit("key:2")
	require.True(t, ok)
	require.Contains(t, refused.buckets, "key:2", "once settled, key:1 gave way")
	require.NotContains(t, refused.buckets, "key:1")
}

// settle adds a failed row's count to the rows dropped while it was being written.
func TestAgent24RefusedKeyAuditLimiterSettleAdds(t *testing.T) {
	refused, clock := newAgent24TestRefusedLimiter(time.Hour, 1, 8)
	ok, _ := refused.admit("key:1")
	require.True(t, ok)
	ok, _ = refused.admit("key:1")
	require.False(t, ok)
	clock.t = clock.t.Add(time.Hour)
	ok, n := refused.admit("key:1") // its write will fail
	require.True(t, ok)
	require.Equal(t, int64(1), n)
	ok, _ = refused.admit("key:1") // dropped while that row is written
	require.False(t, ok)
	refused.settle("key:1", n, false)
	clock.t = clock.t.Add(time.Hour)
	ok, n = refused.admit("key:1")
	require.True(t, ok)
	require.Equal(t, int64(2), n)
}

// agent24PanicAudit panics on the next Log when boom is set, otherwise records the row.
type agent24PanicAudit struct {
	agent24AuditStub
	boom bool
}

func (s *agent24PanicAudit) Log(ctx context.Context, entry *types.AuditLog) error {
	if s.boom {
		s.boom = false
		panic("store exploded")
	}
	return s.agent24AuditStub.Log(ctx, entry)
}

// A row whose Log panics returns its count, as a failed write does.
func TestAgent24KnowledgeAuditRefusedKeyPanickedWriteKeepsCount(t *testing.T) {
	stub := &agent24PanicAudit{}
	refused, clock := newAgent24TestRefusedLimiter(time.Hour, 1, 8)
	r := agent24RefusedKeyRouter(stub, refused)
	for range 3 {
		agent24RefusedKeyRequest(t, r, "key:1")
	}
	require.Len(t, stub.entries, 1)
	clock.t = clock.t.Add(time.Hour)
	stub.boom = true
	agent24RefusedKeyRequest(t, r, "key:1") // admitted with 2, Log panics
	clock.t = clock.t.Add(time.Hour)
	agent24RefusedKeyRequest(t, r, "key:1")
	require.Len(t, stub.entries, 2)
	_, n := agent24RefusedRowCredential(t, stub.entries[1])
	require.Equal(t, int64(2), n)
}

// The exported middleware carries the refused-key buckets: a burst of one revoked key's requests writes
// agent24AuditRefusedKeyBurst rows.
func TestAgent24KnowledgeAuditDefaultRefusedKeyBurst(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := &agent24AuditStub{}
	r := gin.New()
	r.Use(Agent24KnowledgeAudit(stub))
	r.POST("/api/v1/agent24/knowledge/search", func(c *gin.Context) {
		c.Set(types.Agent24CredentialRejectionKey, agent24TestRejection("key:1"))
		c.AbortWithStatus(http.StatusUnauthorized)
	})
	started := time.Now()
	for range agent24AuditRefusedKeyBurst + 5 {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/agent24/knowledge/search", nil))
		require.Equal(t, http.StatusUnauthorized, w.Code)
	}
	// The decided burst is 10 and the refill one row per 10 s; a slow run may earn a row per 10 s it took.
	refilled := int(time.Since(started) / (10 * time.Second))
	require.GreaterOrEqual(t, len(stub.entries), 10)
	require.LessOrEqual(t, len(stub.entries), 10+refilled)
}

// An anonymous row goes to the anonymous bucket only, even when a refused key's rejection is on the context.
func TestAgent24KnowledgeAuditAnonymousRowSkipsRefusedKeyBucket(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := &agent24AuditStub{}
	refused, _ := newAgent24TestRefusedLimiter(time.Hour, 1, 8)
	r := gin.New()
	r.Use(agent24KnowledgeAudit(stub, nil, refused))
	r.POST("/api/v1/agent24/knowledge/search", func(c *gin.Context) {
		c.Set(types.Agent24CredentialRejectionKey, agent24TestRejection("key:1"))
		writeAgent24KnowledgeError(c, agent24Unauthorized("credential_invalid"), agent24KnowledgeEcho{})
	})
	for range 3 {
		agent24AuditAnonymousRequest(t, r)
	}
	require.Len(t, stub.entries, 3)
	require.Empty(t, refused.buckets)
}

// Concurrent admits of one key share its bucket: exactly the burst is admitted, every other call is counted,
// and the next admitted row after a refill reports them all. Other keys admitted at the same time each get a
// bucket of their own.
func TestAgent24RefusedKeyAuditLimiterConcurrent(t *testing.T) {
	refused, clock := newAgent24TestRefusedLimiter(time.Hour, 5, 64)
	const calls = 50
	var admitted, others atomic.Int64
	var wg sync.WaitGroup
	for i := range calls {
		wg.Add(2)
		go func() {
			defer wg.Done()
			if ok, _ := refused.admit("key:1"); ok {
				admitted.Add(1)
			}
		}()
		go func() {
			defer wg.Done()
			if ok, _ := refused.admit(fmt.Sprintf("key:%d", 100+i)); ok {
				others.Add(1)
			}
		}()
	}
	wg.Wait()
	require.Equal(t, int64(5), admitted.Load())
	require.Equal(t, int64(calls), others.Load())
	clock.t = clock.t.Add(time.Hour)
	ok, n := refused.admit("key:1")
	require.True(t, ok)
	require.Equal(t, int64(calls-5), n)
}

// Authenticated rows are not limited by the refused-key buckets.
func TestAgent24KnowledgeAuditAuthenticatedRowSkipsRefusedKeyBucket(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := &agent24AuditStub{}
	refused, _ := newAgent24TestRefusedLimiter(time.Hour, 1, 8)
	r := gin.New()
	r.Use(agent24KnowledgeAudit(stub, nil, refused))
	authed := r.Group("/api/v1/agent24/knowledge", agent24AuditIdentity)
	authed.POST("/ready", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	for range 3 {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/agent24/knowledge/ready", nil))
		require.Equal(t, http.StatusOK, w.Code)
	}
	require.Len(t, stub.entries, 3)
	require.Empty(t, refused.buckets)
}

// Without refused-key buckets, a refused key's rows are written without a limit.
func TestAgent24KnowledgeAuditNoRefusedKeyLimiter(t *testing.T) {
	stub := &agent24AuditStub{}
	r := agent24RefusedKeyRouter(stub, nil)
	for range 3 {
		agent24RefusedKeyRequest(t, r, "key:1")
	}
	require.Len(t, stub.entries, 3)
}

func TestAgent24AuditRefusedKeyOf(t *testing.T) {
	cases := []struct {
		name      string
		rejection *types.Agent24CredentialRejection
		want      string
		ok        bool
	}{
		{name: "no rejection"},
		{name: "revoked key", rejection: &types.Agent24CredentialRejection{
			Reason: "credential_revoked", CredentialID: "key:1", TenantID: 7,
		}, want: "key:1", ok: true},
		{name: "expired key", rejection: &types.Agent24CredentialRejection{
			Reason: "credential_expired", CredentialID: "key:2", TenantID: 7,
		}, want: "key:2", ok: true},
		{name: "signed_token with jti", rejection: func() *types.Agent24CredentialRejection {
			r := agent24TestRejection("jti:tok-1")
			return &r
		}()},
		{name: "signed_token without jti", rejection: &types.Agent24CredentialRejection{
			Reason:    "credential_expired",
			Principal: types.Principal{Type: types.PrincipalAPIExternalUser, ID: "7:user-1"}, CredentialID: "key:3",
			TenantID: 7,
		}},
		{name: "other reason", rejection: &types.Agent24CredentialRejection{
			Reason: "credential_invalid", CredentialID: "key:4", TenantID: 7,
		}},
	}
	gin.SetMode(gin.TestMode)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			if tc.rejection != nil {
				c.Set(types.Agent24CredentialRejectionKey, *tc.rejection)
			}
			got, ok := agent24AuditRefusedKeyOf(c)
			require.Equal(t, tc.ok, ok)
			require.Equal(t, tc.want, got)
		})
	}
}

// An anonymous row whose Log panics returns its count too (the settle shared with refused keys).
func TestAgent24KnowledgeAuditAnonymousPanickedWriteKeepsCount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := &agent24PanicAudit{}
	limiter := newAgent24AnonymousAuditLimiter(time.Hour, 1)
	r := gin.New()
	r.Use(agent24KnowledgeAudit(stub, limiter, nil))
	r.POST("/api/v1/agent24/knowledge/search", func(c *gin.Context) {
		c.AbortWithStatus(http.StatusUnauthorized)
	})
	for range 3 {
		agent24AuditAnonymousRequest(t, r)
	}
	require.Len(t, stub.entries, 1)
	limiter.bucket.SetLimit(rate.Inf)
	stub.boom = true
	agent24AuditAnonymousRequest(t, r) // admitted with 2, Log panics
	agent24AuditAnonymousRequest(t, r)
	require.Len(t, stub.entries, 2)
	require.Contains(t, string(stub.entries[1].Details), `"suppressed":2`)
}

// agent24PanicJSON panics when encoded, standing in for a details value that fails after admission.
type agent24PanicJSON struct{}

func (agent24PanicJSON) MarshalJSON() ([]byte, error) { panic("cannot encode") }

// A panic while the admitted row's details are built returns its count and leaves nothing pending (PR-Daemon
// L1 on #124): the key's next row reports the count, and the bucket can give way again.
func TestAgent24KnowledgeAuditRefusedKeyPanicBeforeWriteKeepsCount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := &agent24AuditStub{}
	refused, clock := newAgent24TestRefusedLimiter(time.Hour, 1, 1)
	r := gin.New()
	r.Use(agent24KnowledgeAudit(stub, nil, refused))
	r.POST("/api/v1/agent24/knowledge/search", func(c *gin.Context) {
		if c.GetHeader("X-Test-Panic") != "" {
			agent24NoteAudit(c, func(f *agent24AuditFacts) { f.Revision = agent24PanicJSON{} })
		}
		c.Set(types.Agent24CredentialRejectionKey, agent24TestRejection(c.GetHeader("X-Test-Credential")))
		c.AbortWithStatus(http.StatusUnauthorized)
	})
	agent24RefusedKeyRequest(t, r, "key:1")
	agent24RefusedKeyRequest(t, r, "key:1") // dropped, owes 1
	clock.t = clock.t.Add(time.Hour)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/agent24/knowledge/search", nil)
	req.Header.Set("X-Test-Credential", "key:1")
	req.Header.Set("X-Test-Panic", "1")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req) // admitted with 1, the details panic
	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Len(t, stub.entries, 1)

	clock.t = clock.t.Add(time.Hour)
	agent24RefusedKeyRequest(t, r, "key:1")
	require.Len(t, stub.entries, 2)
	_, n := agent24RefusedRowCredential(t, stub.entries[1])
	require.Equal(t, int64(1), n)
	require.Zero(t, refused.buckets["key:1"].pending)
}

// Two rows of one key in flight at once, each with its own count, add up in pending: the bucket stays until
// both settle, and the failed one's count is what the next row reports (PR-Daemon L2 on #124).
func TestAgent24RefusedKeyAuditLimiterTwoPendingRows(t *testing.T) {
	refused, clock := newAgent24TestRefusedLimiter(time.Hour, 1, 1)
	drop := func(times int) {
		for range times {
			ok, _ := refused.admit("key:1")
			require.False(t, ok)
		}
	}
	ok, _ := refused.admit("key:1")
	require.True(t, ok)
	drop(1)
	clock.t = clock.t.Add(time.Hour)
	ok, a := refused.admit("key:1") // row A carries 1
	require.True(t, ok)
	require.Equal(t, int64(1), a)
	drop(2)
	clock.t = clock.t.Add(time.Hour)
	ok, b := refused.admit("key:1") // row B carries 2 while A is still writing
	require.True(t, ok)
	require.Equal(t, int64(2), b)

	refused.settle("key:1", a, true)
	clock.t = clock.t.Add(time.Hour)
	ok, n := refused.admit("key:2")
	require.True(t, ok)
	require.Zero(t, n)
	require.Contains(t, refused.buckets, "key:1", "B is still writing its count")

	refused.settle("key:1", b, false)
	require.Zero(t, refused.buckets["key:1"].pending)
	ok, n = refused.admit("key:1")
	require.True(t, ok)
	require.Equal(t, int64(2), n)
}

// A dropped row ends quietly: nothing to settle, no panic for the write path to log.
func TestAgent24KnowledgeAuditDroppedRowLogsNothing(t *testing.T) {
	stub := &agent24AuditStub{}
	refused, _ := newAgent24TestRefusedLimiter(time.Hour, 1, 8)
	r := agent24RefusedKeyRouter(stub, refused)
	agent24RefusedKeyRequest(t, r, "key:1")
	buf := agent24CaptureCredentialLogs(t)
	agent24RefusedKeyRequest(t, r, "key:1")
	require.Len(t, stub.entries, 1)
	require.NotContains(t, buf.String(), "panic while writing the row")
}
