//go:build windows

package winvm

import (
	"context"
	"crypto/sha256"
	"fmt"
	"runtime"
	"strings"

	"golang.org/x/sys/windows"
)

// A setup and a removal must not change the same journal concurrently.
// Windows mutex ownership belongs to the thread that acquired it.
func lockMutation(ctx context.Context, n Names) (func(), error) {
	sum := sha256.Sum256([]byte(strings.ToLower(n.DataDir)))
	name := windows.StringToUTF16Ptr(fmt.Sprintf("Global\\CashPilotVM-%x", sum[:16]))
	h, err := windows.CreateMutex(nil, false, name)
	if err != nil && err != windows.ERROR_ALREADY_EXISTS {
		return nil, err
	}
	runtime.LockOSThread()
	for ctx.Err() == nil {
		result, err := windows.WaitForSingleObject(h, 250)
		if err != nil {
			windows.CloseHandle(h)
			runtime.UnlockOSThread()
			return nil, err
		}
		if result == windows.WAIT_OBJECT_0 || result == windows.WAIT_ABANDONED {
			return func() {
				windows.ReleaseMutex(h)
				windows.CloseHandle(h)
				runtime.UnlockOSThread()
			}, nil
		}
	}
	windows.CloseHandle(h)
	runtime.UnlockOSThread()
	return nil, ctx.Err()
}
