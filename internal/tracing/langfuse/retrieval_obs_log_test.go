package langfuse

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

func TestRetrievalObsOmitsHitText(t *testing.T) {
	const sentinel = "S3NT1NEL"
	content := "prefix " + sentinel + " suffix"

	retrieved := SummarizeRetrieveOutput([]*types.RetrieveResult{{
		RetrieverType: types.VectorRetrieverType,
		Results: []*types.IndexWithScore{{
			ChunkID: "c1", Content: content, Score: 0.5,
		}},
	}})
	blob := fmt.Sprintf("%#v", retrieved)
	if strings.Contains(blob, sentinel) {
		t.Fatalf("trace attributes contain hit text: %s", blob)
	}

	hits := retrieved["top_hits"].([]map[string]interface{})
	if hits[0]["content_bytes"] != len(content) {
		t.Fatalf("top_hits content_bytes = %v, want %d", hits[0]["content_bytes"], len(content))
	}
	groups := retrieved["top_hits_by_retriever"].([]map[string]interface{})
	groupHits := groups[0]["top_hits"].([]map[string]interface{})
	if groupHits[0]["content_bytes"] != len(content) {
		t.Fatalf("per-retriever content_bytes = %v, want %d", groupHits[0]["content_bytes"], len(content))
	}
}
