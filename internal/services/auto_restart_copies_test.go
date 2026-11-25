package services

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/health"
	"github.com/GeiserX/CashPilot-Desktop/internal/proxies"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
)

// The manager is what the windowed check reads and restarts every copy through.
var (
	_ health.CopyWindowReader = (*Manager)(nil)
	_ health.CopyRestarter    = (*Manager)(nil)
)

// restartingCopies is a copyFake that can also restart a copy in place.
type restartingCopies struct {
	*copyFake
	restarted []int
}

func (f *restartingCopies) RestartCopy(_ context.Context, _ string, n, _ int) error {
	f.restarted = append(f.restarted, n)
	return nil
}

func newRestartingCopies(list []runtime.ContainerInfo) *restartingCopies {
	f := &restartingCopies{copyFake: newCopyFake()}
	f.listResult = list
	return f
}

var copy2Running = []runtime.ContainerInfo{
	{Slug: "example", Instance: 1, Status: "running"},
	{Slug: "example", Instance: 2, Status: "running"},
}

func TestRestartStuckCopyRestartsThatCopyAndNamesIt(t *testing.T) {
	dir, st := newTestStoreInDir(t)
	f := newRestartingCopies(copy2Running)
	m := NewManager(f, newTestCatalog(t), st)
	if err := m.RestartStuckCopy(context.Background(), "example", 2, 0, time.Now(), "it has not reached its relay"); err != nil {
		t.Fatalf("RestartStuckCopy: %v", err)
	}
	if len(f.restarted) != 1 || f.restarted[0] != 2 || len(f.restartTimeouts) != 0 {
		t.Fatalf("copy restarts = %v, first-copy restarts = %d; want copy 2 alone", f.restarted, len(f.restartTimeouts))
	}
	if got, ok := eventDetail(t, dir, "example", "auto_restarted"); !ok || got != "copy 2: it has not reached its relay" {
		t.Fatalf("auto_restarted event = %q (found %v), want it to name copy 2", got, ok)
	}
}

// As for the first copy: the owner's Stop of copy 2 during the check wins, and a copy
// that is not running now is left alone.
func TestRestartStuckCopyLeavesACopyStoppedOrDown(t *testing.T) {
	t.Run("stopped by the owner during the check", func(t *testing.T) {
		_, st := newTestStoreInDir(t)
		f := newRestartingCopies(copy2Running)
		m := NewManager(f, newTestCatalog(t), st)
		listedAt := time.Now()
		if err := m.StopCopy(context.Background(), "example", 2); err != nil {
			t.Fatalf("StopCopy: %v", err)
		}
		if err := m.RestartStuckCopy(context.Background(), "example", 2, 0, listedAt, "stuck"); err != nil {
			t.Fatalf("RestartStuckCopy: %v", err)
		}
		if len(f.restarted) != 0 {
			t.Fatalf("restarted copy 2 after the owner stopped it")
		}
	})
	t.Run("not running now", func(t *testing.T) {
		_, st := newTestStoreInDir(t)
		f := newRestartingCopies([]runtime.ContainerInfo{{Slug: "example", Instance: 1, Status: "running"}, {Slug: "example", Instance: 2, Status: "exited"}})
		m := NewManager(f, newTestCatalog(t), st)
		if err := m.RestartStuckCopy(context.Background(), "example", 2, 0, time.Now(), "stuck"); err != nil {
			t.Fatalf("RestartStuckCopy: %v", err)
		}
		if len(f.restarted) != 0 {
			t.Fatalf("restarted copy 2 while it is not running")
		}
	})
}

// restartingProxies is a fakeProxyProvider that can restart a proxied copy in place.
type restartingProxies struct {
	*fakeProxyProvider
	restarted []string
	err       error
}

func (f *restartingProxies) RestartProxied(_ context.Context, slug string, id int64, _ int) error {
	f.restarted = append(f.restarted, key(slug, id))
	return f.err
}

func newRestartingProxyManager(t *testing.T) (*Manager, *restartingProxies, string, int64) {
	t.Helper()
	dir, st := newTestStoreInDir(t)
	p := &restartingProxies{fakeProxyProvider: newFakeProxyProvider()}
	m := NewManager(p, newTestCatalog(t), st)
	view, err := m.SaveProxy(context.Background(), 0, map[string]string{"name": "Gate", "kind": "socks5", "host": "gate.example.net", "port": "7000"})
	if err != nil {
		t.Fatal(err)
	}
	return m, p, dir, view.ID
}

func TestRestartStuckProxiedCopyRestartsItInPlaceAndNamesIt(t *testing.T) {
	m, p, dir, id := newRestartingProxyManager(t)
	if err := m.RestartStuckCopy(context.Background(), "example", 0, id, time.Now(), "it has not reached its relay"); err != nil {
		t.Fatalf("RestartStuckCopy: %v", err)
	}
	if len(p.restarted) != 1 || p.restarted[0] != key("example", id) {
		t.Fatalf("restarts = %v, want the copy through proxy %d", p.restarted, id)
	}
	if len(p.stops) != 0 || len(p.starts) != 0 {
		t.Fatalf("a stop and a start instead of a restart in place: stops %v starts %v", p.stops, p.starts)
	}
	want := "copy through proxy " + string(rune('0'+id)) + ": it has not reached its relay"
	if got, ok := eventDetail(t, dir, "example", "auto_restarted"); !ok || got != want {
		t.Fatalf("auto_restarted event = %q (found %v), want %q", got, ok, want)
	}
}

func TestRestartStuckProxiedCopyLeavesACappedProxy(t *testing.T) {
	m, p, dir, id := newRestartingProxyManager(t)
	if err := m.patchState(id, func(s *proxies.State) { s.CappedAt = "2026-11-25T00:00:00Z" }); err != nil {
		t.Fatal(err)
	}
	if err := m.RestartStuckCopy(context.Background(), "example", 0, id, time.Now(), "stuck"); err != nil {
		t.Fatalf("RestartStuckCopy: %v", err)
	}
	if len(p.restarted) != 0 {
		t.Fatalf("restarted a copy whose proxy is at its cap: %v", p.restarted)
	}
	if _, ok := eventDetail(t, dir, "example", "auto_restarted"); ok {
		t.Fatal("recorded an automatic restart that did not happen")
	}
}

// The runtime refuses a copy that is down or whose tunnel is stopped (a
// *runtime.ProxyError): the copy is left as it is, which is not a failed restart.
func TestARefusedProxiedRestartRecordsNoRestart(t *testing.T) {
	m, p, dir, id := newRestartingProxyManager(t)
	p.err = &runtime.ProxyError{}
	if err := m.RestartStuckCopy(context.Background(), "example", 0, id, time.Now(), "stuck"); err == nil {
		t.Fatal("a refused restart reported success")
	}
	if _, ok := eventDetail(t, dir, "example", "auto_restarted"); ok {
		t.Fatal("recorded an automatic restart that did not happen")
	}
	if got, ok := eventDetail(t, dir, "example", "restart_error"); ok {
		t.Fatalf("a refusal was recorded as a failed restart: %q", got)
	}
}

// Any other failure is a failed restart, recorded under the copy's name as the
// copy-N path does.
func TestAFailedProxiedRestartIsRecordedForThatCopy(t *testing.T) {
	m, p, dir, id := newRestartingProxyManager(t)
	p.err = errors.New("Docker could not restart the container")
	if err := m.RestartStuckCopy(context.Background(), "example", 0, id, time.Now(), "stuck"); err == nil {
		t.Fatal("a failed restart reported success")
	}
	want := fmt.Sprintf("copy through proxy %d: Docker could not restart the container", id)
	if got, ok := eventDetail(t, dir, "example", "restart_error"); !ok || got != want {
		t.Fatalf("restart_error event = %q (found %v), want %q", got, ok, want)
	}
	if _, ok := eventDetail(t, dir, "example", "auto_restarted"); ok {
		t.Fatal("recorded an automatic restart that did not happen")
	}
}
