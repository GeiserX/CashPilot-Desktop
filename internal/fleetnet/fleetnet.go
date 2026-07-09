// Package fleetnet validates the URL of a remote CashPilot worker before the
// desktop (master) makes an authenticated request to it.
//
// The master attaches the shared fleet bearer token to every worker request, so
// an unvalidated worker URL — which arrives either in a worker's own heartbeat
// body (attacker-influenceable) or is typed by the operator — is a classic SSRF
// confused-deputy hazard: it could steer authenticated requests at cloud-metadata
// endpoints (169.254.169.254), the master's own loopback services, or otherwise
// unreachable internal hosts.
//
// This is a straight port of the CashPilot server's SSRF guard
// (CashPilot/app/main.py:792-918). The always-blocked address sets, the
// IPv4-mapped-IPv6 normalization, and the DNS-rebinding guard (resolve the host
// and check EVERY resolved IP) are reproduced faithfully; the three policy modes
// (strict/private/public) are the Desktop's adaptation of the original's
// permissive/strict pair. See docs/REMOTE-DEPLOY-DESIGN.md §4.
//
// The package is pure and has no callers yet — Phase 2 of the remote-deploy plan
// wires it into the outbound fleet client.
package fleetnet

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// lookupIP resolves a hostname to its IP addresses. It is a package var so tests
// can inject a deterministic resolver and exercise the DNS-rebinding guard fully
// offline (no real DNS). Production uses net.LookupIP which — like the original's
// socket.getaddrinfo — returns every A/AAAA record for the name.
var lookupIP = net.LookupIP

// allowedSchemes is the scheme allowlist. Only plain HTTP(S) worker endpoints are
// ever valid; file://, gopher://, etc. are rejected. (main.py:792)
var allowedSchemes = map[string]bool{"http": true, "https": true}

// metadataIPs are cloud instance-metadata endpoints (AWS/GCP/Azure IMDS). They are
// never a valid worker and are blocked unless Policy.AllowMetadata is set. The IPv6
// address sits inside ULA fd00::/8, which the "private"/"public" range checks would
// otherwise accept, so it must be named explicitly. (main.py:808-813)
var metadataIPs = []net.IP{
	net.ParseIP("169.254.169.254"), // AWS/GCP/Azure IMDS (IPv4)
	net.ParseIP("fd00:ec2::254"),   // AWS IMDS over IPv6
}

// gcpMetadataHost is the GCP metadata service's DNS name; blocked by name (before
// any resolution) whenever metadata is disallowed. The resolved-IP check is the
// real guard, but rejecting the well-known name early is cheap defense in depth.
const gcpMetadataHost = "metadata.google.internal"

// privateNets are the ranges the "private" policy treats as safe worker targets:
// RFC1918, the CGNAT block Tailscale uses for its 100.x addresses, and IPv6 ULA.
var privateNets = mustCIDRs(
	"10.0.0.0/8",     // RFC1918
	"172.16.0.0/12",  // RFC1918
	"192.168.0.0/16", // RFC1918
	"100.64.0.0/10",  // CGNAT / Tailscale (100.64.0.0 – 100.127.255.255)
	"fc00::/7",       // IPv6 unique-local (ULA)
)

// Policy selects how ValidateWorkerURL classifies a worker endpoint. It mirrors the
// Phase 0 config surface (config.AppConfig.WorkerURLPolicy / WorkerAllowedHosts /
// WorkerAllowMetadata).
//
// Mode:
//   - "strict":  allow ONLY hosts/IPs matched by AllowedHosts.
//   - "private": allow RFC1918 + CGNAT/Tailscale 100.64.0.0/10 + IPv6 ULA fc00::/7,
//     plus AllowedHosts; public addresses are rejected. This is the homelab default
//     and the value an empty Mode is treated as.
//   - "public":  allow any address that is not loopback / link-local / metadata /
//     unspecified.
//
// AllowedHosts entries may be an exact hostname, a "*.suffix" DNS-name suffix (e.g.
// "*.mango-alpha.ts.net" for Tailscale MagicDNS), a CIDR, or a literal IP — a port
// of the original's allowlist parsing (main.py:823-839).
//
// AllowMetadata, when false (the default), blocks the cloud-metadata endpoints
// unconditionally: no AllowedHosts entry overrides that. Setting it true is the
// only way to reach a metadata address.
type Policy struct {
	Mode          string
	AllowedHosts  []string
	AllowMetadata bool
}

// ValidateWorkerURL parses rawURL and returns nil if it is a safe worker target
// under p, or a descriptive error naming why it was rejected.
//
// Precedence, highest first (documented so the allowlist-vs-metadata question is
// unambiguous — see docs/REMOTE-DEPLOY-DESIGN.md §4):
//
//  1. Structural — the scheme must be http/https and the URL must have a host.
//  2. Metadata — a metadata IP (or the GCP metadata hostname) is rejected whenever
//     AllowMetadata is false. This is NOT overridable by AllowedHosts, faithful to
//     the original whose metadata block runs unconditionally; AllowMetadata=true is
//     the only lift and then treats those endpoints as explicitly allowed.
//  3. Explicit allowlist — an exact IP/CIDR entry in AllowedHosts allows that IP,
//     overriding the loopback/link-local/unspecified block AND the policy range
//     check. A name/suffix entry overrides the range check (and a resolution
//     failure) but never the metadata (2) or loopback/link-local (4) block on a
//     RESOLVED IP, matching the original where _assert_ip_not_blocked always runs.
//  4. Always-blocked — loopback, link-local and unspecified addresses are rejected.
//  5. Policy range check — strict/private/public as described on Policy.
//
// For a hostname the host is resolved and EVERY resolved IP must pass steps 2-5 —
// the DNS-rebinding guard: a name that resolves to any blocked/disallowed IP is
// rejected, and the resolution is done at call time (never cached) so a name that
// later flips to a blocked address is still caught.
//
// A URL may legitimately carry userinfo ("user:pass@host"); like the original it is
// not rejected, because url.Hostname() (as with Python's urlparse().hostname)
// extracts exactly the authority host the HTTP client will dial, so userinfo cannot
// smuggle a different target past the check.
func ValidateWorkerURL(rawURL string, p Policy) error {
	// Normalize the mode the way the original does (strip + lower), defaulting an
	// empty mode to the safe "private" homelab policy.
	p.Mode = strings.ToLower(strings.TrimSpace(p.Mode))
	if p.Mode == "" {
		p.Mode = "private"
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("worker URL is not parseable: %w", err)
	}
	if !allowedSchemes[strings.ToLower(u.Scheme)] {
		return fmt.Errorf("worker URL scheme %q is not allowed (only http and https)", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("worker URL has no host")
	}

	al := parseAllowlist(p.AllowedHosts)

	// Metadata-by-name: the GCP metadata service is reachable via a DNS name; block
	// it before any resolution unless metadata is explicitly allowed. (main.py:882
	// blocks localhost by name; this extends the same idea to the GCP name.)
	if !p.AllowMetadata && strings.EqualFold(host, gcpMetadataHost) {
		return fmt.Errorf("worker URL host %s is a cloud metadata endpoint", host)
	}

	// Case A: literal IP — classify directly, no DNS needed. (main.py:885-894)
	if ip := net.ParseIP(host); ip != nil {
		return classifyIP(ip, host, p, al)
	}

	// Case B: hostname.
	// Loopback names never point at a worker; block unless explicitly allowlisted
	// by name. (main.py:882-883)
	if isLoopbackName(host) && !al.nameAllowed(host) {
		return fmt.Errorf("worker URL host %s is a loopback name", host)
	}

	ips, err := lookupIP(host)
	if err != nil || len(ips) == 0 {
		// Unresolvable: a name explicitly trusted in AllowedHosts is tolerated
		// (mirrors the original's allowlisted-name short-circuit, main.py:909-911);
		// anything else is rejected so a bad/typo'd host never becomes a blind fetch.
		if al.nameAllowed(host) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("worker URL host %s does not resolve: %w", host, err)
		}
		return fmt.Errorf("worker URL host %s does not resolve", host)
	}

	// DNS-rebinding guard: every resolved IP must pass. (main.py:913-917)
	for _, ip := range ips {
		if err := classifyIP(ip, host, p, al); err != nil {
			return err
		}
	}
	return nil
}

// classifyIP applies the metadata / allowlist / always-blocked / range-check
// precedence to a single resolved-or-literal IP. host is the original URL host,
// used for name/suffix allowlist matching.
func classifyIP(ip net.IP, host string, p Policy, al allowlist) error {
	ip = normalizeIP(ip) // collapse ::ffff:a.b.c.d -> a.b.c.d (main.py:845-849)

	// (2) Metadata wins over everything except the AllowMetadata escape hatch.
	if isMetadataIP(ip) {
		if p.AllowMetadata {
			return nil
		}
		return fmt.Errorf("worker URL host %s is a cloud metadata address %s", host, ip)
	}

	// (3a) An exact IP/CIDR allowlist entry overrides the always-blocked set and the
	// range check (but not metadata, already handled above).
	if al.ipAllowed(ip) {
		return nil
	}

	// (4) Always-blocked: loopback (127/8, ::1), link-local (169.254/16, fe80::/10)
	// and unspecified (0.0.0.0, ::). (main.py:815-820, plus the unspecified address.)
	switch {
	case ip.IsLoopback():
		return fmt.Errorf("worker URL host %s is a loopback address %s", host, ip)
	case ip.IsLinkLocalUnicast():
		return fmt.Errorf("worker URL host %s is a link-local address %s", host, ip)
	case ip.IsUnspecified():
		return fmt.Errorf("worker URL host %s is an unspecified address %s", host, ip)
	}

	// (3b) A name/suffix allowlist entry overrides the range check.
	nameAllowed := al.nameAllowed(host)

	// (5) Policy range check.
	switch p.Mode {
	case "strict":
		if nameAllowed {
			return nil
		}
		return fmt.Errorf("worker URL host %s (%s) is not in the strict allowlist", host, ip)
	case "public":
		return nil
	case "private":
		if nameAllowed || inNets(ip, privateNets) {
			return nil
		}
		return fmt.Errorf("worker URL host %s resolves to public address %s (blocked by the private policy)", host, ip)
	default:
		return fmt.Errorf("unknown worker URL policy %q", p.Mode)
	}
}

// normalizeIP collapses an IPv4-mapped IPv6 address (::ffff:a.b.c.d) to its IPv4
// form so the block checks below cannot be bypassed via the mapped spelling. This
// is the equivalent of the original's _normalize_ip (main.py:845-849).
func normalizeIP(ip net.IP) net.IP {
	if v4 := ip.To4(); v4 != nil {
		return v4
	}
	return ip
}

func isMetadataIP(ip net.IP) bool {
	for _, m := range metadataIPs {
		if m != nil && m.Equal(ip) {
			return true
		}
	}
	return false
}

func inNets(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

func isLoopbackName(host string) bool {
	h := strings.ToLower(host)
	return h == "localhost" || h == "localhost.localdomain"
}

func mustCIDRs(cidrs ...string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			panic("fleetnet: invalid CIDR " + c + ": " + err.Error())
		}
		out = append(out, n)
	}
	return out
}

// allowlist is Policy.AllowedHosts parsed into its match kinds — a port of the
// original's _parse_worker_allowlist (main.py:823-839): "*.suffix" name suffixes,
// CIDRs, literal IPs, and exact hostnames.
type allowlist struct {
	cidrs    []*net.IPNet
	ips      []net.IP
	suffixes []string        // lower-cased, without the leading "*."
	hosts    map[string]bool // lower-cased exact hostnames
}

func parseAllowlist(entries []string) allowlist {
	al := allowlist{hosts: map[string]bool{}}
	for _, raw := range entries {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		if strings.HasPrefix(entry, "*.") {
			al.suffixes = append(al.suffixes, strings.ToLower(entry[2:]))
			continue
		}
		if _, n, err := net.ParseCIDR(entry); err == nil {
			al.cidrs = append(al.cidrs, n)
			continue
		}
		if ip := net.ParseIP(entry); ip != nil {
			al.ips = append(al.ips, ip)
			continue
		}
		al.hosts[strings.ToLower(entry)] = true
	}
	return al
}

// ipAllowed reports whether ip matches a literal-IP or CIDR allowlist entry.
func (al allowlist) ipAllowed(ip net.IP) bool {
	for _, want := range al.ips {
		if want.Equal(ip) {
			return true
		}
	}
	return inNets(ip, al.cidrs)
}

// nameAllowed reports whether host matches an exact-hostname or "*.suffix" entry.
func (al allowlist) nameAllowed(host string) bool {
	h := strings.ToLower(host)
	if al.hosts[h] {
		return true
	}
	for _, suf := range al.suffixes {
		if h == suf || strings.HasSuffix(h, "."+suf) {
			return true
		}
	}
	return false
}
