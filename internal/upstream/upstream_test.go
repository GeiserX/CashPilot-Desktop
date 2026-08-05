package upstream

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/fleetnet"
)

// loopbackOK lets a test server on 127.0.0.1 through the SSRF validator, which
// blocks loopback by default. Only the transport is under test here; the
// validator has its own suite.
func loopbackOK() fleetnet.Policy {
	return fleetnet.Policy{Mode: "private", AllowedHosts: []string{"127.0.0.1"}}
}

func TestAuthTokenPrefersThePerWorkerKey(t *testing.T) {
	// Once enrolled, the server REFUSES the shared key for this worker, so
	// presenting it again would lock the machine out of its own fleet.
	if got := AuthToken("worker-key", "shared-key"); got != "worker-key" {
		t.Fatalf("got %q, want the per-worker key", got)
	}
}

func TestAuthTokenFallsBackToTheEnrolmentKey(t *testing.T) {
	// Bootstrap: nothing has been issued yet.
	if got := AuthToken("", "shared-key"); got != "shared-key" {
		t.Fatalf("got %q, want the enrolment key", got)
	}
	if got := AuthToken("   ", "shared-key"); got != "shared-key" {
		t.Fatalf("blank-but-present worker key must not be used: %q", got)
	}
}

func TestKeyToPersist(t *testing.T) {
	for _, tc := range []struct {
		name             string
		current, receive string
		want             string
	}{
		// Already enrolled and confirmed: the server stops sending it.
		{"nothing received keeps what we hold", "mine", "", ""},
		// Re-issue path: a dropped enrolment response must not lock us out.
		{"a different key is stored", "old", "new", "new"},
		{"first issue is stored", "", "new", "new"},
		// Rewriting every heartbeat would churn the keychain for nothing.
		{"the same key is not rewritten", "same", "same", ""},
		{"whitespace does not count as a new key", "same", "  same  ", ""},
		{"blank received is ignored", "mine", "   ", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := KeyToPersist(tc.current, tc.receive); got != tc.want {
				t.Fatalf("KeyToPersist(%q, %q) = %q, want %q", tc.current, tc.receive, got, tc.want)
			}
		})
	}
}

func TestSendRefusesWhenNotPaired(t *testing.T) {
	// Standalone is the default and a normal state, so this is a named error
	// rather than a generic failure.
	if _, err := New(loopbackOK()).Send(context.Background(), "", "tok", Payload{}); err != ErrNotPaired {
		t.Fatalf("got %v, want ErrNotPaired", err)
	}
}

func TestSendRefusesWithoutACredential(t *testing.T) {
	// A blank token means the credential was lost. Sending anyway would just
	// earn a 401 and hide the real cause.
	_, err := New(loopbackOK()).Send(context.Background(), "http://127.0.0.1:1", "", Payload{})
	if err == nil || !strings.Contains(err.Error(), "no credential") {
		t.Fatalf("got %v, want a missing-credential error", err)
	}
}

func TestSendValidatesTheURLBeforeDialing(t *testing.T) {
	// The URL is user-supplied and the request carries a bearer token, so it
	// gets the same SSRF check as any other outbound worker call. Metadata
	// endpoints are always blocked.
	c := New(fleetnet.Policy{Mode: "private"})
	_, err := c.Send(context.Background(), "http://169.254.169.254", "tok", Payload{})
	if err == nil || !strings.Contains(err.Error(), "refusing to contact") {
		t.Fatalf("got %v, want the metadata address refused", err)
	}
}

func TestSendPostsTheServerContract(t *testing.T) {
	var gotPath, gotAuth, gotCT string
	var body Payload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth, gotCT = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &body)
		_, _ = w.Write([]byte(`{"status":"ok","worker_id":7}`))
	}))
	defer srv.Close()

	in := Payload{
		Name:       "macmini",
		ClientID:   "abc",
		Containers: []Item{{Slug: "earnapp", Name: "EarnApp", Status: "running"}},
		SystemInfo: SystemInfo{OS: "darwin", DeviceType: "desktop", Version: "1.2.3"},
	}
	resp, err := New(loopbackOK()).Send(context.Background(), srv.URL, "tok", in)
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if gotPath != "/api/workers/heartbeat" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("auth = %q", gotAuth)
	}
	if gotCT != "application/json" {
		t.Errorf("content-type = %q", gotCT)
	}
	if resp.WorkerID != 7 {
		t.Errorf("worker_id = %d", resp.WorkerID)
	}
	// The server discovers what a worker runs via containers[*].slug. Sending
	// Desktop's own inbound shape (Apps as []string) earns a 422.
	if len(body.Containers) != 1 || body.Containers[0].Slug != "earnapp" {
		t.Errorf("containers did not survive as objects with a slug: %+v", body.Containers)
	}
	if body.SystemInfo.Version != "1.2.3" {
		t.Errorf("version not sent: %+v", body.SystemInfo)
	}
}

func TestSendTrimsATrailingSlash(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()
	// Without the trim this requests //api/workers/heartbeat, which some proxies
	// treat as a different path.
	if _, err := New(loopbackOK()).Send(context.Background(), srv.URL+"/", "tok", Payload{}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if gotPath != "/api/workers/heartbeat" {
		t.Fatalf("path = %q, want no double slash", gotPath)
	}
}

func TestSendReturnsTheIssuedWorkerKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok","worker_id":1,"worker_key":"issued-123"}`))
	}))
	defer srv.Close()
	resp, err := New(loopbackOK()).Send(context.Background(), srv.URL, "shared", Payload{})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if resp.WorkerKey != "issued-123" {
		t.Fatalf("worker_key = %q", resp.WorkerKey)
	}
	if got := KeyToPersist("", resp.WorkerKey); got != "issued-123" {
		t.Fatalf("the issued key would not be persisted: %q", got)
	}
}

func TestSendSurfacesTheStatusCode(t *testing.T) {
	// 401 and 422 mean very different things to a user -- a wrong key versus a
	// payload this server version does not accept -- so the code must survive.
	for _, code := range []int{http.StatusUnauthorized, http.StatusUnprocessableEntity, http.StatusInternalServerError} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
			_, _ = w.Write([]byte(`{"detail":"nope"}`))
		}))
		_, err := New(loopbackOK()).Send(context.Background(), srv.URL, "tok", Payload{})
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), http.StatusText(code)) && !strings.Contains(err.Error(), strconv.Itoa(code)) {
			t.Fatalf("code %d: got %v, want the status in the error", code, err)
		}
	}
}

func TestSendDoesNotTreatAnErrorBodyAsSuccess(t *testing.T) {
	// A 500 whose body happens to be valid JSON must not decode into a Response
	// and read as a successful heartbeat.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"status":"ok","worker_id":9}`))
	}))
	defer srv.Close()
	resp, err := New(loopbackOK()).Send(context.Background(), srv.URL, "tok", Payload{})
	if err == nil {
		t.Fatalf("a 500 decoded as success: %+v", resp)
	}
}

func TestSendRejectsAGarbageBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()
	if _, err := New(loopbackOK()).Send(context.Background(), srv.URL, "tok", Payload{}); err == nil {
		t.Fatal("a non-JSON 200 was accepted")
	}
}

func TestSendHonoursContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(loopbackOK()).Send(ctx, srv.URL, "tok", Payload{}); err == nil {
		t.Fatal("a cancelled context still sent a heartbeat")
	}
}

func TestSendOmitsAnEmptyVersionRatherThanSendingBlank(t *testing.T) {
	// An ABSENT version must read as unknown on the server, never as a match.
	var raw map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &raw)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()
	if _, err := New(loopbackOK()).Send(context.Background(), srv.URL, "tok", Payload{}); err != nil {
		t.Fatalf("send: %v", err)
	}
	si, _ := raw["system_info"].(map[string]any)
	if _, present := si["version"]; present {
		t.Fatalf("an empty version was sent as a key: %v", si)
	}
}
