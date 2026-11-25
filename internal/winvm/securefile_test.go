package winvm

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// OpenAppend appends to a log it made, and never writes through a link that
// someone put in the log's place.
func TestOpenAppend(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "guard.log")
	write := func(s string) {
		t.Helper()
		f, err := OpenAppend(log)
		if err != nil {
			t.Fatal(err)
		}
		f.WriteString(s)
		f.Close()
	}
	write("a")
	write("b")
	if b, _ := os.ReadFile(log); string(b) != "ab" {
		t.Fatalf("guard.log %q, want ab", b)
	}
	victim := filepath.Join(dir, "victim")
	os.WriteFile(victim, []byte("x"), 0o600)
	os.Remove(log)
	if err := os.Symlink(victim, log); err != nil {
		t.Skipf("no symlinks here: %v", err)
	}
	if f, err := OpenAppend(log); err == nil {
		f.Close()
		t.Fatal("OpenAppend accepted a replacement link")
	}
	if b, _ := os.ReadFile(victim); string(b) != "x" {
		t.Fatalf("the log wrote through the link: victim %q", b)
	}
	if fi, err := os.Lstat(log); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the independent link changed: %v %v", fi, err)
	}
}

// A log file whose access rule could not be set is removed, so the next call
// does not accept a file that kept the rule it inherited.
func TestOpenAppendRemovesAFileItCouldNotProtect(t *testing.T) {
	log := filepath.Join(t.TempDir(), "guard.log")
	old := protectFile
	t.Cleanup(func() { protectFile = old })
	protectFile = func(string, fileAccess) error { return errors.New("no DACL") }
	if f, err := OpenAppend(log); err == nil {
		f.Close()
		t.Fatal("OpenAppend accepted a file it could not protect")
	}
	if _, err := os.Lstat(log); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the unprotected file is still there: %v", err)
	}
}
