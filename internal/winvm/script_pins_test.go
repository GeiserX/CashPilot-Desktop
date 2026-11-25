package winvm

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// scriptPinsFile holds the SHA-256 of every PowerShell script the seam runs
// on the host and every script it runs in the guest, one per line.
var scriptPinsFile = filepath.Join("testdata", "script-pins.txt")

// TestScriptPins pins every host and guest script, so any edit to what runs
// as admin or in the guest needs a deliberate update of the pin file in the
// same commit. After a deliberate edit, refresh it with
// CPD_UPDATE_PINS=1 go test ./internal/winvm/ -run TestScriptPins.
func TestScriptPins(t *testing.T) {
	got := map[string]string{}
	for name, s := range hvScripts {
		got["host "+name] = s
	}
	for _, g := range guestScripts {
		got["guest "+g.Name] = g.Text
	}
	var lines []string
	for name, s := range got {
		sum := sha256.Sum256([]byte(s))
		lines = append(lines, hex.EncodeToString(sum[:])+"  "+name)
	}
	slices.SortFunc(lines, func(a, b string) int { return strings.Compare(a[66:], b[66:]) })
	want := strings.Join(lines, "\n") + "\n"
	if os.Getenv("CPD_UPDATE_PINS") == "1" {
		if err := os.WriteFile(scriptPinsFile, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile(scriptPinsFile)
	if err != nil {
		t.Fatal(err)
	}
	pinned := map[string]string{}
	for _, l := range strings.Split(strings.TrimSpace(strings.ReplaceAll(string(raw), "\r\n", "\n")), "\n") {
		sum, name, ok := strings.Cut(l, "  ")
		if !ok {
			t.Fatalf("%s: bad line %q", scriptPinsFile, l)
		}
		pinned[name] = sum
	}
	for _, l := range lines {
		sum, name, _ := strings.Cut(l, "  ")
		switch p, ok := pinned[name]; {
		case !ok:
			t.Errorf("%s is new and has no pin", name)
		case p != sum:
			t.Errorf("%s changed: sha256 %s, pinned %s", name, sum, p)
		}
		delete(pinned, name)
	}
	for name := range pinned {
		t.Errorf("%s is pinned but no longer exists", name)
	}
	if t.Failed() {
		t.Log("after a deliberate edit: CPD_UPDATE_PINS=1 go test ./internal/winvm/ -run TestScriptPins")
	}
}
