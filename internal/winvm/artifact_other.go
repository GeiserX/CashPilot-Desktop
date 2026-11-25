//go:build !windows

package winvm

import (
	"fmt"
	"os"
	"slices"
	"syscall"
)

func artifactID(path string) (string, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() && !fi.Mode().IsRegular() {
		return "", untrackedFile(path)
	}
	st := fi.Sys().(*syscall.Stat_t)
	return fmt.Sprintf("%x:%x", st.Dev, st.Ino), nil
}

func deleteArtifact(path string, ids []string) error {
	id, err := artifactID(path)
	if err != nil {
		return err
	}
	if !slices.Contains(ids, id) {
		return untrackedFile(path)
	}
	return os.Remove(path)
}
