//go:build windows

package winvm

import (
	"os"
	"path/filepath"
	"testing"
)

// TestBootstrapAndGuestScriptsParse sends the bootstraps, every guest script
// and the lab's T1/T2 scripts through PowerShell 5.1's parser. The seam's
// scripts, the write side and the Prober's included, are in TestScriptsParse
// through hvScripts.
func TestBootstrapAndGuestScriptsParse(t *testing.T) {
	all := map[string]string{
		"setup.ps1":  string(SetupBootstrap().Text),
		"remove.ps1": string(RemoveBootstrap().Text),
	}
	for _, s := range GuestScripts() {
		all["guest:"+s.Name] = s.Text
	}
	for _, f := range []string{"t2-proof.ps1", "t2-build.ps1", "t2-remove.ps1"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "scripts", "winvm-lab", f))
		if err != nil {
			t.Fatal(err)
		}
		all["scripts/winvm-lab/"+f] = string(b)
	}
	for _, name := range []string{"Inventory", "CreateVM", "Guest", "RegisterGuardTask", "Prober.PktmonStart", "Prober.Allow"} {
		if _, ok := hvScripts[name]; !ok {
			t.Fatalf("hvScripts lacks %s, so TestScriptsParse does not parse it", name)
		}
	}
	for name, s := range all {
		t.Run(name, func(t *testing.T) {
			if n, msgs := parseErrors(t, s); n != 0 {
				t.Fatalf("%d parse errors:\n%s", n, msgs)
			}
		})
	}
}
