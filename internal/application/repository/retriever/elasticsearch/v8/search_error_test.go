package v8

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	typesLocal "github.com/Tencent/WeKnora/internal/types"
	"github.com/elastic/go-elasticsearch/v8"
	"github.com/stretchr/testify/require"
)

func TestRetrieveErrorOmitsReason(t *testing.T) {
	const sentinel = "S3NT1NEL"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("X-Elastic-Product", "Elasticsearch")
		if r.URL.Path == "/" {
			_, _ = w.Write([]byte(`{"version":{"number":"8.11.0","build_flavor":"default"}}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"type":"query_shard_exception",` +
			`"reason":"failed to create query: ` + sentinel + `"},"status":400}`))
	}))
	defer server.Close()
	client, err := elasticsearch.NewTypedClient(
		elasticsearch.Config{Addresses: []string{server.URL}, DisableRetry: true},
	)
	require.NoError(t, err)
	repo := &elasticsearchRepository{client: client, index: "vectors"}
	params := typesLocal.RetrieveParams{Query: "find " + sentinel, Embedding: []float32{1, 0}, TopK: 3}

	_, err = repo.KeywordsRetrieve(context.Background(), params)
	require.EqualError(t, err, "status: 400, failed: [query_shard_exception], reason: ")
	_, err = repo.VectorRetrieve(context.Background(), params)
	require.EqualError(t, err, "status: 400, failed: [query_shard_exception], reason: ")
}
