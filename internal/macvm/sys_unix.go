//go:build unix

package macvm

import (
	"os"
	"syscall"
)

const oNoFollow = syscall.O_NOFOLLOW

func freeBytes(path string) (int64, error) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(path, &s); err != nil {
		return 0, err
	}
	return int64(s.Bavail) * int64(s.Bsize), nil
}

// statOwner is the uid that owns fi.
func statOwner(fi os.FileInfo) (int, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}

// ownGroup starts a child in its own process group.
func ownGroup() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setpgid: true} }

// rootLock holds an exclusive lock on dir until unlock is called. Root
// Install and Remove both take it, so a second app process or a direct CLI
// call waits instead of tearing down what the other is setting up. The lock
// is on the folder itself, so it leaves no file behind.
func rootLock(dir string) (unlock func(), err error) {
	f, err := os.Open(dir)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}
