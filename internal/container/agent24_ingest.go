// Agent24 fork product patch A24-KB-41 (KB-C4-04, WeKnora#136) and A24-KB-42 (KB-SOP-02, WeKnora#146).

package container

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/dig"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/router"
	"github.com/Tencent/WeKnora/internal/runtime"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// agent24IngestReconcileTimeout bounds one sweep, so a stalled database cannot hold the loop (or shutdown).
const agent24IngestReconcileTimeout = time.Minute

// agent24SweepInterval is how often each background sweep runs; a test shortens it.
var agent24SweepInterval = time.Minute

// ensureAgent24IngestJobs creates agent24_ingest_jobs in both modes: the integration process creates jobs and the
// standalone worker advances them. A failure is returned; must panics, so the process does not start. It starts
// no background work.
func ensureAgent24IngestJobs(db *gorm.DB) error {
	if err := repository.EnsureAgent24IngestJobTable(db); err != nil {
		return err
	}
	// A24-KB-42: the knowledge-entry tables; after the job table, whose principal column it widens
	return repository.EnsureAgent24CandidateTables(db)
}

// provideAgent24Ingest registers the internal ingest entry. The executor check goes through the asynq inspector
// when Redis is present, and is inline (standalone) or absent (integration) without it.
func provideAgent24Ingest(c *dig.Container, redisAvailable bool) {
	must(c.Provide(repository.NewAgent24IngestJobStore))
	must(c.Provide(repository.NewAgent24CandidateStore)) // A24-KB-42
	if redisAvailable {
		must(c.Provide(router.NewAgent24AsynqIngestExecutor))
	} else {
		must(c.Provide(router.NewAgent24LiteIngestExecutor))
	}
	must(c.Provide(service.NewAgent24Ingest))
	must(c.Provide(handler.NewAgent24IngestPolicy))
}

// installAgent24StageGate installs the W1.3 stage gate the document pipeline calls. It starts no background
// work: the gate runs inside the pipeline's own tasks.
func installAgent24StageGate(
	db *gorm.DB, jobs *repository.Agent24IngestJobStore, policy service.Agent24IngestPolicy,
) {
	service.SetAgent24StageGate(db, jobs, policy)
}

// installAgent24KBRemovalHook installs the A24-KB-48 W1.11/A14 hook that the upstream knowledge base delete and
// clear call first, in both modes (the processes share one database). It only stores the hook; it starts nothing.
func installAgent24KBRemovalHook(db *gorm.DB, audit interfaces.AuditLogService) {
	handler.InstallAgent24KBRemovalHook(db, audit)
}

// startAgent24IngestReconcile sweeps Agent24 ingest jobs once a minute in the process that runs the pipeline
// (standalone); integration mode skips it through invokeBackground, like every other background job.
// A24-KB-42: the same loop applies the retention of the knowledge-entry records (W8.9), which must run without
// any request.
func startAgent24IngestReconcile(
	jobs *repository.Agent24IngestJobStore, candidates *repository.Agent24CandidateStore,
	cleaner interfaces.ResourceCleaner,
) {
	r := service.NewAgent24JobReconciler(jobs)
	retention := service.NewAgent24CandidateRetention(candidates)
	stop := make(chan struct{})
	jobsDone := agent24EveryMinute(stop, "ingest job sweep", func(ctx context.Context) error {
		_, err := r.Sweep(ctx)
		return err
	})
	// A24-KB-43 (PR-Daemon on #156): the W8.9 purge has its own loop, so a slow or panicking job sweep never delays
	// it; with its 55-minute threshold the content is gone within 57 minutes (one tick plus one sweep deadline).
	retentionDone := agent24EveryMinute(stop, "candidate retention sweep", func(ctx context.Context) error {
		_, err := retention.Sweep(ctx)
		return err
	})
	cleaner.RegisterWithName("Agent24IngestReconcile", func() error {
		close(stop)
		for _, done := range []<-chan struct{}{jobsDone, retentionDone} {
			if !runtime.WaitFor(done, cleanupStepTimeout) {
				logger.Warnf(context.Background(),
					"[agent24] background sweep still running after %s; continuing shutdown", cleanupStepTimeout)
			}
		}
		return nil
	})
}

// agent24EveryMinute runs sweep once a minute, each run with its own agent24IngestReconcileTimeout deadline, until
// stop closes; the returned channel closes when the loop has ended. A failing or panicking run is logged (only
// the error's length and type) and the loop goes on.
func agent24EveryMinute(stop <-chan struct{}, name string, sweep func(context.Context) error) <-chan struct{} {
	done := make(chan struct{})
	once := func() {
		ctx, cancel := context.WithTimeout(context.Background(), agent24IngestReconcileTimeout)
		defer cancel()
		defer func() {
			if v := recover(); v != nil {
				logger.Warnf(ctx, "[agent24] %s panicked: value_type=%T", name, v)
			}
		}()
		if err := sweep(ctx); err != nil {
			logger.Warnf(ctx, "[agent24] %s failed: error_len=%d error_type=%T", name, len(fmt.Sprint(err)), err)
		}
	}
	go func() {
		defer close(done)
		ticker := time.NewTicker(agent24SweepInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				once()
			case <-stop:
				return
			}
		}
	}()
	return done
}
