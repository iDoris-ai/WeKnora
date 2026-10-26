package im

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
)

// ParseCredentials parses the JSONB credentials field into a map.
func ParseCredentials(data []byte) (map[string]any, error) {
	if len(data) == 0 {
		return map[string]any{}, nil
	}
	var creds map[string]any
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, err
	}
	return creds, nil
}

// GetString safely extracts a string value from a credentials map.
func GetString(creds map[string]any, key string) string {
	if v, ok := creds[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

// GetBool reads a boolean from JSON credentials (bool, string "true"/"1", or non-zero number).
func GetBool(creds map[string]any, key string) bool {
	v, ok := creds[key]
	if !ok {
		return false
	}
	switch x := v.(type) {
	case bool:
		return x
	case string:
		s := strings.TrimSpace(strings.ToLower(x))
		return s == "true" || s == "1" || s == "yes"
	case float64:
		return x != 0
	case int:
		return x != 0
	default:
		return false
	}
}

// RedactRequestError (Agent24 KB-OBS-01) returns err with the request URL of a *url.Error cut down to
// scheme://host: IM APIs carry credentials in the path (Telegram's /bot<token>/) or the query
// (access_token, corpsecret), and net/http quotes the whole URL. Pass the error http.Client.Do returned,
// which is a *url.Error itself; any other error, wrapped ones included, is returned as it was.
func RedactRequestError(err error) error {
	uerr, ok := err.(*url.Error) // not errors.As: a wrapper around it could not be rebuilt
	if !ok || uerr == nil {
		return err
	}
	// Copy so the caller's error keeps its URL. Unwrap stays the original cause, so
	// errors.Is and net.Error.Timeout still see that cause.
	cloned := *uerr
	cloned.URL = requestURLSchemeHost(uerr.URL)
	cloned.Err = redactRequestCause(uerr.Err)
	return &cloned
}

// RedactURLErrorInChain is for errors from URL validation, which wrap url.Parse's
// *url.Error and so quote the whole URL. It returns the first *url.Error in the chain
// redacted, dropping the wrapper text around it (the copy cannot be rewrapped). An
// error without a *url.Error is returned unchanged.
func RedactURLErrorInChain(err error) error {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		return RedactRequestError(uerr)
	}
	return err
}

// redactRequestCause keeps a transport cause that is itself a dial or DNS failure (*net.OpError,
// *net.DNSError). Dial and DNS failures in this repo usually name only a host and port; the SSRF
// dial check returns a fmt.Errorf wrapper and does not take this branch. A deadline, a cancellation
// or EOF is kept too. The cause's own type decides, not anything it wraps: a *url.Error is a
// net.Error too, and the SSRF redirect check wraps one that quotes the whole redirect target. Every
// other cause keeps only its type and length.
func redactRequestCause(cause error) error {
	switch cause.(type) { // the cause's own type, on purpose
	case nil, *net.OpError, *net.DNSError:
		return cause
	}
	// Identity with the sentinels, not errors.Is: a wrapper around one may add text.
	if cause == context.DeadlineExceeded || cause == context.Canceled || cause == io.EOF ||
		cause == io.ErrUnexpectedEOF {
		return cause
	}
	return redactedRequestCause{cause: cause}
}

// redactedRequestCause hides its cause's text; errors.Is and errors.As still reach the cause.
type redactedRequestCause struct{ cause error }

func (e redactedRequestCause) Error() string {
	return fmt.Sprintf("%T (len=%d)", e.cause, len(e.cause.Error()))
}

func (e redactedRequestCause) Unwrap() error { return e.cause }

// Timeout and Temporary let url.Error's methods of the same name see through the wrapper.
func (e redactedRequestCause) Timeout() bool {
	t, ok := e.cause.(interface{ Timeout() bool })
	return ok && t.Timeout()
}

func (e redactedRequestCause) Temporary() bool {
	t, ok := e.cause.(interface{ Temporary() bool })
	return ok && t.Temporary()
}

// requestURLSchemeHost returns scheme://host. Parse failure, or a URL with no
// scheme or host, becomes "" so the original credential-bearing string is dropped.
func requestURLSchemeHost(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}
