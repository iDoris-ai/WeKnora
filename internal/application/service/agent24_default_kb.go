// Agent24 fork product patch A24-KB-51 (KB-C4-02b, CONTRACT-OPS-v2 W4.1–W4.6): idempotent default knowledge
// base initialization.
//
// An owner gets at most one default KB (W4.2): the first request inserts the owner's row (pending), creates the KB
// under an id it chose beforehand, and marks the row ready. The row's primary key, not a read before the write,
// keeps a concurrent request from creating a second one; that request hears ErrAgent24DefaultKBInProgress
// (W4.5 request_in_progress). A request that stops midway holds the row only until its lease runs out: a retry
// with the same idempotency key then takes the row over and finishes the same KB, and a request under another key
// reclaims it, removing the half-made KB first. A creation that fails removes what it made and releases the row.
// A ready row is only reported (existing), never changed (W4.4): a newer configuration version sets
// ConfigMismatch and creates nothing. Initialization does not wait for readiness and readiness does not depend on
// it (W4.6). This service binds no authorization; the init route (F2) issues the caller's credential for the KB.

package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

// agent24DefaultKBLease is how long a request holds the owner's pending row; it is well above the time a KB
// creation takes, so a live request is never taken over.
const agent24DefaultKBLease = 2 * time.Minute

// ErrAgent24DefaultKBInProgress is returned while another request holds the owner's pending row.
var ErrAgent24DefaultKBInProgress = errors.New("agent24 default kb: initialization in progress")

// Agent24DefaultKBOutcome is created or existing (W4.3).
type Agent24DefaultKBOutcome string

// The outcomes of Init.
const (
	Agent24DefaultKBCreated  Agent24DefaultKBOutcome = "created"
	Agent24DefaultKBExisting Agent24DefaultKBOutcome = "existing"
)

// Agent24DefaultKBResult is what Init reports.
type Agent24DefaultKBResult struct {
	KnowledgeBaseID string
	Outcome         Agent24DefaultKBOutcome
	ConfigVersion   string // the version the KB was created under
	ConfigMismatch  bool   // the caller asked under another version (W4.3 details.config_mismatch)
}

// Agent24DefaultKBProvisioner makes and removes the KB itself. Create must succeed when a KB with that id already
// exists (a same-key retry finishing an interrupted request); Remove must succeed when there is none.
type Agent24DefaultKBProvisioner interface {
	Create(ctx context.Context, tenantID uint64, kbID string) error
	Remove(ctx context.Context, tenantID uint64, kbID string) error
}

// Agent24DefaultKBInit runs default KB initialization.
type Agent24DefaultKBInit struct {
	store *repository.Agent24DefaultKBStore
	kbs   Agent24DefaultKBProvisioner
	lease time.Duration
	now   func() time.Time
	newID func() string
}

// NewAgent24DefaultKBInit returns the service.
func NewAgent24DefaultKBInit(
	store *repository.Agent24DefaultKBStore, kbs Agent24DefaultKBProvisioner,
) *Agent24DefaultKBInit {
	return &Agent24DefaultKBInit{
		store: store, kbs: kbs, lease: agent24DefaultKBLease, now: time.Now,
		newID: func() string { return uuid.New().String() },
	}
}

// agent24DefaultKBKeyHash is what the row keeps of an idempotency key.
func agent24DefaultKBKeyHash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// Init makes sure owner (a Principal.StorageID() in tenantID, taken from the caller's credential, never from the
// request body: W4.1) has a default KB. key is the request's idempotency key.
func (s *Agent24DefaultKBInit) Init(
	ctx context.Context, tenantID uint64, owner, key, configVersion string,
) (Agent24DefaultKBResult, error) {
	if tenantID == 0 || owner == "" || key == "" {
		return Agent24DefaultKBResult{}, errors.New("agent24 default kb: tenant, owner and key are required")
	}
	now := s.now().UTC()
	mine := types.Agent24DefaultKB{
		TenantID: tenantID, Owner: owner, KnowledgeBaseID: s.newID(), State: types.Agent24DefaultKBPending,
		KeyHash: agent24DefaultKBKeyHash(key), Claim: uuid.New().String(), ConfigVersion: configVersion,
		LeaseUntil: now.Add(s.lease), CreatedAt: now, UpdatedAt: now,
	}
	inserted, err := s.store.Insert(ctx, &mine)
	if err != nil {
		return Agent24DefaultKBResult{}, err
	}
	if inserted {
		return s.finish(ctx, mine)
	}
	row, err := s.store.Get(ctx, tenantID, owner)
	if err != nil {
		return Agent24DefaultKBResult{}, err
	}
	if row == nil {
		// released between the insert and the read: the caller retries
		return Agent24DefaultKBResult{}, ErrAgent24DefaultKBInProgress
	}
	if row.State == types.Agent24DefaultKBReady {
		return Agent24DefaultKBResult{
			KnowledgeBaseID: row.KnowledgeBaseID, Outcome: Agent24DefaultKBExisting,
			ConfigVersion: row.ConfigVersion, ConfigMismatch: row.ConfigVersion != configVersion,
		}, nil
	}
	if now.Before(row.LeaseUntil) {
		return Agent24DefaultKBResult{}, ErrAgent24DefaultKBInProgress
	}
	sameKey := row.KeyHash == mine.KeyHash
	if sameKey {
		// finish the KB the interrupted request started, under its configuration
		mine.KnowledgeBaseID, mine.ConfigVersion = row.KnowledgeBaseID, row.ConfigVersion
	}
	taken, err := s.store.TakeOver(ctx, *row, mine)
	if err != nil {
		return Agent24DefaultKBResult{}, err
	}
	if !taken {
		return Agent24DefaultKBResult{}, ErrAgent24DefaultKBInProgress
	}
	if !sameKey {
		if err := s.kbs.Remove(ctx, tenantID, row.KnowledgeBaseID); err != nil {
			// the half-made KB has no documents and no row points at it any more; creating ours goes on
			logger.Warnf(ctx, "[agent24 default kb] reclaiming %s failed: %v", row.KnowledgeBaseID, err)
		}
	}
	return s.finish(ctx, mine)
}

// finish creates the KB of the row the caller holds and marks the row ready. A failed creation removes what it
// made and releases the row.
func (s *Agent24DefaultKBInit) finish(ctx context.Context, row types.Agent24DefaultKB) (Agent24DefaultKBResult, error) {
	if err := s.kbs.Create(ctx, row.TenantID, row.KnowledgeBaseID); err != nil {
		if rmErr := s.kbs.Remove(ctx, row.TenantID, row.KnowledgeBaseID); rmErr != nil {
			logger.Warnf(ctx, "[agent24 default kb] removing %s after a failed create: %v", row.KnowledgeBaseID, rmErr)
		}
		if relErr := s.store.Release(ctx, row.TenantID, row.Owner, row.Claim); relErr != nil {
			logger.Warnf(ctx, "[agent24 default kb] releasing the row after a failed create: %v", relErr)
		}
		return Agent24DefaultKBResult{}, err
	}
	ready, err := s.store.MarkReady(ctx, row.TenantID, row.Owner, row.Claim, s.now())
	if err != nil {
		return Agent24DefaultKBResult{}, err
	}
	if !ready {
		// another request took the row over after this one's lease ran out; it owns the outcome
		return Agent24DefaultKBResult{}, ErrAgent24DefaultKBInProgress
	}
	return Agent24DefaultKBResult{
		KnowledgeBaseID: row.KnowledgeBaseID, Outcome: Agent24DefaultKBCreated, ConfigVersion: row.ConfigVersion,
	}, nil
}
