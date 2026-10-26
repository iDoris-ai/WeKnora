package notion

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	secutils "github.com/Tencent/WeKnora/internal/utils"
)

func allowNotionTestServer(t *testing.T) {
	t.Helper()
	t.Setenv("SSRF_WHITELIST", "127.0.0.1,localhost")
	secutils.ResetSSRFWhitelistForTest()
	t.Cleanup(secutils.ResetSSRFWhitelistForTest)
}

func makeNotionConfig(cfg *Config, baseURL string, resourceIDs []string) *types.DataSourceConfig {
	return &types.DataSourceConfig{
		Type: types.ConnectorTypeNotion,
		Credentials: map[string]interface{}{
			"api_key": cfg.APIKey,
		},
		ResourceIDs: resourceIDs,
		Settings: map[string]interface{}{
			"base_url": baseURL,
		},
	}
}

func TestConnectorType(t *testing.T) {
	c := NewConnector()
	if c.Type() != types.ConnectorTypeNotion {
		t.Errorf("Type() = %q, want %q", c.Type(), types.ConnectorTypeNotion)
	}
}

func TestConnectorValidate(t *testing.T) {
	allowNotionTestServer(t)
	ts, cfg := fakeNotion()
	defer ts.Close()

	c := NewConnector()
	err := c.Validate(context.Background(), makeNotionConfig(cfg, ts.URL, nil))
	if err != nil {
		t.Fatalf("Validate() error: %v", err)
	}
}

func TestConnectorValidate_BadToken(t *testing.T) {
	ts, _ := fakeNotion()
	defer ts.Close()

	c := NewConnector()
	err := c.Validate(context.Background(), makeNotionConfig(
		&Config{APIKey: "bad-token"}, ts.URL, nil,
	))
	if err == nil {
		t.Fatal("expected error for bad token")
	}
}

func TestConnectorListResources(t *testing.T) {
	allowNotionTestServer(t)
	ts, cfg := fakeNotion()
	defer ts.Close()

	c := NewConnector()
	resources, err := c.ListResources(context.Background(), makeNotionConfig(cfg, ts.URL, nil), "")
	if err != nil {
		t.Fatalf("ListResources() error: %v", err)
	}

	if len(resources) != 2 {
		t.Fatalf("expected 2 resources, got %d", len(resources))
	}
	if resources[0].ExternalID != "page-1" || resources[0].Type != "page" || resources[0].Name != "Test Page" {
		t.Errorf("resource[0] = %+v", resources[0])
	}
	if resources[1].ExternalID != "db-1" || resources[1].Type != "database" || resources[1].Name != "Test Database" {
		t.Errorf("resource[1] = %+v, want Name=%q", resources[1], "Test Database")
	}
}

func TestConnectorFetchAll(t *testing.T) {
	allowNotionTestServer(t)
	ts, cfg := fakeNotion()
	defer ts.Close()

	c := NewConnector()
	items, err := c.FetchAll(context.Background(), makeNotionConfig(cfg, ts.URL, []string{"page-1"}), []string{"page-1"})
	if err != nil {
		t.Fatalf("FetchAll() error: %v", err)
	}

	if len(items) == 0 {
		t.Fatal("expected at least 1 item")
	}

	// First item should be the page with markdown content
	found := false
	for _, item := range items {
		if item.ExternalID == "page-1" {
			found = true
			if item.ContentType != "text/markdown" {
				t.Errorf("ContentType = %q", item.ContentType)
			}
			if item.Metadata["channel"] != "notion" {
				t.Errorf("channel = %q", item.Metadata["channel"])
			}
			if len(item.Content) == 0 {
				t.Error("expected non-empty content")
			}
		}
	}
	if !found {
		t.Error("page-1 not found in items")
	}
}

func TestConnectorFetchAll_Database(t *testing.T) {
	allowNotionTestServer(t)
	ts, cfg := fakeNotion()
	defer ts.Close()

	c := NewConnector()
	items, err := c.FetchAll(context.Background(), makeNotionConfig(cfg, ts.URL, []string{"db-1"}), []string{"db-1"})
	if err != nil {
		t.Fatalf("FetchAll() error: %v", err)
	}

	if len(items) == 0 {
		t.Fatal("expected at least 1 item")
	}

	// The entire database should be synced as a single table knowledge item
	found := false
	for _, item := range items {
		if item.ExternalID == "db-1" {
			found = true
			if item.Metadata["object_type"] != "database" {
				t.Errorf("object_type = %q, want %q", item.Metadata["object_type"], "database")
			}
			if item.ContentType != "text/markdown" {
				t.Errorf("ContentType = %q", item.ContentType)
			}
			if len(item.Content) == 0 {
				t.Error("expected non-empty content")
			}
			// Verify it contains table headers
			contentStr := string(item.Content)
			if !strings.Contains(contentStr, "| Title |") {
				t.Errorf("expected table header in content, got: %s", contentStr)
			}
		}
	}
	if !found {
		t.Error("db-1 not found in items")
	}
}

// TestConnectorFetchAll_SingleRecord verifies that selecting a single database
// row by ID routes through fetchPage's record-detection branch and produces an
// item via buildRecordItem (instead of being silently dropped as an empty page).
func TestConnectorFetchAll_SingleRecord(t *testing.T) {
	allowNotionTestServer(t)
	ts, cfg := fakeNotion()
	defer ts.Close()

	c := NewConnector()
	items, err := c.FetchAll(context.Background(), makeNotionConfig(cfg, ts.URL, []string{"record-1"}), []string{"record-1"})
	if err != nil {
		t.Fatalf("FetchAll() error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].ExternalID != "record-1" {
		t.Errorf("ExternalID = %q, want record-1", items[0].ExternalID)
	}
	if items[0].Metadata["object_type"] != "page" {
		t.Errorf("object_type = %q, want page", items[0].Metadata["object_type"])
	}
	if len(items[0].Content) == 0 {
		t.Error("expected non-empty content from buildRecordItem")
	}
}

func TestConnectorFetchIncremental_NoChanges(t *testing.T) {
	allowNotionTestServer(t)
	ts, cfg := fakeNotion()
	defer ts.Close()

	c := NewConnector()
	config := makeNotionConfig(cfg, ts.URL, []string{"page-1"})

	// First: full fetch to establish baseline
	_, err := c.FetchAll(context.Background(), config, []string{"page-1"})
	if err != nil {
		t.Fatalf("FetchAll() error: %v", err)
	}

	// Build a cursor that matches current state
	cursor := &types.SyncCursor{
		ConnectorCursor: map[string]interface{}{
			"page_edit_times": map[string]interface{}{
				"page-1": "2026-01-15T10:00:00Z",
			},
		},
	}

	items, newCursor, err := c.FetchIncremental(context.Background(), config, cursor)
	if err != nil {
		t.Fatalf("FetchIncremental() error: %v", err)
	}

	// No changes expected (timestamps match)
	if len(items) != 0 {
		t.Errorf("expected 0 items for no changes, got %d", len(items))
	}
	if newCursor == nil {
		t.Fatal("expected non-nil cursor")
	}
}

func TestPropertyToString(t *testing.T) {
	tests := []struct {
		name     string
		value    map[string]interface{}
		expected string
	}{
		{
			name: "select",
			value: map[string]interface{}{
				"type":   "select",
				"select": map[string]interface{}{"name": "Done"},
			},
			expected: "Done",
		},
		{
			name: "rich_text",
			value: map[string]interface{}{
				"type": "rich_text",
				"rich_text": []interface{}{
					map[string]interface{}{"plain_text": "Hello"},
				},
			},
			expected: "Hello",
		},
		{
			name: "number",
			value: map[string]interface{}{
				"type":   "number",
				"number": 42.0,
			},
			expected: "42",
		},
		{
			name: "checkbox true",
			value: map[string]interface{}{
				"type":     "checkbox",
				"checkbox": true,
			},
			expected: "true",
		},
		{
			name: "date",
			value: map[string]interface{}{
				"type": "date",
				"date": map[string]interface{}{"start": "2026-01-15", "end": "2026-01-20"},
			},
			expected: "2026-01-15 ~ 2026-01-20",
		},
		{
			name: "multi_select",
			value: map[string]interface{}{
				"type": "multi_select",
				"multi_select": []interface{}{
					map[string]interface{}{"name": "Tag1"},
					map[string]interface{}{"name": "Tag2"},
				},
			},
			expected: "Tag1, Tag2",
		},
		{
			name:     "nil value",
			value:    map[string]interface{}{"type": "rich_text", "rich_text": nil},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := propertyToString(tt.value)
			if got != tt.expected {
				t.Errorf("propertyToString() = %q, want %q", got, tt.expected)
			}
		})
	}
}

func TestAttachmentDownloadFailureLogOmitsFileName(t *testing.T) {
	const sentinel = "S3NT1NEL"
	allowNotionTestServer(t)
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	logger.SetLogLevel(logger.LevelDebug)
	t.Cleanup(logger.ConfigureFromEnv)

	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/search":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"results":[],"has_more":false}`)
		case "/v1/pages/page":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"id":"page","object":"page","url":"https://notion.so/page",`+
				`"last_edited_time":"2026-09-26T10:00:00Z","parent":{"type":"workspace"},`+
				`"properties":{"title":{"type":"title","title":[{"plain_text":"Notes"}]}}}`)
		case "/v1/blocks/page/children":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"results":[{"id":"file-1","type":"file","file":{"type":"external",`+
				`"name":"%s.pdf","external":{"url":"%s/download"}}}],"has_more":false}`,
				sentinel, serverURL)
		case "/download":
			w.WriteHeader(http.StatusBadRequest)
		default:
			http.NotFound(w, r)
		}
	}))
	serverURL = server.URL
	t.Cleanup(server.Close)

	config := makeNotionConfig(&Config{APIKey: "test-token"}, server.URL, []string{"page"})
	if _, err := NewConnector().FetchAll(context.Background(), config, []string{"page"}); err != nil {
		t.Fatalf("FetchAll: %v", err)
	}
	logs := buf.String()
	if strings.Contains(logs, sentinel) {
		t.Fatalf("log contains sentinel:\n%s", logs)
	}
	if !strings.Contains(logs, "failed to download attachment name_len=") ||
		!strings.Contains(logs, "error_len=") {
		t.Fatalf("log missing attachment download line:\n%s", logs)
	}
}
