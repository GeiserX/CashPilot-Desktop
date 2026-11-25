//go:build windows

package winvm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// NewHost is the real Hyper-V, read and guarded through PowerShell, with the
// drop counter's PDH fallback.
func NewHost() Host {
	return &psHost{run: runPowerShell, pdh: pdhDropInstances, sleep: sleepCtx}
}

// NewBuilder is the real Hyper-V for setup, Repair and Remove VM.
func NewBuilder() Builder {
	return psBuilder{&psHost{run: runPowerShell, pdh: pdhDropInstances, sleep: sleepCtx}}
}

// NewProber is the proof's Prober for instance n. Its canaries let in only
// this exe, and pktmon's capture files go under the instance's data folder.
func NewProber(n Names) Prober {
	exe, _ := os.Executable()
	return &psProber{
		h:       &psHost{run: runPowerShell, pdh: pdhDropInstances, sleep: sleepCtx},
		n:       n,
		program: exe,
		hv: func(ctx context.Context, bindVMID, service string, register bool) (HVListener, error) {
			return openHVListener(ctx, bindVMID, service, register, ProofElementName(n))
		},
		tmp: filepath.Join(n.DataDir, proofDir),
	}
}

// runPowerShell runs one fixed script in a hidden Windows PowerShell 5.1, found
// by full path so PATH cannot point elsewhere. The script text goes in as
// -EncodedCommand and never changes; the argument goes in through the
// environment as base64 JSON. Output and error text are capped.
func runPowerShell(ctx context.Context, script string, arg []byte) ([]byte, error) {
	sys, err := windows.GetSystemDirectory()
	if err != nil {
		return nil, err
	}
	exe := filepath.Join(sys, "WindowsPowerShell", "v1.0", "powershell.exe")
	cmd := hidden(ctx, exe, "-NoProfile", "-NonInteractive", "-EncodedCommand", psEncode(script))
	cmd.Env = append(os.Environ(), hvArg(arg))
	stdout := &cappedBuffer{max: maxHVRaw}
	stderr := &cappedBuffer{max: 4 << 10}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("PowerShell: %w", ctx.Err())
		}
		return nil, fmt.Errorf("PowerShell: %v: %s", err, strings.TrimSpace(stderr.buf.String()))
	}
	if stdout.overflow {
		return nil, fmt.Errorf("the script printed more than %d bytes", maxHVRaw)
	}
	return decodeOutput(stdout.buf.Bytes())
}
