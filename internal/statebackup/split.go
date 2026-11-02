package statebackup

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

// SplitArchives turns a decrypted payload into one tar per backed-up mount.
//
// The payload is Docker's archive of each critical mount, one after another, so
// each archive's entries sit under the mount's last path component
// (/var/lib/mysterium-node becomes mysterium-node/...). They are regrouped by that
// name. Owners, modes and times are kept, which is what `docker cp` needs to put
// the files back the way the service expects.
//
// The archives get extracted as root into a volume, and in recipient mode anyone
// holding the public key can build a bundle that opens cleanly. So every entry is
// checked on its own and one unsafe entry fails the whole restore: an absolute
// name, "..", a link that points out of its folder, a path through a link, a device
// or a pipe. Setuid and setgid bits are dropped. Same rules as the web's
// split_archives.
func SplitArchives(payload []byte) (map[string][]byte, error) {
	type entry struct {
		hdr  *tar.Header
		data []byte
	}
	groups := map[string][]entry{}
	var order []string
	links := map[string]bool{}

	err := eachMember(payload, func(hdr *tar.Header, body io.Reader) error {
		name := strings.TrimPrefix(hdr.Name, "./")
		if why := unsafeMember(hdr, name, links); why != "" {
			return fail("the backup holds an unsafe entry and was not restored: %s", why)
		}
		name = strings.Join(cleanParts(name), "/")
		if hdr.Typeflag == tar.TypeSymlink || hdr.Typeflag == tar.TypeLink {
			links[name] = true
		}
		out := *hdr
		out.Name = name
		out.Mode &= 0o1777
		out.Format = tar.FormatPAX
		out.PAXRecords = nil
		var data []byte
		switch hdr.Typeflag {
		case tar.TypeReg, tar.TypeCont, tar.TypeGNUSparse:
			b, err := io.ReadAll(body)
			if err != nil {
				return err
			}
			data = b
			out.Typeflag = tar.TypeReg
			out.Size = int64(len(b))
		case tar.TypeDir:
			out.Name = name + "/"
			out.Size = 0
		default:
			out.Size = 0
		}
		top := strings.SplitN(name, "/", 2)[0]
		if _, ok := groups[top]; !ok {
			order = append(order, top)
		}
		groups[top] = append(groups[top], entry{&out, data})
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

// unsafeMember says why an entry could write outside its own folder on restore,
// or "" when it cannot. links holds the (cleaned) names of links seen so far.
func unsafeMember(hdr *tar.Header, name string, links map[string]bool) string {
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
	switch hdr.Typeflag {
	case tar.TypeSymlink, tar.TypeLink:
		target := hdr.Linkname
		tparts := strings.Split(target, "/")
		if target == "" || strings.HasPrefix(target, "/") || strings.HasPrefix(target, `\`) || contains(tparts, "..") {
			return fmt.Sprintf("%q links to %q, outside its folder", name, target)
		}
		if hdr.Typeflag == tar.TypeLink {
			if tparts[0] != parts[0] {
				return fmt.Sprintf("%q links to %q, outside its folder", name, target)
			}
			for d := 1; d < len(tparts); d++ {
				if links[strings.Join(tparts[:d], "/")] {
					return fmt.Sprintf("%q links to %q, outside its folder", name, target)
				}
			}
		}
	case tar.TypeReg, tar.TypeCont, tar.TypeGNUSparse, tar.TypeDir:
	default:
		return fmt.Sprintf("%q is a device or pipe, not a file or folder", name)
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
