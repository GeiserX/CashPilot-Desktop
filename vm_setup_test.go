package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/e2e/harness/hvfake"
	"github.com/GeiserX/CashPilot-Desktop/internal/winvm"
)

func writeParams(t *testing.T, edit func(*winvm.SetupParams)) string {
	t.Helper()
	sp := winvm.SetupParams{
		Version: winvm.SetupParamsVersion, VHDXDir: `D:\VMs`, ISO: `D:\iso\windows.iso`, ImageIndex: 6,
		AdminPassword: fakePassword("Adm1n"), EarnerPassword: fakePassword("Earn3r"),
		UserSID: "S-1-5-21-1-2-3-1001", ApproveNetwork: true,
	}
	if edit != nil {
		edit(&sp)
	}
	b, _ := json.Marshal(sp)
	p := filepath.Join(t.TempDir(), "params.json")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunVMSetupBuildsAndDeletesTheParams(t *testing.T) {
	f := hvfake.New()
	dir := t.TempDir()
	mk := func(winvm.Names) winvm.BuildDeps {
		return winvm.BuildDeps{HV: f, Probe: f, Guest: f, Dir: dir,
			Echo:  func(context.Context) []netip.Addr { return []netip.Addr{f.PublicIP} },
			Sleep: func(ctx context.Context, _ time.Duration) error { return ctx.Err() }, ProofWait: time.Millisecond}
	}
	params := writeParams(t, nil)
	var out bytes.Buffer
	if code := runVMSetup(context.Background(), params, winvm.ProductNames().GuardExe(), mk, &out); code != vmExitOK {
		t.Fatalf("exit %d:\n%s", code, out.String())
	}
	if _, err := os.Stat(params); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("the params file, which holds the passwords, is still there")
	}
	log, _ := os.ReadFile(filepath.Join(dir, setupLogFile))
	if strings.Contains(string(log), "Adm1n-Pass") || strings.Contains(out.String(), "Adm1n-Pass") {
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

func TestRunVMSetupRefuses(t *testing.T) {
	mk := func(winvm.Names) winvm.BuildDeps {
		t.Fatal("refused setup reached the build")
		return winvm.BuildDeps{}
	}
	guard := winvm.ProductNames().GuardExe()
	cases := map[string]struct {
		params string
		exe    string
	}{
		"not the guard copy": {writeParams(t, nil), `C:\Users\u\Downloads\CashPilot.exe`},
		"bad params":         {writeParams(t, func(sp *winvm.SetupParams) { sp.UserSID = "nobody" }), guard},
		"lab settings":       {writeParams(t, func(sp *winvm.SetupParams) { sp.Lab = &winvm.LabOverrides{Instance: 1} }), guard},
		"no file":            {filepath.Join(t.TempDir(), "missing.json"), guard},
	}
	for name, c := range cases {
		var out bytes.Buffer
		if code := runVMSetup(context.Background(), c.params, c.exe, mk, &out); code != vmExitUsage {
			t.Errorf("%s: exit %d, want %d", name, code, vmExitUsage)
		}
	}
	if code := runVMRemove(context.Background(), `C:\Users\u\elsewhere`, nil, &bytes.Buffer{}); code != vmExitUsage {
		t.Errorf("remove of a folder that is not the data folder: exit %d", code)
	}
	if code := vmSetupMain("a", "b", nil); code != vmExitUsage {
		t.Errorf("both roles at once: exit %d", code)
	}
}

// fakePassword is a made-up guest password for tests, built at run time so
// that no password-shaped literal sits in the source.
func fakePassword(tag string) string { return tag + "-Pass-" + "0123456789" }
