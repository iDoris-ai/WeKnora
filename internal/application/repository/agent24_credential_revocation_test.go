package repository

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
)

func agent24RevocationDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, EnsureAgent24CredentialRevocationTable(db))
	return db
}

func TestEnsureAgent24CredentialRevocationTable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })

	require.NoError(t, EnsureAgent24CredentialRevocationTable(db))
	require.True(t, db.Migrator().HasTable(&types.Agent24CredentialRevocation{}))
	require.NoError(t, EnsureAgent24CredentialRevocationTable(db))
}

// agent24RevocationCreateHook runs fn just before the CREATE TABLE for agent24_credential_revocations is
// executed, standing in for what a concurrent replica does between the first HasTable and CreateTable.
func agent24RevocationCreateHook(t *testing.T, db *gorm.DB, fn func(tx *gorm.DB)) {
	t.Helper()
	require.NoError(t, db.Callback().Raw().Before("gorm:raw").Register("test:agent24_revocation_create",
		func(tx *gorm.DB) {
			sql := tx.Statement.SQL.String()
			if strings.HasPrefix(sql, "CREATE TABLE") && strings.Contains(sql, "agent24_credential_revocations") {
				fn(tx)
			}
		}))
}

func TestEnsureAgent24CredentialRevocationTableLostRace(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	raced := false
	agent24RevocationCreateHook(t, db, func(tx *gorm.DB) {
		if raced {
			return
		}
		raced = true
		_, err := tx.Statement.ConnPool.ExecContext(tx.Statement.Context, tx.Statement.SQL.String(),
			tx.Statement.Vars...)
		require.NoError(t, err)
	})

	require.NoError(t, EnsureAgent24CredentialRevocationTable(db))
	require.True(t, raced)
	require.True(t, db.Migrator().HasTable(&types.Agent24CredentialRevocation{}))
}

func TestEnsureAgent24CredentialRevocationTableCreateFails(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	agent24RevocationCreateHook(t, db, func(tx *gorm.DB) { _ = tx.AddError(errors.New("create refused")) })

	err = EnsureAgent24CredentialRevocationTable(db)
	require.ErrorContains(t, err, "create refused")
	require.False(t, db.Migrator().HasTable(&types.Agent24CredentialRevocation{}))
}

func TestAgent24CredentialRevocationRevokeAndIsRevoked(t *testing.T) {
	db := agent24RevocationDB(t)
	store := NewAgent24CredentialRevocationStore(db)
	ctx := context.Background()
	expires := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	require.NoError(t, store.Revoke(ctx, 7, "jti-1", expires))

	revoked, err := store.IsRevoked(ctx, 7, "jti-1", expires.Add(-time.Minute))
	require.NoError(t, err)
	require.True(t, revoked)

	revoked, err = store.IsRevoked(ctx, 8, "jti-1", expires.Add(-time.Minute))
	require.NoError(t, err)
	require.False(t, revoked)

	revoked, err = store.IsRevoked(ctx, 7, "other", expires.Add(-time.Minute))
	require.NoError(t, err)
	require.False(t, revoked)

	revoked, err = store.IsRevoked(ctx, 7, "jti-1", expires.Add(time.Minute))
	require.NoError(t, err)
	require.False(t, revoked)
}

func TestAgent24CredentialRevocationRevokeTwiceKeepsOneRow(t *testing.T) {
	db := agent24RevocationDB(t)
	store := NewAgent24CredentialRevocationStore(db)
	ctx := context.Background()
	first := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	require.NoError(t, store.Revoke(ctx, 7, "jti-1", first))
	require.NoError(t, store.Revoke(ctx, 7, "jti-1", first.Add(time.Hour)))

	var n int64
	require.NoError(t, db.Model(&types.Agent24CredentialRevocation{}).Count(&n).Error)
	require.Equal(t, int64(1), n)

	var row types.Agent24CredentialRevocation
	require.NoError(t, db.First(&row).Error)
	require.WithinDuration(t, first, row.ExpiresAt, time.Second)
}

func TestAgent24CredentialRevocationRejectsInvalid(t *testing.T) {
	db := agent24RevocationDB(t)
	store := NewAgent24CredentialRevocationStore(db)
	ctx := context.Background()
	expires := time.Now().UTC().Add(time.Hour)
	cases := []struct {
		name     string
		tenantID uint64
		jti      string
	}{
		{"tenant zero", 0, "jti-1"},
		{"empty jti", 7, ""},
		{"blank jti", 7, "   "},
		{"jti too long", 7, strings.Repeat("j", 129)},
		{"jti control", 7, "bad\njti"},
		{"jti delete", 7, "bad\x7fjti"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Error(t, store.Revoke(ctx, tc.tenantID, tc.jti, expires))
		})
	}
	var n int64
	require.NoError(t, db.Model(&types.Agent24CredentialRevocation{}).Count(&n).Error)
	require.Equal(t, int64(0), n)
}

func TestAgent24CredentialRevocationPurgeExpired(t *testing.T) {
	db := agent24RevocationDB(t)
	store := NewAgent24CredentialRevocationStore(db)
	ctx := context.Background()
	expiredAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	liveAt := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	require.NoError(t, store.Revoke(ctx, 7, "old", expiredAt))
	require.NoError(t, store.Revoke(ctx, 8, "also-old", expiredAt))
	require.NoError(t, store.Revoke(ctx, 7, "live", liveAt))

	n, err := store.PurgeExpired(ctx, expiredAt.Add(time.Second))
	require.NoError(t, err)
	require.Equal(t, int64(2), n)

	var rows []types.Agent24CredentialRevocation
	require.NoError(t, db.Find(&rows).Error)
	require.Len(t, rows, 1)
	require.Equal(t, "live", rows[0].JTI)

	revoked, err := store.IsRevoked(ctx, 7, "live", time.Now())
	require.NoError(t, err)
	require.True(t, revoked)
	revoked, err = store.IsRevoked(ctx, 7, "old", time.Now())
	require.NoError(t, err)
	require.False(t, revoked)
}
