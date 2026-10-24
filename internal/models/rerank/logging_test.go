package rerank

import (
	"fmt"
	"strings"
	"testing"
)

func TestBuildRerankRequestDebugOmitsText(t *testing.T) {
	const sentinel = "S3NT1NEL"
	query := "find " + sentinel
	documents := []string{"doc " + sentinel, "second"}

	got := buildRerankRequestDebug("model", "https://u:"+sentinel+"@example.test?key="+sentinel, query, documents)
	if strings.Contains(got, sentinel) {
		t.Fatalf("rerank log contains text: %s", got)
	}
	if !strings.Contains(got, fmt.Sprintf("query_len=%d", len(query))) {
		t.Fatalf("query byte length missing: %s", got)
	}
	if !strings.Contains(got, "documents=2") {
		t.Fatalf("document count missing: %s", got)
	}
	wantDocs := fmt.Sprintf("doc_bytes=[%d %d]", len(documents[0]), len(documents[1]))
	if !strings.Contains(got, wantDocs) {
		t.Fatalf("document byte lengths missing: %s", got)
	}
}
