package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/require"
)

func TestDoKeepsHeadersOnHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "12")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limited"}`))
	}))
	defer srv.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, nil)
	require.NoError(t, err)
	_, err = Endpoint{Client: srv.Client()}.Do(req)

	var httpErr *HTTPError
	require.True(t, errors.As(err, &httpErr))
	require.Equal(t, http.StatusTooManyRequests, httpErr.StatusCode)
	require.Equal(t, 12*time.Second, httpErr.RetryAfter())
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name  string
		value string
		want  time.Duration
	}{
		{"absent", "", 0},
		{"seconds", "30", 30 * time.Second},
		{"seconds with spaces", " 5 ", 5 * time.Second},
		{"zero", "0", 0},
		{"negative", "-3", 0},
		{"http date", now.Add(90 * time.Second).Format(http.TimeFormat), 90 * time.Second},
		{"http date in the past", now.Add(-time.Minute).Format(http.TimeFormat), 0},
		{"garbage", "tomorrow", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, parseRetryAfter(tc.value, now))
		})
	}
}

func TestRetryAfterOnNilError(t *testing.T) {
	var e *HTTPError
	require.Zero(t, e.RetryAfter())
	require.Zero(t, (&HTTPError{StatusCode: 503}).RetryAfter())
}

type allowProcessingGuard struct{}

func (allowProcessingGuard) Allow(context.Context, types.ProcessingDestination) error { return nil }

// A caller's CheckRedirect can rewrite req.URL after the guard has checked it (Agent24 A24-KB-21).
// The rewritten host is checked again: the other server sees nothing. Without the wrapper it is reached.
func TestGuardedProcessingClientRechecksURLAfterCheckRedirect(t *testing.T) {
	bHits := 0
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		bHits++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(b.Close)
	var a *httptest.Server
	a = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, a.URL+"/next", http.StatusFound)
	}))
	t.Cleanup(a.Close)

	approved, err := types.NormalizeProcessingEndpoint(a.URL)
	require.NoError(t, err)
	ctx := types.WithProcessingApprovedEndpoint(
		types.WithProcessingGuard(context.Background(), allowProcessingGuard{}), approved)
	bURL, err := url.Parse(b.URL)
	require.NoError(t, err)
	raw := &http.Client{CheckRedirect: func(req *http.Request, _ []*http.Request) error {
		req.URL.Host = bURL.Host
		return nil
	}}
	do := func(client *http.Client) (*http.Response, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL+"/", nil)
		require.NoError(t, err)
		return client.Do(req)
	}

	resp, err := do(guardedProcessingClient(raw))
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	require.ErrorIs(t, err, types.ErrProcessingDenied)
	require.Zero(t, bHits)

	resp, err = do(raw)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	require.NoError(t, err)
	require.Equal(t, 1, bHits)

	// A CheckRedirect that also swaps the request's context for one without the guard is checked against
	// the context the redirect arrived with.
	swap := &http.Client{CheckRedirect: func(req *http.Request, _ []*http.Request) error {
		*req = *req.WithContext(context.Background())
		req.URL.Host = bURL.Host
		return nil
	}}
	resp, err = do(guardedProcessingClient(swap))
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	require.ErrorIs(t, err, types.ErrProcessingDenied)
	require.Equal(t, 1, bHits)
}
