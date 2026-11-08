// Agent24 fork product patch A24-KB-41 (WeKnora#136).
package container

import (
	"testing"

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
}
