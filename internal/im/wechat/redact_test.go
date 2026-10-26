package wechat

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/im"
	secutils "github.com/Tencent/WeKnora/internal/utils"
)

type redactRoundTripper func(*http.Request) (*http.Response, error)

func (f redactRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// The qrcode in the poll URL can be exchanged for the bot token once scanned.
func TestPollQRCodeStatusTransportErrorRedactsQRCode(t *testing.T) {
	const sentinel = "S3NT1NEL"
	s := &QRCodeService{client: &http.Client{Transport: redactRoundTripper(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection reset")
	})}}
	_, err := s.PollQRCodeStatus(context.Background(), sentinel)
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Fatalf("error contains qrcode: %v", err)
	}
	if !strings.Contains(err.Error(), "request qrcode status:") {
		t.Fatalf("error = %v, want request qrcode status failure", err)
	}
}

// The CDN download URL carries encrypted_query_param.
func TestDownloadFileTransportErrorRedactsQueryParam(t *testing.T) {
	const sentinel = "S3NT1NEL"
	secutils.SetSSRFWhitelistFromRaw("novac2c.cdn.weixin.qq.com")
	t.Cleanup(secutils.ResetSSRFWhitelistForTest)
	saved := ilinkHTTPClient
	ilinkHTTPClient = &http.Client{Transport: redactRoundTripper(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection reset")
	})}
	t.Cleanup(func() { ilinkHTTPClient = saved })

	a := NewAdapter("bot-token", "bot-id")
	_, _, err := a.DownloadFile(context.Background(), &im.IncomingMessage{
		FileKey:  "https://novac2c.cdn.weixin.qq.com/c2c/download?encrypted_query_param=" + sentinel,
		FileName: "a.bin",
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Fatalf("error contains encrypted_query_param: %v", err)
	}
	if !strings.Contains(err.Error(), "download file:") {
		t.Fatalf("error = %v, want download failure", err)
	}
}
