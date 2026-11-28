//go:build windows

package winvm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
)

// NewHost is the real Hyper-V, read and guarded through PowerShell, with the
// drop counter's PDH fallback.
func NewHost(dir string) Host {
	return windowsHost(dir)
}

func windowsHost(dir string) *psHost {
	return &psHost{run: func(ctx context.Context, script string, arg []byte) ([]byte, error) {
		return runPowerShell(ctx, dir, script, arg)
	}, pdh: pdhDropInstances, sleep: sleepCtx}
}

// NewBuilder is the real Hyper-V for setup, Repair and Remove VM.
func NewBuilder(n Names) Builder {
	return psBuilder{windowsHost(n.DataDir)}
}

// NewProber is the proof's Prober for instance n. Its canaries let in only
// this exe, and pktmon's capture files go under the instance's data folder.
func NewProber(n Names) Prober {
	exe, _ := os.Executable()
	return &psProber{
		h:       windowsHost(n.DataDir),
		n:       n,
		program: exe,
		hv:      openHVListener,
		tmp:     filepath.Join(n.DataDir, proofDir),
	}
}

// runPowerShell runs fixed code from a verified file. Its JSON argument travels
// in the environment and is never part of the script or command line.
func runPowerShell(ctx context.Context, dir, script string, arg []byte) ([]byte, error) {
	cmd, release, err := filePowerShellCommand(ctx, dir, script)
	if err != nil {
		return nil, err
	}
	defer release()
	cmd.Env = append(cmd.Env, hvArg(arg))
	if script == scriptGuest {
		var request struct {
			Guest string
		}
		if err := json.Unmarshal(arg, &request); err != nil {
			return nil, err
		}
		guest, ok := guestScriptNamed(request.Guest)
		if !ok {
			return nil, fmt.Errorf("unknown guest script %q", request.Guest)
		}
		path, release, err := preparePowerShellFile(dir, guest.Text)
		if err != nil {
			return nil, err
		}
		defer release()
		cmd.Env = append(cmd.Env, "CPD_HV_GUEST_FILE="+path)
	}
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
