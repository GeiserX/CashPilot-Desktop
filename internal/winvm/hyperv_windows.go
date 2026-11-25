//go:build windows

package winvm

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
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
		hv:      openHVListener,
		tmp:     filepath.Join(n.DataDir, proofDir),
	}
}

// runPowerShell runs one fixed script in a hidden Windows PowerShell 5.1, found
// by full path so PATH cannot point elsewhere. It reads the fixed script
// from stdin to keep its size out of the Windows command line. The argument
// travels in the environment as base64 JSON.
func runPowerShell(ctx context.Context, script string, arg []byte) ([]byte, error) {
	cmd, err := hostPowerShellCommand(ctx, "-EncodedCommand", psEncode(hostScriptLauncher))
	if err != nil {
		return nil, err
	}
	cmd.Stdin = strings.NewReader(base64.StdEncoding.EncodeToString([]byte(script)))
	cmd.Env = append(cmd.Env, hvArg(arg))
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

func hostPowerShellCommand(ctx context.Context, args ...string) (*exec.Cmd, error) {
	exe, err := runtime.PowerShellExe()
	if err != nil {
		return nil, err
	}
	cmd := hidden(ctx, exe, append([]string{"-NoProfile", "-NonInteractive"}, args...)...)
	// A parent pwsh can leave PowerShell 7 modules on the inherited path.
	modules := filepath.Join(os.Getenv("ProgramFiles"), "WindowsPowerShell", "Modules") + ";" +
		filepath.Join(filepath.Dir(exe), "Modules")
	cmd.Env = append(cmd.Environ(), "PSModulePath="+modules)
	return cmd, nil
}
