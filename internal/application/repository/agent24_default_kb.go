// Agent24 fork product patch A24-KB-51 (KB-C4-02b, CONTRACT-OPS-v2 W4.2, W4.5).

package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Tencent/WeKnora/internal/types"
)

// Agent24DefaultKBStore reads and writes agent24_default_kbs. Every write after the first insert is conditional on
// the row's claim, so only the request holding the row can move it.
type Agent24DefaultKBStore struct {
	db *gorm.DB
}

// NewAgent24DefaultKBStore returns the store over db.
func NewAgent24DefaultKBStore(db *gorm.DB) *Agent24DefaultKBStore {
	return &Agent24DefaultKBStore{db: db}
}

// EnsureAgent24DefaultKBTable creates agent24_default_kbs when it is missing, the same way as
// EnsureAgent24IngestJobTable (no AutoMigrate, no numbered migration, re-check after a lost race). The primary
// key (tenant_id, owner) is the only index the table needs.
func EnsureAgent24DefaultKBTable(db *gorm.DB) error {
	m := db.Migrator()
	if m.HasTable(&types.Agent24DefaultKB{}) {
		return nil
	}
	if err := m.CreateTable(&types.Agent24DefaultKB{}); err != nil && !m.HasTable(&types.Agent24DefaultKB{}) {
		return err
	}
	return nil
}

// Insert adds row unless the owner already has one, and reports whether it did.
func (s *Agent24DefaultKBStore) Insert(ctx context.Context, row *types.Agent24DefaultKB) (bool, error) {
	res := s.db.WithContext(ctx).Clauses(clause.OnConflict{DoNothing: true}).Create(row)
	return res.RowsAffected == 1, res.Error
}

// Get returns the owner's row, or nil when there is none.
func (s *Agent24DefaultKBStore) Get(
	ctx context.Context, tenantID uint64, owner string,
) (*types.Agent24DefaultKB, error) {
	var row types.Agent24DefaultKB
	err := s.db.WithContext(ctx).Where("tenant_id = ? AND owner = ?", tenantID, owner).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

// TakeOver replaces a pending row still held under old.Claim with next (same owner), and reports whether it did.
func (s *Agent24DefaultKBStore) TakeOver(
	ctx context.Context, old types.Agent24DefaultKB, next types.Agent24DefaultKB,
) (bool, error) {
	res := s.db.WithContext(ctx).Model(&types.Agent24DefaultKB{}).
		Where("tenant_id = ? AND owner = ? AND state = ? AND claim = ?",
			old.TenantID, old.Owner, types.Agent24DefaultKBPending, old.Claim).
		Updates(map[string]any{
			"knowledge_base_id": next.KnowledgeBaseID, "key_hash": next.KeyHash, "claim": next.Claim,
			"config_version": next.ConfigVersion, "lease_until": next.LeaseUntil, "updated_at": next.UpdatedAt,
		})
	return res.RowsAffected == 1, res.Error
}

// MarkReady turns the pending row held under claim ready, and reports whether it did.
func (s *Agent24DefaultKBStore) MarkReady(
	ctx context.Context, tenantID uint64, owner, claim string, now time.Time,
) (bool, error) {
	res := s.db.WithContext(ctx).Model(&types.Agent24DefaultKB{}).
		Where("tenant_id = ? AND owner = ? AND state = ? AND claim = ?",
			tenantID, owner, types.Agent24DefaultKBPending, claim).
		Updates(map[string]any{"state": types.Agent24DefaultKBReady, "updated_at": now.UTC()})
	return res.RowsAffected == 1, res.Error
}

// Release deletes the pending row held under claim, so the owner can start again.
func (s *Agent24DefaultKBStore) Release(ctx context.Context, tenantID uint64, owner, claim string) error {
	return s.db.WithContext(ctx).
		Where("tenant_id = ? AND owner = ? AND state = ? AND claim = ?",
			tenantID, owner, types.Agent24DefaultKBPending, claim).
		Delete(&types.Agent24DefaultKB{}).Error
}
