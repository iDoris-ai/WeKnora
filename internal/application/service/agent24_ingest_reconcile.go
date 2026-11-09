// Agent24 fork product patch A24-KB-41 (KB-C4-04, WeKnora#136, CONTRACT-OPS-v2 W3.3, W5.6, W5.7): settling Agent24
// ingest jobs whose knowledge moved on without passing a stage gate (upstream failures, deletion, stalls, a job
// still saved past its time limit). Agent24JobGet reconciles before it answers, so a reader never depends on the
// sweep's rhythm; the standalone sweep settles the rest and purges old jobs.

package service

import (
	"context"
	"fmt"
	"time"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/types"
)

// The W5.7 and W3.3 limits. A worker picks a saved job up within seconds; ten minutes covers a busy queue and a
// worker restart. Enrichment covers summary generation on a slow model.
const (
	agent24JobSavedLimit      = 10 * time.Minute
	agent24JobEnrichmentLimit = 2 * time.Hour
	agent24JobRetention       = 90 * 24 * time.Hour
	agent24ReconcileBatch     = 200
)

// Agent24JobReconciler applies Agent24ReconcileJob to stored jobs.
type Agent24JobReconciler struct {
	jobs   *repository.Agent24IngestJobStore
	limits types.Agent24ReconcileLimits
	now    func() time.Time
}

// NewAgent24JobReconciler reconciles over the job store with the W5.7 limits.
func NewAgent24JobReconciler(jobs *repository.Agent24IngestJobStore) *Agent24JobReconciler {
	return &Agent24JobReconciler{
		jobs:   jobs,
		limits: types.Agent24ReconcileLimits{Saved: agent24JobSavedLimit, Enrichment: agent24JobEnrichmentLimit},
		now:    time.Now,
	}
}

// Get returns the tenant's job after reconciling it, or nil when there is none.
func (r *Agent24JobReconciler) Get(ctx context.Context, tenantID uint64, id string) (*types.Agent24IngestJob, error) {
	job, err := r.jobs.Get(ctx, tenantID, id)
	if err != nil || job == nil {
		return job, err
	}
	if err := r.reconcile(ctx, *job); err != nil {
		return nil, err
	}
	return r.jobs.Get(ctx, tenantID, id)
}

// reconcile moves one job to what its knowledge says. A lost race (another writer moved the job first) is not
// an error: that writer's state stands.
func (r *Agent24JobReconciler) reconcile(ctx context.Context, job types.Agent24IngestJob) error {
	view, err := r.jobs.KnowledgeView(ctx, job.TenantID, job.KnowledgeID)
	if err != nil {
		return err
	}
	if job.Attempt == 0 {
		// Dispatch has not recorded the attempt yet: an older attempt of the same knowledge is not a supersede
		view.LatestAttempt = 0
	}
	change, ok := types.Agent24ReconcileJob(job, view, r.now(), r.limits)
	if !ok {
		return nil
	}
	if !change.FailKnowledge {
		applied, err := r.jobs.Apply(ctx, job, change, r.now())
		if err != nil || !applied {
			return err
		}
		return nil
	}
	// Stage takes this same knowledge row lock, so the occupier read after the lock is the last one. When this
	// job is no longer that occupier, the knowledge belongs to the newer revision and must not be failed.
	now := r.now()
	return r.jobs.Transaction(ctx, func(tx *repository.Agent24IngestJobStore) error {
		row, err := tx.LockKnowledge(ctx, job.TenantID, job.KnowledgeID)
		if err != nil {
			return err
		}
		cur, err := tx.Occupying(ctx, job.TenantID, job.KnowledgeID)
		if err != nil {
			return err
		}
		applied, err := tx.Apply(ctx, job, change, now)
		if err != nil {
			return err
		}
		if !applied {
			return nil
		}
		if row == nil || cur == nil || cur.ID != job.ID {
			return nil
		}
		return tx.FailPendingKnowledge(ctx, job.TenantID, job.KnowledgeID,
			"Agent24: no worker picked the job up in time", now)
	})
}

// Sweep reconciles one batch of unsettled jobs and purges jobs settled longer ago than the retention. It returns
// how many jobs it looked at.
func (r *Agent24JobReconciler) Sweep(ctx context.Context) (int, error) {
	jobs, err := r.jobs.ListUnsettled(ctx, agent24ReconcileBatch)
	if err != nil {
		return 0, err
	}
	var firstErr error
	for _, job := range jobs {
		if err := r.reconcile(ctx, job); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("reconcile job: %w", err)
		}
	}
	if _, err := r.jobs.PurgeSettled(ctx, r.now().Add(-agent24JobRetention)); err != nil && firstErr == nil {
		firstErr = fmt.Errorf("purge jobs: %w", err)
	}
	return len(jobs), firstErr
}
