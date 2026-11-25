package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/bgservice"
)

// uninstallFake records which steps the cleanup ran, in order.
type uninstallFake struct {
	calls     []string
	installed bool
	failStep  string
}

func (f *uninstallFake) step(name string) error {
	f.calls = append(f.calls, name)
	if f.failStep == name {
		return errors.New(name + " broke")
	}
	return nil
}

func (f *uninstallFake) Install(string, []string) error { return f.step("helper-install") }
func (f *uninstallFake) Uninstall() error               { return f.step("helper-remove") }
func (f *uninstallFake) Stop() error                    { return f.step("helper-stop") }
func (f *uninstallFake) Status() (bgservice.Status, error) {
	return bgservice.Status{Installed: f.installed, Label: `CashPilot\Helper`}, f.step("helper-status")
}

func (f *uninstallFake) steps(t *testing.T, out *bytes.Buffer) uninstallSteps {
	appDir := filepath.Join(t.TempDir(), "CashPilot Desktop")
	return uninstallSteps{
		out:           out,
		agent:         f,
		disarmRunOnce: func() error { return f.step("runonce") },
		removeContainers: func(context.Context) ([]string, error) {
			return []string{"cashpilot-honeygain"}, f.step("containers")
		},
		removeNative:  func(context.Context) ([]string, error) { return nil, f.step("native") },
		deleteSecrets: func() error { return f.step("secrets") },
		appDir:        appDir,
		removeAll: func(dir string) error {
			if dir != appDir {
				t.Errorf("removeAll(%q), want the data folder %q", dir, appDir)
			}
			return f.step("data")
		},
	}
}

func TestUninstallDefaultKeepsEarnersAndData(t *testing.T) {
	f := &uninstallFake{installed: true}
	var out bytes.Buffer
	if code := runUninstall(context.Background(), f.steps(t, &out), false, false); code != 0 {
		t.Fatalf("exit %d, output:\n%s", code, out.String())
	}
	if got, want := strings.Join(f.calls, ","), "helper-stop,helper-status,helper-remove,runonce"; got != want {
		t.Fatalf("steps %s, want %s", got, want)
	}
	for _, line := range []string{"Removed the background helper", "!CashPilotResumeSetup", "Kept the earning containers", "Kept your data in"} {
		if !strings.Contains(out.String(), line) {
			t.Errorf("output lacks %q:\n%s", line, out.String())
		}
	}
}

func TestUninstallRemovesEarnersOnlyWhenAsked(t *testing.T) {
	f := &uninstallFake{installed: true}
	var out bytes.Buffer
	if code := runUninstall(context.Background(), f.steps(t, &out), true, false); code != 0 {
		t.Fatalf("exit %d, output:\n%s", code, out.String())
	}
	if got, want := strings.Join(f.calls, ","), "helper-stop,helper-status,helper-remove,runonce,containers,native"; got != want {
		t.Fatalf("steps %s, want %s", got, want)
	}
	if !strings.Contains(out.String(), "cashpilot-honeygain") || !strings.Contains(out.String(), "Kept your data in") {
		t.Errorf("output:\n%s", out.String())
	}
}

// Deleting the data takes the native earners too, since their files are in it,
// but leaves the containers, whose data is in Docker's volumes.
func TestUninstallDeleteDataTakesNativeEarnersKeysAndFolder(t *testing.T) {
	f := &uninstallFake{}
	var out bytes.Buffer
	if code := runUninstall(context.Background(), f.steps(t, &out), false, true); code != 0 {
		t.Fatalf("exit %d, output:\n%s", code, out.String())
	}
	if got, want := strings.Join(f.calls, ","), "helper-stop,helper-status,runonce,native,secrets,data"; got != want {
		t.Fatalf("steps %s, want %s", got, want)
	}
	if !strings.Contains(out.String(), "No background helper was registered") || !strings.Contains(out.String(), "Kept the earning containers") {
		t.Errorf("output:\n%s", out.String())
	}
}

// One failed step does not stop the others, and the exit code says it failed.
func TestUninstallRunsEveryStepWhenOneFails(t *testing.T) {
	for _, broken := range []string{"helper-status", "helper-remove", "runonce", "containers", "native", "secrets", "data"} {
		f := &uninstallFake{installed: true, failStep: broken}
		var out bytes.Buffer
		code := runUninstall(context.Background(), f.steps(t, &out), true, true)
		if code != 1 {
			t.Errorf("%s failing: exit %d, want 1", broken, code)
		}
		if !strings.Contains(out.String(), "FAILED:") {
			t.Errorf("%s failing: no FAILED line:\n%s", broken, out.String())
		}
		last := f.calls[len(f.calls)-1]
		if last != "data" {
			t.Errorf("%s failing: the cleanup stopped at %s; steps %v", broken, last, f.calls)
		}
	}
}

// A data folder that is not one is never deleted.
func TestUninstallRefusesToDeleteADangerousFolder(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip(err)
	}
	root := filepath.VolumeName(home) + string(filepath.Separator)
	for _, dir := range []string{"", "relative", root, home} {
		f := &uninstallFake{}
		var out bytes.Buffer
		s := f.steps(t, &out)
		s.appDir = dir
		s.removeAll = func(d string) error { return f.step("data:" + d) }
		if code := runUninstall(context.Background(), s, false, true); code != 1 {
			t.Errorf("data folder %q: exit %d, want 1", dir, code)
		}
		for _, c := range f.calls {
			if strings.HasPrefix(c, "data:") {
				t.Errorf("data folder %q was deleted", dir)
			}
		}
	}
	if err := safeDataDir(filepath.Join(home, "AppData", "Roaming", "CashPilot Desktop")); err != nil {
		t.Errorf("the real data folder is refused: %v", err)
	}
}

func TestUninstallSaysWhenTheSecureVMStays(t *testing.T) {
	f := &uninstallFake{}
	var out bytes.Buffer
	s := f.steps(t, &out)
	s.secureVMDir = t.TempDir()
	runUninstall(context.Background(), s, false, false)
	if !strings.Contains(out.String(), "Secure VM (Windows 11 Pro) was not removed") {
		t.Errorf("no Secure VM line:\n%s", out.String())
	}
	out.Reset()
	s.secureVMDir = filepath.Join(t.TempDir(), "missing")
	runUninstall(context.Background(), s, false, false)
	if strings.Contains(out.String(), "Secure VM") {
		t.Errorf("Secure VM line with no Secure VM:\n%s", out.String())
	}
}

func TestUninstallMainRefusesAnUnparsedCommandLine(t *testing.T) {
	if code := runUninstallMain(true, true, errors.New("flag provided but not defined: -x")); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
}
