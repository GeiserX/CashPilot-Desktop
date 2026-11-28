package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/winvm"
)

// The Secure VM's elevated roles. The admin bootstraps (setup.ps1,
// remove.ps1) run the admin-only copy of this exe with one of them; all the
// work is winvm.Build and winvm.Remove. Neither opens a window.
const (
	vmSetupFlag  = "vm-setup"
	vmRemoveFlag = "vm-remove"
)

// Exit codes of the two roles. 3010 is never used: nothing here needs a
// restart.
const (
	vmExitOK          = 0
	vmExitFailed      = 1
	vmExitUsage       = 2
	vmExitProofFailed = 3 // built, but the proof was not green: the guard keeps the VM off
)

// vmSetupTimeout bounds a whole setup: Windows Setup alone can take an hour.
const vmSetupTimeout = 4 * time.Hour

// maxParamsFile caps the params file.
const maxParamsFile = 64 << 10

// setupLogFile is setup's log in the data folder, readable by users so the
// app can show progress. It never holds a password: Build logs step names.
const setupLogFile = "setup.log"

// removeLogFile keeps what a Remove that did not finish removed, kept and
// left, and why. The bootstrap starts Remove with no standard error, so
// without it the reason is lost. A finished Remove writes none.
const removeLogFile = "remove.log"

// vmSetupMain is main's --vm-setup / --vm-remove branch.
func vmSetupMain(params, dir string, parseErr error) int {
	if parseErr != nil || (params != "") == (dir != "") {
		fmt.Fprintln(os.Stderr, "vm-setup: give exactly one of --vm-setup <params.json> or --vm-remove <data folder>")
		return vmExitUsage
	}
	if runtime.GOOS != "windows" {
		fmt.Fprintln(os.Stderr, "vm-setup:", winvm.ErrUnsupported)
		return vmExitUsage
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, "vm-setup:", err)
		return vmExitFailed
	}
	if params != "" {
		ctx, cancel := context.WithTimeout(ctx, vmSetupTimeout)
		defer cancel()
		return runVMSetup(ctx, params, exe, productBuildDeps, os.Stderr)
	}
	n, err := namesForDir(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "vm-remove:", err)
		return vmExitUsage
	}
	if err := winvm.CheckCreatedDirectory(n.DataDir); err != nil {
		fmt.Fprintln(os.Stderr, "vm-remove:", err)
		return vmExitUsage
	}
	return runVMRemove(ctx, dir, func(n winvm.Names) winvm.RemoveDeps {
		return winvm.RemoveDeps{HV: winvm.NewBuilder(n)}
	}, os.Stderr)
}

// productBuildDeps is the real Hyper-V, the proof's Prober and the echo
// services. The guest probe is the PowerShell Direct one Build picks.
func productBuildDeps(n winvm.Names) winvm.BuildDeps {
	return winvm.BuildDeps{
		HV:    winvm.NewBuilder(n),
		Probe: winvm.NewProber(n),
		Echo:  winvm.DefaultEcho(),
		Now:   time.Now,
	}
}

// readParamsFile reads a regular file of at most maxParamsFile bytes.
func readParamsFile(path string) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, maxParamsFile+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxParamsFile {
		return nil, fmt.Errorf("the params file is over %d bytes", maxParamsFile)
	}
	return b, nil
}

// runVMSetup builds or repairs the instance the params file names. It must
// run as that instance's guard copy, so the guard task it registers points at
// an exe only an admin can change. The params file holds the guest passwords
// and the key: it must be setup.ps1's copy in the guard folder, and it is
// deleted once setup ends.
func runVMSetup(ctx context.Context, params, exe string, mk func(winvm.Names) winvm.BuildDeps, errOut io.Writer) int {
	n, err := namesForDir(filepath.Dir(filepath.Dir(exe)))
	if err != nil || !strings.EqualFold(filepath.Clean(exe), n.GuardExe()) {
		fmt.Fprintln(errOut, "vm-setup: run the guard copy from the Secure VM data folder")
		return vmExitUsage
	}
	return runVMSetupFor(ctx, params, n, n.DataDir, func(paths ...string) error {
		if err := winvm.CheckCreatedDirectory(n.DataDir); err != nil {
			return err
		}
		return winvm.CheckSetupAccess(paths...)
	}, mk, errOut)
}

var setupParamsName = regexp.MustCompile(`^\.setup-params\.[0-9a-f]{32}\.json$`)

func runVMSetupFor(ctx context.Context, params string, n winvm.Names, dir string, checkAccess func(...string) error, mk func(winvm.Names) winvm.BuildDeps, errOut io.Writer) int {
	guard := filepath.Join(dir, "guard")
	expected := filepath.Clean(params)
	if !strings.EqualFold(filepath.Dir(expected), guard) || !setupParamsName.MatchString(filepath.Base(expected)) {
		fmt.Fprintln(errOut, "vm-setup: the params file must be the bootstrap's temporary copy in", guard)
		return vmExitUsage
	}
	resolved, err := filepath.EvalSymlinks(expected)
	if err != nil || !strings.EqualFold(resolved, expected) {
		fmt.Fprintln(errOut, "vm-setup: the params path must exist without links or junctions")
		return vmExitUsage
	}
	if checkAccess != nil {
		if err := checkAccess(dir, guard, expected); err != nil {
			fmt.Fprintln(errOut, "vm-setup:", err)
			return vmExitUsage
		}
		if err := winvm.CheckJournaledFile(dir, expected); err != nil {
			fmt.Fprintln(errOut, "vm-setup:", err)
			return vmExitUsage
		}
	}
	// Read the same canonical path that was checked above.
	params = expected
	raw, err := readParamsFile(params)
	if err != nil {
		fmt.Fprintln(errOut, "vm-setup:", err)
		return vmExitUsage
	}
	sp, err := winvm.ParseSetupParams(raw)
	if err != nil {
		fmt.Fprintln(errOut, "vm-setup:", err)
		return vmExitUsage
	}
	names, _, _, err := winvm.SetupNames(sp)
	if err != nil || names != n {
		fmt.Fprintln(errOut, "vm-setup: the params do not identify this Secure VM instance")
		return vmExitUsage
	}
	d := mk(n)
	d.Dir = dir
	logger := log.New(errOut, "", log.LstdFlags)
	if f, err := winvm.OpenAppend(filepath.Join(d.Dir, setupLogFile)); err == nil {
		defer f.Close()
		// setup.ps1 starts this GUI-subsystem exe hidden, with no standard
		// error, so writes to errOut fail. io.MultiWriter stops at the first
		// failing writer: the log goes first and errOut never stops it.
		logger = log.New(io.MultiWriter(f, bestEffort{errOut}), "", log.LstdFlags)
	}
	d.Exe, d.Logf = n.GuardExe(), logger.Printf
	logger.Printf("VM storage volume %s (private directory under %s, 128 GiB dynamic disk)", filepath.VolumeName(dir), dir)
	res, err := winvm.Build(ctx, d, sp)
	switch {
	case errors.Is(err, winvm.ErrProofFailed):
		logger.Printf("setup: built, but the proof did not pass: %v", err)
		return vmExitProofFailed
	case err != nil:
		logger.Printf("setup failed: %v", err)
		return vmExitFailed
	}
	logger.Printf("setup done: network=%s fence=%s", res.Status.Network, res.Status.Fence)
	return vmExitOK
}

// bestEffort writes to w and reports success even when w fails, so a missing
// standard error never stops the writers after it.
type bestEffort struct{ w io.Writer }

func (b bestEffort) Write(p []byte) (int, error) {
	_, _ = b.w.Write(p)
	return len(p), nil
}

// namesForDir is the instance whose data folder dir is: the product's or a
// lab instance's, nothing else.
func namesForDir(dir string) (winvm.Names, error) {
	d, err := guardDir(dir)
	if err != nil {
		return winvm.Names{}, err
	}
	if d == winvm.ProductNames().DataDir {
		return winvm.ProductNames(), nil
	}
	for i := 1; i <= winvm.MaxLabInstance; i++ {
		if n, _ := winvm.LabNames(i); n.DataDir == d {
			return n, nil
		}
	}
	return winvm.Names{}, fmt.Errorf("%q is not the Secure VM's data folder", dir)
}

// runVMRemove removes the instance of the data folder dir and prints what it
// removed, kept and left.
func runVMRemove(ctx context.Context, dir string, mk func(winvm.Names) winvm.RemoveDeps, errOut io.Writer) int {
	n, err := namesForDir(dir)
	if err != nil {
		fmt.Fprintln(errOut, "vm-remove:", err)
		return vmExitUsage
	}
	d := mk(n)
	if d.Dir == "" {
		d.Dir = n.DataDir
	}
	var report bytes.Buffer
	d.Logf = log.New(io.MultiWriter(&report, bestEffort{errOut}), "", log.LstdFlags).Printf
	// Remove calls Finish under its lock and only while desired.json remains,
	// so only a Remove that did not finish writes its log.
	d.Finish = func(res winvm.RemoveResult, err error) {
		f, ferr := winvm.AppendLog(filepath.Join(d.Dir, removeLogFile))
		if ferr != nil {
			return
		}
		fmt.Fprintf(f, "%s remove:\n", time.Now().Format("2006/01/02 15:04:05"))
		_, _ = f.Write(report.Bytes())
		writeRemoveResult(f, res, err)
		_ = f.Close()
	}
	res, err := winvm.Remove(ctx, d, n)
	writeRemoveResult(bestEffort{errOut}, res, err)
	if err != nil {
		return vmExitFailed
	}
	return vmExitOK
}

// writeRemoveResult prints what Remove removed, kept and left, and its error.
func writeRemoveResult(w io.Writer, res winvm.RemoveResult, err error) {
	for _, x := range res.Removed {
		fmt.Fprintln(w, "removed:", x)
	}
	for _, x := range res.Kept {
		fmt.Fprintln(w, "kept:", x)
	}
	for _, x := range res.Left {
		fmt.Fprintln(w, "left:", x)
	}
	if err != nil {
		fmt.Fprintln(w, "vm-remove:", err)
	}
}
