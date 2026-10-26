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
	"time"

	"github.com/Tencent/WeKnora/internal/im"
	"github.com/Tencent/WeKnora/internal/logger"
	secutils "github.com/Tencent/WeKnora/internal/utils"
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

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func TestGetAccessTokenRedactsCorpSecret(t *testing.T) {
	const sentinel = "S3NT1NEL"
	saved := httpClient
	httpClient = &http.Client{
		Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, context.DeadlineExceeded
		}),
	}
	t.Cleanup(func() { httpClient = saved })

	adapter := &WebhookAdapter{
		corpID:      "ww_corp",
		agentSecret: sentinel,
		apiBaseURL:  "https://qyapi.weixin.qq.com",
	}
	_, err := adapter.getAccessToken(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Fatalf("error contains corpsecret: %v", err)
	}
}

// url.Parse accepts %zz and spaces in a query. A control character is what makes
// NewRequest fail while the credential is still in the URL string.
func TestGetAccessTokenNewRequestRedactsCorpSecret(t *testing.T) {
	const sentinel = "S3NT1NEL"
	saved := httpClient
	httpClient = &http.Client{
		Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, context.DeadlineExceeded
		}),
	}
	t.Cleanup(func() { httpClient = saved })

	adapter := &WebhookAdapter{
		corpID:      "ww",
		agentSecret: sentinel + "\n",
		apiBaseURL:  "https://qyapi.weixin.qq.com",
	}
	_, err := adapter.getAccessToken(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Fatalf("error contains corpsecret: %v", err)
	}
	if strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("Do was reached, want a URL parse error: %v", err)
	}
	if !strings.Contains(err.Error(), "create request:") {
		t.Fatalf("error = %v, want create request failure", err)
	}
}

func TestSendToAppChatNewRequestRedactsAccessToken(t *testing.T) {
	const sentinel = "S3NT1NEL"
	saved := httpClient
	httpClient = &http.Client{
		Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, context.DeadlineExceeded
		}),
	}
	t.Cleanup(func() { httpClient = saved })

	a := &WebhookAdapter{apiBaseURL: "https://qyapi.weixin.qq.com"}
	err := a.sendToAppChat(context.Background(), sentinel+"\n", "chat-1", &im.ReplyMessage{Content: "hi"})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Fatalf("error contains access token: %v", err)
	}
	if strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("Do was reached, want a URL parse error: %v", err)
	}
	if !strings.Contains(err.Error(), "create request:") {
		t.Fatalf("error = %v, want create request failure", err)
	}
}

func TestSendToUserNewRequestRedactsAccessToken(t *testing.T) {
	const sentinel = "S3NT1NEL"
	saved := httpClient
	httpClient = &http.Client{
		Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, context.DeadlineExceeded
		}),
	}
	t.Cleanup(func() { httpClient = saved })

	a := &WebhookAdapter{apiBaseURL: "https://qyapi.weixin.qq.com"}
	err := a.sendToUser(context.Background(), sentinel+"\n", "user-1", &im.ReplyMessage{Content: "hi"})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Fatalf("error contains access token: %v", err)
	}
	if strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("Do was reached, want a URL parse error: %v", err)
	}
	if !strings.Contains(err.Error(), "create request:") {
		t.Fatalf("error = %v, want create request failure", err)
	}
}

func TestSendToAppChatDoRedactsAccessToken(t *testing.T) {
	const sentinel = "S3NT1NEL"
	saved := httpClient
	httpClient = &http.Client{
		Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, context.DeadlineExceeded
		}),
	}
	t.Cleanup(func() { httpClient = saved })

	a := &WebhookAdapter{apiBaseURL: "https://qyapi.weixin.qq.com"}
	err := a.sendToAppChat(context.Background(), sentinel, "chat-1", &im.ReplyMessage{Content: "hi"})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Fatalf("error contains access token: %v", err)
	}
	if !strings.Contains(err.Error(), "send appchat message:") || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("error = %v, want appchat send failure", err)
	}
}

func TestSendToUserDoRedactsAccessToken(t *testing.T) {
	const sentinel = "S3NT1NEL"
	saved := httpClient
	httpClient = &http.Client{
		Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, context.DeadlineExceeded
		}),
	}
	t.Cleanup(func() { httpClient = saved })

	a := &WebhookAdapter{apiBaseURL: "https://qyapi.weixin.qq.com"}
	err := a.sendToUser(context.Background(), sentinel, "user-1", &im.ReplyMessage{Content: "hi"})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Fatalf("error contains access token: %v", err)
	}
	if !strings.Contains(err.Error(), "send message:") || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("error = %v, want message send failure", err)
	}
}

func TestDownloadFromURLDoRedactsAccessToken(t *testing.T) {
	const sentinel = "S3NT1NEL"
	saved := httpClient
	httpClient = &http.Client{
		Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, context.DeadlineExceeded
		}),
	}
	t.Cleanup(func() { httpClient = saved })

	rawURL := "https://qyapi.weixin.qq.com/cgi-bin/media/get?access_token=" + sentinel + "&media_id=m1"
	_, _, err := downloadFromURL(context.Background(), rawURL, "a.bin", "")
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Fatalf("error contains access token: %v", err)
	}
	if !strings.Contains(err.Error(), "download:") || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("error = %v, want download failure", err)
	}
}

// A URL that fails to parse is rejected by the SSRF check before NewRequest runs.
func TestDownloadFromURLUnparseableURLRedactsAccessToken(t *testing.T) {
	const sentinel = "S3NT1NEL"
	rawURL := "https://qyapi.weixin.qq.com/cgi-bin/media/get?access_token=" + sentinel + "&media_id=m1\n"
	_, _, err := downloadFromURL(context.Background(), rawURL, "a.bin", "")
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Fatalf("error contains access token: %v", err)
	}
	if !strings.Contains(err.Error(), "URL rejected for security reasons:") {
		t.Fatalf("error = %v, want the SSRF rejection", err)
	}
}

// A host that starts with a digit, has a port, and has no scheme passes the SSRF
// check (it is parsed as https://host:port/...) and fails only in NewRequest.
func TestDownloadFromURLDigitHostPortWithoutSchemeRedactsAccessToken(t *testing.T) {
	secutils.SetSSRFWhitelistFromRaw("1password.com")
	t.Cleanup(secutils.ResetSSRFWhitelistForTest)
	const sentinel = "S3NT1NEL"
	_, _, err := downloadFromURL(context.Background(),
		"1password.com:443/file?access_token="+sentinel, "f", "")
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Fatalf("error contains access token: %v", err)
	}
	if !strings.Contains(err.Error(), "create request:") {
		t.Fatalf("error = %v, want create request failure", err)
	}
}

func TestSendReplyAppChatFailureLogOmitsAccessToken(t *testing.T) {
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	logger.SetLogLevel(logger.LevelDebug)
	t.Cleanup(logger.ConfigureFromEnv)

	const sentinel = "S3NT1NEL"
	saved := httpClient
	httpClient = &http.Client{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			body := `{"errcode":0,"errmsg":"ok"}`
			if strings.Contains(r.URL.Path, "/cgi-bin/appchat/send") {
				// The vendor errmsg can echo request content; the fallback log keeps only its length.
				body = `{"errcode":86003,"errmsg":"chat not found ` + sentinel + `"}`
			}
			if strings.Contains(r.URL.Path, "/cgi-bin/appchat/send") ||
				strings.Contains(r.URL.Path, "/cgi-bin/message/send") {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(body)),
					Request:    r,
				}, nil
			}
			return nil, errors.New("unexpected request")
		}),
	}
	t.Cleanup(func() { httpClient = saved })

	a := &WebhookAdapter{
		apiBaseURL:  "https://qyapi.weixin.qq.com",
		corpAgentID: 1,
		tokenCache:  sentinel,
		tokenExpAt:  time.Now().Add(time.Hour),
	}
	err := a.SendReply(context.Background(), &im.IncomingMessage{
		ChatType: im.ChatTypeGroup,
		ChatID:   "chat-1",
		UserID:   "user-1",
	}, &im.ReplyMessage{Content: "hi"})
	if err != nil {
		t.Fatalf("SendReply error = %v, want touser fallback success", err)
	}
	logs := buf.String()
	if strings.Contains(logs, sentinel) {
		t.Fatalf("log contains sentinel: %s", logs)
	}
	if !strings.Contains(logs, "appchat/send failed for chat=chat-1") {
		t.Fatalf("log missing appchat failure: %s", logs)
	}
	if !strings.Contains(logs, "error_len=") || !strings.Contains(logs, "error_type=*errors.errorString") {
		t.Fatalf("log missing redacted appchat error: %s", logs)
	}
	if strings.Contains(logs, "error_type=<nil>") {
		t.Fatalf("log error_type is nil: %s", logs)
	}
}

func TestValidateEndpointURLParseErrorOmitsURL(t *testing.T) {
	const sentinel = "S3NT1NEL"
	err := validateEndpointURL("https://qyapi.example.com/%zz?sig="+sentinel, "https://qyapi.weixin.qq.com", "https")
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Fatalf("error contains endpoint secret: %v", err)
	}
	if !strings.Contains(err.Error(), "invalid endpoint URL:") {
		t.Fatalf("error = %v, want invalid endpoint URL", err)
	}
}
