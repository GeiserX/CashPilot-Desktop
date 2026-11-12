package upstream

import "testing"

// TestValidateServerURL pins the pairing rule to CashPilot-android's
// ServerUrlPolicy.allowsToken. Pairing used the SSRF worker policy before,
// which refused every public https server and http://localhost, and in its
// "public" mode let the keys go over plain http to the internet.
func TestValidateServerURL(t *testing.T) {
	for _, tc := range []struct {
		url   string
		allow bool
	}{
		// https to any host, the public server case that used to be refused.
		{"https://cashpilot.example.com", true},
		{"https://203.0.113.10:8443", true},
		{"https://localhost:8080", true},
		// http only where it cannot cross the internet.
		{"http://localhost:8080", true},
		{"http://127.0.0.1:8080", true},
		{"http://[::1]:8080", true},
		{"http://192.168.1.10:8080", true},
		{"http://10.0.0.5", true},
		{"http://172.20.0.2", true},
		{"http://100.101.102.103", true},
		{"http://[fd12:3456::1]", true},
		{"http://nas.local:8080", true},
		{"http://cashpilot.lan", true},
		{"http://box.home.arpa", true},
		{"http://watchtower.tailnet-x.ts.net:8080", true},
		{"http://nas", true},
		// plain http to a public host would send the key in clear text.
		{"http://cashpilot.example.com", false},
		{"http://203.0.113.10", false},
		{"http://8.8.8.8", false},
		{"http://[::ffff:8.8.8.8]", false},
		{"http://134744072", false}, // dotless number: 8.8.8.8 to the resolver
		{"http://0x08080808", false},
		{"http://010.0.0.1", false}, // not canonical
		{"http://.local", false},
		{"http://[2001:db8::1]", false},
		// never a server.
		{"http://169.254.169.254", false},
		{"https://169.254.169.254", false},
		{"http://metadata.google.internal", false},
		{"ftp://192.168.1.10", false},
		{"http://", false},
		{"192.168.1.10:8080", false},
	} {
		err := ValidateServerURL(tc.url)
		if (err == nil) != tc.allow {
			t.Errorf("ValidateServerURL(%q) = %v, want allow=%v", tc.url, err, tc.allow)
		}
	}
}
