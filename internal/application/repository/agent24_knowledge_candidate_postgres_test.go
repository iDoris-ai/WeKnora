// Agent24 fork product patch A24-KB-42 (WeKnora#146 S1): the behaviour SQLite cannot show.
package repository

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
)

func agent24CandidatePostgres(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("WEKNORA_REPOSITORY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set WEKNORA_REPOSITORY_TEST_POSTGRES_DSN for the PostgreSQL candidate store tests")
	}
	admin, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	adminSQL, err := admin.DB()
	require.NoError(t, err)
	schema := "agent24_candidates_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	require.NoError(t, admin.Exec("CREATE SCHEMA "+schema).Error)
	t.Cleanup(func() {
		require.NoError(t, admin.Exec("DROP SCHEMA "+schema+" CASCADE").Error)
		require.NoError(t, adminSQL.Close())
	})
	separator := " "
	if strings.Contains(dsn, "://") {
		separator = "?"
		if strings.Contains(dsn, "?") {
			separator = "&"
		}
	}
	db, err := gorm.Open(postgres.Open(dsn+separator+"search_path="+schema), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	return db
}

func TestAgent24CandidateTablesPostgres(t *testing.T) {
	db := agent24CandidatePostgres(t)
	ctx := context.Background()
	// the job table as A24-KB-41 created it before the widening: principal_user_id varchar(64)
	require.NoError(t, EnsureAgent24IngestJobTable(db))
	require.NoError(t, db.Exec("ALTER TABLE agent24_ingest_jobs ALTER COLUMN principal_user_id TYPE varchar(64)").Error)
	long := "api_external_user:1:" + fmt.Sprintf("%0128d", 7)
	tooLong := agent24TestJob("j0", "k", types.Agent24JobSaved, agent24T0)
	tooLong.PrincipalUserID = long
	require.Error(t, db.Create(tooLong).Error, "the column is narrow before Ensure")

	require.NoError(t, EnsureAgent24CandidateTables(db))
	require.NoError(t, EnsureAgent24CandidateTables(db))
	cols, err := db.Migrator().ColumnTypes(&types.Agent24IngestJob{})
	require.NoError(t, err)
	for _, c := range cols {
		if c.Name() == "principal_user_id" {
			n, ok := c.Length()
			require.True(t, ok)
			require.GreaterOrEqual(t, n, int64(agent24PrincipalColumnSize))
		}
	}
	job := agent24TestJob("j1", "k", types.Agent24JobSaved, agent24T0)
	job.PrincipalUserID = long
	require.NoError(t, db.Create(job).Error)

	s := NewAgent24CandidateStore(db)
	require.NoError(t, s.CreateCandidate(ctx, agent24TestCandidate("c1", long)))
	dup := agent24TestCandidate("c2", long)
	dup.ContentSHA256 = "h-c1"
	// inside one transaction: the refused insert leaves the transaction usable for the duplicate's read
	err = s.Transaction(ctx, func(tx *Agent24CandidateStore) error {
		require.ErrorIs(t, tx.CreateCandidate(ctx, dup), ErrAgent24CandidateDuplicate)
		found, err := tx.FindSubmittedDuplicate(ctx, dup)
		require.NoError(t, err)
		require.NotNil(t, found)
		require.Equal(t, "c1", found.ID)

		rec := &types.Agent24WriteIdempotency{
			TenantID: 1, Principal: long, Op: "candidate.propose", Key: "k", Fingerprint: "f",
			StatusCode: 200, Response: "{}", CreatedAt: agent24T0, ExpiresAt: agent24T0.AddDate(0, 0, 90),
		}
		require.NoError(t, tx.PutIdempotency(ctx, rec))
		again := *rec
		require.ErrorIs(t, tx.PutIdempotency(ctx, &again), ErrAgent24IdempotencyExists)
		got, err := tx.GetIdempotency(ctx, 1, long, "candidate.propose", "k", agent24T0)
		require.NoError(t, err)
		require.NotNil(t, got)
		return nil
	})
	require.NoError(t, err)

	// the partial index only covers submitted rows
	ok, err := s.TransitionCandidate(ctx, 1, "c1",
		types.Agent24CandidateTransition{To: types.Agent24CandidateWithdrawn}, agent24T0)
	require.NoError(t, err)
	require.True(t, ok)
	require.NoError(t, s.CreateCandidate(ctx, dup))

	err = s.Transaction(ctx, func(tx *Agent24CandidateStore) error {
		c, err := tx.LockCandidate(ctx, 1, "c2")
		require.NoError(t, err)
		require.NotNil(t, c)
		return nil
	})
	require.NoError(t, err)

	first, err := s.Withdraw(ctx, &types.Agent24EntryWithdrawal{
		TenantID: 1, KnowledgeID: "k1", WithdrawnBy: long, WithdrawnAt: agent24T0,
	})
	require.NoError(t, err)
	require.True(t, first)
	first, err = s.Withdraw(ctx, &types.Agent24EntryWithdrawal{
		TenantID: 1, KnowledgeID: "k1", WithdrawnBy: "other", WithdrawnAt: agent24T0,
	})
	require.NoError(t, err)
	require.False(t, first)
}
