//go:build windows

package winvm

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// parseScript runs PowerShell's own parser over a script and prints how many
// parse errors it found. It never runs the script.
const parseScript = `$ErrorActionPreference = 'Stop'
$t = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String([Console]::In.ReadToEnd()))
$tokens = $null
$errs = $null
[void][System.Management.Automation.Language.Parser]::ParseInput($t, [ref]$tokens, [ref]$errs)
foreach ($e in $errs) { [Console]::Error.WriteLine([string]$e.Extent.StartLineNumber + ': ' + $e.Message) }
[Console]::Out.Write([string]@($errs).Count)`

func parseErrors(t *testing.T, script string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd, err := hostPowerShellCommand(ctx, "-EncodedCommand", psEncode(parseScript))
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stdin = strings.NewReader(base64.StdEncoding.EncodeToString([]byte(script)))
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("the parser did not run: %v: %s", err, stderr.String())
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("the parser printed %q", out)
	}
	return n, stderr.String()
}

// TestScriptsParse sends every script this package runs, and the lab's read
// script, through PowerShell 5.1's parser and wants no parse error. The
// runner has no Hyper-V, so nothing is run. A quoting mistake like the one that
// made the first lab run report a false green fails here.
func TestScriptsParse(t *testing.T) {
	all := map[string]string{"cpuScript": cpuScript, "featureScript": featureScript, "hostScriptLauncher": hostScriptLauncher}
	for name, s := range hvScripts {
		all[name] = s
	}
	lab, err := os.ReadFile(filepath.Join("..", "..", "scripts", "winvm-lab", "read.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	all["scripts/winvm-lab/read.ps1"] = string(lab)
	for name, s := range all {
		t.Run(name, func(t *testing.T) {
			if n, msgs := parseErrors(t, s); n != 0 {
				t.Fatalf("%d parse errors:\n%s", n, msgs)
			}
		})
	}
}

// The parser check can fail: an unclosed string and an unclosed block are
// reported.
func TestScriptsParseCanFail(t *testing.T) {
	broken := scriptConnect + "\nif ($x) { Write-Doc ([ordered]@{ a = 'b })\n"
	if n, _ := parseErrors(t, broken); n == 0 {
		t.Fatal("a broken script parsed without errors, so the parse test proves nothing")
	}
}
