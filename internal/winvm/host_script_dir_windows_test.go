//go:build windows

package winvm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestHostPowerShellUsesPrivateStorage(t *testing.T) {
	temp := t.TempDir()
	t.Setenv("TEMP", temp)
	t.Setenv("TMP", temp)
	programData, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ProgramData", temp)
	want := filepath.Join(programData, "CashPilot-HostScripts")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	elevated := windows.GetCurrentProcessToken().IsElevated()

	t.Run("probe", func(t *testing.T) {
		cmd, release, err := cimCommand(ctx, "exit 0")
		if !elevated {
			if release != nil {
				defer release()
			}
			if !errors.Is(err, windows.ERROR_ELEVATION_REQUIRED) {
				t.Fatalf("unelevated PowerShell command = %v, error = %v", cmd, err)
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		path := cmd.Args[len(cmd.Args)-1]
		if !strings.EqualFold(filepath.Dir(path), want) {
			t.Fatalf("probe script = %q, want it under %q", path, want)
		}
		if err := os.Remove(path); !errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			t.Fatalf("remove while the script is open: %v", err)
		}
	})

	t.Run("inspector", func(t *testing.T) {
		host := windowsHost("")
		out, err := host.run(ctx, hvPrelude+"Write-Doc @{path = $PSCommandPath}", []byte("{}"))
		if !elevated {
			if !errors.Is(err, windows.ERROR_ELEVATION_REQUIRED) {
				t.Fatalf("unelevated PowerShell output = %q, error = %v", out, err)
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		var result struct{ Path string }
		if err := decodeDoc(out, &result); err != nil {
			t.Fatal(err)
		}
		if !strings.EqualFold(filepath.Dir(result.Path), want) {
			t.Fatalf("inspector script = %q, want it under %q", result.Path, want)
		}
		if _, err := os.Stat(result.Path); !os.IsNotExist(err) {
			t.Fatalf("inspector script cleanup: %v", err)
		}
	})
}
