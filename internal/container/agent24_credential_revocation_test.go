package container

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// agent24RevocationCleaner is the smallest ResourceCleaner. The container tests have no existing double.
type agent24RevocationCleaner struct {
	name string
	fn   types.CleanupFunc
}

func (c *agent24RevocationCleaner) Register(fn types.CleanupFunc) { c.fn = fn }

func (c *agent24RevocationCleaner) RegisterWithName(name string, fn types.CleanupFunc) {
	c.name = name
	c.fn = fn
}

func (c *agent24RevocationCleaner) Promote(string) {}

func (c *agent24RevocationCleaner) Cleanup(context.Context) []error {
	if c.fn == nil {
		return nil
	}
	if err := c.fn(); err != nil {
		return []error{err}
	}
	return nil
}

func openAgent24RevocationDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	return db
}

func TestEnsureAgent24CredentialRevocationsStandalone(t *testing.T) {
	t.Setenv(handler.Agent24KnowledgeModeEnv, handler.Agent24KnowledgeModeStandalone)
	db := openAgent24RevocationDB(t)
	cleaner := &agent24RevocationCleaner{}
	require.NoError(t, ensureAgent24CredentialRevocations(db, cleaner))
	require.False(t, db.Migrator().HasTable(&types.Agent24CredentialRevocation{}))
	require.Nil(t, cleaner.fn)
}

func TestEnsureAgent24CredentialRevocationsIntegration(t *testing.T) {
	t.Setenv(handler.Agent24KnowledgeModeEnv, handler.Agent24KnowledgeModeIntegration)
	db := openAgent24RevocationDB(t)
	cleaner := &agent24RevocationCleaner{}
	t.Cleanup(func() { _ = cleaner.Cleanup(context.Background()) })
	require.NoError(t, ensureAgent24CredentialRevocations(db, cleaner))
	require.True(t, db.Migrator().HasTable(&types.Agent24CredentialRevocation{}))
	require.Equal(t, "Agent24CredentialRevocationPurge", cleaner.name)
	require.NotNil(t, cleaner.fn)
	var _ interfaces.ResourceCleaner = cleaner
}
