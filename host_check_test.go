package main

import (
	"context"
	goruntime "runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/winvm"
)

// THE RULE: a registered native runtime is not "ready" by itself. It counts only
// when it can run a catalog service on this machine, and the status names those
// services, so first run can say "only Mysterium runs without Docker" instead of
// "ready".
func TestRuntimeStatusCountsNativeOnlyWhenItRunsSomething(t *testing.T) {
	app := newRemovalTestApp(t, stubProvider{})
	nativeHere := catalog.Service{Name: "Mysterium", Slug: "mysterium", Native: catalog.NativeConfig{Binaries: []catalog.NativeBinary{{OS: goruntime.GOOS, Arch: goruntime.GOARCH}}}}
	imageOnly := catalog.Service{Name: "Honeygain", Slug: "honeygain"}

	app.services.Register(runtime.NativeRuntimeKind, stubProvider{})
	st := app.runtimeStatus([]catalog.Service{imageOnly})
	if st.NativeAvailable || len(st.NativeServices) != 0 {
		t.Fatalf("a native runtime that runs no catalog service counted as available: %+v", st)
	}
	st = app.runtimeStatus([]catalog.Service{imageOnly, nativeHere})
	if !st.NativeAvailable || len(st.NativeServices) != 1 || st.NativeServices[0] != "Mysterium" {
		t.Fatalf("the native runtime that runs Mysterium: %+v", st)
	}
}

func TestHostFactsHintsNeverWaitAndRefreshInTheBackground(t *testing.T) {
	var reads atomic.Int32
	release := make(chan struct{})
	h := &hostFacts{read: func(context.Context) winvm.HostFacts {
		reads.Add(1)
		<-release
		f := winvm.HostFacts{Virtualization: winvm.No, WSL: winvm.WSLOutdated, WSLVersion: "2.0.9.0", DockerDesktopInstalled: winvm.Yes}
		return f
	}}
	start := time.Now()
	if got := h.hints(context.Background()); got != (runtime.HostHints{}) {
		t.Fatalf("an empty cache returned hints: %+v", got)
	}
	_ = h.hints(context.Background()) // a second call while reading starts no second read
	if took := time.Since(start); took > time.Second {
		t.Fatalf("hints waited %v for the reading", took)
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	var got runtime.HostHints
	for time.Now().Before(deadline) {
		if got = h.hints(context.Background()); got.WSL != "" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got.Virtualization != runtime.HintNo || got.WSL != "outdated" || got.WSLVersion != "2.0.9.0" || got.DockerInstalled != runtime.HintYes {
		t.Fatalf("the background reading did not reach the hints: %+v", got)
	}
	if n := reads.Load(); n != 1 {
		t.Fatalf("%d readings started, want 1", n)
	}
}

func TestHostCheckReturnsFactsAndTheRuntimeReason(t *testing.T) {
	app := newRemovalTestApp(t, reasonProvider{})
	app.host = &hostFacts{read: func(context.Context) winvm.HostFacts {
		return winvm.HostFacts{OS: "windows", Supported: true, Build: 26100}
	}}
	res, err := app.HostCheck()
	if err != nil {
		t.Fatal(err)
	}
	if res.Facts.Build != 26100 || res.Runtime.Reason != runtime.ReasonWSLMissing {
		t.Fatalf("HostCheck returned %+v", res)
	}
}

func TestRemoteEngineKeepsLocalDockerSetupInAlerts(t *testing.T) {
	provider := &engineProvider{status: runtime.Status{Available: true, Reason: runtime.ReasonRunning}}
	app := gateApp(t, provider)
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:2375")
	app.host = &hostFacts{read: func(context.Context) winvm.HostFacts {
		return winvm.HostFacts{OS: "windows", Supported: true, Build: 26100, Virtualization: winvm.Yes, WSL: winvm.WSLMissing}
	}}
	res, err := app.HostCheck()
	if err != nil {
		t.Fatal(err)
	}
	if res.SetupFinished || !res.Runtime.Available {
		t.Fatalf("remote engine changed local setup completion or engine availability: %+v", res)
	}
	if res.RuntimeNotice == nil || !res.RuntimeNotice.Runtime || !strings.Contains(res.RuntimeNotice.Message, "WSL") {
		t.Fatalf("HostCheck did not return the runtime alert with the reading: %+v", res.RuntimeNotice)
	}
	state, err := app.GetAppState()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range state.Notifications {
		if item.Title == "Docker setup needs you" && strings.Contains(item.Message, "WSL") {
			found = true
		}
	}
	if !found {
		t.Errorf("alerts omit local WSL setup: %+v", state.Notifications)
	}
	if _, err := app.DeployService("earnfm", map[string]string{"EARNFM_TOKEN": "token"}, nil, nil); err != nil {
		t.Fatalf("working engine should still deploy: %v", err)
	}
}

func TestRemoteEngineCannotHideStoppedDockerDesktop(t *testing.T) {
	app := gateApp(t, &engineProvider{status: runtime.Status{Available: true, Reason: runtime.ReasonRunning}})
	t.Setenv("DOCKER_HOST", "tcp://192.0.2.1:2375")
	app.host = &hostFacts{read: func(context.Context) winvm.HostFacts {
		return winvm.HostFacts{
			OS: "windows", Supported: true, Build: 26100, Virtualization: winvm.Yes,
			WSL: winvm.WSLReady, WSLVersion: "2.3.0", VMPlatformFeature: winvm.Yes,
			DockerDesktopInstalled: winvm.Yes, DockerDesktopRunning: winvm.No, DockerDesktopStarted: winvm.No,
		}
	}}
	res, err := app.HostCheck()
	if err != nil {
		t.Fatal(err)
	}
	rows := make(map[string]string)
	for _, step := range res.Steps {
		rows[step.ID] = step.State
	}
	if rows["a6"] != winvm.StepNeedsYou || rows["a7"] != winvm.StepNeedsYou || rows["a9"] != winvm.StepBlocked {
		t.Errorf("running, terms and Linux containers rows = %q, %q, %q", rows["a6"], rows["a7"], rows["a9"])
	}
	if !res.Runtime.Available || res.SetupFinished || !strings.Contains(res.Runtime.SetupTask, "Docker Desktop is not running") {
		t.Errorf("remote engine availability or local setup is wrong: %+v", res)
	}
	if res.RuntimeNotice == nil || !strings.Contains(res.RuntimeNotice.Message, "Docker Desktop is not running") {
		t.Errorf("Alerts omit the stopped desktop: %+v", res.RuntimeNotice)
	}
	if _, err := app.DeployService("earnfm", map[string]string{"EARNFM_TOKEN": "token"}, nil, nil); err != nil {
		t.Fatalf("working remote engine should still deploy: %v", err)
	}
}

func TestAppStateRefreshesStaleWindowsFactsWithWorkingEngine(t *testing.T) {
	app := gateApp(t, &engineProvider{status: runtime.Status{Available: true, Reason: runtime.ReasonRunning}})
	t.Setenv("DOCKER_HOST", "tcp://127.0.0.1:2375")
	old := winvm.HostFacts{
		OS: "windows", Supported: true, Build: 26100, Virtualization: winvm.Yes,
		WSL: winvm.WSLReady, WSLVersion: "2.3.0", VMPlatformFeature: winvm.Yes,
	}
	started, release := make(chan struct{}), make(chan struct{})
	app.host = &hostFacts{facts: old, at: time.Now().Add(-20 * time.Minute), read: func(context.Context) winvm.HostFacts {
		close(started)
		<-release
		fresh := old
		fresh.WSL = winvm.WSLMissing
		return fresh
	}}
	if _, err := app.GetAppState(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("GetAppState did not start a Windows reading with the working loopback engine")
	}
	close(release)
	deadline := time.Now().Add(time.Second)
	for app.host.cached().WSL != winvm.WSLMissing && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	state, err := app.GetAppState()
	if err != nil {
		t.Fatal(err)
	}
	if !state.Runtime.Available || !strings.Contains(state.Runtime.SetupTask, "WSL") {
		t.Errorf("runtime did not pick up the new Windows reading: %+v", state.Runtime)
	}
	for _, notice := range state.Notifications {
		if notice.Runtime && strings.Contains(notice.Message, "WSL") {
			return
		}
	}
	t.Errorf("Alerts omit the refreshed Windows reading: %+v", state.Notifications)
}

// CheckRuntime runs after every deploy, stop, restart and remove (refreshState),
// so it must answer from the cache and never start a host reading of its own.
func TestCheckRuntimeNeverReadsTheHost(t *testing.T) {
	app := newRemovalTestApp(t, reasonProvider{})
	var reads atomic.Int32
	app.host = &hostFacts{read: func(context.Context) winvm.HostFacts {
		reads.Add(1)
		return winvm.HostFacts{}
	}}
	st, err := app.CheckRuntime()
	if err != nil {
		t.Fatal(err)
	}
	if st.Reason != runtime.ReasonWSLMissing {
		t.Fatalf("CheckRuntime returned %+v", st)
	}
	if n := reads.Load(); n != 0 {
		t.Fatalf("CheckRuntime read the host %d times, want 0", n)
	}
}

// A background reading that started first and ends last must not overwrite the
// reading HostCheck made after it.
func TestHostFactsKeepTheNewerReading(t *testing.T) {
	var calls atomic.Int32
	releaseOld := make(chan struct{})
	h := &hostFacts{read: func(context.Context) winvm.HostFacts {
		if calls.Add(1) == 1 {
			<-releaseOld
			return winvm.HostFacts{Build: 1}
		}
		return winvm.HostFacts{Build: 2}
	}}
	oldDone := make(chan winvm.HostFacts)
	go func() { oldDone <- h.refresh(context.Background()) }()
	for calls.Load() < 1 {
		time.Sleep(time.Millisecond)
	}
	if got := h.refresh(context.Background()); got.Build != 2 {
		t.Fatalf("the newer reading returned build %d", got.Build)
	}
	close(releaseOld)
	if got := <-oldDone; got.Build != 1 {
		t.Fatalf("the older reading returned build %d, want its own reading", got.Build)
	}
	h.mu.Lock()
	build := h.facts.Build
	h.mu.Unlock()
	if build != 2 {
		t.Fatalf("the older reading overwrote the newer one: cached build %d, want 2", build)
	}
}

type reasonProvider struct{ stubProvider }

func (reasonProvider) Status(context.Context) runtime.Status {
	return runtime.Status{Reason: runtime.ReasonWSLMissing}
}
