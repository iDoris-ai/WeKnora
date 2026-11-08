// Agent24 fork product patch A24-KB-41 (KB-C4-04, WeKnora#136, CONTRACT-OPS-v2 W1.3): the stage gate of an
// Agent24 ingest job, run by the standalone worker at the boundaries upstream already checks for cancellation.
//
// Upstream calls it from two places, one line each: isKnowledgeAborted (the worker entry before it marks the row
// processing, and every boundary of processChunks) and the post-process handler before it promotes the row to
// finalizing, which is when the build becomes servable. A knowledge without the agent24_origin metadata is not an
// Agent24 ingest and goes through untouched, without a database read.
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
// upstream, so a retried post-process of an activated build still finishes its enrichment.
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

// agent24StageGateAborts runs the gate for k. aborted reports that the caller must stop; status is the
// isKnowledgeAborted status to stop with (cancelled once the gate settled the job, unreadable to retry).
func agent24StageGateAborts(ctx context.Context, k *types.Knowledge) (aborted bool, status string) {
	if !agent24IsIngest(k) {
		return false, ""
	}
	g := agent24Gate.Load()
	if g == nil || g.db == nil || g.jobs == nil || g.policy == nil {
		logger.Warnf(ctx, "[agent24] stage gate is not installed; an Agent24 ingest does not run unchecked")
		return true, abortStatusUnreadable
	}
	return g.check(ctx, k)
}

func (g *agent24StageGate) check(ctx context.Context, k *types.Knowledge) (bool, string) {
	job, err := g.jobs.Occupying(ctx, k.TenantID, k.ID)
	if err != nil {
		return true, abortStatusUnreadable
	}
	if job == nil {
		return g.orphan(ctx, k)
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
	default:
		// candidate.approve is rechecked against the organisation grants KB-SOP-02 (WeKnora#146) adds
		return false, nil
	}
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
func (g *agent24StageGate) orphan(ctx context.Context, k *types.Knowledge) (bool, string) {
	switch k.ParseStatus {
	case types.ParseStatusFinalizing, types.ParseStatusCompleted:
		return false, ""
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
