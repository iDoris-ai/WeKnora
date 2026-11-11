// Agent24 fork product patch A24-KB-46 (KB-SOP-02 S4a, WeKnora 原 #146, CONTRACT-OPS-v2 W1.9, W2.5, W8.10, W8.12):
// entry.withdraw, served to an organisation administrator's UI session. S5 registers the route and puts the
// session's effective scope E on the request context; this handler only reads the context. A UI request has no
// generation, and neither has its response.
//
// One transaction locks the knowledge row, records the withdrawal (agent24_entry_withdrawals, the first record
// wins: a repeat is details.already), and ends the document's work: every occupying job becomes
// cancelled/document_withdrawn, every indexed job whose enrichment runs stops it with document_withdrawn, and a row
// still pending or processing becomes cancelled. The stage gate stops a withdrawn document at each later boundary
// (A24-KB-46 in agent24_ingest_gate.go). The read surfaces filter withdrawn documents in S4b.

package handler

import (
	"context"
	"errors"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
)

// EntryWithdraw is W8.10. Order (W1.9): ② shape (knowledge_id; no generation); disabled (no store); caller;
// ③(a) Authorize(entry.withdraw); ③(b) the document exists in the tenant, is not being deleted, and the caller
// holds entry.withdraw on its knowledge base (one not_found otherwise, byte for byte); ⑤ it is an entry document
// (forbidden/operation_not_allowed otherwise); then the transaction (entryWithdrawIn).
func (h *Agent24KnowledgeCandidatesHandler) EntryWithdraw(c *gin.Context) {
	req, audit, ok := agent24ReviewParsed(c, "knowledge_id")
	if !ok {
		return
	}
	kid, ok := agent24WireID(req.fields, "knowledge_id")
	if !ok {
		agent24CandidateRefuse(c, agent24InvalidRequest(), req.echo, audit)
		return
	}
	audit.KnowledgeID = kid
	caller, ok := h.adminAuth(c, Agent24WriteEntryWithdraw, req, audit)
	if !ok {
		return
	}
	ctx := c.Request.Context()
	k, kerr, err := h.entryTarget(ctx, caller, kid)
	if err != nil {
		agent24ReviewStorage(c, req.echo, audit, err)
		return
	}
	if kerr != nil {
		agent24CandidateRefuse(c, kerr, req.echo, audit)
		return
	}
	audit.TargetKnowledgeBaseID = k.KnowledgeBaseID
	var already bool
	err = h.transaction(ctx, func(tx *repository.Agent24CandidateStore) error {
		var err error
		already, kerr, err = h.entryWithdrawIn(ctx, tx, caller, kid, k.KnowledgeBaseID, audit)
		return err
	})
	if err != nil {
		agent24ReviewStorage(c, req.echo, audit, err)
		return
	}
	if kerr != nil {
		agent24CandidateRefuse(c, kerr, req.echo, audit)
		return
	}
	body := map[string]any{"authz_version": audit.AuthzVersion, "knowledge_id": kid, "withdrawn": true}
	if already {
		body["details"] = map[string]any{"already": true}
		audit.Already = true
	}
	audit.Outcome = "withdrawn"
	agent24WriteUIOK(c, req.echo, body, audit)
}

// entryTarget is ③(b) and ⑤ outside the transaction: the knowledge of the tenant, not deleting, on a knowledge base
// where the caller holds entry.withdraw (HoldsOn; #162 N1), or agent24NotFound(); then IsEntryDocument, or
// agent24Forbidden("operation_not_allowed"). A withdrawn entry document passes (the transaction answers
// already).
func (h *Agent24KnowledgeCandidatesHandler) entryTarget(
	ctx context.Context, caller agent24CandidateCaller, kid string,
) (*types.Knowledge, *agent24KnowledgeError, error) {
	var k types.Knowledge
	err := h.store.DB().WithContext(ctx).Where("tenant_id = ? AND id = ?", caller.tenant, kid).Take(&k).Error
	if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && k.ParseStatus == types.ParseStatusDeleting) {
		return nil, agent24NotFound(), nil
	}
	if err != nil {
		return nil, nil, err
	}
	held, err := h.auth.HoldsOn(ctx, Agent24WriteEntryWithdraw, k.KnowledgeBaseID)
	if err != nil {
		return nil, nil, err
	}
	if !held {
		return nil, agent24NotFound(), nil
	}
	entry, err := h.store.IsEntryDocument(ctx, caller.tenant, kid)
	if err != nil {
		return nil, nil, err
	}
	if !entry {
		return nil, agent24Forbidden("operation_not_allowed"), nil
	}
	return &k, nil, nil
}

// entryWithdrawIn is the transaction: lock the knowledge row (gone, deleting, or moved out of the authorized KB:
// not_found), Withdraw with the current source revision (false: already, nothing else written), then end the
// occupying job's work. It fills audit.SourceRevision.
func (h *Agent24KnowledgeCandidatesHandler) entryWithdrawIn(
	ctx context.Context, tx *repository.Agent24CandidateStore, caller agent24CandidateCaller,
	kid, authorizedKBID string,
	audit *agent24CandidateAudit,
) (already bool, kerr *agent24KnowledgeError, err error) {
	var k types.Knowledge
	err = tx.DB().WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("tenant_id = ? AND id = ?", caller.tenant, kid).Take(&k).Error
	if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && k.ParseStatus == types.ParseStatusDeleting) {
		return false, agent24NotFound(), nil
	}
	if err != nil {
		return false, nil, err
	}
	if k.KnowledgeBaseID != authorizedKBID {
		return false, agent24NotFound(), nil
	}
	current, err := agent24KnowledgeSourceRevision(h.key, &k)
	if err != nil {
		return false, nil, err
	}
	audit.SourceRevision = current
	wrote, err := agent24WithdrawEntryIn(ctx, tx, caller.tenant, kid, caller.author, current, h.now)
	if err != nil {
		return false, nil, err
	}
	if !wrote {
		var row types.Agent24EntryWithdrawal
		err = tx.DB().WithContext(ctx).
			Where("tenant_id = ? AND knowledge_id = ?", caller.tenant, kid).Take(&row).Error
		if err != nil {
			return false, nil, err
		}
		audit.SourceRevision = row.SourceRevision
		return true, nil, nil
	}
	return false, nil, nil
}

// agent24WithdrawEntryIn is entryWithdrawIn's ②–④ (the Withdraw, then EndForKnowledge and MoveKnowledge). now is
// called at the same two points as that code was: WithdrawnAt, and, only when the withdrawal was written, the
// clock of EndForKnowledge and MoveKnowledge. !wrote returns before that work; the caller re-reads the existing
// SourceRevision.
func agent24WithdrawEntryIn(
	ctx context.Context, tx *repository.Agent24CandidateStore,
	tenantID uint64, kid, by, revision string, now func() time.Time,
) (bool, error) {
	wrote, err := tx.Withdraw(ctx, &types.Agent24EntryWithdrawal{
		TenantID: tenantID, KnowledgeID: kid, WithdrawnBy: by,
		SourceRevision: revision, WithdrawnAt: now(),
	})
	if err != nil {
		return false, err
	}
	if !wrote {
		return false, nil
	}
	// end the in-flight work like a new revision does, with document_withdrawn (W5.7); a job a worker moved after
	// the read rolls the transaction back (ErrAgent24JobChanged) and the request can be retried
	jobs := repository.NewAgent24IngestJobStore(tx.DB())
	at := now()
	if err = jobs.EndForKnowledge(ctx, tx.DB(), tenantID, kid, types.Agent24JobCancelWithdrawn, at); err != nil {
		return false, err
	}
	// a run still pending or processing stops like an upstream cancel: the worker's abort check reads cancelled,
	// and the activation writes (SetFinalizing, CompleteProcessingWithoutSubtasks) need processing, so no build
	// activates after this commit (W8.10). An activated build keeps its status; the read surfaces filter it (S4b)
	err = jobs.MoveKnowledge(ctx, tenantID, kid,
		[]string{types.ParseStatusPending, types.ParseStatusProcessing}, types.ParseStatusCancelled,
		agent24EntryWithdrawnMessage, at)
	return wrote, err
}

// agent24EntryWithdrawnMessage is the error_message of an in-flight knowledge row that entry.withdraw cancelled. It
// is the stage gate's message for the same stop.
const agent24EntryWithdrawnMessage = "Agent24: the entry document was withdrawn"
