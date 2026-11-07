// Agent24 fork product patch A24-KB-31 (KB-C2-01a, WeKnora#133, CONTRACT-UI-BRIDGE-v1 U2–U3).

package interfaces

import (
	"context"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

// ErrAgent24UIFamilyOwner means this vph is already registered to another tenant or principal.
// CreateTicket returns it and does not insert the ticket.
var ErrAgent24UIFamilyOwner = errors.New("agent24 ui family belongs to another tenant or principal")

// ErrAgent24UIFamilyRevoked means this vph is already revoked. CreateTicket returns it and does not insert
// the ticket.
var ErrAgent24UIFamilyRevoked = errors.New("agent24 ui family is revoked")

// Agent24UISessionStore is the UI ticket and view-family store (A24-KB-31).
// now is supplied by the caller so tests can fix the clock.
type Agent24UISessionStore interface {
	// CreateTicket registers the ticket's view family and inserts the ticket, in one transaction.
	// A missing family is inserted: tenant and principal come from the ticket, and expires_at is the ticket's
	// ExpiresAt plus types.Agent24UISessionMaxLifetime. An existing family with a different tenant or principal
	// returns ErrAgent24UIFamilyOwner and the ticket is not inserted. An existing revoked family returns
	// ErrAgent24UIFamilyRevoked and the ticket is not inserted. An existing family with the same owner keeps
	// the later of its expires_at and the ticket's ExpiresAt plus types.Agent24UISessionMaxLifetime.
	// TicketHash, TicketID, TenantID, Principal or VPH empty, or an empty scope, is an error.
	CreateTicket(ctx context.Context, t *types.Agent24UITicket) error
	// ConsumeTicket marks the ticket consumed and returns the stored row.
	// One statement does the write: UPDATE agent24_ui_tickets SET consumed_at = now WHERE ticket_hash = ?
	// AND consumed_at IS NULL. RowsAffected == 1 means first is true, and under concurrency at most one
	// caller observes that. The row is then read back by ticket_hash. A missing row is (nil, false, nil).
	// A row already consumed is returned with first false. Expiry and revocation are not decided here:
	// U2.6 spends the ticket whichever way the later checks go, and the caller decides in order.
	ConsumeTicket(ctx context.Context, ticketHash string, now time.Time) (
		t *types.Agent24UITicket, first bool, err error)
	// RevokeTicket sets revoked_at on the row whose ticket_id, tenant and principal all match and whose
	// revoked_at is still null. No matching row is not an error: U3.7 makes existence indistinguishable.
	RevokeTicket(ctx context.Context, tenantID uint64, principal, ticketID string, now time.Time) error
	// RevokeFamily sets revoked_at on the family whose vph, tenant and principal all match and whose
	// revoked_at is still null. No matching row is not an error (U3.7).
	RevokeFamily(ctx context.Context, tenantID uint64, principal, vph string, now time.Time) error
	// TicketRevoked reports whether the ticket is revoked. revoked_at set is true. A missing row is true
	// too: a JWT whose ticket cannot be found is refused (fail closed).
	TicketRevoked(ctx context.Context, ticketID string) (bool, error)
	// FamilyRevoked reports whether the view family is revoked. revoked_at set is true. A missing row is
	// true too (fail closed).
	FamilyRevoked(ctx context.Context, vph string) (bool, error)
	// PurgeExpired deletes tickets whose expires_at is at or before now minus types.Agent24UISessionMaxLifetime
	// (a JWT taken for a ticket lives at most that long after the ticket expires) and families whose
	// expires_at is at or before now. It returns how many rows it deleted.
	PurgeExpired(ctx context.Context, now time.Time) (int64, error)
}
