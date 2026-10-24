package yuque

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/logger"
)

// The client's errors carry the response body, so a caller logging them
// with %v would put the vendor's text back into the log.
func TestListResourcesGroupFailureLogOmitsResponseBody(t *testing.T) {
	const sentinel = "S3NT1NEL"
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	logger.SetLogLevel(logger.LevelDebug)
	t.Cleanup(logger.ConfigureFromEnv)

	f := newFakeYuque()
	defer f.Close()
	f.mux = http.NewServeMux()
	f.server.Config.Handler = f.mux
	f.handleJSON("/api/v2/user", 200, v2UserResponse{Data: v2User{ID: 1, Login: "alice"}})
	f.handleJSON("/api/v2/users/alice/repos", 200, v2RepoListResponse{Data: []v2Repo{
		{ID: 10, Slug: "personal", Name: "Personal", Type: "Book", Namespace: "alice/personal"},
	}})
	f.handleJSON("/api/v2/users/1/groups", 404, map[string]string{"message": sentinel})

	if _, err := NewConnector().ListResources(context.Background(), makeDSConfig(f, nil), ""); err != nil {
		t.Fatalf("ListResources: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, sentinel) {
		t.Fatalf("log carries the response body: %s", out)
	}
	if !strings.Contains(out, "list user groups failed (treating as empty): error_len=") {
		t.Fatalf("log lacks the group failure line: %s", out)
	}
}
