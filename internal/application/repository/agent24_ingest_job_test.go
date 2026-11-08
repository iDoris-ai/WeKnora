// Agent24 fork product patch A24-KB-41 (WeKnora#136).
package repository

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
)

func agent24IngestJobDB(t *testing.T) (*gorm.DB, *Agent24IngestJobStore) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, EnsureAgent24IngestJobTable(db))
	return db, NewAgent24IngestJobStore(db)
}

func agent24TestJob(id, knowledge string, status types.Agent24JobStatus, saved time.Time) *types.Agent24IngestJob {
	return &types.Agent24IngestJob{
		ID: id, TenantID: 1, KnowledgeBaseID: "kb", KnowledgeID: knowledge, OpClass: types.Agent24JobOpIngestSubmit,
		Status: status, Enrichment: types.Agent24EnrichmentNone, SavedAt: saved, UpdatedAt: saved,
	}
}

func TestEnsureAgent24IngestJobTableIsIdempotentAndIndexed(t *testing.T) {
	db, _ := agent24IngestJobDB(t)
	require.NoError(t, EnsureAgent24IngestJobTable(db))
	for _, name := range agent24IngestJobIndexes {
		require.True(t, db.Migrator().HasIndex(&types.Agent24IngestJob{}, name), name)
	}
	// a table left without its indexes gets them back
	require.NoError(t, db.Migrator().DropIndex(&types.Agent24IngestJob{}, "idx_agent24_ingest_jobs_status"))
	require.NoError(t, EnsureAgent24IngestJobTable(db))
	require.True(t, db.Migrator().HasIndex(&types.Agent24IngestJob{}, "idx_agent24_ingest_jobs_status"))
}

func TestAgent24IngestJobCreateNeedsTheCallersTransaction(t *testing.T) {
	db, store := agent24IngestJobDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	require.Error(t, store.Create(ctx, nil, agent24TestJob("j0", "k", types.Agent24JobSaved, t0)))
	require.Error(t, store.SupersedeForKnowledge(ctx, nil, 1, "k", t0))
	// a rolled-back transaction leaves no job
	errRollback := errors.New("rollback")
	err := db.Transaction(func(tx *gorm.DB) error {
		require.NoError(t, store.Create(ctx, tx, agent24TestJob("j1", "k", types.Agent24JobSaved, t0)))
		return errRollback
	})
	require.ErrorIs(t, err, errRollback)
	got, err := store.Get(ctx, 1, "j1")
	require.NoError(t, err)
	require.Nil(t, got)
}

func TestAgent24IngestJobSupersedeForKnowledge(t *testing.T) {
	db, store := agent24IngestJobDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	indexed := agent24TestJob("idx", "k", types.Agent24JobIndexed, t0)
	indexed.Enrichment = types.Agent24EnrichmentRunning
	done := agent24TestJob("done", "k", types.Agent24JobIndexed, t0)
	done.Enrichment = types.Agent24EnrichmentDone
	other := agent24TestJob("other", "k2", types.Agent24JobProcessing, t0)
	otherTenant := agent24TestJob("tenant2", "k", types.Agent24JobSaved, t0)
	otherTenant.TenantID = 2
	for _, j := range []*types.Agent24IngestJob{
		agent24TestJob("saved", "k", types.Agent24JobSaved, t0),
		agent24TestJob("proc", "k", types.Agent24JobProcessing, t0),
		agent24TestJob("failed", "k", types.Agent24JobFailed, t0),
		indexed, done, other, otherTenant,
	} {
		require.NoError(t, db.Create(j).Error)
	}
	t1 := t0.Add(time.Minute)
	require.NoError(t, db.Transaction(func(tx *gorm.DB) error {
		return store.SupersedeForKnowledge(ctx, tx, 1, "k", t1)
	}))
	want := map[string][3]string{ // status, cancel_reason, enrichment/stop_reason
		"saved":   {"cancelled", "superseded", "none/"},
		"proc":    {"cancelled", "superseded", "none/"},
		"failed":  {"failed", "", "none/"},
		"idx":     {"indexed", "", "stopped/superseded"},
		"done":    {"indexed", "", "done/"},
		"other":   {"processing", "", "none/"},
		"tenant2": {"saved", "", "none/"},
	}
	for id, w := range want {
		tenant := uint64(1)
		if id == "tenant2" {
			tenant = 2
		}
		got, err := store.Get(ctx, tenant, id)
		require.NoError(t, err)
		require.Equal(t, w, [3]string{string(got.Status), got.CancelReason, got.Enrichment + "/" + got.StopReason}, id)
		if w[1] == "superseded" {
			require.NotNil(t, got.TerminalAt, id)
		}
		// the jobs this supersede ended spend nothing more; the others keep their cost as it was
		final := id == "saved" || id == "proc" || id == "idx"
		require.Equal(t, final, got.Cost.Final, id)
	}
}

func TestAgent24IngestJobSetAttemptOnlyOnASavedJob(t *testing.T) {
	db, store := agent24IngestJobDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	require.NoError(t, db.Create(agent24TestJob("saved", "k", types.Agent24JobSaved, t0)).Error)
	require.NoError(t, db.Create(agent24TestJob("gone", "k", types.Agent24JobCancelled, t0)).Error)
	ok, err := store.SetAttempt(ctx, 1, "saved", 3)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = store.SetAttempt(ctx, 1, "gone", 4)
	require.NoError(t, err)
	require.False(t, ok, "a superseded job records no attempt")
	got, err := store.Get(ctx, 1, "gone")
	require.NoError(t, err)
	require.Equal(t, 0, got.Attempt)
	got, err = store.Get(ctx, 1, "saved")
	require.NoError(t, err)
	require.Equal(t, 3, got.Attempt)
}

func TestAgent24IngestJobOccupyingAndApply(t *testing.T) {
	db, store := agent24IngestJobDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	require.NoError(t, db.Create(agent24TestJob("old", "k", types.Agent24JobFailed, t0)).Error)
	got, err := store.Occupying(ctx, 1, "k")
	require.NoError(t, err)
	require.Nil(t, got, "a failed job does not occupy")
	require.NoError(t, db.Create(agent24TestJob("j", "k", types.Agent24JobSaved, t0)).Error)
	got, err = store.Occupying(ctx, 1, "k")
	require.NoError(t, err)
	require.Equal(t, "j", got.ID)
	none, err := store.Occupying(ctx, 2, "k")
	require.NoError(t, err)
	require.Nil(t, none, "other tenant")

	t1 := t0.Add(time.Minute)
	ok, err := store.Apply(ctx, *got,
		types.Agent24JobChange{Status: types.Agent24JobProcessing, Enrichment: "none"}, t1)
	require.NoError(t, err)
	require.True(t, ok)
	// a second writer holding the stale saved copy loses
	ok, err = store.Apply(ctx, *got, types.Agent24JobChange{
		Status: types.Agent24JobCancelled, CancelReason: "user", Enrichment: "none",
	}, t1)
	require.NoError(t, err)
	require.False(t, ok)
	cur, err := store.Get(ctx, 1, "j")
	require.NoError(t, err)
	require.Equal(t, types.Agent24JobProcessing, cur.Status)
	require.NotNil(t, cur.ProcessingAt)
	require.Nil(t, cur.TerminalAt)

	ok, err = store.Apply(ctx, *cur, types.Agent24JobChange{
		Status: types.Agent24JobFailed, FailureReason: "embedding_failed", Enrichment: "none",
	}, t1.Add(time.Minute))
	require.NoError(t, err)
	require.True(t, ok)
	cur, err = store.Get(ctx, 1, "j")
	require.NoError(t, err)
	require.Equal(t, "embedding_failed", cur.FailureReason)
	require.NotNil(t, cur.TerminalAt)
}

func TestAgent24IngestJobListAndPurge(t *testing.T) {
	db, store := agent24IngestJobDB(t)
	ctx := context.Background()
	t0 := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	old := t0
	recent := t0.AddDate(0, 3, 0)
	running := agent24TestJob("running", "k1", types.Agent24JobIndexed, old)
	running.Enrichment = types.Agent24EnrichmentRunning
	doneOld := agent24TestJob("done-old", "k2", types.Agent24JobIndexed, old)
	doneOld.Enrichment = types.Agent24EnrichmentDone
	failedOld := agent24TestJob("failed-old", "k3", types.Agent24JobFailed, old)
	failedOld.TerminalAt = &old
	failedNew := agent24TestJob("failed-new", "k4", types.Agent24JobFailed, recent)
	failedNew.TerminalAt = &recent
	for _, j := range []*types.Agent24IngestJob{
		agent24TestJob("saved", "k5", types.Agent24JobSaved, recent), running, doneOld, failedOld, failedNew,
		agent24TestJob("proc", "k6", types.Agent24JobProcessing, old),
	} {
		require.NoError(t, db.Create(j).Error)
	}
	jobs, err := store.ListUnsettled(ctx, 10)
	require.NoError(t, err)
	ids := make([]string, 0, len(jobs))
	for _, j := range jobs {
		ids = append(ids, j.ID)
	}
	require.ElementsMatch(t, []string{"saved", "running", "proc"}, ids)
	n, err := store.PurgeSettled(ctx, t0.AddDate(0, 0, 30))
	require.NoError(t, err)
	require.Equal(t, int64(2), n)
	for _, id := range []string{"done-old", "failed-old"} {
		got, err := store.Get(ctx, 1, id)
		require.NoError(t, err)
		require.Nil(t, got, id)
	}
	for _, id := range []string{"running", "failed-new", "saved", "proc"} {
		got, err := store.Get(ctx, 1, id)
		require.NoError(t, err)
		require.NotNil(t, got, id)
	}
}
