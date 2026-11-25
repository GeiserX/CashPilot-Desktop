//go:build !windows

package winvm

import "os"

// protect sets the mode that stands for the access rule. The guard runs only
// on Windows; this keeps the files sane where the tests run.
func protect(path string, a fileAccess) error {
	mode := os.FileMode(0o600)
	if a == usersRead {
		mode = 0o644
	}
	return os.Chmod(path, mode)
}

// ownedByAdmin has no owner rule to check outside Windows.
func ownedByAdmin(*os.File) error { return nil }
