// Agent24 fork product patch A24-KB-30 (KB-SVC-POLICY-01 P-c, WeKnora#42).
//
// In AGENT24_KNOWLEDGE_MODE=integration the process serves only the allowlisted
// read routes and runs no background work. It shares the database (and, in
// distributed mode, Redis) with the standalone process that owns ingestion, so
// an integration worker would pick up standalone's jobs and process them
// outside the processing policy, or under it and fail them. Every step that
// starts a task consumer, a scheduler, a reaper, a recovery or an IM channel
// goes through invokeBackground and is skipped here; resetPendingTasks, which
// rewrites rows standalone may be processing, is skipped too. The A24-KB-22
// revocation purge stays: it runs only in integration mode, on the fork's own
// table.

package container

import (
	"context"

	"github.com/Tencent/WeKnora/internal/application/service"
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

// startAgentRuntimes starts upstream v0.9.0's delegated-run (collaboration)
// runtime and automation scheduler. Upstream starts both at the end of the
// step that wires them; here the wiring stays a foreground Invoke and the
// start goes through invokeBackground, so integration mode leaves both stopped.
// The AgentCollaboration cleanup step calls Runtime.Stop, which returns at
// once for a runtime that never started.
func startAgentRuntimes(collaboration *service.AgentCollaboration, automations *service.AutomationService) {
	collaboration.Runtime.Start(context.Background())
	automations.Start(context.Background())
}
