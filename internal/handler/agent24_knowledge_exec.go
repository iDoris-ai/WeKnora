// Agent24 fork product patch A24-KB-03 (CONTRACT-READONLY-v1 R3.3, R7.1–R7.2).
//
// Deadline clamping and execution error classification of the Agent24
// knowledge integration endpoints. Raw error text never leaves this file.

package handler

import (
	"context"
	"errors"
	"time"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/models/api"
)

// agent24KnowledgeDeadline returns the applied deadline: requestedMS <= 0
// means the server maximum; larger values are clamped without error (R3.3).
func agent24KnowledgeDeadline(requestedMS int64) (time.Duration, int) {
	ms := int64(agent24KnowledgeMaxDeadlineMS)
	if requestedMS > 0 && requestedMS < ms {
		ms = requestedMS
	}
	return time.Duration(ms) * time.Millisecond, int(ms)
}

// classifyAgent24KnowledgeExecError maps an execution error to the envelope.
// clientGone means the caller aborted and no response must be written (R7.1).
// An explicit non-deadline cause wins and is not overridden by an expired ctx;
// timeout means only the caller's deadline (CONTRACT-READONLY-v1 §6, R3.4).
// Known limit: the fan-out does not keep per-branch causes, so "all branches
// hit the deadline" and "dependency failure mixed with expiry" are not
// distinguishable here; partial results and branch causes are a later slice.
func classifyAgent24KnowledgeExecError(ctx context.Context, err error) (*agent24KnowledgeError, bool) {
	if errors.Is(ctx.Err(), context.Canceled) {
		return nil, true
	}
	ctxExpired := errors.Is(ctx.Err(), context.DeadlineExceeded)
	deadlineOr := func() *agent24KnowledgeError {
		if ctxExpired {
			return agent24Timeout()
		}
		return agent24Unavailable("dependency")
	}
	var appErr *apperrors.AppError
	if errors.As(err, &appErr) && appErr != nil {
		switch appErr.Code {
		case apperrors.ErrTooManyRequests:
			return agent24Unavailable("overloaded"), false
		case apperrors.ErrServiceUnavailable:
			return agent24Unavailable("dependency"), false
		case apperrors.ErrVectorStoreUnavailable, apperrors.ErrTimeout:
			// The upstream fan-out folds an expired request deadline into
			// ErrVectorStoreUnavailable (knowledgebase_search_fanout.go), so
			// these are a timeout only when the request ctx has expired.
			return deadlineOr(), false
		}
		return agent24Internal(), false
	}
	var httpErr *api.HTTPError
	if errors.As(err, &httpErr) && httpErr != nil {
		switch {
		case httpErr.StatusCode == 429:
			return agent24Unavailable("overloaded"), false
		case httpErr.StatusCode >= 500:
			return agent24Unavailable("dependency"), false
		}
		return agent24Internal(), false
	}
	// An inner HTTP timeout or child-context expiry is a dependency failure,
	// not the caller's deadline.
	var transportErr *api.TransportError
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &transportErr) {
		return deadlineOr(), false
	}
	return agent24Internal(), false
}
