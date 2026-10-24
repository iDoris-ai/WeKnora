package rerank

import (
	"fmt"

	secutils "github.com/Tencent/WeKnora/internal/utils"
)

func buildRerankRequestDebug(model, endpoint, query string, documents []string) string {
	docBytes := make([]int, len(documents))
	for i, doc := range documents {
		docBytes[i] = len(doc)
	}
	return fmt.Sprintf(
		"rerank request endpoint=%s model=%s query_len=%d documents=%d doc_bytes=%v",
		secutils.RedactURL(endpoint),
		model,
		len(query),
		len(documents),
		docBytes,
	)
}
