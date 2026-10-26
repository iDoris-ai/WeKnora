package service

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/Tencent/WeKnora/internal/event"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/models/asr"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/models/embedding"
	"github.com/Tencent/WeKnora/internal/models/rerank"
	"github.com/Tencent/WeKnora/internal/models/vlm"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type captureChatModel struct {
	lastMessages []chat.Message
}

func (m *captureChatModel) Chat(
	context.Context,
	[]chat.Message,
	*chat.ChatOptions,
) (*types.ChatResponse, error) {
	return nil, nil
}

func (m *captureChatModel) ChatStream(
	_ context.Context,
	messages []chat.Message,
	_ *chat.ChatOptions,
) (<-chan types.StreamResponse, error) {
	m.lastMessages = append([]chat.Message(nil), messages...)

	ch := make(chan types.StreamResponse, 1)
	ch <- types.StreamResponse{
		ResponseType: types.ResponseTypeAnswer,
		Content:      "ok",
		Done:         true,
	}
	close(ch)
	return ch, nil
}

func (m *captureChatModel) GetModelName() string { return "capture" }
func (m *captureChatModel) GetModelID() string   { return "capture" }

type stubModelService struct {
	chatModel       chat.Chat
	modelsByID      map[string]*types.Model
	availableModels []*types.Model
}

func TestEmitKnowledgeReferencesEventIgnoresCitationOutputSetting(t *testing.T) {
	bus := event.NewEventBus()
	var emitted []event.Event
	bus.On(event.EventAgentReferences, func(_ context.Context, evt event.Event) error {
		emitted = append(emitted, evt)
		return nil
	})
	disabled := false
	result := &types.SearchResult{ID: "chunk-1", KnowledgeTitle: "Doc"}
	cm := &types.ChatManage{
		PipelineRequest: types.PipelineRequest{CitationEnabled: &disabled},
		PipelineState: types.PipelineState{
			MergeResult: []*types.SearchResult{result},
		},
		PipelineContext: types.PipelineContext{EventBus: bus.AsEventBusInterface()},
	}

	emitKnowledgeReferencesEvent(context.Background(), cm)
	require.Len(t, emitted, 1)
	require.Equal(t, event.EventAgentReferences, emitted[0].Type)
	require.Equal(t, []*types.SearchResult{result}, emitted[0].Data.(event.AgentReferencesData).References)

	enabled := true
	cm.CitationEnabled = &enabled
	emitKnowledgeReferencesEvent(context.Background(), cm)
	require.Len(t, emitted, 2)
}

func (s *stubModelService) CreateModel(context.Context, *types.Model) error {
	return nil
}

func (s *stubModelService) CopyModel(context.Context, string, string) (*types.Model, error) {
	return nil, nil
}

func (s *stubModelService) GetModelByID(_ context.Context, id string) (*types.Model, error) {
	return s.modelsByID[id], nil
}

func (s *stubModelService) ListModels(context.Context) ([]*types.Model, error) {
	return s.availableModels, nil
}

func (s *stubModelService) UpdateModel(context.Context, *types.Model) error {
	return nil
}

func (s *stubModelService) DeleteModel(context.Context, string) error {
	return nil
}

func (s *stubModelService) UpdateModelCredentials(
	context.Context, string, *string, *string,
) (*types.Model, error) {
	return nil, nil
}

func (s *stubModelService) ClearModelCredential(context.Context, string, string) error {
	return nil
}

func (s *stubModelService) GetEmbeddingModel(context.Context, string) (embedding.Embedder, error) {
	return nil, nil
}

func (s *stubModelService) GetEmbeddingModelForTenant(context.Context, string, uint64) (embedding.Embedder, error) {
	return nil, nil
}

func (s *stubModelService) GetRerankModel(context.Context, string) (rerank.Reranker, error) {
	return nil, nil
}

func (s *stubModelService) GetChatModel(context.Context, string) (chat.Chat, error) {
	return s.chatModel, nil
}

func (s *stubModelService) GetVLMModel(context.Context, string) (vlm.VLM, error) {
	return nil, nil
}

func (s *stubModelService) GetASRModel(context.Context, string) (asr.ASR, error) {
	return nil, nil
}

func TestHandleModelFallback_IncludesHistoryMessages(t *testing.T) {
	chatModel := &captureChatModel{}
	svc := &sessionService{
		modelService: &stubModelService{chatModel: chatModel},
	}

	bus := event.NewEventBus()
	cm := &types.ChatManage{
		PipelineRequest: types.PipelineRequest{
			SessionID:      "session-1",
			Query:          "现在还能继续讲吗？",
			ChatModelID:    "chat-model",
			FallbackPrompt: "Answer the latest user question: {{query}}",
			SummaryConfig: types.SummaryConfig{
				Temperature: 0.2,
			},
			Language: "zh-CN",
		},
		PipelineState: types.PipelineState{
			History: []*types.History{
				{
					Query:  "先介绍一下 WeKnora",
					Answer: "WeKnora 是一个知识库问答系统。",
				},
			},
		},
		PipelineContext: types.PipelineContext{
			EventBus: bus.AsEventBusInterface(),
		},
	}

	svc.handleModelFallback(context.Background(), cm)

	// Corrected fallback shape: a system message carries the fallback
	// instruction, history is replayed in the middle, and the turn ends on the
	// user's question. Previously the system message was dropped entirely.
	require.Len(t, chatModel.lastMessages, 4)
	assert.Equal(t, "system", chatModel.lastMessages[0].Role)
	assert.Contains(t, chatModel.lastMessages[0].Content, "Answer the latest user question")
	assert.Equal(t, "user", chatModel.lastMessages[1].Role)
	assert.Equal(t, "先介绍一下 WeKnora", chatModel.lastMessages[1].Content)
	assert.Equal(t, "assistant", chatModel.lastMessages[2].Role)
	assert.Equal(t, "WeKnora 是一个知识库问答系统。", chatModel.lastMessages[2].Content)
	assert.Equal(t, "user", chatModel.lastMessages[3].Role)
	assert.Contains(t, chatModel.lastMessages[3].Content, "现在还能继续讲吗？")
}

// lockedLogBuffer is a log sink that can be read while another goroutine writes to it.
type lockedLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// streamFailChat fails ChatStream with err.
type streamFailChat struct {
	captureChatModel
	err error
}

func (m *streamFailChat) ChatStream(
	context.Context, []chat.Message, *chat.ChatOptions,
) (<-chan types.StreamResponse, error) {
	return nil, m.err
}

// A vendor's error reply can echo the question; the fallback's start-failure log keeps its length and type.
func TestHandleModelFallback_StreamFailureLogOmitsErrorText(t *testing.T) {
	const sentinel = "S3NT1NEL"
	// A fallback stream from an earlier test may still log; the lock keeps its write apart from the read below.
	buf := &lockedLogBuffer{}
	logger.SetOutput(buf)
	logger.SetLogLevel(logger.LevelDebug)
	t.Cleanup(logger.ConfigureFromEnv)

	svc := &sessionService{
		modelService: &stubModelService{chatModel: &streamFailChat{err: errors.New("vendor echoed " + sentinel)}},
	}
	bus := event.NewEventBus()
	cm := &types.ChatManage{
		PipelineRequest: types.PipelineRequest{
			SessionID:        "session-1",
			Query:            "q",
			ChatModelID:      "chat-model",
			FallbackPrompt:   "Answer: {{query}}",
			FallbackResponse: "fixed",
		},
		PipelineContext: types.PipelineContext{EventBus: bus.AsEventBusInterface()},
	}

	svc.handleModelFallback(context.Background(), cm)

	out := buf.String()
	require.Contains(t, out, "Failed to start streaming fallback response: error_len=")
	assert.False(t, strings.Contains(out, sentinel), "log carries the vendor error text: %s", out)
	require.NotNil(t, cm.ChatResponse)
	assert.Equal(t, "fixed", cm.ChatResponse.Content)
}
