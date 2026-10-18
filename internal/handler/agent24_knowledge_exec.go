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
func classifyAgent24KnowledgeExecError(ctx context.Context, err error) (*agent24KnowledgeError, bool) {
	if errors.Is(ctx.Err(), context.Canceled) {
		return nil, true
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return agent24Timeout(), false
	}
	var appErr *apperrors.AppError
	if errors.As(err, &appErr) && appErr != nil {
		switch appErr.Code {
		case apperrors.ErrVectorStoreUnavailable, apperrors.ErrServiceUnavailable:
			return agent24Unavailable("dependency"), false
		case apperrors.ErrTimeout:
			return agent24Timeout(), false
		case apperrors.ErrTooManyRequests:
			return agent24Unavailable("overloaded"), false
		}
	}
	return agent24Internal(), false
}
