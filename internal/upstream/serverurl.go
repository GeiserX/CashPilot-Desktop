package upstream

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

// ValidateServerURL decides whether this Desktop may send its pairing keys to
// serverURL. It is the same rule as CashPilot-android's ServerUrlPolicy:
//
//   - https to any host;
//   - http only to a host that cannot be on the public internet: loopback,
//     RFC1918, CGNAT/Tailscale 100.64.0.0/10, link-local, IPv6 ULA, a
//     single-label name, or a name under .local, .lan, .home.arpa, .internal
//     or .ts.net.
//
// The server URL is one the user typed, not one a remote party supplied, so
// the SSRF worker policy (fleetnet) is the wrong rule here: it refused a
// server on a public name or on localhost, and in its "public" mode it let the
// keys go over plain http to the internet. The answer comes from the URL's
// literal host and never from DNS, so it does not depend on the network the
// machine is on. Cloud metadata addresses stay refused.
func ValidateServerURL(serverURL string) error {
	u, err := url.Parse(strings.TrimSpace(serverURL))
	if err != nil {
		return fmt.Errorf("server URL is not parseable: %w", err)
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" {
		return fmt.Errorf("server URL has no host")
	}
	if host == "metadata.google.internal" || host == "metadata" {
		return fmt.Errorf("server URL host %s is a cloud metadata endpoint", host)
	}
	if ip := net.ParseIP(host); ip != nil && isMetadataIP(ip) {
		return fmt.Errorf("server URL host %s is a cloud metadata address", host)
	}
	switch strings.ToLower(u.Scheme) {
	case "https":
		return nil
	case "http":
		if cleartextAllowed(host) {
			return nil
		}
		return fmt.Errorf("server URL %s is plain http to a host that is not provably private; use https, or http only to an IP on this machine, the LAN or Tailscale, a single-label name, or a .local, .lan, .home.arpa, .internal or .ts.net name, so the pairing key is not sent in clear text", serverURL)
	default:
		return fmt.Errorf("server URL scheme %q is not allowed (only http and https)", u.Scheme)
	}
}

var (
	canonicalHost   = regexp.MustCompile(`^[a-z0-9.:-]+$`)
	numericHost     = regexp.MustCompile(`^(0x[0-9a-f]*|\d+)$`)
	privateSuffixes = []string{".local", ".lan", ".home.arpa", ".internal", ".ts.net"}
	cleartextNets   = func() []*net.IPNet {
		var out []*net.IPNet
		for _, c := range []string{
			"127.0.0.0/8", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16",
			"169.254.0.0/16", "100.64.0.0/10", "::1/128", "fc00::/7", "fe80::/10",
		} {
			_, n, _ := net.ParseCIDR(c)
			out = append(out, n)
		}
		return out
	}()
	metadataIPs = []net.IP{net.ParseIP("169.254.169.254"), net.ParseIP("169.254.170.2"), net.ParseIP("fd00:ec2::254")}
)

// cleartextAllowed reports whether plain http may go to host (already lower
// case, without brackets or a trailing dot).
func cleartextAllowed(host string) bool {
	if !canonicalHost.MatchString(host) {
		return false
	}
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil {
			ip = v4
		}
		for _, n := range cleartextNets {
			if n.Contains(ip) {
				return true
			}
		}
		return false
	}
	if strings.Contains(host, ".") {
		for _, s := range privateSuffixes {
			if strings.HasSuffix(host, s) && len(host) > len(s) {
				return true
			}
		}
		return false
	}
	// A dotless number is an IPv4 address to the system resolver
	// (134744072 is 8.8.8.8); a dotless name is a LAN name.
	return !strings.Contains(host, ":") && !numericHost.MatchString(host)
}

func isMetadataIP(ip net.IP) bool {
	for _, m := range metadataIPs {
		if m.Equal(ip) {
			return true
		}
	}
	return false
}

// checkRedirect keeps the bearer key from following a redirect somewhere the
// first request could not have gone. Go resends Authorization on a redirect to
// the same host even when the scheme drops from https to http, so the scheme
// must not change, and the new address must pass the same rule.
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("upstream: stopped after 10 redirects")
	}
	if !strings.EqualFold(req.URL.Scheme, via[0].URL.Scheme) {
		return fmt.Errorf("upstream: refusing a redirect from %s to %s", via[0].URL.Scheme, req.URL.Redacted())
	}
	if err := ValidateServerURL(req.URL.String()); err != nil {
		return fmt.Errorf("upstream: refusing a redirect: %w", err)
	}
	return nil
}
