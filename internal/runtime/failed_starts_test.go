package runtime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
)

const goferError = "failed to create task for container: OCI runtime create failed: creating gofer filestore files: file exists"

// inspected is a container as docker inspect shows it.
func inspected(name string, labels map[string]string, status container.ContainerState, stateError string, policy container.RestartPolicyMode) container.InspectResponse {
	return container.InspectResponse{
		ID:         "id-" + name,
		Name:       "/" + name,
		State:      &container.State{Status: status, Error: stateError},
		HostConfig: &container.HostConfig{RestartPolicy: container.RestartPolicy{Name: policy}},
		Config:     &container.Config{Labels: labels},
	}
}

func earnerLabels(slug string) map[string]string {
	return map[string]string{LabelManaged: "true", LabelRuntime: desktopRuntime, LabelService: slug}
}

// THE RULE: Docker's own sign of a start it could not make is an exited container
// with State.Error set and a restart policy that says Docker means it to run. A run
// that ended, or a stop, leaves State.Error empty; a fence-gated node and a proxied
// copy have policy "no" and a guard of their own. The cases mirror the web worker's
// (tests/test_failed_start_retry.py).
func TestFailedStartReadsDockersSign(t *testing.T) {
	second := earnerLabels("honeygain")
	second[LabelInstance] = "2"
	proxied := earnerLabels("honeygain")
	proxied[LabelProxy] = "3"
	tunnel := earnerLabels("proxy-tunnel")
	tunnel[LabelRole] = RoleTunnel
	cases := []struct {
		name string
		c    container.InspectResponse
		want bool
	}{
		{"boot start failed, unless-stopped", inspected("cashpilot-honeygain", earnerLabels("honeygain"), container.StateExited, goferError, container.RestartPolicyUnlessStopped), true},
		{"boot start failed, always", inspected("cashpilot-honeygain", earnerLabels("honeygain"), container.StateExited, goferError, container.RestartPolicyAlways), true},
		{"a second copy", inspected("cashpilot-honeygain-2", second, container.StateExited, goferError, container.RestartPolicyUnlessStopped), true},
		{"exited on its own or stopped", inspected("cashpilot-honeygain", earnerLabels("honeygain"), container.StateExited, "", container.RestartPolicyUnlessStopped), false},
		{"only blanks in the error", inspected("cashpilot-honeygain", earnerLabels("honeygain"), container.StateExited, "  ", container.RestartPolicyUnlessStopped), false},
		{"fence-gated, policy no", inspected("cashpilot-mysterium", earnerLabels("mysterium"), container.StateExited, goferError, container.RestartPolicyDisabled), false},
		{"on-failure", inspected("cashpilot-honeygain", earnerLabels("honeygain"), container.StateExited, goferError, container.RestartPolicyOnFailure), false},
		{"running", inspected("cashpilot-honeygain", earnerLabels("honeygain"), container.StateRunning, goferError, container.RestartPolicyUnlessStopped), false},
		{"created, never started", inspected("cashpilot-honeygain", earnerLabels("honeygain"), container.StateCreated, goferError, container.RestartPolicyUnlessStopped), false},
		{"a proxied copy", inspected("cashpilot-honeygain-p3", proxied, container.StateExited, goferError, container.RestartPolicyUnlessStopped), false},
		{"a tunnel", inspected("cashpilot-tunnel-p3", tunnel, container.StateExited, goferError, container.RestartPolicyUnlessStopped), false},
		{"no service label", inspected("cashpilot-x", map[string]string{LabelManaged: "true"}, container.StateExited, goferError, container.RestartPolicyUnlessStopped), false},
		{"a CashPilot worker's", inspected("cashpilot-honeygain", map[string]string{LabelManaged: "true", LabelService: "honeygain"}, container.StateExited, goferError, container.RestartPolicyUnlessStopped), false},
		{"not ours", inspected("someone-else", map[string]string{LabelService: "honeygain"}, container.StateExited, goferError, container.RestartPolicyUnlessStopped), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, got := failedStart(tc.c); got != tc.want {
				t.Fatalf("failedStart = %v, want %v", got, tc.want)
			}
		})
	}
	got, _ := failedStart(cases[2].c)
	if got.Slug != "honeygain" || got.Instance != 2 || got.Name != "cashpilot-honeygain-2" || got.Error != goferError || got.ID != "id-cashpilot-honeygain-2" {
		t.Fatalf("FailedStart %+v", got)
	}
}

// FailedStarts against a daemon: it lists only exited managed containers, inspects
// each, and leaves out a set-aside one whose name is still held.
func TestFailedStartsAsksTheDaemon(t *testing.T) {
	setAside := earnerLabels("earnapp")
	containers := map[string]container.InspectResponse{
		"id-cashpilot-honeygain":        inspected("cashpilot-honeygain", earnerLabels("honeygain"), container.StateExited, goferError, container.RestartPolicyUnlessStopped),
		"id-cashpilot-bitping":          inspected("cashpilot-bitping", earnerLabels("bitping"), container.StateExited, "", container.RestartPolicyUnlessStopped),
		"id-cashpilot-earnapp-replaced": inspected("cashpilot-earnapp-replaced", setAside, container.StateExited, goferError, container.RestartPolicyUnlessStopped),
	}
	var listQuery string
	// inspectStatus, when set for an id, makes its inspect answer with that status.
	inspectStatus := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if i := strings.Index(path[1:], "/"); i >= 0 && strings.HasPrefix(path, "/v1.") {
			path = path[i+1:]
		}
		w.Header().Set("Api-Version", "1.51")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case path == "/containers/json":
			listQuery = r.URL.Query().Get("filters")
			var list []map[string]any
			for _, c := range containers {
				list = append(list, map[string]any{"Id": c.ID, "Names": []string{c.Name}, "State": string(c.State.Status), "Labels": c.Config.Labels})
			}
			_ = json.NewEncoder(w).Encode(list)
		case strings.HasPrefix(path, "/containers/") && strings.HasSuffix(path, "/json"):
			id := strings.TrimSuffix(strings.TrimPrefix(path, "/containers/"), "/json")
			if status := inspectStatus[id]; status != 0 {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"message":"inspect failed"}`))
				return
			}
			c, ok := containers[id]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte(`{"message":"No such container"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(c)
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("DOCKER_HOST", "tcp://"+strings.TrimPrefix(srv.URL, "http://"))
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	t.Setenv("DOCKER_API_VERSION", "")

	got, err := (&DockerProvider{}).FailedStarts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range got {
		names = append(names, f.Name)
	}
	if !slices.Equal(names, []string{"cashpilot-honeygain"}) {
		t.Fatalf("FailedStarts = %v", names)
	}
	if !strings.Contains(listQuery, `"status":{"exited":true}`) || !strings.Contains(listQuery, LabelManaged+"=true") {
		t.Fatalf("list filters %s", listQuery)
	}

	// A container removed between the list and its inspect is skipped.
	inspectStatus["id-cashpilot-honeygain"] = http.StatusNotFound
	if got, err := (&DockerProvider{}).FailedStarts(context.Background()); err != nil || len(got) != 0 {
		t.Fatalf("a removed container: %v, %v", got, err)
	}
	// Any other inspect failure fails the list: an incomplete pass would make the
	// caller forget when it last tried that container.
	inspectStatus["id-cashpilot-honeygain"] = http.StatusInternalServerError
	if got, err := (&DockerProvider{}).FailedStarts(context.Background()); err == nil {
		t.Fatalf("an inspect that failed gave a complete list: %v", got)
	}
}
