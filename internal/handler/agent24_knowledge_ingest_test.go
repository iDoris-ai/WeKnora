// Agent24 fork product patch A24-KB-54 (KB-C4-04 routes, Loop 7 first round F3/F4).

package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/types"
)

const agent24IngestKey = "idem-ingest-submit-key-01"

type agent24IngestEnv struct {
	db   *gorm.DB
	h    *Agent24KnowledgeIngestHandler
	fake *agent24IngestFake
}

func newAgent24IngestEnv(t *testing.T) *agent24IngestEnv {
	t.Helper()
	t.Setenv(Agent24KnowledgePolicyEnv, "")
	t.Setenv(Agent24KnowledgePolicyFileEnv, "")
	ResetAgent24ProcessingPolicyForTest()
	require.Equal(t, agent24DefaultProcessingVersion, Agent24ProcessingPolicyVersion())
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	require.NoError(t, db.Migrator().CreateTable(&types.Knowledge{}, &types.KnowledgeProcessingSpan{}))
	require.NoError(t, repository.EnsureAgent24IngestJobTable(db))
	require.NoError(t, repository.EnsureAgent24CandidateTables(db))
	fake := &agent24IngestFake{savedAt: time.Date(2026, 11, 24, 1, 2, 3, 0, time.UTC)}
	clock := time.Date(2026, 11, 24, 3, 0, 0, 0, time.UTC)
	e := &agent24IngestEnv{db: db, fake: fake}
	activations, err := service.LoadAgent24MockActivations(agent24IngestMockFile(t, agent24IngestMockConfig))
	require.NoError(t, err)
	require.NotNil(t, activations)
	e.h = &Agent24KnowledgeIngestHandler{
		activations: activations, ingest: fake, jobs: repository.NewAgent24IngestJobStore(db),
		candidates: repository.NewAgent24CandidateStore(db), db: db,
		key: bytes.Repeat([]byte{9}, 32), now: func() time.Time { return clock },
		visible: func(_ context.Context, tenant uint64, kbID string) (bool, error) {
			return tenant == 1 && kbID == "kbA", nil
		},
	}
	return e
}

func (e *agent24IngestEnv) rows(t *testing.T, model any) int64 {
	t.Helper()
	var n int64
	require.NoError(t, e.db.Model(model).Count(&n).Error)
	return n
}

func (e *agent24IngestEnv) requireNoWrite(t *testing.T) {
	t.Helper()
	prep, stage, dispatch, abandon := e.fake.counts()
	require.Zero(t, prep)
	require.Zero(t, stage)
	require.Zero(t, dispatch)
	require.Zero(t, abandon)
	require.Zero(t, e.rows(t, &types.Agent24WriteIdempotency{}))
	require.Zero(t, e.rows(t, &types.Agent24IngestJob{}))
}

func agent24IngestAs(caps []string, kbs ...string) agent24CandidateAs {
	if len(kbs) == 0 {
		kbs = []string{"kbA"}
	}
	return agent24CandidateAs{principal: agent24Alice, tenant: 1, scope: types.TenantAPIKeyScope{
		KeyID: 42, KnowledgeBaseIDs: kbs, Capabilities: caps,
	}}
}

func agent24IngestBody(t *testing.T, content []byte, mut func(map[string]any)) string {
	t.Helper()
	if content == nil {
		content = []byte("# hello\n")
	}
	sum := sha256.Sum256(content)
	m := map[string]any{
		"request_id": "req-1", "idempotency_key": agent24IngestKey, "knowledge_base_id": "kbA",
		"file_name": "note.md", "content_base64": base64.StdEncoding.EncodeToString(content),
		"content_sha256": hex.EncodeToString(sum[:]), "expected_policy_version": Agent24ProcessingPolicyVersion(),
	}
	if mut != nil {
		mut(m)
	}
	raw, err := json.Marshal(m)
	require.NoError(t, err)
	return string(raw)
}

func agent24IngestJobBody(id string) string {
	raw, _ := json.Marshal(map[string]any{"request_id": "req-1", "job_id": id})
	return string(raw)
}

func (e *agent24IngestEnv) call(
	t *testing.T, serve func(*gin.Context), as agent24CandidateAs, body string,
) agent24CandidateReply {
	t.Helper()
	return agent24CandidateCall(t, serve, as, body)
}

type agent24IngestFake struct {
	mu                                     sync.Mutex
	prepares, stages, dispatches, abandons int
	prepErr, stageErr, dispatchErr         error
	lastWho                                service.Agent24IngestPrincipal
	lastReq                                service.Agent24IngestRequest
	revision                               string
	savedAt                                time.Time
	persist                                bool
	onDispatch                             func()
}

func (f *agent24IngestFake) counts() (prep, stage, dispatch, abandon int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.prepares, f.stages, f.dispatches, f.abandons
}

func (f *agent24IngestFake) Prepare(
	_ context.Context, req service.Agent24IngestRequest,
) (*service.Agent24IngestPrepared, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prepares++
	f.lastReq = req
	if f.prepErr != nil {
		return nil, f.prepErr
	}
	return &service.Agent24IngestPrepared{}, nil
}

func (f *agent24IngestFake) Stage(
	ctx context.Context, tx *gorm.DB, _ *service.Agent24IngestPrepared, who service.Agent24IngestPrincipal,
	sourceRevision func(*types.Knowledge) string,
) (*types.Agent24IngestJob, *types.Knowledge, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stages++
	f.lastWho = who
	if f.stageErr != nil {
		return nil, nil, f.stageErr
	}
	k := &types.Knowledge{ID: "doc-1", Type: "file"}
	if sourceRevision != nil {
		f.revision = sourceRevision(k)
	}
	units := int64(4)
	job := &types.Agent24IngestJob{
		ID: "job-1", TenantID: 1, KnowledgeBaseID: f.lastReq.KnowledgeBaseID, KnowledgeID: k.ID,
		OpClass: who.OpClass, PrincipalUserID: who.UserID, CredentialID: who.CredentialID,
		PolicyVersion: who.PolicyVersion, SourceRevision: f.revision, Status: types.Agent24JobSaved,
		Enrichment: types.Agent24EnrichmentNone, SavedAt: f.savedAt, UpdatedAt: f.savedAt,
		Cost: types.Agent24JobCost{Items: []types.Agent24JobCostItem{
			{Kind: "storage", Units: &units, UnitType: "bytes", Measured: "exact"},
			{Kind: "embedding", Destination: "emb", UnitType: "tokens", Measured: "unknown"},
		}},
	}
	if f.persist {
		if err := tx.WithContext(ctx).Create(job).Error; err != nil {
			return nil, nil, err
		}
	}
	return job, k, nil
}

func (f *agent24IngestFake) Dispatch(
	_ context.Context, _ *types.Agent24IngestJob, _ *types.Knowledge, _ *service.Agent24IngestPrepared,
) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dispatches++
	if f.onDispatch != nil {
		f.onDispatch()
	}
	return f.dispatchErr
}

func (f *agent24IngestFake) Abandon(context.Context, *service.Agent24IngestPrepared) {
	f.mu.Lock()
	f.abandons++
	f.mu.Unlock()
}

func TestAgent24KnowledgeIngestScope(t *testing.T) {
	e := newAgent24IngestEnv(t)
	body := agent24IngestBody(t, nil, nil)
	t.Run("no ingest", func(t *testing.T) {
		r := e.call(t, e.h.Submit, agent24IngestAs([]string{"retrieve"}), body)
		require.Equal(t, http.StatusForbidden, r.code, r.raw)
		require.Equal(t, "forbidden/operation_not_allowed", r.outcome())
		e.requireNoWrite(t)
	})
	t.Run("outside the key list", func(t *testing.T) {
		r := e.call(t, e.h.Submit, agent24IngestAs([]string{"ingest"}, "kbB"), body)
		require.Equal(t, http.StatusForbidden, r.code, r.raw)
		require.Equal(t, "forbidden/empty_scope", r.outcome())
		e.requireNoWrite(t)
	})
	t.Run("full access", func(t *testing.T) {
		as := agent24IngestAs([]string{"ingest"})
		as.scope.FullAccess = true
		r := e.call(t, e.h.Submit, as, body)
		require.Equal(t, "forbidden/operation_not_allowed", r.outcome())
		e.requireNoWrite(t)
	})
	t.Run("platform", func(t *testing.T) {
		as := agent24IngestAs([]string{"ingest"})
		as.scope.ScopeType = types.APIKeyScopePlatform
		r := e.call(t, e.h.Submit, as, body)
		require.Equal(t, "forbidden/operation_not_allowed", r.outcome())
		e.requireNoWrite(t)
	})
	t.Run("not visible", func(t *testing.T) {
		as := agent24IngestAs([]string{"ingest"}, "kbA", "kbZ")
		r := e.call(t, e.h.Submit, as, agent24IngestBody(t, nil, func(m map[string]any) {
			m["knowledge_base_id"] = "kbZ"
		}))
		require.Equal(t, http.StatusNotFound, r.code, r.raw)
		require.Equal(t, "not_found", r.outcome())
		e.requireNoWrite(t)
	})
}

func TestAgent24KnowledgeIngestAdmission(t *testing.T) {
	e := newAgent24IngestEnv(t)
	as := agent24IngestAs([]string{"ingest"})
	t.Run("policy", func(t *testing.T) {
		r := e.call(t, e.h.Submit, as, agent24IngestBody(t, nil, func(m map[string]any) {
			m["expected_policy_version"] = "not-the-policy"
		}))
		require.Equal(t, http.StatusConflict, r.code, r.raw)
		require.Equal(t, "stale/policy_changed", r.outcome())
		require.Equal(t, Agent24ProcessingPolicyVersion(), r.details()["current_policy_version"])
		e.requireNoWrite(t)
	})
	t.Run("format", func(t *testing.T) {
		r := e.call(t, e.h.Submit, as, agent24IngestBody(t, nil, func(m map[string]any) {
			m["file_name"] = "notes.pdf"
		}))
		require.Equal(t, http.StatusBadRequest, r.code, r.raw)
		require.Equal(t, "invalid_request/unsupported_format", r.outcome())
		e.requireNoWrite(t)
	})
	t.Run("size", func(t *testing.T) {
		big := bytes.Repeat([]byte("a"), service.Agent24IngestMaxBytes+1)
		r := e.call(t, e.h.Submit, as, agent24IngestBody(t, big, func(m map[string]any) {
			m["file_name"] = "big.md"
		}))
		require.Equal(t, http.StatusBadRequest, r.code, r.raw)
		require.Equal(t, "invalid_request/too_large", r.outcome())
		e.requireNoWrite(t)
	})
}

func TestAgent24KnowledgeIngestSubmitReplay(t *testing.T) {
	e := newAgent24IngestEnv(t)
	as := agent24IngestAs([]string{"ingest"})
	body := agent24IngestBody(t, []byte("# hello\n"), nil)
	first := e.call(t, e.h.Submit, as, body)
	require.Equal(t, http.StatusOK, first.code, first.raw)
	require.Equal(t, "req-1", first.body["request_id"])
	_, hasGen := first.body["generation"]
	require.False(t, hasGen)
	_, hasDetails := first.body["details"]
	require.False(t, hasDetails)
	job, _ := first.body["job"].(map[string]any)
	require.Equal(t, "saved", job["status"])
	require.Equal(t, "ingest.submit", job["op_class"])
	require.Equal(t, "kbA", job["knowledge_base_id"])
	require.Equal(t, "doc-1", job["knowledge_id"])
	require.Equal(t, "job-1", job["job_id"])
	require.Equal(t, true, job["original_retained"])
	require.Equal(t, "retained_invisible", job["partial_artifacts"])
	require.Equal(t, "2026-11-24T01:02:03Z", job["saved_at"])
	require.Nil(t, job["processing_at"])
	require.Nil(t, job["indexed_at"])
	require.Nil(t, job["terminal_at"])
	_, hasStop := job["stop_reason"]
	require.False(t, hasStop)
	require.Equal(t, "", job["failure_reason"])
	require.Equal(t, "", job["cancel_reason"])
	cost, _ := job["cost"].(map[string]any)
	require.Equal(t, false, cost["final"])
	items, _ := cost["items"].([]any)
	require.Len(t, items, 2)
	exact, _ := items[0].(map[string]any)
	unknown, _ := items[1].(map[string]any)
	require.Equal(t, float64(4), exact["units"])
	require.Nil(t, unknown["units"])
	require.Equal(t, "unknown", unknown["measured"])
	require.Len(t, job["source_revision"], 64)

	prep, stage, dispatch, abandon := e.fake.counts()
	require.Equal(t, 1, prep)
	require.Equal(t, 1, stage)
	require.Equal(t, 1, dispatch)
	require.Zero(t, abandon)
	require.Equal(t, int64(1), e.rows(t, &types.Agent24WriteIdempotency{}))
	e.fake.mu.Lock()
	who := e.fake.lastWho
	req := e.fake.lastReq
	e.fake.mu.Unlock()
	require.Equal(t, types.Agent24JobOpIngestSubmit, who.OpClass)
	require.Equal(t, agent24Alice.StorageID(), who.UserID)
	require.Equal(t, uint64(42), who.CredentialID)
	require.Equal(t, Agent24ProcessingPolicyVersion(), who.PolicyVersion)
	require.Equal(t, service.Agent24OriginUpload, req.Origin)
	require.Equal(t, "note.md", req.FileName)
	require.Equal(t, []byte("# hello\n"), req.Content)

	again := e.call(t, e.h.Submit, as, body)
	require.Equal(t, http.StatusOK, again.code, again.raw)
	require.Equal(t, true, again.details()["replayed"])
	require.Equal(t, first.body["job"], again.body["job"])
	require.Equal(t, first.body["request_id"], again.body["request_id"])
	prep, _, _, _ = e.fake.counts()
	require.Equal(t, 1, prep)

	reused := e.call(t, e.h.Submit, as, agent24IngestBody(t, []byte("# hello\n"), func(m map[string]any) {
		m["file_name"] = "other.md"
	}))
	require.Equal(t, http.StatusBadRequest, reused.code, reused.raw)
	require.Equal(t, "invalid_request/idempotency_key_reused", reused.outcome())
	prep, _, _, _ = e.fake.counts()
	require.Equal(t, 1, prep)
	require.Equal(t, int64(1), e.rows(t, &types.Agent24WriteIdempotency{}))
}

func TestAgent24KnowledgeIngestIdempotencyMissing(t *testing.T) {
	e := newAgent24IngestEnv(t)
	r := e.call(t, e.h.Submit, agent24IngestAs([]string{"ingest"}), agent24IngestBody(t, nil, func(m map[string]any) {
		delete(m, "idempotency_key")
	}))
	require.Equal(t, http.StatusBadRequest, r.code, r.raw)
	require.Equal(t, "invalid_request/idempotency_key_missing", r.outcome())
	require.Equal(t, "req-1", r.details()["request_id"])
	e.requireNoWrite(t)

	shortBody := agent24IngestBody(t, nil, func(m map[string]any) { m["idempotency_key"] = "short-key" })
	shortKey := e.call(t, e.h.Submit, agent24IngestAs([]string{"ingest"}), shortBody)
	require.Equal(t, "invalid_request", shortKey.outcome())
	e.requireNoWrite(t)
}

func TestAgent24KnowledgeIngestPrepareErrors(t *testing.T) {
	e := newAgent24IngestEnv(t)
	as := agent24IngestAs([]string{"ingest"})
	cases := []struct {
		name string
		err  error
		want string
		code int
	}{
		{"hash", service.ErrAgent24ContentHashMismatch, "invalid_request/content_hash_mismatch", http.StatusBadRequest},
		{"down", service.ErrAgent24IngestUnavailable, "unavailable", http.StatusServiceUnavailable},
		{"missing", service.ErrAgent24KnowledgeNotFound, "not_found", http.StatusNotFound},
		{"name", service.ErrAgent24InvalidFileName, "invalid_request", http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e.fake.mu.Lock()
			e.fake.prepErr = tc.err
			e.fake.mu.Unlock()
			r := e.call(t, e.h.Submit, as, agent24IngestBody(t, nil, func(m map[string]any) {
				m["idempotency_key"] = agent24IngestKey + tc.name
				m["file_name"] = tc.name + ".md"
			}))
			require.Equal(t, tc.code, r.code, r.raw)
			require.Equal(t, tc.want, r.outcome())
			if tc.name == "down" {
				_, hasReason := r.details()["reason"]
				require.False(t, hasReason)
				require.Equal(t, true, r.details()["retryable"])
			}
			_, _, _, abandon := e.fake.counts()
			require.Zero(t, abandon)
		})
	}
	e.fake.mu.Lock()
	e.fake.prepErr = nil
	e.fake.stageErr = service.ErrAgent24UnsupportedKnowledge
	e.fake.mu.Unlock()
	r := e.call(t, e.h.Submit, as, agent24IngestBody(t, nil, func(m map[string]any) {
		m["idempotency_key"] = agent24IngestKey + "stage"
		m["file_name"] = "stage.md"
	}))
	require.Equal(t, "invalid_request/unsupported_knowledge", r.outcome(), r.raw)
	_, _, _, abandon := e.fake.counts()
	require.Equal(t, 1, abandon)
	require.Zero(t, e.rows(t, &types.Agent24WriteIdempotency{}))
}

func TestAgent24KnowledgeIngestDispatchReread(t *testing.T) {
	e := newAgent24IngestEnv(t)
	e.fake.persist = true
	e.fake.dispatchErr = errors.New("enqueue")
	e.fake.onDispatch = func() {
		require.NoError(t, e.db.Model(&types.Agent24IngestJob{}).Where("id = ?", "job-1").Updates(map[string]any{
			"status": types.Agent24JobFailed, "failure_reason": types.Agent24JobFailEnqueue,
		}).Error)
	}
	as := agent24IngestAs([]string{"ingest"})
	body := agent24IngestBody(t, nil, nil)
	first := e.call(t, e.h.Submit, as, body)
	require.Equal(t, http.StatusOK, first.code, first.raw)
	job, _ := first.body["job"].(map[string]any)
	require.Equal(t, "failed", job["status"])
	require.Equal(t, "enqueue_failed", job["failure_reason"])
	again := e.call(t, e.h.Submit, as, body)
	require.Equal(t, http.StatusOK, again.code, again.raw)
	require.Equal(t, true, again.details()["replayed"])
	replayed, _ := again.body["job"].(map[string]any)
	require.Equal(t, "saved", replayed["status"])
	prep, _, dispatch, _ := e.fake.counts()
	require.Equal(t, 1, prep)
	require.Equal(t, 1, dispatch)
}

func (e *agent24IngestEnv) ensureDoc(t *testing.T, id, status string) {
	t.Helper()
	require.NoError(t, e.db.Create(&types.Knowledge{
		ID: id, TenantID: 1, KnowledgeBaseID: "kbA", Type: "file", ParseStatus: status,
	}).Error)
}

func (e *agent24IngestEnv) insertJob(t *testing.T, job types.Agent24IngestJob) {
	t.Helper()
	if job.TenantID == 0 {
		job.TenantID = 1
	}
	if job.KnowledgeBaseID == "" {
		job.KnowledgeBaseID = "kbA"
	}
	if job.OpClass == "" {
		job.OpClass = types.Agent24JobOpIngestSubmit
	}
	if job.PrincipalUserID == "" {
		job.PrincipalUserID = agent24Alice.StorageID()
	}
	if job.Enrichment == "" {
		job.Enrichment = types.Agent24EnrichmentNone
	}
	if job.SavedAt.IsZero() {
		job.SavedAt = time.Now().UTC()
	}
	if job.UpdatedAt.IsZero() {
		job.UpdatedAt = job.SavedAt
	}
	if job.Cost.Items == nil {
		n := int64(8)
		job.Cost = types.Agent24JobCost{Items: []types.Agent24JobCostItem{
			{Kind: "storage", Units: &n, UnitType: "bytes", Measured: "exact"},
		}}
	}
	require.NoError(t, e.db.Create(&job).Error)
}

func TestAgent24KnowledgeIngestGetJob(t *testing.T) {
	e := newAgent24IngestEnv(t)
	e.ensureDoc(t, "doc-own", types.ParseStatusPending)
	e.insertJob(t, types.Agent24IngestJob{
		ID: "job-own", KnowledgeID: "doc-own", Status: types.Agent24JobSaved,
	})
	own := e.call(t, e.h.GetJob, agent24IngestAs([]string{"ingest"}), agent24IngestJobBody("job-own"))
	require.Equal(t, http.StatusOK, own.code, own.raw)
	job, _ := own.body["job"].(map[string]any)
	require.Equal(t, "saved", job["status"])
	require.Equal(t, true, job["original_retained"])
	require.Equal(t, "retained_invisible", job["partial_artifacts"])
	require.Equal(t, "req-1", own.body["request_id"])

	other := agent24IngestAs([]string{"ingest"})
	other.principal = agent24Bob
	r := e.call(t, e.h.GetJob, other, agent24IngestJobBody("job-own"))
	require.Equal(t, http.StatusNotFound, r.code, r.raw)
	require.Equal(t, "not_found", r.outcome())
	missing := e.call(t, e.h.GetJob, agent24IngestAs([]string{"ingest"}), agent24IngestJobBody("job-missing"))
	require.Equal(t, "not_found", missing.outcome())
	require.Equal(t, r.body["error"], missing.body["error"])

	lost := e.call(t, e.h.GetJob, agent24IngestAs([]string{"retrieve"}), agent24IngestJobBody("job-own"))
	require.Equal(t, http.StatusForbidden, lost.code, lost.raw)
	require.Equal(t, "forbidden/operation_not_allowed", lost.outcome())
}

func TestAgent24KnowledgeIngestCancelJob(t *testing.T) {
	e := newAgent24IngestEnv(t)
	now := time.Now().UTC()
	e.ensureDoc(t, "doc-saved", types.ParseStatusPending)
	e.ensureDoc(t, "doc-run", types.ParseStatusPending)
	e.ensureDoc(t, "doc-done", types.ParseStatusPending)
	e.ensureDoc(t, "doc-proc", types.ParseStatusPending)
	e.insertJob(t, types.Agent24IngestJob{
		ID: "job-saved", KnowledgeID: "doc-saved", Status: types.Agent24JobSaved, SavedAt: now, UpdatedAt: now,
	})
	e.insertJob(t, types.Agent24IngestJob{
		ID: "job-proc", KnowledgeID: "doc-proc", Status: types.Agent24JobProcessing, SavedAt: now, UpdatedAt: now,
	})
	e.insertJob(t, types.Agent24IngestJob{
		ID: "job-run", KnowledgeID: "doc-run", Status: types.Agent24JobIndexed,
		Enrichment: types.Agent24EnrichmentRunning, IndexedAt: &now, SavedAt: now, UpdatedAt: now,
	})
	e.insertJob(t, types.Agent24IngestJob{
		ID: "job-done", KnowledgeID: "doc-done", Status: types.Agent24JobIndexed,
		Enrichment: types.Agent24EnrichmentNone, SavedAt: now, UpdatedAt: now,
	})
	as := agent24IngestAs([]string{"ingest"})

	saved := e.call(t, e.h.CancelJob, as, agent24IngestJobBody("job-saved"))
	require.Equal(t, http.StatusOK, saved.code, saved.raw)
	require.Equal(t, "cancelled", saved.body["cancel_effect"])
	savedJob, _ := saved.body["job"].(map[string]any)
	require.Equal(t, "cancelled", savedJob["status"])
	require.Equal(t, "user", savedJob["cancel_reason"])
	require.Equal(t, true, savedJob["original_retained"])
	require.Equal(t, "retained_invisible", savedJob["partial_artifacts"])
	savedCost, _ := savedJob["cost"].(map[string]any)
	require.Equal(t, true, savedCost["final"])
	row, err := e.h.jobs.Get(context.Background(), 1, "job-saved")
	require.NoError(t, err)
	require.Equal(t, types.Agent24JobCancelled, row.Status)
	require.Equal(t, types.Agent24JobCancelUser, row.CancelReason)
	require.True(t, row.Cost.Final)
	require.NotNil(t, row.TerminalAt)

	proc := e.call(t, e.h.CancelJob, as, agent24IngestJobBody("job-proc"))
	require.Equal(t, "cancelled", proc.body["cancel_effect"])
	procJob, _ := proc.body["job"].(map[string]any)
	require.Equal(t, "cancelled", procJob["status"])
	require.Equal(t, "user", procJob["cancel_reason"])

	run := e.call(t, e.h.CancelJob, as, agent24IngestJobBody("job-run"))
	require.Equal(t, http.StatusOK, run.code, run.raw)
	require.Equal(t, "enrichment_stopped", run.body["cancel_effect"])
	runJob, _ := run.body["job"].(map[string]any)
	require.Equal(t, "indexed", runJob["status"])
	require.Equal(t, "stopped", runJob["enrichment"])
	require.Equal(t, "user", runJob["stop_reason"])
	require.Equal(t, true, runJob["original_retained"])

	none := e.call(t, e.h.CancelJob, as, agent24IngestJobBody("job-done"))
	require.Equal(t, http.StatusOK, none.code, none.raw)
	require.Equal(t, "none", none.body["cancel_effect"])
	noneJob, _ := none.body["job"].(map[string]any)
	require.Equal(t, "indexed", noneJob["status"])
	require.Equal(t, "none", noneJob["enrichment"])
	noneCost, _ := noneJob["cost"].(map[string]any)
	require.Equal(t, false, noneCost["final"])

	other := agent24IngestAs([]string{"ingest"})
	other.principal = agent24Bob
	denied := e.call(t, e.h.CancelJob, other, agent24IngestJobBody("job-done"))
	require.Equal(t, http.StatusNotFound, denied.code, denied.raw)
	require.Equal(t, "not_found", denied.outcome())
	still, err := e.h.jobs.Get(context.Background(), 1, "job-done")
	require.NoError(t, err)
	require.Equal(t, types.Agent24JobIndexed, still.Status)
	require.Equal(t, types.Agent24EnrichmentNone, still.Enrichment)
}

const agent24IngestMockConfig = `{"activations":[{"code_sha256":` +
	`"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",` +
	`"tenant_id":1,"template_kb_id":"template","config_version":"fixture"}]}`

func agent24IngestMockFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mock-activations.json")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
	return path
}

func TestAgent24KnowledgeIngestConstructor(t *testing.T) {
	ingest := &service.Agent24Ingest{}
	jobs := &repository.Agent24IngestJobStore{}
	candidates := &repository.Agent24CandidateStore{}
	valid := agent24IngestMockFile(t, agent24IngestMockConfig)
	t.Setenv(Agent24KnowledgeTagKeyEnv, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))

	for _, tc := range []struct {
		name, mode, path  string
		wantOpen, wantErr bool
	}{
		{name: "default"},
		{name: "non-integration ignores configured file", path: valid},
		{name: "invalid mode", mode: "nope", path: valid, wantErr: true},
		{
			name: "missing file", mode: Agent24KnowledgeModeIntegration,
			path: filepath.Join(t.TempDir(), "missing"), wantErr: true,
		},
		{
			name: "empty allowlist", mode: Agent24KnowledgeModeIntegration,
			path: agent24IngestMockFile(t, `{"activations":[]}`), wantErr: true,
		},
		{
			name: "trailing invalid data", mode: Agent24KnowledgeModeIntegration,
			path: agent24IngestMockFile(t, agent24IngestMockConfig+` {`), wantErr: true,
		},
		{name: "valid mock", mode: Agent24KnowledgeModeIntegration, path: valid, wantOpen: true},
		{name: "unset after enabled", mode: Agent24KnowledgeModeIntegration},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(Agent24KnowledgeModeEnv, tc.mode)
			t.Setenv(Agent24MockActivationFileEnv, tc.path)
			h, err := NewAgent24KnowledgeIngestHandler(ingest, jobs, candidates, nil)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.wantOpen, h != nil)
		})
	}

	t.Setenv(Agent24KnowledgeModeEnv, Agent24KnowledgeModeIntegration)
	t.Setenv(Agent24MockActivationFileEnv, valid)
	h, err := NewAgent24KnowledgeIngestHandler(nil, jobs, candidates, nil)
	require.NoError(t, err)
	require.Nil(t, h)
	t.Setenv(Agent24KnowledgeTagKeyEnv, "")
	h, err = NewAgent24KnowledgeIngestHandler(ingest, jobs, candidates, nil)
	require.Error(t, err)
	require.Nil(t, h)
}

func TestAgent24KnowledgeIngestDeniesUnlistedTenantBeforeTargetAccess(t *testing.T) {
	e := newAgent24IngestEnv(t)
	as := agent24IngestAs([]string{"ingest"})
	as.tenant = 2
	as.principal.ID = "2:fixture"
	e.insertJob(t, types.Agent24IngestJob{
		ID: "job-unlisted", TenantID: as.tenant, KnowledgeID: "doc-unlisted",
		PrincipalUserID: as.principal.StorageID(), Status: types.Agent24JobSaved,
	})
	kbLookups, dbQueries := 0, 0
	e.h.visible = func(context.Context, uint64, string) (bool, error) {
		kbLookups++
		return true, nil
	}
	require.NoError(t, e.db.Callback().Query().Before("gorm:query").Register("count_target_queries", func(*gorm.DB) {
		dbQueries++
	}))

	r := e.call(t, e.h.Submit, as, agent24IngestBody(t, nil, nil))
	require.Equal(t, "forbidden/operation_not_allowed", r.outcome(), r.raw)
	for _, serve := range []func(*gin.Context){e.h.GetJob, e.h.CancelJob} {
		own := e.call(t, serve, as, agent24IngestJobBody("job-unlisted"))
		missing := e.call(t, serve, as, agent24IngestJobBody("job-missing"))
		require.Equal(t, "forbidden/operation_not_allowed", own.outcome(), own.raw)
		require.Equal(t, own.raw, missing.raw)
	}
	require.Zero(t, kbLookups)
	require.Zero(t, dbQueries)
	prep, stage, dispatch, abandon := e.fake.counts()
	require.Equal(t, [4]int{}, [4]int{prep, stage, dispatch, abandon})
	require.Zero(t, e.rows(t, &types.Agent24WriteIdempotency{}))
	require.EqualValues(t, 1, e.rows(t, &types.Agent24IngestJob{}))
	job, err := e.h.jobs.Get(context.Background(), as.tenant, "job-unlisted")
	require.NoError(t, err)
	require.Equal(t, types.Agent24JobSaved, job.Status)
}

func TestAgent24KnowledgeIngestJobView(t *testing.T) {
	zero := int64(0)
	n := int64(3)
	when := time.Date(2026, 11, 24, 1, 2, 3, 0, time.UTC)
	raw, err := json.Marshal(newAgent24IngestJobView(&types.Agent24IngestJob{
		ID: "j", OpClass: types.Agent24JobOpIngestSubmit, Status: types.Agent24JobIndexed,
		Enrichment: types.Agent24EnrichmentRunning, SavedAt: when,
		Cost: types.Agent24JobCost{Items: []types.Agent24JobCostItem{
			{Kind: "embedding", Measured: "unknown", Units: &zero, UnitType: "tokens"},
			{Kind: "storage", Measured: "exact", Units: &n, UnitType: "bytes"},
			{Kind: "extra", Measured: "unknown", Units: &n, UnitType: "tokens"},
		}},
	}))
	require.NoError(t, err)
	var view map[string]any
	require.NoError(t, json.Unmarshal(raw, &view))
	require.Equal(t, true, view["original_retained"])
	require.Equal(t, "retained_invisible", view["partial_artifacts"])
	require.Equal(t, "2026-11-24T01:02:03Z", view["saved_at"])
	require.Nil(t, view["terminal_at"])
	_, hasStop := view["stop_reason"]
	require.False(t, hasStop)
	items, _ := view["cost"].(map[string]any)["items"].([]any)
	require.Len(t, items, 3)
	require.Nil(t, items[0].(map[string]any)["units"])
	require.Equal(t, float64(3), items[1].(map[string]any)["units"])
	require.Nil(t, items[2].(map[string]any)["units"])
}

// #175 F1: a key of the same tenant principal whose scope is another knowledge base neither reads nor cancels a
// job of kbA: the same not_found as a missing job, and the job is left alone.
func TestAgent24KnowledgeIngestJobOutsideTheKeysScopeIsNotFound(t *testing.T) {
	e := newAgent24IngestEnv(t)
	now := time.Now().UTC()
	e.ensureDoc(t, "doc-a", types.ParseStatusPending)
	e.insertJob(t, types.Agent24IngestJob{
		ID: "job-a", KnowledgeID: "doc-a", Status: types.Agent24JobSaved, SavedAt: now, UpdatedAt: now,
	})
	other := agent24IngestAs([]string{"ingest"}, "kbOther")
	got := e.call(t, e.h.GetJob, other, agent24IngestJobBody("job-a"))
	require.Equal(t, http.StatusNotFound, got.code, got.raw)
	cancelled := e.call(t, e.h.CancelJob, other, agent24IngestJobBody("job-a"))
	require.Equal(t, http.StatusNotFound, cancelled.code, cancelled.raw)
	row, err := e.h.jobs.Get(context.Background(), 1, "job-a")
	require.NoError(t, err)
	require.Equal(t, types.Agent24JobSaved, row.Status)
}

// #175 M1: cancelling a saved or processing job stops its knowledge in the same transaction.
func TestAgent24KnowledgeIngestCancelStopsTheKnowledge(t *testing.T) {
	e := newAgent24IngestEnv(t)
	now := time.Now().UTC()
	e.ensureDoc(t, "doc-p", types.ParseStatusProcessing)
	e.insertJob(t, types.Agent24IngestJob{
		ID: "job-p", KnowledgeID: "doc-p", Status: types.Agent24JobProcessing, SavedAt: now, UpdatedAt: now,
	})
	r := e.call(t, e.h.CancelJob, agent24IngestAs([]string{"ingest"}), agent24IngestJobBody("job-p"))
	require.Equal(t, http.StatusOK, r.code, r.raw)
	var k types.Knowledge
	require.NoError(t, e.db.Where("id = ?", "doc-p").Take(&k).Error)
	require.Equal(t, types.ParseStatusCancelled, k.ParseStatus)
}

// #175 round 2: what lands between job.cancel's first read and its knowledge lock is decided on the locked row.
// The handler's cancelJob is called with the job as read before the change, as a concurrent worker would leave it.
func TestAgent24KnowledgeIngestCancelDecidesOnTheLockedRow(t *testing.T) {
	as := agent24IngestAs([]string{"ingest"})
	caller, ok := agent24CandidateCallerOf(as.ctx())
	require.True(t, ok)
	for _, c := range []struct {
		name       string
		change     func(e *agent24IngestEnv)
		wantEffect string
		wantErr    error
		wantParse  string
	}{
		{"a worker finalized it", func(e *agent24IngestEnv) {
			require.NoError(t, e.db.Model(&types.Knowledge{}).Where("id = ?", "doc-x").
				Update("parse_status", types.ParseStatusFinalizing).Error)
		}, agent24CancelEffectEnrichmentStopped, nil, types.ParseStatusFinalizing},
		{"a worker completed it", func(e *agent24IngestEnv) {
			require.NoError(t, e.db.Model(&types.Knowledge{}).Where("id = ?", "doc-x").
				Update("parse_status", types.ParseStatusCompleted).Error)
		}, agent24CancelEffectNone, nil, types.ParseStatusCompleted},
		{"a newer revision started", func(e *agent24IngestEnv) {
			require.NoError(t, e.db.Create(&types.KnowledgeProcessingSpan{
				KnowledgeID: "doc-x", Attempt: 2,
				SpanID: "s2", Name: "attempt", Kind: "attempt", Status: "running",
			}).Error)
		}, agent24CancelEffectNone, nil, types.ParseStatusProcessing},
		{"the document moved to another knowledge base", func(e *agent24IngestEnv) {
			require.NoError(t, e.db.Model(&types.Knowledge{}).Where("id = ?", "doc-x").
				Updates(map[string]any{"knowledge_base_id": "kbB", "parse_status": types.ParseStatusPending}).Error)
		}, "", errAgent24IngestJobGone, types.ParseStatusPending},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newAgent24IngestEnv(t)
			now := time.Now().UTC()
			e.ensureDoc(t, "doc-x", types.ParseStatusProcessing)
			e.insertJob(t, types.Agent24IngestJob{
				ID: "job-x", KnowledgeID: "doc-x", Status: types.Agent24JobProcessing, Attempt: 1,
				SavedAt: now, UpdatedAt: now,
			})
			stale, err := e.h.jobs.Get(context.Background(), 1, "job-x")
			require.NoError(t, err)
			c.change(e)
			effect, _, err := e.h.cancelJob(context.Background(), caller, as.scope, stale)
			if c.wantErr != nil {
				require.ErrorIs(t, err, c.wantErr)
			} else {
				require.NoError(t, err)
				require.Equal(t, c.wantEffect, effect)
			}
			var k types.Knowledge
			require.NoError(t, e.db.Where("id = ?", "doc-x").Take(&k).Error)
			require.Equal(t, c.wantParse, k.ParseStatus, "the knowledge is never cancelled by a stale decision")
		})
	}
}
