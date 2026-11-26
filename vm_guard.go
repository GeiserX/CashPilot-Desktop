package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/winvm"
)

// vmGuardFlag is the SYSTEM guard's role: --vm-guard <data folder>. The guard
// task runs the admin-only copy of the exe with it. It never opens a window.
const vmGuardFlag = "vm-guard"

// vmGuardOnceFlag runs one routine and exits, for the lab: --once boot or
// --once pass.
const vmGuardOnceFlag = "once"

// guardLogFile is the guard's log in its data folder, kept to about
// maxGuardLog bytes with one older copy.
const (
	guardLogFile = "guard.log"
	maxGuardLog  = 1 << 20
)

// vmGuardMain is main's --vm-guard branch; run is runVMGuardMain. Unlike a
// GUI launch, the guard refuses a command line it could not read: "--once"
// with no value would otherwise run the whole loop, Boot included. It returns
// the exit code.
func vmGuardMain(dir, once string, parseErr error, run func(dir, once string) int) int {
	if parseErr != nil {
		fmt.Fprintln(os.Stderr, "vm-guard:", parseErr)
		return 2
	}
	return run(dir, once)
}

// runVMGuardMain is the --vm-guard entry point. It returns the exit code.
func runVMGuardMain(dir, once string) int {
	if runtime.GOOS != "windows" {
		fmt.Fprintln(os.Stderr, "vm-guard:", winvm.ErrUnsupported)
		return 2
	}
	d, err := guardDir(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "vm-guard:", err)
		return 2
	}
	if err := winvm.CheckCreatedDirectory(d); err != nil {
		fmt.Fprintln(os.Stderr, "vm-guard:", err)
		return 2
	}
	logger := openGuardLog(d, once != "")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	deps := winvm.GuardDeps{
		HV:   winvm.NewHost(d),
		Echo: winvm.DefaultEcho(),
		Now:  time.Now,
		Dir:  d,
		Logf: logger.Printf,
	}
	return runVMGuard(ctx, deps, once, logger)
}

// runVMGuard runs the loop, or one routine with once. Exit codes: 0 when the
// routine ran (whatever it found), 2 for a bad --once value.
func runVMGuard(ctx context.Context, d winvm.GuardDeps, once string, logger *log.Logger) int {
	var st winvm.Status
	switch once {
	case "":
		logger.Printf("guard started")
		if err := d.Run(ctx); err != nil && ctx.Err() == nil {
			logger.Printf("guard stopped: %v", err)
			return 1
		}
		return 0
	case "boot":
		st = d.Boot(ctx)
	case "pass":
		st = d.Pass(ctx)
	default:
		logger.Printf("--once takes boot or pass, not %q", once)
		return 2
	}
	logger.Printf("%s: network=%s fence=%s reason=%q", once, st.Network, st.Fence, st.Reason)
	return 0
}

// guardDir accepts only the product's data folder or a lab instance's, so the
// task's argument cannot point the SYSTEM guard at a folder a user controls.
func guardDir(dir string) (string, error) {
	clean := filepath.Clean(dir)
	known := []winvm.Names{winvm.ProductNames()}
	for i := 1; i <= winvm.MaxLabInstance; i++ {
		n, _ := winvm.LabNames(i)
		known = append(known, n)
	}
	for _, n := range known {
		if strings.EqualFold(clean, n.DataDir) {
			return n.DataDir, nil
		}
	}
	return "", fmt.Errorf("%q is not the Secure VM's data folder", dir)
}

// openGuardLog logs to dir\guard.log, kept to about maxGuardLog bytes with
// one older copy, guard.log.1, even across a loop that runs for weeks. With
// echo set (a lab run by hand) it also writes to stderr.
func openGuardLog(dir string, echo bool) *log.Logger {
	var w io.Writer = &rotatingLog{path: filepath.Join(dir, guardLogFile), max: maxGuardLog}
	if echo {
		w = io.MultiWriter(w, os.Stderr)
	}
	return log.New(w, "", log.LstdFlags|log.LUTC)
}

// rotatingLog appends to path, and moves it to path.1 once it passes max. A
// log line that cannot be written is dropped: logging never stops the guard.
// So is a line that would push the log past max when the move fails, so the
// log stays bounded.
type rotatingLog struct {
	mu   sync.Mutex
	path string
	max  int64
}

func (r *rotatingLog) Write(b []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if fi, err := os.Stat(r.path); err == nil && fi.Size()+int64(len(b)) > r.max {
		if err := winvm.RotateJournaledLog(r.path); err != nil {
			return len(b), nil
		}
	}
	f, err := winvm.OpenAppend(r.path)
	if err != nil {
		return len(b), nil
	}
	defer f.Close()
	f.Write(b)
	return len(b), nil
}
