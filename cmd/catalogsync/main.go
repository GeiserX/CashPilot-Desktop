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
//	go run ./cmd/catalogsync -check          # same, but only report drift (exit 1 if any)
//	go run ./cmd/catalogsync -src ../CashPilot   # use a local checkout instead of fetching
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

func main() {
	var (
		src     = flag.String("src", "", "local CashPilot checkout to copy from; empty means fetch the tarball")
		ref     = flag.String("ref", "main", "git ref of GeiserX/CashPilot to fetch when -src is empty")
		out     = flag.String("out", "services", "directory to write the vendored catalog into")
		overlay = flag.String("overlay", "catalog-overlay", "directory holding the Desktop-only deltas")
		check   = flag.Bool("check", false, "report drift instead of writing; exit 1 when the vendored copy differs")
	)
	flag.Parse()

	if err := run(*src, *ref, *out, *overlay, *check); err != nil {
		fmt.Fprintf(os.Stderr, "catalogsync: %v\n", err)
		os.Exit(1)
	}
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
	return errors.New(b.String())
}

// readDir reads every .yml file under root into a map keyed by slash-separated path
// relative to root. A missing root is an empty catalog, not an error: -check must be
// able to say "everything is missing" rather than fail to run.
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
		files[filepath.ToSlash(rel)] = raw
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

// fetchUpstream downloads the CashPilot source tarball for ref from codeload and
// returns its services/**.yml entries. codeload serves public repositories without
// any credential, so the weekly drift workflow needs no token.
func fetchUpstream(ref string) (map[string][]byte, error) {
	url := fmt.Sprintf("https://codeload.github.com/GeiserX/CashPilot/tar.gz/refs/heads/%s", ref)
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch %s: HTTP %s", url, resp.Status)
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
		files[parts[2]] = body
	}
	return files, nil
}
