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

// classifyAgent24KnowledgeExecError: clientGone means the caller aborted, write
// nothing (R7.1). Explicit non-deadline causes win over an expired ctx; timeout
// means only the caller's deadline (§6, R3.4). The fan-out keeps no per-branch
// causes, so mixed failures are not separable here (partial: a later slice).
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
	// A deadline error is the caller's deadline only if the request ctx expired;
	// otherwise it is an inner HTTP or child-context timeout (a dependency).
	if errors.Is(err, context.DeadlineExceeded) {
		return deadlineOr(), false
	}
	// Any other transport failure (refused, reset) is a dependency failure.
	var transportErr *api.TransportError
	if errors.As(err, &transportErr) {
		return agent24Unavailable("dependency"), false
	}
	return agent24Internal(), false
}
