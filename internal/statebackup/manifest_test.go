package statebackup

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// webRestoreVectors is testdata/web_restore_vectors.json, written by
// gen_restore_vectors.py with the web app's own restore code.
type webRestoreVectors struct {
	Passphrase string             `json:"passphrase"`
	Bundle     string             `json:"bundle"`
	Manifests  map[string][][]any `json:"manifests"`
	Cases      []struct {
		Name  string   `json:"name"`
		Found [][]any  `json:"found"`
		Lines []string `json:"lines"`
	} `json:"cases"`
}

func loadRestoreVectors(t *testing.T) webRestoreVectors {
	t.Helper()
	raw, err := os.ReadFile("testdata/web_restore_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v webRestoreVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// manifestFromRows reads the web's [path, kind, mode, uid, gid, content] rows.
func manifestFromRows(t *testing.T, rows [][]any) map[string]ManifestEntry {
	t.Helper()
	out := map[string]ManifestEntry{}
	for _, r := range rows {
		if len(r) != 6 {
			t.Fatalf("bad row %v", r)
		}
		out[r[0].(string)] = ManifestEntry{
			Kind: r[1].(string), Mode: int64(r[2].(float64)), UID: int(r[3].(float64)), GID: int(r[4].(float64)), Content: r[5].(string),
		}
	}
	return out
}

// A passphrase file the web app sealed opens here, and the folder Go reads out of it
// is the one the web would restore and verify: same paths, types, modes, owners and
// hashes, the setuid bit dropped, the long PAX name intact.
func TestWebBundleManifestMatchesTheWeb(t *testing.T) {
	v := loadRestoreVectors(t)
	payload, err := Open(b64(t, v.Bundle), v.Passphrase, "")
	if err != nil {
		t.Fatal(err)
	}
	archives, err := SplitArchives(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(archives) != len(v.Manifests) {
		t.Fatalf("got folders %v, the web has %d", keys(archives), len(v.Manifests))
	}
	for top, rows := range v.Manifests {
		got, err := TreeManifest(archives[top])
		if err != nil {
			t.Fatal(err)
		}
		want := manifestFromRows(t, rows)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s manifest differs from the web's\n got  %v\n want %v", top, got, want)
		}
		// What Desktop writes into the volume reads back as the same folder.
		writable, err := WritableArchive(archives[top])
		if err != nil {
			t.Fatal(err)
		}
		again, err := TreeManifest(writable)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(again, want) {
			t.Fatalf("%s: the archive Desktop writes is not the backup\n got  %v\n want %v", top, again, want)
		}
	}
}

// The differences a read-back reports are the web's, line for line.
func TestManifestDifferencesMatchTheWeb(t *testing.T) {
	v := loadRestoreVectors(t)
	base := manifestFromRows(t, v.Manifests["mysterium-node"])
	if len(v.Cases) < 5 {
		t.Fatalf("only %d cases in the vectors", len(v.Cases))
	}
	for _, c := range v.Cases {
		got := ManifestDifferences(base, manifestFromRows(t, c.Found))
		if len(got) == 0 && len(c.Lines) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, c.Lines) {
			t.Errorf("%s: got %q, the web says %q", c.Name, got, c.Lines)
		}
	}
}

func keys(m map[string][]byte) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

func headerTar(t *testing.T, entries ...*tar.Header) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for _, h := range entries {
		h.ModTime = time.Unix(1767225600, 0)
		h.Format = tar.FormatPAX
		body := ""
		if h.Typeflag == tar.TypeReg {
			body = strings.Repeat("k", int(h.Size))
		}
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if body != "" {
			if _, err := w.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// The volume's root is its mount point: an archive whose top entry is a file or a
// link must not replace it. The entry is left out, so the read-back finds a folder
// where the backup had something else and refuses.
func TestWritableArchiveDropsANonFolderRoot(t *testing.T) {
	archive := headerTar(t,
		&tar.Header{Typeflag: tar.TypeSymlink, Name: "anon", Linkname: "elsewhere", Mode: 0o777},
	)
	writable, err := WritableArchive(archive)
	if err != nil {
		t.Fatal(err)
	}
	got, err := TreeManifest(writable)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("the root link was written: %v", got)
	}
	want, _ := TreeManifest(archive)
	found := map[string]ManifestEntry{Root: {Kind: "dir", Mode: 0o755}}
	if diff := ManifestDifferences(want, found); len(diff) != 1 || diff[0] != "the folder itself: a dir, not a symlink" {
		t.Fatalf("diff = %q", diff)
	}
}

func TestTreeManifestRefusesMoreThanABackupHolds(t *testing.T) {
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	_ = w.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: "x/big", Size: MaxArchiveBytes + 1, Mode: 0o600})
	_ = w.Flush()
	if _, err := TreeManifest(buf.Bytes()); err == nil || !strings.Contains(err.Error(), "more than a backup can") {
		t.Fatalf("err = %v", err)
	}
}

func TestReadBundleFileRefusesAnOversizedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big.cpbackup")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(MaxBundleBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := ReadBundleFile(path); err == nil || !strings.Contains(err.Error(), "larger than any backup") {
		t.Fatalf("err = %v", err)
	}
}

func TestFolderName(t *testing.T) {
	for in, want := range map[string]string{"/var/lib/mysterium-node": "mysterium-node", "/var/lib/anon/": "anon", "/home/proxybase/.proxybase": ".proxybase"} {
		if got := FolderName(in); got != want {
			t.Errorf("FolderName(%q) = %q, want %q", in, got, want)
		}
	}
}
