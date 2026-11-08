// Agent24 fork product patch A24-KB-41 (KB-C4-04, WeKnora#136).

package container

import (
	"go.uber.org/dig"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/application/repository"
	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/router"
)

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
}
