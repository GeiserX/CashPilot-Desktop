package macvm

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Check is one thing this Mac needs before Install. Problem is "" when it is
// met, or one line for the user that says what to do.
type Check struct {
	ID      string `json:"id"`
	Problem string `json:"problem"`
}

// Preflight checks what Install needs, as the user, reading only. creating is
// whether the VM still has to be made (it needs the disk space then).
func Preflight(ctx context.Context, r Runner, tartHome string, creating bool) []Check {
	var out []Check
	add := func(id, problem string) { out = append(out, Check{ID: id, Problem: problem}) }

	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		add("apple-silicon", "The Secure VM needs a Mac with Apple silicon (M1 or newer).")
		return out
	}
	add("apple-silicon", "")

	ver, _ := r.Run(ctx, "/usr/bin/sw_vers", "-productVersion")
	if major := hostMajor(ver); major < MinHostMajor {
		add("macos", fmt.Sprintf("The Secure VM needs macOS %d or newer. This Mac runs macOS %s. Update macOS in System Settings, then try again.", MinHostMajor, strings.TrimSpace(ver)))
	} else {
		add("macos", "")
	}

	if total, err := totalRAM(ctx, r); err == nil && total < NeedRAM {
		add("memory", fmt.Sprintf("The Secure VM needs a Mac with at least %s of memory. This Mac has %s.", gib(NeedRAM), gib(total)))
	} else {
		add("memory", "")
	}

	if creating {
		free, err := FreeBytes(existingParent(tartHome))
		switch {
		case err != nil:
			add("disk", "")
		case free < NeedFreeDisk:
			add("disk", fmt.Sprintf("The Secure VM needs %s of free disk space and this Mac has %s. Free some space, then try again.", gib(NeedFreeDisk), gib(free)))
		default:
			add("disk", "")
		}
	}

	// Our own VM, when it runs (a repair or an update), is not another guest.
	if n := RunningGuests(ctx, r) - ownGuest(ctx, r); n >= 2 {
		add("guests", fmt.Sprintf("macOS lets a Mac run at most 2 macOS virtual machines at once, and %d run now. Stop one in the app that runs it, then try again.", n))
	} else {
		add("guests", "")
	}
	return out
}

// Problems is the checks that are not met.
func Problems(cs []Check) []string {
	var out []string
	for _, c := range cs {
		if c.Problem != "" {
			out = append(out, c.Problem)
		}
	}
	return out
}

func hostMajor(ver string) int {
	f := strings.SplitN(strings.TrimSpace(ver), ".", 2)
	n, _ := strconv.Atoi(f[0])
	return n
}

func gib(n int64) string { return fmt.Sprintf("%.0f GB", float64(n)/float64(1<<30)) }

// existingParent is the nearest folder of p that exists, so the free space of
// a Tart folder that is not made yet is read on its volume.
func existingParent(p string) string {
	for p != "/" && p != "." {
		if _, err := os.Stat(p); err == nil {
			return p
		}
		p = filepath.Dir(p)
	}
	return "/"
}

// totalRAM is the Mac's memory.
func totalRAM(ctx context.Context, r Runner) (int64, error) {
	out, err := r.Run(ctx, "/usr/sbin/sysctl", "-n", "hw.memsize")
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(out), 10, 64)
}

// RunningGuests counts the macOS virtual machines running on this Mac, from
// any app. Each macOS guest has its own Apple platform identity process; a
// Linux VM (Docker Desktop, Lima) has none, so it does not count.
func RunningGuests(ctx context.Context, r Runner) int {
	out, err := r.Run(ctx, "/bin/ps", "-axww", "-o", "comm=")
	if err != nil {
		return 0
	}
	return countGuests(out)
}

// ownGuest is 1 when CashPilot's own VM runs.
func ownGuest(ctx context.Context, r Runner) int {
	out, err := r.Run(ctx, "/bin/ps", "-axww", "-o", "args=")
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(out, "\n") {
		if _, rest, ok := strings.Cut(l, "/tart run "+VMName); ok && (rest == "" || rest[0] == ' ') {
			return 1
		}
	}
	return 0
}

func countGuests(ps string) int {
	n := 0
	for _, l := range strings.Split(ps, "\n") {
		if strings.HasSuffix(strings.TrimSpace(l), "/com.apple.Virtualization.AppleVirtualPlatformIdentity") {
			n++
		}
	}
	return n
}
