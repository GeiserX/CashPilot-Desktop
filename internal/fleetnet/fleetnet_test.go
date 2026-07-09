package fleetnet

import (
	"net"
	"strings"
	"testing"
)

// resolveTo builds a stub resolver that returns the given literal IPs for any
// host, so the DNS-rebinding branch is exercised deterministically and offline.
func resolveTo(ips ...string) func(string) ([]net.IP, error) {
	parsed := make([]net.IP, 0, len(ips))
	for _, s := range ips {
		ip := net.ParseIP(s)
		if ip == nil {
			panic("resolveTo: invalid test IP " + s)
		}
		parsed = append(parsed, ip)
	}
	return func(string) ([]net.IP, error) { return parsed, nil }
}

// resolveFail models a host that does not resolve.
func resolveFail(string) ([]net.IP, error) {
	return nil, &net.DNSError{Err: "no such host", Name: "test", IsNotFound: true}
}

func TestValidateWorkerURL(t *testing.T) {
	// Restore the real resolver once the table finishes.
	t.Cleanup(func() { lookupIP = net.LookupIP })

	cases := []struct {
		name string
		url  string
		pol  Policy
		// resolver is installed for hostname cases. Leave nil for literal-IP /
		// structural cases: those must NOT trigger a DNS lookup, and the harness
		// fails the test if they do.
		resolver func(string) ([]net.IP, error)
		wantErr  bool
	}{
		// ---- scheme + structural -------------------------------------------------
		{name: "https private IP ok", url: "https://192.168.1.50:8081", pol: Policy{Mode: "private"}},
		{name: "non-http scheme file blocked", url: "file:///etc/passwd", pol: Policy{Mode: "public"}, wantErr: true},
		{name: "non-http scheme gopher blocked", url: "gopher://10.0.0.1/", pol: Policy{Mode: "private"}, wantErr: true},
		{name: "empty host blocked", url: "http://", pol: Policy{Mode: "public"}, wantErr: true},
		{name: "schemeless input blocked", url: "//169.254.169.254", pol: Policy{Mode: "public"}, wantErr: true},
		{name: "unparseable url blocked", url: "http://[::1", pol: Policy{Mode: "public"}, wantErr: true},

		// ---- private policy on literal IPs --------------------------------------
		{name: "rfc1918 10/8 ok under private", url: "http://10.1.2.3:8081", pol: Policy{Mode: "private"}},
		{name: "rfc1918 172.16/12 ok under private", url: "http://172.16.5.5", pol: Policy{Mode: "private"}},
		{name: "cgnat tailscale 100.64/10 ok under private", url: "http://100.100.1.1", pol: Policy{Mode: "private"}},
		{name: "ipv6 ula ok under private", url: "http://[fd00::1]:8081", pol: Policy{Mode: "private"}},
		{name: "public IP blocked under private", url: "http://93.184.216.34", pol: Policy{Mode: "private"}, wantErr: true},
		{name: "empty mode defaults to private (public IP blocked)", url: "http://93.184.216.34", pol: Policy{}, wantErr: true},
		{name: "empty mode defaults to private (private IP ok)", url: "http://192.168.9.9", pol: Policy{}},

		// ---- public policy -------------------------------------------------------
		{name: "public IP ok under public", url: "http://93.184.216.34", pol: Policy{Mode: "public"}},
		{name: "loopback still blocked under public", url: "http://127.0.0.1", pol: Policy{Mode: "public"}, wantErr: true},
		{name: "link-local still blocked under public", url: "http://169.254.10.10", pol: Policy{Mode: "public"}, wantErr: true},

		// ---- strict policy -------------------------------------------------------
		{name: "strict rejects non-allowlisted", url: "http://10.1.2.3", pol: Policy{Mode: "strict"}, wantErr: true},
		{name: "strict allows allowlisted IP", url: "http://10.1.2.3", pol: Policy{Mode: "strict", AllowedHosts: []string{"10.1.2.3"}}},
		{name: "strict allows allowlisted CIDR", url: "http://10.9.9.9", pol: Policy{Mode: "strict", AllowedHosts: []string{"10.0.0.0/8"}}},
		{name: "strict rejects IP outside allowlisted CIDR", url: "http://11.9.9.9", pol: Policy{Mode: "strict", AllowedHosts: []string{"10.0.0.0/8"}}, wantErr: true},

		// ---- always-blocked (loopback / link-local / unspecified) ---------------
		{name: "ipv4 loopback blocked", url: "http://127.0.0.1", pol: Policy{Mode: "private"}, wantErr: true},
		{name: "ipv4 loopback range blocked", url: "http://127.9.9.9", pol: Policy{Mode: "public"}, wantErr: true},
		{name: "ipv6 loopback blocked", url: "http://[::1]:8081", pol: Policy{Mode: "public"}, wantErr: true},
		{name: "localhost name blocked", url: "http://localhost:8081", pol: Policy{Mode: "private"}, wantErr: true},
		{name: "localhost.localdomain name blocked", url: "http://localhost.localdomain", pol: Policy{Mode: "public"}, wantErr: true},
		{name: "ipv4 link-local blocked", url: "http://169.254.5.5", pol: Policy{Mode: "public"}, wantErr: true},
		{name: "ipv6 link-local fe80 blocked", url: "http://[fe80::1]", pol: Policy{Mode: "public"}, wantErr: true},
		{name: "unspecified 0.0.0.0 blocked", url: "http://0.0.0.0:8081", pol: Policy{Mode: "public"}, wantErr: true},
		{name: "unspecified :: blocked", url: "http://[::]:8081", pol: Policy{Mode: "public"}, wantErr: true},

		// ---- metadata ------------------------------------------------------------
		{name: "ipv4 metadata blocked by default", url: "http://169.254.169.254", pol: Policy{Mode: "public"}, wantErr: true},
		{name: "ipv4 metadata allowed with AllowMetadata", url: "http://169.254.169.254", pol: Policy{Mode: "public", AllowMetadata: true}},
		{name: "ipv6 metadata blocked despite being ULA (private range)", url: "http://[fd00:ec2::254]", pol: Policy{Mode: "private"}, wantErr: true},
		{name: "ipv6 metadata allowed with AllowMetadata", url: "http://[fd00:ec2::254]", pol: Policy{Mode: "private", AllowMetadata: true}},
		{name: "metadata NOT overridable by allowlist", url: "http://169.254.169.254", pol: Policy{Mode: "public", AllowedHosts: []string{"169.254.169.254"}}, wantErr: true},
		{name: "gcp metadata hostname blocked by name", url: "http://metadata.google.internal/", pol: Policy{Mode: "public"}, wantErr: true},

		// ---- IPv4-mapped IPv6 normalization -------------------------------------
		{name: "ipv4-mapped metadata blocked (normalization)", url: "http://[::ffff:169.254.169.254]/", pol: Policy{Mode: "public"}, wantErr: true},
		{name: "ipv4-mapped loopback blocked (normalization)", url: "http://[::ffff:127.0.0.1]/", pol: Policy{Mode: "public"}, wantErr: true},
		{name: "ipv4-mapped private ok under private", url: "http://[::ffff:192.168.1.10]/", pol: Policy{Mode: "private"}},

		// ---- userinfo does not bypass the host check ----------------------------
		{name: "userinfo private host still validated by host", url: "http://user:pass@192.168.1.5/", pol: Policy{Mode: "private"}},
		{name: "userinfo cannot smuggle metadata", url: "http://user:pass@169.254.169.254/", pol: Policy{Mode: "public"}, wantErr: true},

		// ---- allowlist overrides -------------------------------------------------
		{name: "allowlist IP overrides private range check", url: "http://8.8.8.8", pol: Policy{Mode: "private", AllowedHosts: []string{"8.8.8.8"}}},
		{name: "allowlist exact IP overrides loopback block", url: "http://127.0.0.1", pol: Policy{Mode: "private", AllowedHosts: []string{"127.0.0.1"}}},

		// ---- DNS-rebinding guard (injected resolver) ----------------------------
		{name: "hostname resolving to private ok under private", url: "http://worker.lan:8081", pol: Policy{Mode: "private"}, resolver: resolveTo("192.168.4.4")},
		{name: "hostname resolving to public blocked under private", url: "http://worker.example.com", pol: Policy{Mode: "private"}, resolver: resolveTo("93.184.216.34"), wantErr: true},
		{name: "hostname resolving to public ok under public", url: "http://worker.example.com", pol: Policy{Mode: "public"}, resolver: resolveTo("93.184.216.34")},
		{name: "hostname rebinding to metadata blocked", url: "http://sneaky.example.com", pol: Policy{Mode: "public"}, resolver: resolveTo("169.254.169.254"), wantErr: true},
		{name: "hostname rebinding to loopback blocked", url: "http://sneaky.example.com", pol: Policy{Mode: "public"}, resolver: resolveTo("127.0.0.1"), wantErr: true},
		{name: "hostname with one bad IP among many blocked", url: "http://multi.example.com", pol: Policy{Mode: "private"}, resolver: resolveTo("192.168.1.1", "169.254.169.254"), wantErr: true},
		{name: "hostname does not resolve blocked", url: "http://ghost.example.com", pol: Policy{Mode: "private"}, resolver: resolveFail, wantErr: true},

		// ---- name-based allowlist (Tailscale MagicDNS suffix) -------------------
		{name: "strict allows *.suffix name even if unresolvable", url: "http://nodeA.mango-alpha.ts.net:8081", pol: Policy{Mode: "strict", AllowedHosts: []string{"*.mango-alpha.ts.net"}}, resolver: resolveFail},
		{name: "strict suffix name resolving to public ok (name override)", url: "http://nodeA.mango-alpha.ts.net", pol: Policy{Mode: "strict", AllowedHosts: []string{"*.mango-alpha.ts.net"}}, resolver: resolveTo("93.184.216.34")},
		{name: "suffix-allowlisted name resolving to metadata still blocked", url: "http://nodeA.mango-alpha.ts.net", pol: Policy{Mode: "strict", AllowedHosts: []string{"*.mango-alpha.ts.net"}}, resolver: resolveTo("169.254.169.254"), wantErr: true},
		{name: "strict rejects name outside suffix", url: "http://nodeA.evil.example", pol: Policy{Mode: "strict", AllowedHosts: []string{"*.mango-alpha.ts.net"}}, resolver: resolveTo("93.184.216.34"), wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.resolver != nil {
				lookupIP = tc.resolver
			} else {
				lookupIP = func(host string) ([]net.IP, error) {
					t.Fatalf("unexpected DNS lookup for %q (literal-IP/structural cases must not resolve)", host)
					return nil, nil
				}
			}

			err := ValidateWorkerURL(tc.url, tc.pol)
			if tc.wantErr && err == nil {
				t.Fatalf("ValidateWorkerURL(%q, %+v) = nil, want error", tc.url, tc.pol)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateWorkerURL(%q, %+v) = %v, want nil", tc.url, tc.pol, err)
			}
		})
	}
}

// TestValidateWorkerURLErrorMessages pins that rejections name the reason, so an
// operator (and Phase 2's caller) can tell an SSRF block from a policy rejection.
func TestValidateWorkerURLErrorMessages(t *testing.T) {
	t.Cleanup(func() { lookupIP = net.LookupIP })
	lookupIP = func(host string) ([]net.IP, error) {
		t.Fatalf("unexpected DNS lookup for %q", host)
		return nil, nil
	}

	cases := []struct {
		name    string
		url     string
		pol     Policy
		wantSub string
	}{
		{name: "scheme", url: "file:///x", pol: Policy{Mode: "public"}, wantSub: "scheme"},
		{name: "metadata", url: "http://169.254.169.254", pol: Policy{Mode: "public"}, wantSub: "metadata"},
		{name: "loopback", url: "http://127.0.0.1", pol: Policy{Mode: "public"}, wantSub: "loopback"},
		{name: "link-local", url: "http://169.254.1.1", pol: Policy{Mode: "public"}, wantSub: "link-local"},
		{name: "unspecified", url: "http://0.0.0.0", pol: Policy{Mode: "public"}, wantSub: "unspecified"},
		{name: "private-policy public IP", url: "http://8.8.8.8", pol: Policy{Mode: "private"}, wantSub: "public address"},
		{name: "strict", url: "http://8.8.8.8", pol: Policy{Mode: "strict"}, wantSub: "strict allowlist"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateWorkerURL(tc.url, tc.pol)
			if err == nil {
				t.Fatalf("expected an error for %q", tc.url)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q does not mention %q", err.Error(), tc.wantSub)
			}
		})
	}
}
