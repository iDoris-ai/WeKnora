// Agent24 fork product patch A24-KB-41 (WeKnora#136).
package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
)

type agent24ReconcileHarness struct {
	db   *gorm.DB
	jobs *repository.Agent24IngestJobStore
	r    *Agent24JobReconciler
	t0   time.Time
	now  time.Time
	ctx  context.Context
}

func newAgent24ReconcileHarness(t *testing.T) *agent24ReconcileHarness {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.Migrator().CreateTable(&types.Knowledge{}, &types.KnowledgeProcessingSpan{}))
	require.NoError(t, repository.EnsureAgent24IngestJobTable(db))
	h := &agent24ReconcileHarness{
		db: db, jobs: repository.NewAgent24IngestJobStore(db), ctx: context.Background(),
		t0: time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC),
	}
	h.now = h.t0
	h.r = NewAgent24JobReconciler(h.jobs)
	h.r.now = func() time.Time { return h.now }
	return h
}

func (h *agent24ReconcileHarness) add(
	t *testing.T, id, knowledge, parseStatus, message string, job types.Agent24JobStatus, attempt int,
) {
	t.Helper()
	if parseStatus != "" {
		require.NoError(t, h.db.Create(&types.Knowledge{
			ID: knowledge, TenantID: 7, KnowledgeBaseID: "kb", Type: "file", ParseStatus: parseStatus,
			ErrorMessage: message,
		}).Error)
	}
	n := int64(3)
	require.NoError(t, h.db.Create(&types.Agent24IngestJob{
		ID: id, TenantID: 7, KnowledgeBaseID: "kb", KnowledgeID: knowledge, OpClass: types.Agent24JobOpIngestSubmit,
		Status: job, Enrichment: types.Agent24EnrichmentNone, Attempt: attempt, SavedAt: h.t0, UpdatedAt: h.t0,
		Cost: types.Agent24JobCost{Items: []types.Agent24JobCostItem{
			{Kind: "storage", Units: &n, UnitType: "bytes", Measured: "exact"},
		}},
	}).Error)
}

func (h *agent24ReconcileHarness) span(t *testing.T, knowledge string, attempt int, stage, status string) {
	t.Helper()
	require.NoError(t, h.db.Create(&types.KnowledgeProcessingSpan{
		KnowledgeID: knowledge, Attempt: attempt, SpanID: knowledge + stage, Name: stage, Kind: types.SpanKindStage,
		Status: status,
	}).Error)
}

func (h *agent24ReconcileHarness) get(t *testing.T, id string) *types.Agent24IngestJob {
	t.Helper()
	j, err := h.r.Get(h.ctx, 7, id)
	require.NoError(t, err)
	require.NotNil(t, j)
	return j
}

func TestAgent24ReconcileGetMapsUpstreamFailures(t *testing.T) {
	h := newAgent24ReconcileHarness(t)
	h.add(t, "emb", "k-emb", types.ParseStatusFailed, "embedding model unavailable", types.Agent24JobProcessing, 1)
	h.span(t, "k-emb", 1, types.StageChunking, types.SpanStatusDone)
	h.span(t, "k-emb", 1, types.StageEmbedding, types.SpanStatusFailed)
	h.add(t, "parse", "k-parse", types.ParseStatusFailed, "docreader error", types.Agent24JobProcessing, 1)
	h.span(t, "k-parse", 1, types.StageDocReader, types.SpanStatusFailed)
	h.add(t, "enq", "k-enq", types.ParseStatusFailed, "Failed to enqueue processing task", types.Agent24JobSaved, 0)
	h.add(t, "stall", "k-stall", types.ParseStatusFailed,
		"task stuck in processing at embedding stage: no progress for > 1h10m0s, recovered by housekeeping",
		types.Agent24JobProcessing, 1)
	h.add(t, "unknown", "k-unknown", types.ParseStatusFailed, "boom", types.Agent24JobProcessing, 1)
	// the call-time embedding guard refused a destination or a redirect; upstream records the error text
	h.add(t, "denied", "k-denied", types.ParseStatusFailed,
		"failed to write vectors: "+types.ErrProcessingDenied.Error(), types.Agent24JobProcessing, 1)
	h.span(t, "k-denied", 1, types.StageEmbedding, types.SpanStatusFailed)
	for id, want := range map[string]string{
		"denied": "policy_denied",
		"emb":    "embedding_failed", "parse": "parse_failed", "enq": "enqueue_failed", "stall": "timeout",
		"unknown": "internal",
	} {
		j := h.get(t, id)
		require.Equal(t, types.Agent24JobFailed, j.Status, id)
		require.Equal(t, want, j.FailureReason, id)
		require.True(t, j.Cost.Final, "%s: nothing more is spent after a terminal state", id)
		require.NotNil(t, j.TerminalAt, id)
	}
}

func TestAgent24ReconcileGetIndexedAndDeleted(t *testing.T) {
	h := newAgent24ReconcileHarness(t)
	h.add(t, "fin", "k-fin", types.ParseStatusFinalizing, "", types.Agent24JobProcessing, 1)
	h.add(t, "done", "k-done", types.ParseStatusCompleted, "", types.Agent24JobProcessing, 1)
	h.add(t, "gone", "k-gone", "", "", types.Agent24JobProcessing, 1)

	fin := h.get(t, "fin")
	require.Equal(t, types.Agent24JobIndexed, fin.Status)
	require.Equal(t, types.Agent24EnrichmentRunning, fin.Enrichment)
	require.False(t, fin.Cost.Final, "enrichment may still spend")
	require.NotNil(t, fin.IndexedAt)

	done := h.get(t, "done")
	require.Equal(t, types.Agent24JobIndexed, done.Status)
	require.True(t, done.Cost.Final)

	gone := h.get(t, "gone")
	require.Equal(t, types.Agent24JobCancelled, gone.Status)
	require.Equal(t, types.Agent24JobCancelDeleted, gone.CancelReason)

	// enrichment finishing settles the cost
	require.NoError(t, h.db.Model(&types.Knowledge{}).Where("id = ?", "k-fin").
		Update("parse_status", types.ParseStatusCompleted).Error)
	fin = h.get(t, "fin")
	require.Equal(t, types.Agent24EnrichmentDone, fin.Enrichment)
	require.True(t, fin.Cost.Final)

	missing, err := h.r.Get(h.ctx, 7, "nope")
	require.NoError(t, err)
	require.Nil(t, missing)
}

func TestAgent24ReconcileSupersedeWaitsForTheRecordedAttempt(t *testing.T) {
	h := newAgent24ReconcileHarness(t)
	// an earlier attempt of the same knowledge exists; the new job has not recorded its own attempt yet
	h.add(t, "new", "k", types.ParseStatusPending, "", types.Agent24JobSaved, 0)
	h.span(t, "k", 4, types.StageDocReader, types.SpanStatusDone)
	require.Equal(t, types.Agent24JobSaved, h.get(t, "new").Status, "attempt 0 is not superseded by attempt 4")

	h.add(t, "old", "k2", types.ParseStatusProcessing, "", types.Agent24JobProcessing, 1)
	h.span(t, "k2", 2, types.StageDocReader, types.SpanStatusRunning)
	old := h.get(t, "old")
	require.Equal(t, types.Agent24JobCancelled, old.Status)
	require.Equal(t, types.Agent24JobCancelSuperseded, old.CancelReason)
}

func TestAgent24ReconcileSweepTimesOutSavedJobsAndPurges(t *testing.T) {
	h := newAgent24ReconcileHarness(t)
	h.add(t, "stuck", "k-stuck", types.ParseStatusPending, "", types.Agent24JobSaved, 1)
	h.add(t, "fresh", "k-fresh", types.ParseStatusPending, "", types.Agent24JobSaved, 1)
	require.NoError(t, h.db.Model(&types.Agent24IngestJob{}).Where("id = ?", "fresh").
		Update("saved_at", h.t0.Add(9*time.Minute)).Error)
	h.now = h.t0.Add(10*time.Minute + time.Second)
	n, err := h.r.Sweep(h.ctx)
	require.NoError(t, err)
	require.Equal(t, 2, n)

	stuck, err := h.jobs.Get(h.ctx, 7, "stuck")
	require.NoError(t, err)
	require.Equal(t, types.Agent24JobFailed, stuck.Status)
	require.Equal(t, types.Agent24JobFailTimeout, stuck.FailureReason)
	var row types.Knowledge
	require.NoError(t, h.db.Where("id = ?", "k-stuck").Take(&row).Error)
	require.Equal(t, types.ParseStatusFailed, row.ParseStatus, "the pending row nothing will pick up is failed too")
	fresh, err := h.jobs.Get(h.ctx, 7, "fresh")
	require.NoError(t, err)
	require.Equal(t, types.Agent24JobSaved, fresh.Status)

	// 90 days after the terminal state the job is purged
	h.now = h.now.Add(91 * 24 * time.Hour)
	_, err = h.r.Sweep(h.ctx)
	require.NoError(t, err)
	gone, err := h.jobs.Get(h.ctx, 7, "stuck")
	require.NoError(t, err)
	require.Nil(t, gone)
}

func TestAgent24ReconcileSweepDoesNotFailAPickedUpRow(t *testing.T) {
	h := newAgent24ReconcileHarness(t)
	h.add(t, "j", "k", types.ParseStatusPending, "", types.Agent24JobSaved, 1)
	h.now = h.t0.Add(time.Hour)
	// the worker marked the row processing after the sweep read the job but before it failed the row
	require.NoError(t, h.jobs.FailPendingKnowledge(h.ctx, 7, "k", "x", h.now))
	require.NoError(t, h.db.Model(&types.Knowledge{}).Where("id = ?", "k").
		Update("parse_status", types.ParseStatusProcessing).Error)
	require.NoError(t, h.jobs.FailPendingKnowledge(h.ctx, 7, "k", "x", h.now))
	var row types.Knowledge
	require.NoError(t, h.db.Where("id = ?", "k").Take(&row).Error)
	require.Equal(t, types.ParseStatusProcessing, row.ParseStatus)
}
