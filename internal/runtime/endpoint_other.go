//go:build !windows

package runtime

// probePipe: named pipes exist only on Windows.
func probePipe(string) endpointState { return endpointUnknown }
