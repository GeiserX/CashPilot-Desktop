package runtime

import "testing"

// PowerShell's path is built from the system folder, always absolute, and never
// the bare name PATH would resolve.
func TestWindowsPowerShellIn(t *testing.T) {
	for _, dir := range []string{`C:\Windows\System32`, `C:\Windows\System32\`, `D:\WIN\system32`} {
		got, err := WindowsPowerShellIn(dir)
		if err != nil {
			t.Fatalf("%q: %v", dir, err)
		}
		want := map[string]string{
			`C:\Windows\System32`:  `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`,
			`C:\Windows\System32\`: `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`,
			`D:\WIN\system32`:      `D:\WIN\system32\WindowsPowerShell\v1.0\powershell.exe`,
		}[dir]
		if got != want {
			t.Errorf("%q: %q, want %q", dir, got, want)
		}
	}
	for _, dir := range []string{"", "System32", `.\System32`, "/usr/bin"} {
		if got, err := WindowsPowerShellIn(dir); err == nil {
			t.Errorf("%q was accepted as the system folder: %q", dir, got)
		}
	}
}
