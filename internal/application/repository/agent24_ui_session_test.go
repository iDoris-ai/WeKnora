// Agent24 fork product patch A24-KB-31 (KB-C2-01a, WeKnora#133, CONTRACT-UI-BRIDGE-v1 U2–U3).

package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

func agent24UISessionDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	require.NoError(t, EnsureAgent24UISessionTables(db))
	return db
}

func requireAgent24UISessionIndexes(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, name := range []string{
		"idx_agent24_ui_ticket_id",
		"idx_agent24_ui_ticket_vph",
		"idx_agent24_ui_ticket_expires_at",
	} {
		require.True(t, db.Migrator().HasIndex(&types.Agent24UITicket{}, name), name)
	}
	require.True(t, db.Migrator().HasIndex(&types.Agent24UIFamily{}, "idx_agent24_ui_family_expires_at"))
}

func newAgent24UITicket(t *testing.T, vph, hash, id string, exp time.Time) *types.Agent24UITicket {
	t.Helper()
	ticket := &types.Agent24UITicket{
		TicketHash:   hash,
		TicketID:     id,
		TenantID:     7,
		Principal:    "principal-a",
		CredentialID: "cred-1",
		BindingTag:   "bind",
		PartitionTag: "part",
		VPH:          vph,
		Generation:   3,
		ExpiresAt:    exp,
	}
	require.NoError(t, ticket.SetScopeKnowledgeBaseIDs([]string{"kb-1"}))
	return ticket
}

func countAgent24UI(t *testing.T, db *gorm.DB, model any) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Model(model).Count(&n).Error)
	return n
}

func TestEnsureAgent24UISessionTables(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })

	require.NoError(t, EnsureAgent24UISessionTables(db))
	require.True(t, db.Migrator().HasTable(&types.Agent24UITicket{}))
	require.True(t, db.Migrator().HasTable(&types.Agent24UIFamily{}))
	requireAgent24UISessionIndexes(t, db)
	require.NoError(t, EnsureAgent24UISessionTables(db))

	require.NoError(t, db.Migrator().DropIndex(&types.Agent24UITicket{}, "idx_agent24_ui_ticket_vph"))
	require.False(t, db.Migrator().HasIndex(&types.Agent24UITicket{}, "idx_agent24_ui_ticket_vph"))
	require.NoError(t, EnsureAgent24UISessionTables(db))
	require.True(t, db.Migrator().HasIndex(&types.Agent24UITicket{}, "idx_agent24_ui_ticket_vph"))
}

func TestAgent24UISessionCreateTicket(t *testing.T) {
	db := agent24UISessionDB(t)
	store := NewAgent24UISessionStore(db)
	ctx := context.Background()
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

	t.Run("ok", func(t *testing.T) {
		ticket := newAgent24UITicket(t, "vph-ok", "hash-ok", "id-ok", now.Add(time.Minute))
		credExp := now.Add(time.Hour)
		ticket.CredentialExpiresAt = &credExp
		require.NoError(t, ticket.SetScopeKnowledgeBaseIDs([]string{"kb-1", "kb-2"}))
		require.NoError(t, store.CreateTicket(ctx, ticket))

		var stored types.Agent24UITicket
		require.NoError(t, db.Where("ticket_hash = ?", "hash-ok").Take(&stored).Error)
		ids, err := stored.ScopeKnowledgeBaseIDs()
		require.NoError(t, err)
		require.Equal(t, []string{"kb-1", "kb-2"}, ids)
		require.NotNil(t, stored.CredentialExpiresAt)

		var family types.Agent24UIFamily
		require.NoError(t, db.Where("vph = ?", "vph-ok").Take(&family).Error)
		require.Equal(t, uint64(7), family.TenantID)
		require.Equal(t, "principal-a", family.Principal)
		require.Nil(t, family.RevokedAt)
		wantExp := ticket.ExpiresAt.Add(types.Agent24UISessionMaxLifetime)
		require.WithinDuration(t, wantExp, family.ExpiresAt, time.Second)
	})

	t.Run("other tenant", func(t *testing.T) {
		first := newAgent24UITicket(t, "vph-owner", "hash-owner-1", "id-owner-1", now.Add(time.Minute))
		require.NoError(t, store.CreateTicket(ctx, first))
		other := newAgent24UITicket(t, "vph-owner", "hash-owner-2", "id-owner-2", now.Add(time.Minute))
		other.TenantID = 8
		require.ErrorIs(t, store.CreateTicket(ctx, other), interfaces.ErrAgent24UIFamilyOwner)
		require.Equal(t, int64(1), countWhere(t, db, &types.Agent24UITicket{}, "vph = ?", "vph-owner"))
	})

	t.Run("other principal", func(t *testing.T) {
		first := newAgent24UITicket(t, "vph-prin", "hash-prin-1", "id-prin-1", now.Add(time.Minute))
		require.NoError(t, store.CreateTicket(ctx, first))
		other := newAgent24UITicket(t, "vph-prin", "hash-prin-2", "id-prin-2", now.Add(time.Minute))
		other.Principal = "principal-b"
		require.ErrorIs(t, store.CreateTicket(ctx, other), interfaces.ErrAgent24UIFamilyOwner)
		require.Equal(t, int64(1), countWhere(t, db, &types.Agent24UITicket{}, "vph = ?", "vph-prin"))
	})

	t.Run("revoked family", func(t *testing.T) {
		first := newAgent24UITicket(t, "vph-rev", "hash-rev-1", "id-rev-1", now.Add(time.Minute))
		require.NoError(t, store.CreateTicket(ctx, first))
		require.NoError(t, store.RevokeFamily(ctx, 7, "principal-a", "vph-rev", now))
		second := newAgent24UITicket(t, "vph-rev", "hash-rev-2", "id-rev-2", now.Add(time.Minute))
		require.ErrorIs(t, store.CreateTicket(ctx, second), interfaces.ErrAgent24UIFamilyRevoked)
		require.Equal(t, int64(1), countWhere(t, db, &types.Agent24UITicket{}, "vph = ?", "vph-rev"))
	})

	t.Run("empty scope", func(t *testing.T) {
		beforeTickets := countAgent24UI(t, db, &types.Agent24UITicket{})
		beforeFamilies := countAgent24UI(t, db, &types.Agent24UIFamily{})
		for _, scope := range []string{"", "[]", "not-json"} {
			ticket := newAgent24UITicket(t, "vph-empty-"+scope, "hash-empty-"+scope, "id-empty-"+scope, now)
			ticket.ScopeKBIDs = scope
			require.Error(t, store.CreateTicket(ctx, ticket))
		}
		blank := newAgent24UITicket(t, "vph-set-empty", "hash-set-empty", "id-set-empty", now)
		require.Error(t, blank.SetScopeKnowledgeBaseIDs(nil))
		require.Error(t, blank.SetScopeKnowledgeBaseIDs([]string{}))
		require.Equal(t, beforeTickets, countAgent24UI(t, db, &types.Agent24UITicket{}))
		require.Equal(t, beforeFamilies, countAgent24UI(t, db, &types.Agent24UIFamily{}))
	})

	t.Run("missing fields", func(t *testing.T) {
		base := newAgent24UITicket(t, "vph-miss", "hash-miss", "id-miss", now)
		cases := []*types.Agent24UITicket{
			nil,
			cloneTicket(base, func(t *types.Agent24UITicket) { t.TicketHash = "" }),
			cloneTicket(base, func(t *types.Agent24UITicket) { t.TicketID = "" }),
			cloneTicket(base, func(t *types.Agent24UITicket) { t.TenantID = 0 }),
			cloneTicket(base, func(t *types.Agent24UITicket) { t.Principal = "" }),
			cloneTicket(base, func(t *types.Agent24UITicket) { t.VPH = "" }),
		}
		for _, ticket := range cases {
			require.Error(t, store.CreateTicket(ctx, ticket))
		}
	})

	t.Run("extends family expiry", func(t *testing.T) {
		firstExp := now.Add(time.Minute)
		secondExp := now.Add(time.Hour)
		earlierExp := now.Add(30 * time.Second)
		first := newAgent24UITicket(t, "vph-ext", "hash-ext-1", "id-ext-1", firstExp)
		second := newAgent24UITicket(t, "vph-ext", "hash-ext-2", "id-ext-2", secondExp)
		earlier := newAgent24UITicket(t, "vph-ext", "hash-ext-3", "id-ext-3", earlierExp)
		require.NoError(t, store.CreateTicket(ctx, first))
		require.NoError(t, store.CreateTicket(ctx, second))
		require.NoError(t, store.CreateTicket(ctx, earlier))

		var family types.Agent24UIFamily
		require.NoError(t, db.Where("vph = ?", "vph-ext").Take(&family).Error)
		require.WithinDuration(t, secondExp.Add(types.Agent24UISessionMaxLifetime), family.ExpiresAt, time.Second)
		require.Equal(t, int64(3), countWhere(t, db, &types.Agent24UITicket{}, "vph = ?", "vph-ext"))
	})
}

func cloneTicket(src *types.Agent24UITicket, edit func(*types.Agent24UITicket)) *types.Agent24UITicket {
	cp := *src
	edit(&cp)
	return &cp
}

func countWhere(t *testing.T, db *gorm.DB, model any, query string, arg any) int64 {
	t.Helper()
	var n int64
	require.NoError(t, db.Model(model).Where(query, arg).Count(&n).Error)
	return n
}

func TestAgent24UISessionConsumeTicket(t *testing.T) {
	db := agent24UISessionDB(t)
	store := NewAgent24UISessionStore(db)
	ctx := context.Background()
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	ticket := newAgent24UITicket(t, "vph-c", "hash-c", "id-c", now.Add(time.Minute))
	require.NoError(t, store.CreateTicket(ctx, ticket))

	row, first, err := store.ConsumeTicket(ctx, "hash-c", now)
	require.NoError(t, err)
	require.True(t, first)
	require.NotNil(t, row)
	require.Equal(t, "id-c", row.TicketID)
	require.NotNil(t, row.ConsumedAt)
	require.WithinDuration(t, now, *row.ConsumedAt, time.Second)
	consumed := *row.ConsumedAt

	row, first, err = store.ConsumeTicket(ctx, "hash-c", now.Add(time.Minute))
	require.NoError(t, err)
	require.False(t, first)
	require.NotNil(t, row)
	require.Equal(t, "id-c", row.TicketID)
	require.NotNil(t, row.ConsumedAt)
	require.WithinDuration(t, consumed, *row.ConsumedAt, time.Second)

	row, first, err = store.ConsumeTicket(ctx, "hash-missing", now)
	require.NoError(t, err)
	require.False(t, first)
	require.Nil(t, row)
}

func TestAgent24UISessionConsumeTicketConcurrent(t *testing.T) {
	dir := t.TempDir()
	dsn := dir + "/ui.db?_busy_timeout=5000&_journal_mode=WAL"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(16)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, EnsureAgent24UISessionTables(db))
	store := NewAgent24UISessionStore(db)
	ctx := context.Background()
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	race := newAgent24UITicket(t, "vph-race", "hash-race", "id-race", now.Add(time.Minute))
	require.NoError(t, store.CreateTicket(ctx, race))

	const n = 16
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(n)
	firsts := make([]bool, n)
	errs := make([]error, n)
	rows := make([]*types.Agent24UITicket, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			<-start
			rows[i], firsts[i], errs[i] = store.ConsumeTicket(ctx, "hash-race", now)
		}(i)
	}
	close(start)
	wg.Wait()

	got := 0
	for i := 0; i < n; i++ {
		require.NoError(t, errs[i])
		require.NotNil(t, rows[i])
		if firsts[i] {
			got++
		}
	}
	require.Equal(t, 1, got)
}

// Two issuances for the same new vph race to register the family. Both must succeed (same owner), and
// a racing issuance from another principal must get ErrAgent24UIFamilyOwner, never a raw key conflict.
func TestAgent24UISessionCreateTicketFamilyRace(t *testing.T) {
	dir := t.TempDir()
	dsn := dir + "/ui.db?_busy_timeout=5000&_journal_mode=WAL"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(16)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, EnsureAgent24UISessionTables(db))
	store := NewAgent24UISessionStore(db)
	ctx := context.Background()
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

	const n = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			hash, id := fmt.Sprintf("hash-%d", i), fmt.Sprintf("id-%d", i)
			tk := newAgent24UITicket(t, "vph-new", hash, id, now.Add(time.Minute))
			<-start
			errs[i] = store.CreateTicket(ctx, tk)
		}(i)
	}
	close(start)
	wg.Wait()
	for i := 0; i < n; i++ {
		require.NoError(t, errs[i], "issuance %d", i)
	}
	require.Equal(t, int64(1), countAgent24UI(t, db, &types.Agent24UIFamily{}))
	require.Equal(t, int64(n), countAgent24UI(t, db, &types.Agent24UITicket{}))

	other := newAgent24UITicket(t, "vph-new", "hash-other", "id-other", now.Add(time.Minute))
	other.Principal = "principal-b"
	require.ErrorIs(t, store.CreateTicket(ctx, other), interfaces.ErrAgent24UIFamilyOwner)
}

// A concurrent issuance can write a later family expiry between this issuance's read and its update. The
// update must not move expires_at back: the hook stands in for that other transaction.
func TestAgent24UISessionFamilyExpiryNeverShrinks(t *testing.T) {
	db := agent24UISessionDB(t)
	store := NewAgent24UISessionStore(db)
	ctx := context.Background()
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	require.NoError(t, store.CreateTicket(ctx, newAgent24UITicket(t, "vph-x", "hash-1", "id-1", now.Add(time.Minute))))

	later := now.Add(2 * time.Hour)
	fired := false
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register("test:agent24_ui_family_race",
		func(tx *gorm.DB) {
			if fired || tx.Statement.Table != "agent24_ui_families" {
				return
			}
			fired = true
			require.NoError(t, tx.Session(&gorm.Session{NewDB: true}).
				Exec("UPDATE agent24_ui_families SET expires_at = ? WHERE vph = ?", later, "vph-x").Error)
		}))
	t.Cleanup(func() { _ = db.Callback().Update().Remove("test:agent24_ui_family_race") })

	// This ticket extends the family to now+2m+15m, earlier than the value the hook writes.
	second := newAgent24UITicket(t, "vph-x", "hash-2", "id-2", now.Add(2*time.Minute))
	require.NoError(t, store.CreateTicket(ctx, second))
	require.True(t, fired, "the family update ran")
	var family types.Agent24UIFamily
	require.NoError(t, db.Where("vph = ?", "vph-x").Take(&family).Error)
	require.True(t, family.ExpiresAt.Equal(later), "expires_at moved back to %s", family.ExpiresAt)
}

// PR #138 G1: between registration's unlocked read of an existing family and its update, a revoke or a
// purge commits. Neither may end with a ticket inserted into that family. The hook stands in for the other
// transaction, as in TestAgent24UISessionFamilyExpiryNeverShrinks.
func TestAgent24UISessionCreateTicketFamilyChangedMidway(t *testing.T) {
	cases := []struct {
		name   string
		change string
		want   error
	}{
		{
			"revoked", "UPDATE agent24_ui_families SET revoked_at = CURRENT_TIMESTAMP WHERE vph = ?",
			interfaces.ErrAgent24UIFamilyRevoked,
		},
		{"purged", "DELETE FROM agent24_ui_families WHERE vph = ?", interfaces.ErrAgent24UIFamilyGone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := agent24UISessionDB(t)
			store := NewAgent24UISessionStore(db)
			ctx := context.Background()
			now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
			first := newAgent24UITicket(t, "vph-m", "hash-1", "id-1", now.Add(time.Minute))
			require.NoError(t, store.CreateTicket(ctx, first))

			fired := false
			name := "test:agent24_ui_family_changed_" + tc.name
			require.NoError(t, db.Callback().Update().Before("gorm:update").Register(name, func(tx *gorm.DB) {
				if fired || tx.Statement.Table != "agent24_ui_families" {
					return
				}
				fired = true
				require.NoError(t, tx.Session(&gorm.Session{NewDB: true}).Exec(tc.change, "vph-m").Error)
			}))
			t.Cleanup(func() { _ = db.Callback().Update().Remove(name) })

			second := newAgent24UITicket(t, "vph-m", "hash-2", "id-2", now.Add(2*time.Minute))
			require.ErrorIs(t, store.CreateTicket(ctx, second), tc.want)
			require.True(t, fired, "the family update ran")
			var n int64
			require.NoError(t, db.Model(&types.Agent24UITicket{}).Where("ticket_hash = ?", "hash-2").Count(&n).Error)
			require.Zero(t, n, "no ticket was inserted")
		})
	}
}

func TestAgent24UISessionRevoke(t *testing.T) {
	db := agent24UISessionDB(t)
	store := NewAgent24UISessionStore(db)
	ctx := context.Background()
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	require.NoError(t, store.CreateTicket(ctx, newAgent24UITicket(t, "vph-r", "hash-r", "id-r", now.Add(time.Minute))))

	require.NoError(t, store.RevokeTicket(ctx, 8, "principal-a", "id-r", now))
	revoked, err := store.TicketRevoked(ctx, "id-r")
	require.NoError(t, err)
	require.False(t, revoked)

	require.NoError(t, store.RevokeTicket(ctx, 7, "principal-b", "id-r", now))
	revoked, err = store.TicketRevoked(ctx, "id-r")
	require.NoError(t, err)
	require.False(t, revoked)

	require.NoError(t, store.RevokeTicket(ctx, 7, "principal-a", "missing-ticket", now))
	missing, err := store.TicketRevoked(ctx, "missing-ticket")
	require.NoError(t, err)
	require.True(t, missing)

	require.NoError(t, store.RevokeTicket(ctx, 7, "principal-a", "id-r", now))
	revoked, err = store.TicketRevoked(ctx, "id-r")
	require.NoError(t, err)
	require.True(t, revoked)
	var stored types.Agent24UITicket
	require.NoError(t, db.Where("ticket_id = ?", "id-r").Take(&stored).Error)
	require.NotNil(t, stored.RevokedAt)
	require.WithinDuration(t, now, *stored.RevokedAt, time.Second)

	require.NoError(t, store.RevokeFamily(ctx, 8, "principal-a", "vph-r", now))
	fam, err := store.FamilyRevoked(ctx, "vph-r")
	require.NoError(t, err)
	require.False(t, fam)

	require.NoError(t, store.RevokeFamily(ctx, 7, "principal-b", "vph-r", now))
	fam, err = store.FamilyRevoked(ctx, "vph-r")
	require.NoError(t, err)
	require.False(t, fam)

	require.NoError(t, store.RevokeFamily(ctx, 7, "principal-a", "missing-vph", now))
	missingFam, err := store.FamilyRevoked(ctx, "missing-vph")
	require.NoError(t, err)
	require.True(t, missingFam)

	require.NoError(t, store.RevokeFamily(ctx, 7, "principal-a", "vph-r", now))
	fam, err = store.FamilyRevoked(ctx, "vph-r")
	require.NoError(t, err)
	require.True(t, fam)
}

func TestAgent24UISessionRestartPersists(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/ui.db"
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	require.NoError(t, EnsureAgent24UISessionTables(db))

	store := NewAgent24UISessionStore(db)
	ctx := context.Background()
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	disk := newAgent24UITicket(t, "vph-disk", "hash-disk", "id-disk", now.Add(time.Minute))
	require.NoError(t, store.CreateTicket(ctx, disk))
	_, first, err := store.ConsumeTicket(ctx, "hash-disk", now)
	require.NoError(t, err)
	require.True(t, first)
	require.NoError(t, store.RevokeTicket(ctx, 7, "principal-a", "id-disk", now))
	require.NoError(t, store.RevokeFamily(ctx, 7, "principal-a", "vph-disk", now))
	require.NoError(t, sqlDB.Close())

	db2, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	require.NoError(t, err)
	sqlDB2, err := db2.DB()
	require.NoError(t, err)
	sqlDB2.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB2.Close() })
	store2 := NewAgent24UISessionStore(db2)

	row, first, err := store2.ConsumeTicket(ctx, "hash-disk", now.Add(time.Second))
	require.NoError(t, err)
	require.False(t, first)
	require.NotNil(t, row)
	require.NotNil(t, row.ConsumedAt)
	revoked, err := store2.TicketRevoked(ctx, "id-disk")
	require.NoError(t, err)
	require.True(t, revoked)
	fam, err := store2.FamilyRevoked(ctx, "vph-disk")
	require.NoError(t, err)
	require.True(t, fam)
}

func TestAgent24UISessionPurgeExpired(t *testing.T) {
	db := agent24UISessionDB(t)
	store := NewAgent24UISessionStore(db)
	ctx := context.Background()
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	require.NoError(t, store.CreateTicket(ctx,
		newAgent24UITicket(t, "vph-old", "hash-old", "id-old", now.Add(-16*time.Minute))))
	require.NoError(t, store.CreateTicket(ctx,
		newAgent24UITicket(t, "vph-keep", "hash-keep", "id-keep", now.Add(-14*time.Minute))))
	require.NoError(t, db.Create(&types.Agent24UIFamily{
		VPH: "vph-due", TenantID: 7, Principal: "principal-a", ExpiresAt: now, CreatedAt: now,
	}).Error)
	require.NoError(t, db.Create(&types.Agent24UIFamily{
		VPH: "vph-live", TenantID: 7, Principal: "principal-a", ExpiresAt: now.Add(time.Minute), CreatedAt: now,
	}).Error)

	n, err := store.PurgeExpired(ctx, now)
	require.NoError(t, err)
	// The 16-minute ticket, its family (expires one minute ago), and vph-due.
	require.Equal(t, int64(3), n)

	var tickets []types.Agent24UITicket
	require.NoError(t, db.Find(&tickets).Error)
	require.Len(t, tickets, 1)
	require.Equal(t, "hash-keep", tickets[0].TicketHash)

	var families []types.Agent24UIFamily
	require.NoError(t, db.Order("vph").Find(&families).Error)
	require.Len(t, families, 2)
	require.Equal(t, "vph-keep", families[0].VPH)
	require.Equal(t, "vph-live", families[1].VPH)
}
