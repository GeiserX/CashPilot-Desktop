package statebackup

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
)

// webVectors are bundles sealed by the web app's own app/state_backup.py
// (testdata/gen_vectors.py). Opening them here is the proof the two apps share one
// format; a Go-only round trip could agree with itself and still disagree with
// the web.
type webVectors struct {
	Passphrase       string                     `json:"passphrase"`
	PrivateKey       string                     `json:"private_key"`
	PublicKey        string                     `json:"public_key"`
	PayloadSHA256    string                     `json:"payload_sha256"`
	PassphraseBundle string                     `json:"passphrase_bundle"`
	RecipientBundle  string                     `json:"recipient_bundle"`
	Archives         map[string][]archiveMember `json:"archives"`
}

type archiveMember struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Mode     int64  `json:"mode"`
	Linkname string `json:"linkname"`
	SHA256   string `json:"sha256"`
}

func loadVectors(t *testing.T) webVectors {
	t.Helper()
	raw, err := os.ReadFile("testdata/web_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v webVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func b64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func TestOpensBundlesSealedByTheWebApp(t *testing.T) {
	v := loadVectors(t)
	for _, tc := range []struct {
		name, bundle, pass, key string
	}{
		{"passphrase", v.PassphraseBundle, v.Passphrase, ""},
		{"recipient", v.RecipientBundle, "", v.PrivateKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bundle := b64(t, tc.bundle)
			payload, err := Open(bundle, tc.pass, tc.key)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if got := sum(payload); got != v.PayloadSHA256 {
				t.Fatalf("payload sha256 = %s, want %s", got, v.PayloadSHA256)
			}
			h, err := ReadHeader(bundle)
			if err != nil {
				t.Fatal(err)
			}
			if h.Mode != tc.name || h.Metadata["slug"] != "mysterium" {
				t.Fatalf("header = %+v", h)
			}
		})
	}
}

// describe reads a tar the way gen_vectors.py's describe does.
func describe(t *testing.T, data []byte) []archiveMember {
	t.Helper()
	var out []archiveMember
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(tr)
		kind := "file"
		switch hdr.Typeflag {
		case tar.TypeDir:
			kind = "dir"
		case tar.TypeSymlink:
			kind = "sym"
		}
		out = append(out, archiveMember{
			Name: strings.TrimSuffix(hdr.Name, "/"), Type: kind, Mode: hdr.Mode, Linkname: hdr.Linkname, SHA256: sum(body),
		})
	}
}

// The web's split_archives and this one produce the same tars: same members, same
// modes (setuid dropped), same links, same bytes.
func TestSplitMatchesTheWebApp(t *testing.T) {
	v := loadVectors(t)
	payload, err := Open(b64(t, v.PassphraseBundle), v.Passphrase, "")
	if err != nil {
		t.Fatal(err)
	}
	archives, err := SplitArchives(payload)
	if err != nil {
		t.Fatalf("SplitArchives: %v", err)
	}
	got := map[string][]archiveMember{}
	for top, data := range archives {
		got[top] = describe(t, data)
	}
	if !reflect.DeepEqual(got, v.Archives) {
		t.Fatalf("split differs from the web's\n got: %+v\nwant: %+v", got, v.Archives)
	}
}

func TestRoundTripAndRefusals(t *testing.T) {
	payload := []byte("node identity")
	meta := map[string]any{"slug": "storj", "worker": "box", "targets": []string{"/app/identity"}}

	sealed, err := Seal(payload, "twelve chars!", "", meta)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Open(sealed, "twelve chars!", ""); err != nil || string(got) != string(payload) {
		t.Fatalf("passphrase round trip: %q, %v", got, err)
	}
	if _, err := Open(sealed, "twelve chars?", ""); !errors.Is(err, ErrBackup) {
		t.Fatalf("a wrong passphrase opened the bundle: %v", err)
	}

	priv, pub, err := GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	sealed2, err := Seal(payload, "", pub, meta)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Open(sealed2, "", priv); err != nil || string(got) != string(payload) {
		t.Fatalf("recipient round trip: %q, %v", got, err)
	}
	other, _, _ := GenerateKeypair()
	if _, err := Open(sealed2, "", other); !errors.Is(err, ErrBackup) {
		t.Fatalf("another private key opened the bundle: %v", err)
	}

	if _, err := Seal(payload, "eleven char", "", nil); err == nil {
		t.Fatal("an 11-character passphrase was accepted")
	}
	if _, err := Seal(payload, "", "", nil); err == nil {
		t.Fatal("no secret at all was accepted")
	}
	if _, err := Seal(payload, "twelve chars!", pub, nil); err == nil {
		t.Fatal("both a passphrase and a key were accepted")
	}
	if _, err := Seal(payload, "", strings.Repeat("00", 32), nil); err == nil {
		t.Fatal("a low-order recipient key was accepted")
	}
	if _, err := Seal(payload, "", "abcd", nil); err == nil {
		t.Fatal("a short recipient key was accepted")
	}
}

// The header is associated data: changing which box a web bundle says it came from
// must break it, even though the header is not encrypted.
func TestAlteredHeaderDoesNotOpen(t *testing.T) {
	v := loadVectors(t)
	bundle := b64(t, v.PassphraseBundle)
	i := bytes.Index(bundle, []byte("vector-box"))
	if i < 0 {
		t.Fatal("vector has no worker name to alter")
	}
	altered := bytes.Clone(bundle)
	copy(altered[i:], "vector-bot")
	if _, err := ReadHeader(altered); err != nil {
		t.Fatalf("the altered header should still parse: %v", err)
	}
	if _, err := Open(altered, v.Passphrase, ""); !errors.Is(err, ErrBackup) {
		t.Fatalf("a bundle with an altered header opened: %v", err)
	}
	if _, err := Open(bundle, v.Passphrase, ""); err != nil {
		t.Fatalf("control: the unaltered bundle must open: %v", err)
	}
}

type member struct {
	name, link string
	kind       byte
}

func tarOf(t *testing.T, members ...member) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for _, m := range members {
		hdr := &tar.Header{Name: m.name, Typeflag: m.kind, Linkname: m.link, Mode: 0o644}
		if m.kind == tar.TypeReg {
			hdr.Size = 1
		}
		if err := w.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if m.kind == tar.TypeReg {
			_, _ = w.Write([]byte("x"))
		}
	}
	_ = w.Close()
	return buf.Bytes()
}

func TestSplitRefusesEntriesThatEscapeTheirFolder(t *testing.T) {
	dir := member{name: "data/", kind: tar.TypeDir}
	for name, payload := range map[string][]byte{
		"absolute":            tarOf(t, member{name: "/etc/passwd", kind: tar.TypeReg}),
		"dot-dot":             tarOf(t, dir, member{name: "data/../../x", kind: tar.TypeReg}),
		"symlink out":         tarOf(t, dir, member{name: "data/l", link: "../../etc", kind: tar.TypeSymlink}),
		"absolute symlink":    tarOf(t, dir, member{name: "data/l", link: "/etc", kind: tar.TypeSymlink}),
		"through a link":      tarOf(t, dir, member{name: "data/l", link: "sub", kind: tar.TypeSymlink}, member{name: "data/l/f", kind: tar.TypeReg}),
		"hardlink other root": tarOf(t, dir, member{name: "data/h", link: "other/f", kind: tar.TypeLink}),
		"device":              tarOf(t, dir, member{name: "data/dev", kind: tar.TypeChar}),
		"fifo":                tarOf(t, dir, member{name: "data/p", kind: tar.TypeFifo}),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := SplitArchives(payload); !errors.Is(err, ErrBackup) {
				t.Fatalf("unsafe archive was split: %v", err)
			}
		})
	}
	// Control: the same shapes kept inside their folder pass.
	ok := tarOf(t, dir, member{name: "data/l", link: "f", kind: tar.TypeSymlink}, member{name: "data/f", kind: tar.TypeReg},
		member{name: "data/h", link: "data/f", kind: tar.TypeLink})
	if _, err := SplitArchives(ok); err != nil {
		t.Fatalf("control archive refused: %v", err)
	}
}

var goVector = flag.String("go-vector", "", "write a Go-sealed bundle here for gen_vectors.py --open")

// TestWriteGoVector is the other direction of the format check: it writes a bundle
// sealed here for the web's Python to open (see testdata/gen_vectors.py). It only
// runs when asked.
func TestWriteGoVector(t *testing.T) {
	if *goVector == "" {
		t.Skip("set -go-vector=<file>")
	}
	v := loadVectors(t)
	payload, err := Open(b64(t, v.PassphraseBundle), v.Passphrase, "")
	if err != nil {
		t.Fatal(err)
	}
	meta := map[string]any{"slug": "mysterium", "worker": "go-box", "targets": []string{"/app/identity", "/var/lib/mysterium-node"}}
	byPass, err := Seal(payload, v.Passphrase, "", meta)
	if err != nil {
		t.Fatal(err)
	}
	byKey, err := Seal(payload, "", v.PublicKey, meta)
	if err != nil {
		t.Fatal(err)
	}
	for suffix, doc := range map[string]map[string]string{
		".pass.json": {"bundle": base64.StdEncoding.EncodeToString(byPass), "passphrase": v.Passphrase},
		".key.json":  {"bundle": base64.StdEncoding.EncodeToString(byKey), "private_key": v.PrivateKey},
	} {
		raw, _ := json.Marshal(doc)
		if err := os.WriteFile(*goVector+suffix, raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
