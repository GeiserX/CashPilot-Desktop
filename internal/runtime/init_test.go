package runtime

import (
	"context"
	"testing"

	"github.com/moby/moby/api/types/container"
)

// THE RULE: an earner runs with Docker's init as PID 1, so a process orphaned inside
// the container is reaped instead of staying a zombie that counts against the PID
// ceiling. An image that starts its own small init keeps PID 1: s6-overlay's /init
// exits when it is not PID 1, and tini, dumb-init and s6-svscan already reap. The
// cases mirror the web worker's (tests/test_worker_resources.py).
func TestBringsOwnInit(t *testing.T) {
	cases := []struct {
		name                      string
		imageEntrypoint, imageCmd []string
		entrypoint, cmd           []string
		want                      bool
	}{
		{"s6-overlay", []string{"/init"}, nil, nil, nil, true},
		{"tini by path", []string{"/usr/bin/tini", "--", "/entrypoint.sh"}, nil, nil, nil, true},
		{"static tini", []string{"/sbin/tini-static", "--"}, nil, nil, nil, true},
		{"dumb-init in the cmd", nil, []string{"dumb-init", "app"}, nil, nil, true},
		{"s6-svscan", []string{"/command/s6-svscan"}, nil, nil, nil, true},
		{"plain earner", []string{"/app/main"}, nil, nil, nil, false},
		{"a script named like an init", []string{"/usr/local/bin/init-node.sh"}, nil, nil, nil, false},
		{"catalog entrypoint replaces the image's init", []string{"/init"}, nil, []string{"/app/run"}, nil, false},
		{"catalog command does not replace the entrypoint", []string{"/init"}, nil, nil, []string{"run"}, true},
		{"empty image", nil, nil, nil, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := bringsOwnInit(tc.imageEntrypoint, tc.imageCmd, tc.entrypoint, tc.cmd); got != tc.want {
				t.Fatalf("bringsOwnInit = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestApplyInitSkipsAWindowsDaemon(t *testing.T) {
	for _, osType := range []string{"linux", ""} {
		host := &container.HostConfig{}
		applyInit(host, false, osType)
		if host.Init == nil || !*host.Init {
			t.Fatalf("osType %q: Init = %v, want true", osType, host.Init)
		}
	}
	host := &container.HostConfig{}
	applyInit(host, false, "windows")
	if host.Init != nil {
		t.Fatalf("a Windows daemon got Init = %v", *host.Init)
	}
}

// The deploy path itself, through a fake daemon: what the pulled image runs decides
// Init on the container that is created. The plain image is the positive control for
// the own-init image, and the reverse.
func TestDeploySetsInitFromWhatTheImageRuns(t *testing.T) {
	created := func(imageConfig map[string]any, entrypoint []string) *bool {
		t.Helper()
		fake := &fakeDocker{imageConfig: imageConfig}
		fake.start(t)
		spec := deploySpec("orderly")
		spec.Service.Docker.Entrypoint = entrypoint
		if _, err := (&DockerProvider{}).Deploy(context.Background(), spec, nil); err != nil {
			t.Fatalf("Deploy: %v", err)
		}
		return createdHostConfig(t, fake).Init
	}
	if got := created(map[string]any{"Entrypoint": []string{"/app/main"}}, nil); got == nil || !*got {
		t.Fatalf("a plain earner was created without Docker's init: %v", got)
	}
	if got := created(map[string]any{"Entrypoint": []string{"/init"}}, nil); got == nil || *got {
		t.Fatalf("an s6-overlay image got Docker's init in front of its /init: %v", got)
	}
	if got := created(map[string]any{"Entrypoint": []string{"/init"}}, []string{"/app/run"}); got == nil || !*got {
		t.Fatalf("the catalog replaced the image's /init, yet the earner has no init: %v", got)
	}
}

// A proxied copy is created on its own path (deployProxied), so it gets the same.
func TestProxiedEarnerSetsInitFromWhatTheImageRuns(t *testing.T) {
	withTestTunnel(t)
	earnerInit := func(imageEntrypoint []string) *bool {
		t.Helper()
		f := newFakeTunnelDocker()
		f.imageEntrypoint = imageEntrypoint
		if _, err := deployProxied(context.Background(), f, deployRequest()); err != nil {
			t.Fatal(err)
		}
		return f.containers["cashpilot-earner-p3"].opts.HostConfig.Init
	}
	if got := earnerInit(nil); got == nil || !*got {
		t.Fatalf("a plain proxied earner was created without Docker's init: %v", got)
	}
	if got := earnerInit([]string{"/usr/bin/tini", "--"}); got == nil || *got {
		t.Fatalf("a tini image got Docker's init too: %v", got)
	}
}
