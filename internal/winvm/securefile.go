package winvm

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// fileAccess is who may read a file the guard writes. Only SYSTEM and
// Administrators may ever write one.
type fileAccess int

const (
	// adminOnly: SYSTEM and Administrators read and write; nobody else.
	adminOnly fileAccess = iota
	// usersRead: SYSTEM and Administrators read and write; Users read.
	usersRead
)

// writeAtomic writes b to path through a new temporary file in the same
// folder: the access rule is set on the temporary file first, then it is
// renamed over path. A reader sees the old file or the new one, never half of
// one, and never a moment with a looser access rule.
func writeAtomic(path string, b []byte, a fileAccess) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	done := false
	defer func() {
		if !done {
			f.Close()
			os.Remove(tmp)
		}
	}()
	if err := protect(tmp, a); err != nil {
		return fmt.Errorf("set the access rule on %s: %w", filepath.Base(path), err)
	}
	if _, err := f.Write(b); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// On Windows a reader that holds the file open can make the rename fail for
	// a moment, so it is tried a few times.
	for try := 0; ; try++ {
		err = os.Rename(tmp, path)
		if err == nil || try == 4 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		return err
	}
	done = true
	return nil
}

// errNotRegular is a path that is a folder, a link or a device.
var errNotRegular = errors.New("not a regular file")

// readCapped reads a regular file of at most max bytes. With trusted set, the
// file must also be owned by SYSTEM, by Administrators, or by the account this
// process runs as (SYSTEM for the guard): a file any user could have written is
// refused. The owner is read from the open handle, so it is the owner of what
// is read.
func readCapped(path string, max int64, trusted bool) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), errNotRegular)
	}
	if trusted {
		if err := ownedByAdmin(f); err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
	}
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%s is over the %d byte cap", filepath.Base(path), max)
	}
	return b, nil
}
