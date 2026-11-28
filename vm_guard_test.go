package main

import (
	"bytes"
	"context"
	"flag"
	"io"
	"log"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/e2e/harness/hvfake"
	"github.com/GeiserX/CashPilot-Desktop/internal/winvm"
)

// The guard takes only the product's data folder or a lab instance's.
func TestGuardDir(t *testing.T) {
	for _, ok := range []string{`C:\ProgramData\CashPilot`, `c:\programdata\cashpilot`, `C:\ProgramData\CashPilot\lab\cpd-svm-1`, `C:\ProgramData\CashPilot\lab\cpd-svm-9`} {
		if _, err := guardDir(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{``, `C:\Users\Public`, `C:\ProgramData\CashPilot\lab\cpd-svm-10`, `C:\ProgramData\CashPilot\..\Evil`, `/tmp/x`} {
		if _, err := guardDir(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func guardFixture(t *testing.T) (*hvfake.HyperV, winvm.GuardDeps, *bytes.Buffer) {
	t.Helper()
	f := hvfake.New()
	p, err := f.BuildFenced(true)
	if err != nil {
		t.Fatal(err)
	}
	p.ProvedAt = time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	b, _ := p.Marshal()
	os.WriteFile(filepath.Join(dir, winvm.PlanFile), b, 0o600)
	os.WriteFile(filepath.Join(dir, winvm.PrefsFile), []byte(`{"approvedNetworks":[{"gatewayMac":"00-00-5E-00-53-01","profileId":"{6A5D2C1E-0B7F-4C3A-9E21-5F8D7C6B4A30}"}]}`), 0o644)
	var buf bytes.Buffer
	logger := log.New(&buf, "", 0)
	d := winvm.GuardDeps{
		HV:   f,
		Echo: func(context.Context) []netip.Addr { return []netip.Addr{f.PublicIP} },
		Now:  func() time.Time { return p.ProvedAt.Add(time.Hour) },
		Dir:  dir,
		Logf: logger.Printf,
	}
	return f, d, &buf
}

// --once boot connects a matching VM, --once pass leaves it, each writes the
// status, and a bad --once value is refused before anything runs.
func TestRunVMGuardOnce(t *testing.T) {
	f, d, buf := guardFixture(t)
	logger := log.New(buf, "", 0)
	if code := runVMGuard(context.Background(), d, "boot", logger); code != 0 {
		t.Fatalf("boot exit %d: %s", code, buf)
	}
	st, err := winvm.ReadStatus(filepath.Join(d.Dir, winvm.StatusFile))
	if err != nil || st.Network != winvm.NetConnected || st.Fence != winvm.FenceHeld {
		t.Fatalf("after boot: %+v %v\n%s", st, err, buf)
	}
	n := len(f.Calls())
	if code := runVMGuard(context.Background(), d, "pass", logger); code != 0 {
		t.Fatalf("pass exit %d", code)
	}
	if calls := f.Calls()[n:]; slices.Contains(calls, "Connect") || slices.Contains(calls, "Disconnect") {
		t.Fatalf("a clean pass changed the network: %v", calls)
	}
	if !strings.Contains(buf.String(), "pass: network=connected fence=held") {
		t.Fatalf("log:\n%s", buf)
	}
	n = len(f.Calls())
	if code := runVMGuard(context.Background(), d, "connect", logger); code != 2 || len(f.Calls()) != n {
		t.Fatalf("a bad --once: exit %d, calls %v", code, f.Calls()[n:])
	}
}

// Without --once the guard boots and loops until it is stopped.
func TestRunVMGuardLoopStops(t *testing.T) {
	f, d, buf := guardFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	go func() { done <- runVMGuard(ctx, d, "", log.New(&bytes.Buffer{}, "", 0)) }()
	for deadline := time.Now().Add(5 * time.Second); !slices.Contains(f.Calls(), "Usage"); time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("the loop did not boot")
		}
	}
	cancel()
	if code := <-done; code != 0 {
		t.Fatalf("exit %d: %s", code, buf)
	}
}

// Off Windows the role refuses at once.
func TestRunVMGuardMainOtherOS(t *testing.T) {
	if filepath.Separator == '\\' {
		t.Skip("Windows runs the guard")
	}
	if code := runVMGuardMain(`C:\ProgramData\CashPilot`, "pass"); code != 2 {
		t.Fatalf("exit %d", code)
	}
}

// The log stays near its cap with one older copy, however long the loop runs.
func TestGuardLogRotates(t *testing.T) {
	dir := t.TempDir()
	r := &rotatingLog{path: filepath.Join(dir, guardLogFile), max: 100}
	line := []byte(strings.Repeat("x", 39) + "\n")
	for range 10 {
		r.Write(line)
	}
	cur, err := os.Stat(r.path)
	if err != nil || cur.Size() > 100 {
		t.Fatalf("guard.log: %v %v", cur, err)
	}
	old, err := os.Stat(r.path + ".1")
	if err != nil || old.Size() > 100 {
		t.Fatalf("guard.log.1: %v %v", old, err)
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) != 3 {
		t.Fatalf("the folder holds %d files, want two logs and the file journal", len(ents))
	}
}

// A guard command line that could not be read is refused before anything
// runs: "--vm-guard <dir> --once" with no value must not start the loop.
func TestVMGuardMainRefusesABadCommandLine(t *testing.T) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	guard := fs.String(vmGuardFlag, "", "")
	once := fs.String(vmGuardOnceFlag, "", "")
	parseErr := fs.Parse([]string{"--" + vmGuardFlag, `C:\ProgramData\CashPilot`, "--" + vmGuardOnceFlag})
	if parseErr == nil || *guard == "" {
		t.Fatalf("the command line parsed: guard %q, err %v", *guard, parseErr)
	}
	ran := false
	run := func(string, string) int { ran = true; return 0 }
	if code := vmGuardMain(*guard, *once, parseErr, run); code != 2 || ran {
		t.Fatalf("exit %d, ran %v", code, ran)
	}
	if code := vmGuardMain(*guard, "pass", nil, run); code != 0 || !ran {
		t.Fatalf("a good command line: exit %d, ran %v", code, ran)
	}
}

// When the move to guard.log.1 fails, a line that would push the log past its
// cap is dropped, so the log stays bounded.
func TestGuardLogStaysBoundedWhenTheMoveFails(t *testing.T) {
	dir := t.TempDir()
	r := &rotatingLog{path: filepath.Join(dir, guardLogFile), max: 100}
	// A folder in guard.log.1's place makes every move fail.
	if err := os.MkdirAll(filepath.Join(r.path+".1", "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	line := []byte(strings.Repeat("x", 39) + "\n")
	for range 10 {
		r.Write(line)
	}
	if cur, err := os.Stat(r.path); err != nil || cur.Size() > 100 {
		t.Fatalf("guard.log: %v %v", cur, err)
	}
}
