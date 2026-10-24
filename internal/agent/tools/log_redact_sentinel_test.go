package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/sandbox"
	"github.com/Tencent/WeKnora/internal/types"
)

const agent24LogSentinel = "S3NT1NEL"

func captureAgent24DebugLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	logger.SetLogLevel(logger.LevelDebug)
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	t.Cleanup(logger.ConfigureFromEnv)
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

func TestShellExecLogOmitsCommand(t *testing.T) {
	for _, command := range []string{
		"grep " + agent24LogSentinel + " /workspace/a.txt", // runs
		"rm -rf / # " + agent24LogSentinel,                 // rejected by the blacklist
	} {
		buf := captureAgent24DebugLog(t)
		args, err := json.Marshal(ShellExecInput{Command: command})
		if err != nil {
			t.Fatalf("marshal args: %v", err)
		}
		executor := &fakeShellExecutor{result: &sandbox.ExecuteResult{}}
		if _, err := NewShellExecTool(executor, nil).Execute(shellExecTestContext(), args); err != nil {
			t.Fatalf("Execute(%q): %v", command, err)
		}
		logs := buf.String()
		if strings.Contains(logs, agent24LogSentinel) {
			t.Fatalf("log leaked command: %s", logs)
		}
		if want := fmt.Sprintf("command_len=%d", len(command)); !strings.Contains(logs, want) {
			t.Fatalf("log = %s, want substring %s", logs, want)
		}
	}
}

// failingTool returns a tool error and an execution error that quote its input.
type failingTool struct{ mockTool }

func (f *failingTool) Execute(_ context.Context, args json.RawMessage) (*types.ToolResult, error) {
	text := fmt.Sprintf("invalid regex query %q", string(args))
	return &types.ToolResult{Success: false, Error: text}, errors.New(text)
}

func TestToolRegistryLogOmitsToolErrorText(t *testing.T) {
	buf := captureAgent24DebugLog(t)
	registry := NewToolRegistry()
	registry.RegisterTool(&failingTool{mockTool{name: "failing"}})
	args := json.RawMessage(`{"query":"` + agent24LogSentinel + `("}`)

	result, err := registry.ExecuteTool(context.Background(), "failing", args)
	if err == nil || result == nil || !strings.Contains(result.Error, agent24LogSentinel) {
		t.Fatalf("the tool's own result must be unchanged: result=%#v err=%v", result, err)
	}
	logs := buf.String()
	if strings.Contains(logs, agent24LogSentinel) {
		t.Fatalf("log leaked tool error text: %s", logs)
	}
	if !strings.Contains(logs, "error_len=") || !strings.Contains(logs, "error_type=") {
		t.Fatalf("log = %s, want error_len and error_type", logs)
	}
}
