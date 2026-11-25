//go:build !windows

package winvm

import "os"

// protect sets the mode that stands for the access rule. The guard runs only
// on Windows; this keeps the files sane where the tests run.
func protect(path string, a fileAccess) error {
	mode := os.FileMode(0o600)
	switch a {
	case usersRead:
		mode = 0o644
	case usersWrite:
		mode = 0o666
	}
	return os.Chmod(path, mode)
}

// ownedByAdmin has no owner rule to check outside Windows.
func ownedByAdmin(*os.File) error { return nil }

// CheckSetupAccess is only used by the Windows setup role.
func CheckSetupAccess(...string) error { return ErrUnsupported }
