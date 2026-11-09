// Agent24 fork product patch A24-KB-42 (WeKnora#146 S1).
package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
)

func TestAgent24CandidateRetentionSweep(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, repository.EnsureAgent24IngestJobTable(db))
	require.NoError(t, repository.EnsureAgent24CandidateTables(db))
	store := repository.NewAgent24CandidateStore(db)
	ctx := context.Background()
	t0 := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	mk := func(id string, to types.Agent24CandidateStatus, at time.Time) {
		c := &types.Agent24KnowledgeCandidate{
			ID: id, TenantID: 1, Author: "p", TargetKnowledgeBaseID: "kb", Mode: types.Agent24CandidateCreate,
			ContentSHA256: "h-" + id, Kind: types.Agent24EntryKindNote, Title: "t", Body: "b", SessionRef: "s",
			MessageTo: 1, SkillVersion: "v", RedactionRulesVersion: 1, Status: types.Agent24CandidateSubmitted,
			SubmittedAt: at,
		}
		require.NoError(t, store.CreateCandidate(ctx, c))
		ok, err := store.TransitionCandidate(ctx, 1, id,
			types.Agent24CandidateTransition{To: to, ReturnReason: types.Agent24ReturnReviewer, Note: "n"}, at)
		require.NoError(t, err)
		require.True(t, ok)
	}
	now := t0.Add(100 * 24 * time.Hour)
	mk("ancient", types.Agent24CandidateReturned, t0)                     // past 90 days: deleted
	mk("hour", types.Agent24CandidateWithdrawn, now.Add(-time.Hour))      // past the hour: purged
	mk("fresh", types.Agent24CandidateReturned, now.Add(-30*time.Minute)) // inside the hour: kept
	require.NoError(t, store.PutIdempotency(ctx, &types.Agent24WriteIdempotency{
		TenantID: 1, Principal: "p", Op: "candidate.propose", Key: "old", Fingerprint: "f", StatusCode: 200,
		Response: "{}", CreatedAt: t0, ExpiresAt: t0.Add(90 * 24 * time.Hour),
	}))

	r := NewAgent24CandidateRetention(store)
	r.now = func() time.Time { return now }
	res, err := r.Sweep(ctx)
	require.NoError(t, err)
	// the ancient one is purged first, then deleted
	require.Equal(t, Agent24CandidateRetentionResult{ContentPurged: 2, CandidatesDeleted: 1, IdempotencyDeleted: 1},
		res)
	got, err := store.GetCandidate(ctx, 1, "ancient")
	require.NoError(t, err)
	require.Nil(t, got)
	got, err = store.GetCandidate(ctx, 1, "hour")
	require.NoError(t, err)
	require.Empty(t, got.Body)
	require.NotNil(t, got.PurgedAt)
	got, err = store.GetCandidate(ctx, 1, "fresh")
	require.NoError(t, err)
	require.Equal(t, "b", got.Body)

	// a failing step does not stop the others, and the error is returned: the purge (an update) fails, the
	// deletes still run
	mk("ancient2", types.Agent24CandidateReturned, t0)
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register("agent24_test_fail",
		func(tx *gorm.DB) { _ = tx.AddError(errors.New("update refused")) }))
	res, err = r.Sweep(ctx)
	require.ErrorContains(t, err, "purge candidate content")
	require.Equal(t, int64(1), res.CandidatesDeleted)
	require.NoError(t, db.Callback().Update().Remove("agent24_test_fail"))

	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	_, err = r.Sweep(ctx)
	require.Error(t, err)
}

// A24-KB-43 (PR-Daemon on #156): the content threshold is 55 minutes, so with one tick and one sweep deadline of
// lateness the content is still gone before the hour of W8.9.
func TestAgent24CandidateRetentionBoundary(t *testing.T) {
	require.Equal(t, 55*time.Minute, agent24CandidateContentRetention)
	require.LessOrEqual(t, agent24CandidateContentRetention+2*time.Minute, time.Hour)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, repository.EnsureAgent24IngestJobTable(db))
	require.NoError(t, repository.EnsureAgent24CandidateTables(db))
	store := repository.NewAgent24CandidateStore(db)
	ctx := context.Background()
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	for id, age := range map[string]time.Duration{
		"keep": 55*time.Minute - time.Second, "purge": 55*time.Minute + time.Second, "late": 59 * time.Minute,
	} {
		at := now.Add(-age)
		require.NoError(t, store.CreateCandidate(ctx, &types.Agent24KnowledgeCandidate{
			ID: id, TenantID: 1, Author: "p", TargetKnowledgeBaseID: "kb", Mode: types.Agent24CandidateCreate,
			ContentSHA256: "h-" + id, Kind: types.Agent24EntryKindNote, Title: "t", Body: "b", SessionRef: "s",
			SkillVersion: "v", RedactionRulesVersion: 1, Status: types.Agent24CandidateSubmitted, SubmittedAt: at,
		}))
		ok, err := store.TransitionCandidate(ctx, 1, id,
			types.Agent24CandidateTransition{To: types.Agent24CandidateWithdrawn}, at)
		require.NoError(t, err)
		require.True(t, ok)
	}
	r := NewAgent24CandidateRetention(store)
	r.now = func() time.Time { return now }
	res, err := r.Sweep(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(2), res.ContentPurged)
	for id, purged := range map[string]bool{"keep": false, "purge": true, "late": true} {
		got, err := store.GetCandidate(ctx, 1, id)
		require.NoError(t, err)
		require.Equal(t, purged, got.PurgedAt != nil, id)
	}
}
