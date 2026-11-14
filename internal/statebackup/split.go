package statebackup

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// MaxArchiveBytes is the most file data a restore unpacks. An export never reads
// more than MaxStateBytes, tar headers included, so every bundle Desktop made
// fits. The web caps its own exports at 64 MB, so its bundles fit too.
const MaxArchiveBytes = MaxStateBytes

// MaxArchiveEntries is the most entries a restore accepts. An identity, keystore
// or wallet folder holds tens of files, a relay folder a few hundred. Past this it
// is a data folder, which belongs in a disk-level backup. Same number as the web.
const MaxArchiveEntries = 20_000

// paxFields are the only PAX records a restore accepts. The tar reader turns each
// into a Header field (name, link, size, owner, times) and the entry is rebuilt
// from those fields. Anything else is refused: SCHILY.xattr.* (file capabilities,
// SELinux labels), ACLs, GNU.sparse.*, vendor records. Docker applies every
// SCHILY.xattr.* record it extracts, so passing one through would hand a bundle's
// author a file capability on the restored volume. Same list as the web.
var paxFields = map[string]bool{
	"path": true, "linkpath": true, "size": true, "uid": true, "gid": true, "uname": true,
	"gname": true, "mtime": true, "atime": true, "ctime": true, "hdrcharset": true,
}

// maxModTime is how far from 1970 a modification time may be (about 317 years).
// The web refuses past it too, so a bundle opens in both tools or in neither.
const maxModTime = 10_000_000_000

// controlChar returns the first control character in s, or -1. A NUL would end
// the name once it is written back to a fixed ustar field, so the rules below
// would have judged a different name than the one that gets extracted.
func controlChar(s string) rune {
	for _, c := range s {
		if c < 0x20 || c == 0x7f {
			return c
		}
	}
	return -1
}

// SplitArchives turns a decrypted payload into one tar per backed-up mount.
//
// The payload is Docker's archive of each critical mount, one after another, so
// each archive's entries sit under the mount's last path component
// (/var/lib/mysterium-node becomes mysterium-node/...). They are regrouped by that
// name.
//
// Every entry is rebuilt from a short list of fields: name, type, permission bits,
// owner ids, modification time, size and link target. Owners, modes and times are
// what `docker cp` and `tar -x` need to put the files back the way the service
// expects; nothing else from the input reaches the output.
//
// The archives get extracted as root into a volume, and in recipient mode anyone
// holding the public key can build a bundle that opens cleanly. So every entry is
// checked on its own and one refused entry fails the whole restore, with an error
// that names the rule: an absolute name, "..", a link that points out of its
// folder, a path through a link, a path given twice, a device or pipe, a sparse
// file, a PAX global header, any extended record such as a file capability, a
// control character in a name or link target, or an invalid time. Setuid, setgid and sticky bits are dropped. More than MaxArchiveBytes of file
// data or MaxArchiveEntries entries is refused before the data is read. Same rules
// as the web's split_archives.
func SplitArchives(payload []byte) (map[string][]byte, error) {
	type entry struct {
		hdr  *tar.Header
		data []byte
	}
	groups := map[string][]entry{}
	var order []string
	links := map[string]bool{}
	files := map[string]bool{}
	seen := map[string]bool{}
	count := 0
	var total int64

	err := eachMember(payload, func(hdr *tar.Header, body io.Reader) error {
		count++
		if count > MaxArchiveEntries {
			return fail("the backup holds more than %d entries and was not restored (entry cap); that is a data folder, not an identity: back it up at the disk level", MaxArchiveEntries)
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		if why := unsafeMember(hdr, name, links, files, seen); why != "" {
			return fail("the backup holds an unsafe entry and was not restored: %s", why)
		}
		name = strings.Join(cleanParts(name), "/")
		seen[name] = true
		out := &tar.Header{
			Name:    name,
			Mode:    hdr.Mode & 0o777,
			Uid:     hdr.Uid,
			Gid:     hdr.Gid,
			ModTime: hdr.ModTime.Truncate(time.Second),
			Format:  tar.FormatPAX,
		}
		var data []byte
		switch hdr.Typeflag {
		case tar.TypeReg, tar.TypeCont:
			// Compared before the addition, so a huge declared size cannot wrap total.
			if hdr.Size > MaxArchiveBytes-total {
				return fail("the backup unpacks to more than %d MB and was not restored (size cap); no export is that large", MaxArchiveBytes>>20)
			}
			total += hdr.Size
			b, err := io.ReadAll(body)
			if err != nil {
				return fail("the backup's archive is damaged: %v", err)
			}
			data = b
			out.Typeflag = tar.TypeReg
			out.Size = int64(len(b))
			files[name] = true
		case tar.TypeDir:
			out.Typeflag = tar.TypeDir
			out.Name = name + "/"
		case tar.TypeSymlink:
			out.Typeflag = tar.TypeSymlink
			out.Linkname = hdr.Linkname
			links[name] = true
		case tar.TypeLink:
			out.Typeflag = tar.TypeLink
			out.Linkname = strings.Join(cleanParts(hdr.Linkname), "/")
			links[name] = true
		}
		top := strings.SplitN(name, "/", 2)[0]
		if _, ok := groups[top]; !ok {
			order = append(order, top)
		}
		groups[top] = append(groups[top], entry{out, data})
		return nil
	})
	if err != nil {
		return nil, err
	}

	result := make(map[string][]byte, len(groups))
	sort.Strings(order)
	for _, top := range order {
		var buf bytes.Buffer
		w := tar.NewWriter(&buf)
		for _, e := range groups[top] {
			if err := w.WriteHeader(e.hdr); err != nil {
				return nil, err
			}
			if e.data != nil {
				if _, err := w.Write(e.data); err != nil {
					return nil, err
				}
			}
		}
		if err := w.Close(); err != nil {
			return nil, err
		}
		result[top] = buf.Bytes()
	}
	return result, nil
}

// eachMember walks every entry of one or more tar archives laid end to end,
// skipping the zero blocks between them (Python's ignore_zeros).
func eachMember(payload []byte, fn func(*tar.Header, io.Reader) error) error {
	r := bytes.NewReader(payload)
	zero := make([]byte, 512)
	for {
		// Skip end-of-archive padding until the next archive, or the end.
		for r.Len() >= 512 {
			off := len(payload) - r.Len()
			if !bytes.Equal(payload[off:off+512], zero) {
				break
			}
			_, _ = r.Seek(512, io.SeekCurrent)
		}
		if r.Len() < 512 {
			return nil
		}
		tr := tar.NewReader(r)
		seen := false
		for {
			hdr, err := tr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return fail("the backup's archive is damaged: %v", err)
			}
			seen = true
			if err := fn(hdr, tr); err != nil {
				return err
			}
		}
		if !seen {
			return fail("the backup's archive is damaged")
		}
	}
}

func cleanParts(name string) []string {
	var parts []string
	for _, p := range strings.Split(name, "/") {
		if p != "" && p != "." {
			parts = append(parts, p)
		}
	}
	return parts
}

// unsafeMember says which rule refuses an entry, or "" when it may be restored.
// links, files and seen hold the cleaned names of the links, regular files and all
// entries read so far. A hard link must point at a regular file that came earlier
// in the same folder.
func unsafeMember(hdr *tar.Header, name string, links, files, seen map[string]bool) string {
	if hdr.Typeflag == tar.TypeXGlobalHeader {
		return "the archive carries a PAX global header (refused: it would apply its records to every entry after it)"
	}
	if c := controlChar(name + hdr.Linkname); c >= 0 {
		return fmt.Sprintf("%q has the control character %q in its name or link target", name, c)
	}
	if u := hdr.ModTime.Unix(); u >= maxModTime || u <= -maxModTime {
		return fmt.Sprintf("%q has an invalid modification time", name)
	}
	sparse := hdr.Typeflag == tar.TypeGNUSparse
	var extra []string
	for k := range hdr.PAXRecords {
		if strings.HasPrefix(k, "GNU.sparse.") {
			sparse = true
		}
		if !paxFields[k] {
			extra = append(extra, k)
		}
	}
	if sparse {
		return fmt.Sprintf("%q is a sparse file (refused: a few bytes in the file unpack to gigabytes)", name)
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		key := extra[0]
		if len(key) > 80 {
			key = key[:80]
		}
		return fmt.Sprintf("%q carries the extended record %q (refused: only names, sizes, owners, modes and times are restored, never extended attributes such as file capabilities)", name, key)
	}
	switch hdr.Typeflag {
	case tar.TypeReg, tar.TypeCont, tar.TypeDir, tar.TypeSymlink, tar.TypeLink:
	default:
		return fmt.Sprintf("%q is a device, pipe or other special entry, not a file, folder or link", name)
	}
	if strings.HasPrefix(name, "/") || strings.HasPrefix(name, `\`) {
		return fmt.Sprintf("%q is an absolute path", name)
	}
	parts := cleanParts(name)
	if len(parts) == 0 || contains(parts, "..") {
		return fmt.Sprintf("%q leaves its folder", name)
	}
	for depth := 1; depth < len(parts); depth++ {
		if links[strings.Join(parts[:depth], "/")] {
			return fmt.Sprintf("%q is written through a link", name)
		}
	}
	if seen[strings.Join(parts, "/")] {
		return fmt.Sprintf("%q appears twice (each path may appear once)", name)
	}
	if hdr.Typeflag == tar.TypeSymlink || hdr.Typeflag == tar.TypeLink {
		target := hdr.Linkname
		tparts := strings.Split(target, "/")
		if target == "" || strings.HasPrefix(target, "/") || strings.HasPrefix(target, `\`) || contains(tparts, "..") {
			return fmt.Sprintf("%q links to %q, outside its folder", name, target)
		}
		if hdr.Typeflag == tar.TypeLink {
			clean := cleanParts(target)
			if len(clean) == 0 || !files[strings.Join(clean, "/")] || clean[0] != parts[0] {
				return fmt.Sprintf("%q hard-links to %q, which is not a file earlier in the same folder", name, target)
			}
		}
	}
	return ""
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
