package collectors

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// This file exercises the shared retry path added around the single r.http.Do call
// in doRaw/doWithRetry. Every collector funnels through it, so driving doRaw (and
// doJSON) directly with a scripted RoundTripper proves the retry policy for all of
// them at once: transient failures (transport error, 429, >=500) are retried with
// backoff, 429/Retry-After is honored, non-429 4xx is NOT retried, attempts are
// capped, and a ctx cancel during backoff returns promptly.

// TestMain shrinks the exponential backoff base for the whole package test binary.
// Several tests (here and the existing HTTP-500 collector cases in
// collectors_http_test.go) walk the retry path; without this each retry would sleep
// the real 500ms+ backoff. Production keeps the 500ms default — only the test binary
// lowers it.
func TestMain(m *testing.M) {
	retryBaseWait = time.Millisecond
	os.Exit(m.Run())
}

// scriptStep is one canned reply in a scriptedTransport sequence. If err is set,
// RoundTrip returns it as a transport error (resp nil); otherwise it returns a
// response with the given status (0 -> 200), optional Retry-After header, and body
// ("" -> "{}").
type scriptStep struct {
	status     int
	retryAfter string // Retry-After header value; "" omits the header
	body       string
	err        error
}

// scriptedTransport answers each successive Do from steps (repeating the last step
// once exhausted, so an "always 503" case lists a single 503), counts calls, and
// can run onCall per call (e.g. to cancel a ctx mid-flight). It never touches the
// network. Calls are all on the caller's goroutine (Do -> RoundTrip is synchronous
// for a custom RoundTripper), so reading .calls after the call under test is race-free.
type scriptedTransport struct {
	steps  []scriptStep
	calls  int
	onCall func(n int) // optional; receives the 1-based call number
}

func (t *scriptedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.calls++
	if t.onCall != nil {
		t.onCall(t.calls)
	}
	i := t.calls - 1
	if i >= len(t.steps) {
		i = len(t.steps) - 1 // repeat the last step once the script is exhausted
	}
	step := t.steps[i]
	if step.err != nil {
		return nil, step.err
	}
	status := step.status
	if status == 0 {
		status = http.StatusOK
	}
	header := make(http.Header)
	if step.retryAfter != "" {
		header.Set("Retry-After", step.retryAfter)
	}
	body := step.body
	if body == "" {
		body = "{}"
	}
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}, nil
}

// registryWith builds a minimal Registry over a scripted transport. doRaw/doJSON
// never touch the store, so this stays store-free (unlike newTestRegistry).
func registryWith(tr *scriptedTransport) *Registry {
	return &Registry{http: &http.Client{Transport: tr}}
}

// TestRetryThenSucceeds: 500, 500, 200 -> the send ultimately succeeds, proving two
// retries happened before the 200.
func TestRetryThenSucceeds(t *testing.T) {
	tr := &scriptedTransport{steps: []scriptStep{
		{status: 500},
		{status: 500},
		{status: 200, body: `{"ok":true}`},
	}}
	r := registryWith(tr)

	raw, status, _, err := r.doRaw(context.Background(), "GET", "https://example.test/x", nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if string(raw) != `{"ok":true}` {
		t.Fatalf("body = %q, want the third-attempt body", raw)
	}
	if tr.calls != 3 {
		t.Fatalf("calls = %d, want 3 (2 retries + success)", tr.calls)
	}
}

// TestRetryAfterHonoredThenSucceeds: 429 with Retry-After: 0 (kept 0 so the test is
// instant), then 200 -> succeeds after honoring the header. The second call proves
// the 429 was retried via the Retry-After path.
func TestRetryAfterHonoredThenSucceeds(t *testing.T) {
	tr := &scriptedTransport{steps: []scriptStep{
		{status: http.StatusTooManyRequests, retryAfter: "0"},
		{status: 200, body: `{"ok":true}`},
	}}
	r := registryWith(tr)

	_, status, _, err := r.doRaw(context.Background(), "GET", "https://example.test/x", nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if tr.calls != 2 {
		t.Fatalf("calls = %d, want 2 (429 retried after honoring Retry-After)", tr.calls)
	}
}

// TestNon429ClientErrorNotRetried: a 401 is a real auth error, not transient, so it
// must surface after exactly ONE call. Driven through doJSON so the non-2xx status
// becomes an error the collector would return.
func TestNon429ClientErrorNotRetried(t *testing.T) {
	tr := &scriptedTransport{steps: []scriptStep{
		{status: http.StatusUnauthorized, body: `{"error":"nope"}`},
	}}
	r := registryWith(tr)

	var out map[string]any
	err := r.doJSON(context.Background(), "GET", "https://example.test/x", nil, nil, &out)
	if err == nil {
		t.Fatal("expected an error for HTTP 401")
	}
	if tr.calls != 1 {
		t.Fatalf("calls = %d, want 1 (a non-429 4xx must not be retried)", tr.calls)
	}
}

// TestExhaustsAttempts: an always-503 endpoint errors after exactly maxAttempts
// calls (initial + maxAttempts-1 retries), never more.
func TestExhaustsAttempts(t *testing.T) {
	tr := &scriptedTransport{steps: []scriptStep{
		{status: http.StatusServiceUnavailable},
	}}
	r := registryWith(tr)

	_, _, _, err := r.doRaw(context.Background(), "GET", "https://example.test/x", nil, nil, nil)
	if err == nil {
		t.Fatal("expected an error after exhausting retries")
	}
	if tr.calls != maxAttempts {
		t.Fatalf("calls = %d, want maxAttempts (%d)", tr.calls, maxAttempts)
	}
}

// TestTransportErrorIsRetried: a transport-level error (err != nil from Do) is
// transient and retried up to maxAttempts, then surfaces.
func TestTransportErrorIsRetried(t *testing.T) {
	tr := &scriptedTransport{steps: []scriptStep{
		{err: errors.New("connection reset")},
	}}
	r := registryWith(tr)

	_, _, _, err := r.doRaw(context.Background(), "GET", "https://example.test/x", nil, nil, nil)
	if err == nil {
		t.Fatal("expected an error after exhausting retries on a transport error")
	}
	if tr.calls != maxAttempts {
		t.Fatalf("calls = %d, want maxAttempts (%d)", tr.calls, maxAttempts)
	}
}

// TestContextCancelDuringBackoffReturnsPromptly: cancelling ctx during the first
// (retryable) response must abort the backoff and return a ctx error before a second
// attempt. A large base makes time.After lose the select deterministically, so the
// return is driven purely by ctx.Done — yet the test finishes in microseconds
// because ctx.Done is already ready.
func TestContextCancelDuringBackoffReturnsPromptly(t *testing.T) {
	old := retryBaseWait
	retryBaseWait = 30 * time.Second
	t.Cleanup(func() { retryBaseWait = old })

	ctx, cancel := context.WithCancel(context.Background())
	tr := &scriptedTransport{
		steps:  []scriptStep{{status: http.StatusServiceUnavailable}},
		onCall: func(int) { cancel() }, // cancel while the first request is in flight
	}
	r := registryWith(tr)

	start := time.Now()
	_, _, _, err := r.doRaw(ctx, "GET", "https://example.test/x", nil, nil, nil)
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("doRaw took %v, expected a prompt ctx-cancel return", elapsed)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if tr.calls != 1 {
		t.Fatalf("calls = %d, want 1 (cancel must stop before a second attempt)", tr.calls)
	}
}

// TestRetryAfterParsing pins the Retry-After parser: delta-seconds, negative/garbage/
// missing (not honored), and both HTTP-date directions.
func TestRetryAfterParsing(t *testing.T) {
	withHeader := func(v string) http.Header {
		h := make(http.Header)
		h.Set("Retry-After", v)
		return h
	}
	cases := []struct {
		name   string
		header http.Header
		want   time.Duration
		wantOk bool
	}{
		{"delta zero", withHeader("0"), 0, true},
		{"delta seconds", withHeader("5"), 5 * time.Second, true},
		{"negative delta not honored", withHeader("-1"), 0, false},
		{"garbage not honored", withHeader("soon"), 0, false},
		{"missing header not honored", make(http.Header), 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := retryAfter(tc.header)
			if ok != tc.wantOk || (ok && got != tc.want) {
				t.Fatalf("retryAfter = (%v, %v), want (%v, %v)", got, ok, tc.want, tc.wantOk)
			}
		})
	}
	t.Run("future http-date honored as positive", func(t *testing.T) {
		h := withHeader(time.Now().Add(90 * time.Second).UTC().Format(http.TimeFormat))
		got, ok := retryAfter(h)
		if !ok || got <= 0 {
			t.Fatalf("retryAfter = (%v, %v), want (>0, true)", got, ok)
		}
	})
	t.Run("past http-date clamped to zero", func(t *testing.T) {
		h := withHeader(time.Now().Add(-90 * time.Second).UTC().Format(http.TimeFormat))
		got, ok := retryAfter(h)
		if !ok || got != 0 {
			t.Fatalf("retryAfter = (%v, %v), want (0, true)", got, ok)
		}
	})
}
