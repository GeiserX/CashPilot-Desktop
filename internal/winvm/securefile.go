package winvm

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
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
	// usersWrite: SYSTEM and Administrators full; Users read and write the
	// contents but cannot change the access rule or delete the file. Only
	// prefs.json has it: the one input the user may write, which can only
	// keep the VM off.
	usersWrite
)

// writeAtomic writes b to path through a new temporary file in the same
// folder: the access rule is set on the temporary file first, then it is
// renamed over path. A reader sees the old file or the new one, never half of
// one, and never a moment with a looser access rule.
func writeAtomic(path string, b []byte, a fileAccess) error {
	artifactMu.Lock()
	defer artifactMu.Unlock()
	dir, target := filepath.Dir(path), filepath.Base(path)
	j, err := openArtifactLog(dir, true)
	if err != nil {
		return err
	}
	defer j.file.Close()
	if err := j.verify(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	rel := randomArtifactName("."+target+".", ".tmp")
	if err := j.record(rel, ""); err != nil {
		return err
	}
	tmp := filepath.Join(dir, rel)
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	id, err := j.note(rel)
	defer func() {
		f.Close()
		if id != "" {
			_ = removeRecorded(dir, j.root, rel, []string{id})
		}
	}()
	if err != nil {
		return err
	}
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
	// Record the ID under its destination before the rename. Either
	// path can be verified if setup stops during the move.
	if err := j.record(target, id); err != nil {
		return err
	}
	if err := j.verify(target); err != nil && !errors.Is(err, fs.ErrNotExist) {
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

// OpenAppend creates a recorded log or appends to a previously recorded one.
func OpenAppend(path string) (*os.File, error) { return openAppend(path, true) }

// AppendLog is OpenAppend in a folder that already has its file journal. It
// never starts a journal, so it cannot claim a folder Setup did not make.
func AppendLog(path string) (*os.File, error) { return openAppend(path, false) }

func openAppend(path string, newJournal bool) (*os.File, error) {
	artifactMu.Lock()
	defer artifactMu.Unlock()
	dir, rel := filepath.Dir(path), filepath.Base(path)
	j, err := openArtifactLog(dir, newJournal)
	if err != nil {
		return nil, err
	}
	defer j.file.Close()
	err = j.verify(rel)
	if err == nil {
		return os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err := j.record(rel, ""); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	id, err := j.note(rel)
	if err == nil {
		err = protectFile(path, usersRead)
	}
	if err != nil {
		f.Close()
		if id != "" {
			_ = removeRecorded(dir, j.root, rel, []string{id})
		}
		return nil, err
	}
	return f, nil
}

// protectFile is protect, replaced by a test that needs it to fail.
var protectFile = protect
