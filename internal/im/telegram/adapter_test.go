package telegram

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/logger"
)

func TestParseTelegramMessage_ForumTopicThread(t *testing.T) {
	msg := &telegramMsg{
		MessageID:       100,
		MessageThreadID: 42, // Forum topic ID
		From:            &telegramUser{ID: 1001, FirstName: "Alice"},
		Chat:            telegramChat{ID: -9999, Type: "supergroup"},
		Text:            "hello",
	}

	incoming := parseTelegramMessage(msg)
	if incoming == nil {
		t.Fatal("expected non-nil message")
	}

	if incoming.ThreadID != "42" {
		t.Errorf("ThreadID = %q, want %q", incoming.ThreadID, "42")
	}
}

func TestParseTelegramMessage_NonForumGroup(t *testing.T) {
	msg := &telegramMsg{
		MessageID:       200,
		MessageThreadID: 0, // not a Forum group
		From:            &telegramUser{ID: 1002, FirstName: "Bob"},
		Chat:            telegramChat{ID: -8888, Type: "group"},
		Text:            "hello",
	}

	incoming := parseTelegramMessage(msg)
	if incoming == nil {
		t.Fatal("expected non-nil message")
	}

	// Non-Forum groups: ThreadID should be empty
	if incoming.ThreadID != "" {
		t.Errorf("ThreadID = %q, want empty for non-Forum group", incoming.ThreadID)
	}
}

func TestParseTelegramMessage_DirectMessage(t *testing.T) {
	msg := &telegramMsg{
		MessageID: 300,
		From:      &telegramUser{ID: 1003, FirstName: "Carol"},
		Chat:      telegramChat{ID: 1003, Type: "private"},
		Text:      "hi bot",
	}

	incoming := parseTelegramMessage(msg)
	if incoming == nil {
		t.Fatal("expected non-nil message")
	}

	if incoming.ThreadID != "" {
		t.Errorf("ThreadID = %q, want empty for DM", incoming.ThreadID)
	}
	if incoming.ChatType != im.ChatTypeDirect {
		t.Errorf("ChatType = %q, want %q", incoming.ChatType, im.ChatTypeDirect)
	}
}

func TestParseTelegramMessage_NilMessage(t *testing.T) {
	incoming := parseTelegramMessage(nil)
	if incoming != nil {
		t.Error("expected nil for nil message")
	}
}

func TestParseTelegramMessage_Document(t *testing.T) {
	msg := &telegramMsg{
		MessageID:       400,
		MessageThreadID: 7, // in a Forum topic
		From:            &telegramUser{ID: 1004, FirstName: "Dave"},
		Chat:            telegramChat{ID: -7777, Type: "supergroup"},
		Document: &telegramDoc{
			FileID:   "doc-123",
			FileName: "report.pdf",
			FileSize: 2048,
		},
	}

	incoming := parseTelegramMessage(msg)
	if incoming == nil {
		t.Fatal("expected non-nil message")
	}

	if incoming.ThreadID != "7" {
		t.Errorf("ThreadID = %q, want %q", incoming.ThreadID, "7")
	}
	if incoming.MessageType != im.MessageTypeFile {
		t.Errorf("MessageType = %q, want %q", incoming.MessageType, im.MessageTypeFile)
	}
}

func TestParseUpdate_NilMessage(t *testing.T) {
	update := &telegramUpdate{UpdateID: 1, Message: nil}
	incoming := parseUpdate(update)
	if incoming != nil {
		t.Error("expected nil for nil update.Message")
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestGetUpdatesRedactsBotToken(t *testing.T) {
	const sentinel = "S3NT1NEL"
	client := &LongConnClient{
		botToken: sentinel,
		httpClient: &http.Client{
			Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return nil, context.DeadlineExceeded
			}),
		},
	}

	_, err := client.getUpdates(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Fatalf("error contains bot token: %v", err)
	}
}

// startLogCapture records logger writes. hit closes once the getUpdates error
// line is present, so the test can cancel Start after that line is written.
type startLogCapture struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	hit  chan struct{}
	once sync.Once
}

func (c *startLogCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n, err := c.buf.Write(p)
	if strings.Contains(c.buf.String(), "getUpdates error:") {
		c.once.Do(func() { close(c.hit) })
	}
	return n, err
}

func (c *startLogCapture) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

func TestLongConnStartGetUpdatesLogOmitsBotToken(t *testing.T) {
	logs := &startLogCapture{hit: make(chan struct{})}
	logger.SetOutput(logs)
	logger.SetLogLevel(logger.LevelError)
	t.Cleanup(logger.ConfigureFromEnv)

	const sentinel = "S3NT1NEL"
	client := &LongConnClient{
		botToken: sentinel,
		handler:  func(context.Context, *im.IncomingMessage) error { return nil },
		httpClient: &http.Client{
			Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
				return nil, context.DeadlineExceeded
			}),
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- client.Start(ctx)
	}()

	select {
	case <-logs.hit:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("getUpdates error was not logged")
	}
	// Cancel only after the error line is written. An earlier cancel makes
	// Start return ctx.Err() and skip the getUpdates log.
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after cancel")
	}

	text := logs.String()
	if strings.Contains(text, sentinel) {
		t.Fatalf("log contains bot token: %s", text)
	}
	if !strings.Contains(text, "getUpdates error: error_len=") {
		t.Fatalf("log missing redacted getUpdates error: %s", text)
	}
}
