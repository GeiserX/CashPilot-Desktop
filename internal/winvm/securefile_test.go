package winvm

import (
	"os"
	"path/filepath"
	"testing"
)

// OpenAppend appends to a log it made, and never writes through a link that
// someone put in the log's place: the link goes and a new file is made.
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
	write("y")
	if b, _ := os.ReadFile(victim); string(b) != "x" {
		t.Fatalf("the log wrote through the link: victim %q", b)
	}
	fi, err := os.Lstat(log)
	if err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("guard.log is not a new regular file: %v %v", fi, err)
	}
	if b, _ := os.ReadFile(log); string(b) != "y" {
		t.Fatalf("guard.log %q, want y", b)
	}
}
