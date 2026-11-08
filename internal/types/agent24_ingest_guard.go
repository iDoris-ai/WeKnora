// Agent24 fork product patch A24-KB-41 (KB-C4-04, WeKnora#136, CONTRACT-OPS-v2 W1.3 ③): the processing guard of
// an Agent24 ingest's embedding calls. The ingest worker puts it on the task context; the embedding decorator
// (policyEmbedder) installs it as the processing guard for each embedding call only, so the actual destination is
// checked at call time and a redirect away from the approved endpoint is refused unsent, as on the search path
// (A24-KB-17, A24-KB-21). Other model calls of the same task (LLM, VLM) do not see it.

package types

import "context"

type processingEmbeddingGuardKey struct{}

// WithProcessingEmbeddingGuard returns a context whose embedding calls run under g.
func WithProcessingEmbeddingGuard(ctx context.Context, g ProcessingGuard) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, processingEmbeddingGuardKey{}, g)
}

// ProcessingEmbeddingGuardFromContext returns the embedding-only guard on ctx, or nil.
func ProcessingEmbeddingGuardFromContext(ctx context.Context) ProcessingGuard {
	if ctx == nil {
		return nil
	}
	g, _ := ctx.Value(processingEmbeddingGuardKey{}).(ProcessingGuard)
	return g
}
