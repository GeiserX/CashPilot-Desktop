package runtime

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

// A stuck proxied copy restarts in place: only its earner restarts, inside the
// tunnel that already runs, never a stop and a start of the pair.
func TestRestartProxiedRestartsOnlyTheEarnerInItsReadyTunnel(t *testing.T) {
	withTestTunnel(t)
	f := newFakeTunnelDocker()
	ctx := context.Background()
	if _, err := deployProxied(ctx, f, deployRequest()); err != nil {
		t.Fatal(err)
	}
	before := len(f.callList())
	if err := restartProxied(ctx, f, "earner", 3, 0); err != nil {
		t.Fatalf("restartProxied: %v", err)
	}
	calls := f.callList()[before:]
	if !slices.Equal(calls, []string{"restart cashpilot-earner-p3"}) {
		t.Fatalf("calls = %v, want the earner's restart alone", calls)
	}
	if err := restartProxied(ctx, f, "earner", 4, 0); err == nil {
		t.Fatal("restarted a copy through proxy 4, which has none here")
	}
}

// The owner and the traffic cap stop a copy by stopping its tunnel. An automatic
// restart must never bring such a copy back, nor touch one whose earner is down or
// whose tunnel has not proved its exit.
func TestRestartProxiedLeavesACopyThatIsNotUp(t *testing.T) {
	cases := map[string]func(f *fakeTunnelDocker){
		"stopped by the owner or the cap": func(f *fakeTunnelDocker) {
			if _, err := stopProxied(context.Background(), f, "earner", 3, 0); err != nil {
				t.Fatal(err)
			}
		},
		"earner down":   func(f *fakeTunnelDocker) { f.containers["cashpilot-earner-p3"].state = "exited" },
		"tunnel down":   func(f *fakeTunnelDocker) { f.containers["cashpilot-tunnel-earner-p3"].state = "exited" },
		"no exit found": func(f *fakeTunnelDocker) { f.exit = "" },
	}
	for name, prepare := range cases {
		t.Run(name, func(t *testing.T) {
			withTestTunnel(t)
			f := newFakeTunnelDocker()
			ctx := context.Background()
			if _, err := deployProxied(ctx, f, deployRequest()); err != nil {
				t.Fatal(err)
			}
			prepare(f)
			before := len(f.callList())
			if err := restartProxied(ctx, f, "earner", 3, 0); err == nil {
				t.Fatal("restartProxied succeeded on a copy that is not up")
			}
			if calls := f.callList()[before:]; len(calls) != 0 {
				t.Fatalf("touched a copy that is not up: %v", calls)
			}
		})
	}
}

// Copy n is read and restarted as itself: never the first copy, never another one.
func TestCopyLogsMatchingAndRestartCopyTouchOnlyThatCopy(t *testing.T) {
	docker := startHarnessDocker(t)
	ctx := context.Background()
	p := NewDockerProvider()
	for n := 1; n <= 2; n++ {
		if _, err := p.Deploy(ctx, DeploySpec{Slug: "cmd-example", Service: commandService, Env: map[string]string{"TOKEN": "t"}, Instance: n}, nil); err != nil {
			t.Fatalf("Deploy copy %d: %v", n, err)
		}
	}
	docker.SetLogs("Failed to connect\nnoise\n")
	got, err := p.CopyLogsMatching(ctx, "cmd-example", 2, time.Hour, regexp.MustCompile("Failed"), 100)
	if err != nil || got != "Failed to connect" {
		t.Fatalf("CopyLogsMatching(2) = %q, %v", got, err)
	}
	if _, err := p.CopyLogsMatching(ctx, "cmd-example", 3, time.Hour, regexp.MustCompile("Failed"), 100); err == nil {
		t.Fatal("CopyLogsMatching answered for copy 3, which does not exist")
	}
	docker.ResetCalls()
	if err := p.RestartCopy(ctx, "cmd-example", 2, 0); err != nil {
		t.Fatalf("RestartCopy(2): %v", err)
	}
	var restarts []string
	for _, call := range docker.Calls() {
		if strings.HasSuffix(call, "/restart") {
			restarts = append(restarts, call)
		}
	}
	if len(restarts) != 1 || !strings.Contains(restarts[0], copyName("cmd-example", 2)+"/restart") {
		t.Fatalf("restart calls = %v, want copy 2's alone", restarts)
	}
	if err := p.RestartCopy(ctx, "cmd-example", 1, 0); err == nil {
		t.Fatal("RestartCopy took copy 1, which is the service itself")
	}
}
