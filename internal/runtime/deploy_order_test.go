package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// fakeDocker is a stand-in Docker daemon that records the calls a deploy makes. It
// exists because the order of those calls is the whole behaviour under test and it
// is invisible from Deploy's return value: a deploy that fails and a deploy that
// fails AFTER destroying the running container return the same error.
type fakeDocker struct {
	mu       sync.Mutex
	calls    []string
	pullFail bool
}

func (f *fakeDocker) record(call string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, call)
}

func (f *fakeDocker) recorded() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fakeDocker) removedAContainer() bool {
	for _, call := range f.recorded() {
		if strings.HasPrefix(call, "DELETE /containers/") {
			return true
		}
	}
	return false
}

// start brings up the fake and points the environment-based client at it, so
// Deploy's own dockerClient() reaches it with no seam added to production code.
func (f *fakeDocker) start(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Strip the negotiated /v1.xx prefix so assertions read like the API.
		path := r.URL.Path
		if i := strings.Index(path[1:], "/"); i >= 0 && strings.HasPrefix(path, "/v1.") {
			path = path[i+1:]
		}
		w.Header().Set("Api-Version", "1.51")
		w.Header().Set("Ostype", "linux")
		switch {
		case path == "/_ping":
			w.WriteHeader(http.StatusOK)
		case path == "/images/create":
			f.record("POST /images/create")
			if f.pullFail {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_ = json.NewEncoder(w).Encode(map[string]string{"message": "pull refused by the fake registry"})
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"Pulled"}` + "\n"))
		case path == "/containers/create":
			f.record("POST /containers/create")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": "deadbeef", "Warnings": []string{}})
		case strings.HasSuffix(path, "/start"):
			f.record("POST " + path)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && strings.HasPrefix(path, "/containers/"):
			f.record("DELETE " + path)
			w.WriteHeader(http.StatusNoContent)
		default:
			f.record(r.Method + " " + path)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)

	t.Setenv("DOCKER_HOST", "tcp://"+strings.TrimPrefix(srv.URL, "http://"))
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	t.Setenv("DOCKER_API_VERSION", "")
}

func deploySpec(slug string) DeploySpec {
	return DeploySpec{
		Slug: slug,
		Service: catalog.Service{
			Name: "Orderly",
			Docker: catalog.DockerConfig{
				Image: "example/orderly@sha256:0000000000000000000000000000000000000000000000000000000000000000",
			},
		},
	}
}

// THE RULE: a deploy that cannot succeed must leave the running container alone.
//
// A redeploy removes cashpilot-<slug> before creating its replacement, and the
// removal used to happen first. Anything that failed afterwards — a blocked device,
// a malformed mem_limit, a typo'd port, a registry that would not serve the image —
// therefore took down an earner that was working a second earlier and put nothing in
// its place. The user asked for a redeploy and got an uninstall.
//
// The positive control is the happy path below: the same fake daemon proves the
// removal really does happen when the deploy can go through, so "no DELETE" means
// the deploy stopped early and not that the assertion cannot fire.
func TestDeployLeavesTheRunningContainerAloneWhenItCannotSucceed(t *testing.T) {
	cases := []struct {
		name     string
		mutate   func(*DeploySpec)
		pullFail bool
		wantErr  string
	}{
		{
			name:    "device outside the ceiling",
			mutate:  func(s *DeploySpec) { s.Service.Docker.Devices = []string{"/dev/mem"} },
			wantErr: "/dev/mem",
		},
		{
			name:    "device with no host path",
			mutate:  func(s *DeploySpec) { s.Service.Docker.Devices = []string{":/dev/net/tun"} },
			wantErr: "no host path",
		},
		{
			name:    "malformed memory limit",
			mutate:  func(s *DeploySpec) { s.Service.Docker.Resources.MemLimit = "768 megabytes" },
			wantErr: "mem_limit",
		},
		{
			name:    "unparseable port mapping",
			mutate:  func(s *DeploySpec) { s.Service.Docker.Ports = []string{"8080:notaport"} },
			wantErr: "notaport",
		},
		{
			name:     "the image cannot be pulled",
			mutate:   func(s *DeploySpec) {},
			pullFail: true,
			wantErr:  "pull refused",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeDocker{pullFail: tc.pullFail}
			fake.start(t)

			spec := deploySpec("orderly")
			tc.mutate(&spec)

			_, err := (&DockerProvider{}).Deploy(context.Background(), spec, nil)
			if err == nil {
				t.Fatal("the deploy succeeded; this case is supposed to fail before anything is created")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not mention %q, so it may be failing for another reason", err, tc.wantErr)
			}
			if fake.removedAContainer() {
				t.Errorf("a deploy that failed with %v removed the running container first (calls: %v)", err, fake.recorded())
			}
			for _, call := range fake.recorded() {
				if strings.HasPrefix(call, "POST /containers/create") {
					t.Errorf("a deploy that failed still created a container (calls: %v)", fake.recorded())
				}
			}
		})
	}
}

// The positive control: a deploy that CAN succeed still removes the old container,
// still pulls, and does both in that order. Without this, the assertions above would
// pass against a Deploy that had simply stopped removing anything.
func TestDeployStillReplacesTheContainerOnTheHappyPath(t *testing.T) {
	fake := &fakeDocker{}
	fake.start(t)

	info, err := (&DockerProvider{}).Deploy(context.Background(), deploySpec("orderly"), nil)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if info.ContainerID != "deadbeef" || info.Name != containerName("orderly") {
		t.Fatalf("Deploy returned %+v", info)
	}

	calls := fake.recorded()
	var pull, remove, create int = -1, -1, -1
	for i, call := range calls {
		switch {
		case call == "POST /images/create":
			pull = i
		case strings.HasPrefix(call, "DELETE /containers/"):
			remove = i
		case call == "POST /containers/create":
			create = i
		}
	}
	if pull < 0 || remove < 0 || create < 0 {
		t.Fatalf("the deploy did not pull, remove and create; calls: %v", calls)
	}
	if !(pull < remove && remove < create) {
		t.Fatalf("expected pull -> remove -> create, got %v", calls)
	}
}
