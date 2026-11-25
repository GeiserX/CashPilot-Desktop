package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A seed deleted from the stored credentials leaves no older copy of its blob in
// the database file or its log: the blob's key stays in the keychain, so a copy
// left in a free page could still be decrypted. Every form save before the delete
// left one, and the scrub must find those too.
func TestAScrubbedCredentialLeavesNoOldCopyOnDisk(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()

	var blobs []string
	for _, password := range []string{"first", "second", "third"} {
		values := map[string]string{"HOSTD_WALLET_SEED": "legal winner thank year wave sausage worth useful legal winner thank yellow", "HOSTD_API_PASSWORD": password}
		if err := s.SaveCredentials("sia-hostd", values); err != nil {
			t.Fatalf("SaveCredentials: %v", err)
		}
		var blob string
		if err := s.db.QueryRow(`SELECT value FROM credentials WHERE slug = 'sia-hostd'`).Scan(&blob); err != nil {
			t.Fatalf("read the blob: %v", err)
		}
		blobs = append(blobs, blob)
	}
	// Other rows, so the file has pages to reuse and free.
	for i := 0; i < 50; i++ {
		s.RecordEvent("other", "event", strings.Repeat("x", 500))
	}

	removed, busy, err := s.ScrubCredential("sia-hostd", "HOSTD_WALLET_SEED")
	if err != nil || !removed || busy {
		t.Fatalf("ScrubCredential = %v, %v, %v", removed, busy, err)
	}
	got, err := s.GetCredentials("sia-hostd")
	if err != nil {
		t.Fatalf("GetCredentials: %v", err)
	}
	if _, ok := got["HOSTD_WALLET_SEED"]; ok || got["HOSTD_API_PASSWORD"] != "third" {
		t.Fatalf("the scrub kept the seed or lost the other settings: %v", got)
	}

	var disk []byte
	for _, name := range []string{"cashpilot-desktop.db", "cashpilot-desktop.db-wal", "cashpilot-desktop.db-shm"} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("read %s: %v", name, err)
		}
		disk = append(disk, raw...)
	}
	for i, blob := range blobs {
		// A fragment is enough to find a leftover that a later write cut short.
		if strings.Contains(string(disk), blob[len(blob)/2:len(blob)/2+40]) {
			t.Errorf("save %d of the seed is still on disk after the scrub", i+1)
		}
	}

	if removed, _, err := s.ScrubCredential("sia-hostd", "HOSTD_WALLET_SEED"); err != nil || removed {
		t.Fatalf("a second scrub found a key to remove: %v, %v", removed, err)
	}
}
