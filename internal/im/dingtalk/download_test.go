package dingtalk

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/im"
	secutils "github.com/Tencent/WeKnora/internal/utils"
)

func useTestHTTPClient(t *testing.T) {
	t.Helper()
	original := httpClient
	httpClient = &http.Client{Timeout: 5 * time.Second}
	t.Cleanup(func() { httpClient = original })
}

// TestDownloadFile_EndToEnd drives the full DownloadFile orchestration against a
// fake DingTalk OpenAPI: access-token fetch → messageFiles/download (downloadCode
// → temporary downloadUrl) → GET the temp URL for the bytes. This covers the HTTP
// path that the pure-function unit tests deliberately skip (issue #1771).
func TestDownloadFile_EndToEnd(t *testing.T) {
	useTestHTTPClient(t)
	fileBytes := []byte("%PDF-1.7 fake product spec bytes")

	var downloadReq map[string]string
	var tokenSeen string

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1.0/oauth2/accessToken":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"accessToken": "tok-abc",
				"expireIn":    7200,
			})
		case "/v1.0/robot/messageFiles/download":
			tokenSeen = r.Header.Get("x-acs-dingtalk-access-token")
			_ = json.NewDecoder(r.Body).Decode(&downloadReq)
			_ = json.NewEncoder(w).Encode(map[string]string{
				"downloadUrl": srv.URL + "/temp/file",
			})
		case "/temp/file":
			_, _ = w.Write(fileBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	orig := apiBaseURL
	apiBaseURL = srv.URL
	defer func() { apiBaseURL = orig }()

	origValidate := validateFileDownloadURL
	validateFileDownloadURL = func(string) error { return nil }
	defer func() { validateFileDownloadURL = origValidate }()

	a := &Adapter{clientID: "cid", clientSecret: "sec"}
	msg := &im.IncomingMessage{
		MessageType: im.MessageTypeFile,
		FileKey:     "DL-CODE",
		FileName:    "spec.pdf",
		Extra:       map[string]string{"robot_code": "rc-1"},
	}

	reader, name, err := a.DownloadFile(context.Background(), msg)
	if err != nil {
		t.Fatalf("DownloadFile error: %v", err)
	}
	defer reader.Close()

	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(got) != string(fileBytes) {
		t.Errorf("downloaded bytes = %q, want %q", got, fileBytes)
	}
	if name != "spec.pdf" {
		t.Errorf("resolved name = %q, want %q", name, "spec.pdf")
	}
	if tokenSeen != "tok-abc" {
		t.Errorf("download request auth header = %q, want %q", tokenSeen, "tok-abc")
	}
	if downloadReq["robotCode"] != "rc-1" {
		t.Errorf("robotCode sent = %q, want %q", downloadReq["robotCode"], "rc-1")
	}
	if downloadReq["downloadCode"] != "DL-CODE" {
		t.Errorf("downloadCode sent = %q, want %q", downloadReq["downloadCode"], "DL-CODE")
	}
}

// TestDownloadFile_TempURLError verifies a non-200 from the temporary download
// URL surfaces as an error rather than silently returning empty content.
func TestDownloadFile_TempURLError(t *testing.T) {
	useTestHTTPClient(t)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1.0/oauth2/accessToken":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"accessToken": "tok", "expireIn": 7200})
		case "/v1.0/robot/messageFiles/download":
			_ = json.NewEncoder(w).Encode(map[string]string{"downloadUrl": srv.URL + "/temp/gone"})
		case "/temp/gone":
			http.Error(w, "expired", http.StatusForbidden)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	orig := apiBaseURL
	apiBaseURL = srv.URL
	defer func() { apiBaseURL = orig }()

	origValidate := validateFileDownloadURL
	validateFileDownloadURL = func(string) error { return nil }
	defer func() { validateFileDownloadURL = origValidate }()

	a := &Adapter{clientID: "cid", clientSecret: "sec"}
	msg := &im.IncomingMessage{FileKey: "DL-CODE", FileName: "x.pdf", Extra: map[string]string{"robot_code": "rc"}}

	if _, _, err := a.DownloadFile(context.Background(), msg); err == nil {
		t.Errorf("expected error on non-200 download URL, got nil")
	}
}

func TestDownloadFile_SSRFRejected(t *testing.T) {
	useTestHTTPClient(t)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1.0/oauth2/accessToken":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"accessToken": "tok", "expireIn": 7200})
		case "/v1.0/robot/messageFiles/download":
			_ = json.NewEncoder(w).Encode(map[string]string{"downloadUrl": "http://127.0.0.1:1/internal"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	orig := apiBaseURL
	apiBaseURL = srv.URL
	defer func() { apiBaseURL = orig }()

	a := &Adapter{clientID: "cid", clientSecret: "sec"}
	msg := &im.IncomingMessage{FileKey: "DL-CODE", FileName: "x.pdf", Extra: map[string]string{"robot_code": "rc"}}

	if _, _, err := a.DownloadFile(context.Background(), msg); err == nil {
		t.Fatal("expected SSRF rejection error, got nil")
	}
}

func TestIsAllowedDingTalkDownloadHost(t *testing.T) {
	cases := []struct {
		url   string
		allow bool
	}{
		{"https://wukong-abc.oss-cn-hangzhou.aliyuncs.com/file?sig=x", true},
		{"https://api.dingtalk.com/temp/file", true},
		{"http://127.0.0.1:8080/file", false},
	}
	for _, tc := range cases {
		if got := isAllowedDingTalkDownloadHost(tc.url); got != tc.allow {
			t.Errorf("isAllowedDingTalkDownloadHost(%q) = %v, want %v", tc.url, got, tc.allow)
		}
	}
}

func jsonOK(req *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}

func TestDownloadFileDoRedactsSignedURL(t *testing.T) {
	const sentinel = "S3NT1NEL"
	previous := httpClient
	t.Cleanup(func() { httpClient = previous })
	signed := "https://wukong-abc.oss-cn-hangzhou.aliyuncs.com/file?sig=" + sentinel
	httpClient = &http.Client{Transport: sessionReplyTransport(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/v1.0/oauth2/accessToken":
			return jsonOK(req, `{"accessToken":"tok","expireIn":7200}`), nil
		case "/v1.0/robot/messageFiles/download":
			return jsonOK(req, `{"downloadUrl":"`+signed+`"}`), nil
		default:
			return nil, context.DeadlineExceeded
		}
	})}

	a := &Adapter{clientID: "cid", clientSecret: "sec"}
	_, _, err := a.DownloadFile(context.Background(), &im.IncomingMessage{
		FileKey: "DL-CODE", FileName: "x.pdf", Extra: map[string]string{"robot_code": "rc"},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Fatalf("error contains signed URL secret: %v", err)
	}
	if !strings.Contains(err.Error(), "download file:") || !strings.Contains(err.Error(), "deadline exceeded") {
		t.Fatalf("error = %v, want download failure", err)
	}
}

func TestDownloadFileCreateRequestRedactsUnparseableSignedURL(t *testing.T) {
	const sentinel = "S3NT1NEL"
	secutils.SetSSRFWhitelistFromRaw("1password.com")
	t.Cleanup(secutils.ResetSSRFWhitelistForTest)
	previous := httpClient
	t.Cleanup(func() { httpClient = previous })
	rawURL := "1password.com:443/file?sig=" + sentinel
	httpClient = &http.Client{Transport: sessionReplyTransport(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/v1.0/oauth2/accessToken":
			return jsonOK(req, `{"accessToken":"tok","expireIn":7200}`), nil
		case "/v1.0/robot/messageFiles/download":
			return jsonOK(req, `{"downloadUrl":"`+rawURL+`"}`), nil
		default:
			t.Errorf("unexpected request %s", req.URL.Path)
			return nil, context.DeadlineExceeded
		}
	})}

	a := &Adapter{clientID: "cid", clientSecret: "sec"}
	_, _, err := a.DownloadFile(context.Background(), &im.IncomingMessage{
		FileKey: "DL-CODE", FileName: "x.pdf", Extra: map[string]string{"robot_code": "rc"},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Fatalf("error contains signed URL secret: %v", err)
	}
	if !strings.Contains(err.Error(), "create download request:") {
		t.Fatalf("error = %v, want create request failure", err)
	}
}

func TestDownloadFileRejectedURLRedactsSignedURL(t *testing.T) {
	const sentinel = "S3NT1NEL"
	previous := httpClient
	t.Cleanup(func() { httpClient = previous })
	// The escaped newline decodes to a control character, so the SSRF check's url.Parse
	// fails and its error quotes the signed URL.
	rawURL := "https://files.example.com/file?sig=" + sentinel + "\\n"
	httpClient = &http.Client{Transport: sessionReplyTransport(func(req *http.Request) (*http.Response, error) {
		switch req.URL.Path {
		case "/v1.0/oauth2/accessToken":
			return jsonOK(req, `{"accessToken":"tok","expireIn":7200}`), nil
		case "/v1.0/robot/messageFiles/download":
			return jsonOK(req, `{"downloadUrl":"`+rawURL+`"}`), nil
		default:
			t.Errorf("unexpected request %s", req.URL.Path)
			return nil, context.DeadlineExceeded
		}
	})}

	a := &Adapter{clientID: "cid", clientSecret: "sec"}
	_, _, err := a.DownloadFile(context.Background(), &im.IncomingMessage{
		FileKey: "DL-CODE", FileName: "x.pdf", Extra: map[string]string{"robot_code": "rc"},
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Fatalf("error contains signed URL secret: %v", err)
	}
	if !strings.Contains(err.Error(), "download url rejected:") {
		t.Fatalf("error = %v, want download url rejected", err)
	}
}
