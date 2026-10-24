package chat

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/tracing/langfuse"
	"github.com/Tencent/WeKnora/internal/types"
)

func TestBuildLangfuseGenerationOutput(t *testing.T) {
	toolCalls := []types.LLMToolCall{{ID: "call_1", Type: "function"}}

	got := buildLangfuseGenerationOutput("", "", "tool_calls", toolCalls)
	want := map[string]interface{}{
		"content":       "",
		"tool_calls":    toolCalls,
		"finish_reason": "tool_calls",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("output without reasoning = %#v; want %#v", got, want)
	}

	got = buildLangfuseGenerationOutput("answer", "thinking", "stop", nil)
	want = map[string]interface{}{
		"content":           "answer",
		"tool_calls":        []types.LLMToolCall(nil),
		"finish_reason":     "stop",
		"reasoning_content": "thinking",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("output with reasoning = %#v; want %#v", got, want)
	}
}

func TestSnapshotLangfuseToolCallsKeepsModelArguments(t *testing.T) {
	providerCalls := []types.LLMToolCall{{
		ID:       "call_1",
		Function: types.FunctionCall{Name: "wiki_read_page", Arguments: `{"slugs":["res://0001"]}`},
	}}
	snapshot := snapshotLangfuseToolCalls(providerCalls)
	providerCalls[0].Function.Arguments = `{"slugs":["summary/uuid"]}`

	if got := snapshot[0].Function.Arguments; got != `{"slugs":["res://0001"]}` {
		t.Fatalf("Langfuse snapshot was mutated to %s", got)
	}
}

func TestBuildLangfuseMessagesReasoningContent(t *testing.T) {
	msgs := buildLangfuseMessages([]Message{
		{Role: "assistant", ReasoningContent: "chain of thought", ToolCalls: []ToolCall{{ID: "tc1"}}},
	})
	if len(msgs) != 1 {
		t.Fatalf("len(messages) = %d; want 1", len(msgs))
	}
	if msgs[0]["reasoning_content"] != "chain of thought" {
		t.Fatalf("reasoning_content = %v; want chain of thought", msgs[0]["reasoning_content"])
	}
}

func TestBuildLangfuseMessagesRecordsImagesWithoutPayload(t *testing.T) {
	dataURI := "data:image/png;base64," + strings.Repeat("A", 2048)
	msgs := buildLangfuseMessages([]Message{
		{Role: "user", Content: "look", Images: []string{dataURI, "https://example.com/a.png"}},
	})
	want := []string{"data:image/png (3 KB)", "https://example.com/a.png"}
	if !reflect.DeepEqual(msgs[0]["images"], want) {
		t.Fatalf("images = %#v; want %#v", msgs[0]["images"], want)
	}
}

func TestConvertUsageIncludesPromptCacheCounters(t *testing.T) {
	got := convertUsage(&types.TokenUsage{
		PromptTokens: 1000, CompletionTokens: 50, TotalTokens: 1050,
		CacheReadTokens: 800, CacheWriteTokens: 100, CacheMissTokens: 200,
	})
	if got == nil {
		t.Fatal("convertUsage returned nil")
	}
	if got.CacheRead != 800 || got.CacheWrite != 100 || got.CacheMiss != 200 {
		t.Fatalf("cache usage = read:%d write:%d miss:%d", got.CacheRead, got.CacheWrite, got.CacheMiss)
	}
}

func TestBuildLangfuseChatMetadataIncludesMCPCatalog(t *testing.T) {
	meta := buildLangfuseChatMetadata("model-1", "agent", "fp", true, &ChatOptions{
		Tools: []Tool{
			{Function: FunctionDef{Name: "knowledge_search", Description: "search kb"}},
			{Function: FunctionDef{
				Name:        langfuseDiscoverMCPTool,
				Description: "MCP tools are available without an @mention.\n{\"server_id\":\"svc\"}",
			}},
			{Function: FunctionDef{Name: "call_mcp_tool", Description: "call"}},
		},
	}, true)
	if meta["has_tools"] != true || meta["streaming"] != true {
		t.Fatalf("flags = %#v", meta)
	}
	names, _ := meta["tool_names"].([]string)
	if !reflect.DeepEqual(names, []string{"knowledge_search", langfuseDiscoverMCPTool, "call_mcp_tool"}) {
		t.Fatalf("tool_names = %#v", names)
	}
	catalog, _ := meta["mcp_catalog"].(string)
	if !strings.Contains(catalog, `"server_id":"svc"`) {
		t.Fatalf("mcp_catalog = %q", catalog)
	}
}

func TestBuildLangfuseChatMetadataRedactsMCPCatalog(t *testing.T) {
	const sentinel = "S3NT1NEL"
	desc := "system prompt " + sentinel + " mcp catalog"
	meta := buildLangfuseChatMetadata("model-1", "agent", "fp", false, &ChatOptions{
		Tools: []Tool{
			{Function: FunctionDef{Name: "knowledge_search", Description: "search kb"}},
			{Function: FunctionDef{
				Name:        langfuseDiscoverMCPTool,
				Description: desc,
			}},
		},
	}, false)
	if _, ok := meta["mcp_catalog"]; ok {
		t.Fatalf("mcp_catalog = %#v", meta["mcp_catalog"])
	}
	if got, ok := meta["mcp_catalog_bytes"].(int); !ok || got != len(desc) {
		t.Fatalf("mcp_catalog_bytes = %#v, want %d", meta["mcp_catalog_bytes"], len(desc))
	}
	names, _ := meta["tool_names"].([]string)
	if !reflect.DeepEqual(names, []string{"knowledge_search", langfuseDiscoverMCPTool}) {
		t.Fatalf("tool_names = %#v", names)
	}
	for k, v := range meta {
		if strings.Contains(fmt.Sprint(v), sentinel) {
			t.Fatalf("meta[%s] contains sentinel: %#v", k, v)
		}
	}
}

func TestTruncateLangfuseText(t *testing.T) {
	if got := truncateLangfuseText("一二三四五", 3); got != "一二三…" {
		t.Fatalf("got %q", got)
	}
}

func TestLangfuseChatOmitsMCPCatalogBody(t *testing.T) {
	const sentinel = "S3NT1NEL"
	var mu sync.Mutex
	var raw bytes.Buffer
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		raw.Write(b)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)

	mgr, err := langfuse.Init(langfuse.Config{
		Enabled:        true,
		Host:           srv.URL,
		PublicKey:      "pk",
		SecretKey:      "sk",
		FlushAt:        1,
		FlushInterval:  20 * time.Millisecond,
		QueueSize:      16,
		RequestTimeout: 2 * time.Second,
		SampleRate:     1,
		ExportBodies:   false,
	})
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	t.Cleanup(func() {
		_, _ = langfuse.Init(langfuse.Config{})
	})

	desc := "discover_mcp_tools catalog " + sentinel
	wrapped := &langfuseChat{inner: &fakeChat{id: "model-1"}}
	_, err = wrapped.Chat(context.Background(), []Message{{Role: "user", Content: "hi"}}, &ChatOptions{
		Tools: []Tool{{
			Function: FunctionDef{
				Name:        langfuseDiscoverMCPTool,
				Description: desc,
			},
		}},
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := mgr.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	mu.Lock()
	body := raw.String()
	mu.Unlock()
	if strings.Contains(body, sentinel) {
		t.Fatalf("export contains sentinel (%d bytes)", len(body))
	}
	if !strings.Contains(body, "mcp_catalog_bytes") {
		t.Fatalf("export missing mcp_catalog_bytes (%d bytes)", len(body))
	}
}
