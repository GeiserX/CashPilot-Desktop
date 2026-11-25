//go:build !windows

package winvm

import "context"

// Hyper-V mutation is only supported on Windows.
func lockMutation(context.Context) (func(), error) {
	return func() {}, nil
}
