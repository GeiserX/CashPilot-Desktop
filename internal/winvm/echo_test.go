package winvm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"
)

func echoServer(t *testing.T, body string, status int) string {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(s.Close)
	return s.URL
}

func addrs(ss ...string) []netip.Addr {
	var out []netip.Addr
	for _, s := range ss {
		out = append(out, netip.MustParseAddr(s))
	}
	return out
}

// Both services are asked. Agreement gives one address; disagreement gives
// both, since each one gets a Deny; a failed or wrong answer is dropped.
func TestEcho(t *testing.T) {
	ok := func(s string) string { return echoServer(t, s, http.StatusOK) }
	cases := map[string]struct {
		urls []string
		want []netip.Addr
	}{
		"agree":               {[]string{ok("203.0.113.7\n"), ok("203.0.113.7")}, addrs("203.0.113.7")},
		"disagree":            {[]string{ok("203.0.113.7\n"), ok("198.51.100.4\n")}, addrs("198.51.100.4", "203.0.113.7")},
		"one fails":           {[]string{echoServer(t, "203.0.113.9", 500), ok("203.0.113.7")}, addrs("203.0.113.7")},
		"both fail":           {[]string{echoServer(t, "", 502), echoServer(t, "", 503)}, nil},
		"a private answer":    {[]string{ok("192.168.1.10"), ok("203.0.113.7")}, addrs("203.0.113.7")},
		"a shared answer":     {[]string{ok("100.64.3.4"), ok("203.0.113.7")}, addrs("203.0.113.7")},
		"an IPv6 answer":      {[]string{ok("2001:db8::7"), ok("203.0.113.7")}, addrs("203.0.113.7")},
		"a mapped answer":     {[]string{ok("::ffff:203.0.113.8"), ok("203.0.113.7")}, addrs("203.0.113.7")},
		"garbage":             {[]string{ok("<html>hi</html>"), ok("203.0.113.7")}, addrs("203.0.113.7")},
		"an oversized answer": {[]string{ok("203.0.113.8" + strings.Repeat(" ", maxEchoBody)), ok("203.0.113.7")}, addrs("203.0.113.7")},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got := NewEcho(c.urls, http.DefaultClient)(context.Background())
			if !slices.Equal(got, c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

// A service that never answers does not hold the other one's answer back
// past the deadline.
func TestEchoSlowService(t *testing.T) {
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer hang.Close()
	good := echoServer(t, "203.0.113.7", http.StatusOK)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	got := NewEcho([]string{hang.URL, good}, http.DefaultClient)(ctx)
	if !slices.Equal(got, addrs("203.0.113.7")) || time.Since(start) > 3*time.Second {
		t.Fatalf("got %v after %v", got, time.Since(start))
	}
}

func TestPublicIPv4(t *testing.T) {
	for s, want := range map[string]bool{
		"203.0.113.7": true, "1.1.1.1": true, "198.51.100.4": true,
		"10.1.2.3": false, "172.20.0.1": false, "192.168.0.1": false, "100.100.100.100": false,
		"127.0.0.1": false, "169.254.1.1": false, "0.0.0.0": false, "224.0.0.1": false,
		"255.255.255.255": false, "192.0.0.8": false, "198.18.0.1": false, "2001:db8::1": false,
	} {
		if got := PublicIPv4(netip.MustParseAddr(s)); got != want {
			t.Errorf("%s: %v, want %v", s, got, want)
		}
	}
}
