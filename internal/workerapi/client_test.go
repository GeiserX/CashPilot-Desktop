package workerapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeWorker records each request and answers with the handler's reply.
type fakeWorker struct {
	method, path, query, auth string
	body                      map[string]any
}

func newFakeWorker(t *testing.T, status int, reply string) (*httptest.Server, *fakeWorker) {
	t.Helper()
	seen := &fakeWorker{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.method, seen.path, seen.query, seen.auth = r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		seen.body = nil
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &seen.body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv, seen
}

func TestDeploySendsTheSpecWithTheWorkersKey(t *testing.T) {
	srv, seen := newFakeWorker(t, 200, `{"status":"deployed","container_id":"abc123","kept_mounts":[{"target":"/data","kept":"/old","requested":"/new"}]}`)
	mode := "bridge"
	spec := DeploySpec{
		Image:       "repo/img:1",
		Env:         map[string]string{"TOKEN": "t0ken-value"},
		Ports:       map[string]any{"80/tcp": 8080},
		Volumes:     map[string]Mount{"/srv/x": {Bind: "/data", Mode: "rw"}},
		NetworkMode: &mode,
		MovedMounts: []string{},
	}
	c := &Client{BaseURL: srv.URL + "/", Key: "own-key"}
	got, err := c.Deploy(context.Background(), "my svc", spec, 5*time.Second)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if seen.method != http.MethodPost || seen.path != "/api/containers/my svc/deploy" {
		t.Fatalf("sent %s %s", seen.method, seen.path)
	}
	if seen.auth != "Bearer own-key" {
		t.Fatalf("Authorization = %q, want the worker's own key", seen.auth)
	}
	if seen.body["image"] != "repo/img:1" || seen.body["hostname"] != nil || seen.body["privileged"] != false {
		t.Fatalf("body = %v", seen.body)
	}
	if env, _ := seen.body["env"].(map[string]any); env["TOKEN"] != "t0ken-value" {
		t.Fatalf("env = %v: the deploy must carry the credentials", seen.body["env"])
	}
	for _, key := range []string{"cap_add", "devices", "moved_mounts", "network_mode", "ports", "volumes"} {
		if _, ok := seen.body[key]; !ok {
			t.Fatalf("body has no %q, which the web UI always sends: %v", key, seen.body)
		}
	}
	if _, ok := seen.body["approval"]; ok {
		t.Fatal("an unsigned deploy must not send an approval field")
	}
	if got.ContainerID != "abc123" || len(got.KeptMounts) != 1 || got.KeptMounts[0].Kept != "/old" {
		t.Fatalf("result = %+v", got)
	}
}

func TestEachCommandUsesTheWorkersRoute(t *testing.T) {
	cases := []struct {
		command  string
		instance int
		method   string
		path     string
		query    string
	}{
		{CommandStop, 1, http.MethodPost, "/api/containers/honeygain/stop", ""},
		{CommandStop, 2, http.MethodPost, "/api/containers/honeygain/stop", "instance=2"},
		{CommandStart, 1, http.MethodPost, "/api/containers/honeygain/start", ""},
		{CommandRestart, 3, http.MethodPost, "/api/containers/honeygain/restart", "instance=3"},
		{CommandRemove, 1, http.MethodDelete, "/api/containers/honeygain", ""},
		{CommandRemove, 2, http.MethodDelete, "/api/containers/honeygain/instances/2", ""},
	}
	for _, tc := range cases {
		srv, seen := newFakeWorker(t, 200, `{"status":"ok"}`)
		c := &Client{BaseURL: srv.URL, Key: "k"}
		if err := c.Command(context.Background(), "honeygain", tc.command, tc.instance, time.Second); err != nil {
			t.Fatalf("%s %d: %v", tc.command, tc.instance, err)
		}
		if seen.method != tc.method || seen.path != tc.path || seen.query != tc.query || seen.auth != "Bearer k" {
			t.Fatalf("%s %d sent %s %s?%s auth %q", tc.command, tc.instance, seen.method, seen.path, seen.query, seen.auth)
		}
	}
	c := &Client{BaseURL: "http://127.0.0.1:1", Key: "k"}
	if err := c.Command(context.Background(), "x", "delete-everything", 1, time.Second); err == nil {
		t.Fatal("an unknown command must be refused before anything is sent")
	}
}

func TestARefusedSpecShowsTheWorkersOwnWords(t *testing.T) {
	srv, _ := newFakeWorker(t, 403, `{"detail":"Volume /etc is not allowed, token t0ken-value-123 rejected\n"}`)
	c := &Client{BaseURL: srv.URL, Key: "k"}
	_, err := c.Deploy(context.Background(), "x", DeploySpec{Image: "i", Env: map[string]string{"TOKEN": "t0ken-value-123", "PUID": "1000"}}, time.Second)
	var werr *Error
	if !errors.As(err, &werr) || werr.Status != 403 {
		t.Fatalf("err = %#v", err)
	}
	if werr.Message != "Volume /etc is not allowed, token *** rejected" {
		t.Fatalf("message = %q: the worker's own reason, one line, with the credential masked", werr.Message)
	}
}

func TestAStructuredRefusalKeepsItsCode(t *testing.T) {
	srv, _ := newFakeWorker(t, 403, `{"detail":{"error":"route_locked","message":"honeygain runs through a proxy here."}}`)
	c := &Client{BaseURL: srv.URL, Key: "k"}
	_, err := c.Deploy(context.Background(), "honeygain", DeploySpec{Image: "i"}, time.Second)
	var werr *Error
	if !errors.As(err, &werr) || werr.Code != "route_locked" || werr.Message != "honeygain runs through a proxy here." {
		t.Fatalf("err = %#v", err)
	}
}

func TestAnUnvettedAnswerIsNotShown(t *testing.T) {
	for status, body := range map[int]string{
		500: `{"detail":"Traceback ... /secret/path"}`,
		422: `{"detail":[{"input":"t0ken-value-123"}]}`,
		404: `{"detail":"Not Found"}`,
	} {
		srv, _ := newFakeWorker(t, status, body)
		c := &Client{BaseURL: srv.URL, Key: "k"}
		err := c.Command(context.Background(), "x", CommandStop, 1, time.Second)
		if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "t0ken") {
			t.Fatalf("%d: err = %v", status, err)
		}
		if !strings.Contains(err.Error(), "HTTP") {
			t.Fatalf("%d: %q should name the status and where the reason is", status, err)
		}
	}
}

func TestAnOfflineWorkerSaysItCouldNotBeReached(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	c := &Client{BaseURL: url, Key: "k"}
	err := c.Command(context.Background(), "x", CommandStart, 1, 2*time.Second)
	var werr *Error
	if !errors.As(err, &werr) || werr.Unknown || !strings.Contains(werr.Message, "Could not reach the worker") {
		t.Fatalf("err = %#v", err)
	}
}

func TestASlowWorkerMayStillFinish(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Key: "k"}
	err := c.Command(context.Background(), "x", CommandStop, 1, 50*time.Millisecond)
	var werr *Error
	if !errors.As(err, &werr) || !werr.Unknown || !strings.Contains(werr.Message, "may still be working on it") {
		t.Fatalf("err = %#v", err)
	}
}

func TestTheKeyNeverFollowsARedirect(t *testing.T) {
	var leaked string
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = r.Header.Get("Authorization") }))
	defer other.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/steal", http.StatusTemporaryRedirect)
	}))
	defer srv.Close()
	c := &Client{BaseURL: srv.URL, Key: "k"}
	_ = c.Command(context.Background(), "x", CommandStop, 1, time.Second)
	if leaked != "" {
		t.Fatalf("the key went to the redirect target: %q", leaked)
	}
}

func TestValidBaseURL(t *testing.T) {
	for _, bad := range []string{"", "ftp://x", "http://", "http://u:p@host:8081", "host:8081", "http://h:1/?a=b"} {
		if _, err := ValidBaseURL(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if got, err := ValidBaseURL(" http://worker.lan:8081/ "); err != nil || got != "http://worker.lan:8081" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestTimeoutsCoverTheStop(t *testing.T) {
	if got := DeployTimeout(300); got != 480*time.Second {
		t.Fatalf("deploy timeout = %v", got)
	}
	if got := CommandTimeout(CommandStop, 0, false); got != 60*time.Second {
		t.Fatalf("stop timeout = %v", got)
	}
	if got := CommandTimeout(CommandStart, 300, false); got != 30*time.Second {
		t.Fatalf("start timeout = %v", got)
	}
	if got := CommandTimeout(CommandStop, 30, true); got != 270*time.Second {
		t.Fatalf("fence-gated stop timeout = %v", got)
	}
}

func TestDeployStateDistinguishesAnOlderWorkerFromLookupFailure(t *testing.T) {
	cases := []struct {
		name              string
		status            int
		body              string
		supported, exists bool
		wantErr           bool
	}{
		{name: "existing", status: 200, body: `{"exists":true}`, supported: true, exists: true},
		{name: "absent", status: 200, body: `{"exists":false}`, supported: true},
		{name: "older worker", status: 404, body: `{"detail":"Not Found"}`},
		{name: "lookup failed", status: 503, body: `{"detail":"Docker offline"}`, wantErr: true},
		{name: "another 404", status: 404, body: `{"detail":"could not inspect Docker"}`, wantErr: true},
		{name: "malformed state", status: 200, body: `{}`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, seen := newFakeWorker(t, tc.status, tc.body)
			c := &Client{BaseURL: srv.URL, Key: "own-key"}
			supported, exists, err := c.DeployState(context.Background(), "honeygain")
			if supported != tc.supported || exists != tc.exists || (err != nil) != tc.wantErr {
				t.Fatalf("state = %t, %t, %v", supported, exists, err)
			}
			if seen.method != http.MethodGet || seen.path != "/api/containers/honeygain/deploy-checked" || seen.auth != "Bearer own-key" {
				t.Fatalf("lookup = %+v", seen)
			}
		})
	}
}

func TestLegacyDeployInventoryLooksForTheExactContainerName(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		exists  bool
		wantErr bool
	}{
		{name: "stopped copy", status: 200, body: `[{"name":"cashpilot-honeygain","status":"exited"}]`, exists: true},
		{name: "other copy", status: 200, body: `[{"name":"cashpilot-honeygain-2","status":"running"}]`},
		{name: "error", status: 503, body: `{"detail":"Docker offline"}`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, seen := newFakeWorker(t, tc.status, tc.body)
			c := &Client{BaseURL: srv.URL, Key: "own-key"}
			exists, err := c.LegacyDeployTargetExists(context.Background(), "honeygain")
			if exists != tc.exists || (err != nil) != tc.wantErr {
				t.Fatalf("inventory = %t, %v", exists, err)
			}
			if seen.method != http.MethodGet || seen.path != "/api/containers" || seen.auth != "Bearer own-key" {
				t.Fatalf("lookup = %+v", seen)
			}
		})
	}
}

func TestCheckedDeployCarriesOnlyExplicitReplacementApproval(t *testing.T) {
	srv, seen := newFakeWorker(t, 200, `{"status":"deployed","container_id":"new"}`)
	c := &Client{BaseURL: srv.URL, Key: "own-key"}
	for _, approved := range []bool{false, true} {
		res, err := c.DeployChecked(context.Background(), "honeygain", DeploySpec{Image: "img", ReplaceExisting: approved}, time.Second)
		if err != nil || res.ContainerID != "new" {
			t.Fatalf("checked deploy = %+v, %v", res, err)
		}
		value, present := seen.body["replace_existing"]
		if present != approved || (present && value != true) {
			t.Fatalf("approved = %t, sent %v", approved, seen.body)
		}
		if seen.method != http.MethodPost || seen.path != "/api/containers/honeygain/deploy-checked" || seen.body["image"] != "img" || seen.auth != "Bearer own-key" {
			t.Fatalf("deploy = %+v", seen)
		}
	}
}
