// Agent24 fork product patch A24-KB-20 (KB-C1-03 C3-b2, WeKnora#41, CONTRACT-READONLY-v1 R1.6).

package types

import "time"

// Agent24CredentialRevocation is one revoked signed_token jti. The row lives until the token would have expired;
// a token past its exp is refused as expired anyway.
type Agent24CredentialRevocation struct {
	ID        uint64    `gorm:"primaryKey;autoIncrement"`
	TenantID  uint64    `gorm:"not null;uniqueIndex:idx_agent24_cred_rev_tenant_jti,priority:1"`
	JTI       string    `gorm:"column:jti;type:varchar(128);not null;uniqueIndex:idx_agent24_cred_rev_tenant_jti,priority:2"`
	ExpiresAt time.Time `gorm:"not null;index:idx_agent24_cred_rev_expires_at"`
	CreatedAt time.Time
}

func (Agent24CredentialRevocation) TableName() string { return "agent24_credential_revocations" }
