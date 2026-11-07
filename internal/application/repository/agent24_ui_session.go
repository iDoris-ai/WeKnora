// Agent24 fork product patch A24-KB-31 (KB-C2-01a, WeKnora#133, CONTRACT-UI-BRIDGE-v1 U2–U3).

package repository

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type agent24UISessionStore struct {
	db *gorm.DB
}

// NewAgent24UISessionStore stores UI tickets and view families.
func NewAgent24UISessionStore(db *gorm.DB) interfaces.Agent24UISessionStore {
	return &agent24UISessionStore{db: db}
}

func agent24UTCPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func (s *agent24UISessionStore) CreateTicket(ctx context.Context, t *types.Agent24UITicket) error {
	if t == nil || t.TicketHash == "" || t.TicketID == "" || t.TenantID == 0 || t.Principal == "" || t.VPH == "" {
		return errors.New("agent24 ui ticket is missing required fields")
	}
	if _, err := t.ScopeKnowledgeBaseIDs(); err != nil {
		return err
	}
	row := *t
	row.ExpiresAt = row.ExpiresAt.UTC()
	if row.CreatedAt.IsZero() {
		row.CreatedAt = time.Now().UTC()
	} else {
		row.CreatedAt = row.CreatedAt.UTC()
	}
	row.CredentialExpiresAt = agent24UTCPtr(row.CredentialExpiresAt)
	row.ConsumedAt = agent24UTCPtr(row.ConsumedAt)
	row.RevokedAt = agent24UTCPtr(row.RevokedAt)

	return s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		extended := row.ExpiresAt.Add(types.Agent24UISessionMaxLifetime)
		// Register the family. Two issuances for a new vph can race: ON CONFLICT DO NOTHING lets the later one
		// fall through to the read below and the same ownership and revocation checks.
		created := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "vph"}}, DoNothing: true}).
			Create(&types.Agent24UIFamily{
				VPH:       row.VPH,
				TenantID:  row.TenantID,
				Principal: row.Principal,
				ExpiresAt: extended,
				CreatedAt: time.Now().UTC(),
			})
		if created.Error != nil {
			return created.Error
		}
		if created.RowsAffected == 0 {
			var family types.Agent24UIFamily
			if err := tx.Where("vph = ?", row.VPH).Take(&family).Error; err != nil {
				return err
			}
			switch {
			case family.TenantID != row.TenantID || family.Principal != row.Principal:
				return interfaces.ErrAgent24UIFamilyOwner
			case family.RevokedAt != nil:
				return interfaces.ErrAgent24UIFamilyRevoked
			case extended.After(family.ExpiresAt):
				// expires_at only moves later. The guard is in the WHERE so the database decides it on the
				// current row: a concurrent issuance that read the same old value and wrote a later expiry first
				// is not overwritten with this earlier one (which would let PurgeExpired drop a revoked family
				// while one of its session JWTs is still valid).
				err := tx.Model(&types.Agent24UIFamily{}).
					Where("vph = ? AND tenant_id = ? AND principal = ? AND revoked_at IS NULL AND expires_at < ?",
						row.VPH, row.TenantID, row.Principal, extended).
					Update("expires_at", extended).Error
				if err != nil {
					return err
				}
			}
		}
		return tx.Create(&row).Error
	})
}

func (s *agent24UISessionStore) ConsumeTicket(
	ctx context.Context, ticketHash string, now time.Time,
) (*types.Agent24UITicket, bool, error) {
	// U2.6: spend the ticket before any expiry or revocation decision. At most one caller updates the row.
	// A24-KB-33: the update and the read are one transaction. A read that fails rolls the spend back, so a
	// ticket is never consumed without the caller seeing the row it needs for the U2.8 leak decision.
	var row types.Agent24UITicket
	var first, found bool
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Exec(
			"UPDATE agent24_ui_tickets SET consumed_at = ? WHERE ticket_hash = ? AND consumed_at IS NULL",
			now.UTC(), ticketHash,
		)
		if res.Error != nil {
			return res.Error
		}
		err := tx.Where("ticket_hash = ?", ticketHash).Take(&row).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		found, first = true, res.RowsAffected == 1
		return nil
	})
	if err != nil || !found {
		return nil, false, err
	}
	return &row, first, nil
}

func (s *agent24UISessionStore) RevokeTicket(
	ctx context.Context, tenantID uint64, principal, ticketID string, now time.Time,
) error {
	res := s.db.WithContext(ctx).Model(&types.Agent24UITicket{}).
		Where("ticket_id = ? AND tenant_id = ? AND principal = ? AND revoked_at IS NULL",
			ticketID, tenantID, principal).
		Update("revoked_at", now.UTC())
	return res.Error
}

func (s *agent24UISessionStore) RevokeFamily(
	ctx context.Context, tenantID uint64, principal, vph string, now time.Time,
) error {
	res := s.db.WithContext(ctx).Model(&types.Agent24UIFamily{}).
		Where("vph = ? AND tenant_id = ? AND principal = ? AND revoked_at IS NULL",
			vph, tenantID, principal).
		Update("revoked_at", now.UTC())
	return res.Error
}

// A24-KB-33: the session probe needs the row. A missing ticket is (nil, nil).
func (s *agent24UISessionStore) GetTicketByID(
	ctx context.Context, ticketID string,
) (*types.Agent24UITicket, error) {
	var row types.Agent24UITicket
	err := s.db.WithContext(ctx).Where("ticket_id = ?", ticketID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}

func (s *agent24UISessionStore) TicketRevoked(ctx context.Context, ticketID string) (bool, error) {
	var row types.Agent24UITicket
	err := s.db.WithContext(ctx).Where("ticket_id = ?", ticketID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return row.RevokedAt != nil, nil
}

func (s *agent24UISessionStore) FamilyRevoked(ctx context.Context, vph string) (bool, error) {
	var row types.Agent24UIFamily
	err := s.db.WithContext(ctx).Where("vph = ?", vph).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return row.RevokedAt != nil, nil
}

func (s *agent24UISessionStore) PurgeExpired(ctx context.Context, now time.Time) (int64, error) {
	now = now.UTC()
	// A JWT taken for a ticket lives at most Agent24UISessionMaxLifetime after the ticket's expires_at.
	tickets := s.db.WithContext(ctx).
		Where("expires_at <= ?", now.Add(-types.Agent24UISessionMaxLifetime)).
		Delete(&types.Agent24UITicket{})
	if tickets.Error != nil {
		return 0, tickets.Error
	}
	families := s.db.WithContext(ctx).Where("expires_at <= ?", now).Delete(&types.Agent24UIFamily{})
	return tickets.RowsAffected + families.RowsAffected, families.Error
}

// agent24UITicketIndexes are the indexes agent24_ui_tickets relies on: ticket_id is unique, and vph and
// expires_at are read by lookup and purge.
var agent24UITicketIndexes = []string{
	"idx_agent24_ui_ticket_id",
	"idx_agent24_ui_ticket_vph",
	"idx_agent24_ui_ticket_expires_at",
}

// agent24UIFamilyIndexes are the indexes agent24_ui_families relies on. Purge reads by expires_at.
var agent24UIFamilyIndexes = []string{
	"idx_agent24_ui_family_expires_at",
}

// EnsureAgent24UISessionTables creates agent24_ui_tickets, agent24_ui_families and their indexes when they
// are missing. AutoMigrate is not used: it would alter columns that already exist. CreateTable has no
// IF NOT EXISTS, so a concurrent cold start can lose the race to another replica that created the table
// first; a second HasTable treats that as success. CreateTable also creates the indexes in separate
// statements after the table, so a table can exist without them (an index statement failed, or the process
// stopped in between); every start therefore checks each index and creates the missing ones, with the same
// re-check after a failure.
func EnsureAgent24UISessionTables(db *gorm.DB) error {
	if err := ensureAgent24UISessionTable(db, &types.Agent24UITicket{}, agent24UITicketIndexes); err != nil {
		return err
	}
	return ensureAgent24UISessionTable(db, &types.Agent24UIFamily{}, agent24UIFamilyIndexes)
}

func ensureAgent24UISessionTable(db *gorm.DB, model any, indexes []string) error {
	m := db.Migrator()
	if !m.HasTable(model) {
		if err := m.CreateTable(model); err != nil && !m.HasTable(model) {
			return err
		}
	}
	for _, name := range indexes {
		if m.HasIndex(model, name) {
			continue
		}
		if err := m.CreateIndex(model, name); err != nil && !m.HasIndex(model, name) {
			return err
		}
	}
	return nil
}
