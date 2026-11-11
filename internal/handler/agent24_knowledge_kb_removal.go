package handler

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// A24-KB-48: W1.11/A14, the fork hook in front of an upstream knowledge base delete or clear (CONTRACT-OPS-v2
// W1.11, decision #A14; design ws2/kb-sop-02 §3.5). Placement and transaction boundary:
// decisions/2026-11-11-s4c-hook-placement (R0 unanimous).

const (
	// agent24KBRemovalReason is the audit reason of every row the hook writes (W1.11: 审计原因 kb_deleted), for a
	// delete and a clear alike.
	agent24KBRemovalReason = "kb_deleted"
	// agent24KBRemovalActor is withdrawn_by on the withdrawal rows the hook writes: no Agent24 principal asked.
	agent24KBRemovalActor = "agent24:kb_removal"

	// the CONTRACT-OPS-v2 W8.12 events (§6 审计事件) of the objects the hook changes
	agent24AuditActionCandidateReturn types.AuditAction = "knowledge.candidate.return"
	agent24AuditActionEntryWithdraw   types.AuditAction = "knowledge.entry.withdraw"
)

// errAgent24KBRemovalNoKey: there are entry documents to withdraw and no revision key to record their source
// revision with (agent24KnowledgeSourceRevision). Fail closed: nothing is written and nothing is removed.
var errAgent24KBRemovalNoKey = errors.New("agent24 kb removal: revision key missing")

// agent24KBRemoval implements service.Agent24KBRemovalHook.
type agent24KBRemoval struct {
	key   []byte
	store *repository.Agent24CandidateStore
	audit interfaces.AuditLogService // nil: no audit rows (tests)
	now   func() time.Time
}

// agent24KBRemovalChange is one object the hook's transaction actually changed; only these get an audit row.
type agent24KBRemovalChange struct {
	// candidate: a submitted candidate the hook returned (target_gone)
	Candidate *types.Agent24KnowledgeCandidate
	// entry document: the knowledge id the hook withdrew and the source revision it recorded
	KnowledgeID    string
	SourceRevision string
}

// InstallAgent24KBRemovalHook installs the hook on the service (both modes; see service.SetAgent24KBRemovalHook).
func InstallAgent24KBRemovalHook(db *gorm.DB, audit interfaces.AuditLogService) {
	store := repository.NewAgent24CandidateStore(db)
	service.SetAgent24KBRemovalHook(newAgent24KBRemoval(agent24SearchTagKey(), store, audit))
}

func newAgent24KBRemoval(
	key []byte, store *repository.Agent24CandidateStore, audit interfaces.AuditLogService,
) *agent24KBRemoval {
	return &agent24KBRemoval{key: key, store: store, audit: audit, now: time.Now}
}

// BeforeRemoval is service.Agent24KBRemovalHook. In one store.Transaction, in this order:
//  1. ListEntryDocumentsForKnowledgeBase(tenantID, kbID) on the transaction. Ids to withdraw and r.key == nil:
//     errAgent24KBRemovalNoKey (fail closed).
//  2. For each id, withdrawIn; collect the ones it wrote.
//  3. ListSubmittedForKnowledgeBase(tenantID, kbID) on the transaction; TransitionCandidate each to returned with
//     ReturnReason types.Agent24ReturnTargetGone (no note, no approver), at r.now(); collect the ones that moved
//     (false: it settled concurrently, nothing to record).
//  4. within(tx.DB()) when within is not nil (the soft delete of a deleted base).
//
// Any error returns at once and rolls everything back, within included; no audit row is written then. After the
// commit, writeAudit with the collected changes. Every row in the same tenant; nothing in another tenant or base
// is read or written.
func (r *agent24KBRemoval) BeforeRemoval(
	ctx context.Context, tenantID uint64, kbID string, within func(tx *gorm.DB) error,
) error {
	var changes []agent24KBRemovalChange
	err := r.store.Transaction(ctx, func(tx *repository.Agent24CandidateStore) error {
		ids, err := tx.ListEntryDocumentsForKnowledgeBase(ctx, tenantID, kbID)
		if err != nil {
			return err
		}
		if len(ids) > 0 && r.key == nil {
			return errAgent24KBRemovalNoKey
		}
		for _, kid := range ids {
			wrote, rev, err := r.withdrawIn(ctx, tx, tenantID, kbID, kid)
			if err != nil {
				return err
			}
			if wrote {
				changes = append(changes, agent24KBRemovalChange{KnowledgeID: kid, SourceRevision: rev})
			}
		}
		cands, err := tx.ListSubmittedForKnowledgeBase(ctx, tenantID, kbID)
		if err != nil {
			return err
		}
		for i := range cands {
			moved, err := tx.TransitionCandidate(ctx, tenantID, cands[i].ID, types.Agent24CandidateTransition{
				To: types.Agent24CandidateReturned, ReturnReason: types.Agent24ReturnTargetGone,
			}, r.now())
			if err != nil {
				return err
			}
			if !moved {
				continue
			}
			c := cands[i]
			changes = append(changes, agent24KBRemovalChange{Candidate: &c})
		}
		if within != nil {
			return within(tx.DB())
		}
		return nil
	})
	if err != nil {
		return err
	}
	r.writeAudit(ctx, tenantID, kbID, changes)
	return nil
}

// withdrawIn withdraws one entry document inside the hook's transaction, as entry.withdraw does (W8.10;
// entryWithdrawIn ②–④, which the first draft refactors to share one helper with this):
//   - lock the knowledge row (tenant, id) FOR UPDATE; not found, parse status deleting, or no longer in kbID:
//     nothing to do (false, "", nil);
//   - source revision = agent24KnowledgeSourceRevision(r.key, row);
//   - tx.Withdraw with WithdrawnBy agent24KBRemovalActor and WithdrawnAt r.now(); false (already withdrawn):
//     (false, "", nil);
//   - EndForKnowledge(..., types.Agent24JobCancelWithdrawn, now) and MoveKnowledge pending/processing →
//     cancelled with agent24EntryWithdrawnMessage, exactly as entryWithdrawIn; their errors are returned
//     (ErrAgent24JobChanged rolls the hook back: the delete or clear fails and can be retried).
//
// Returns whether it wrote the withdrawal and the source revision it recorded.
func (r *agent24KBRemoval) withdrawIn(
	ctx context.Context, tx *repository.Agent24CandidateStore, tenantID uint64, kbID, kid string,
) (bool, string, error) {
	var k types.Knowledge
	err := tx.DB().WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("tenant_id = ? AND id = ?", tenantID, kid).Take(&k).Error
	if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && k.ParseStatus == types.ParseStatusDeleting) {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	if k.KnowledgeBaseID != kbID {
		return false, "", nil
	}
	current, err := agent24KnowledgeSourceRevision(r.key, &k)
	if err != nil {
		return false, "", err
	}
	wrote, err := agent24WithdrawEntryIn(ctx, tx, tenantID, kid, agent24KBRemovalActor, current, r.now)
	if err != nil {
		return false, "", err
	}
	if !wrote {
		return false, "", nil
	}
	return true, current, nil
}

// writeAudit writes one agent24_knowledge row per change through r.audit (nothing when r.audit is nil), after
// the commit, in the order of changes, each on a context the caller leaving does not cancel
// (agent24AuditWriteTimeout); a failed write is logged (error type only) and the rest still written.
// Each row: TenantID tenantID, ScopeType agent24AuditScopeType, ActorUserID "", Outcome success, and
//   - candidate: Action agent24AuditActionCandidateReturn, TargetType "candidate", TargetID its id;
//   - entry document: Action agent24AuditActionEntryWithdraw, TargetType "knowledge", TargetID the knowledge id;
//
// Details is agent24KBRemovalAuditDetails as JSON.
func (r *agent24KBRemoval) writeAudit(
	ctx context.Context, tenantID uint64, kbID string, changes []agent24KBRemovalChange,
) {
	if r.audit == nil {
		return
	}
	for _, ch := range changes {
		details := agent24KBRemovalAuditDetails{
			OccurredAt: r.now().UTC().Format(time.RFC3339),
			TenantID:   tenantID,
			Reason:     agent24KBRemovalReason,
		}
		entry := &types.AuditLog{
			TenantID: tenantID, ScopeType: agent24AuditScopeType, ActorUserID: "",
			Outcome: types.AuditOutcomeSuccess,
		}
		if c := ch.Candidate; c != nil {
			resource := &agent24CandidateAudit{
				CandidateID: c.ID, ContentSHA256: c.ContentSHA256,
				TargetKnowledgeBaseID: c.TargetKnowledgeBaseID, Mode: string(c.Mode),
				ReturnReason: types.Agent24ReturnTargetGone, Outcome: "returned",
			}
			if c.KnowledgeID != "" {
				resource.KnowledgeID = c.KnowledgeID
			}
			if types.Agent24EntryKindKnown(c.Kind) {
				resource.Kind = c.Kind
			}
			details.Resource = resource
			details.Outcome = "returned"
			entry.Action = agent24AuditActionCandidateReturn
			entry.TargetType = "candidate"
			entry.TargetID = c.ID
		} else {
			details.Resource = &agent24CandidateAudit{
				KnowledgeID: ch.KnowledgeID, TargetKnowledgeBaseID: kbID,
				SourceRevision: ch.SourceRevision, Outcome: "withdrawn",
			}
			details.Outcome = "withdrawn"
			entry.Action = agent24AuditActionEntryWithdraw
			entry.TargetType = "knowledge"
			entry.TargetID = ch.KnowledgeID
		}
		raw, err := json.Marshal(details)
		if err != nil {
			logger.Warnf(ctx, "[agent24] kb removal: audit details: error_type=%T", err)
			continue
		}
		entry.Details = types.JSON(raw)
		// as the middleware writes its rows: on a context the client leaving does not cancel, bounded per row, so
		// a delete already committed still gets its rows when the caller has gone
		writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), agent24AuditWriteTimeout)
		err = r.audit.Log(writeCtx, entry)
		cancel()
		if err != nil {
			logger.Warnf(ctx, "[agent24] kb removal: audit write: error_type=%T", err)
		}
	}
}

// agent24KBRemovalAuditDetails is the Details of one row. Resource is an agent24CandidateAudit (W8.12: never a
// title, body or note):
//   - candidate: CandidateID, ContentSHA256, TargetKnowledgeBaseID, Mode, KnowledgeID (revise), Kind when
//     types.Agent24EntryKindKnown, ReturnReason types.Agent24ReturnTargetGone, Outcome "returned";
//   - entry document: KnowledgeID, TargetKnowledgeBaseID kbID, SourceRevision, Outcome "withdrawn".
type agent24KBRemovalAuditDetails struct {
	OccurredAt string                 `json:"occurred_at"` // r.now(), RFC 3339 UTC
	TenantID   uint64                 `json:"tenant_id"`
	Resource   *agent24CandidateAudit `json:"resource"`
	Outcome    string                 `json:"outcome"` // "returned" or "withdrawn"
	Reason     string                 `json:"reason"`  // agent24KBRemovalReason
}
