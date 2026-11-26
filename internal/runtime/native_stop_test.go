package runtime

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// errNoSignal is what os.Process.Signal returns on Windows for anything but Kill.
var errNoSignal = errors.New("signal not supported on this platform")

func noSignal(*os.Process) error { return errNoSignal }

// startStub launches the test binary as a long-running stub child and returns it
// with a channel that closes once the child has been reaped.
func startStub(t *testing.T) (*exec.Cmd, <-chan struct{}) {
	t.Helper()
	binPath := filepath.Join(t.TempDir(), stubBinName())
	if err := os.WriteFile(binPath, stubBinaryBytes(t), 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "marker")
	cmd := exec.Command(binPath)
	cmd.Env = append(os.Environ(), "CASHPILOT_NATIVE_STUB=1", "CASHPILOT_STUB_MARKER="+marker)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start stub: %v", err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-done })
	if !waitFor(t, 10*time.Second, func() bool { return fileExists(marker) }) {
		t.Fatal("the stub never wrote its marker")
	}
	return cmd, done
}

// THE RULE: when the graceful signal cannot be delivered (every stop on Windows,
// where os.Process.Signal supports only Kill), a stop kills the process at once.
// It does not wait out a grace period that no signal started.
func TestGracefulKillKillsAtOnceWhenNoSignalIsDelivered(t *testing.T) {
	cmd, done := startStub(t)

	const grace = 30 * time.Second
	start := time.Now()
	returned := make(chan struct{})
	go func() { gracefulKill(cmd, grace, done, noSignal); close(returned) }()

	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatalf("gracefulKill still waiting after %v with a %v grace: an undelivered signal sat out the grace period", time.Since(start).Round(time.Millisecond), grace)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the process is still alive after gracefulKill returned")
	}
}

// The same rule for an orphan recorded by a previous session, stopped by pid.
func TestNativeStopKillsOrphanAtOnceWhenNoSignalIsDelivered(t *testing.T) {
	p := newTestNativeProvider(t)
	p.stopTimeout = 30 * time.Second
	p.terminate = noSignal

	cmd, done := startStub(t)
	if err := p.mutateRegistry(func(reg *nativeRegistry) {
		reg.Entries["orphan"] = &nativeRegistryEntry{Slug: "orphan", PID: cmd.Process.Pid, BinPath: cmd.Path, URL: "https://example.test/x", Desired: "running"}
	}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}

	start := time.Now()
	stopped := make(chan error, 1)
	go func() { stopped <- p.Stop(context.Background(), "orphan", 0) }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("Stop orphan: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("Stop still waiting after %v with a %v grace: an undelivered signal sat out the grace period", time.Since(start).Round(time.Millisecond), p.stopTimeout)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the orphan is still alive after Stop returned")
	}
}

// A real stop of a real earner with the catalog's default 30 s grace returns well
// under that grace, on every platform CI runs: on Unix because SIGTERM ends the stub,
// on Windows because there is no SIGTERM and the stop kills at once.
func TestNativeStopOfARunningEarnerDoesNotWaitTheGrace(t *testing.T) {
	p := newTestNativeProvider(t)
	art := tarGzArtifact(t, stubBinName(), stubBinaryBytes(t))
	url := serveArtifact(t, p, art)
	marker := filepath.Join(t.TempDir(), "marker")
	svc := nativeService("stubsvc", url, sha256hex(art), "tar.gz")

	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: "stubsvc", Service: svc, Env: stubEnv(marker)}, nil); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if !waitFor(t, 10*time.Second, func() bool { return fileExists(marker) }) {
		t.Fatal("stub marker never written (binary did not execute)")
	}

	const graceSeconds = defaultStopTimeout
	start := time.Now()
	var stopErr error
	stopped := make(chan struct{})
	go func() { stopErr = p.Stop(context.Background(), "stubsvc", graceSeconds); close(stopped) }()
	// On a failure the stop still ends the child; wait for it so no stub outlives the test.
	t.Cleanup(func() { <-stopped })
	select {
	case <-stopped:
		if stopErr != nil {
			t.Fatalf("Stop: %v", stopErr)
		}
		t.Logf("Stop returned after %v with a %ds grace", time.Since(start).Round(time.Millisecond), graceSeconds)
	case <-time.After(10 * time.Second):
		t.Fatalf("Stop still waiting after %v with a %ds grace", time.Since(start).Round(time.Millisecond), graceSeconds)
	}
}
