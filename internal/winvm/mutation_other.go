//go:build !windows

package winvm

import "context"

// Hyper-V mutation is only supported on Windows.
func lockMutation(context.Context, Names) (func(), error) {
	return func() {}, nil
}
