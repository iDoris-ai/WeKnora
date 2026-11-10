// Agent24 fork product patch A24-KB-41 (KB-C4-04, WeKnora#136, CONTRACT-OPS-v2 W1.3): the stage gate of an
// Agent24 ingest job, run by the standalone worker at the boundaries upstream already checks for cancellation.
//
// Upstream calls it from two places, one line each: isKnowledgeAborted (the worker entry before it marks the row
// processing, and every boundary of processChunks) and the post-process handler before it promotes the row to
// finalizing, which is when the build becomes servable. A knowledge without the agent24_origin metadata is read
// once for an occupying job: a job means it is an Agent24 ingest (an upstream whole-row save may have wiped the
// marker); no job goes through untouched.
//
// At each boundary, in order (W1.3; the first that fails decides):
//
//	① the job's principal still holds the operation class that established the job, on the target knowledge base;
//	② the credential that established it is not revoked (expiry is not revocation);
//	③ the current processing policy still allows the knowledge base's embedding destination.
//
// ① or ② failing cancels the knowledge (upstream checkpoints then stop) and the job becomes
// cancelled/authorization_revoked; ③ failing fails the knowledge and the job becomes failed/policy_denied.
// The job and the knowledge move in one transaction under the knowledge row lock Stage takes, so a gate that
// read an older job cannot stop the knowledge of a newer revision.
//
// An Agent24 ingest that no job occupies any more (its job ended, or an upstream reparse restarted it) does not
// run: a late or retried task stops, and a row still pending or processing is failed (W5.8: a settled job's
// knowledge does not become servable afterwards). A build already servable (finalizing, completed) is left to
// upstream at the post-process boundary, so a retried post-process of an activated build still finishes its
// enrichment; at the worker entry and processChunks it stops without a write (a redelivered document task must not
// rebuild an activated build).
//
// A saved job whose attempt Dispatch has not recorded yet has no task: a worker that meets it runs an older
// attempt and stops without touching the job, so the job's own Dispatch still finds it saved.
// A gate that cannot read what it needs aborts with the upstream "unreadable" status, so the task is retried
// rather than run unchecked; so does a process where the gate was never installed.

package service

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync/atomic"
	"time"

	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

// Agent24IngestPolicy is W1.3 ③: whether the current processing policy allows the knowledge base's processing
// destination. The integration handlers implement it over the A24-KB-27/29 policy.
type Agent24IngestPolicy interface {
	AllowIngest(ctx context.Context, kb *types.KnowledgeBase) error
	// EmbeddingGuard is the guard an Agent24 ingest's embedding calls run under (refuses everything when the
	// policy cannot be read).
	EmbeddingGuard() types.ProcessingGuard
}

// ErrAgent24PolicyDenied is what an Agent24IngestPolicy returns for a destination the policy does not allow.
// Any other error is a failure to answer and is retried.
var ErrAgent24PolicyDenied = errors.New("agent24 ingest: the processing policy does not allow this destination")

type agent24StageGate struct {
	db     *gorm.DB
	jobs   *repository.Agent24IngestJobStore
	policy Agent24IngestPolicy
	now    func() time.Time
}

var agent24Gate atomic.Pointer[agent24StageGate]

// SetAgent24StageGate installs the stage gate. The container calls it once in both modes.
func SetAgent24StageGate(db *gorm.DB, jobs *repository.Agent24IngestJobStore, policy Agent24IngestPolicy) {
	agent24Gate.Store(&agent24StageGate{db: db, jobs: jobs, policy: policy, now: time.Now})
}

// agent24IngestEmbeddingContext puts the policy's embedding guard on an Agent24 ingest's task context, so each
// embedding call is checked at call time and cannot follow a redirect away from the approved endpoint (W1.3 ③,
// same as the search path). Without the gate installed a marked knowledge's guard refuses every destination, and
// an unmarked one whose occupying job cannot be read gets the same. Other documents get ctx back unchanged.
func agent24IngestEmbeddingContext(ctx context.Context, k *types.Knowledge) context.Context {
	ingest, err := agent24KnowledgeIsIngest(ctx, k)
	if err != nil {
		// the occupying-job read failed: an unmarked row cannot be told from an Agent24 ingest, so fail closed
		return types.WithProcessingEmbeddingGuard(ctx, agent24DenyAllGuard{})
	}
	if !ingest {
		return ctx
	}
	var guard types.ProcessingGuard = agent24DenyAllGuard{}
	if g := agent24Gate.Load(); g != nil && g.policy != nil {
		if pg := g.policy.EmbeddingGuard(); pg != nil {
			guard = pg
		}
	}
	return types.WithProcessingEmbeddingGuard(ctx, guard)
}

// agent24DenyAllGuard refuses every destination.
type agent24DenyAllGuard struct{}

func (agent24DenyAllGuard) Allow(context.Context, types.ProcessingDestination) error {
	return types.ErrProcessingDenied
}

// agent24IsIngest reports whether the knowledge was created or revised by the Agent24 ingest entry.
func agent24IsIngest(k *types.Knowledge) bool {
	if k == nil || len(k.Metadata) == 0 {
		return false
	}
	var meta map[string]any
	if err := json.Unmarshal(k.Metadata, &meta); err != nil {
		return false
	}
	_, ok := meta[agent24OriginMetadataKey]
	return ok
}

// agent24KnowledgeIsIngest reports whether k is an Agent24 ingest: the origin marker, or, when that marker is
// gone, an occupying job. An upstream whole-row save can wipe the marker. A gate that is not installed cannot
// read the job, so an unmarked knowledge is not an ingest (it goes through untouched). err is set only when
// that occupying-job read fails.
func agent24KnowledgeIsIngest(ctx context.Context, k *types.Knowledge) (bool, error) {
	if agent24IsIngest(k) {
		return true, nil
	}
	g := agent24Gate.Load()
	if k == nil || g == nil || g.db == nil || g.jobs == nil || g.policy == nil {
		return false, nil
	}
	job, err := g.jobs.Occupying(ctx, k.TenantID, k.ID)
	if err != nil {
		return false, err
	}
	return job != nil, nil
}

// agent24StageGateAborts runs the gate for k. aborted reports that the caller must stop; status is the
// isKnowledgeAborted status to stop with (cancelled once the gate settled the job, unreadable to retry).
func agent24StageGateAborts(ctx context.Context, k *types.Knowledge) (aborted bool, status string) {
	return agent24RunStageGate(ctx, k, false)
}

// agent24PostProcessGateAborts is agent24StageGateAborts at the post-process boundary, where an activated build
// (finalizing, completed) with no occupying job goes on to its enrichment.
func agent24PostProcessGateAborts(ctx context.Context, k *types.Knowledge) (aborted bool, status string) {
	return agent24RunStageGate(ctx, k, true)
}

func agent24RunStageGate(ctx context.Context, k *types.Knowledge, postProcess bool) (bool, string) {
	ingest, err := agent24KnowledgeIsIngest(ctx, k)
	if err != nil {
		return true, abortStatusUnreadable
	}
	if !ingest {
		// unmarked and no occupying job: an upstream document, not an orphan
		return false, ""
	}
	g := agent24Gate.Load()
	if g == nil || g.db == nil || g.jobs == nil || g.policy == nil {
		logger.Warnf(ctx, "[agent24] stage gate is not installed; an Agent24 ingest does not run unchecked")
		return true, abortStatusUnreadable
	}
	return g.check(ctx, k, postProcess)
}

func (g *agent24StageGate) check(ctx context.Context, k *types.Knowledge, postProcess bool) (bool, string) {
	if !postProcess && (k.ParseStatus == types.ParseStatusFinalizing || k.ParseStatus == types.ParseStatusCompleted) {
		// the build is already servable: a redelivered document task stops before it marks the row processing
		// and rebuilds, whether or not the reconciler has moved the job to indexed yet
		return true, types.ParseStatusCancelled
	}
	job, err := g.jobs.Occupying(ctx, k.TenantID, k.ID)
	if err != nil {
		return true, abortStatusUnreadable
	}
	if job == nil {
		return g.orphan(ctx, k, postProcess)
	}
	if job.Status == types.Agent24JobSaved && job.Attempt == 0 {
		// the job's task is not enqueued yet (Dispatch records the attempt first): this worker is an older one
		return true, types.ParseStatusCancelled
	}
	held, err := g.holds(ctx, job)
	if err != nil {
		return true, abortStatusUnreadable
	}
	if !held {
		return g.settle(ctx, job, k, types.Agent24JobChange{
			Status: types.Agent24JobCancelled, CancelReason: types.Agent24JobCancelRevoked, Enrichment: job.Enrichment,
		}, types.ParseStatusCancelled, "Agent24: the job's authorization was revoked")
	}
	var kb types.KnowledgeBase
	if err := g.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", k.TenantID, k.KnowledgeBaseID).
		Take(&kb).Error; err != nil {
		return true, abortStatusUnreadable
	}
	if err := g.policy.AllowIngest(ctx, &kb); err != nil {
		if !errors.Is(err, ErrAgent24PolicyDenied) {
			return true, abortStatusUnreadable
		}
		return g.settle(ctx, job, k, types.Agent24JobChange{
			Status: types.Agent24JobFailed, FailureReason: types.Agent24JobFailPolicy, Enrichment: job.Enrichment,
		}, types.ParseStatusFailed, "Agent24: the processing policy does not allow this destination")
	}
	if job.Status == types.Agent24JobSaved {
		if _, err := g.jobs.Apply(ctx, *job, types.Agent24JobChange{
			Status: types.Agent24JobProcessing, Enrichment: job.Enrichment,
		}, g.now()); err != nil {
			logger.Warnf(ctx, "[agent24] stage gate: recording processing failed: error_type=%T", err)
		}
	}
	return false, ""
}

// holds is W1.3 ① and ②, by operation class. A class this build does not know is held by nobody.
func (g *agent24StageGate) holds(ctx context.Context, job *types.Agent24IngestJob) (bool, error) {
	switch job.OpClass {
	case types.Agent24JobOpIngestSubmit:
		return g.keyHolds(ctx, job, types.APIKeyCapabilityIngest)
	case types.Agent24JobOpCandidateApprove:
		return g.approveHolds(ctx, job)
	default:
		return false, nil
	}
}

// approveHolds is W1.3 ① and ② for a candidate.approve job (A24-KB-45): the approver still holds an unrevoked
// organisation-administrator grant on the job's knowledge base, and the establishing key still carries
// kb_candidate_approve on it. A key that does not hold is not an unreadable grant table.
func (g *agent24StageGate) approveHolds(ctx context.Context, job *types.Agent24IngestJob) (bool, error) {
	held, err := g.keyHolds(ctx, job, types.APIKeyCapabilityKBCandidateApprove)
	if err != nil || !held {
		return false, err
	}
	return repository.NewAgent24CandidateStore(g.db).AdminHolds(
		ctx, job.TenantID, job.PrincipalUserID, job.KnowledgeBaseID)
}

// keyHolds reads the establishing tenant API key as it is now: it still exists and is not revoked (②), and it
// carries the capability with the job's knowledge base in its allow-list (①). An empty allow-list or a
// full-access key is no write scope (READONLY r12 R2.7). Expiry is not consulted (W1.3).
func (g *agent24StageGate) keyHolds(
	ctx context.Context, job *types.Agent24IngestJob, capability types.APIKeyCapability,
) (bool, error) {
	if job.CredentialID == 0 {
		return false, nil
	}
	var key types.TenantAPIKey
	err := g.db.WithContext(ctx).Where("tenant_id = ? AND id = ?", job.TenantID, job.CredentialID).Take(&key).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if key.RevokedAt != nil || key.ScopeType == types.APIKeyScopePlatform {
		return false, nil
	}
	scope := types.TenantAPIKeyScope{
		KeyID: key.ID, FullAccess: key.FullAccess, KnowledgeBaseIDs: key.KnowledgeBaseIDs,
		Capabilities: key.Capabilities,
	}.Normalize()
	if scope.FullAccess || !scope.HasCapability(capability) {
		return false, nil
	}
	return slices.Contains([]string(scope.KnowledgeBaseIDs), job.KnowledgeBaseID), nil
}

// errAgent24GateRace: the knowledge's occupying job changed under the gate; the task retries and the next check
// reads the new state.
var errAgent24GateRace = errors.New("agent24 stage gate: the knowledge's job changed")

// settle ends the job and moves the knowledge so upstream stops. Both happen under the knowledge row lock, and
// only while the job is still the one the gate read; the knowledge is only moved from a state still in flight,
// so a cancel or delete that won the race is kept.
func (g *agent24StageGate) settle(
	ctx context.Context, job *types.Agent24IngestJob, k *types.Knowledge, change types.Agent24JobChange,
	parseStatus, message string,
) (bool, string) {
	now := g.now()
	err := g.jobs.Transaction(ctx, func(tx *repository.Agent24IngestJobStore) error {
		row, err := tx.LockKnowledge(ctx, k.TenantID, k.ID)
		if err != nil {
			return err
		}
		applied, err := tx.Apply(ctx, *job, change, now)
		if err != nil {
			return err
		}
		if !applied {
			return errAgent24GateRace
		}
		if row == nil {
			return nil
		}
		return tx.MoveKnowledge(ctx, k.TenantID, k.ID, agent24InFlight, parseStatus, message, now)
	})
	if err != nil {
		return true, abortStatusUnreadable
	}
	logger.Infof(ctx, "[agent24] stage gate stopped knowledge %s: job=%s status=%s", k.ID, job.ID, change.Status)
	return true, types.ParseStatusCancelled
}

// agent24InFlight are the knowledge states a run is still moving.
var agent24InFlight = []string{types.ParseStatusPending, types.ParseStatusProcessing, types.ParseStatusFinalizing}

// orphan stops an Agent24 ingest that no job occupies (see the file comment).
func (g *agent24StageGate) orphan(ctx context.Context, k *types.Knowledge, postProcess bool) (bool, string) {
	switch k.ParseStatus {
	case types.ParseStatusFinalizing, types.ParseStatusCompleted:
		if postProcess {
			return false, ""
		}
		return true, types.ParseStatusCancelled
	}
	err := g.jobs.Transaction(ctx, func(tx *repository.Agent24IngestJobStore) error {
		row, err := tx.LockKnowledge(ctx, k.TenantID, k.ID)
		if err != nil || row == nil {
			return err
		}
		cur, err := tx.Occupying(ctx, k.TenantID, k.ID)
		if err != nil {
			return err
		}
		if cur != nil {
			return errAgent24GateRace
		}
		return tx.MoveKnowledge(ctx, k.TenantID, k.ID,
			[]string{types.ParseStatusPending, types.ParseStatusProcessing}, types.ParseStatusFailed,
			"Agent24: no Agent24 job covers this run; submit the document again through Agent24", g.now())
	})
	if err != nil {
		return true, abortStatusUnreadable
	}
	logger.Infof(ctx, "[agent24] stage gate stopped knowledge %s: no occupying job", k.ID)
	return true, types.ParseStatusCancelled
}
