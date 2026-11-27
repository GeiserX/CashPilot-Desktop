package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/e2e/harness/hvfake"
	"github.com/GeiserX/CashPilot-Desktop/internal/winvm"
)

func writeParams(t *testing.T, edit func(*winvm.SetupParams)) string {
	return writeParamsIn(t, t.TempDir(), edit)
}

// writeParamsIn writes a params file into dir, as setup.ps1 writes its copy
// into the guard folder.
func writeParamsIn(t *testing.T, dir string, edit func(*winvm.SetupParams)) string {
	t.Helper()
	sp := winvm.SetupParams{
		Version: winvm.SetupParamsVersion, ISO: `D:\iso\windows.iso`, ImageIndex: 6,
		AdminPassword: testPassword(1), EarnerPassword: testPassword(2),
		UserSID: "S-1-5-21-1-2-3-1001", ApproveNetwork: true,
	}
	if edit != nil {
		edit(&sp)
	}
	b, _ := json.Marshal(sp)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, ".setup-params."+strings.Repeat("a", 32)+".json")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunVMSetupBuildsWithBootstrapParams(t *testing.T) {
	f := hvfake.New()
	dir := canonicalTempDir(t)
	mk := func(winvm.Names) winvm.BuildDeps {
		return winvm.BuildDeps{HV: f, Probe: f, Guest: f, Dir: dir,
			Echo:  func(context.Context) []netip.Addr { return []netip.Addr{f.PublicIP} },
			Sleep: func(ctx context.Context, _ time.Duration) error { return ctx.Err() }, ProofWait: time.Millisecond}
	}
	params := writeParamsIn(t, filepath.Join(dir, "guard"), nil)
	var out bytes.Buffer
	if code := runVMSetupFor(context.Background(), params, winvm.ProductNames(), dir, nil, mk, &out); code != vmExitOK {
		t.Fatalf("exit %d:\n%s", code, out.String())
	}
	if _, err := os.Stat(params); err != nil {
		t.Fatal("the setup role changed the bootstrap's params file")
	}
	log, _ := os.ReadFile(filepath.Join(dir, setupLogFile))
	if strings.Contains(string(log), testPassword(1)) || strings.Contains(out.String(), testPassword(1)) {
		t.Fatal("a password reached the log")
	}
	var rout bytes.Buffer
	code := runVMRemove(context.Background(), winvm.ProductNames().DataDir, func(winvm.Names) winvm.RemoveDeps {
		return winvm.RemoveDeps{HV: f, Dir: dir}
	}, &rout)
	if code != vmExitOK || !strings.Contains(rout.String(), "removed: the VM") {
		t.Fatalf("remove exit %d:\n%s", code, rout.String())
	}
}

// brokenStderr is the standard error of a GUI-subsystem exe that setup.ps1
// starts hidden: Windows gives it no handle, so every write fails.
type brokenStderr struct{}

func (brokenStderr) Write([]byte) (int, error) { return 0, os.ErrInvalid }

// TestSetupLogKeepsTheReasonWithoutStderr: the bootstrap starts the setup role
// with no standard error. setup.log is then the only place the reason for a
// refusal can reach, so a failing stderr must not stop the log file. On the
// live run the log stayed empty for every refusal.
func TestSetupLogKeepsTheReasonWithoutStderr(t *testing.T) {
	f := hvfake.New()
	dir := canonicalTempDir(t)
	f.FailNext("Inventory", errors.New("an inventory read failed for the test"))
	mk := func(winvm.Names) winvm.BuildDeps {
		return winvm.BuildDeps{HV: f, Probe: f, Guest: f, Dir: dir,
			Echo:  func(context.Context) []netip.Addr { return []netip.Addr{f.PublicIP} },
			Sleep: func(ctx context.Context, _ time.Duration) error { return ctx.Err() }, ProofWait: time.Millisecond}
	}
	params := writeParamsIn(t, filepath.Join(dir, "guard"), nil)
	if code := runVMSetupFor(context.Background(), params, winvm.ProductNames(), dir, nil, mk, brokenStderr{}); code != vmExitFailed {
		t.Fatalf("exit %d, want %d", code, vmExitFailed)
	}
	log, err := os.ReadFile(filepath.Join(dir, setupLogFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"VM storage volume", "setup failed:", "an inventory read failed for the test"} {
		if !strings.Contains(string(log), want) {
			t.Fatalf("setup.log lacks %q:\n%s", want, log)
		}
	}
}

func TestRunVMSetupRefuses(t *testing.T) {
	mk := func(winvm.Names) winvm.BuildDeps {
		t.Fatal("refused setup reached the build")
		return winvm.BuildDeps{}
	}
	for name, edit := range map[string]func(*winvm.SetupParams){
		"bad params":       func(sp *winvm.SetupParams) { sp.UserSID = "nobody" },
		"Everyone as user": func(sp *winvm.SetupParams) { sp.UserSID = "S-1-1-0" },
		"Users as user":    func(sp *winvm.SetupParams) { sp.UserSID = "S-1-5-32-545" },
		"lab settings":     func(sp *winvm.SetupParams) { sp.Lab = &winvm.LabOverrides{Instance: 1} },
	} {
		t.Run(name, func(t *testing.T) {
			dir := canonicalTempDir(t)
			params := writeParamsIn(t, filepath.Join(dir, "guard"), edit)
			var out bytes.Buffer
			if code := runVMSetupFor(context.Background(), params, winvm.ProductNames(), dir, nil, mk, &out); code != vmExitUsage {
				t.Errorf("exit %d, want %d: %s", code, vmExitUsage, &out)
			}
		})
	}
	if code := runVMSetup(context.Background(), writeParams(t, nil), `C:\Users\u\Downloads\CashPilot.exe`, mk, &bytes.Buffer{}); code != vmExitUsage {
		t.Errorf("setup with an exe outside the guard folder returned %d", code)
	}
	if code := runVMRemove(context.Background(), `C:\Users\u\elsewhere`, nil, &bytes.Buffer{}); code != vmExitUsage {
		t.Errorf("remove of a folder that is not the data folder: exit %d", code)
	}
	if code := vmSetupMain("a", "b", nil); code != vmExitUsage {
		t.Errorf("both roles at once: exit %d", code)
	}
}

// TestRunVMSetupRefusesParamsOutsideTheGuardFolder: the elevated setup reads
// only setup.ps1's copy in the admin-only guard folder. A params file in a
// folder the user can write is neither used nor deleted.
func TestRunVMSetupRefusesParamsOutsideTheGuardFolder(t *testing.T) {
	dir := canonicalTempDir(t)
	mk := func(winvm.Names) winvm.BuildDeps { return winvm.BuildDeps{Dir: dir} }
	for name, params := range map[string]string{
		"a user folder":   writeParams(t, nil),
		"the data folder": writeParamsIn(t, dir, nil),
	} {
		var out bytes.Buffer
		if code := runVMSetupFor(context.Background(), params, winvm.ProductNames(), dir, nil, mk, &out); code != vmExitUsage {
			t.Errorf("%s: exit %d, want %d:\n%s", name, code, vmExitUsage, out.String())
		}
		if _, err := os.Stat(params); err != nil {
			t.Errorf("%s: the elevated setup deleted a params file outside the guard folder: %v", name, err)
		}
	}
}

// testPassword is a made-up guest password for tests, built at run time from
// its number so that no password-shaped literal sits in the source.
func testPassword(n int) string { return "t" + strconv.Itoa(n) + strings.Repeat("Qz7", 6) }

func TestRunVMSetupRejectsLinkedGuardFolder(t *testing.T) {
	dir := canonicalTempDir(t)
	outside := t.TempDir()
	original := writeParamsIn(t, outside, nil)
	guard := filepath.Join(dir, "guard")
	if err := os.Symlink(outside, guard); err != nil {
		t.Skipf("cannot create test link: %v", err)
	}
	f := hvfake.New()
	mk := func(winvm.Names) winvm.BuildDeps {
		return winvm.BuildDeps{HV: f, Probe: f, Guest: f, Dir: dir,
			Echo:  func(context.Context) []netip.Addr { return []netip.Addr{f.PublicIP} },
			Sleep: func(ctx context.Context, _ time.Duration) error { return ctx.Err() }, ProofWait: time.Millisecond}
	}
	var out bytes.Buffer
	params := filepath.Join(guard, filepath.Base(original))
	if code := runVMSetupFor(context.Background(), params, winvm.ProductNames(), dir, nil, mk, &out); code != vmExitUsage {
		t.Fatalf("linked guard folder returned exit %d:\n%s", code, &out)
	}
	if _, err := os.Stat(original); err != nil {
		t.Fatalf("params outside the trusted guard folder were changed: %v", err)
	}
	if len(f.Calls()) > 0 {
		t.Fatalf("setup reached Hyper-V: %v", f.Calls())
	}
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestRemoveLogKeepsTheReasonWithoutStderr: remove.ps1 starts the remove role
// with no standard error, so a Remove that did not finish must say why in
// remove.log (live run 5 had no trace of why Remove left the switch, NAT and
// disk). A Remove that finishes leaves no log behind.
func TestRemoveLogKeepsTheReasonWithoutStderr(t *testing.T) {
	f := hvfake.New()
	dir := canonicalTempDir(t)
	mk := func(winvm.Names) winvm.BuildDeps {
		return winvm.BuildDeps{HV: f, Probe: f, Guest: f, Dir: dir,
			Echo:  func(context.Context) []netip.Addr { return []netip.Addr{f.PublicIP} },
			Sleep: func(ctx context.Context, _ time.Duration) error { return ctx.Err() }, ProofWait: time.Millisecond}
	}
	params := writeParamsIn(t, filepath.Join(dir, "guard"), nil)
	if code := runVMSetupFor(context.Background(), params, winvm.ProductNames(), dir, nil, mk, &bytes.Buffer{}); code != vmExitOK {
		t.Fatalf("setup exit %d", code)
	}
	rm := func(winvm.Names) winvm.RemoveDeps { return winvm.RemoveDeps{HV: f, Dir: dir} }
	f.FailNext("RemoveSwitch", errors.New("a switch removal failed for the test"))
	if code := runVMRemove(context.Background(), winvm.ProductNames().DataDir, rm, brokenStderr{}); code != vmExitFailed {
		t.Fatalf("remove exit %d, want %d", code, vmExitFailed)
	}
	log, err := os.ReadFile(filepath.Join(dir, removeLogFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"removed: the VM", "vm-remove:", "a switch removal failed for the test"} {
		if !strings.Contains(string(log), want) {
			t.Fatalf("remove.log lacks %q:\n%s", want, log)
		}
	}
	if code := runVMRemove(context.Background(), winvm.ProductNames().DataDir, rm, brokenStderr{}); code != vmExitOK {
		t.Fatalf("second remove exit %d", code)
	}
	if _, err := os.Stat(filepath.Join(dir, removeLogFile)); !os.IsNotExist(err) {
		t.Fatalf("a finished Remove left remove.log: %v", err)
	}
}
