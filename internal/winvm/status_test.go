package winvm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func sampleStatus() Status {
	n := uint64(37)
	sent := uint64(123456)
	return Status{
		Version:         StatusVersion,
		WrittenAt:       time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC),
		VM:              "CashPilot-VM",
		Network:         NetDisconnected,
		Fence:           FenceCut,
		Reason:          "Your VM is cut off from the internet because its protection changed (acl).",
		Changes:         []Change{{Field: "acl", Want: "missing: Deny Both 10.0.0.0/8", Got: "extra: none"}},
		ProvedAt:        time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC),
		Blocked:         &n,
		BlockedSince:    time.Date(2026, 11, 25, 10, 5, 0, 0, time.UTC),
		EnhancedSession: "off",
		Usage:           &Usage{CPUPercent: 4, MemoryBytes: 6 << 30, UptimeSeconds: 600, DiskBytes: 20 << 30, BytesSent: &sent},
	}
}

// What WriteStatus writes, ReadStatus reads back the same, and only through a
// rename: no temporary file is left.
func TestStatusRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := sampleStatus()
	if err := WriteStatus(dir, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadStatus(filepath.Join(dir, StatusFile))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) || got.ReadOnly() {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
	if err := WriteStatus(dir, Status{Network: NetAbsent, Fence: FenceUnproven}); err != nil {
		t.Fatal(err)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 2 || ents[0].Name() != artifactLogName || ents[1].Name() != StatusFile {
		t.Fatalf("the folder holds %v, want the file journal and %s", ents, StatusFile)
	}
}

// An unknown count stays unknown: nil is written as null and read as nil, and
// a real 0 stays 0.
func TestStatusBlockedNilIsNeverZero(t *testing.T) {
	dir := t.TempDir()
	s := sampleStatus()
	s.Blocked = nil
	if err := WriteStatus(dir, s); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, StatusFile))
	if !strings.Contains(string(b), `"blocked": null`) {
		t.Fatalf("an unknown count is not null:\n%s", b)
	}
	got, err := ParseStatus(b)
	if err != nil || got.Blocked != nil {
		t.Fatalf("read back as %v, %v", got.Blocked, err)
	}
	zero := strings.Replace(string(b), `"blocked": null`, `"blocked": 0`, 1)
	got, err = ParseStatus([]byte(zero))
	if err != nil || got.Blocked == nil || *got.Blocked != 0 {
		t.Fatalf("a real 0: %v, %v", got.Blocked, err)
	}
}

// The status is decoded strictly, and capped.
func TestParseStatusStrict(t *testing.T) {
	good, _ := json.Marshal(sampleStatus())
	if _, err := ParseStatus(good); err != nil {
		t.Fatal(err)
	}
	edit := func(old, nu string) []byte { return []byte(strings.Replace(string(good), old, nu, 1)) }
	bad := map[string][]byte{
		"an unknown field":       edit(`"vm":`, `"surprise":1,"vm":`),
		"trailing data":          append(append([]byte{}, good...), []byte(` {}`)...),
		"no version":             edit(`"version":1,`, ``),
		"version 0":              edit(`"version":1`, `"version":0`),
		"an unknown network":     edit(`"network":"disconnected"`, `"network":"maybe"`),
		"an unknown fence":       edit(`"fence":"cut"`, `"fence":"green"`),
		"a wrong type":           edit(`"blocked":37`, `"blocked":"37"`),
		"not JSON":               []byte(`<status/>`),
		"over the 256 KB cap":    edit(`"reason":"`, `"reason":"`+strings.Repeat("x", maxStatusBytes)),
		"a negative block count": edit(`"blocked":37`, `"blocked":-1`),
	}
	for name, b := range bad {
		if _, err := ParseStatus(b); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// A status from another version is read without the strict checks, and is
// read-only.
func TestParseStatusOtherVersion(t *testing.T) {
	b := []byte(`{"version":2,"vm":"CashPilot-VM","network":"paused","fence":"held","newThing":{"a":1}}`)
	s, err := ParseStatus(b)
	if err != nil {
		t.Fatal(err)
	}
	if !s.ReadOnly() || s.VM != "CashPilot-VM" || s.Network != "paused" {
		t.Fatalf("got %+v", s)
	}
}

// ReadStatus caps the file it reads and reads regular files only.
func TestReadStatusFile(t *testing.T) {
	dir := t.TempDir()
	big := filepath.Join(dir, "big.json")
	os.WriteFile(big, []byte(`{"version":1,"reason":"`+strings.Repeat("x", maxStatusBytes)+`"}`), 0o644)
	if _, err := ReadStatus(big); err == nil || !strings.Contains(err.Error(), "cap") {
		t.Fatalf("an oversized file: %v", err)
	}
	if _, err := ReadStatus(dir); err == nil {
		t.Fatal("a folder was read as a status")
	}
	if _, err := ReadStatus(filepath.Join(dir, "none.json")); !os.IsNotExist(err) {
		t.Fatalf("a missing file: %v", err)
	}
}

// The proof lock round trip, and a lock that is not one.
func TestProofLockFile(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)
	if err := WriteProofLock(dir, at); err != nil {
		t.Fatal(err)
	}
	got, err := ReadProofLock(dir)
	if err != nil || !got.Equal(at) {
		t.Fatalf("got %v, %v", got, err)
	}
	for _, b := range []string{`{"takenAt":"2026-11-25T09:00:00Z","by":"me"}`, `{"takenAt":"soon"}`, `{"takenAt":"2026-11-25T09:00:00Z"} {}`} {
		os.WriteFile(filepath.Join(dir, LockFile), []byte(b), 0o600)
		if _, err := ReadProofLock(dir); err == nil {
			t.Errorf("%s was read as a lock", b)
		}
	}
}
