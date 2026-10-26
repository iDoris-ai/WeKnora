package im

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"testing"

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
