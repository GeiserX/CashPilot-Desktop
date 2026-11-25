package winvm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"
)

// EchoServices are the two services, run by different companies, that tell the
// guard this line's public IPv4 address.
var EchoServices = []string{"https://api.ipify.org", "https://icanhazip.com"}

const (
	// echoTimeout bounds each question, so one slow service never holds a pass.
	echoTimeout = 5 * time.Second
	// maxEchoBody caps an answer: an IPv4 address and a line ending.
	maxEchoBody = 64
)

// NewEcho asks every service at once, each with echoTimeout, and returns every
// public IPv4 address any of them reported, sorted and without repeats. When
// the two disagree, both addresses are returned: each one gets a Deny at once,
// and a wrong answer only blocks one extra address (DESKTOP-VM.md §5). When
// both fail, the result is empty and OwnIPs keeps its list.
func NewEcho(urls []string, c *http.Client) func(ctx context.Context) []netip.Addr {
	return func(ctx context.Context) []netip.Addr {
		var (
			mu  sync.Mutex
			out []netip.Addr
			wg  sync.WaitGroup
		)
		for _, u := range urls {
			wg.Add(1)
			go func() {
				defer wg.Done()
				a, err := askEcho(ctx, c, u)
				if err != nil {
					return
				}
				mu.Lock()
				defer mu.Unlock()
				if !slices.Contains(out, a) {
					out = append(out, a)
				}
			}()
		}
		wg.Wait()
		slices.SortFunc(out, netip.Addr.Compare)
		return out
	}
}

// DefaultEcho asks EchoServices directly over IPv4: no proxy, which would
// report its own address, and no redirect.
func DefaultEcho() func(ctx context.Context) []netip.Addr {
	d := &net.Dialer{Timeout: echoTimeout}
	tr := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
			return d.DialContext(ctx, "tcp4", addr)
		},
		TLSHandshakeTimeout:    echoTimeout,
		ResponseHeaderTimeout:  echoTimeout,
		MaxResponseHeaderBytes: 16 << 10,
		DisableKeepAlives:      true,
	}
	c := &http.Client{
		Transport:     tr,
		Timeout:       echoTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return NewEcho(EchoServices, c)
}

// askEcho asks one service and accepts only a public IPv4 address.
func askEcho(ctx context.Context, c *http.Client, url string) (netip.Addr, error) {
	ctx, cancel := context.WithTimeout(ctx, echoTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return netip.Addr{}, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return netip.Addr{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return netip.Addr{}, fmt.Errorf("%s answered %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxEchoBody+1))
	if err != nil {
		return netip.Addr{}, err
	}
	if len(b) > maxEchoBody {
		return netip.Addr{}, fmt.Errorf("%s answered more than an address", url)
	}
	a, err := netip.ParseAddr(strings.TrimSpace(string(b)))
	if err != nil {
		return netip.Addr{}, fmt.Errorf("%s: %w", url, err)
	}
	if !PublicIPv4(a) {
		return netip.Addr{}, errors.New(url + " answered an address that is not a public IPv4 address")
	}
	return a, nil
}

// PublicIPv4 is whether a is a unicast IPv4 address outside every static Deny
// range: an address the internet could see as this line's.
func PublicIPv4(a netip.Addr) bool {
	if !a.Is4() || !a.IsGlobalUnicast() {
		return false
	}
	for _, p := range StaticDeny() {
		if p.Contains(a) {
			return false
		}
	}
	return true
}
