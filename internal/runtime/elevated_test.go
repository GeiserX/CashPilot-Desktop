package runtime

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

type exitErr int

func (e exitErr) Error() string { return fmt.Sprintf("exit status %d", int(e)) }
func (e exitErr) ExitCode() int { return int(e) }

func TestElevatedOutcomeReadsRestartCodesAsSuccess(t *testing.T) {
	cases := []struct {
		err     error
		ok      bool
		restart bool
	}{
		{nil, true, false},
		// powerShell wraps the *exec.ExitError with stderr; the code must still be read.
		{fmt.Errorf("%w: stderr text", exitErr(3010)), true, true},
		{fmt.Errorf("%w: stderr text", exitErr(1641)), true, true},
		{fmt.Errorf("%w: stderr text", exitErr(3017)), false, false},
		{fmt.Errorf("%w: stderr text", exitErr(1)), false, false},
		{errors.New("the admin prompt was declined"), false, false},
	}
	for _, c := range cases {
		res, err := elevatedOutcome(c.err)
		if (err == nil) != c.ok || res.RestartNeeded != c.restart {
			t.Errorf("%v: got %+v, %v; want ok %v, restart %v", c.err, res, err, c.ok, c.restart)
		}
		if !c.ok && err != c.err {
			t.Errorf("%v: the failure was not returned unchanged: %v", c.err, err)
		}
	}
}

func TestElevatedScriptCarriesTheSignedInSID(t *testing.T) {
	const sid = "S-1-5-21-3623811015-3361044348-30300820-1013"
	got, err := elevatedScript("Add-LocalGroupMember -SID 'S-1-5-32-578' -Member $CashPilotUserSid", sid)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, "$CashPilotUserSid = '"+sid+"'\n") {
		t.Errorf("the SID is not set first: %q", got)
	}
	if !strings.HasSuffix(got, "-Member $CashPilotUserSid") {
		t.Errorf("the script was changed: %q", got)
	}
	for _, bad := range []string{"", "Administrator", "S-1-5-21-1'; Remove-Item C:\\ -Recurse; '", "S-1-5", "S-1-5-21-1\n"} {
		if _, err := elevatedScript("exit 0", bad); err == nil {
			t.Errorf("%q was accepted as a SID", bad)
		}
	}
}

func TestSecureVMStubRefusesBadValues(t *testing.T) {
	ok := SecureVMStub{DataDir: `C:\ProgramData\CashPilot`, Script: `C:\Users\u\AppData\Local\Temp\setup.ps1`,
		ScriptName: "setup.ps1", ScriptSHA256: strings.Repeat("AB", 32),
		Args: [][2]string{{"Exe", `C:\Program Files\CashPilot\CashPilot.exe`}, {"Params", `C:\Users\u\p.json`}}}
	s, err := ok.Build()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"'" + strings.Repeat("AB", 32) + "'", "-Exe 'C:\\Program Files\\CashPilot\\CashPilot.exe'", "exit 13", "ReparsePoint", "O:BAD:P"} {
		if !strings.Contains(s, want) {
			t.Errorf("the stub lacks %q", want)
		}
	}
	bad := map[string]func(*SecureVMStub){
		"quote in the data folder": func(s *SecureVMStub) { s.DataDir = `C:\x'; calc; '` },
		"relative script":          func(s *SecureVMStub) { s.Script = `setup.ps1` },
		"dot dot":                  func(s *SecureVMStub) { s.DataDir = `C:\ProgramData\..\Windows` },
		"lowercase hash":           func(s *SecureVMStub) { s.ScriptSHA256 = strings.Repeat("ab", 32) },
		"short hash":               func(s *SecureVMStub) { s.ScriptSHA256 = "AB" },
		"script name with a path":  func(s *SecureVMStub) { s.ScriptName = `..\x.ps1` },
		"quote in a value":         func(s *SecureVMStub) { s.Args = [][2]string{{"Exe", `C:\a' + (calc) + '`}} },
		"dollar in a value":        func(s *SecureVMStub) { s.Args = [][2]string{{"Exe", `C:\$env:x`}} },
		"bad parameter name":       func(s *SecureVMStub) { s.Args = [][2]string{{"exe; calc", `C:\a`}} },
		"newline in a value":       func(s *SecureVMStub) { s.Args = [][2]string{{"Exe", "C:\\a\ncalc"}} },
	}
	for name, edit := range bad {
		s := ok
		edit(&s)
		if _, err := s.Build(); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
