package macvm

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Runner runs a program and returns its standard output. An error carries the
// program's standard error.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) (string, error)
}

// Exec is the real Runner.
type Exec struct{}

// Run runs name with args.
func (Exec) Run(ctx context.Context, name string, args ...string) (string, error) {
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout, cmd.Stderr = &out, &errb
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = strings.TrimSpace(out.String())
		}
		if len(msg) > 2000 {
			msg = msg[len(msg)-2000:]
		}
		return out.String(), fmt.Errorf("%s %s: %w: %s", name, strings.Join(redactArgs(args), " "), err, msg)
	}
	return out.String(), nil
}

// redactArgs keeps a long script out of an error message.
func redactArgs(args []string) []string {
	out := make([]string, len(args))
	for i, a := range args {
		if len(a) > 120 {
			a = a[:60] + "…"
		}
		out[i] = a
	}
	return out
}

// ownerEnv is the environment Tart runs with: softnet must be on PATH, and
// nothing else of root's environment leaks in.
func ownerEnv(inst Install, bin string) []string {
	return []string{
		"PATH=" + bin + ":/usr/bin:/bin:/usr/sbin:/sbin",
		"HOME=" + inst.OwnerHome,
		"TART_HOME=" + inst.TartHome,
		"USER=" + inst.Owner,
		"LOGNAME=" + inst.Owner,
	}
}

// AsOwnerArgs is the command that runs args as the owner, inside the owner's
// launchd domain. A system daemon sits outside it, and Virtualization
// framework's XPC services live there: without `launchctl asuser`, Tart
// starts, prints nothing and never brings the VM up (lab, M5).
func AsOwnerArgs(inst Install, bin string, args ...string) []string {
	a := []string{"asuser", strconv.Itoa(inst.OwnerUID), "/usr/bin/sudo", "-u", inst.Owner, "-H", "/usr/bin/env", "-i"}
	a = append(a, ownerEnv(inst, bin)...)
	return append(a, args...)
}

// asOwner runs args as the owner and waits.
func asOwner(ctx context.Context, r Runner, inst Install, bin string, args ...string) (string, error) {
	return r.Run(ctx, "/bin/launchctl", AsOwnerArgs(inst, bin, args...)...)
}

// startDetached starts args as the owner in its own process group, with its
// output in logPath, and returns at once. Tart must outlive the daemon's own
// restarts, so the plist also sets AbandonProcessGroup.
func startDetached(inst Install, bin, logPath string, args ...string) (*exec.Cmd, error) {
	log, err := openLog(logPath, 0o600)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command("/bin/launchctl", AsOwnerArgs(inst, bin, args...)...)
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = ownGroup()
	if err := cmd.Start(); err != nil {
		log.Close()
		return nil, err
	}
	go func() { _ = cmd.Wait(); log.Close() }()
	return cmd, nil
}
