//go:build windows

package runtime

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// The command the app runs is System32's Windows PowerShell by its full path,
// never the bare name PATH would resolve.
func TestPowerShellCommandUsesTheFullPath(t *testing.T) {
	cmd, err := powerShellCommand(context.Background(), "-Command", "exit 0")
	if err != nil {
		t.Fatal(err)
	}
	dir, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "WindowsPowerShell", "v1.0", "powershell.exe")
	if !filepath.IsAbs(cmd.Path) || !strings.EqualFold(cmd.Path, want) || !strings.EqualFold(cmd.Args[0], want) {
		t.Fatalf("PowerShell runs as %q (args[0] %q), want %q", cmd.Path, cmd.Args[0], want)
	}
	if err := cmd.Run(); err != nil {
		t.Fatalf("PowerShell at its full path did not run: %v", err)
	}
}
