//go:build !windows

package filelock

import (
	"os"

	"golang.org/x/sys/unix"
)

// tryLock takes an exclusive flock on f without waiting. flock belongs to the open
// file, so a second open of the same file conflicts even in the same process.
func tryLock(f *os.File) bool {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) == nil
}

func unlock(f *os.File) {
	_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
