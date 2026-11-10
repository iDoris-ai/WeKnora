// Agent24 fork product patch A24-KB-45 (KB-SOP-02 S3b, WeKnora 原 #146, CONTRACT-OPS-v2 W1.3, W1.9, W3.1, W3.2,
// W3.4, W3.5, W5.7, W8.5, W8.7, W8.12): candidate.approve, served to an approver's UI session. S5 registers the
// route and puts the session's effective scope E on the request context; this handler only reads the context. A UI
// request has no generation, and neither has its response.
//
// The approval is atomic (W8.5): one transaction checks ⑤ 1–5 under the candidate row lock (and the knowledge row
// lock for a revision), stages the source revision and the job through the internal ingest entry (A24-KB-41),
// moves the candidate to approved and writes the W3.2 record last. The original is saved before the transaction
// and deleted again when it does not commit; the job is dispatched after the commit. A refusal that returns the
// candidate also writes its record, so a retry gets the same refusal.
//
// Approvals of one knowledge (a revision) or of one candidate (a creation) are serialised by an in-process mutex
// held until Dispatch returns (design §3.3 step 1): one integration process per deployment, as the M3 demo stack
// runs it; several processes are not supported (PATCH-LEDGER).

package handler

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

// agent24CandidateIngest is the part of service.Agent24Ingest an approval uses (a test replaces it).
type agent24CandidateIngest interface {
	Prepare(ctx context.Context, req service.Agent24IngestRequest) (*service.Agent24IngestPrepared, error)
	Abandon(ctx context.Context, p *service.Agent24IngestPrepared)
	Stage(
		ctx context.Context, tx *gorm.DB, p *service.Agent24IngestPrepared, who service.Agent24IngestPrincipal,
		sourceRevision func(*types.Knowledge) string,
	) (*types.Agent24IngestJob, *types.Knowledge, error)
	Dispatch(
		ctx context.Context, job *types.Agent24IngestJob, k *types.Knowledge, p *service.Agent24IngestPrepared,
	) error
}

// agent24ApproveResult is the outcome of the approval transaction: a refusal, or the response to write. job, k
// and staged are set when a source revision was staged (Dispatch runs after the commit).
type agent24ApproveResult struct {
	kerr     *agent24KnowledgeError
	status   int
	stored   string
	replayed bool
	job      *types.Agent24IngestJob
	k        *types.Knowledge
	// returned is the return_reason when this request returned the candidate (W8.12 audit)
	returned string
}

// errAgent24EntryHash: the stored body no longer hashes to its content_sha256 (W8.13); approveAnswer answers
// internal and sets audit.Outcome.
var errAgent24EntryHash = errors.New(
	"agent24 candidate: recomputed entry hash does not match the stored content hash")

var (
	errAgent24ReturnNotMoved  = errors.New("agent24 candidate: return did not move the submitted candidate")
	errAgent24ApproveNotMoved = errors.New("agent24 candidate: approve did not move the submitted candidate")
)

// ReviewApprove is W8.5. Order (W1.9): ② shape (candidate_id, content_sha256; no generation); disabled (no store
// or no ingest); caller; ③(a) Authorize(candidate.approve); ③(b) the candidate is visible (not_found otherwise);
// ④ a W3.2 record of this approver for this candidate replays; then Prepare, and in one transaction ⑤ 1–5 and the
// writes (approveIn). After the commit, Dispatch.
func (h *Agent24KnowledgeCandidatesHandler) ReviewApprove(c *gin.Context) {
	req, audit, ok := agent24ReviewParsed(c, "candidate_id", "content_sha256")
	if !ok {
		return
	}
	id, sha, kerr := agent24ApproveArgs(req)
	if kerr != nil {
		agent24CandidateRefuse(c, kerr, req.echo, audit)
		return
	}
	audit.CandidateID = id
	if h.store != nil && h.ingest == nil {
		agent24CandidateRefuse(c, agent24Forbidden("operation_disabled"), req.echo, audit)
		return
	}
	caller, ok := h.adminAuth(c, Agent24WriteCandidateApprove, req, audit)
	if !ok {
		return
	}
	cand, ok := h.adminVisible(c, Agent24WriteCandidateApprove, caller, id, req, audit)
	if !ok {
		return
	}
	agent24CandidateAuditFill(audit, cand)
	ctx := c.Request.Context()
	fingerprint := agent24ApproveFingerprint(id, sha)
	unlock := h.locks.Lock(agent24ApproveLockKey(caller, cand))
	defer unlock()
	if res, done, err := h.approveReplay(ctx, caller, id, fingerprint); err != nil || done {
		h.approveAnswer(c, req, audit, res, err)
		return
	}
	prep, kerr, err := h.approvePrepare(ctx, cand, sha)
	if err != nil || kerr != nil {
		h.approveAnswer(c, req, audit, agent24ApproveResult{kerr: kerr}, err)
		return
	}
	var res agent24ApproveResult
	err = h.transaction(ctx, func(tx *repository.Agent24CandidateStore) error {
		var err error
		res, err = h.approveIn(ctx, tx, caller, id, sha, fingerprint, audit.AuthzVersion, prep)
		return err
	})
	if err != nil || res.job == nil {
		// nothing staged, or the transaction rolled back: the saved original belongs to no revision
		h.ingest.Abandon(ctx, prep)
	}
	if err == nil && res.job != nil {
		h.approveDispatch(ctx, res, prep)
	}
	h.approveAnswer(c, req, audit, res, err)
}

// agent24ApproveArgs reads candidate_id and content_sha256 (lowercase hex, 64 characters; invalid_request
// otherwise).
func agent24ApproveArgs(req agent24WriteRequest) (id, sha string, kerr *agent24KnowledgeError) {
	var ok bool
	if id, ok = agent24WireID(req.fields, "candidate_id"); !ok {
		return "", "", agent24InvalidRequest()
	}
	sha, ok = agent24WireString(req.fields, "content_sha256")
	if !ok || !agent24SHA256Pattern.MatchString(sha) {
		return "", "", agent24InvalidRequest()
	}
	return id, sha, nil
}

// agent24ApproveFingerprint is the W3.2 fingerprint of an approval: the operation class, the candidate and the
// claimed hash.
func agent24ApproveFingerprint(id, sha string) string {
	raw, _ := json.Marshal([]any{string(Agent24WriteCandidateApprove), "approve", id, sha})
	return agent24SHA256Hex(string(raw))
}

// agent24ApproveLockKey is design §3.3 step 1: tenant and knowledge_id for a revision, tenant and candidate_id for
// a creation.
func agent24ApproveLockKey(caller agent24CandidateCaller, cand *types.Agent24KnowledgeCandidate) string {
	target := "c:" + cand.ID
	if cand.Mode == types.Agent24CandidateRevise {
		target = "k:" + cand.KnowledgeID
	}
	return strconv.FormatUint(caller.tenant, 10) + "\x00" + target
}

// approveReplay is ④ before any side effect: this approver's record for id replays (W3.2; a different
// fingerprint is idempotency_key_reused). done is false when there is no record.
func (h *Agent24KnowledgeCandidatesHandler) approveReplay(
	ctx context.Context, caller agent24CandidateCaller, id, fingerprint string,
) (res agent24ApproveResult, done bool, err error) {
	rec, err := h.store.GetIdempotency(
		ctx, caller.tenant, caller.author, string(Agent24WriteCandidateApprove), id, h.now())
	if err != nil {
		return agent24ApproveResult{}, false, err
	}
	return h.agent24ApproveRecord(ctx, h.store, caller, id, fingerprint, rec, nil, 0, nil)
}

// agent24ApproveRecord replays rec, or stores a refusal (StatusCode 0) or a success body. Nothing to store is done
// false. A concurrent insert is errAgent24IdempotencyRace (M3 does not reread).
func (h *Agent24KnowledgeCandidatesHandler) agent24ApproveRecord(
	ctx context.Context, store *repository.Agent24CandidateStore, caller agent24CandidateCaller,
	id, fingerprint string, rec *types.Agent24WriteIdempotency,
	kerr *agent24KnowledgeError, status int, body map[string]any,
) (agent24ApproveResult, bool, error) {
	if rec != nil {
		if rec.Fingerprint != fingerprint {
			return agent24ApproveResult{kerr: agent24InvalidWrite("idempotency_key_reused")}, true, nil
		}
		if rec.StatusCode > 0 {
			return agent24ApproveResult{status: rec.StatusCode, stored: rec.Response, replayed: true}, true, nil
		}
		parsed, err := agent24ApproveRefusalOf(rec.Response)
		if err != nil {
			return agent24ApproveResult{}, false, err
		}
		return agent24ApproveResult{kerr: parsed, replayed: true}, true, nil
	}
	if kerr == nil && body == nil {
		return agent24ApproveResult{}, false, nil
	}
	raw, code, err := []byte(nil), status, error(nil)
	if kerr != nil {
		extra := kerr.Extra
		if extra == nil {
			extra = map[string]any{}
		}
		raw, err = json.Marshal(map[string]any{
			"error": map[string]any{"code": string(kerr.Code), "reason": kerr.Reason, "extra": extra},
		})
		code = 0
	} else {
		raw, err = json.Marshal(body)
	}
	if err != nil {
		return agent24ApproveResult{}, false, err
	}
	now := h.now()
	err = store.PutIdempotency(ctx, &types.Agent24WriteIdempotency{
		TenantID: caller.tenant, Principal: caller.author, Op: string(Agent24WriteCandidateApprove), Key: id,
		Fingerprint: fingerprint, StatusCode: code, Response: string(raw),
		CreatedAt: now, ExpiresAt: now.Add(agent24WriteIdempotencyRetention),
	})
	if errors.Is(err, repository.ErrAgent24IdempotencyExists) {
		return agent24ApproveResult{}, false, errAgent24IdempotencyRace
	}
	if err != nil {
		return agent24ApproveResult{}, false, err
	}
	return agent24ApproveResult{kerr: kerr, status: status, stored: string(raw)}, true, nil
}

func agent24ApproveRefusalOf(raw string) (*agent24KnowledgeError, error) {
	var body struct {
		Error struct {
			Code   string         `json:"code"`
			Reason string         `json:"reason"`
			Extra  map[string]any `json:"extra"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(raw), &body); err != nil {
		return nil, err
	}
	if body.Error.Code == "" {
		return nil, errors.New("agent24 candidate: approval refusal record has no code")
	}
	kerr := &agent24KnowledgeError{
		Code: agent24KnowledgeCode(body.Error.Code), Reason: body.Error.Reason, Retryable: false,
	}
	if len(body.Error.Extra) > 0 {
		kerr.Extra = body.Error.Extra
	}
	return kerr, nil
}

// approvePrepare pre-judges ⑤1 and ⑤2 on the unlocked row (a refusal saves nothing), then saves the original of
// cand before the transaction (design §3.3 step 2) and maps a Prepare error.
func (h *Agent24KnowledgeCandidatesHandler) approvePrepare(
	ctx context.Context, cand *types.Agent24KnowledgeCandidate, sha string,
) (*service.Agent24IngestPrepared, *agent24KnowledgeError, error) {
	if cand.Status != types.Agent24CandidateSubmitted {
		return nil, agent24StaleWrite("candidate_not_submitted", nil), nil
	}
	if sha != cand.ContentSHA256 {
		return nil, agent24InvalidWrite("content_hash_mismatch"), nil
	}
	knowledgeID := ""
	if cand.Mode == types.Agent24CandidateRevise {
		knowledgeID = cand.KnowledgeID
	}
	sum := sha256.Sum256([]byte(cand.Body))
	req := service.Agent24IngestRequest{
		KnowledgeBaseID: cand.TargetKnowledgeBaseID, KnowledgeID: knowledgeID, FileName: cand.ID + ".md",
		Content: []byte(cand.Body), ContentSHA256: hex.EncodeToString(sum[:]), Origin: service.Agent24OriginEntry,
	}
	// Prepare reads TenantIDFromContext; a UI session only puts the caller on the context.
	pctx := context.WithValue(ctx, types.TenantIDContextKey, types.CallerFromContext(ctx).TenantID)
	prep, err := h.ingest.Prepare(pctx, req)
	if err == nil {
		return prep, nil, nil
	}
	switch {
	case errors.Is(err, service.ErrAgent24IngestUnavailable):
		return nil, agent24Unavailable("not_ready"), nil
	case errors.Is(err, service.ErrAgent24KnowledgeNotFound):
		return nil, agent24NotFound(), nil
	case errors.Is(err, service.ErrAgent24UnsupportedKB):
		return nil, agent24Unavailable("unsupported_kb_type"), nil
	default:
		return nil, nil, err
	}
}

// approveIn is ④ again and ⑤ 1–5 with the writes, inside tx. The unlocked read only picks the row to lock.
func (h *Agent24KnowledgeCandidatesHandler) approveIn(
	ctx context.Context, tx *repository.Agent24CandidateStore, caller agent24CandidateCaller,
	id, sha, fingerprint, authz string, prep *service.Agent24IngestPrepared,
) (agent24ApproveResult, error) {
	peek, err := tx.GetCandidate(ctx, caller.tenant, id)
	if err != nil {
		return agent24ApproveResult{}, err
	}
	lockedK, gone, err := (*types.Knowledge)(nil), true, error(nil)
	if peek != nil && peek.Mode == types.Agent24CandidateRevise {
		var row types.Knowledge
		err = tx.DB().WithContext(ctx).Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("tenant_id = ? AND id = ?", caller.tenant, peek.KnowledgeID).Take(&row).Error
		if err == nil {
			lockedK, gone = &row, false
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return agent24ApproveResult{}, err
		}
	}
	cand, err := tx.LockCandidate(ctx, caller.tenant, id)
	if err != nil {
		return agent24ApproveResult{}, err
	}
	if cand == nil {
		return agent24ApproveResult{kerr: agent24StaleWrite("candidate_not_submitted", nil)}, nil
	}
	rec, err := tx.GetIdempotency(
		ctx, caller.tenant, caller.author, string(Agent24WriteCandidateApprove), id, h.now())
	if err != nil {
		return agent24ApproveResult{}, err
	}
	res, done, err := h.agent24ApproveRecord(ctx, tx, caller, id, fingerprint, rec, nil, 0, nil)
	if err != nil || done {
		return res, err
	}
	switch {
	case cand.Status != types.Agent24CandidateSubmitted:
		return agent24ApproveResult{kerr: agent24StaleWrite("candidate_not_submitted", nil)}, nil
	case sha != cand.ContentSHA256:
		return agent24ApproveResult{kerr: agent24InvalidWrite("content_hash_mismatch")}, nil
	case agent24EntryHash(cand.Kind, cand.Title, cand.Body) != cand.ContentSHA256:
		return agent24ApproveResult{}, errAgent24EntryHash
	case agent24RestrictedMarkdownViolation(cand.Body):
		return h.approveReturn(ctx, tx, caller, cand, fingerprint,
			types.Agent24ReturnUnsupportedFormat, agent24InvalidWrite("unsupported_format"))
	case len(cand.Body) > agent24EntryBodyMaxBytes:
		return h.approveReturn(ctx, tx, caller, cand, fingerprint,
			types.Agent24ReturnTooLarge, agent24InvalidWrite("too_large"))
	}
	if cand.Mode == types.Agent24CandidateRevise {
		res, err = h.approveReviseTarget(ctx, tx, caller, cand, fingerprint, lockedK, gone)
		if err != nil || res.kerr != nil {
			return res, err
		}
	}
	k, err := h.approveExisting(ctx, tx, caller, cand)
	if err != nil {
		return agent24ApproveResult{}, err
	}
	if k != nil {
		return h.approveBindExisting(ctx, tx, caller, cand, fingerprint, authz, k)
	}
	return h.approveStage(ctx, tx, caller, cand, fingerprint, authz, prep)
}

func (h *Agent24KnowledgeCandidatesHandler) approveReturn(
	ctx context.Context, tx *repository.Agent24CandidateStore, caller agent24CandidateCaller,
	cand *types.Agent24KnowledgeCandidate, fingerprint, reason string, kerr *agent24KnowledgeError,
) (agent24ApproveResult, error) {
	moved, err := tx.TransitionCandidate(ctx, caller.tenant, cand.ID, types.Agent24CandidateTransition{
		To: types.Agent24CandidateReturned, ReturnReason: reason, Approver: caller.author,
	}, h.now())
	if err != nil {
		return agent24ApproveResult{}, err
	}
	if !moved {
		return agent24ApproveResult{}, errAgent24ReturnNotMoved
	}
	res, _, err := h.agent24ApproveRecord(ctx, tx, caller, cand.ID, fingerprint, nil, kerr, 0, nil)
	res.returned = reason
	return res, err
}

// approveReviseTarget is W8.5 ⑤4. A passing target returns an empty result.
func (h *Agent24KnowledgeCandidatesHandler) approveReviseTarget(
	ctx context.Context, tx *repository.Agent24CandidateStore, caller agent24CandidateCaller,
	cand *types.Agent24KnowledgeCandidate, fingerprint string, k *types.Knowledge, gone bool,
) (agent24ApproveResult, error) {
	if gone || k == nil || k.ParseStatus == types.ParseStatusDeleting {
		return h.approveReturn(ctx, tx, caller, cand, fingerprint, types.Agent24ReturnTargetGone, agent24NotFound())
	}
	withdrawn, err := tx.IsWithdrawn(ctx, caller.tenant, k.ID)
	if err != nil {
		return agent24ApproveResult{}, err
	}
	entry, err := tx.IsEntryDocument(ctx, caller.tenant, k.ID)
	if err != nil {
		return agent24ApproveResult{}, err
	}
	if k.KnowledgeBaseID != cand.TargetKnowledgeBaseID || withdrawn || !entry {
		return h.approveReturn(
			ctx, tx, caller, cand, fingerprint, types.Agent24ReturnTargetWithdrawn, agent24NotFound())
	}
	current, err := agent24KnowledgeSourceRevision(h.key, k)
	if err != nil {
		return agent24ApproveResult{}, err
	}
	if current == cand.BaseRevision {
		return agent24ApproveResult{}, nil
	}
	return h.approveReturn(ctx, tx, caller, cand, fingerprint, types.Agent24ReturnConflicted,
		agent24StaleWrite("base_revision_changed", map[string]any{"current_revision": current}))
}

// approveExisting is W3.5: the first not-withdrawn document of the KB with an active build (completed or
// finalizing) still at the source revision an approved candidate with this content published (a revision only
// matches its own document).
func (h *Agent24KnowledgeCandidatesHandler) approveExisting(
	ctx context.Context, tx *repository.Agent24CandidateStore, caller agent24CandidateCaller,
	cand *types.Agent24KnowledgeCandidate,
) (*types.Knowledge, error) {
	list, err := tx.FindApprovedByContent(ctx, caller.tenant, cand.TargetKnowledgeBaseID, cand.ContentSHA256)
	if err != nil {
		return nil, err
	}
	for i := range list {
		if cand.Mode == types.Agent24CandidateRevise && list[i].KnowledgeID != cand.KnowledgeID {
			// a revision publishes into its own document; the same content elsewhere is not that document
			continue
		}
		var k types.Knowledge
		err = tx.DB().WithContext(ctx).
			Where("tenant_id = ? AND id = ?", caller.tenant, list[i].KnowledgeID).Take(&k).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if k.KnowledgeBaseID != cand.TargetKnowledgeBaseID || !agent24HasBuild(k.ParseStatus) {
			continue
		}
		withdrawn, err := tx.IsWithdrawn(ctx, caller.tenant, k.ID)
		if err != nil {
			return nil, err
		}
		if withdrawn {
			continue
		}
		current, err := agent24KnowledgeSourceRevision(h.key, &k)
		if err != nil {
			return nil, err
		}
		if current == list[i].SourceRevision {
			return &k, nil
		}
	}
	return nil, nil
}

func (h *Agent24KnowledgeCandidatesHandler) approveBindExisting(
	ctx context.Context, tx *repository.Agent24CandidateStore, caller agent24CandidateCaller,
	cand *types.Agent24KnowledgeCandidate, fingerprint, authz string, k *types.Knowledge,
) (agent24ApproveResult, error) {
	current, err := agent24KnowledgeSourceRevision(h.key, k)
	if err != nil {
		return agent24ApproveResult{}, err
	}
	moved, err := tx.TransitionCandidate(ctx, caller.tenant, cand.ID, types.Agent24CandidateTransition{
		To: types.Agent24CandidateApproved, Approver: caller.author, AuthorIsApprover: caller.author == cand.Author,
		KnowledgeID: k.ID, SourceRevision: current,
	}, h.now())
	if err != nil {
		return agent24ApproveResult{}, err
	}
	if !moved {
		return agent24ApproveResult{}, errAgent24ApproveNotMoved
	}
	body := map[string]any{
		"authz_version": authz, "candidate_id": cand.ID, "status": string(types.Agent24CandidateApproved),
		"knowledge_id": k.ID, "source_revision": current, "job_id": nil,
		"author_is_approver": caller.author == cand.Author,
		"details":            map[string]any{"outcome": "existing", "document_status": "indexed"},
	}
	res, _, err := h.agent24ApproveRecord(ctx, tx, caller, cand.ID, fingerprint, nil, nil, http.StatusOK, body)
	return res, err
}

func (h *Agent24KnowledgeCandidatesHandler) approveStage(
	ctx context.Context, tx *repository.Agent24CandidateStore, caller agent24CandidateCaller,
	cand *types.Agent24KnowledgeCandidate, fingerprint, authz string, prep *service.Agent24IngestPrepared,
) (agent24ApproveResult, error) {
	scope, _ := types.TenantAPIKeyScopeFromContext(ctx)
	who := service.Agent24IngestPrincipal{
		OpClass: types.Agent24JobOpCandidateApprove, UserID: caller.author, CredentialID: scope.KeyID,
		AuthzVersion: authz, PolicyVersion: Agent24ProcessingPolicyVersion(),
	}
	job, k, err := h.ingest.Stage(ctx, tx.DB(), prep, who, Agent24IngestSourceRevision(h.key))
	if errors.Is(err, service.ErrAgent24KnowledgeNotFound) {
		return h.approveReturn(ctx, tx, caller, cand, fingerprint, types.Agent24ReturnTargetGone, agent24NotFound())
	}
	if errors.Is(err, service.ErrAgent24UnsupportedKnowledge) {
		return h.approveReturn(
			ctx, tx, caller, cand, fingerprint, types.Agent24ReturnTargetWithdrawn, agent24NotFound())
	}
	if err != nil {
		return agent24ApproveResult{}, err
	}
	err = tx.DB().WithContext(ctx).Model(&types.Knowledge{}).
		Where("tenant_id = ? AND id = ?", caller.tenant, k.ID).Update("title", cand.Title).Error
	if err != nil {
		return agent24ApproveResult{}, err
	}
	k.Title = cand.Title
	moved, err := tx.TransitionCandidate(ctx, caller.tenant, cand.ID, types.Agent24CandidateTransition{
		To: types.Agent24CandidateApproved, Approver: caller.author, AuthorIsApprover: caller.author == cand.Author,
		KnowledgeID: k.ID, SourceRevision: job.SourceRevision, JobID: job.ID,
	}, h.now())
	if err != nil {
		return agent24ApproveResult{}, err
	}
	if !moved {
		return agent24ApproveResult{}, errAgent24ApproveNotMoved
	}
	body := map[string]any{
		"authz_version": authz, "candidate_id": cand.ID, "status": string(types.Agent24CandidateApproved),
		"knowledge_id": k.ID, "source_revision": job.SourceRevision, "job_id": job.ID,
		"author_is_approver": caller.author == cand.Author,
	}
	res, _, err := h.agent24ApproveRecord(ctx, tx, caller, cand.ID, fingerprint, nil, nil, http.StatusOK, body)
	if err != nil {
		return agent24ApproveResult{}, err
	}
	res.job, res.k = job, k
	return res, nil
}

// approveDispatch enqueues after the commit (W8.5). WithoutCancel: the response is already decided.
func (h *Agent24KnowledgeCandidatesHandler) approveDispatch(
	ctx context.Context, res agent24ApproveResult, prep *service.Agent24IngestPrepared,
) {
	dctx := context.WithoutCancel(ctx)
	if err := h.ingest.Dispatch(dctx, res.job, res.k, prep); err != nil {
		logger.Warnf(dctx, "[agent24] candidate approve: dispatch failed: error_type=%T", err)
	}
}

// approveAnswer writes the outcome: an error through agent24ReviewStorage (or the mapping of a Prepare error), a
// refusal, or the stored response without generation. A failed entry-hash recompute is internal.
func (h *Agent24KnowledgeCandidatesHandler) approveAnswer(
	c *gin.Context, req agent24WriteRequest, audit *agent24CandidateAudit, res agent24ApproveResult, err error,
) {
	if errors.Is(err, errAgent24EntryHash) {
		// W8.5 ⑤2: the stored body no longer hashes to its content_sha256; internal, candidate unchanged, audited
		audit.Outcome = "content_hash_recompute_failed"
		agent24CandidateRefuse(c, agent24Internal(), req.echo, audit)
		return
	}
	if err != nil {
		agent24ReviewStorage(c, req.echo, audit, err)
		return
	}
	if res.kerr != nil {
		audit.Replayed = res.replayed
		audit.ReturnReason = res.returned
		agent24CandidateRefuse(c, res.kerr, req.echo, audit)
		return
	}
	agent24WriteStoredUI(c, req.echo, res.status, res.stored, res.replayed, audit)
}

// agent24WriteStoredUI answers a stored body: this request_id, no generation, details.replayed on a replay.
func agent24WriteStoredUI(
	c *gin.Context, echo agent24KnowledgeEcho, status int, stored string, replayed bool, a *agent24CandidateAudit,
) {
	var body map[string]any
	if json.Unmarshal([]byte(stored), &body) != nil || body == nil {
		agent24CandidateRefuse(c, agent24Internal(), echo, a)
		return
	}
	if replayed {
		details, _ := body["details"].(map[string]any)
		if details == nil {
			details = map[string]any{}
		}
		details["replayed"] = true
		body["details"] = details
	}
	if a != nil {
		a.Replayed = replayed
		details, _ := body["details"].(map[string]any)
		if outcome, ok := details["outcome"].(string); ok {
			a.Outcome = outcome
		} else if a.Outcome == "" {
			a.Outcome = "approved"
		}
		if id, ok := body["candidate_id"].(string); ok {
			a.CandidateID = id
		}
		if v, ok := body["authz_version"].(string); ok && a.AuthzVersion == "" {
			a.AuthzVersion = v
		}
		// W8.12, W2.4: the approval's result document and separation of duties
		if v, ok := body["knowledge_id"].(string); ok {
			a.KnowledgeID = v
		}
		if v, ok := body["author_is_approver"].(bool); ok {
			a.AuthorIsApprover = &v
		}
	}
	body["request_id"] = echo.RequestID
	delete(body, "generation")
	agent24NoteAudit(c, func(f *agent24AuditFacts) {
		f.Echo = echo
		f.Outcome = agent24AuditOK
		f.Resource = a
	})
	c.JSON(status, body)
}
