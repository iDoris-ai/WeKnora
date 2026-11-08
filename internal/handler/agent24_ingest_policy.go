// Agent24 fork product patch A24-KB-41 (KB-C4-04, WeKnora#136, CONTRACT-OPS-v2 W1.3 ③): the processing-policy
// check of the ingest stage gate, over the same policy and destination rules the integration search uses
// (A24-KB-27, A24-KB-29). The standalone worker runs it, so standalone must load the same policy file as the
// integration process; without one the default policy denies every destination and Agent24 jobs fail with
// policy_denied.

package handler

import (
	"context"
	"errors"

	"github.com/Tencent/WeKnora/internal/application/service"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type agent24IngestPolicy struct {
	destinations agent24ModelEmbeddingDestinations
	policy       func() (*agent24ProcessingPolicy, error)
}

// NewAgent24IngestPolicy builds the policy check over the model repository.
func NewAgent24IngestPolicy(models interfaces.ModelRepository) service.Agent24IngestPolicy {
	return agent24IngestPolicy{
		destinations: agent24ModelEmbeddingDestinations{models: models},
		policy:       agent24ProcessingPolicyFromEnv,
	}
}

// AllowIngest refuses a knowledge base bound to a tenant vector store (A24-KB-29: only the deployment's own
// engine), an unreadable policy, and an embedding destination the policy does not list. A model row that
// cannot be read is an error the caller retries, not a refusal.
func (p agent24IngestPolicy) AllowIngest(ctx context.Context, kb *types.KnowledgeBase) error {
	if kb == nil {
		return service.ErrAgent24PolicyDenied
	}
	if kb.HasVectorStore() {
		return service.ErrAgent24PolicyDenied
	}
	policy, err := p.policy()
	if err != nil || policy == nil {
		return service.ErrAgent24PolicyDenied
	}
	dest, ok, err := p.destinations.QueryEmbedding(ctx, []*types.KnowledgeBase{kb})
	if err != nil {
		return err
	}
	if !ok {
		// no embedding model: the pipeline calls no embedding destination
		return nil
	}
	if err := policy.Allow(ctx, dest); err != nil {
		if errors.Is(err, types.ErrProcessingDenied) {
			return service.ErrAgent24PolicyDenied
		}
		return err
	}
	return nil
}
