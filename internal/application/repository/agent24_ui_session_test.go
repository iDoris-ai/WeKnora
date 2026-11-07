// Agent24 fork product patch A24-KB-31 (KB-C2-01a, WeKnora#133, CONTRACT-UI-BRIDGE-v1 U2–U3).

package repository

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
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

// A24-KB-33: GetTicketByID returns the row, including the key id and principal mode, or (nil, nil).
func TestAgent24UISessionGetTicketByID(t *testing.T) {
	db := agent24UISessionDB(t)
	store := NewAgent24UISessionStore(db)
	ctx := context.Background()
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	ticket := newAgent24UITicket(t, "vph-get", "hash-get", "id-get", now.Add(time.Minute))
	ticket.KeyID = 42
	ticket.PrincipalMode = "signed_token"
	require.NoError(t, store.CreateTicket(ctx, ticket))

	got, err := store.GetTicketByID(ctx, "id-get")
	require.NoError(t, err)
	require.NotNil(t, got)
	require.Equal(t, uint64(42), got.KeyID)
	require.Equal(t, "signed_token", got.PrincipalMode)
	require.Equal(t, "hash-get", got.TicketHash)

	var stored types.Agent24UITicket
	require.NoError(t, db.Where("ticket_id = ?", "id-get").Take(&stored).Error)
	require.Equal(t, uint64(42), stored.KeyID)
	require.Equal(t, "signed_token", stored.PrincipalMode)

	missing, err := store.GetTicketByID(ctx, "missing")
	require.NoError(t, err)
	require.Nil(t, missing)
}

// A24-KB-33: a read that fails after the spend rolls the spend back. The next presentation is then a
// first presentation, not a replay that the caller never had the row to judge.
func TestAgent24UISessionConsumeTicketReadFailureRollsBack(t *testing.T) {
	db := agent24UISessionDB(t)
	store := NewAgent24UISessionStore(db)
	ctx := context.Background()
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	require.NoError(t, store.CreateTicket(ctx, newAgent24UITicket(t, "vph-r", "hash-r", "id-r", now.Add(time.Minute))))

	const name = "agent24_test_fail_ticket_read"
	require.NoError(t, db.Callback().Query().Before("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement.Table == "agent24_ui_tickets" {
			_ = tx.AddError(fmt.Errorf("read down"))
		}
	}))
	_, _, err := store.ConsumeTicket(ctx, "hash-r", now)
	require.Error(t, err)
	require.NoError(t, db.Callback().Query().Remove(name))

	spent := countWhere(t, db, &types.Agent24UITicket{}, "consumed_at IS NOT NULL AND ticket_hash = ?", "hash-r")
	require.Equal(t, int64(0), spent)
	row, first, err := store.ConsumeTicket(ctx, "hash-r", now)
	require.NoError(t, err)
	require.True(t, first)
	require.NotNil(t, row)
}

const agent24RevokedVPH = "revoked_at IS NOT NULL AND vph = ?"

// agent24FamilyWriteSwitch registers one update callback on db for the families table. It never changes
// afterwards, so background retries cannot race a registration: failing makes the write fail, and a
// non-nil hold blocks the write until it is closed. writes counts the writes that reached it.
type agent24FamilyWriteSwitch struct {
	failing atomic.Bool
	writes  atomic.Int32
	mu      sync.Mutex
	hold    chan struct{}
}

func newAgent24FamilyWriteSwitch(t *testing.T, db *gorm.DB) *agent24FamilyWriteSwitch {
	t.Helper()
	sw := &agent24FamilyWriteSwitch{}
	require.NoError(t, db.Callback().Update().Before("gorm:update").Register("agent24_test_family_write",
		func(tx *gorm.DB) {
			if tx.Statement.Table != "agent24_ui_families" {
				return
			}
			sw.writes.Add(1)
			sw.mu.Lock()
			hold := sw.hold
			sw.mu.Unlock()
			if hold != nil {
				<-hold
			}
			if sw.failing.Load() {
				_ = tx.AddError(fmt.Errorf("write down"))
			}
		}))
	return sw
}

func (sw *agent24FamilyWriteSwitch) holdWrites() chan struct{} {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	sw.hold = make(chan struct{})
	return sw.hold
}

// A24-KB-33: a leak revocation whose write fails is still in force (U2.8): FamilyRevoked is true and
// CreateTicket refuses the family, and once the database recovers a background retry writes the row.
func TestAgent24UISessionLeakRevokeWriteFailureStaysRevoked(t *testing.T) {
	db := agent24UISessionDB(t)
	store := NewAgent24UISessionStore(db)
	ctx := context.Background()
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	ticket := newAgent24UITicket(t, "vph-p", "hash-p", "id-p", now.Add(time.Minute))
	require.NoError(t, store.CreateTicket(ctx, ticket))
	revoked, err := store.FamilyRevoked(ctx, "vph-p")
	require.NoError(t, err)
	require.False(t, revoked)

	sw := newAgent24FamilyWriteSwitch(t, db)
	sw.failing.Store(true)
	require.Error(t, store.RevokeLeakedFamily(ctx, ticket.TenantID, ticket.Principal, "vph-p", now))
	revoked, err = store.FamilyRevoked(ctx, "vph-p")
	require.NoError(t, err)
	require.True(t, revoked, "a failed revocation must still hold")
	next := newAgent24UITicket(t, "vph-p", "hash-p2", "id-p2", now.Add(time.Minute))
	require.ErrorIs(t, store.CreateTicket(ctx, next), interfaces.ErrAgent24UIFamilyRevoked)
	require.Equal(t, int64(0), countWhere(t, db, &types.Agent24UIFamily{}, agent24RevokedVPH, "vph-p"))

	sw.failing.Store(false)
	require.Eventually(t, func() bool {
		revoked, err := store.FamilyRevoked(ctx, "vph-p")
		return err == nil && revoked &&
			countWhere(t, db, &types.Agent24UIFamily{}, agent24RevokedVPH, "vph-p") == 1
	}, 5*time.Second, 10*time.Millisecond)
	// The row now carries the revocation on its own: a fresh store over the same database sees it.
	revoked, err = NewAgent24UISessionStore(db).FamilyRevoked(ctx, "vph-p")
	require.NoError(t, err)
	require.True(t, revoked)
}

// A24-KB-33: the host revocation path (RevokeFamily) by another owner neither clears nor replaces a
// pending leak revocation, and has no effect on the family (U3.7).
func TestAgent24UISessionOtherOwnerDoesNotTouchPendingRevoke(t *testing.T) {
	db := agent24UISessionDB(t)
	store := NewAgent24UISessionStore(db)
	ctx := context.Background()
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	ticket := newAgent24UITicket(t, "vph-o", "hash-o", "id-o", now.Add(time.Minute))
	require.NoError(t, store.CreateTicket(ctx, ticket))

	sw := newAgent24FamilyWriteSwitch(t, db)
	sw.failing.Store(true)
	require.Error(t, store.RevokeLeakedFamily(ctx, ticket.TenantID, ticket.Principal, "vph-o", now))
	// Another owner's revocation fails while the database is down, then succeeds as a no-op after it
	// recovers. Neither may change the pending entry.
	require.Error(t, store.RevokeFamily(ctx, 99, "principal-b", "vph-o", now))
	sw.failing.Store(false)
	require.NoError(t, store.RevokeFamily(ctx, 99, "principal-b", "vph-o", now))
	require.Equal(t, int64(0), countWhere(t, db, &types.Agent24UIFamily{}, agent24RevokedVPH, "vph-o"))

	revoked, err := store.FamilyRevoked(ctx, "vph-o")
	require.NoError(t, err)
	require.True(t, revoked, "another owner's revocation cleared the pending leak revocation")
	require.Eventually(t, func() bool {
		var row types.Agent24UIFamily
		if db.Where("vph = ?", "vph-o").Take(&row).Error != nil || row.RevokedAt == nil {
			return false
		}
		return row.TenantID == ticket.TenantID && row.Principal == ticket.Principal
	}, 5*time.Second, 10*time.Millisecond)
}

// A24-KB-33: FamilyRevoked does not wait on the retry write, and one family has at most one retry in flight.
func TestAgent24UISessionPendingRetryDoesNotBlock(t *testing.T) {
	db := agent24UISessionDB(t)
	store := NewAgent24UISessionStore(db)
	ctx := context.Background()
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	ticket := newAgent24UITicket(t, "vph-b", "hash-b", "id-b", now.Add(time.Minute))
	require.NoError(t, store.CreateTicket(ctx, ticket))
	sw := newAgent24FamilyWriteSwitch(t, db)
	sw.failing.Store(true)
	require.Error(t, store.RevokeLeakedFamily(ctx, ticket.TenantID, ticket.Principal, "vph-b", now))
	sw.failing.Store(false)
	sw.writes.Store(0)
	release := sw.holdWrites()

	for i := 0; i < 5; i++ {
		start := time.Now()
		revoked, err := store.FamilyRevoked(ctx, "vph-b")
		require.NoError(t, err)
		require.True(t, revoked)
		require.Less(t, time.Since(start), time.Second, "FamilyRevoked waited on the retry write")
	}
	require.Eventually(t, func() bool { return sw.writes.Load() == 1 }, 2*time.Second, 5*time.Millisecond)
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, int32(1), sw.writes.Load(), "more than one retry in flight")
	close(release)
	require.Eventually(t, func() bool {
		return countWhere(t, db, &types.Agent24UIFamily{}, agent24RevokedVPH, "vph-b") == 1
	}, 5*time.Second, 10*time.Millisecond)
}

// A24-KB-33: RevokeLeakedFamily succeeds only once the row is confirmed revoked. A write that matched no
// row of an existing family (the owner does not match) is an error, and the family stays pending.
func TestAgent24UISessionLeakRevokeConfirmsRow(t *testing.T) {
	db := agent24UISessionDB(t)
	store := NewAgent24UISessionStore(db)
	ctx := context.Background()
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	ticket := newAgent24UITicket(t, "vph-c2", "hash-c2", "id-c2", now.Add(time.Minute))
	require.NoError(t, store.CreateTicket(ctx, ticket))

	require.Error(t, store.RevokeLeakedFamily(ctx, 99, "principal-b", "vph-c2", now))
	require.Equal(t, int64(0), countWhere(t, db, &types.Agent24UIFamily{}, agent24RevokedVPH, "vph-c2"))
	revoked, err := store.FamilyRevoked(ctx, "vph-c2")
	require.NoError(t, err)
	require.True(t, revoked)

	// A family that no longer exists is done: FamilyRevoked treats a missing row as revoked.
	require.NoError(t, store.RevokeLeakedFamily(ctx, 7, "principal-a", "vph-missing", now))
	require.NoError(t, store.RevokeLeakedFamily(ctx, ticket.TenantID, ticket.Principal, "vph-c2", now))
	require.Equal(t, int64(1), countWhere(t, db, &types.Agent24UIFamily{}, agent24RevokedVPH, "vph-c2"))
}

// A24-KB-33: at most one background retry per family is in flight.
func TestAgent24UISessionPendingRetrySingleFlight(t *testing.T) {
	store := NewAgent24UISessionStore(agent24UISessionDB(t)).(*agent24UISessionStore)
	_, ok := store.startPendingRetry("vph-s")
	require.False(t, ok, "nothing pending")
	store.addPendingRevoke("vph-s", agent24UIPendingRevoke{tenantID: 7, principal: "principal-a"})
	_, ok = store.startPendingRetry("vph-s")
	require.True(t, ok)
	_, ok = store.startPendingRetry("vph-s")
	require.False(t, ok, "a second retry started while one is in flight")
	store.endPendingRetry("vph-s")
	_, ok = store.startPendingRetry("vph-s")
	require.True(t, ok)
	// A later failure does not replace the first owner.
	store.addPendingRevoke("vph-s", agent24UIPendingRevoke{tenantID: 99, principal: "principal-b"})
	p, _ := store.pendingRevoke("vph-s")
	require.Equal(t, uint64(7), p.tenantID)
}
