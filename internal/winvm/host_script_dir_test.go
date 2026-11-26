package winvm

import (
	"path/filepath"
	"testing"
)

func TestHostScriptDirectoryUsesProgramData(t *testing.T) {
	programData := filepath.Join(t.TempDir(), "ProgramData")
	temp := t.TempDir()
	t.Setenv("TEMP", temp)
	t.Setenv("TMP", temp)
	t.Setenv("TMPDIR", temp)

	got, err := hostScriptDirectory(programData)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(programData, "CashPilot-HostScripts")
	if got != want {
		t.Fatalf("host script directory = %q, want %q", got, want)
	}
	if path, err := hostScriptDirectory(""); err == nil {
		t.Fatalf("missing ProgramData resolved to %q", path)
	}
}
