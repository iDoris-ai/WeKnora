// Agent24 fork product patch A24-KB-17 (CONTRACT-READONLY-v1 r10 R3.11,
// §6 policy_denied, WeKnora#42): the send-time destination check in Do.

package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	secutils "github.com/Tencent/WeKnora/internal/utils"
)

// AuthFunc stamps credentials onto an outbound request. body is the exact
// bytes being sent so signing vendors can hash them.
type AuthFunc func(req *http.Request, body []byte)

// BearerAuth is the default: Authorization: Bearer <key>.
func BearerAuth(apiKey string) AuthFunc {
	return func(req *http.Request, _ []byte) {
		if apiKey != "" {
			req.Header.Set("Authorization", "Bearer "+apiKey)
		}
	}
}

// HeaderAuth writes the key under an arbitrary header (api-key for Azure,
// x-api-key for Anthropic, x-goog-api-key for Gemini).
func HeaderAuth(header, apiKey string) AuthFunc {
	return func(req *http.Request, _ []byte) {
		if apiKey != "" {
			req.Header.Set(header, apiKey)
		}
	}
}

// NoAuth sends no credentials at all (local deployments).
func NoAuth() AuthFunc { return func(*http.Request, []byte) {} }

// Endpoint is everything a protocol client needs to reach one model.
type Endpoint struct {
	// BaseURL is the vendor root without a trailing slash.
	BaseURL string
	// URL, when set, is the complete request URL and overrides the protocol
	// default of BaseURL + protocol path. Vendors with non-standard paths
	// (Azure deployments, WeKnora Cloud) set it.
	URL string
	// Query is appended to the final URL (Azure api-version).
	Query map[string]string
	// Model is the name sent on the wire; ModelID is WeKnora's own identifier.
	Model   string
	ModelID string
	// Auth stamps credentials; Headers are static extra headers (vendor
	// betas, user-configured custom headers). User headers never override
	// the protocol's own reserved headers.
	Auth    AuthFunc
	Headers map[string]string
	// Client is the HTTP client; nil uses the shared SSRF-safe client.
	Client *http.Client
}

// Resolve builds the request URL from the endpoint description.
func (e Endpoint) Resolve(defaultPath string) string {
	target := e.URL
	if target == "" {
		target = strings.TrimRight(e.BaseURL, "/") + defaultPath
	}
	if len(e.Query) == 0 {
		return target
	}
	var b strings.Builder
	b.WriteString(target)
	sep := "?"
	if strings.Contains(target, "?") {
		sep = "&"
	}
	for k, v := range e.Query {
		b.WriteString(sep)
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(v)
		sep = "&"
	}
	return b.String()
}

func (e Endpoint) httpClient() *http.Client {
	if e.Client != nil {
		return e.Client
	}
	return HTTPClient
}

// NewRequest prepares an authenticated JSON POST. The body is marshalled
// once so signing vendors see exactly what is sent.
func (e Endpoint) NewRequest(ctx context.Context, url string, body any, stream bool) (*http.Request, []byte, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal request: %w", err)
	}
	req, err := e.newPost(ctx, url, data, "application/json")
	if err != nil {
		return nil, nil, err
	}
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	return req, data, nil
}

// newPost prepares an authenticated POST of an already-encoded body.
func (e Endpoint) newPost(ctx context.Context, url string, data []byte, contentType string) (*http.Request, error) {
	// Agent24 A24-KB-17: under a processing guard the destination is checked first, so a URL that does not
	// parse is a refusal too, and no later error quotes it.
	if err := types.CheckProcessingRequestURL(ctx, url); err != nil {
		return nil, err
	}
	if err := secutils.ValidateURLForSSRF(url); err != nil {
		return nil, fmt.Errorf("endpoint SSRF check failed: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	if e.Auth != nil {
		e.Auth(req, data)
	}
	// User headers are applied last but the helper skips reserved names so
	// they cannot clobber auth or content negotiation.
	secutils.ApplyCustomHeaders(req, e.Headers)
	return req, nil
}

// Do sends the request and returns the response. Non-2xx responses are
// turned into an error carrying the body so callers surface vendor messages.
//
// Agent24 A24-KB-17: under a processing guard the URL actually sent, and every
// redirect target, must normalize to the endpoint the guard approved (the URL
// is built after the decision, from a configured path or a vendor hook). A
// refusal sends nothing further and is returned bare, without the URL that
// net/http would quote, and is not a TransportError, so it is not retried.
func (e Endpoint) Do(req *http.Request) (*http.Response, error) {
	client := e.httpClient()
	if types.ProcessingGuardFromContext(req.Context()) != nil {
		if err := types.CheckProcessingRequestURL(req.Context(), req.URL.String()); err != nil {
			return nil, err
		}
		client = guardedProcessingClient(client)
	}
	resp, err := client.Do(req)
	if err != nil {
		if errors.Is(err, types.ErrProcessingDenied) {
			return nil, fmt.Errorf("%w", types.ErrProcessingDenied)
		}
		return nil, &TransportError{Op: "send request", Err: processingRedact(req.Context(), err)}
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		httpErr := &HTTPError{StatusCode: resp.StatusCode, Body: string(body), Header: resp.Header}
		if types.ProcessingGuardFromContext(req.Context()) != nil {
			// A vendor body can echo the request path and query; under a guard the text keeps the status only.
			return nil, &processingRedactedError{
				msg: fmt.Sprintf("API request failed with status %d (body_len=%d)", resp.StatusCode, len(body)),
				err: httpErr,
			}
		}
		return nil, httpErr
	}
	return resp, nil
}

// processingRedactedError carries an error raised under a processing guard with a text that quotes nothing from
// the request or the reply. Unwrap keeps the original, so errors.Is and errors.As (a deadline, an HTTPError's
// status) still classify it; only the text changes.
type processingRedactedError struct {
	msg string
	err error
}

func (e *processingRedactedError) Error() string { return e.msg }
func (e *processingRedactedError) Unwrap() error { return e.err }

// processingRedact hides the text of a transport error under a processing guard: net/http quotes the URL (path
// and query, also a redirect Location, and again inside the cause when the Location does not parse). The text
// keeps the operation and the cause's type. Without a guard the error is returned as it was.
func processingRedact(ctx context.Context, err error) error {
	if types.ProcessingGuardFromContext(ctx) == nil {
		return err
	}
	var ue *url.Error
	if errors.As(err, &ue) {
		return &processingRedactedError{msg: fmt.Sprintf("%s: request failed (%T)", ue.Op, ue.Err), err: err}
	}
	return &processingRedactedError{msg: fmt.Sprintf("request failed (%T)", err), err: err}
}

// guardedProcessingClient is c with each redirect target checked against the
// approved endpoint before the redirect policy c already has, and again after
// that policy returns nil: the caller's CheckRedirect may have rewritten req.URL.
func guardedProcessingClient(c *http.Client) *http.Client {
	guarded := *c
	next := c.CheckRedirect
	guarded.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		// Agent24 A24-KB-21: the context is taken before next runs, so a next that swaps it cannot drop the guard.
		ctx := req.Context()
		if err := types.CheckProcessingRequestURL(ctx, req.URL.String()); err != nil {
			return err
		}
		if next != nil {
			if err := next(req, via); err != nil {
				return err
			}
			// Agent24 A24-KB-21: next may have rewritten req.URL after the check above.
			return types.CheckProcessingRequestURL(ctx, req.URL.String())
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return &guarded
}

// PostJSON sends one authenticated JSON request and decodes the reply into
// out. It is the whole round-trip for the non-streaming protocols (rerank,
// embeddings), which have no SSE to assemble.
//
// It never returns a nil error with nothing decoded: every failure path —
// marshalling, SSRF, transport, non-2xx, decoding — returns an error, so a
// caller cannot mistake an empty result for a successful empty answer.
func (e Endpoint) PostJSON(ctx context.Context, url string, body, out any) error {
	req, _, err := e.NewRequest(ctx, url, body, false)
	if err != nil {
		return err
	}
	// No LogRequest here: the chat protocols log their own bodies because a
	// prompt is what an operator debugs, while a rerank body is a query plus
	// every candidate chunk. The rerank layer logs a truncated line at Debug
	// instead, so this would have duplicated it at Info.
	return e.roundTrip(req, out)
}

// roundTrip sends a prepared request and decodes a 2xx reply into out.
func (e Endpoint) roundTrip(req *http.Request, out any) error {
	resp, err := e.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		// The connection broke before the reply arrived whole: nothing was
		// received, so this is the network failing, not the vendor answering.
		// Agent24 A24-KB-21: a read error can quote bytes from the reply. Under a
		// guard the text is redacted; the result stays a TransportError.
		return &TransportError{Op: "read response", Err: processingRedact(req.Context(), err)}
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		// Agent24 A24-KB-21: a decode error quotes number literals from the reply.
		if types.ProcessingGuardFromContext(req.Context()) != nil {
			return RedactReplyError(req.Context(), "decode response", err)
		}
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// RedactReplyError (Agent24 A24-KB-21): under a processing guard, an error built from what a vendor replied
// (a decode error, an embedding index) keeps only the operation and the error's type. Unwrap keeps the
// original. Without a guard, or for a nil error, err is returned as it was.
func RedactReplyError(ctx context.Context, op string, err error) error {
	if err == nil || types.ProcessingGuardFromContext(ctx) == nil {
		return err
	}
	return &processingRedactedError{msg: fmt.Sprintf("%s: invalid reply (%T)", op, err), err: err}
}

// TransportError is a request that never got a whole answer: DNS, connect,
// TLS, a reset or a timeout, while sending or while reading the reply. It is
// the only failure worth sending again.
type TransportError struct {
	// Op is the phase that failed: "send request" or "read response".
	Op  string
	Err error
}

func (e *TransportError) Error() string { return e.Op + ": " + e.Err.Error() }
func (e *TransportError) Unwrap() error { return e.Err }

// HTTPError is a non-2xx vendor reply.
type HTTPError struct {
	StatusCode int
	Body       string
	// Header is the reply's headers, kept so a retry can honour the vendor's
	// Retry-After on a 429 or 503 instead of guessing its own backoff.
	Header http.Header
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("API request failed with status %d: %s", e.StatusCode, e.Body)
}

// RetryAfter is how long the vendor asked the caller to wait before trying
// again, from the Retry-After header in either of its two forms (delay in
// seconds or an HTTP date). It is zero when the header is absent, malformed
// or already in the past.
func (e *HTTPError) RetryAfter() time.Duration {
	if e == nil {
		return 0
	}
	return parseRetryAfter(e.Header.Get("Retry-After"), time.Now())
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if secs, err := strconv.Atoi(value); err == nil {
		if secs <= 0 {
			return 0
		}
		return time.Duration(secs) * time.Second
	}
	if at, err := http.ParseTime(value); err == nil {
		if d := at.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

// LogRequest emits the standard request log line. The body is recorded as a
// byte length so prompts stay out of this log, and the endpoint loses its
// userinfo and query values (some vendors take the API key there).
func LogRequest(ctx context.Context, url, model string, body []byte, stream bool) {
	logger.Infof(ctx, "[LLM Request] endpoint=%s, model=%s, stream=%v, request_bytes=%d",
		secutils.RedactURL(url), model, stream, len(body))
}
