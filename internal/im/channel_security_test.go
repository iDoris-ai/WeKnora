package im

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDingtalkOnlySupportsStream(t *testing.T) {
	service := &Service{}
	channel := &IMChannel{Platform: "dingtalk", Mode: "webhook"}
	// No database/connection factory is installed: reject before persistence or IO.
	require.Error(t, service.CreateChannel(channel))
	require.Error(t, service.UpdateChannel(channel))
	require.Error(t, service.StartChannel(channel))
	channel.Mode = "websocket"
	require.NoError(t, validateChannelTransport(channel))
	channel.Mode = ""
	require.NoError(t, validateChannelTransport(channel))
	channel.Platform = "slack"
	channel.Mode = "webhook"
	require.NoError(t, validateChannelTransport(channel))
}

func TestRedactRequestError(t *testing.T) {
	const sentinel = "S3NT1NEL"

	t.Run("telegram path token", func(t *testing.T) {
		err := &url.Error{
			Op:  "Post",
			URL: "https://api.telegram.org/bot" + sentinel + "/sendMessage",
			Err: context.DeadlineExceeded,
		}
		got := RedactRequestError(err)
		require.NotContains(t, got.Error(), sentinel)
		require.Contains(t, got.Error(), "api.telegram.org")
		require.ErrorIs(t, got, context.DeadlineExceeded)
		var uerr *url.Error
		require.ErrorAs(t, got, &uerr)
		require.True(t, uerr.Timeout())
		require.Contains(t, err.URL, sentinel, "the caller's error keeps its URL")
	})

	t.Run("access_token query", func(t *testing.T) {
		err := &url.Error{
			Op:  "Get",
			URL: "https://qyapi.weixin.qq.com/cgi-bin/message/send?access_token=" + sentinel,
			Err: context.DeadlineExceeded,
		}
		got := RedactRequestError(err)
		require.NotContains(t, got.Error(), sentinel)
		require.Contains(t, got.Error(), "qyapi.weixin.qq.com")
		require.ErrorIs(t, got, context.DeadlineExceeded)
	})

	t.Run("corpsecret query", func(t *testing.T) {
		err := &url.Error{
			Op:  "Get",
			URL: "https://qyapi.weixin.qq.com/cgi-bin/gettoken?corpid=ww&corpsecret=" + sentinel,
			Err: context.DeadlineExceeded,
		}
		got := RedactRequestError(err)
		require.NotContains(t, got.Error(), sentinel)
		require.Contains(t, got.Error(), "qyapi.weixin.qq.com")
		require.ErrorIs(t, got, context.DeadlineExceeded)
	})

	t.Run("cause quoting the redirect target", func(t *testing.T) {
		cause := fmt.Errorf("failed to parse Location header %q: invalid URL escape",
			"https://api.telegram.org/bot"+sentinel+"/%zz")
		err := &url.Error{Op: "Get", URL: "https://api.telegram.org/bot" + sentinel + "/getUpdates", Err: cause}
		got := RedactRequestError(err)
		require.NotContains(t, got.Error(), sentinel)
		require.Contains(t, got.Error(), "api.telegram.org")
		require.ErrorIs(t, got, cause)
	})

	t.Run("cause wrapping a url.Error from the SSRF redirect check", func(t *testing.T) {
		inner := &url.Error{
			Op: "parse", URL: "https://https:qyapi.weixin.qq.com/cgi-bin/gettoken?corpsecret=" + sentinel,
			Err: errors.New("invalid port"),
		}
		cause := fmt.Errorf("%w: %w", errors.New("redirect blocked"), fmt.Errorf("invalid URL: %w", inner))
		loc := "https:qyapi.weixin.qq.com/cgi-bin/gettoken?corpsecret=" + sentinel
		err := &url.Error{Op: "Get", URL: loc, Err: cause}
		got := RedactRequestError(err)
		require.NotContains(t, got.Error(), sentinel)
		require.ErrorIs(t, got, cause)
	})

	t.Run("network cause keeps its text", func(t *testing.T) {
		cause := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}
		err := &url.Error{Op: "Post", URL: "https://api.telegram.org/bot" + sentinel + "/x", Err: cause}
		got := RedactRequestError(err)
		require.Contains(t, got.Error(), "connection refused")
		require.NotContains(t, got.Error(), sentinel)
	})

	t.Run("other error", func(t *testing.T) {
		err := errors.New("plain failure")
		got := RedactRequestError(err)
		require.True(t, got == err)
	})

	t.Run("nil", func(t *testing.T) {
		require.Nil(t, RedactRequestError(nil))
	})
}

// http.Client.Timeout reports its own timeout error as the cause; once wrapped, the *url.Error still says
// Timeout and Temporary.
func TestRedactRequestErrorKeepsClientTimeoutClassification(t *testing.T) {
	const sentinel = "S3NT1NEL"
	client := &http.Client{
		Timeout: 20 * time.Millisecond,
		Transport: redactTestRoundTripper(func(req *http.Request) (*http.Response, error) {
			<-req.Context().Done()
			return nil, req.Context().Err()
		}),
	}
	req, err := http.NewRequest(http.MethodGet, "https://api.telegram.org/bot"+sentinel+"/getUpdates", nil)
	require.NoError(t, err)
	_, err = client.Do(req) // Do fails, there is no body to close
	require.Error(t, err)

	got := RedactRequestError(err)
	require.NotContains(t, got.Error(), sentinel)
	var uerr *url.Error
	require.ErrorAs(t, got, &uerr)
	require.True(t, uerr.Timeout(), "Timeout: %v", got)
	require.True(t, uerr.Temporary(), "Temporary: %v", got)
}

type redactTestRoundTripper func(*http.Request) (*http.Response, error)

func (f redactTestRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRedactRequestErrorHidesURLErrorCause(t *testing.T) {
	const sentinel = "S3NT1NEL"
	cause := &url.Error{
		Op:  "parse",
		URL: "https://api.telegram.org/bot" + sentinel + "/%zz",
		Err: errors.New("invalid URL escape"),
	}
	err := &url.Error{
		Op:  "Get",
		URL: "https://api.telegram.org/bot" + sentinel + "/getUpdates",
		Err: cause,
	}
	got := RedactRequestError(err)
	require.Error(t, got)
	require.NotContains(t, got.Error(), sentinel)
	require.Contains(t, got.Error(), "api.telegram.org")
	require.Contains(t, got.Error(), "*url.Error (len=")
}

// TestRedactRequestErrorForwardsTemporaryForWrappedCause pins the Temporary delegation
// with a fixed cause. The client-timeout test above can miss it: http.Client.Timeout
// sometimes leaves context.DeadlineExceeded as the cause, which is kept as itself and
// implements Temporary on its own.
func TestRedactRequestErrorForwardsTemporaryForWrappedCause(t *testing.T) {
	const sentinel = "S3NT1NEL"
	cause := &temporaryCause{text: "dial " + sentinel}
	err := &url.Error{
		Op:  "Get",
		URL: "https://api.telegram.org/bot" + sentinel + "/getUpdates",
		Err: cause,
	}
	got := RedactRequestError(err)
	require.Error(t, got)
	require.NotContains(t, got.Error(), sentinel)
	require.Contains(t, got.Error(), "temporaryCause")
	var uerr *url.Error
	require.ErrorAs(t, got, &uerr)
	require.True(t, uerr.Temporary(), "Temporary: %v", got)
}

type temporaryCause struct{ text string }

func (e *temporaryCause) Error() string   { return e.text }
func (e *temporaryCause) Temporary() bool { return true }
