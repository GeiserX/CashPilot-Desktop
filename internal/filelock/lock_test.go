package filelock

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// Two holders of the same lock file take turns: the second waits while the first
// holds it, gives up after its wait, and gets it once the first lets go. The two
// opens stand for the window and the helper.
func TestAcquireMakesTheOtherProcessWait(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "heartbeat.lock")
	first, ok := Acquire(t.Context(), path, time.Second)
	if !ok {
		t.Fatal("the first holder did not get the lock")
	}
	began := time.Now()
	if _, ok := Acquire(t.Context(), path, 300*time.Millisecond); ok {
		t.Fatal("a second holder got the lock while the first held it")
	}
	if waited := time.Since(began); waited < 250*time.Millisecond {
		t.Fatalf("the second holder gave up after %v, before its wait", waited)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, ok := Acquire(ctx, path, time.Second); ok {
		t.Fatal("a cancelled wait got the lock while the first held it")
	}

	first()
	second, ok := Acquire(t.Context(), path, time.Second)
	if !ok {
		t.Fatal("the second holder did not get the lock once the first let go")
	}
	second()
}
