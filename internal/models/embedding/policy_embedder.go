// Agent24 fork product patch A24-KB-17 (CONTRACT-READONLY-v1 r10 R3.11,
// §6 policy_denied, WeKnora#42).
//
// policyEmbedder is the innermost embedding decorator. It checks the
// processing guard on the context before any provider call. A context with
// no guard (standalone mode) calls through.

package embedding

import (
	"context"
	"errors"
	"fmt"
	"strings"

	modelruntime "github.com/Tencent/WeKnora/internal/models/runtime"
	"github.com/Tencent/WeKnora/internal/models/utils/ollama"
	"github.com/Tencent/WeKnora/internal/types"
)

// policyEmbedder guards one already-resolved destination.
type policyEmbedder struct {
	inner   Embedder
	dest    types.ProcessingDestination
	destErr error
}

// authorize asks the guard and, on a pass, returns ctx with the approved
// endpoint recorded: api.Endpoint checks every request it sends under the
// guard (each split request, each retry, each redirect) against it.
func (p *policyEmbedder) authorize(ctx context.Context) (context.Context, error) {
	guard := types.ProcessingGuardFromContext(ctx)
	if guard == nil {
		return ctx, nil
	}
	if p.destErr != nil {
		return ctx, fmt.Errorf("%w", types.ErrProcessingDenied)
	}
	if err := guard.Allow(ctx, p.dest); err != nil {
		return ctx, err
	}
	return types.WithProcessingApprovedEndpoint(ctx, p.dest.Endpoint), nil
}

func (p *policyEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	ctx, err := p.authorize(ctx)
	if err != nil {
		return nil, err
	}
	return p.inner.Embed(ctx, text)
}

func (p *policyEmbedder) BatchEmbed(ctx context.Context, texts []string) ([][]float32, error) {
	ctx, err := p.authorize(ctx)
	if err != nil {
		return nil, err
	}
	return p.inner.BatchEmbed(ctx, texts)
}

// BatchEmbedWithPool threads the caller's model down unchanged: the outer concurrencyEmbedder passes itself so
// the pooler's per-sub-batch calls go through its gate, and that gate calls this BatchEmbed, which checks the
// guard. Every model above sits on top of this wrapper, so the guard is never skipped.
func (p *policyEmbedder) BatchEmbedWithPool(ctx context.Context, model Embedder, texts []string) ([][]float32, error) {
	if model == nil {
		model = p
	}
	return p.inner.BatchEmbedWithPool(ctx, model, texts)
}

func (p *policyEmbedder) AcceptsImages() bool {
	_, ok := AsImageEmbedder(p.inner)
	return ok
}

func (p *policyEmbedder) ImageLimits() ImageLimits { return imageLimitsOf(p.inner) }

func (p *policyEmbedder) BatchEmbedImages(ctx context.Context, images []Image) ([][]float32, error) {
	ctx, err := p.authorize(ctx)
	if err != nil {
		return nil, err
	}
	inner, err := imageSide(p.inner)
	if err != nil {
		return nil, err
	}
	return inner.BatchEmbedImages(ctx, images)
}

func (p *policyEmbedder) GetModelName() string { return p.inner.GetModelName() }
func (p *policyEmbedder) GetDimensions() int   { return p.inner.GetDimensions() }
func (p *policyEmbedder) GetModelID() string   { return p.inner.GetModelID() }

// ProcessingDestinationForConfig is where an embedder built from config will send text, computed without
// credentials or network access (no DNS: construction's SSRF check resolves the host, so a pre-check must not
// build the embedder). Remote: the vendor Endpoint URL when it sets one, otherwise the resolved base URL
// (a vendor default for an empty BaseURL), with the remote model name the client sends. Local: the Ollama
// service base URL (svc, or OLLAMA_BASE_URL when svc is nil) and the model the Ollama embedder uses.
func ProcessingDestinationForConfig(config Config, svc *ollama.OllamaService) (types.ProcessingDestination, error) {
	var raw, model string
	switch strings.ToLower(string(config.Source)) {
	case string(types.ModelSourceRemote):
		if strings.TrimSpace(config.ModelName) == "" {
			return types.ProcessingDestination{}, errors.New("embedding processing destination is unavailable")
		}
		resolved, err := modelruntime.Resolve(remoteEmbeddingRef(config))
		if err != nil {
			return types.ProcessingDestination{}, errors.New("embedding processing destination is unavailable")
		}
		// A configured field named "model" would overwrite the model the guard judged in the request body
		// (the OpenAI-shape client writes these after "model"; ExtraBody cannot, it only fills absent keys).
		if s := resolved.Embeddings; s.DimensionsField == "model" || s.InputTypeField == "model" ||
			s.TruncateField == "model" {
			return types.ProcessingDestination{}, errors.New("embedding processing destination is unavailable")
		}
		raw, model = resolved.EmbeddingDialTarget(config.ExtraConfig)
	case string(types.ModelSourceLocal):
		raw = ollama.BaseURLFromEnv()
		if svc != nil {
			raw = svc.BaseURL()
		}
		model = config.ModelName
		if model == "" {
			model = defaultOllamaEmbeddingModel
		}
	default:
		return types.ProcessingDestination{}, errors.New("embedding processing destination is unavailable")
	}
	endpoint, err := types.NormalizeProcessingEndpoint(raw)
	if err != nil {
		return types.ProcessingDestination{}, err
	}
	return types.ProcessingDestination{Category: types.ProcessingEmbedding, Endpoint: endpoint, Model: model}, nil
}
