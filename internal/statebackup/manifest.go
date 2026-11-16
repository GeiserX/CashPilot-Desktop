package statebackup

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"sort"
	"strings"
)

// Root is the folder's own entry in a manifest: its owner and mode, which a service
// needs as much as its files'. Same key as the web's tree_manifest.
const Root = "."

// MaxBundleBytes is the largest file a restore reads: the export cap, plus room for
// the header and the encryption. Anything bigger was not made by an export, from
// this app (MaxStateBytes) or from the web (64 MB).
const MaxBundleBytes = MaxStateBytes + 1<<20

// ManifestEntry is what one path in a folder is: its type, permission bits, owner
// ids, and a file's SHA-256 or a link's target. Modification times are left out:
// they say nothing about whether a restore is the backup.
type ManifestEntry struct {
	Kind    string // "file", "dir", "symlink", "hardlink" or "other"
	Mode    int64
	UID     int
	GID     int
	Content string
}

// belowTop is name without its first path component: the path inside the backed-up
// folder ("" for the folder itself).
func belowTop(name string) string {
	parts := cleanParts(name)
	if len(parts) < 2 {
		return ""
	}
	return strings.Join(parts[1:], "/")
}

// TreeManifest is what a folder holds, as path -> entry, from Docker's archive of it
// (entries under the folder's own name). path is below the folder, and Root for the
// folder's own entry. A restore reads it twice: on what it meant to write and on
// what Docker reads back, so a file missing, changed, added or owned by someone else
// shows as a difference. More than MaxArchiveBytes of file data is refused rather
// than read. Port of the web's state_backup.tree_manifest.
func TreeManifest(archive []byte) (map[string]ManifestEntry, error) {
	found := map[string]ManifestEntry{}
	var total int64
	tr := tar.NewReader(bytes.NewReader(archive))
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fail("the restored folder could not be read back: %v", err)
		}
		path := belowTop(hdr.Name)
		if path == "" {
			path = Root
		}
		entry := ManifestEntry{Mode: hdr.Mode & 0o777, UID: hdr.Uid, GID: hdr.Gid}
		switch hdr.Typeflag {
		case tar.TypeReg, tar.TypeCont:
			if hdr.Size > MaxArchiveBytes-total {
				return nil, fail("the restored folder holds more than a backup can, so it was not compared")
			}
			total += hdr.Size
			h := sha256.New()
			if _, err := io.Copy(h, tr); err != nil {
				return nil, fail("the restored folder could not be read back: %v", err)
			}
			entry.Kind, entry.Content = "file", hex.EncodeToString(h.Sum(nil))
		case tar.TypeDir:
			entry.Kind = "dir"
		case tar.TypeSymlink:
			entry.Kind, entry.Content = "symlink", hdr.Linkname
		case tar.TypeLink:
			entry.Kind, entry.Content = "hardlink", belowTop(hdr.Linkname)
		default:
			entry.Kind = "other"
		}
		found[path] = entry
	}
	return found, nil
}

// ManifestDifferences says how found differs from expected, one line per path, in
// path order; nil when it is the backup.
//
// A folder the image already had at that path (Docker copies the image's folders
// into a new volume when the container is created) may stay when the backup does
// not name it: it is empty, or its files are listed on their own. Every file, link
// and owner must match exactly, the folder's own owner and mode (Root) included.
// Paths hardlinked to one another may come back with another of them as the file
// (see sameLinkRoles). Otherwise the same lines as the web's manifest_differences.
func ManifestDifferences(expected, found map[string]ManifestEntry) []string {
	found = sameLinkRoles(expected, found)
	paths := map[string]bool{}
	for p := range expected {
		paths[p] = true
	}
	for p := range found {
		paths[p] = true
	}
	sorted := make([]string, 0, len(paths))
	for p := range paths {
		sorted = append(sorted, p)
	}
	sort.Strings(sorted)
	var lines []string
	for _, path := range sorted {
		want, hasWant := expected[path]
		got, hasGot := found[path]
		if hasWant == hasGot && want == got {
			continue
		}
		if !hasWant && got.Kind == "dir" {
			continue
		}
		shown := path
		if path == Root {
			shown = "the folder itself"
		}
		switch {
		case !hasWant:
			lines = append(lines, shown+": not in the backup")
		case !hasGot:
			lines = append(lines, shown+": missing")
		case want.Kind != got.Kind:
			lines = append(lines, fmt.Sprintf("%s: a %s, not a %s", shown, got.Kind, want.Kind))
		case want.Content != got.Content:
			lines = append(lines, shown+": different content")
		default:
			lines = append(lines, shown+": owner or permissions differ")
		}
	}
	return lines
}

// sameLinkRoles gives found's hardlink groups the file the backup named. Docker's
// archive makes the first path its walk visits the file and every other path to the
// same inode a link to it, so a backup whose archive named another path first reads
// back with the roles swapped although it holds the same files. When the backup
// links found's file to a path in the same group, that path becomes the file (with
// the content found holds) and the group links to it. Anything else is left as found
// has it, so its differences show as the web's would.
func sameLinkRoles(expected, found map[string]ManifestEntry) map[string]ManifestEntry {
	groups := map[string][]string{}
	for path, e := range found {
		if e.Kind == "hardlink" && found[e.Content].Kind == "file" {
			groups[e.Content] = append(groups[e.Content], path)
		}
	}
	var out map[string]ManifestEntry
	for target, links := range groups {
		want := expected[target]
		if want.Kind != "hardlink" || expected[want.Content].Kind != "file" || !slices.Contains(links, want.Content) {
			continue
		}
		if out == nil {
			out = make(map[string]ManifestEntry, len(found))
			for path, e := range found {
				out[path] = e
			}
		}
		file := want.Content
		for _, path := range append(links, target) {
			e := out[path]
			if path == file {
				e.Kind, e.Content = "file", found[target].Content
			} else {
				e.Kind, e.Content = "hardlink", file
			}
			out[path] = e
		}
	}
	if out == nil {
		return found
	}
	return out
}

// WritableArchive is one archive from SplitArchives, ready to be extracted into the
// folder that holds the volume's mount point. Its entries already sit under the
// folder's own name, so the folder's entry gives the volume's root the backup's owner
// and mode. The root is the volume's mount point, so only a folder entry may name it:
// any other entry under that exact name is left out, and the read-back then refuses
// the restore (not a folder). Every other entry is copied field by field as
// SplitArchives wrote it.
func WritableArchive(archive []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	tr := tar.NewReader(bytes.NewReader(archive))
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fail("the backup's archive is damaged: %v", err)
		}
		if belowTop(hdr.Name) == "" && hdr.Typeflag != tar.TypeDir {
			continue
		}
		out := &tar.Header{
			Typeflag: hdr.Typeflag,
			Name:     hdr.Name,
			Linkname: hdr.Linkname,
			Size:     hdr.Size,
			Mode:     hdr.Mode & 0o777,
			Uid:      hdr.Uid,
			Gid:      hdr.Gid,
			ModTime:  hdr.ModTime,
			Format:   tar.FormatPAX,
		}
		if err := w.WriteHeader(out); err != nil {
			return nil, err
		}
		if hdr.Typeflag == tar.TypeReg {
			if _, err := io.Copy(w, tr); err != nil {
				return nil, fail("the backup's archive is damaged: %v", err)
			}
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ReadBundleFile reads a backup file, refusing one larger than MaxBundleBytes
// without reading it whole.
func ReadBundleFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("could not open the backup file: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxBundleBytes+1))
	if err != nil {
		return nil, fmt.Errorf("could not read the backup file: %w", err)
	}
	if len(data) > MaxBundleBytes {
		return nil, fail("this file is larger than any backup CashPilot makes")
	}
	return data, nil
}

// MetadataString is a string field of the header's metadata, or "".
func (h Header) MetadataString(key string) string {
	s, _ := h.Metadata[key].(string)
	return s
}

// FolderName is the last path component of a critical target: the name its
// archive's entries sit under (/var/lib/mysterium-node -> mysterium-node).
func FolderName(target string) string {
	trimmed := strings.TrimRight(target, "/")
	return trimmed[strings.LastIndex(trimmed, "/")+1:]
}
