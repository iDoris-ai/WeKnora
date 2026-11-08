// Agent24 fork product patch A24-KB-41 (KB-C4-04, WeKnora#136).

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

// ensureAgent24IngestJobs creates agent24_ingest_jobs in both modes: the integration process creates jobs and the
// standalone worker advances them. A failure is returned; must panics, so the process does not start. It starts
// no background work.
func ensureAgent24IngestJobs(db *gorm.DB) error {
	return repository.EnsureAgent24IngestJobTable(db)
}

// provideAgent24Ingest registers the internal ingest entry. The executor check goes through the asynq inspector
// when Redis is present, and is inline (standalone) or absent (integration) without it.
func provideAgent24Ingest(c *dig.Container, redisAvailable bool) {
	must(c.Provide(repository.NewAgent24IngestJobStore))
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

// startAgent24IngestReconcile sweeps Agent24 ingest jobs once a minute in the process that runs the pipeline
// (standalone); integration mode skips it through invokeBackground, like every other background job.
func startAgent24IngestReconcile(jobs *repository.Agent24IngestJobStore, cleaner interfaces.ResourceCleaner) {
	r := service.NewAgent24JobReconciler(jobs)
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		sweep := func() {
			ctx, cancel := context.WithTimeout(context.Background(), agent24IngestReconcileTimeout)
			defer cancel()
			if _, err := r.Sweep(ctx); err != nil {
				logger.Warnf(ctx, "[agent24] ingest job sweep failed: error_len=%d error_type=%T",
					len(fmt.Sprint(err)), err)
			}
		}
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				sweep()
			case <-stop:
				return
			}
		}
	}()
	cleaner.RegisterWithName("Agent24IngestReconcile", func() error {
		close(stop)
		if !runtime.WaitFor(done, cleanupStepTimeout) {
			logger.Warnf(context.Background(),
				"[agent24] ingest job sweep still running after %s; continuing shutdown", cleanupStepTimeout)
		}
		return nil
	})
}
