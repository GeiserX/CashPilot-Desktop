package runtime

import (
	"fmt"
	"regexp"
	"strings"
)

// windowsDrivePath is an absolute path on a drive letter, as GetSystemDirectory
// reports it (C:\Windows\System32).
var windowsDrivePath = regexp.MustCompile(`^[A-Za-z]:\\`)

// WindowsPowerShellIn is Windows PowerShell's full path inside systemDir, the
// System32 folder that GetSystemDirectory reports:
// %SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe. The app starts
// PowerShell only by this path, never by a bare name looked up on PATH, so a
// powershell.exe in a folder the user can write to never runs, elevated or not.
func WindowsPowerShellIn(systemDir string) (string, error) {
	dir := strings.TrimRight(strings.TrimSpace(systemDir), `\`)
	if !windowsDrivePath.MatchString(dir + `\`) {
		return "", fmt.Errorf("%q is not an absolute Windows folder", systemDir)
	}
	return dir + `\WindowsPowerShell\v1.0\powershell.exe`, nil
}
