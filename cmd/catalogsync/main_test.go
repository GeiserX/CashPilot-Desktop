package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A minimal but complete web catalog: one live, digest-pinned entry needs no overlay
// pin, so Apply has nothing to complain about and the test is about drift alone.
const upstreamEntry = `name: Example
slug: example
category: bandwidth
status: active
docker:
  image: "example/image@sha256:0000000000000000000000000000000000000000000000000000000000000000"
`

func writeCatalog(t *testing.T, root string, files map[string]string) string {
	t.Helper()
	for rel, body := range files {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return root
}

// webCheckout lays out a local CashPilot checkout (-src) with one entry.
func webCheckout(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	writeCatalog(t, root, map[string]string{"services/bandwidth/example.yml": body})
	return root
}

// THE RULE: only real drift is drift.
//
// -check answers three different questions with three different exit codes, because
// the weekly workflow acts on them differently. A comparison that never happened —
// codeload down, a ref that does not exist, an overlay that will not parse — used to
// exit 1 exactly like drift, and the workflow then printed "services/ no longer
// matches the CashPilot web catalog" over a network error, naming files nobody had
// touched.
func TestCheckSeparatesDriftFromFailureToCompare(t *testing.T) {
	src := webCheckout(t, upstreamEntry)
	overlay := t.TempDir()

	t.Run("agreeing catalogs exit 0", func(t *testing.T) {
		out := t.TempDir()
		writeCatalog(t, out, map[string]string{"bandwidth/example.yml": upstreamEntry})
		err := run(src, "main", out, overlay, true)
		if code := exitCodeFor(err); code != 0 {
			t.Fatalf("exit %d (%v); want 0", code, err)
		}
	})

	t.Run("real drift exits 1", func(t *testing.T) {
		out := t.TempDir()
		writeCatalog(t, out, map[string]string{"bandwidth/example.yml": upstreamEntry + "notes: edited by hand\n"})
		err := run(src, "main", out, overlay, true)
		if code := exitCodeFor(err); code != exitDrift {
			t.Fatalf("exit %d (%v); want %d", code, err, exitDrift)
		}
		if !strings.Contains(err.Error(), "bandwidth/example.yml") {
			t.Fatalf("drift error does not name the file: %v", err)
		}
	})

	t.Run("a fetch that fails exits 2, not drift", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "nope", http.StatusNotFound)
		}))
		defer server.Close()
		restore := pointFetchAt(t, server.URL)
		defer restore()

		out := t.TempDir()
		writeCatalog(t, out, map[string]string{"bandwidth/example.yml": upstreamEntry})
		err := run("", "no-such-ref", out, overlay, true)
		if code := exitCodeFor(err); code != exitFailure {
			t.Fatalf("exit %d (%v); want %d", code, err, exitFailure)
		}
		if strings.Contains(err.Error(), "drifted") {
			t.Fatalf("a fetch failure was reported as drift: %v", err)
		}
	})

	t.Run("an unreadable overlay exits 2, not drift", func(t *testing.T) {
		broken := t.TempDir()
		writeCatalog(t, broken, map[string]string{"image-pins.yml": "pins: [this is not a map\n"})
		out := t.TempDir()
		writeCatalog(t, out, map[string]string{"bandwidth/example.yml": upstreamEntry})
		err := run(src, "main", out, broken, true)
		if code := exitCodeFor(err); code != exitFailure {
			t.Fatalf("exit %d (%v); want %d", code, err, exitFailure)
		}
	})
}

// pointFetchAt redirects the codeload fetch at a test server and removes the retry
// pause. Returns the restore function; the caller defers it.
func pointFetchAt(t *testing.T, base string) func() {
	t.Helper()
	oldBase, oldSleep := codeloadBase, fetchSleep
	codeloadBase = base
	fetchSleep = func(time.Duration) {}
	return func() { codeloadBase, fetchSleep = oldBase, oldSleep }
}

// tarball builds a gzipped tar shaped like codeload's, with the usual
// CashPilot-<ref>/ top-level directory.
func tarball(t *testing.T, top string, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for rel, body := range files {
		hdr := &tar.Header{Name: top + "/" + rel, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar header: %v", err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatalf("tar body: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// A transient failure is worth another go: the drift job runs once a week, and one
// dropped connection used to cost the whole week's answer — reported as drift.
func TestFetchRetriesATransientFailure(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			http.Error(w, "upstream hiccup", http.StatusBadGateway)
			return
		}
		_, _ = w.Write(tarball(t, "CashPilot-main", map[string]string{"services/bandwidth/example.yml": upstreamEntry}))
	}))
	defer server.Close()
	defer pointFetchAt(t, server.URL)()

	files, err := fetchUpstream("main")
	if err != nil {
		t.Fatalf("fetch gave up on a transient failure: %v", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("made %d attempts; want 3", calls.Load())
	}
	if _, ok := files["bandwidth/example.yml"]; !ok {
		t.Fatalf("catalog not extracted: %v", files)
	}
}

// A ref that does not exist will not start existing: retrying a 404 only makes the
// job take three times as long to give the same answer.
func TestFetchDoesNotRetryAMissingRef(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer server.Close()
	defer pointFetchAt(t, server.URL)()

	if _, err := fetchUpstream("no-such-ref"); err == nil {
		t.Fatal("a 404 was reported as success")
	}
	if calls.Load() != 1 {
		t.Fatalf("made %d attempts at a 404; want 1", calls.Load())
	}
}

// THE RULE: -ref takes any ref, because the workflow input says so.
//
// codeload resolves a branch, a tag or a commit SHA under /tar.gz/<ref>, while the
// refs/heads/ form resolves branches ONLY. With the prefix, `-ref v1.37.0` answered
// 404 — which, before the exit codes above, the workflow announced as drift.
func TestFetchResolvesTagsAndCommitSHAs(t *testing.T) {
	for _, ref := range []string{"main", "v1.37.0", "51a1f6c0d0f3f2a1b9c8d7e6f5a4b3c2d1e0f9a8"} {
		t.Run(ref, func(t *testing.T) {
			var requested atomic.Value
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requested.Store(r.URL.Path)
				_, _ = w.Write(tarball(t, "CashPilot-"+ref, map[string]string{"services/bandwidth/example.yml": upstreamEntry}))
			}))
			defer server.Close()
			defer pointFetchAt(t, server.URL)()

			files, err := fetchUpstream(ref)
			if err != nil {
				t.Fatalf("fetch %s: %v", ref, err)
			}
			if got, want := requested.Load(), "/tar.gz/"+ref; got != want {
				t.Fatalf("requested %v; want %v", got, want)
			}
			if _, ok := files["bandwidth/example.yml"]; !ok {
				t.Fatalf("catalog not extracted for %s: %v", ref, files)
			}
		})
	}
}

// THE RULE: a Windows checkout is not drift.
//
// Git for Windows checks files out with CRLF by default (core.autocrlf=true), so a
// byte-for-byte comparison there reported every single file as changed on a clone
// nobody had touched. .gitattributes pins these paths to LF for a fresh clone; this
// is the other half, for a working tree that is already CRLF.
func TestACRLFWorkingTreeIsNotDrift(t *testing.T) {
	src := webCheckout(t, upstreamEntry)
	out := t.TempDir()
	writeCatalog(t, out, map[string]string{
		"bandwidth/example.yml": strings.ReplaceAll(upstreamEntry, "\n", "\r\n"),
	})
	if err := run(src, "main", out, t.TempDir(), true); err != nil {
		t.Fatalf("a CRLF checkout was reported as drift: %v", err)
	}
	// And the real thing is still caught through the CRLF.
	writeCatalog(t, out, map[string]string{
		"bandwidth/example.yml": strings.ReplaceAll(upstreamEntry+"notes: edited\n", "\n", "\r\n"),
	})
	err := run(src, "main", out, t.TempDir(), true)
	if code := exitCodeFor(err); code != exitDrift {
		t.Fatalf("real drift through a CRLF checkout exited %d (%v); want %d", code, err, exitDrift)
	}
}

// Writing the catalog out is the other half of -check: it must produce exactly what
// -check then accepts, including on a tree that was CRLF before.
func TestSyncWritesWhatCheckAccepts(t *testing.T) {
	src := webCheckout(t, upstreamEntry)
	out := t.TempDir()
	writeCatalog(t, out, map[string]string{
		"bandwidth/example.yml": strings.ReplaceAll(upstreamEntry, "\n", "\r\n"),
		"bandwidth/gone.yml":    "name: Gone\nslug: gone\n",
	})
	overlay := t.TempDir()

	if err := run(src, "main", out, overlay, false); err != nil {
		t.Fatalf("sync: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "bandwidth", "gone.yml")); !os.IsNotExist(err) {
		t.Fatalf("an entry the web catalog no longer ships survived the sync (err=%v)", err)
	}
	written, err := os.ReadFile(filepath.Join(out, "bandwidth", "example.yml"))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if bytes.Contains(written, []byte("\r\n")) {
		t.Fatal("the sync wrote CRLF line endings")
	}
	if err := run(src, "main", out, overlay, true); err != nil {
		t.Fatalf("-check rejected what the sync just wrote: %v", err)
	}
}
