package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
)

const agent24LogSentinel = "S3NT1NEL"

// agent24LogLevelFromEnv mirrors logger startup so a test can put the level back.
// The logger package does not export the current level.
func agent24LogLevelFromEnv() logger.LogLevel {
	switch strings.ToLower(os.Getenv("LOG_LEVEL")) {
	case "info":
		return logger.LevelInfo
	case "warn", "warning":
		return logger.LevelWarn
	case "error":
		return logger.LevelError
	case "fatal":
		return logger.LevelFatal
	default:
		return logger.LevelDebug
	}
}

func captureAgent24DebugLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	original := agent24LogLevelFromEnv()
	logger.SetLogLevel(logger.LevelDebug)
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	t.Cleanup(func() {
		logger.SetOutput(os.Stdout)
		logger.SetLogLevel(original)
	})
	return &buf
}

func TestSequentialThinkingExecuteLogOmitsThought(t *testing.T) {
	buf := captureAgent24DebugLog(t)
	thought := "prefix " + agent24LogSentinel + " suffix"
	args, err := json.Marshal(map[string]any{
		"thought":             thought,
		"next_thought_needed": false,
		"thought_number":      1,
		"total_thoughts":      1,
	})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	_, err = NewSequentialThinkingTool().Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	logs := buf.String()
	if strings.Contains(logs, agent24LogSentinel) {
		t.Fatalf("log leaked thought: %s", logs)
	}
	want := fmt.Sprintf("thought_len=%d", len(thought))
	if !strings.Contains(logs, want) {
		t.Fatalf("log = %s, want substring %s", logs, want)
	}
}

func TestWebSearchExecuteLogOmitsQuery(t *testing.T) {
	buf := captureAgent24DebugLog(t)
	query := "prefix " + agent24LogSentinel + " suffix"
	svc := &searchOnlyWebService{}
	tool := NewWebSearchTool(svc, 2, "provider-1")
	ctx := context.WithValue(t.Context(), types.TenantIDContextKey, uint64(1))
	result, err := tool.Execute(ctx, json.RawMessage(fmt.Sprintf(`{"query":%q}`, query)))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result == nil || !result.Success {
		t.Fatalf("Execute result = %#v, want success", result)
	}
	logs := buf.String()
	if strings.Contains(logs, agent24LogSentinel) {
		t.Fatalf("log leaked query: %s", logs)
	}
	want := fmt.Sprintf("query_len=%d", len(query))
	if !strings.Contains(logs, want) {
		t.Fatalf("log = %s, want substring %s", logs, want)
	}
}
