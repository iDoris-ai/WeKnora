// Agent24 fork product patch A24-KB-09 (CONTRACT-READONLY-v1 r10 R3.4, R3.5): a retrieval that kept
// results despite a store-level partial failure leaves a mark on a recorder carried by the context.

package types

import (
	"context"
	"sync/atomic"
)

type retrievalDegradedKey struct{}

// WithRetrievalDegradedRecorder returns a context carrying a fresh recorder and a function that reports
// whether any retrieval under that context was marked degraded. Safe for concurrent fan-out.
func WithRetrievalDegradedRecorder(ctx context.Context) (context.Context, func() bool) {
	var degraded atomic.Bool
	return context.WithValue(ctx, retrievalDegradedKey{}, &degraded), degraded.Load
}

// MarkRetrievalDegraded marks the recorder on ctx, if any; without one it does nothing.
func MarkRetrievalDegraded(ctx context.Context) {
	if degraded, ok := ctx.Value(retrievalDegradedKey{}).(*atomic.Bool); ok {
		degraded.Store(true)
	}
}
