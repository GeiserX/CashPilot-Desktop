package macvm

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// openLog opens path for a fresh log: truncated, mode perm.
func openLog(path string, perm os.FileMode) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC|oNoFollow, perm)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// appendLog opens path for appending.
func appendLog(path string, perm os.FileMode) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND|oNoFollow, perm)
}

// FreeBytes is the free space for a non-root user on the volume of path.
func FreeBytes(path string) (int64, error) { return freeBytes(path) }

func (d *Daemon) freeDisk() int64 {
	inst, err := d.loadInstall()
	if err != nil {
		return 0
	}
	n, err := FreeBytes(inst.TartHome)
	if err != nil {
		return 0
	}
	return n
}

// errLink refuses a path that runs through a link, so a root write never
// follows one out of the folder it was aimed at.
var errLink = errors.New("refused: the path runs through a link or a non-folder")

// SafeDir makes rel (slash separated) under root, owned root:wheel mode 755,
// refusing any existing component that is not a real folder.
func SafeDir(root, rel string) (string, error) {
	cur := root
	if fi, err := os.Lstat(cur); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("%s: %w", cur, errLink)
	}
	for _, part := range strings.Split(rel, "/") {
		if part == "" || part == "." || part == ".." {
			if part == ".." {
				return "", errLink
			}
			continue
		}
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		switch {
		case errors.Is(err, os.ErrNotExist):
			if err := os.Mkdir(cur, 0o755); err != nil {
				return "", err
			}
			if err := os.Lchown(cur, 0, 0); err != nil {
				return "", err
			}
		case err != nil:
			return "", err
		case fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir():
			return "", fmt.Errorf("%s: %w", cur, errLink)
		}
	}
	return cur, nil
}

// SafeWrite writes b to root/rel as root:wheel with mode perm. Every folder on
// the way must be a real folder, and the file itself is opened without
// following a link, replaced if it exists.
func SafeWrite(root, rel string, b []byte, perm os.FileMode) error {
	dir, err := SafeDir(root, filepath.Dir(rel))
	if err != nil {
		return err
	}
	path := filepath.Join(dir, filepath.Base(rel))
	if fi, err := os.Lstat(path); err == nil {
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("%s: %w", path, errLink)
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY|oNoFollow, perm)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Chown(0, 0); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// copyFile copies src to dst (a new file, mode perm) and returns the bytes.
func copyFile(src, dst string, perm os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if fi, err := in.Stat(); err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", src)
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY|oNoFollow, perm)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
