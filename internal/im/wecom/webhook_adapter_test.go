package wecom

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
		"<PicUrl>https://example.com/p.png</PicUrl><MediaId>media-1</MediaId></xml>"
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
	if !strings.Contains(logs, "body_bytes=") || !strings.Contains(logs, "content_len=") {
		t.Fatalf("log missing length fields: %s", logs)
	}
}
