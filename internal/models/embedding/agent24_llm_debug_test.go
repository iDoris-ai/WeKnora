// Agent24 fork product patch A24-KB-29 (WeKnora#42).
package embedding

import (
	"context"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

type llmDebugGuard struct{}

func (llmDebugGuard) Allow(context.Context, types.ProcessingDestination) error { return nil }

// Under a processing guard the debug record keeps the input lengths only; without one it keeps the preview.
func TestEmbeddingDebugInputOmitsTextUnderGuard(t *testing.T) {
	inputs := []string{"S3NT1NEL query", "第二段 S3NT1NEL"}
	guarded := embeddingDebugInput(types.WithProcessingGuard(context.Background(), llmDebugGuard{}), inputs)
	if strings.Contains(guarded, "S3NT1NEL") || !strings.Contains(guarded, "[0] (len=14)") ||
		!strings.Contains(guarded, "[1] (len=12)") || !strings.Contains(guarded, "count=2") {
		t.Fatalf("guarded: %q", guarded)
	}
	plain := embeddingDebugInput(context.Background(), inputs)
	if strings.Count(plain, "S3NT1NEL") != 2 {
		t.Fatalf("standalone keeps the preview: %q", plain)
	}
}
