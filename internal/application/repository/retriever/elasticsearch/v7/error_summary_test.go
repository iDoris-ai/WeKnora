package v7

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/elastic/go-elasticsearch/v7/esapi"
)

func TestProcessSearchResponseErrorOmitsBody(t *testing.T) {
	const sentinel = "S3NT1NEL"
	body := `{"error":{"type":"query_shard_exception",` +
		`"reason":"failed to create query: {` + sentinel + `}"},"status":400}`
	resp := &esapi.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(body))}

	repo := &elasticsearchRepository{}
	_, err := repo.processSearchResponse(context.Background(), resp, types.KeywordsRetrieverType)
	if err == nil {
		t.Fatal("want an error for a failed search")
	}
	if got := err.Error(); got != "failed to retrieve: status=400 error_type=query_shard_exception" {
		t.Fatalf("error = %q", got)
	}
}
