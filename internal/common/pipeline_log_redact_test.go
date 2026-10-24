package common

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const pipelineLogSentinel = "S3NT1NEL"

// agent24PipelineContentKeyList is the fork list PipelineLog must redact. Kept next to the tests so a new
// key added to agent24PipelineContentKeys is covered here.
func agent24PipelineContentKeyList() []string {
	return []string{
		"user_query",
		"query",
		"rewrite_query",
		"user_question",
		"query_preview",
		"content",
		"image_description",
		"system_prompt",
		"prompt",
		"answer",
		"answer_preview",
		"original_output",
		"args",
		"neighbor_content",
		"base_content",
		"after_content",
		"thought",
		"output",
		"title",
		"question",
		"sql",
	}
}

func TestPipelineLogRedactsContentKeyStrings(t *testing.T) {
	keys := agent24PipelineContentKeyList()
	if len(keys) != len(agent24PipelineContentKeys) {
		t.Fatalf("content key list has %d keys, map has %d", len(keys), len(agent24PipelineContentKeys))
	}
	for _, key := range keys {
		if _, ok := agent24PipelineContentKeys[key]; !ok {
			t.Fatalf("test key %s is not in agent24PipelineContentKeys", key)
		}
		t.Run(key, func(t *testing.T) {
			value := pipelineLogSentinel + "-text"
			got := PipelineLog("Stage", "act", map[string]interface{}{key: value})
			assertPipelineLogRedacted(t, got, key, len(value))
		})
	}
}

func TestPipelineLogRedactsContentKeyBytesAndRawMessage(t *testing.T) {
	payload := []byte(pipelineLogSentinel + "-raw")
	for _, key := range []string{"query", "args", "sql"} {
		t.Run(key+"/bytes", func(t *testing.T) {
			got := PipelineLog("Stage", "act", map[string]interface{}{key: append([]byte(nil), payload...)})
			assertPipelineLogRedacted(t, got, key, len(payload))
		})
		t.Run(key+"/raw", func(t *testing.T) {
			raw := json.RawMessage(append([]byte(nil), payload...))
			got := PipelineLog("Stage", "act", map[string]interface{}{key: raw})
			assertPipelineLogRedacted(t, got, key, len(raw))
		})
	}
}

func TestPipelineLogKeepsIntegerOnContentKey(t *testing.T) {
	got := PipelineLog("Stage", "act", map[string]interface{}{"query": 7})
	if !strings.Contains(got, "query=7") {
		t.Fatalf("PipelineLog = %s, want query=7", got)
	}
	if strings.Contains(got, "query_len=") {
		t.Fatalf("PipelineLog rewrote an integer query: %s", got)
	}
}

func TestPipelineLogKeepsNonContentString(t *testing.T) {
	got := PipelineLog("Stage", "act", map[string]interface{}{"session_id": pipelineLogSentinel})
	want := `session_id="` + pipelineLogSentinel + `"`
	if !strings.Contains(got, want) {
		t.Fatalf("PipelineLog = %s, want substring %s", got, want)
	}
}

func TestPipelineLogContentLenUsesOriginalBytes(t *testing.T) {
	// Past the 300-rune truncation cap, and byte length differs from rune length.
	value := strings.Repeat(pipelineLogSentinel, 40) + "中文"
	if len([]rune(value)) <= pipelineLogValueMaxRune {
		t.Fatalf("fixture has %d runes, want > %d", len([]rune(value)), pipelineLogValueMaxRune)
	}
	if len(value) == len([]rune(value)) {
		t.Fatal("fixture byte length equals rune length")
	}
	got := PipelineLog("Stage", "act", map[string]interface{}{"content": value})
	assertPipelineLogRedacted(t, got, "content", len(value))
}

func assertPipelineLogRedacted(t *testing.T, got, key string, n int) {
	t.Helper()
	if strings.Contains(got, pipelineLogSentinel) {
		t.Fatalf("PipelineLog leaked %q for key %s: %s", pipelineLogSentinel, key, got)
	}
	want := fmt.Sprintf("%s_len=%d", key, n)
	if !strings.Contains(got, want) {
		t.Fatalf("PipelineLog = %s, want substring %s", got, want)
	}
}
