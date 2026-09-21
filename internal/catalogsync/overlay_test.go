package catalogsync

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// liveEntry is a minimal but realistic upstream entry: live status, a floating image,
// and enough shape around docker.image that a rewrite has to find the right line.
const liveEntry = `name: Example
slug: example
category: bandwidth
status: active

referral:
  signup_url: "https://example.com/?ref=abc"
  code: "abc"

docker:
  # An image: mentioned in a comment, which a text search would hit first.
  image: example/thing
  platforms: [linux/amd64]
  env:
    - key: TOKEN
      label: "Token"
      description: "Paste the image: token from the dashboard"
`

func overlayDir(t *testing.T, pins string, appends map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if pins != "" {
		if err := os.WriteFile(filepath.Join(dir, "image-pins.yml"), []byte(pins), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for rel, body := range appends {
		full := filepath.Join(dir, "append", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func applyWith(t *testing.T, upstream map[string][]byte, pins string, appends map[string]string) (map[string][]byte, error) {
	t.Helper()
	ov, err := LoadOverlay(overlayDir(t, pins, appends))
	if err != nil {
		t.Fatalf("LoadOverlay: %v", err)
	}
	return Apply(upstream, ov)
}

const examplePin = `pins:
  example:
    image: "example/thing@sha256:aaaa"
    why: "test"
`

// TestApplyPinsOnlyTheImageLine is the core promise of the sync: the vendored file is
// upstream's bytes with one line changed. Everything else — comments, blank lines,
// the word "image:" inside a comment and inside a description — must survive
// untouched, because a sync that reflows the file hides the one change that matters
// inside hundreds that do not.
func TestApplyPinsOnlyTheImageLine(t *testing.T) {
	upstream := map[string][]byte{"bandwidth/example.yml": []byte(liveEntry)}

	out, err := applyWith(t, upstream, examplePin, nil)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got := string(out["bandwidth/example.yml"])

	wantLines := strings.Split(liveEntry, "\n")
	gotLines := strings.Split(got, "\n")
	if len(gotLines) != len(wantLines) {
		t.Fatalf("line count changed: %d -> %d\n%s", len(wantLines), len(gotLines), got)
	}
	changed := 0
	for i := range wantLines {
		if wantLines[i] != gotLines[i] {
			changed++
			if gotLines[i] != `  image: "example/thing@sha256:aaaa"` {
				t.Errorf("line %d changed to %q, which is not the pin", i+1, gotLines[i])
			}
		}
	}
	if changed != 1 {
		t.Errorf("%d lines changed, want exactly 1\n%s", changed, got)
	}
}

// TestApplyRejectsPinForAMovedImage is the guard that earns the overlay its keep. The
// ProxyBase migration moved an image to a different repository; holding the old
// digest under the new entry would leave Desktop deploying a retired image that looks
// healthy and earns nothing.
func TestApplyRejectsPinForAMovedImage(t *testing.T) {
	upstream := map[string][]byte{
		"bandwidth/example.yml": []byte(strings.Replace(liveEntry, "image: example/thing", "image: ghcr.io/elsewhere/thing", 1)),
	}

	_, err := applyWith(t, upstream, examplePin, nil)
	if err == nil {
		t.Fatal("Apply accepted a pin for a different repository; the sync must refuse")
	}
	if !strings.Contains(err.Error(), "ghcr.io/elsewhere/thing") {
		t.Errorf("error should name the image upstream now uses, got: %v", err)
	}
}

// TestApplyRejectsPinForARetaggedImage: a tag change is how upstream ships a new
// generation of a client. Keeping the old digest under the new tag is the same
// failure as keeping it under an old repository, and is easier to miss.
func TestApplyRejectsPinForARetaggedImage(t *testing.T) {
	upstream := map[string][]byte{
		"bandwidth/example.yml": []byte(strings.Replace(liveEntry, "image: example/thing", "image: example/thing:g4-latest", 1)),
	}

	if _, err := applyWith(t, upstream, examplePin, nil); err == nil {
		t.Fatal("Apply accepted a pin whose tag differs from upstream; the sync must refuse")
	}
}

// TestApplyRejectsAnUnpinnedLiveImage keeps the digest-pin rule from being dropped by
// omission: a new live entry arriving from upstream with a floating image stops the
// sync rather than landing unpinned and failing a different test later.
func TestApplyRejectsAnUnpinnedLiveImage(t *testing.T) {
	upstream := map[string][]byte{"bandwidth/example.yml": []byte(liveEntry)}

	_, err := applyWith(t, upstream, "", nil)
	if err == nil {
		t.Fatal("Apply accepted a live entry with a floating image and no pin")
	}
	if !strings.Contains(err.Error(), "example") {
		t.Errorf("error should name the unpinned service, got: %v", err)
	}
}

// TestApplyExemptsRetiredEntriesFromPinning: a dead entry's image may no longer
// resolve and it is never deployed, so demanding a digest for it would block the sync
// on a question nobody can answer.
func TestApplyExemptsRetiredEntriesFromPinning(t *testing.T) {
	for _, status := range []string{"dead", "broken", "dropped"} {
		body := strings.Replace(liveEntry, "status: active", "status: "+status, 1)
		out, err := applyWith(t, map[string][]byte{"bandwidth/example.yml": []byte(body)}, "", nil)
		if err != nil {
			t.Fatalf("status %s: Apply returned %v, want the entry to pass through unpinned", status, err)
		}
		if string(out["bandwidth/example.yml"]) != body {
			t.Errorf("status %s: a retired entry must be copied verbatim", status)
		}
	}
}

// TestApplyRejectsAStalePin: a pin left behind after upstream dropped or retired the
// service pins nothing, and left in place it reads as a live guarantee.
func TestApplyRejectsAStalePin(t *testing.T) {
	t.Run("service gone", func(t *testing.T) {
		upstream := map[string][]byte{"bandwidth/other.yml": []byte(strings.NewReplacer(
			"slug: example", "slug: other",
			"image: example/thing", "image: other/thing@sha256:bbbb",
		).Replace(liveEntry))}
		if _, err := applyWith(t, upstream, examplePin, nil); err == nil {
			t.Fatal("Apply accepted a pin for a slug upstream no longer ships")
		}
	})

	t.Run("upstream pins it itself", func(t *testing.T) {
		upstream := map[string][]byte{"bandwidth/example.yml": []byte(strings.Replace(
			liveEntry, "image: example/thing", "image: example/thing@sha256:cccc", 1))}
		if _, err := applyWith(t, upstream, examplePin, nil); err == nil {
			t.Fatal("Apply accepted a pin for an entry upstream already pins")
		}
	})
}

// TestApplyAppendsDesktopOnlyBlocks: the second overlay operation. The upstream bytes
// stay first and unchanged, the Desktop block follows.
func TestApplyAppendsDesktopOnlyBlocks(t *testing.T) {
	upstream := map[string][]byte{"bandwidth/example.yml": []byte(liveEntry)}
	extra := "native:\n  command: \"run\"\n"

	out, err := applyWith(t, upstream, examplePin, map[string]string{"bandwidth/example.yml": extra})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	got := string(out["bandwidth/example.yml"])
	if !strings.HasSuffix(got, "\n\n"+extra) {
		t.Errorf("appended block missing or misplaced:\n%s", got)
	}
	if !strings.Contains(got, `image: "example/thing@sha256:aaaa"`) {
		t.Error("the pin must still be applied to a file that also carries an append")
	}
}

// TestApplyWritesOverlayOnlyEntries: an overlay file with no upstream counterpart
// becomes the entry, which is how a Desktop-only service would be carried.
func TestApplyAcceptsOverlayOnlyEntries(t *testing.T) {
	upstream := map[string][]byte{"bandwidth/example.yml": []byte(liveEntry)}
	only := "name: Desktop Only\nslug: desktop-only\nstatus: active\n"

	out, err := applyWith(t, upstream, examplePin, map[string]string{"bandwidth/desktop-only.yml": only})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := string(out["bandwidth/desktop-only.yml"]); got != only {
		t.Errorf("overlay-only entry = %q, want it written verbatim", got)
	}
}

// TestApplyIsIdempotent: running the sync twice on unchanged inputs must produce the
// same bytes, or every sync would show a diff and the drift check would never be
// quiet.
func TestApplyIsIdempotent(t *testing.T) {
	upstream := map[string][]byte{"bandwidth/example.yml": []byte(liveEntry)}
	appends := map[string]string{"bandwidth/example.yml": "native:\n  command: \"run\"\n"}

	first, err := applyWith(t, upstream, examplePin, appends)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	second, err := applyWith(t, upstream, examplePin, appends)
	if err != nil {
		t.Fatalf("Apply (second run): %v", err)
	}
	if len(Diff(first, second)) != 0 {
		t.Error("two runs over identical inputs produced different output")
	}
}

// TestDiffReportsEveryKindOfDrift covers what the weekly check reports: an entry
// upstream added, one it removed, and one whose content moved.
func TestDiffReportsEveryKindOfDrift(t *testing.T) {
	want := map[string][]byte{
		"a.yml": []byte("one"),
		"b.yml": []byte("two"),
	}
	have := map[string][]byte{
		"a.yml": []byte("one"),
		"b.yml": []byte("CHANGED"),
		"c.yml": []byte("three"),
	}

	got := Diff(want, have)
	if len(got) != 2 {
		t.Fatalf("Diff reported %d drifts, want 2: %+v", len(got), got)
	}
	if got[0].Path != "b.yml" || got[0].Kind != DriftChanged {
		t.Errorf("first drift = %+v, want b.yml changed", got[0])
	}
	if got[1].Path != "c.yml" || got[1].Kind != DriftExtra {
		t.Errorf("second drift = %+v, want c.yml extra", got[1])
	}

	if got := Diff(want, map[string][]byte{"a.yml": []byte("one")}); len(got) != 1 || got[0].Kind != DriftMissing {
		t.Errorf("a file upstream has and the vendored tree lacks = %+v, want one missing", got)
	}
	if len(Diff(want, want)) != 0 {
		t.Error("identical trees must report no drift")
	}
}

// TestLoadOverlayOnAMissingDirectory: no overlay is a legitimate state, not an error.
func TestLoadOverlayOnAMissingDirectory(t *testing.T) {
	ov, err := LoadOverlay(filepath.Join(t.TempDir(), "absent"))
	if err != nil {
		t.Fatalf("LoadOverlay on a missing directory returned %v, want an empty overlay", err)
	}
	if len(ov.ImagePins) != 0 || len(ov.Appends) != 0 {
		t.Errorf("empty overlay = %+v", ov)
	}
}
