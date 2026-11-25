//go:build windows

package winvm

import (
	"context"
	"runtime"

	"golang.org/x/sys/windows"
)

// Serialize Setup and Remove across instances because WinNAT is host-wide.
// Windows mutex ownership belongs to the thread that acquired it.
func lockMutation(ctx context.Context) (func(), error) {
	name := windows.StringToUTF16Ptr("Global\\CashPilotVM-Mutation")
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
