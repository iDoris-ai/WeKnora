package middleware

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/logger"
	secutils "github.com/Tencent/WeKnora/internal/utils"
	"github.com/gin-gonic/gin"
)

func TestSanitizeBody(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "browser device credentials",
			in: `{"pairing_link":"wss://example.com/#secret",` +
				`"next_token":"new-secret","deviceToken":"device-secret"}`,
			want: `{"pairing_link":"***","next_token":"***","deviceToken":"***"}`,
		},
		{
			name: "camelCase apiKey",
			in:   `{"modelName":"gpt-5.2","apiKey":"sk-secret-123","provider":"azure_openai"}`,
			want: `{"modelName":"gpt-5.2","apiKey":"***","provider":"azure_openai"}`,
		},
		{
			name: "snake_case api_key",
			in:   `{"api_key":"sk-secret-123"}`,
			want: `{"api_key":"***"}`,
		},
		{
			name: "PascalCase APIKey",
			in:   `{"APIKey":"sk-secret-123"}`,
			want: `{"APIKey":"***"}`,
		},
		{
			name: "secretKey camelCase",
			in:   `{"secretKey":"abc","accessKeyId":"id"}`,
			want: `{"secretKey":"***","accessKeyId":"id"}`,
		},
		{
			name: "refreshToken / accessToken camelCase",
			in:   `{"refreshToken":"rt","accessToken":"at"}`,
			want: `{"refreshToken":"***","accessToken":"***"}`,
		},
		{
			name: "password and token preserved as masked",
			in:   `{"password":"p","token":"t"}`,
			want: `{"password":"***","token":"***"}`,
		},
		{
			name: "sandbox terminal handshake ticket in JSON body",
			in:   `{"success":true,"data":{"ticket":"eyJhbGciOiJIUzI1NiJ9.payload.signature","expires_in":120}}`,
			want: `{"success":true,"data":{"ticket":"***","expires_in":120}}`,
		},
		{
			name: "snake_case new_password and old_password",
			in:   `{"email":"alice@example.com","new_password":"FreshPass9","old_password":"OldPass9"}`,
			want: `{"email":"alice@example.com","new_password":"***","old_password":"***"}`,
		},
		{
			name: "extra whitespace around colon",
			in:   `{"apiKey"  :   "leak"}`,
			want: `{"apiKey":"***"}`,
		},
		{
			name: "MCP OAuth JWT bearer assertion",
			in:   `{"assertion":"eyJhbGciOiJFUzI1NiJ9.payload.signature","redirect_uri":"https://example.com/cb"}`,
			want: `{"assertion":"***","redirect_uri":"https://example.com/cb"}`,
		},
		{
			name: "non sensitive fields untouched",
			in:   `{"baseUrl":"https://example.com","modelName":"gpt"}`,
			want: `{"baseUrl":"https://example.com","modelName":"gpt"}`,
		},
		{
			name: "OAuth authorization response fields",
			in:   `{"authorization_url":"https://idp.example/authorize?state=secret","authorization_attempt":"secret-state"}`,
			want: `{"authorization_url":"***","authorization_attempt":"***"}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeBody(tc.in)
			if got != tc.want {
				t.Errorf("sanitizeBody(%q)\n got: %s\nwant: %s", tc.in, got, tc.want)
			}
		})
	}
}

// The logging middleware is registered ahead of Auth and of every route
// handler, so whatever it buffers is buffered before a route's own body cap can
// refuse the request. These tests pin both halves of that: the middleware stays
// inside its own log budget, and the handler still reads every byte.

// countingBody records how many bytes a reader actually pulled, so a test can
// tell "the middleware peeked" from "the middleware slurped the request".
type countingBody struct {
	io.Reader
	read int
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.read += n
	return n, err
}

func (b *countingBody) Close() error { return nil }

// newJSONContext returns a context whose body is a countingBody, plus that
// counter, so a test can observe how much the middleware consumed.
func newJSONContext(t *testing.T, path, body string) (*gin.Context, *countingBody) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, path, nil)
	counter := &countingBody{Reader: strings.NewReader(body)}
	c.Request.Body = counter
	c.Request.ContentLength = int64(len(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c, counter
}

func TestReadRequestBodyKeepsShortBodyIntact(t *testing.T) {
	payload := `{"query":"refund policy","apiKey":"sk-secret"}`

	c, _ := newJSONContext(t, "/api/v1/knowledge-search", payload)
	got := readRequestBody(c)

	want := `{"query":"refund policy","apiKey":"***"}`
	if got != want {
		t.Fatalf("readRequestBody() = %q, want %q", got, want)
	}
	rest, err := io.ReadAll(c.Request.Body)
	if err != nil {
		t.Fatalf("handler read: %v", err)
	}
	if string(rest) != payload {
		t.Fatalf("handler saw %q, want the untouched body %q", rest, payload)
	}
}

// A request larger than the log budget must cost the middleware no more than the
// log budget (plus one byte, to tell "exactly at the limit" from "over it").
func TestReadRequestBodyStaysInsideItsLogBudget(t *testing.T) {
	payload := strings.Repeat("a", maxBodySize*8)

	c, counter := newJSONContext(t, "/api/v1/knowledge-search", payload)
	got := readRequestBody(c)

	if budget := maxBodySize + 1; counter.read > budget {
		t.Fatalf("middleware read %d bytes of a %d byte request, want at most %d",
			counter.read, len(payload), budget)
	}
	if want := maxBodySize + len(bodyTruncatedMarker); len(got) != want {
		t.Fatalf("logged body is %d bytes, want %d (%d logged + %q)",
			len(got), want, maxBodySize, bodyTruncatedMarker)
	}
	if !strings.HasSuffix(got, bodyTruncatedMarker) {
		t.Fatalf("logged body %q does not end with the truncation marker", got)
	}
	rest, err := io.ReadAll(c.Request.Body)
	if err != nil {
		t.Fatalf("handler read: %v", err)
	}
	if len(rest) != len(payload) {
		t.Fatalf("handler saw %d bytes, want all %d of the request", len(rest), len(payload))
	}
}

// The peek must not become a cap the handler inherits: the route's own
// http.MaxBytesReader still sees the whole request and still decides.
func TestReadRequestBodyLeavesRouteBodyCapDeciding(t *testing.T) {
	const routeCap = 64 << 10
	payload := strings.Repeat("x", routeCap*2)

	c, _ := newJSONContext(t, "/api/v1/skills/catalog", payload)
	readRequestBody(c)
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, routeCap)

	_, err := io.ReadAll(c.Request.Body)
	var tooLarge *http.MaxBytesError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("MaxBytesReader error = %v, want *http.MaxBytesError", err)
	}
}

func TestReadRequestBodySkipsNonTextBodies(t *testing.T) {
	payload := "--x\r\nContent-Disposition: form-data; name=\"file\"\r\n\r\nbody\r\n--x--\r\n"

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/knowledge/file", strings.NewReader(payload))
	c.Request.Header.Set("Content-Type", "multipart/form-data; boundary=x")

	if got := readRequestBody(c); got != "[非文本类型，已跳过]" {
		t.Fatalf("readRequestBody() = %q, want the non-text marker", got)
	}
	rest, err := io.ReadAll(c.Request.Body)
	if err != nil || string(rest) != payload {
		t.Fatalf("non-text body was disturbed: %q (%v)", rest, err)
	}
}

func TestSanitizeQuery(t *testing.T) {
	got := secutils.RedactQueryValues("code=secret-code&state=secret-state&next=%2Fsettings&state=second")
	want := "code=%2A%2A%2A&next=%2A%2A%2A&state=%2A%2A%2A"
	if got != want {
		t.Fatalf("RedactQueryValues() = %q, want %q", got, want)
	}
}

// The sandbox terminal presents its handshake credential as a query parameter
// because a browser WebSocket upgrade cannot carry Authorization. Holding that
// value for its TTL is enough to open a shell in the session's sandbox, so the
// redaction is a security boundary, not cosmetics.
func TestSanitizeQueryRedactsTerminalTicket(t *testing.T) {
	got := secutils.RedactQueryValues("ticket=eyJhbGciOiJIUzI1NiJ9.payload.signature&provision=1&cols=120")
	want := "cols=%2A%2A%2A&provision=%2A%2A%2A&ticket=%2A%2A%2A"
	if got != want {
		t.Fatalf("RedactQueryValues() = %q, want %q", got, want)
	}
	if strings.Contains(got, "signature") || strings.Contains(got, "cols=120") ||
		strings.Contains(got, "provision=1") {
		t.Fatalf("RedactQueryValues() leaked a query value: %q", got)
	}
}

// agent24RunAccessLog serves, through RequestID and Logger with AGENT24_LOG_HTTP_BODY set to bodySwitch, a
// JSON POST of payload, GET /r/S3NT1NEL-token, GET /api/v1/organizations/preview/S3NT1NEL-code, an unmatched
// POST /r/S3NT1NEL-token and GET /api/v1/search?q=S3NT1NEL-query&sig=S3NT1NEL-sig at debug level, and
// returns the log output and the body the POST handler read.
func agent24RunAccessLog(t *testing.T, bodySwitch, payload string) (string, string) {
	t.Helper()
	t.Setenv(agent24LogHTTPBodyEnv, bodySwitch)
	logger.SetLogLevel(logger.LevelDebug)
	var buf bytes.Buffer
	logger.SetOutput(&buf)
	defer logger.SetOutput(os.Stdout)

	gin.SetMode(gin.TestMode)
	var gotBody string
	r := gin.New()
	r.Use(RequestID(), Logger())
	r.POST("/api/v1/x", func(c *gin.Context) {
		body, _ := io.ReadAll(c.Request.Body)
		gotBody = string(body)
		c.JSON(http.StatusOK, gin.H{"echo": gotBody})
	})
	r.GET("/r/:token", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	r.GET("/api/v1/search", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.GET("/api/v1/organizations/preview/:code", func(c *gin.Context) { c.Status(http.StatusOK) })

	req := httptest.NewRequest(http.MethodPost, "/api/v1/x", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(httptest.NewRecorder(), req)
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/r/S3NT1NEL-token", nil))
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet,
		"/api/v1/organizations/preview/S3NT1NEL-code", nil))
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/r/S3NT1NEL-token", nil))
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet,
		"/api/v1/search?q=S3NT1NEL-query&sig=S3NT1NEL-sig", nil))
	return buf.String(), gotBody
}

// Sentinel negative case (A24-OBS-01): by default neither body is read or logged, a credential path
// parameter is logged as the route template and every query value as ***; the handler still reads the
// whole body. The response echoes the request, so a logged response body would leak too.
func TestAccessLogOmitsBodiesAndCredentialsByDefault(t *testing.T) {
	payload := `{"note":"S3NT1NEL-body","hmac_secret":"S3NT1NEL-secret"}`
	logText, gotBody := agent24RunAccessLog(t, "", payload)
	if gotBody != payload {
		t.Fatalf("handler saw %q, want %q", gotBody, payload)
	}
	if strings.Contains(logText, "S3NT1NEL") {
		t.Fatalf("access log contains a sentinel:\n%s", logText)
	}
	for _, want := range []string{
		"/r/:token", "preview/:code", agent24UnmatchedRoutePath, "q=%2A%2A%2A", "sig=%2A%2A%2A", "status_code",
	} {
		if !strings.Contains(logText, want) {
			t.Fatalf("access log missing %q:\n%s", want, logText)
		}
	}
}

// With the switch on, bodies are logged as before: sanitizeBody masks password by field name, but body text
// is in the log (here the echo, whose escaped quotes the field regex does not match), so only the path and
// query sentinels are asserted absent.
func TestAccessLogHTTPBodyEnabledRedactsPasswordAndCredentials(t *testing.T) {
	payload := `{"password":"S3NT1NEL-secret","note":"visible-note"}`
	logText, gotBody := agent24RunAccessLog(t, "true", payload)
	if gotBody != payload {
		t.Fatalf("handler saw %q, want %q", gotBody, payload)
	}
	for _, want := range []string{"request_body", `"password":"***"`, "visible-note", "/r/:token"} {
		if !strings.Contains(logText, want) {
			t.Fatalf("access log missing %q:\n%s", want, logText)
		}
	}
	for _, leaked := range []string{"S3NT1NEL-token", "S3NT1NEL-code", "S3NT1NEL-query", "S3NT1NEL-sig"} {
		if strings.Contains(logText, leaked) {
			t.Fatalf("access log contains %q:\n%s", leaked, logText)
		}
	}
}
