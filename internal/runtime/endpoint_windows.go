//go:build windows

package runtime

import (
	"errors"

	"golang.org/x/sys/windows"
)

// probePipe opens the engine's named pipe and closes it at once. A missing pipe is
// ERROR_FILE_NOT_FOUND, a pipe this account may not open is ERROR_ACCESS_DENIED,
// and a pipe whose every instance is busy still exists.
func probePipe(path string) endpointState {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return endpointUnknown
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, 0, 0)
	if err == nil {
		windows.CloseHandle(h)
		return endpointPresent
	}
	if errors.Is(err, windows.ERROR_PIPE_BUSY) {
		return endpointPresent
	}
	return dialState(err)
}
