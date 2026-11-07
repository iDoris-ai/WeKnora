// Agent24 fork product patch A24-KB-31 (KB-C2-01a, WeKnora#133, CONTRACT-UI-BRIDGE-v1 U2–U3).

package types

import (
	"encoding/json"
	"errors"
	"time"
)

// Agent24UISessionMaxLifetime is the longest session JWT lifetime (U3.3). Store and JWT code share it.
// A ticket is purged this long after it expires, because a JWT taken for it lives at most this long.
const Agent24UISessionMaxLifetime = 15 * time.Minute

// errAgent24UIScopeEmpty is an empty knowledge-base scope (R2.4).
var errAgent24UIScopeEmpty = errors.New("scope knowledge base ids are empty")

// Agent24UITicket is one UI exchange ticket. The raw ticket is not stored (U2.2–U2.4).
type Agent24UITicket struct {
	// TicketHash is the lowercase hex SHA-256 of the raw ticket bytes.
	TicketHash string `gorm:"column:ticket_hash;type:char(64);primaryKey"`
	// TicketID is the public id used for revocation. It cannot be exchanged.
	TicketID     string `gorm:"column:ticket_id;type:varchar(64);not null;uniqueIndex:idx_agent24_ui_ticket_id"` //nolint:lll // one-line struct tag
	TenantID     uint64 `gorm:"not null"`
	Principal    string `gorm:"type:varchar(256);not null"`
	CredentialID string `gorm:"type:varchar(128);not null"`
	// A24-KB-33: exchange and the session probe recheck C by this key id. Those requests do not carry C.
	// 0 is not a key. The table is created at integration startup; there is no deployed row to migrate.
	KeyID uint64 `gorm:"not null;default:0"`
	// A24-KB-33: principal mode at issuance. A later change of the tenant's mode refuses the ticket.
	PrincipalMode string `gorm:"type:varchar(32);not null;default:''"`
	// A24-KB-33: H("kb-principal-secret-v1"; UI session key; tenant HMAC secret) at issuance. A signed token
	// cannot be verified again on exchange or a UI request, so a rotated secret refuses the ticket instead.
	// Keyed by the UI session key so the stored value does not allow guessing a weak secret offline.
	PrincipalSecretTag string `gorm:"type:varchar(64);not null;default:''"`
	// A24-KB-33: the issuing process (agent24uisession.BootID). Exchange refuses a ticket from another
	// process: a leak revocation that failed to persist lived only there (U2.3).
	BootID              string     `gorm:"type:varchar(32);not null;default:''"`
	CredentialExpiresAt *time.Time // nil means the parent credential does not expire
	ScopeKBIDs          string     `gorm:"column:scope_kb_ids;type:text;not null"` // JSON array, non-empty
	BindingTag          string     `gorm:"type:varchar(64);not null"`
	PartitionTag        string     `gorm:"type:varchar(64);not null"`
	VPH                 string     `gorm:"column:vph;type:varchar(64);not null;index:idx_agent24_ui_ticket_vph"`
	Generation          int64      `gorm:"not null"`
	ExpiresAt           time.Time  `gorm:"not null;index:idx_agent24_ui_ticket_expires_at"`
	ConsumedAt          *time.Time
	RevokedAt           *time.Time
	CreatedAt           time.Time
}

// TableName pins the fork-owned table name.
func (Agent24UITicket) TableName() string { return "agent24_ui_tickets" }

// ScopeKnowledgeBaseIDs parses ScopeKBIDs. An empty array or invalid JSON is an error (R2.4).
func (t Agent24UITicket) ScopeKnowledgeBaseIDs() ([]string, error) {
	var ids []string
	if err := json.Unmarshal([]byte(t.ScopeKBIDs), &ids); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, errAgent24UIScopeEmpty
	}
	return ids, nil
}

// SetScopeKnowledgeBaseIDs stores ids as a JSON array. An empty slice is an error (R2.4).
func (t *Agent24UITicket) SetScopeKnowledgeBaseIDs(ids []string) error {
	if len(ids) == 0 {
		return errAgent24UIScopeEmpty
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return err
	}
	t.ScopeKBIDs = string(raw)
	return nil
}

// Agent24UIFamily is one view family, identified by vph (U2.3, U3.7).
type Agent24UIFamily struct {
	VPH       string `gorm:"column:vph;type:varchar(64);primaryKey"`
	TenantID  uint64 `gorm:"not null"`
	Principal string `gorm:"type:varchar(256);not null"`
	RevokedAt *time.Time
	ExpiresAt time.Time `gorm:"not null;index:idx_agent24_ui_family_expires_at"`
	CreatedAt time.Time
}

// TableName pins the fork-owned table name.
func (Agent24UIFamily) TableName() string { return "agent24_ui_families" }
