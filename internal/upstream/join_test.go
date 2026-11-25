package upstream

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The block CashPilot's Fleet page copies under Add a box, byte for byte
// (app/templates/fleet.html copyWorkerEnv).
const addABoxBlock = "CASHPILOT_UI_URL=http://192.168.1.10:8080\nCASHPILOT_API_KEY=s3cr3t-fleet-key\nCASHPILOT_WORKER_NAME=server-name"

func TestParseJoinReadsTheAddABoxBlock(t *testing.T) {
	cases := []struct {
		name, block, url, key string
		want                  Target
	}{
		{"the block as copied", addABoxBlock, "", "", Target{"http://192.168.1.10:8080", "s3cr3t-fleet-key"}},
		{"windows line ends and blank lines", "\r\n" + strings.ReplaceAll(addABoxBlock, "\n", "\r\n") + "\r\n\r\n", "", "", Target{"http://192.168.1.10:8080", "s3cr3t-fleet-key"}},
		{"spaces and quotes", "  CASHPILOT_UI_URL = \"https://cp.example.com/\"  \n\tCASHPILOT_API_KEY='s3cr3t-fleet-key'\t", "", "", Target{"https://cp.example.com", "s3cr3t-fleet-key"}},
		{"export and compose list markers", "export CASHPILOT_UI_URL=http://nas.lan:8080\n- CASHPILOT_API_KEY=k1", "", "", Target{"http://nas.lan:8080", "k1"}},
		{"yaml colon form", "CASHPILOT_UI_URL: http://10.0.0.2:8080\nCASHPILOT_API_KEY: k2", "", "", Target{"http://10.0.0.2:8080", "k2"}},
		{"the two fields", "", " http://192.168.1.10:8080/ ", " k3 ", Target{"http://192.168.1.10:8080", "k3"}},
		{"a bare host:port gets http", "", "192.168.1.10:8080", "k4", Target{"http://192.168.1.10:8080", "k4"}},
		{"a block without a key takes the key field", "CASHPILOT_UI_URL=http://10.1.1.1:8080", "", "k5", Target{"http://10.1.1.1:8080", "k5"}},
		{"the block wins over the fields", addABoxBlock, "http://other:1", "other", Target{"http://192.168.1.10:8080", "s3cr3t-fleet-key"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseJoin(tc.block, tc.url, tc.key)
			if err != nil {
				t.Fatalf("ParseJoin: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseJoinRefusesWithAPlainReason(t *testing.T) {
	cases := []struct {
		name, block, url, key, want string
	}{
		{"nothing at all", "", "", "", "address of your CashPilot is missing"},
		{"a paste with no known line", "hello there\nhttp://192.168.1.10:8080", "", "", "no CASHPILOT_UI_URL"},
		{"no key", "", "http://192.168.1.10:8080", "", "key is missing"},
		{"the key was never revealed", "CASHPILOT_UI_URL=http://192.168.1.10:8080\nCASHPILOT_API_KEY=••••••••", "", "", "Reveal API Key"},
		{"the reveal failed", "CASHPILOT_UI_URL=http://192.168.1.10:8080\nCASHPILOT_API_KEY=(error)", "", "", "Reveal API Key"},
		{"a key with a space", "", "http://192.168.1.10:8080", "two words", "spaces"},
		{"not a web address", "", "ftp://192.168.1.10", "k", "does not look right"},
		{"an address with spaces", "", "http://my server:8080", "k", "does not look right"},
		{"an address with a login in it", "", "http://user:pw@192.168.1.10:8080", "k", "does not look right"},
		{"only a scheme", "", "http://", "k", "does not look right"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseJoin(tc.block, tc.url, tc.key)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one that says %q", err, tc.want)
			}
		})
	}
}

// TestExplainMapsEachHeartbeatFailure drives the real client against a fake
// server for each answer a join can meet, and checks the plain message.
func TestExplainMapsEachHeartbeatFailure(t *testing.T) {
	cases := []struct {
		name   string
		status int
		ctype  string
		body   string
		want   string
	}{
		{"key refused", 401, "application/json", `{"detail":"Invalid API key"}`, "key-refused"},
		{"already known to the server", 401, "application/json", `{"detail":"Invalid or missing per-worker key"}`, "already-joined"},
		{"enrolment never completed", 401, "application/json", `{"detail":"Enrollment was never completed for this worker"}`, "already-joined"},
		{"another Desktop", 403, "application/json", `{"error":"CashPilot Desktop cannot pair with another Desktop."}`, "desktop"},
		{"forbidden", 403, "application/json", `{"detail":"Forbidden"}`, "key-refused"},
		{"too old: no heartbeat route", 404, "application/json", `{"detail":"Not Found"}`, "too-old"},
		{"too old: payload refused", 422, "application/json", `{"detail":[{"loc":["body","apps"]}]}`, "too-old"},
		{"a web page, not CashPilot", 404, "text/html", `<html>nope</html>`, "not-cashpilot"},
		{"200 but not a heartbeat reply", 200, "text/html", `<html>router login</html>`, "not-cashpilot"},
		{"no fleet key on the server", 503, "application/json", `{"detail":"Fleet key not configured — set CASHPILOT_API_KEY"}`, "no-fleet-key"},
		{"server error", 500, "text/plain", `boom`, "server-error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.ctype)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			_, err := New().Send(context.Background(), srv.URL, "k", Payload{Name: "box", ClientID: "box"})
			if err == nil {
				t.Fatal("Send succeeded")
			}
			got := Explain(err, Host(srv.URL), "my-pc")
			if got.Code != tc.want {
				t.Fatalf("code = %q (%s), want %q; err %v", got.Code, got.Message, tc.want, err)
			}
			if got.Message == "" || strings.Contains(got.Message, "upstream:") {
				t.Fatalf("message is not plain: %q", got.Message)
			}
			if tc.want == "already-joined" && !strings.Contains(got.Message, "my-pc") {
				t.Fatalf("the message does not name the computer to remove: %q", got.Message)
			}
		})
	}
}

func TestExplainNetworkFailures(t *testing.T) {
	// A closed port: nothing listens.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	closed := "http://" + ln.Addr().String()
	_ = ln.Close()
	_, err = New().Send(context.Background(), closed, "k", Payload{})
	if got := Explain(err, Host(closed), "pc"); got.Code != "unreachable" || !strings.Contains(got.Message, Host(closed)) {
		t.Fatalf("closed port: %+v (%v)", got, err)
	}

	dns := fmt.Errorf("upstream: heartbeat failed: %w", &net.DNSError{Err: "no such host", Name: "nas.lan", IsNotFound: true})
	if got := Explain(dns, "nas.lan:8080", "pc"); got.Code != "dns" {
		t.Fatalf("dns: %+v", got)
	}

	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer tlsSrv.Close()
	_, err = New().Send(context.Background(), tlsSrv.URL, "k", Payload{})
	if got := Explain(err, Host(tlsSrv.URL), "pc"); got.Code != "certificate" {
		t.Fatalf("self-signed certificate: %+v (%v)", got, err)
	}

	_, err = New().Send(context.Background(), "http://cashpilot.example.com", "k", Payload{})
	if got := Explain(err, "cashpilot.example.com", "pc"); got.Code != "address" || !strings.Contains(got.Message, "https") {
		t.Fatalf("plain http to a public address: %+v (%v)", got, err)
	}
	if got := Explain(errors.New("x"), "h", "pc"); got.Code != "unreachable" {
		t.Fatalf("an unknown error should read as unreachable: %+v", got)
	}
}

// The pairing loop keys its re-enrolment on ErrUnauthorized; the typed error
// must keep matching it, with the same message as before.
func TestStatusErrorStillMatchesErrUnauthorized(t *testing.T) {
	err := error(&StatusError{Code: 401, Body: "nope"})
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatal("a 401 no longer matches ErrUnauthorized")
	}
	if errors.Is(&StatusError{Code: 500}, ErrUnauthorized) {
		t.Fatal("a 500 matches ErrUnauthorized")
	}
	if want := "upstream: the server refused this machine's key: server returned 401: nope"; err.Error() != want {
		t.Fatalf("message = %q", err.Error())
	}
}
