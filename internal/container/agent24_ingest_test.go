// Agent24 fork product patch A24-KB-41 (WeKnora#136).
package container

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"
	"go.uber.org/dig"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type agent24FakeKnowledgeService struct{ interfaces.KnowledgeService }

// The graph resolves in both branches; the constructor itself refuses a knowledge service that is not this
// package's implementation, which is how the fake shows the providers reached it.
func TestProvideAgent24IngestResolves(t *testing.T) {
	for _, redis := range []bool{false, true} {
		c := dig.New()
		db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
		require.NoError(t, err)
		require.NoError(t, c.Provide(func() *gorm.DB { return db }))
		require.NoError(t, c.Provide(func() interfaces.KnowledgeService { return agent24FakeKnowledgeService{} }))
		if redis {
			require.NoError(t, c.Provide(func() *asynq.Inspector { return nil }))
		}
		provideAgent24Ingest(c, redis)
		err = c.Invoke(func(*service.Agent24Ingest) {})
		require.ErrorContains(t, err, "incomplete wiring", "redis=%v", redis)
	}
}

func TestEnsureAgent24IngestJobsCreatesTheTable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, ensureAgent24IngestJobs(db))
	require.True(t, db.Migrator().HasTable(&types.Agent24IngestJob{}))
	// A24-KB-42
	for _, model := range []any{
		&types.Agent24KnowledgeCandidate{}, &types.Agent24WriteIdempotency{}, &types.Agent24KnowledgeAdmin{},
		&types.Agent24EntryWithdrawal{},
	} {
		require.True(t, db.Migrator().HasTable(model), "%T", model)
	}
}

// A24-KB-43 (PR-Daemon on #156): each background sweep has its own loop; a failing or panicking run is logged and
// the loop goes on, and closing stop ends it.
func TestAgent24EveryMinuteSurvivesPanics(t *testing.T) {
	old := agent24SweepInterval
	agent24SweepInterval = 5 * time.Millisecond
	t.Cleanup(func() { agent24SweepInterval = old })
	var runs atomic.Int32
	stop := make(chan struct{})
	done := agent24EveryMinute(stop, "test sweep", func(ctx context.Context) error {
		_, hasDeadline := ctx.Deadline()
		require.True(t, hasDeadline)
		switch runs.Add(1) {
		case 1:
			panic("boom")
		case 2:
			return errors.New("failed")
		}
		return nil
	})
	require.Eventually(t, func() bool { return runs.Load() >= 3 }, 2*time.Second, time.Millisecond)
	close(stop)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the loop did not end after stop")
	}
}
