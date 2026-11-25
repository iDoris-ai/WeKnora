// Agent24 fork product patch A24-KB-54 (KB-C4-04 routes, Loop 7 first round F3/F4).

// Submit, job read and job cancel for CONTRACT-OPS-v2 W1.9, W5.9 and W5.11. The refusal order matches the
// ingest probe: shape, then the credential's operation and target, then idempotency, then policy, format and
// size, and only then Prepare. A refusal before execute writes nothing.

package handler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/types"
)

// agent24IngestBodyMaxBytes caps a submit body: base64 of the 10 MiB original, plus JSON slack.
const agent24IngestBodyMaxBytes = 14 << 20

// agent24IngestSubmitOp is the idempotency operation of ingest.submit (W3.2).
const agent24IngestSubmitOp = string(types.Agent24JobOpIngestSubmit)

const agent24PartialArtifactsRetained = "retained_invisible"

const (
	agent24CancelEffectCancelled         = "cancelled"
	agent24CancelEffectEnrichmentStopped = "enrichment_stopped"
	agent24CancelEffectNone              = "none"
)

var agent24IngestSHA256Pattern = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// agent24IngestEntry is the ingest probe's submit flow. *service.Agent24Ingest satisfies it; tests substitute
// a fake so the handler does not need the unexported knowledge service.
type agent24IngestEntry interface {
	Prepare(ctx context.Context, req service.Agent24IngestRequest) (*service.Agent24IngestPrepared, error)
	Stage(
		ctx context.Context, tx *gorm.DB, p *service.Agent24IngestPrepared, who service.Agent24IngestPrincipal,
		sourceRevision func(*types.Knowledge) string,
	) (*types.Agent24IngestJob, *types.Knowledge, error)
	Dispatch(
		ctx context.Context, job *types.Agent24IngestJob, k *types.Knowledge, p *service.Agent24IngestPrepared,
	) error
	Abandon(ctx context.Context, p *service.Agent24IngestPrepared)
}

var _ agent24IngestEntry = (*service.Agent24Ingest)(nil)

// Agent24KnowledgeIngestHandler serves ingest.submit, job read and job cancel.
type Agent24KnowledgeIngestHandler struct {
	activations *service.Agent24MockActivations
	ingest      agent24IngestEntry
	jobs        *repository.Agent24IngestJobStore
	candidates  *repository.Agent24CandidateStore
	db          *gorm.DB
	key         []byte
	now         func() time.Time
	// visible reports whether the tenant can see the knowledge base (W1.9 ③(b)). Nil asks the database.
	visible func(ctx context.Context, tenant uint64, kbID string) (bool, error)
}

// NewAgent24KnowledgeIngestHandler only opens these routes in integration mode with a valid mock activation
// file and all required services. Like init, it snapshots the table at startup; invalid configuration is an error.
func NewAgent24KnowledgeIngestHandler(
	ingest *service.Agent24Ingest, jobs *repository.Agent24IngestJobStore,
	candidates *repository.Agent24CandidateStore, db *gorm.DB,
) (*Agent24KnowledgeIngestHandler, error) {
	mode, err := Agent24KnowledgeModeFromEnv()
	if err != nil {
		return nil, err
	}
	if mode != Agent24KnowledgeModeIntegration || ingest == nil || jobs == nil || candidates == nil {
		return nil, nil
	}
	activations, err := agent24MockActivationsFromEnv()
	if err != nil || activations == nil {
		return nil, err
	}
	key, err := Agent24KnowledgeTagKeyFromEnv()
	if err != nil {
		return nil, err
	}
	return &Agent24KnowledgeIngestHandler{
		activations: activations, ingest: ingest, jobs: jobs, candidates: candidates, db: db, key: key, now: time.Now,
	}, nil
}

func (h *Agent24KnowledgeIngestHandler) clock() time.Time {
	if h != nil && h.now != nil {
		return h.now()
	}
	return time.Now()
}

type agent24IngestSubmit struct {
	IdempotencyKey        string
	KnowledgeBaseID       string
	FileName              string
	Content               []byte
	ContentSHA256         string
	ExpectedPolicyVersion string
}

// Submit is POST /api/v1/agent24/knowledge/ingest.
func (h *Agent24KnowledgeIngestHandler) Submit(c *gin.Context) {
	limitJSONBody(c, agent24IngestBodyMaxBytes)
	raw, err := io.ReadAll(c.Request.Body)
	if err != nil { // the cap and a short read are both invalid_request; content too_large is checked later
		writeAgent24KnowledgeError(c, agent24InvalidRequest(), agent24KnowledgeEcho{})
		return
	}
	sub, echo, kerr := parseAgent24IngestSubmit(raw)
	if kerr != nil {
		writeAgent24KnowledgeError(c, kerr, echo)
		return
	}
	ctx := c.Request.Context()
	caller, scope, kerr := h.ingestCaller(ctx)
	if kerr != nil {
		writeAgent24KnowledgeError(c, kerr, echo)
		return
	}
	if !slices.Contains([]string(scope.KnowledgeBaseIDs), sub.KnowledgeBaseID) {
		writeAgent24KnowledgeError(c, agent24Forbidden("empty_scope"), echo)
		return
	}
	seen, err := h.kbVisible(ctx, caller.tenant, sub.KnowledgeBaseID)
	if err != nil {
		h.writeStorage(ctx, c, echo, err)
		return
	}
	if !seen {
		writeAgent24KnowledgeError(c, agent24NotFound(), echo)
		return
	}
	if ctx.Err() != nil {
		agent24NoteCancelled(c, echo, nil)
		return
	}
	fp := agent24IngestFingerprint(sub)
	rec, err := h.candidates.GetIdempotency(ctx, caller.tenant, caller.author, agent24IngestSubmitOp,
		sub.IdempotencyKey, h.clock())
	if err != nil {
		h.writeStorage(ctx, c, echo, err)
		return
	}
	if rec != nil {
		h.finishIdempotency(c, echo, rec, fp)
		return
	}
	if sub.ExpectedPolicyVersion != Agent24ProcessingPolicyVersion() {
		writeAgent24KnowledgeError(c, agent24StaleWrite("policy_changed", map[string]any{
			"current_policy_version": Agent24ProcessingPolicyVersion(),
		}), echo)
		return
	}
	if !service.Agent24IngestFormatAllowed(sub.FileName) {
		writeAgent24KnowledgeError(c, agent24InvalidWrite("unsupported_format"), echo)
		return
	}
	if service.Agent24IngestTooLarge(len(sub.Content)) {
		writeAgent24KnowledgeError(c, agent24InvalidWrite("too_large"), echo)
		return
	}
	if ctx.Err() != nil {
		agent24NoteCancelled(c, echo, nil)
		return
	}
	prep, err := h.ingest.Prepare(ctx, service.Agent24IngestRequest{
		KnowledgeBaseID: sub.KnowledgeBaseID, FileName: sub.FileName, Content: sub.Content,
		ContentSHA256: sub.ContentSHA256, Origin: service.Agent24OriginUpload,
	})
	if err != nil {
		h.writeIngestErr(ctx, c, echo, err)
		return
	}
	who := service.Agent24IngestPrincipal{
		OpClass: types.Agent24JobOpIngestSubmit, UserID: caller.author, CredentialID: scope.KeyID,
		PolicyVersion: Agent24ProcessingPolicyVersion(),
	}
	staged, knowledge, stored, err := h.stageSubmit(ctx, prep, who, caller, sub.IdempotencyKey, fp)
	if err != nil {
		h.ingest.Abandon(ctx, prep)
		if errors.Is(err, errAgent24IngestReplay) || errors.Is(err, repository.ErrAgent24IdempotencyExists) {
			h.replaySubmit(ctx, c, echo, caller, sub.IdempotencyKey, fp)
			return
		}
		h.writeIngestErr(ctx, c, echo, err)
		return
	}
	if derr := h.ingest.Dispatch(context.WithoutCancel(ctx), staged, knowledge, prep); derr != nil {
		h.writeDispatched(ctx, c, echo, caller.tenant, staged.ID)
		return
	}
	h.writeStored(c, echo, http.StatusOK, stored, false)
}

// stageSubmit runs Stage and the idempotency insert in one transaction. The stored body is the success JSON
// without request_id (W3.2 fills that on replay). A record that appears during the transaction rolls the stage
// back; the caller abandons the original and replays.
func (h *Agent24KnowledgeIngestHandler) stageSubmit(
	ctx context.Context, prep *service.Agent24IngestPrepared, who service.Agent24IngestPrincipal,
	caller agent24CandidateCaller, idemKey, fp string,
) (*types.Agent24IngestJob, *types.Knowledge, []byte, error) {
	var staged *types.Agent24IngestJob
	var knowledge *types.Knowledge
	var stored []byte
	err := h.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		store := repository.NewAgent24CandidateStore(tx)
		rec, err := store.GetIdempotency(ctx, caller.tenant, caller.author, agent24IngestSubmitOp, idemKey, h.clock())
		if err != nil {
			return err
		}
		if rec != nil {
			return errAgent24IngestReplay
		}
		job, k, err := h.ingest.Stage(ctx, tx, prep, who, Agent24IngestSourceRevision(h.key))
		if err != nil {
			return err
		}
		if job == nil || k == nil {
			return errors.New("agent24 ingest: stage returned no job")
		}
		body, err := json.Marshal(map[string]any{"job": newAgent24IngestJobView(job)})
		if err != nil {
			return err
		}
		now := h.clock()
		err = store.PutIdempotency(ctx, &types.Agent24WriteIdempotency{
			TenantID: caller.tenant, Principal: caller.author, Op: agent24IngestSubmitOp, Key: idemKey,
			Fingerprint: fp, StatusCode: http.StatusOK, Response: string(body),
			CreatedAt: now, ExpiresAt: now.Add(agent24WriteIdempotencyRetention),
		})
		if err != nil {
			return err
		}
		staged, knowledge, stored = job, k, body
		return nil
	})
	return staged, knowledge, stored, err
}

func (h *Agent24KnowledgeIngestHandler) replaySubmit(
	ctx context.Context, c *gin.Context, echo agent24KnowledgeEcho, caller agent24CandidateCaller, key, fp string,
) {
	if ctx.Err() != nil {
		agent24NoteCancelled(c, echo, nil)
		return
	}
	rec, err := h.candidates.GetIdempotency(ctx, caller.tenant, caller.author, agent24IngestSubmitOp, key, h.clock())
	if err != nil {
		h.writeStorage(ctx, c, echo, err)
		return
	}
	h.finishIdempotency(c, echo, rec, fp)
}

func (h *Agent24KnowledgeIngestHandler) finishIdempotency(
	c *gin.Context, echo agent24KnowledgeEcho, rec *types.Agent24WriteIdempotency, fp string,
) {
	res := agent24ReplayOf(rec, fp)
	if res.kerr != nil {
		writeAgent24KnowledgeError(c, res.kerr, echo)
		return
	}
	h.writeStored(c, echo, res.status, []byte(res.stored), res.replayed)
}

// writeDispatched answers 200 with the job as the store has it after a superseded or failed Dispatch.
func (h *Agent24KnowledgeIngestHandler) writeDispatched(
	ctx context.Context, c *gin.Context, echo agent24KnowledgeEcho, tenant uint64, jobID string,
) {
	if ctx.Err() != nil {
		agent24NoteCancelled(c, echo, nil)
		return
	}
	fresh, err := h.jobs.Get(ctx, tenant, jobID)
	if err != nil {
		h.writeStorage(ctx, c, echo, err)
		return
	}
	if fresh == nil {
		writeAgent24KnowledgeError(c, agent24Internal(), echo)
		return
	}
	h.writeJob(c, echo, fresh)
}

func (h *Agent24KnowledgeIngestHandler) writeJob(
	c *gin.Context, echo agent24KnowledgeEcho, job *types.Agent24IngestJob,
) {
	body, err := json.Marshal(map[string]any{"job": newAgent24IngestJobView(job)})
	if err != nil {
		writeAgent24KnowledgeError(c, agent24Internal(), echo)
		return
	}
	h.writeStored(c, echo, http.StatusOK, body, false)
}

// GetJob is POST /api/v1/agent24/knowledge/jobs/get.
func (h *Agent24KnowledgeIngestHandler) GetJob(c *gin.Context) {
	raw, ok := agent24ReadBody(c)
	if !ok {
		writeAgent24KnowledgeError(c, agent24InvalidRequest(), agent24KnowledgeEcho{})
		return
	}
	jobID, echo, kerr := parseAgent24IngestJobRef(raw)
	if kerr != nil {
		writeAgent24KnowledgeError(c, kerr, echo)
		return
	}
	ctx := c.Request.Context()
	caller, scope, kerr := h.ingestCaller(ctx)
	if kerr != nil {
		writeAgent24KnowledgeError(c, kerr, echo)
		return
	}
	job, err := service.NewAgent24JobReconciler(h.jobs).Get(ctx, caller.tenant, jobID)
	if err != nil {
		h.writeStorage(ctx, c, echo, err)
		return
	}
	if !agent24IngestJobVisible(job, caller, scope) {
		writeAgent24KnowledgeError(c, agent24NotFound(), echo)
		return
	}
	if ctx.Err() != nil {
		agent24NoteCancelled(c, echo, nil)
		return
	}
	h.writeRead(c, echo, newAgent24IngestJobView(job))
}

// CancelJob is POST /api/v1/agent24/knowledge/jobs/cancel.
func (h *Agent24KnowledgeIngestHandler) CancelJob(c *gin.Context) {
	raw, ok := agent24ReadBody(c)
	if !ok {
		writeAgent24KnowledgeError(c, agent24InvalidRequest(), agent24KnowledgeEcho{})
		return
	}
	jobID, echo, kerr := parseAgent24IngestJobRef(raw)
	if kerr != nil {
		writeAgent24KnowledgeError(c, kerr, echo)
		return
	}
	ctx := c.Request.Context()
	caller, scope, kerr := h.ingestCaller(ctx)
	if kerr != nil {
		writeAgent24KnowledgeError(c, kerr, echo)
		return
	}
	job, err := service.NewAgent24JobReconciler(h.jobs).Get(ctx, caller.tenant, jobID)
	if err != nil {
		h.writeStorage(ctx, c, echo, err)
		return
	}
	if !agent24IngestJobVisible(job, caller, scope) {
		writeAgent24KnowledgeError(c, agent24NotFound(), echo)
		return
	}
	if ctx.Err() != nil {
		agent24NoteCancelled(c, echo, nil)
		return
	}
	effect, view, err := h.cancelJob(ctx, caller, scope, job)
	if err != nil {
		if errors.Is(err, errAgent24IngestJobGone) {
			writeAgent24KnowledgeError(c, agent24NotFound(), echo)
			return
		}
		if errors.Is(err, errAgent24IngestMoving) {
			e := agent24Unavailable("request_in_progress")
			e.Extra = map[string]any{"retry_after_ms": agent24RequestInProgressRetryMS}
			writeAgent24KnowledgeError(c, e, echo)
			return
		}
		h.writeStorage(ctx, c, echo, err)
		return
	}
	h.writeCancel(c, echo, effect, view)
}

func (h *Agent24KnowledgeIngestHandler) cancelJob(
	ctx context.Context, caller agent24CandidateCaller, scope types.TenantAPIKeyScope, job *types.Agent24IngestJob,
) (string, agent24IngestJobView, error) {
	var effect string
	var view agent24IngestJobView
	err := h.jobs.Transaction(ctx, func(tx *repository.Agent24IngestJobStore) error {
		row, err := tx.LockKnowledge(ctx, caller.tenant, job.KnowledgeID)
		if err != nil {
			return err
		}
		cur, err := tx.Get(ctx, caller.tenant, job.ID)
		if err != nil {
			return err
		}
		if !agent24IngestJobVisible(cur, caller, scope) {
			return errAgent24IngestJobGone
		}
		// Decide on the locked row, not on the read before the lock (#175 round 2): a knowledge that is gone or
		// moved to another knowledge base is not this job's any more (the key's scope was checked against the job's
		// base); and a worker's finalizing/completed or a newer revision that landed meanwhile is reconciled first.
		if row == nil || row.KnowledgeBaseID != cur.KnowledgeBaseID {
			return errAgent24IngestJobGone
		}
		cur, err = service.NewAgent24JobReconciler(h.jobs).ReconcileLocked(ctx, tx, *cur)
		if err != nil {
			return err
		}
		if !agent24IngestJobVisible(cur, caller, scope) {
			return errAgent24IngestJobGone
		}
		for attempt := 0; attempt < 2; attempt++ {
			change, eff, apply := agent24IngestCancelDecision(cur)
			if !apply {
				effect = eff
				view = newAgent24IngestJobView(cur)
				return nil
			}
			ok, err := tx.Apply(ctx, *cur, change, h.clock())
			if err != nil {
				return err
			}
			if ok && change.Status == types.Agent24JobCancelled {
				// As the gate's revoke and withdraw do: the knowledge stops in the same transaction, so a build
				// past the last stage gate cannot finish and publish it.
				if err := tx.MoveKnowledge(ctx, caller.tenant, cur.KnowledgeID,
					[]string{types.ParseStatusPending, types.ParseStatusProcessing}, types.ParseStatusCancelled,
					agent24CancelledByUserMessage, h.clock()); err != nil {
					return err
				}
			}
			if ok {
				fresh, err := tx.Get(ctx, caller.tenant, job.ID)
				if err != nil {
					return err
				}
				if !agent24IngestJobVisible(fresh, caller, scope) {
					return errAgent24IngestJobGone
				}
				effect = eff
				view = newAgent24IngestJobView(fresh)
				return nil
			}
			cur, err = tx.Get(ctx, caller.tenant, job.ID)
			if err != nil {
				return err
			}
			if !agent24IngestJobVisible(cur, caller, scope) {
				return errAgent24IngestJobGone
			}
		}
		return errAgent24IngestMoving
	})
	return effect, view, err
}

// agent24CancelledByUserMessage is the error_message of a knowledge row stopped by job.cancel.
const agent24CancelledByUserMessage = "Agent24: the job was cancelled by the user"

// agent24IngestJobVisible is W1.9 ③(b) for job.read and job.cancel: the job is the caller's own, and its knowledge
// base is in the caller's key scope now (another key of the same tenant principal does not see it). Anything else is
// the same not_found as a missing job.
func agent24IngestJobVisible(
	job *types.Agent24IngestJob, caller agent24CandidateCaller, scope types.TenantAPIKeyScope,
) bool {
	return job != nil && job.PrincipalUserID == caller.author &&
		slices.Contains([]string(scope.KnowledgeBaseIDs), job.KnowledgeBaseID)
}

// agent24IngestCancelDecision is W5.9. apply is false when the job is left unchanged (effect none).
func agent24IngestCancelDecision(job *types.Agent24IngestJob) (types.Agent24JobChange, string, bool) {
	switch {
	case job.Status == types.Agent24JobSaved || job.Status == types.Agent24JobProcessing:
		return types.Agent24JobChange{
			Status: types.Agent24JobCancelled, CancelReason: types.Agent24JobCancelUser, Enrichment: job.Enrichment,
		}, agent24CancelEffectCancelled, true
	case job.Status == types.Agent24JobIndexed && job.Enrichment == types.Agent24EnrichmentRunning:
		return types.Agent24JobChange{
			Status: types.Agent24JobIndexed, Enrichment: types.Agent24EnrichmentStopped,
			StopReason: types.Agent24JobCancelUser,
		}, agent24CancelEffectEnrichmentStopped, true
	default:
		return types.Agent24JobChange{}, agent24CancelEffectNone, false
	}
}

func (h *Agent24KnowledgeIngestHandler) kbVisible(ctx context.Context, tenant uint64, kbID string) (bool, error) {
	if h.visible != nil {
		return h.visible(ctx, tenant, kbID)
	}
	var n int64
	err := h.db.WithContext(ctx).Model(&types.KnowledgeBase{}).
		Where("id = ? AND tenant_id = ?", kbID, tenant).Count(&n).Error
	return n > 0, err
}

func (h *Agent24KnowledgeIngestHandler) writeIngestErr(
	ctx context.Context, c *gin.Context, echo agent24KnowledgeEcho, err error,
) {
	if ctx.Err() != nil {
		agent24NoteCancelled(c, echo, nil)
		return
	}
	if kerr := agent24IngestMapErr(err); kerr != nil {
		writeAgent24KnowledgeError(c, kerr, echo)
		return
	}
	h.writeStorage(ctx, c, echo, err)
}

func (h *Agent24KnowledgeIngestHandler) writeStorage(
	ctx context.Context, c *gin.Context, echo agent24KnowledgeEcho, err error,
) {
	if e, gone := agent24StorageError(ctx, err); gone {
		agent24NoteCancelled(c, echo, nil)
		return
	} else if e != nil {
		writeAgent24KnowledgeError(c, e, echo)
	}
}

func (h *Agent24KnowledgeIngestHandler) writeStored(
	c *gin.Context, echo agent24KnowledgeEcho, status int, stored []byte, replayed bool,
) {
	var body map[string]any
	if json.Unmarshal(stored, &body) != nil || body == nil {
		writeAgent24KnowledgeError(c, agent24Internal(), echo)
		return
	}
	body["request_id"] = echo.RequestID
	if replayed {
		details, _ := body["details"].(map[string]any)
		if details == nil {
			details = map[string]any{}
		}
		details["replayed"] = true
		body["details"] = details
	}
	agent24NoteAudit(c, func(f *agent24AuditFacts) {
		f.Echo = echo
		f.Outcome = agent24AuditOK
	})
	c.JSON(status, body)
}

func (h *Agent24KnowledgeIngestHandler) writeRead(c *gin.Context, echo agent24KnowledgeEcho, job agent24IngestJobView) {
	agent24NoteAudit(c, func(f *agent24AuditFacts) {
		f.Echo = echo
		f.Outcome = agent24AuditOK
	})
	c.JSON(http.StatusOK, agent24IngestReadBody{RequestID: echo.RequestID, Job: job})
}

func (h *Agent24KnowledgeIngestHandler) writeCancel(
	c *gin.Context, echo agent24KnowledgeEcho, effect string, job agent24IngestJobView,
) {
	agent24NoteAudit(c, func(f *agent24AuditFacts) {
		f.Echo = echo
		f.Outcome = agent24AuditOK
	})
	c.JSON(http.StatusOK, agent24IngestCancelBody{RequestID: echo.RequestID, CancelEffect: effect, Job: job})
}

// agent24IngestMapErr is the probe's runSubmit mapping. Executor-down has no wire reason: ingest_unavailable is
// not one of the unavailable reasons, so the answer is the default unavailable.
func agent24IngestMapErr(err error) *agent24KnowledgeError {
	switch {
	case errors.Is(err, service.ErrAgent24ContentHashMismatch):
		return agent24InvalidWrite("content_hash_mismatch")
	case errors.Is(err, service.ErrAgent24IngestUnavailable):
		return agent24Unavailable("")
	case errors.Is(err, service.ErrAgent24KnowledgeNotFound):
		return agent24NotFound()
	case errors.Is(err, service.ErrAgent24InvalidFileName), errors.Is(err, service.ErrAgent24UnsupportedKB):
		return agent24InvalidRequest()
	case errors.Is(err, service.ErrAgent24UnsupportedKnowledge):
		return agent24InvalidWrite("unsupported_knowledge")
	default:
		return nil
	}
}

// ingestCaller applies W1.9 ③(a) before any target lookup. The mock tenant grant is required in addition
// to a scoped ingest key; neither grant can substitute for the other.
func (h *Agent24KnowledgeIngestHandler) ingestCaller(
	ctx context.Context,
) (agent24CandidateCaller, types.TenantAPIKeyScope, *agent24KnowledgeError) {
	caller, ok := agent24CandidateCallerOf(ctx)
	scope, scopeOK := types.TenantAPIKeyScopeFromContext(ctx)
	if !ok || caller.tenant == 0 || !scopeOK || scope.IsPlatform() || scope.FullAccess ||
		!scope.HasCapability(types.APIKeyCapabilityIngest) || !h.activations.AllowsTenant(caller.tenant) {
		return caller, scope, agent24Forbidden("operation_not_allowed")
	}
	return caller, scope, nil
}

func agent24IngestFingerprint(sub agent24IngestSubmit) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		sub.KnowledgeBaseID, sub.FileName, strings.ToLower(sub.ContentSHA256), sub.ExpectedPolicyVersion,
	}, "\x00")))
	return hex.EncodeToString(sum[:])
}

var (
	errAgent24IngestReplay  = errors.New("agent24 ingest: idempotency record appeared during submit")
	errAgent24IngestMoving  = errors.New("agent24 ingest: the job moved during cancel")
	errAgent24IngestJobGone = errors.New("agent24 ingest: the job is not visible")
)

type agent24IngestReadBody struct {
	RequestID *string              `json:"request_id"`
	Job       agent24IngestJobView `json:"job"`
}

type agent24IngestCancelBody struct {
	RequestID    *string              `json:"request_id"`
	CancelEffect string               `json:"cancel_effect"`
	Job          agent24IngestJobView `json:"job"`
}

type agent24IngestJobView struct {
	JobID            string                `json:"job_id"`
	OpClass          string                `json:"op_class"`
	Status           string                `json:"status"`
	Enrichment       string                `json:"enrichment"`
	KnowledgeBaseID  string                `json:"knowledge_base_id"`
	KnowledgeID      string                `json:"knowledge_id"`
	SourceRevision   string                `json:"source_revision"`
	IndexBuildID     string                `json:"index_build_id"`
	Attempt          int                   `json:"attempt"`
	FailureReason    string                `json:"failure_reason"`
	CancelReason     string                `json:"cancel_reason"`
	StopReason       string                `json:"stop_reason,omitempty"`
	Cost             agent24IngestCostView `json:"cost"`
	SavedAt          any                   `json:"saved_at"`
	ProcessingAt     any                   `json:"processing_at"`
	IndexedAt        any                   `json:"indexed_at"`
	TerminalAt       any                   `json:"terminal_at"`
	OriginalRetained bool                  `json:"original_retained"`
	PartialArtifacts string                `json:"partial_artifacts"`
}

type agent24IngestCostView struct {
	Final bool                    `json:"final"`
	Items []agent24IngestCostItem `json:"items"`
}

type agent24IngestCostItem struct {
	Kind        string `json:"kind"`
	Destination string `json:"destination"`
	Units       *int64 `json:"units"`
	UnitType    string `json:"unit_type"`
	Measured    string `json:"measured"`
}

func newAgent24IngestJobView(job *types.Agent24IngestJob) agent24IngestJobView {
	if job == nil {
		job = &types.Agent24IngestJob{}
	}
	items := make([]agent24IngestCostItem, 0, len(job.Cost.Items))
	for _, it := range job.Cost.Items {
		var units *int64
		// Unknown units are null, never 0, even when a stored row carries a number (W5.10).
		if it.Measured != "unknown" && it.Units != nil {
			v := *it.Units
			units = &v
		}
		items = append(items, agent24IngestCostItem{
			Kind: it.Kind, Destination: it.Destination, Units: units, UnitType: it.UnitType, Measured: it.Measured,
		})
	}
	return agent24IngestJobView{
		JobID: job.ID, OpClass: string(job.OpClass), Status: string(job.Status), Enrichment: job.Enrichment,
		KnowledgeBaseID: job.KnowledgeBaseID, KnowledgeID: job.KnowledgeID, SourceRevision: job.SourceRevision,
		IndexBuildID: job.IndexBuildID, Attempt: job.Attempt, FailureReason: job.FailureReason,
		CancelReason: job.CancelReason, StopReason: job.StopReason,
		Cost:    agent24IngestCostView{Final: job.Cost.Final, Items: items},
		SavedAt: agent24IngestRFC3339(job.SavedAt), ProcessingAt: agent24IngestRFC3339Ptr(job.ProcessingAt),
		IndexedAt: agent24IngestRFC3339Ptr(job.IndexedAt), TerminalAt: agent24IngestRFC3339Ptr(job.TerminalAt),
		OriginalRetained: true, PartialArtifacts: agent24PartialArtifactsRetained,
	}
}

func agent24IngestRFC3339(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}

func agent24IngestRFC3339Ptr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return agent24IngestRFC3339(*t)
}

var agent24IngestSubmitFields = []string{
	"idempotency_key", "knowledge_base_id", "file_name", "content_base64", "content_sha256",
	"expected_policy_version",
}

func parseAgent24IngestSubmit(body []byte) (agent24IngestSubmit, agent24KnowledgeEcho, *agent24KnowledgeError) {
	var sub agent24IngestSubmit
	fields, echo, kerr := agent24IngestObject(body, agent24IngestSubmitFields...)
	if kerr != nil {
		return sub, echo, kerr
	}
	raw, ok := fields["idempotency_key"]
	if !ok {
		return sub, echo, agent24InvalidWrite("idempotency_key_missing")
	}
	var key *string
	if json.Unmarshal(raw, &key) != nil {
		return sub, echo, agent24InvalidRequest()
	}
	if key == nil || *key == "" {
		return sub, echo, agent24InvalidWrite("idempotency_key_missing")
	}
	if !agent24IdempotencyKeyOK(*key) {
		return sub, echo, agent24InvalidRequest()
	}
	sub.IdempotencyKey = *key
	if sub.KnowledgeBaseID, ok = agent24WireID(fields, "knowledge_base_id"); !ok {
		return sub, echo, agent24InvalidRequest()
	}
	name, ok := agent24WireString(fields, "file_name")
	if !ok || name == "" {
		return sub, echo, agent24InvalidRequest()
	}
	sub.FileName = name
	b64, ok := agent24WireString(fields, "content_base64")
	if !ok {
		return sub, echo, agent24InvalidRequest()
	}
	content, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return sub, echo, agent24InvalidRequest()
	}
	sub.Content = content
	sha, ok := agent24WireString(fields, "content_sha256")
	if !ok || !agent24IngestSHA256Pattern.MatchString(sha) {
		return sub, echo, agent24InvalidRequest()
	}
	sub.ContentSHA256 = sha
	policy, ok := agent24WireString(fields, "expected_policy_version")
	if !ok || policy == "" {
		return sub, echo, agent24InvalidRequest()
	}
	sub.ExpectedPolicyVersion = policy
	return sub, echo, nil
}

func parseAgent24IngestJobRef(body []byte) (string, agent24KnowledgeEcho, *agent24KnowledgeError) {
	fields, echo, kerr := agent24IngestObject(body, "job_id")
	if kerr != nil {
		return "", echo, kerr
	}
	id, ok := agent24WireID(fields, "job_id")
	if !ok {
		return "", echo, agent24InvalidRequest()
	}
	return id, echo, nil
}

// agent24IngestObject decodes one JSON object, rejects unknown fields and a bad optional request_id, and echoes a
// valid request_id even when a later field is refused.
func agent24IngestObject(body []byte, allowed ...string) (
	map[string]json.RawMessage, agent24KnowledgeEcho, *agent24KnowledgeError,
) {
	var echo agent24KnowledgeEcho
	if !utf8.Valid(body) {
		return nil, echo, agent24InvalidRequest()
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var fields map[string]json.RawMessage
	if dec.Decode(&fields) != nil || fields == nil {
		return nil, echo, agent24InvalidRequest()
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, echo, agent24InvalidRequest()
	}
	known := map[string]bool{"request_id": true}
	for _, name := range allowed {
		known[name] = true
	}
	for name := range fields {
		if !known[name] {
			return nil, echo, agent24InvalidRequest()
		}
	}
	if raw, ok := fields["request_id"]; ok && !agent24JSONHasUnusableEscape(raw) {
		var s string
		if json.Unmarshal(raw, &s) == nil && s != "" && utf8.RuneCountInString(s) <= agent24RequestIDMaxRunes {
			echo.RequestID = &s
		}
	}
	for _, raw := range fields {
		if agent24JSONHasUnusableEscape(raw) {
			return fields, echo, agent24InvalidRequest()
		}
	}
	if _, ok := fields["request_id"]; ok && echo.RequestID == nil {
		return fields, agent24KnowledgeEcho{}, agent24InvalidRequest()
	}
	return fields, echo, nil
}
