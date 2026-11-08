// Agent24 fork product patch A24-KB-41 (KB-C4-04, WeKnora#136): who runs an Agent24 ingest job, and the
// fail-closed check the internal ingest entry makes before it writes anything.

package router

import (
	"context"
	"errors"

	"github.com/hibiken/asynq"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/handler"
	"github.com/Tencent/WeKnora/internal/types"
)

var (
	errAgent24NoIngestWorker = errors.New("no live asynq server consumes the default queue")
	errAgent24NoIngestQueue  = errors.New("integration mode without Redis has no worker for ingest jobs")
)

// agent24AsynqIngestExecutor finds a live asynq server that consumes the document queue. ListServers only returns
// servers whose heartbeat has not expired, so a listed, active server is a running worker.
type agent24AsynqIngestExecutor struct {
	servers func() ([]*asynq.ServerInfo, error)
}

func (e agent24AsynqIngestExecutor) Available(context.Context) error {
	list, err := e.servers()
	if err != nil {
		return err
	}
	for _, s := range list {
		if s != nil && s.Status == "active" && s.Queues[types.QueueDefault] > 0 {
			return nil
		}
	}
	return errAgent24NoIngestWorker
}

// agent24InlineIngestExecutor is Lite standalone mode: the in-process executor runs the task itself.
type agent24InlineIngestExecutor struct{}

func (agent24InlineIngestExecutor) Available(context.Context) error { return nil }

// agent24NoIngestExecutor is integration mode without Redis: the process runs no background work (A24-KB-30)
// and there is no queue another process could take the job from.
type agent24NoIngestExecutor struct{}

func (agent24NoIngestExecutor) Available(context.Context) error { return errAgent24NoIngestQueue }

// NewAgent24AsynqIngestExecutor checks for a live worker through the asynq inspector (Redis present).
func NewAgent24AsynqIngestExecutor(inspector *asynq.Inspector) service.Agent24IngestExecutor {
	return agent24AsynqIngestExecutor{servers: inspector.Servers}
}

// NewAgent24LiteIngestExecutor is the executor without Redis: inline in standalone, none in integration mode
// (and none when the mode cannot be read; startup refuses that mode later).
func NewAgent24LiteIngestExecutor() service.Agent24IngestExecutor {
	mode, err := handler.Agent24KnowledgeModeFromEnv()
	if err != nil || mode == handler.Agent24KnowledgeModeIntegration {
		return agent24NoIngestExecutor{}
	}
	return agent24InlineIngestExecutor{}
}
