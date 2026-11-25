package filelock

import (
	"context"
	"os"
	"path/filepath"
	"time"
)

const pollInterval = 100 * time.Millisecond

// Acquire locks path for this process until release is called. It returns false
// if the file cannot be opened or another holder keeps it past wait. The file
// must stay in place so every process locks the same file.
func Acquire(ctx context.Context, path string, wait time.Duration) (release func(), ok bool) {
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
		case <-time.After(pollInterval):
		}
	}
}
