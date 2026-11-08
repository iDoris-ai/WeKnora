// Agent24 fork product patch A24-KB-41 (WeKnora#136).
package router

import (
	"context"
	"errors"
	"testing"

	"github.com/hibiken/asynq"
	"github.com/stretchr/testify/require"

	"github.com/Tencent/WeKnora/internal/handler"
)

func TestAgent24AsynqIngestExecutorNeedsAnActiveDefaultQueueServer(t *testing.T) {
	ctx := context.Background()
	exec := func(list []*asynq.ServerInfo, err error) error {
		servers := func() ([]*asynq.ServerInfo, error) { return list, err }
		return agent24AsynqIngestExecutor{servers: servers}.Available(ctx)
	}
	require.ErrorIs(t, exec(nil, nil), errAgent24NoIngestWorker, "no server")
	require.ErrorIs(t, exec([]*asynq.ServerInfo{nil}, nil), errAgent24NoIngestWorker)
	require.ErrorIs(t, exec([]*asynq.ServerInfo{
		{Status: "active", Queues: map[string]int{"postprocess": 3}},
		{Status: "stopped", Queues: map[string]int{"default": 6}},
		{Status: "active", Queues: map[string]int{"default": 0}},
	}, nil), errAgent24NoIngestWorker, "no active server consumes default")
	require.NoError(t, exec([]*asynq.ServerInfo{
		{Status: "active", Queues: map[string]int{"postprocess": 3}},
		{Status: "active", Queues: map[string]int{"default": 6}},
	}, nil))
	boom := errors.New("redis down")
	require.ErrorIs(t, exec(nil, boom), boom, "an inspector error is not availability")
}

func TestAgent24LiteIngestExecutorByMode(t *testing.T) {
	ctx := context.Background()
	t.Setenv(handler.Agent24KnowledgeModeEnv, "integration")
	require.ErrorIs(t, NewAgent24LiteIngestExecutor().Available(ctx), errAgent24NoIngestQueue)
	t.Setenv(handler.Agent24KnowledgeModeEnv, "bogus")
	require.ErrorIs(t, NewAgent24LiteIngestExecutor().Available(ctx), errAgent24NoIngestQueue)
	t.Setenv(handler.Agent24KnowledgeModeEnv, "")
	require.NoError(t, NewAgent24LiteIngestExecutor().Available(ctx))
}
