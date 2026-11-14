package statebackup

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// gnuSparse is a GNU sparse member built by hand, because archive/tar cannot write
// one: 512 stored bytes that unpack to realSize bytes. The single map entry puts
// the stored block at the end of the file, so the rest unpacks as zeros.
func gnuSparse(t *testing.T, name string, realSize int64) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	if err := w.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Size: 512, Mode: 0o644, Format: tar.FormatGNU}); err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(bytes.Repeat([]byte("x"), 512))
	_ = w.Close()
	b := buf.Bytes()
	b[156] = tar.TypeGNUSparse
	copy(b[386:398], fmt.Sprintf("%011o\x00", realSize-512)) // first map entry: offset
	copy(b[398:410], fmt.Sprintf("%011o\x00", 512))          // first map entry: length
	copy(b[483:495], fmt.Sprintf("%011o\x00", realSize))     // the size it unpacks to
	copy(b[148:156], "        ")
	var sum int64
	for _, c := range b[:512] {
		sum += int64(c)
	}
	copy(b[148:156], fmt.Sprintf("%06o\x00 ", sum))
	return b
}

// paxFile is one regular file carrying the given PAX records, as Docker writes
// xattrs. global, when set, is written first as a PAX global header.
func paxFile(t *testing.T, name string, records, global map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	if global != nil {
		if err := w.WriteHeader(&tar.Header{Typeflag: tar.TypeXGlobalHeader, PAXRecords: global}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Size: 1, Mode: 0o644, PAXRecords: records, Format: tar.FormatPAX}); err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("x"))
	_ = w.Close()
	return buf.Bytes()
}

// paxRaw writes a PAX extended header block by hand, for records archive/tar's
// writer refuses to encode (GNU.sparse.*), followed by a regular file header with
// size bytes of data.
func paxRaw(t *testing.T, name string, records map[string]string, size int) []byte {
	t.Helper()
	var rec strings.Builder
	for k, v := range records {
		// A record is "<length> <key>=<value>\n", and the length counts its own digits.
		line := " " + k + "=" + v + "\n"
		n := len(line) + len(fmt.Sprint(len(line)))
		n = len(line) + len(fmt.Sprint(n))
		rec.WriteString(fmt.Sprint(n) + line)
	}
	body := []byte(rec.String())
	block := func(name string, flag byte, size int) []byte {
		b := make([]byte, 512)
		copy(b[0:], name)
		copy(b[100:], "0000644\x00")
		copy(b[108:], "0000000\x00")
		copy(b[116:], "0000000\x00")
		copy(b[124:], fmt.Sprintf("%011o\x00", size))
		copy(b[136:], "00000000000\x00")
		b[156] = flag
		copy(b[257:], "ustar\x0000")
		copy(b[148:156], "        ")
		var sum int
		for _, c := range b {
			sum += int(c)
		}
		copy(b[148:156], fmt.Sprintf("%06o\x00 ", sum))
		return b
	}
	pad := func(n int) []byte { return make([]byte, (512-n%512)%512) }
	var out bytes.Buffer
	out.Write(block("PaxHeader", tar.TypeXHeader, len(body)))
	out.Write(body)
	out.Write(pad(len(body)))
	out.Write(block(name, tar.TypeReg, size))
	out.Write(bytes.Repeat([]byte("x"), size))
	out.Write(pad(size))
	out.Write(make([]byte, 1024))
	return out.Bytes()
}

var capability = map[string]string{"SCHILY.xattr.security.capability": "\x01\x00\x00\x02\x00\x10"}

// Each case is refused, and the error names the rule that refused it.
func TestSplitRefusesWithTheRuleThatFired(t *testing.T) {
	big := int64(100 << 20)
	for name, tc := range map[string]struct {
		payload []byte
		rule    string
	}{
		"sparse":                   {gnuSparse(t, "node/big", big), "sparse file"},
		"pax sparse":               {paxRaw(t, "node/big", map[string]string{"GNU.sparse.size": fmt.Sprint(big), "GNU.sparse.numblocks": "1", "GNU.sparse.map": fmt.Sprintf("%d,512", big-512)}, 512), "sparse file"},
		"file capability":          {paxFile(t, "node/run", capability, nil), "SCHILY.xattr.security.capability"},
		"user xattr":               {paxFile(t, "node/f", map[string]string{"SCHILY.xattr.user.x": "y"}, nil), "SCHILY.xattr.user.x"},
		"acl":                      {paxFile(t, "node/f", map[string]string{"SCHILY.acl.access": "user::rwx"}, nil), "SCHILY.acl.access"},
		"pax global header":        {paxFile(t, "node/f", nil, map[string]string{"comment": "x"}), "global header"},
		"duplicate path":           {tarOf(t, member{name: "node/f", kind: tar.TypeReg}, member{name: "node/f", kind: tar.TypeReg}), "appears twice"},
		"duplicate after cleaning": {tarOf(t, member{name: "node/f", kind: tar.TypeReg}, member{name: "./node//f", kind: tar.TypeReg}), "appears twice"},
		"hardlink to a later file": {tarOf(t, member{name: "node/h", link: "node/f", kind: tar.TypeLink}, member{name: "node/f", kind: tar.TypeReg}), "not a file earlier"},
		"hardlink to a folder":     {tarOf(t, member{name: "node/d/", kind: tar.TypeDir}, member{name: "node/h", link: "node/d", kind: tar.TypeLink}), "not a file earlier"},
		"char device":              {tarOf(t, member{name: "node/dev", kind: tar.TypeChar}), "device, pipe"},
		"fifo":                     {tarOf(t, member{name: "node/p", kind: tar.TypeFifo}), "device, pipe"},
		"absolute":                 {tarOf(t, member{name: "/etc/passwd", kind: tar.TypeReg}), "absolute path"},
		"dot-dot":                  {tarOf(t, member{name: "node/../../x", kind: tar.TypeReg}), "leaves its folder"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := SplitArchives(tc.payload)
			if !errors.Is(err, ErrBackup) {
				t.Fatalf("refused archive was split: %v", err)
			}
			if !strings.Contains(err.Error(), "unsafe entry") || !strings.Contains(err.Error(), tc.rule) {
				t.Fatalf("error %q does not name the rule %q", err, tc.rule)
			}
		})
	}
}

// The sparse fixtures really are bombs: 2 KB on disk, 100 MB through archive/tar.
func TestSparseFixtureUnpacksToItsRealSize(t *testing.T) {
	payload := gnuSparse(t, "node/big", 100<<20)
	if len(payload) != 2048 {
		t.Fatalf("fixture is %d bytes, want 2048", len(payload))
	}
	tr := tar.NewReader(bytes.NewReader(payload))
	hdr, err := tr.Next()
	if err != nil {
		t.Fatal(err)
	}
	if hdr.Typeflag != tar.TypeGNUSparse || hdr.Size != 100<<20 {
		t.Fatalf("fixture header = %c %d, want a 100 MB sparse file", hdr.Typeflag, hdr.Size)
	}
}

// Only name, type, permission bits, owner ids, time, size and link reach the
// archive that gets extracted as root.
func TestSplitKeepsOnlyTheListedFields(t *testing.T) {
	long := "node/" + strings.Repeat("k", 150) // long enough to need a PAX path record
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	mtime := time.Unix(1767225600, 750_000_000)
	for _, h := range []*tar.Header{
		{Name: "node/", Typeflag: tar.TypeDir, Mode: 0o1777, Uid: 100, Gid: 101, Uname: "root", Gname: "wheel", ModTime: time.Unix(1767225600, 0)},
		{Name: long, Typeflag: tar.TypeReg, Mode: 0o6755, Uid: 100, Gid: 101, Size: 1, ModTime: mtime, Format: tar.FormatPAX},
	} {
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			_, _ = w.Write([]byte("x"))
		}
	}
	_ = w.Close()
	out, err := SplitArchives(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(bytes.NewReader(out["node"]))
	got := map[string]*tar.Header{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got[strings.TrimSuffix(h.Name, "/")] = h
	}
	d, f := got["node"], got[long]
	if d == nil || f == nil || len(got) != 2 {
		t.Fatalf("members = %v", got)
	}
	if d.Mode != 0o777 || d.Uid != 100 || d.Gid != 101 || d.Uname != "" || d.Gname != "" {
		t.Fatalf("folder = mode %o uid %d gid %d %q %q", d.Mode, d.Uid, d.Gid, d.Uname, d.Gname)
	}
	if f.Mode != 0o755 || !f.ModTime.Equal(time.Unix(1767225600, 0)) {
		t.Fatalf("file = mode %o mtime %v", f.Mode, f.ModTime)
	}
	for k := range f.PAXRecords {
		if k != "path" {
			t.Fatalf("file carries PAX record %q", k)
		}
	}
	if len(d.PAXRecords) != 0 || len(f.Xattrs) != 0 { //nolint:staticcheck // Xattrs is what the writer re-emits
		t.Fatalf("records survived: %v %v", d.PAXRecords, f.Xattrs) //nolint:staticcheck
	}
}

// Docker writes PAX records for long names and large ids; those restore.
func TestSplitAcceptsHarmlessPAXRecords(t *testing.T) {
	payload := paxRaw(t, "node/f", map[string]string{"uid": "3000000", "gid": "3000001", "uname": "u", "mtime": "1767225600.5"}, 1)
	out, err := SplitArchives(payload)
	if err != nil {
		t.Fatal(err)
	}
	h, err := tar.NewReader(bytes.NewReader(out["node"])).Next()
	if err != nil {
		t.Fatal(err)
	}
	if h.Uid != 3000000 || h.Gid != 3000001 || h.Uname != "" {
		t.Fatalf("header = uid %d gid %d uname %q", h.Uid, h.Gid, h.Uname)
	}
}

func TestSplitRewritesADotPrefixedHardlink(t *testing.T) {
	out, err := SplitArchives(tarOf(t, member{name: "node/f", kind: tar.TypeReg}, member{name: "node/h", link: "./node/f", kind: tar.TypeLink}))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(bytes.NewReader(out["node"]))
	_, _ = tr.Next()
	h, err := tr.Next()
	if err != nil || h.Linkname != "node/f" {
		t.Fatalf("hard link = %+v, %v", h, err)
	}
}

func TestSplitCaps(t *testing.T) {
	if MaxArchiveBytes < 64<<20 {
		t.Fatalf("MaxArchiveBytes %d is under the web's 64 MB export cap", MaxArchiveBytes)
	}
	if MaxArchiveBytes != MaxStateBytes {
		t.Fatalf("MaxArchiveBytes %d differs from the export cap %d", MaxArchiveBytes, MaxStateBytes)
	}

	t.Run("entries", func(t *testing.T) {
		var ms []member
		for i := 0; i <= MaxArchiveEntries; i++ {
			ms = append(ms, member{name: fmt.Sprintf("node/%d", i), kind: tar.TypeDir})
		}
		_, err := SplitArchives(tarOf(t, ms...))
		if !errors.Is(err, ErrBackup) || !strings.Contains(err.Error(), "entry cap") {
			t.Fatalf("%d entries were split: %v", len(ms), err)
		}
		if _, err := SplitArchives(tarOf(t, ms[:MaxArchiveEntries]...)); err != nil {
			t.Fatalf("control: %d entries refused: %v", MaxArchiveEntries, err)
		}
	})

	t.Run("bytes", func(t *testing.T) {
		// One header that declares more than the cap, with no data behind it: the
		// cap must fire on the declared size, before anything is read.
		var buf bytes.Buffer
		w := tar.NewWriter(&buf)
		_ = w.WriteHeader(&tar.Header{Name: "node/big", Typeflag: tar.TypeReg, Size: MaxArchiveBytes + 1, Mode: 0o644})
		_, err := SplitArchives(buf.Bytes())
		if !errors.Is(err, ErrBackup) || !strings.Contains(err.Error(), "size cap") {
			t.Fatalf("an oversized file was not refused by the size cap: %v", err)
		}
	})
}
