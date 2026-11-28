//go:build windows

package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// TestFirewallBlockLive drives the real Windows Firewall. It changes the machine, so
// it runs only when asked for:
//
//	CASHPILOT_FIREWALL_LIVE=ensure|check|remove
//	CASHPILOT_FIREWALL_LIVE_PROGRAM=C:\path\to\program.exe
//
// ensure creates the rule (an admin prompt unless the test runs elevated), check
// reads it the way an unelevated app does, remove deletes it.
func TestFirewallBlockLive(t *testing.T) {
	step := os.Getenv("CASHPILOT_FIREWALL_LIVE")
	if step == "" {
		t.Skip("set CASHPILOT_FIREWALL_LIVE to drive the real Windows Firewall")
	}
	const slug = "cashpilot-live-test"
	program := os.Getenv("CASHPILOT_FIREWALL_LIVE_PROGRAM")
	nets := []string{"100.64.0.0/10", "169.254.0.0/16"}
	switch step {
	case "ensure":
		if err := ensureFirewallBlock(slug, program, nets); err != nil {
			t.Fatal(err)
		}
		fallthrough
	case "check":
		st, err := readFirewallRule(firewallRuleName(slug, program))
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("rule %q: %+v", firewallRuleName(slug, program), st)
		if !st.matches(program, nets) {
			t.Fatal("the rule on this machine is not the one we asked for")
		}
		if why := st.notEnforced(); why != "" {
			t.Fatalf("the rule matches but would do nothing: %s", why)
		}
	case "remove":
		if err := removeFirewallBlock(slug, program, true); err != nil {
			t.Fatal(err)
		}
		st, err := readFirewallRule(firewallRuleName(slug, program))
		if err != nil {
			t.Fatal(err)
		}
		if st.Found {
			t.Fatal("the rule is still there")
		}
	default:
		t.Fatalf("unknown step %q", step)
	}
}

// TestSecureVMStubLive runs the Secure VM's admin stub for real in this
// elevated process: a bootstrap with the right hash runs with its arguments
// and hands back its exit code; one whose hash differs never runs and the stub
// exits ExitBadHash; a data folder that is a link is refused; and a data
// folder holding a file a standard user owns (planted before setup, with a
// rule that lets that user write it) is refused before anything runs.
func TestSecureVMStubLive(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		if os.Getenv("GITHUB_ACTIONS") == "true" {
			t.Fatal("expected an elevated process on the Windows CI runner")
		}
		t.Skip("not elevated: the stub would show the admin prompt")
	}
	tmp := t.TempDir()
	marker := filepath.Join(tmp, "ran.txt")
	body := []byte("param([string]$Mode, [string]$Out)\n'ran' | Set-Content -LiteralPath $Out\nif ($Mode -eq 'restart') { exit 3010 }\nexit 0\n")
	script := filepath.Join(tmp, "boot.ps1")
	if err := os.WriteFile(script, body, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(body)
	good := strings.ToUpper(hex.EncodeToString(sum[:]))
	stub := func(data, hash, mode string) (ElevatedResult, error) {
		s, err := SecureVMStub{DataDir: data, Script: script, ScriptName: "boot.ps1", ScriptSHA256: hash,
			Args: [][2]string{{"Mode", mode}, {"Out", marker}}}.Build()
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(tmp, "bootstrap-wrapper.ps1")
		if err := os.WriteFile(path, []byte("\xef\xbb\xbf"+s), 0o600); err != nil {
			t.Fatal(err)
		}
		exe, err := PowerShellExe()
		if err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(exe, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", path).CombinedOutput()
		if len(out) > 0 {
			t.Logf("bootstrap wrapper: %s", out)
		}
		return elevatedOutcome(err)
	}
	data := filepath.Join(tmp, "data")
	if res, err := stub(data, good, "ok"); err != nil || res.RestartNeeded {
		t.Fatalf("good hash: %+v, %v", res, err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("the bootstrap did not run")
	}
	if res, err := stub(data, good, "restart"); err != nil || !res.RestartNeeded {
		t.Fatalf("exit 3010: %+v, %v", res, err)
	}
	os.Remove(marker)
	bad := strings.Repeat("A", 64)
	_, err := stub(data, bad, "ok")
	if code, ok := ExitCodeOf(err); !ok || code != ExitBadHash {
		t.Fatalf("wrong hash: %v (code %d), want exit %d", err, code, ExitBadHash)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the bootstrap ran with the wrong hash")
	}
	if _, err := os.Stat(filepath.Join(data, "guard", "boot.ps1")); err == nil {
		t.Fatal("the copy with the wrong hash was left in the guard folder")
	}
	link := filepath.Join(tmp, "link")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, tmp).CombinedOutput(); err != nil {
		t.Fatalf("mklink: %v: %s", err, out)
	}
	if _, err := stub(link, good, "ok"); err == nil {
		t.Fatal("a data folder that is a link was accepted")
	}

	// A standard user made the data folder first and planted the bootstrap
	// copy, owned by Users, with a rule that gives Users write.
	planted := filepath.Join(tmp, "planted")
	guard := filepath.Join(planted, "guard")
	if err := os.MkdirAll(guard, 0o700); err != nil {
		t.Fatal(err)
	}
	trap := filepath.Join(guard, "boot.ps1")
	if err := os.WriteFile(trap, []byte("'planted' | Set-Content -LiteralPath $Out\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The folders are Administrators', so only the planted file is refused.
	for _, args := range [][]string{{planted, "/setowner", "*S-1-5-32-544"}, {guard, "/setowner", "*S-1-5-32-544"},
		{trap, "/grant", "*S-1-5-32-545:(M)"}, {trap, "/setowner", "*S-1-5-32-545"}} {
		if out, err := exec.Command("icacls", args...).CombinedOutput(); err != nil {
			t.Fatalf("icacls %v: %v: %s", args, err, out)
		}
	}
	os.Remove(marker)
	_, err = stub(planted, good, "ok")
	if code, ok := ExitCodeOf(err); !ok || code != 1 {
		t.Fatalf("a planted file owned by Users: %v (code %d), want exit 1", err, code)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the bootstrap ran from a data folder holding a planted file")
	}

	// A standard user made the data folder first and left it empty. As its
	// owner they could keep a handle with WRITE_DAC open across the stub's
	// new rules, so the stub refuses a folder it did not make.
	seeded := filepath.Join(tmp, "seeded")
	if err := os.Mkdir(seeded, 0o700); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("icacls", seeded, "/setowner", "*S-1-5-32-545").CombinedOutput(); err != nil {
		t.Fatalf("icacls: %v: %s", err, out)
	}
	_, err = stub(seeded, good, "ok")
	if code, ok := ExitCodeOf(err); !ok || code != 1 {
		t.Fatalf("an empty data folder owned by Users: %v (code %d), want exit 1", err, code)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the bootstrap ran from a data folder a standard user made")
	}
	if _, err := os.Stat(filepath.Join(seeded, "guard")); err == nil {
		t.Fatal("the stub made the guard folder inside a data folder a standard user made")
	}
}
