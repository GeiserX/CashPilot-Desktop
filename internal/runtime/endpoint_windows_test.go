//go:build windows

package runtime

import (
	"context"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// pipeServer creates one instance of a named pipe with the given SDDL, or the
// default security when sddl is "".
func pipeServer(t *testing.T, name, sddl string) {
	t.Helper()
	var sa *windows.SecurityAttributes
	if sddl != "" {
		sd, err := windows.SecurityDescriptorFromString(sddl)
		if err != nil {
			t.Fatal(err)
		}
		sa = &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	}
	p, err := windows.UTF16PtrFromString(`\\.\pipe\` + name)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateNamedPipe(p, windows.PIPE_ACCESS_DUPLEX, windows.PIPE_TYPE_BYTE, 1, 512, 512, 0, sa)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { windows.CloseHandle(h) })
}

// The probe reads the pipe's state from error numbers, on every language of
// Windows: missing, open to this account, and closed to it.
func TestProbeEndpointPipeStates(t *testing.T) {
	ctx := context.Background()
	if got := probeEndpoint(ctx, "npipe:////./pipe/cashpilot_no_such_pipe_for_tests"); got != endpointAbsent {
		t.Errorf("a missing pipe: %v, want absent", got)
	}

	pipeServer(t, "cashpilot_open_pipe_for_tests", "")
	if got := probeEndpoint(ctx, "npipe:////./pipe/cashpilot_open_pipe_for_tests"); got != endpointPresent {
		t.Errorf("a pipe this account may open: %v, want present", got)
	}

	// A protected DACL with no entries grants nobody read or write.
	pipeServer(t, "cashpilot_closed_pipe_for_tests", "D:P")
	if got := probeEndpoint(ctx, "npipe:////./pipe/cashpilot_closed_pipe_for_tests"); got != endpointDenied {
		t.Errorf("a pipe this account may not open: %v, want denied", got)
	}
}
