//go:build !windows

package setupresume

// Arm does nothing outside Windows: only Windows has RunOnce.
func Arm(exe string) error { return nil }

// Disarm does nothing outside Windows.
func Disarm() error { return nil }
