package upstream

import (
	"context"
	"os"
	"path/filepath"
	"time"
)

// The window and the background helper (--daemon) each run the pairing loop, under
// the same client_id. The server keeps one counter baseline per container and reads
// a counter that went down as a restart, so it counts the whole counter again: if
// one process read its counters first but the other's heartbeat reached the server
// first, the server would add hours or days of traffic in one go. SendLock makes the
// two processes take turns from the read to the server's answer, so the server gets
// the readings in the order they were taken.

// sendLockPoll is how often SendLock tries again while the other process holds it.
const sendLockPoll = 100 * time.Millisecond

// SendLock takes the lock file at path, waiting up to wait while another process
// holds it. ok is false when the lock could not be had in time or the file cannot
// be opened. Then the caller must send no counters: a reading sent out of turn is
// what the lock is for. release is never nil and releases the lock.
func SendLock(ctx context.Context, path string, wait time.Duration) (release func(), ok bool) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return func() {}, false
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return func() {}, false
	}
	deadline := time.Now().Add(wait)
	for {
		if tryLock(f) {
			return func() {
				unlock(f)
				_ = f.Close()
			}, true
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return func() {}, false
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return func() {}, false
		case <-time.After(sendLockPoll):
		}
	}
}
