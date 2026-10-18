package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	apperrors "github.com/Tencent/WeKnora/internal/errors"
	"github.com/Tencent/WeKnora/internal/models/api"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func agent24WriteErr(
	t *testing.T, e *agent24KnowledgeError, echo agent24KnowledgeEcho,
) (int, map[string]any, map[string]any, string) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	writeAgent24KnowledgeError(c, e, echo)
	var body map[string]map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	return w.Code, body["error"], body["error"]["details"].(map[string]any), w.Body.String()
}

func TestAgent24KnowledgeErrorEnvelope(t *testing.T) {
	rid, gen := "r-1", int64(7)
	cases := []struct {
		e         *agent24KnowledgeError
		status    int
		code, msg string
		retryable bool
		reason    string
	}{
		{agent24InvalidRequest(), 400, "invalid_request", "invalid request", false, ""},
		{
			agent24UnsupportedProtocolVersion([]int{1}), 400, "invalid_request", "invalid request",
			false, "unsupported_protocol_version",
		},
		{agent24Unauthorized("credential_expired"), 401, "unauthorized", "unauthorized", false, "credential_expired"},
		{agent24Forbidden("empty_scope"), 403, "forbidden", "forbidden", false, "empty_scope"},
		{agent24NotFound(), 404, "not_found", "not found", false, ""},
		{&agent24KnowledgeError{Code: agent24CodeStale}, 409, "stale", "revision is not current", false, ""},
		{agent24Timeout(), 504, "timeout", "deadline exceeded", true, ""},
		{agent24Unavailable(""), 503, "unavailable", "service unavailable", true, ""},
		{agent24Unavailable("overloaded"), 503, "unavailable", "service unavailable", true, "overloaded"},
		{agent24Unavailable("index_failed"), 503, "unavailable", "service unavailable", false, "index_failed"},
		{
			agent24Unavailable("unsupported_kb_type"), 503, "unavailable", "service unavailable",
			false, "unsupported_kb_type",
		},
		{agent24Internal(), 500, "internal", "internal error", false, ""},
		// Invalid reasons fall back to internal without reason.
		{agent24Unauthorized("bogus"), 500, "internal", "internal error", false, ""},
		{agent24Forbidden(""), 500, "internal", "internal error", false, ""},
		{agent24Unavailable("timeout"), 500, "internal", "internal error", false, ""},
	}
	for _, tc := range cases {
		status, env, d, _ := agent24WriteErr(t, tc.e, agent24KnowledgeEcho{RequestID: &rid, Generation: &gen})
		require.Equal(t, tc.status, status, tc.code)
		require.Equal(t, tc.code, env["code"])
		require.Equal(t, tc.msg, env["message"])
		require.NotContains(t, env, "hint")
		require.Equal(t, tc.retryable, d["retryable"])
		require.Equal(t, "r-1", d["request_id"])
		require.Equal(t, float64(7), d["generation"])
		if tc.reason == "" {
			require.NotContains(t, d, "reason")
		} else {
			require.Equal(t, tc.reason, d["reason"])
		}
	}

	_, _, d, _ := agent24WriteErr(t, agent24UnsupportedProtocolVersion([]int{1}), agent24KnowledgeEcho{})
	require.Equal(t, []any{float64(1)}, d["supported"])
	require.Contains(t, d, "request_id")
	require.Nil(t, d["request_id"])
	require.Contains(t, d, "generation")
	require.Nil(t, d["generation"])

	e := agent24NotFound()
	e.Extra = map[string]any{"retryable": true, "request_id": "x", "generation": 9, "reason": "leak", "k": "v"}
	_, _, d, _ = agent24WriteErr(t, e, agent24KnowledgeEcho{})
	require.Equal(t, map[string]any{"retryable": false, "request_id": nil, "generation": nil, "k": "v"}, d)
}

func TestAgent24KnowledgeParseCommon(t *testing.T) {
	long := strings.Repeat("a", 129)
	cases := []struct {
		name, body string
		ok         bool
		reason     string
		rid        any // expected echo; nil means null
		gen        any
	}{
		{"not json", `{`, false, "", nil, nil},
		{"array", `[1]`, false, "", nil, nil},
		{"trailing", `{"protocol_version":1,"request_id":"r","generation":1} {}`, false, "", nil, nil},
		{"missing pv", `{"request_id":"r","generation":1}`, false, "", "r", int64(1)},
		{"pv string", `{"protocol_version":"1","request_id":"r","generation":1}`, false, "", "r", int64(1)},
		{
			"pv 2", `{"protocol_version":2,"request_id":"r","generation":3}`,
			false, "unsupported_protocol_version", "r", int64(3),
		},
		{"rid long", `{"protocol_version":1,"request_id":"` + long + `","generation":1}`, false, "", nil, int64(1)},
		{"rid empty", `{"protocol_version":1,"request_id":"","generation":1}`, false, "", nil, int64(1)},
		{"rid number", `{"protocol_version":1,"request_id":5,"generation":1}`, false, "", nil, int64(1)},
		{"gen negative", `{"protocol_version":1,"request_id":"r","generation":-1}`, false, "", "r", nil},
		{"gen fraction", `{"protocol_version":1,"request_id":"r","generation":1.5}`, false, "", "r", nil},
		{"gen string", `{"protocol_version":1,"request_id":"r","generation":"3"}`, false, "", "r", nil},
		{"valid", `{"protocol_version":1,"request_id":"r","generation":0,"query":"q"}`, true, "", "r", int64(0)},
	}
	for _, tc := range cases {
		fields, echo, e := parseAgent24KnowledgeCommon([]byte(tc.body))
		if tc.ok {
			require.Nil(t, e, tc.name)
			require.JSONEq(t, `"q"`, string(fields["query"]))
		} else {
			require.NotNil(t, e, tc.name)
			require.Equal(t, agent24CodeInvalidRequest, e.Code, tc.name)
			require.Equal(t, tc.reason, e.Reason, tc.name)
		}
		if tc.reason != "" {
			require.Equal(t, []int{1}, e.Extra["supported"])
		}
		if tc.rid == nil {
			require.Nil(t, echo.RequestID, tc.name)
		} else {
			require.Equal(t, tc.rid, *echo.RequestID, tc.name)
		}
		if tc.gen == nil {
			require.Nil(t, echo.Generation, tc.name)
		} else {
			require.Equal(t, tc.gen, *echo.Generation, tc.name)
		}
	}
}

func TestAgent24KnowledgeDeadline(t *testing.T) {
	for in, want := range map[int64]int{0: 30000, -5: 30000, 99999: 30000, 1500: 1500} {
		d, ms := agent24KnowledgeDeadline(in)
		require.Equal(t, want, ms)
		require.Equal(t, time.Duration(want)*time.Millisecond, d)
	}
}

func TestAgent24KnowledgeClassify(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	e, gone := classifyAgent24KnowledgeExecError(canceled, errors.New("x"))
	require.True(t, gone)
	require.Nil(t, e)

	expired, cancel2 := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel2()
	bg := context.Background()
	vendor := func(code int) error { return &api.HTTPError{StatusCode: code, Body: "vendor secret"} }
	transport := &api.TransportError{Op: "send request", Err: errors.New("dial")}
	cases := []struct {
		name   string
		ctx    context.Context
		err    error
		code   agent24KnowledgeCode
		reason string
	}{
		{"live deadline", bg, context.DeadlineExceeded, agent24CodeUnavailable, "dependency"},
		{"expired deadline", expired, context.DeadlineExceeded, agent24CodeTimeout, ""},
		{"live vector", bg, apperrors.NewVectorStoreUnavailableError("s"), agent24CodeUnavailable, "dependency"},
		{"expired vector", expired, apperrors.NewVectorStoreUnavailableError("s"), agent24CodeTimeout, ""},
		{"live apptimeout", bg, &apperrors.AppError{Code: apperrors.ErrTimeout}, agent24CodeUnavailable, "dependency"},
		{"expired apptimeout", expired, &apperrors.AppError{Code: apperrors.ErrTimeout}, agent24CodeTimeout, ""},
		{"expired svc", expired, apperrors.NewServiceUnavailableError("s"), agent24CodeUnavailable, "dependency"},
		{"expired 429", expired, apperrors.NewTooManyRequestsError("s"), agent24CodeUnavailable, "overloaded"},
		{"not found", bg, apperrors.NewNotFoundError("s"), agent24CodeInternal, ""},
		{"nil apperr", bg, (*apperrors.AppError)(nil), agent24CodeInternal, ""},
		{"http 429", bg, vendor(429), agent24CodeUnavailable, "overloaded"},
		{"http 503", bg, vendor(503), agent24CodeUnavailable, "dependency"},
		{"http 400", bg, vendor(400), agent24CodeInternal, ""},
		{"live transport", bg, transport, agent24CodeUnavailable, "dependency"},
		{"expired transport", expired, transport, agent24CodeTimeout, ""},
		{"expired other", expired, errors.New("x"), agent24CodeInternal, ""},
		{"secret", bg, errors.New("secret text"), agent24CodeInternal, ""},
	}
	for _, tc := range cases {
		e, gone := classifyAgent24KnowledgeExecError(tc.ctx, tc.err)
		require.False(t, gone, tc.name)
		require.Equal(t, tc.code, e.Code, tc.name)
		require.Equal(t, tc.reason, e.Reason, tc.name)
		_, _, _, raw := agent24WriteErr(t, e, agent24KnowledgeEcho{})
		require.NotContains(t, raw, "vendor secret", tc.name)
		require.NotContains(t, raw, "secret text", tc.name)
	}

	status, env, _, _ := agent24WriteErr(t, nil, agent24KnowledgeEcho{})
	require.Equal(t, 500, status)
	require.Equal(t, "internal", env["code"])
}
