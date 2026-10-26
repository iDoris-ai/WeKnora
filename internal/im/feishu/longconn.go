package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	larkws "github.com/larksuite/oapi-sdk-go/v3/ws"
)

// MessageHandler is called when an IM message is received via long connection.
type MessageHandler func(ctx context.Context, msg *im.IncomingMessage) error

// LongConnClient manages a Feishu/Lark WebSocket long connection.
type LongConnClient struct {
	region   Region
	appID    string
	wsClient *larkws.Client
}

// NewLongConnClient creates a long connection client on the given region's cloud.
// apiBaseURL overrides the SDK's bootstrap domain (empty uses the region
// default); set it to a reverse-proxy origin for private/internal deployments.
// When a message arrives, it converts it to IncomingMessage and calls handler.
func NewLongConnClient(region Region, appID, appSecret, apiBaseURL string, handler MessageHandler) *LongConnClient {
	// Long connection mode does not require verificationToken or encryptKey;
	// those are only used for webhook signature verification and decryption.
	eventHandler := dispatcher.NewEventDispatcher("", "").
		OnP2MessageReceiveV1(func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
			msg := convertEvent(region, event)
			if msg == nil {
				return nil
			}
			return handler(ctx, msg)
		})

	sdkLogger := &feishuLoggerAdapter{region: region, appID: appID}

	apiBaseURL = strings.TrimRight(strings.TrimSpace(apiBaseURL), "/")
	if apiBaseURL == "" {
		apiBaseURL = region.OpenBaseURL
	}

	// WithDomain points the SDK at the region's cloud (open.feishu.cn by
	// default). apiBaseURL overrides it for deployments that reach Feishu
	// through a reverse proxy.
	wsClient := larkws.NewClient(appID, appSecret,
		larkws.WithEventHandler(eventHandler),
		larkws.WithAutoReconnect(true),
		larkws.WithLogger(sdkLogger),
		larkws.WithDomain(apiBaseURL),
	)

	return &LongConnClient{region: region, appID: appID, wsClient: wsClient}
}

// Start begins the WebSocket long connection. It blocks until ctx is cancelled.
func (c *LongConnClient) Start(ctx context.Context) error {
	logger.Infof(ctx, "[IM] %s WebSocket connecting (app_id=%s)...", c.region.Label, c.appID)
	return c.wsClient.Start(ctx)
}

// Close tears down the WebSocket long connection.
//
// This is the only reliable way to stop a long connection: the SDK's
// Start blocks on a bare select{} and neither Start, pingLoop, nor
// receiveMessageLoop observe the passed context, so cancelling ctx alone leaves
// the underlying socket alive and the SDK auto-reconnecting. Client.Close()
// (added in oapi-sdk-go v3.9.7) flips autoReconnect off and calls disconnect,
// which actually closes the socket. Callers should still cancel the start ctx
// as a belt-and-braces fallback for the start goroutine.
func (c *LongConnClient) Close() {
	c.wsClient.Close()
}

// feishuLoggerAdapter bridges the Feishu/Lark SDK logger to our unified logger,
// replacing raw SDK connection messages with a consistent format.
//
// Agent24 KB-OBS-01: the SDK's messages carry the wss connection URL with its query,
// message payloads (Debug) and raw errors. Connect and disconnect keep a fixed text;
// every other message is logged as its event phrase, integer arguments and length.
type feishuLoggerAdapter struct {
	region Region
	appID  string
}

func (l *feishuLoggerAdapter) Debug(ctx context.Context, args ...interface{}) {
	logger.Debugf(ctx, "[%s] sdk debug: %s", l.region.Label, feishuSDKLogShape(args))
}

func (l *feishuLoggerAdapter) Info(ctx context.Context, args ...interface{}) {
	msg := fmt.Sprint(args...)
	if strings.HasPrefix(msg, "connected to ") {
		logger.Infof(ctx, "[IM] %s WebSocket connected successfully (app_id=%s)", l.region.Label, l.appID)
		return
	}
	if strings.HasPrefix(msg, "disconnected to ") {
		logger.Infof(ctx, "[IM] %s WebSocket disconnected (app_id=%s)", l.region.Label, l.appID)
		return
	}
	logger.Infof(ctx, "[%s] sdk info: %s", l.region.Label, feishuSDKLogShape(args))
}

func (l *feishuLoggerAdapter) Warn(ctx context.Context, args ...interface{}) {
	logger.Warnf(ctx, "[%s] sdk warn: %s", l.region.Label, feishuSDKLogShape(args))
}

func (l *feishuLoggerAdapter) Error(ctx context.Context, args ...interface{}) {
	logger.Errorf(ctx, "[%s] sdk error: %s", l.region.Label, feishuSDKLogShape(args))
}

// feishuSDKLogEvents are the fixed leading phrases of the SDK ws client's log lines
// (oapi-sdk-go v3 ws/client.go). Only the phrase is logged, never the text after it.
var feishuSDKLogEvents = []string{
	"connect failed", "get conn url failed", "trying to reconnect", "response status code",
	"ping loop panic", "ping failed", "ping success", "receive pong",
	"receive message loop panic", "connection is closed", "receive message failed",
	"receive unknown message", "receive message", "handle message panic", "handle message failed",
	"unmarshal message failed", "unmarshal client config failed", "response message failed",
}

// feishuSDKLogShape describes an SDK log call without its text: the event phrase
// (longest known prefix of the first argument), the reconnect attempt, integer
// arguments such as an HTTP status code, the argument types and the message length.
func feishuSDKLogShape(args []interface{}) string {
	event, first := "unknown", ""
	if len(args) > 0 {
		first, _ = args[0].(string)
	}
	for _, e := range feishuSDKLogEvents {
		if strings.HasPrefix(first, e) && (event == "unknown" || len(e) > len(event)) {
			event = e
		}
	}
	var extra strings.Builder
	if event == "trying to reconnect" {
		// The attempt number is formatted into the string: "trying to reconnect: 3".
		if n, err := strconv.Atoi(strings.TrimPrefix(first, "trying to reconnect: ")); err == nil {
			fmt.Fprintf(&extra, " attempt=%d", n)
		}
	}
	types := make([]string, len(args))
	var ints []string
	for i, arg := range args {
		types[i] = fmt.Sprintf("%T", arg)
		switch arg.(type) {
		case int, int32, int64, uint, uint32, uint64:
			ints = append(ints, fmt.Sprint(arg))
		}
	}
	if len(ints) > 0 {
		fmt.Fprintf(&extra, " int_args=[%s]", strings.Join(ints, ","))
	}
	return fmt.Sprintf("event=%q%s arg_types=[%s] len=%d",
		event, extra.String(), strings.Join(types, ","), len(fmt.Sprint(args...)))
}

// convertEvent converts a Feishu/Lark SDK event to a unified IncomingMessage.
// Supports text, file, image and post messages. Returns nil for other types.
func convertEvent(region Region, event *larkim.P2MessageReceiveV1) *im.IncomingMessage {
	if event == nil || event.Event == nil || event.Event.Message == nil {
		logger.Warnf(context.Background(), "[%s][RX] event dropped: nil event/event/message", region.Label)
		return nil
	}

	msg := event.Event.Message

	// Debug: log every raw receive event so we can see exactly what the platform
	// delivers (or doesn't). This is the single chokepoint for all message
	// types — adding it here catches text/file/image/post uniformly.
	logger.Infof(context.Background(),
		"[%s][RX] msg_type=%q chat_type=%q chat_id=%q msg_id=%q root_id=%q parent_id=%q thread_id=%q content_len=%d",
		region.Label,
		ptrStr(msg.MessageType), ptrStr(msg.ChatType), ptrStr(msg.ChatId),
		ptrStr(msg.MessageId), ptrStr(msg.RootId), ptrStr(msg.ParentId),
		ptrStr(msg.ThreadId), len(ptrStr(msg.Content)),
	)

	if msg.MessageType == nil {
		return nil
	}

	msgType := *msg.MessageType

	// Sender info
	openID := ""
	if event.Event.Sender != nil && event.Event.Sender.SenderId != nil && event.Event.Sender.SenderId.OpenId != nil {
		openID = *event.Event.Sender.SenderId.OpenId
	}

	// Chat type
	chatType := im.ChatTypeDirect
	chatID := ""
	if msg.ChatType != nil && *msg.ChatType == "group" {
		chatType = im.ChatTypeGroup
		if msg.ChatId != nil {
			chatID = *msg.ChatId
		}
	}

	// Message ID
	messageID := ""
	if msg.MessageId != nil {
		messageID = *msg.MessageId
	}

	switch msgType {
	case "text":
		return convertTextEvent(region, msg, openID, chatID, chatType, messageID)
	case "file":
		return convertFileEvent(region, msg, openID, chatID, chatType, messageID)
	case "image":
		return convertImageEvent(region, msg, openID, chatID, chatType, messageID)
	case "post":
		return convertPostEvent(region, msg, openID, chatID, chatType, messageID)
	default:
		return nil
	}
}

// convertTextEvent handles text message type.
func convertTextEvent(
	region Region, msg *larkim.EventMessage,
	openID, chatID string, chatType im.ChatType, messageID string,
) *im.IncomingMessage {
	var textContent struct {
		Text string `json:"text"`
	}
	if msg.Content == nil {
		return nil
	}
	if err := json.Unmarshal([]byte(*msg.Content), &textContent); err != nil {
		return nil
	}

	content := textContent.Text
	if chatType == im.ChatTypeGroup {
		for strings.HasPrefix(content, "@_user_") {
			idx := strings.Index(content, " ")
			if idx >= 0 {
				content = content[idx+1:]
			} else {
				break
			}
		}
	}

	return &im.IncomingMessage{
		Platform:    region.Platform,
		MessageType: im.MessageTypeText,
		UserID:      openID,
		ChatID:      chatID,
		ChatType:    chatType,
		Content:     strings.TrimSpace(content),
		MessageID:   messageID,
	}
}

// convertFileEvent handles file message type.
func convertFileEvent(
	region Region, msg *larkim.EventMessage,
	openID, chatID string, chatType im.ChatType, messageID string,
) *im.IncomingMessage {
	if msg.Content == nil {
		return nil
	}
	var fileContent struct {
		FileKey  string `json:"file_key"`
		FileName string `json:"file_name"`
	}
	if err := json.Unmarshal([]byte(*msg.Content), &fileContent); err != nil {
		return nil
	}
	if fileContent.FileKey == "" {
		return nil
	}

	return &im.IncomingMessage{
		Platform:    region.Platform,
		MessageType: im.MessageTypeFile,
		UserID:      openID,
		ChatID:      chatID,
		ChatType:    chatType,
		MessageID:   messageID,
		FileKey:     fileContent.FileKey,
		FileName:    fileContent.FileName,
	}
}

// convertImageEvent handles image message type.
// Downloads via GetMessageResource API with type=image.
func convertImageEvent(
	region Region, msg *larkim.EventMessage,
	openID, chatID string, chatType im.ChatType, messageID string,
) *im.IncomingMessage {
	if msg.Content == nil {
		return nil
	}
	var imageContent struct {
		ImageKey string `json:"image_key"`
	}
	if err := json.Unmarshal([]byte(*msg.Content), &imageContent); err != nil {
		return nil
	}
	if imageContent.ImageKey == "" {
		return nil
	}

	return &im.IncomingMessage{
		Platform:    region.Platform,
		MessageType: im.MessageTypeImage,
		UserID:      openID,
		ChatID:      chatID,
		ChatType:    chatType,
		MessageID:   messageID,
		FileKey:     imageContent.ImageKey,
		FileName:    imageContent.ImageKey + ".png",
	}
}

// convertPostEvent handles rich-text (post) message type.
// Extracts plain text and the first embedded image so mixed posts can use the
// same attachment pipeline as standalone image messages.
func convertPostEvent(
	region Region, msg *larkim.EventMessage,
	openID, chatID string, chatType im.ChatType, messageID string,
) *im.IncomingMessage {
	if msg.Content == nil {
		return nil
	}

	// Post content structure: {"title":"...", "content":[[{"tag":"text","text":"..."},{"tag":"img","image_key":"..."}]]}
	var postContent struct {
		Title   string              `json:"title"`
		Content [][]json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal([]byte(*msg.Content), &postContent); err != nil {
		return nil
	}

	var textParts []string
	imageKey := ""
	if postContent.Title != "" {
		textParts = append(textParts, postContent.Title)
	}

	for _, line := range postContent.Content {
		var lineText strings.Builder
		for _, elem := range line {
			var tag struct {
				Tag      string `json:"tag"`
				Text     string `json:"text"`
				ImageKey string `json:"image_key"`
			}
			if err := json.Unmarshal(elem, &tag); err != nil {
				continue
			}
			switch tag.Tag {
			case "text", "a":
				lineText.WriteString(tag.Text)
			case "img":
				if imageKey == "" {
					imageKey = tag.ImageKey
				}
			case "at":
				// Skip @mentions
			}
		}
		if t := strings.TrimSpace(lineText.String()); t != "" {
			textParts = append(textParts, t)
		}
	}

	content := strings.Join(textParts, "\n")
	if chatType == im.ChatTypeGroup {
		for strings.HasPrefix(content, "@_user_") {
			idx := strings.Index(content, " ")
			if idx >= 0 {
				content = content[idx+1:]
			} else {
				break
			}
		}
	}

	content = strings.TrimSpace(content)
	if content == "" && imageKey == "" {
		return nil
	}

	messageType := im.MessageTypeText
	fileName := ""
	if imageKey != "" {
		messageType = im.MessageTypeImage
		fileName = imageKey + ".png"
	}

	return &im.IncomingMessage{
		Platform:    region.Platform,
		MessageType: messageType,
		UserID:      openID,
		ChatID:      chatID,
		ChatType:    chatType,
		Content:     content,
		MessageID:   messageID,
		FileKey:     imageKey,
		FileName:    fileName,
	}
}

// ptrStr safely dereferences a *string for logging; returns "" for nil so the
// log line stays readable instead of printing <nil>.
func ptrStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
