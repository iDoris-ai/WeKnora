package wecom

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/gin-gonic/gin"
)

func TestWebhookAdapterDecryptPadding(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	corpID := "ww_corp_id"
	adapter := &WebhookAdapter{aesKey: key, corpID: corpID}

	tests := []struct {
		name       string
		message    string
		wantPadLen int
	}{
		{
			name:       "padding below AES block size",
			message:    "message with seven-byte pad",
			wantPadLen: 7,
		},
		{
			name:       "padding above AES block size",
			message:    "message with 19",
			wantPadLen: 19,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encrypted, padLen := encryptWeComTestPayload(t, key, corpID, []byte(tt.message))
			if padLen != tt.wantPadLen {
				t.Fatalf("test fixture padding = %d, want %d", padLen, tt.wantPadLen)
			}

			got, err := adapter.decrypt(encrypted)
			if err != nil {
				t.Fatalf("decrypt() error = %v", err)
			}
			if !bytes.Equal(got, []byte(tt.message)) {
				t.Fatalf("decrypt() = %q, want %q", got, tt.message)
			}
		})
	}
}

func TestWebhookAdapterDecryptRejectsNonBlockAlignedCiphertext(t *testing.T) {
	adapter := &WebhookAdapter{
		aesKey: []byte("0123456789abcdef0123456789abcdef"),
		corpID: "ww_corp_id",
	}
	encrypted := base64.StdEncoding.EncodeToString(make([]byte, aes.BlockSize+1))

	if _, err := adapter.decrypt(encrypted); err == nil {
		t.Fatal("decrypt() succeeded for ciphertext with a non-block-aligned length")
	}
}

func encryptWeComTestPayload(t *testing.T, key []byte, corpID string, message []byte) (string, int) {
	t.Helper()

	plaintext := make([]byte, 16)
	messageLength := make([]byte, 4)
	binary.BigEndian.PutUint32(messageLength, uint32(len(message)))
	plaintext = append(plaintext, messageLength...)
	plaintext = append(plaintext, message...)
	plaintext = append(plaintext, []byte(corpID)...)

	padLen := wecomPKCS7BlockSize - len(plaintext)%wecomPKCS7BlockSize
	plaintext = append(plaintext, bytes.Repeat([]byte{byte(padLen)}, padLen)...)

	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}

	ciphertext := make([]byte, len(plaintext))
	cipher.NewCBCEncrypter(block, key[:aes.BlockSize]).CryptBlocks(ciphertext, plaintext)
	return base64.StdEncoding.EncodeToString(ciphertext), padLen
}

func TestParseCallbackDebugLogOmitsMessageContent(t *testing.T) {
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	logger.SetLogLevel(logger.LevelDebug)
	t.Cleanup(logger.ConfigureFromEnv)

	key := []byte("0123456789abcdef0123456789abcdef")
	corpID := "ww_corp_id"
	adapter := &WebhookAdapter{aesKey: key, corpID: corpID}

	const sentinel = "S3NT1NEL"
	inner := "<xml><MsgType>text</MsgType><Content>" + sentinel +
		"</Content><FromUserName>user</FromUserName><MsgId>1</MsgId>" +
		"<PicUrl>https://example.com/p.png?sig=" + sentinel + "</PicUrl><MediaId>" + sentinel + "</MediaId></xml>"
	encrypted, _ := encryptWeComTestPayload(t, key, corpID, []byte(inner))
	outer := "<xml><Encrypt>" + encrypted + "</Encrypt></xml>"

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/wecom/callback", strings.NewReader(outer))

	msg, err := adapter.ParseCallback(c)
	if err != nil {
		t.Fatalf("ParseCallback: %v", err)
	}
	if msg == nil || !strings.Contains(msg.Content, sentinel) {
		t.Fatalf("message content = %+v, want sentinel preserved on the returned message", msg)
	}
	logs := buf.String()
	if strings.Contains(logs, sentinel) {
		t.Fatalf("log contains sentinel: %s", logs)
	}
	if !strings.Contains(logs, "body_bytes=") || !strings.Contains(logs, "content_len=") ||
		!strings.Contains(logs, "picurl_len=") || !strings.Contains(logs, "mediaid_len=") {
		t.Fatalf("log missing length fields: %s", logs)
	}
}

func TestDownloadFromURLDebugLogOmitsFileName(t *testing.T) {
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	logger.SetLogLevel(logger.LevelDebug)
	t.Cleanup(logger.ConfigureFromEnv)

	const sentinel = "S3NT1NEL"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", `attachment; filename="`+sentinel+`.pdf"`)
		_, _ = w.Write([]byte("%PDF"))
	}))
	t.Cleanup(srv.Close)
	saved := httpClient
	httpClient = srv.Client()
	t.Cleanup(func() { httpClient = saved })
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse server URL: %v", err)
	}

	body, fileName, err := downloadFromURL(context.Background(), srv.URL+"/media", "", u.Hostname())
	if err != nil {
		t.Fatalf("downloadFromURL: %v", err)
	}
	_, _ = io.Copy(io.Discard, body)
	_ = body.Close()
	if fileName != sentinel+".pdf" {
		t.Fatalf("fileName = %q, want the Content-Disposition name", fileName)
	}
	logs := buf.String()
	if strings.Contains(logs, sentinel) {
		t.Fatalf("log contains sentinel: %s", logs)
	}
	if !strings.Contains(logs, "content_disposition_len=") {
		t.Fatalf("log missing content_disposition_len: %s", logs)
	}
}

func TestLongConnCallbackDebugLogOmitsFileURLs(t *testing.T) {
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	logger.SetLogLevel(logger.LevelDebug)
	t.Cleanup(logger.ConfigureFromEnv)

	const sentinel = "S3NT1NEL"
	var got *im.IncomingMessage
	c := &LongConnClient{handler: func(_ context.Context, msg *im.IncomingMessage) error {
		got = msg
		return nil
	}}
	body := `{"msgid":"m1","msgtype":"image","chattype":"single","from":{"userid":"u1"},` +
		`"image":{"url":"https://example.com/i?sig=` + sentinel + `"}}`
	c.handleCallback(context.Background(), nil, wsFrame{Body: []byte(body)})

	if got == nil || !strings.Contains(got.FileKey, sentinel) {
		t.Fatalf("incoming = %+v, want the URL kept in FileKey", got)
	}
	logs := buf.String()
	if strings.Contains(logs, sentinel) {
		t.Fatalf("log contains sentinel: %s", logs)
	}
	if !strings.Contains(logs, "image_url_len=") || !strings.Contains(logs, "file_url_len=") {
		t.Fatalf("log missing url length fields: %s", logs)
	}

	buf.Reset()
	got = nil
	fileURL := "https://example.com/f?sig=" + sentinel
	fileBody := `{"msgid":"m2","msgtype":"file","chattype":"single","from":{"userid":"u1"},` +
		`"file":{"url":"` + fileURL + `"}}`
	c.handleCallback(context.Background(), nil, wsFrame{Body: []byte(fileBody)})
	if got == nil || got.FileKey != fileURL {
		t.Fatalf("file incoming = %+v, want FileKey %q", got, fileURL)
	}
	logs = buf.String()
	if strings.Contains(logs, sentinel) {
		t.Fatalf("file log contains sentinel: %s", logs)
	}
	if !strings.Contains(logs, "msgtype=file") || !strings.Contains(logs, "file_url_len=") {
		t.Fatalf("file log missing length fields: %s", logs)
	}
}

// The error HandleMessage returns can carry user text (a slash command's arguments, a QA failure); the
// long-connection loop logs only its length and type.
func TestLongConnHandleErrorLogOmitsErrorText(t *testing.T) {
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	logger.SetLogLevel(logger.LevelDebug)
	t.Cleanup(logger.ConfigureFromEnv)

	const sentinel = "S3NT1NEL"
	c := &LongConnClient{handler: func(context.Context, *im.IncomingMessage) error {
		return errors.New("search knowledge: " + sentinel)
	}}
	body := `{"msgid":"m1","msgtype":"text","chattype":"single","from":{"userid":"u1"},"text":{"content":"hi"}}`
	c.handleCallback(context.Background(), nil, wsFrame{Body: []byte(body)})

	logs := buf.String()
	if strings.Contains(logs, sentinel) {
		t.Fatalf("log contains sentinel: %s", logs)
	}
	if !strings.Contains(logs, "Handle message error: error_len=") {
		t.Fatalf("log missing the handle error line: %s", logs)
	}
}
