// Agent24 fork product patch A24-KB-03, A24-KB-20 (CONTRACT-READONLY-v1 R3.4–R3.5, R7.1–R7.2, WeKnora#56).
//
// Execution error classification of the Agent24 knowledge integration
// endpoints. Raw error text never leaves this file.

package handler

import (
	"context"
	"errors"
	"io"
	"net"
	"net/url"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/models/api"
)

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
	// Storage, vector-store and Ollama clients return their network errors as
	// is (the single-group fast path passes the retriever's error through), so
	// a network failure there is a dependency failure too (R3.5).
	if failed, timedOut := agent24NetworkFailure(err); failed {
		if timedOut {
			return deadlineOr(), false
		}
		return agent24Unavailable("dependency"), false
	}
	return agent24Internal(), false
}

// agent24NetworkFailure reports whether err is a failure on the network (a
// refused or reset connection, a failed lookup, a TLS handshake timeout, a
// connection closed before the reply), and whether it timed out. A *url.Error
// is judged by its cause: it is a net.Error whatever the cause, including a
// URL that does not parse or a scheme no transport serves, and those never
// work on a retry (internal).
func agent24NetworkFailure(err error) (failed, timedOut bool) {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true, netErr.Timeout()
	}
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF), false
}
