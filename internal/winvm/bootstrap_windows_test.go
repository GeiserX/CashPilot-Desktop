//go:build windows

package winvm

import (
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
)

func fakePowerShellCommand(t *testing.T, ctx context.Context, args ...string) *exec.Cmd {
	t.Helper()
	cmd, err := hostPowerShellCommand(ctx, args...)
	if err != nil {
		t.Fatal(err)
	}
	return cmd
}

// TestBootstrapAndGuestScriptsParse checks the elevation stubs, bootstraps,
// guest scripts, and T2 lab scripts with PowerShell 5.1's parser.
// TestScriptsParse checks the host scripts in hvScripts.
func TestBootstrapAndGuestScriptsParse(t *testing.T) {
	all := make(map[string]string)
	for _, b := range []Bootstrap{SetupBootstrap(), RemoveBootstrap()} {
		all[b.Name] = string(b.Text)
		stub, err := (runtime.SecureVMStub{
			DataDir:      `C:\CashPilot`,
			Script:       `C:\CashPilot\guard\` + b.Name,
			ScriptName:   b.Name,
			ScriptSHA256: b.SHA256,
		}).Build()
		if err != nil {
			t.Fatal(err)
		}
		all["stub:"+b.Name] = stub
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
	for name, script := range answerPowerShell(t) {
		all[name] = script
	}
	for _, name := range []string{"Inventory", "CreateVM", "Guest", "RegisterGuardTask", "Prober.PktmonStart", "Prober.CanaryRule"} {
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

func answerPowerShell(t *testing.T) map[string]string {
	t.Helper()
	raw, err := testAnswer().Render()
	if err != nil {
		t.Fatal(err)
	}
	var answer struct {
		Settings []struct {
			Commands []struct {
				Order int
				Path  string
			} `xml:"component>RunSynchronous>RunSynchronousCommand"`
		} `xml:"settings"`
	}
	if err := xml.Unmarshal(raw, &answer); err != nil {
		t.Fatal(err)
	}
	scripts := make(map[string]string)
	for _, settings := range answer.Settings {
		for _, command := range settings.Commands {
			if !strings.HasPrefix(command.Path, "powershell.exe ") {
				continue
			}
			_, script, ok := strings.Cut(command.Path, ` -Command "`)
			if !ok || !strings.HasSuffix(script, `"`) {
				t.Fatalf("cannot extract PowerShell from answer command %d", command.Order)
			}
			scripts[fmt.Sprintf("guest:answer-command-%d", command.Order)] = strings.TrimSuffix(script, `"`)
		}
	}
	if len(scripts) == 0 {
		t.Fatal("the answer file has no PowerShell commands")
	}
	return scripts
}
