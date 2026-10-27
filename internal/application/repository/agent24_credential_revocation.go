// Agent24 fork product patch A24-KB-22 (KB-C1-03 C3-b2, WeKnora#41, CONTRACT-READONLY-v1 R1.6).

package repository

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type agent24CredentialRevocationStore struct {
	db *gorm.DB
}

// NewAgent24CredentialRevocationStore stores revoked signed_token jti values.
func NewAgent24CredentialRevocationStore(db *gorm.DB) interfaces.Agent24CredentialRevocationStore {
	return &agent24CredentialRevocationStore{db: db}
}

// agent24ValidCredentialJTI applies the same rule middleware.Auth uses when it reads a jti.
// The value is trimmed, then refused when empty, longer than 128 runes, or containing a control character.
// middleware is not imported: this package must not depend on the HTTP stack.
func agent24ValidCredentialJTI(raw string) (string, error) {
	jti := strings.TrimSpace(raw)
	if jti == "" || utf8.RuneCountInString(jti) > 128 {
		return "", errors.New("jti is invalid")
	}
	for _, r := range jti {
		if r < 0x20 || r == 0x7f {
			return "", errors.New("jti is invalid")
		}
	}
	return jti, nil
}

func (s *agent24CredentialRevocationStore) Revoke(
	ctx context.Context, tenantID uint64, jti string, expiresAt time.Time,
) error {
	if tenantID == 0 {
		return errors.New("tenant_id is required")
	}
	jti, err := agent24ValidCredentialJTI(jti)
	if err != nil {
		return err
	}
	row := &types.Agent24CredentialRevocation{
		TenantID:  tenantID,
		JTI:       jti,
		ExpiresAt: expiresAt.UTC(),
	}
	return s.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "tenant_id"}, {Name: "jti"}},
		DoNothing: true,
	}).Create(row).Error
}

func (s *agent24CredentialRevocationStore) IsRevoked(
	ctx context.Context, tenantID uint64, jti string, now time.Time,
) (bool, error) {
	var n int64
	err := s.db.WithContext(ctx).Model(&types.Agent24CredentialRevocation{}).
		Where("tenant_id = ? AND jti = ? AND expires_at > ?", tenantID, jti, now.UTC()).
		Count(&n).Error
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *agent24CredentialRevocationStore) PurgeExpired(ctx context.Context, before time.Time) (int64, error) {
	res := s.db.WithContext(ctx).
		Where("expires_at <= ?", before.UTC()).
		Delete(&types.Agent24CredentialRevocation{})
	return res.RowsAffected, res.Error
}

// agent24CredentialRevocationIndexes are the indexes the table relies on: ON CONFLICT (tenant_id, jti) needs
// the unique one, and the purge reads by expires_at.
var agent24CredentialRevocationIndexes = []string{
	"idx_agent24_cred_rev_tenant_jti",
	"idx_agent24_cred_rev_expires_at",
}

// EnsureAgent24CredentialRevocationTable creates agent24_credential_revocations and its indexes when they are
// missing. AutoMigrate is not used: it would alter columns that already exist. CreateTable has no IF NOT EXISTS,
// so a concurrent cold start can lose the race to another replica that created the table first; a second
// HasTable treats that as success. CreateTable also creates the indexes in separate statements after the table,
// so a table can exist without them (an index statement failed, or the process stopped in between); every
// start therefore checks each index and creates the missing ones, with the same re-check after a failure.
func EnsureAgent24CredentialRevocationTable(db *gorm.DB) error {
	m := db.Migrator()
	if !m.HasTable(&types.Agent24CredentialRevocation{}) {
		if err := m.CreateTable(&types.Agent24CredentialRevocation{}); err != nil &&
			!m.HasTable(&types.Agent24CredentialRevocation{}) {
			return err
		}
	}
	for _, name := range agent24CredentialRevocationIndexes {
		if m.HasIndex(&types.Agent24CredentialRevocation{}, name) {
			continue
		}
		if err := m.CreateIndex(&types.Agent24CredentialRevocation{}, name); err != nil &&
			!m.HasIndex(&types.Agent24CredentialRevocation{}, name) {
			return err
		}
	}
	return nil
}
