package main

import (
	"context"
	goruntime "runtime"
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

type reasonProvider struct{ stubProvider }

func (reasonProvider) Status(context.Context) runtime.Status {
	return runtime.Status{Reason: runtime.ReasonWSLMissing}
}
