// Agent24 fork product patch A24-KB-51 (KB-C4-02b, CONTRACT-OPS-v2 W4.1–W4.6): the record of an owner's default
// knowledge base.
//
// One row per owner (tenant, principal StorageID); the primary key is the server-side constraint that keeps an
// owner at one default KB (W4.2), whatever order concurrent requests arrive in. The row is pending while the KB
// is being created and ready once it exists. Claim changes with every request that takes the row over, so a
// request that lost the row to another (after its lease ran out) can no longer finish or remove it.

package types

import "time"

// The states of an Agent24DefaultKB row.
const (
	Agent24DefaultKBPending = "pending"
	Agent24DefaultKBReady   = "ready"
)

// Agent24DefaultKB is one row of agent24_default_kbs.
type Agent24DefaultKB struct {
	TenantID        uint64    `gorm:"primaryKey;autoIncrement:false"`
	Owner           string    `gorm:"type:varchar(255);primaryKey"`
	KnowledgeBaseID string    `gorm:"type:varchar(36);not null"`
	State           string    `gorm:"type:varchar(16);not null"`
	KeyHash         string    `gorm:"type:varchar(64);not null"` // SHA-256 of the idempotency key, hex
	Claim           string    `gorm:"type:varchar(36);not null"`
	ConfigVersion   string    `gorm:"type:varchar(128);not null;default:''"`
	LeaseUntil      time.Time `gorm:"not null"`
	CreatedAt       time.Time `gorm:"not null"`
	UpdatedAt       time.Time `gorm:"not null"`
}

// TableName pins the fork-owned table name.
func (Agent24DefaultKB) TableName() string { return "agent24_default_kbs" }
