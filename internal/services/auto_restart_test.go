package services

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func eventDetail(t *testing.T, dir, slug, event string) (string, bool) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, "cashpilot-desktop.db"))
	if err != nil {
		t.Fatalf("open store database: %v", err)
	}
	defer db.Close()
	var detail string
	err = db.QueryRow(`SELECT detail FROM runtime_events WHERE slug = ? AND event = ? ORDER BY id DESC LIMIT 1`, slug, event).Scan(&detail)
	if err == sql.ErrNoRows {
		return "", false
	}
	if err != nil {
		t.Fatalf("read %s events: %v", event, err)
	}
	return detail, true
}

func TestRestartStuckRestartsAndSaysWhy(t *testing.T) {
	dir, st := newTestStoreInDir(t)
	fake := &fakeProvider{}
	m := NewManager(fake, newTestCatalog(t), st)
	if err := m.RestartStuck(context.Background(), "example", time.Now(), "it has not reached its relay"); err != nil {
		t.Fatalf("RestartStuck: %v", err)
	}
	if len(fake.restartTimeouts) != 1 {
		t.Fatalf("provider restarts = %d, want 1", len(fake.restartTimeouts))
	}
	if got, ok := eventDetail(t, dir, "example", "auto_restarted"); !ok || got != "it has not reached its relay" {
		t.Fatalf("auto_restarted event = %q (found %v), want the reason", got, ok)
	}
}

// THE RULE: an automatic restart never undoes the owner's Stop. The check reads an
// hour of logs before it asks; a Stop pressed meanwhile must win.
func TestRestartStuckLeavesAServiceTheOwnerStoppedDuringTheCheck(t *testing.T) {
	dir, st := newTestStoreInDir(t)
	fake := &fakeProvider{}
	m := NewManager(fake, newTestCatalog(t), st)
	listedAt := time.Now()
	if err := m.Stop(context.Background(), "example"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := m.RestartStuck(context.Background(), "example", listedAt, "stuck"); err != nil {
		t.Fatalf("RestartStuck: %v", err)
	}
	if len(fake.restartTimeouts) != 0 {
		t.Fatalf("restarted a service the owner stopped after the check began")
	}
	if _, ok := eventDetail(t, dir, "example", "auto_restarted"); ok {
		t.Fatal("recorded an automatic restart that did not happen")
	}
}

func TestRestartStuckIgnoresAStopFromBeforeTheCheck(t *testing.T) {
	_, st := newTestStoreInDir(t)
	fake := &fakeProvider{}
	m := NewManager(fake, newTestCatalog(t), st)
	if err := m.Stop(context.Background(), "example"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	time.Sleep(2 * time.Millisecond)
	if err := m.RestartStuck(context.Background(), "example", time.Now(), "stuck"); err != nil {
		t.Fatalf("RestartStuck: %v", err)
	}
	if len(fake.restartTimeouts) != 1 {
		t.Fatalf("a Stop from before the check blocked the restart")
	}
}

// orderedProvider records the order of Stop and Restart, and holds Restart until
// released, so a test can press Stop while a restart is in flight.
type orderedProvider struct {
	fakeProvider
	mu      sync.Mutex
	order   []string
	entered chan struct{}
	release chan struct{}
}

func (p *orderedProvider) note(s string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.order = append(p.order, s)
}

func (p *orderedProvider) Restart(context.Context, string, int) error {
	close(p.entered)
	<-p.release
	p.note("restart")
	return nil
}

func (p *orderedProvider) Stop(context.Context, string, int) error {
	p.note("stop")
	return nil
}

func TestAStopDuringAnAutomaticRestartRunsAfterIt(t *testing.T) {
	_, st := newTestStoreInDir(t)
	p := &orderedProvider{entered: make(chan struct{}), release: make(chan struct{})}
	m := NewManager(p, newTestCatalog(t), st)
	done := make(chan error, 2)
	go func() { done <- m.RestartStuck(context.Background(), "example", time.Now(), "stuck") }()
	<-p.entered
	go func() { done <- m.Stop(context.Background(), "example") }()
	time.Sleep(20 * time.Millisecond)
	close(p.release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.order) != 2 || p.order[0] != "restart" || p.order[1] != "stop" {
		t.Fatalf("order = %v, want the restart then the owner's stop", p.order)
	}
}
