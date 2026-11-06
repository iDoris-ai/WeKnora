// Agent24 fork product patch A24-KB-30 (KB-SVC-POLICY-01 P-c, WeKnora#42).
//
// In AGENT24_KNOWLEDGE_MODE=integration the process serves only the allowlisted
// read routes and runs no background work. It shares the database (and, in
// distributed mode, Redis) with the standalone process that owns ingestion, so
// an integration worker would pick up standalone's jobs and process them
// outside the processing policy, or under it and fail them. Every step that
// starts a task consumer, a scheduler, a reaper, a recovery or an IM channel
// goes through invokeBackground and is skipped here; resetPendingTasks, which
// rewrites rows standalone may be processing, is skipped too.

package container

import (
	"context"

	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/logger"
	"go.uber.org/dig"
)

// agent24BackgroundJobsDisabled reports whether this process must not run
// background work: integration mode, or a mode value that cannot be read
// (startup refuses it later; until then nothing is started).
func agent24BackgroundJobsDisabled() bool {
	mode, err := handler.Agent24KnowledgeModeFromEnv()
	return err != nil || mode == handler.Agent24KnowledgeModeIntegration
}

// invokeBackground invokes fn, a step that starts background work, unless
// agent24BackgroundJobsDisabled. A skipped step is logged by name.
func invokeBackground(c *dig.Container, name string, fn any) {
	if agent24BackgroundJobsDisabled() {
		logger.Infof(context.Background(),
			"[Container] agent24 integration mode: background job %s not started", name)
		return
	}
	must(c.Invoke(fn))
}
