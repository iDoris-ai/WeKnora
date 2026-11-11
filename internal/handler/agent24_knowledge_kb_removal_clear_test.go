package handler

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/types"
)

// A24-KB-48 (W1.11/A14) on the clear route (Q2-A): the hook runs after the move check and before both the empty
// answer and the enqueue; a failing hook clears nothing.

type agent24S4CClearHook struct {
	queue       *documentDeleteEnqueuer
	err         error
	calls       int
	tenant      uint64
	kbID        string
	withinNil   bool
	queuedFirst bool // the clear task was already enqueued when the hook ran
}

func (h *agent24S4CClearHook) BeforeRemoval(
	_ context.Context, tenantID uint64, kbID string, within func(tx *gorm.DB) error,
) error {
	h.calls++
	h.tenant, h.kbID, h.withinNil = tenantID, kbID, within == nil
	h.queuedFirst = h.queue.task != nil
	return h.err
}

func agent24S4CClear(
	t *testing.T, rows []*types.Knowledge, hookErr error,
) (*agent24S4CClearHook, *documentDeleteEnqueuer, int) {
	t.Helper()
	queue := &documentDeleteEnqueuer{}
	hook := &agent24S4CClearHook{queue: queue, err: hookErr}
	service.SetAgent24KBRemovalHook(hook)
	t.Cleanup(func() { service.SetAgent24KBRemovalHook(nil) })
	h := &KnowledgeHandler{
		cfg:         transferHandlerConfig(),
		kbService:   admissionKB("user"),
		kgService:   &mutationAdmissionKnowledge{rows: rows},
		asynqClient: queue,
	}
	r := documentHandlerRouter()
	r.DELETE("/:id", h.ClearKnowledgeBaseContents)
	w := mutationRequest(r, http.MethodDelete, "/kb", "")
	return hook, queue, w.Code
}

func TestAgent24S4CClearRunsHookBeforeEnqueue(t *testing.T) {
	hook, queue, code := agent24S4CClear(t, []*types.Knowledge{admissionKnowledge(false)}, nil)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, 1, hook.calls)
	assert.Equal(t, uint64(7), hook.tenant)
	assert.Equal(t, "kb", hook.kbID)
	assert.True(t, hook.withinNil, "a clear has nothing to put in the hook's transaction")
	assert.False(t, hook.queuedFirst, "the hook runs before the enqueue")
	assert.NotNil(t, queue.task)
}

func TestAgent24S4CClearEmptyBaseStillRunsHook(t *testing.T) {
	hook, queue, code := agent24S4CClear(t, nil, nil)
	require.Equal(t, http.StatusOK, code)
	assert.Equal(t, 1, hook.calls, "an empty base can still be the target of submitted candidates")
	assert.Nil(t, queue.task)
}

func TestAgent24S4CClearRefusedRunsNoHook(t *testing.T) {
	hook, queue, code := agent24S4CClear(t, []*types.Knowledge{admissionKnowledge(true)}, nil)
	require.Equal(t, http.StatusConflict, code)
	assert.Zero(t, hook.calls, "a refused clear changes nothing")
	assert.Nil(t, queue.task)
}

func TestAgent24S4CClearHookFailsClearsNothing(t *testing.T) {
	for name, rows := range map[string][]*types.Knowledge{
		"non-empty": {admissionKnowledge(false)},
		"empty":     nil,
	} {
		t.Run(name, func(t *testing.T) {
			hook, queue, code := agent24S4CClear(t, rows, errors.New("hook failed"))
			assert.Equal(t, http.StatusInternalServerError, code)
			assert.Equal(t, 1, hook.calls)
			assert.Nil(t, queue.task)
		})
	}
}

// The container's installer puts a hook on the service (both modes).
func TestAgent24S4CInstallSetsTheServiceHook(t *testing.T) {
	service.SetAgent24KBRemovalHook(nil)
	t.Cleanup(func() { service.SetAgent24KBRemovalHook(nil) })
	e := newAgent24CandidateEnv(t)
	InstallAgent24KBRemovalHook(e.db, nil)
	_, ok := service.Agent24KBRemovalHookInstalled().(*agent24KBRemoval)
	assert.True(t, ok)
}
