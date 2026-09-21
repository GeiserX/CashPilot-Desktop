// Command catalogsync vendors the CashPilot web catalog (services/**.yml) into this
// repository and, in -check mode, reports when the vendored copy has drifted from it.
//
// The web repository is the single source of truth for the catalog. Desktop used to
// keep its own hand-edited copy, which went stale silently: entries kept a status the
// web catalog had already retired, capabilities the container needs to earn were
// missing, and new entries never arrived at all.
//
// Everything Desktop keeps that the web catalog does not is declared in
// catalog-overlay/ and applied on top of the copied file. See catalog-overlay/README.md
// for the mechanism and for every delta we currently declare.
//
// Usage:
//
//	go run ./cmd/catalogsync                 # fetch GeiserX/CashPilot@main, rewrite services/
//	go run ./cmd/catalogsync -check          # same, but only report drift
//	go run ./cmd/catalogsync -src ../CashPilot   # use a local checkout instead of fetching
//	go run ./cmd/catalogsync -ref v1.37.0    # any branch, tag or commit SHA
//
// -check exits 0 when the two agree, 1 when they have really drifted, and 2 when the
// comparison could not be made at all (no network, a ref that does not exist, an
// overlay that does not parse). The weekly workflow relies on that difference: a
// codeload outage must not be announced as a catalog that changed.
package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalogsync"
)

// Exit codes. They are the only way the drift workflow can tell "the catalogs have
// parted" from "we could not fetch the catalog to compare": both used to exit 1, so a
// codeload outage on a Monday morning was reported as drift, in an error message
// naming files nobody had touched.
const (
	exitDrift   = 1 // -check found real drift between services/ and the web catalog
	exitFailure = 2 // fetch, overlay or IO failure: the comparison never happened
)

func main() {
	var (
		src     = flag.String("src", "", "local CashPilot checkout to copy from; empty means fetch the tarball")
		ref     = flag.String("ref", "main", "branch, tag or commit SHA of GeiserX/CashPilot to fetch when -src is empty")
		out     = flag.String("out", "services", "directory to write the vendored catalog into")
		overlay = flag.String("overlay", "catalog-overlay", "directory holding the Desktop-only deltas")
		check   = flag.Bool("check", false, "report drift instead of writing; exit 1 on drift, 2 when the comparison could not run")
	)
	flag.Parse()

	if err := run(*src, *ref, *out, *overlay, *check); err != nil {
		fmt.Fprintf(os.Stderr, "catalogsync: %v\n", err)
		os.Exit(exitCodeFor(err))
	}
}

// driftError is the one failure that means the two catalogs really have parted.
// Everything else - no network, a 404 ref, an unreadable overlay - is a failure to
// compare, and the workflow must not announce it as drift.
type driftError struct{ detail string }

func (e *driftError) Error() string { return e.detail }

// exitCodeFor maps an error from run() to this process's exit code.
func exitCodeFor(err error) int {
	if err == nil {
		return 0
	}
	var drift *driftError
	if errors.As(err, &drift) {
		return exitDrift
	}
	return exitFailure
}

func run(src, ref, out, overlayDir string, check bool) error {
	var (
		upstream map[string][]byte
		origin   string
		err      error
	)
	if src != "" {
		origin = filepath.Join(src, "services")
		upstream, err = readDir(origin)
	} else {
		origin = fmt.Sprintf("GeiserX/CashPilot@%s", ref)
		upstream, err = fetchUpstream(ref)
	}
	if err != nil {
		return err
	}
	if len(upstream) == 0 {
		return fmt.Errorf("no catalog files found in %s", origin)
	}

	overlay, err := catalogsync.LoadOverlay(overlayDir)
	if err != nil {
		return err
	}

	want, err := catalogsync.Apply(upstream, overlay)
	if err != nil {
		return err
	}

	have, err := readDir(out)
	if err != nil {
		return err
	}

	if !check {
		if err := writeDir(out, want, have); err != nil {
			return err
		}
		fmt.Printf("catalogsync: %s -> %s (%d files, %d overlay pins, %d overlay appends)\n",
			origin, out, len(want), len(overlay.ImagePins), len(overlay.Appends))
		return nil
	}

	drift := catalogsync.Diff(want, have)
	if len(drift) == 0 {
		fmt.Printf("catalogsync: %s matches %s (%d files)\n", out, origin, len(want))
		return nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "the vendored catalog in %s has drifted from %s in %d file(s):\n", out, origin, len(drift))
	for _, d := range drift {
		fmt.Fprintf(&b, "  %-10s %s\n", d.Kind, d.Path)
	}
	b.WriteString("\nRun `make catalog-sync` (or `go run ./cmd/catalogsync`) to vendor the current\n")
	b.WriteString("web catalog, review the diff, and commit it. If a difference is deliberate,\n")
	b.WriteString("declare it in catalog-overlay/ instead of editing services/ by hand.")
	return &driftError{detail: b.String()}
}

// readDir reads every .yml file under root into a map keyed by slash-separated path
// relative to root. A missing root is an empty catalog, not an error: -check must be
// able to say "everything is missing" rather than fail to run. Line endings are
// normalised to LF, so a Windows checkout (core.autocrlf=true rewrites every file on
// the way out of git) is not reported as a catalog where every single file drifted.
func readDir(root string) (map[string][]byte, error) {
	files := map[string][]byte{}
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".yml") {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = catalogsync.NormaliseEOL(raw)
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return files, nil
	}
	return files, err
}

// writeDir makes root hold exactly want: files are created or overwritten, and .yml
// files present in have but absent from want are deleted. Deleting is the point —
// a service the web catalog removed must disappear from Desktop too, and leaving it
// behind is precisely the staleness this command exists to end.
func writeDir(root string, want, have map[string][]byte) error {
	for rel, body := range want {
		full := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(full, body, 0o644); err != nil {
			return err
		}
	}
	var stale []string
	for rel := range have {
		if _, ok := want[rel]; !ok {
			stale = append(stale, rel)
		}
	}
	sort.Strings(stale)
	for _, rel := range stale {
		if err := os.Remove(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			return err
		}
	}
	return nil
}

// codeloadBase is the tarball endpoint for the web repository. A variable so a test
// can point the fetch at an httptest server instead of the network.
var codeloadBase = "https://codeload.github.com/GeiserX/CashPilot"

// fetchSleep is the pause between retry attempts. A variable so a test does not wait.
var fetchSleep = time.Sleep

// fetchAttempts is how many times a transient fetch failure is retried. The weekly
// job gets one shot a week, so a single dropped connection used to be reported as a
// catalog that had drifted.
const fetchAttempts = 3

// tarballURL builds the codeload URL for ref.
//
// The ref goes in whole, without a refs/heads/ prefix: codeload resolves a branch, a
// tag or a commit SHA at this path, and the prefixed form resolves branches ONLY —
// so -ref v1.37.0 answered 404, which the workflow then announced as drift.
func tarballURL(ref string) string {
	return fmt.Sprintf("%s/tar.gz/%s", strings.TrimSuffix(codeloadBase, "/"), ref)
}

// retryableFetch reports whether a fetch failure is worth another attempt. A
// transport error (DNS, refused connection, reset mid-body) and a 5xx or 429 are;
// a 404 is not — a ref that does not exist will not start existing, and retrying it
// only makes the job take three times as long to say the same thing.
type httpStatusError struct {
	url    string
	status int
	text   string
}

func (e *httpStatusError) Error() string { return fmt.Sprintf("fetch %s: HTTP %s", e.url, e.text) }

func retryableFetch(err error) bool {
	var status *httpStatusError
	if errors.As(err, &status) {
		return status.status == http.StatusTooManyRequests || status.status >= 500
	}
	return true
}

// fetchUpstream downloads the CashPilot source tarball for ref from codeload and
// returns its services/**.yml entries. codeload serves public repositories without
// any credential, so the weekly drift workflow needs no token.
func fetchUpstream(ref string) (map[string][]byte, error) {
	url := tarballURL(ref)
	client := &http.Client{Timeout: 2 * time.Minute}
	var err error
	for attempt := 1; ; attempt++ {
		var files map[string][]byte
		files, err = fetchOnce(client, url)
		if err == nil {
			return files, nil
		}
		if attempt == fetchAttempts || !retryableFetch(err) {
			return nil, err
		}
		fmt.Fprintf(os.Stderr, "catalogsync: %v (attempt %d of %d, retrying)\n", err, attempt, fetchAttempts)
		fetchSleep(time.Duration(attempt) * time.Second)
	}
}

// fetchOnce performs a single download-and-extract.
func fetchOnce(client *http.Client, url string) (map[string][]byte, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &httpStatusError{url: url, status: resp.StatusCode, text: resp.Status}
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("decompress %s: %w", url, err)
	}
	defer gz.Close()

	files := map[string][]byte{}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", url, err)
		}
		if hdr.Typeflag != tar.TypeReg || !strings.HasSuffix(hdr.Name, ".yml") {
			continue
		}
		// The tarball's top-level directory is CashPilot-<ref>; strip it and keep
		// only what sits under services/.
		parts := strings.SplitN(path.Clean(hdr.Name), "/", 3)
		if len(parts) != 3 || parts[1] != "services" {
			continue
		}
		body, err := io.ReadAll(tr)
		if err != nil {
			return nil, fmt.Errorf("read %s from %s: %w", hdr.Name, url, err)
		}
		files[parts[2]] = catalogsync.NormaliseEOL(body)
	}
	return files, nil
}
