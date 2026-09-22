package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	stdruntime "runtime"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/compose"
	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/services"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// archyEntry is a catalog entry that publishes a separate build for each of the
// three architecture families, the shape image_by_arch exists for. Traffmonetizer is
// the real one: Docker Hub labels every one of its tags linux/amd64, so the machine
// running the file cannot pick its own build and the export has to name it.
const archyEntry = `name: Archy
slug: archy
category: bandwidth
status: active
docker:
  image: archy/client:manifest
  image_by_arch:
    amd64: archy/client:amd64
    arm64: archy/client:arm64
    arm: archy/client:arm
  env:
    - key: ARCHY_TOKEN
      label: Token
      required: true
      secret: true
`

// exportApp is an App with just enough of the machinery for ExportCompose, and a
// save dialog a test can answer. The real dialog needs a window.
func exportApp(t *testing.T, answer func(wailsruntime.SaveDialogOptions) (string, error)) *App {
	t.Helper()
	t.Setenv("CASHPILOT_DESKTOP_DATA_DIR", t.TempDir())
	cfg, err := config.NewManager()
	if err != nil {
		t.Fatalf("config.NewManager error: %v", err)
	}
	st, err := store.Open(cfg.DataDir())
	if err != nil {
		t.Fatalf("store.Open error: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	cat, err := catalog.LoadEmbedded(fstest.MapFS{
		"services/bandwidth/archy.yml": {Data: []byte(archyEntry)},
	})
	if err != nil {
		t.Fatalf("catalog.LoadEmbedded error: %v", err)
	}

	provider := runtime.NewDockerProvider()
	return &App{
		cfg:        cfg,
		store:      st,
		catalog:    cat,
		runtime:    provider,
		services:   services.NewManager(provider, cat, st),
		ctx:        context.Background(),
		saveDialog: answer,
	}
}

// The export writes the file where the user pointed the dialog, and answers with
// that path so the card can show it.
func TestExportComposeWritesTheFileTheUserChose(t *testing.T) {
	target := filepath.Join(t.TempDir(), "docker-compose.yml")
	app := exportApp(t, func(wailsruntime.SaveDialogOptions) (string, error) { return target, nil })

	path, err := app.ExportCompose([]string{"archy"}, "arm64")
	if err != nil {
		t.Fatalf("ExportCompose error: %v", err)
	}
	if path != target {
		t.Errorf("path = %q, want %q", path, target)
	}
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("the export reported success and wrote nothing: %v", err)
	}
	if !strings.Contains(string(body), "archy/client:arm64") {
		t.Errorf("the saved file is not the one that was asked for:\n%s", body)
	}
}

// Dismissing the dialog is not an error. It has to answer with no path and write
// nothing, or the card shows a failure for a choice the user made deliberately.
func TestExportComposeWritesNothingWhenTheDialogIsDismissed(t *testing.T) {
	dir := t.TempDir()
	app := exportApp(t, func(wailsruntime.SaveDialogOptions) (string, error) { return "", nil })

	path, err := app.ExportCompose([]string{"archy"}, "arm64")
	if err != nil {
		t.Fatalf("a dismissed dialog must not be an error, got %v", err)
	}
	if path != "" {
		t.Errorf("path = %q, want nothing saved", path)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("a dismissed dialog left %d files behind", len(entries))
	}
}

// A file that could not be written says so. Reporting the path as saved would leave
// the user looking for a file that is not there.
func TestExportComposeSaysSoWhenTheFileCannotBeWritten(t *testing.T) {
	// A path whose parent is a regular file: no platform can create a file under it,
	// and no test needs a permission bit that Windows reads differently.
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parent, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := exportApp(t, func(wailsruntime.SaveDialogOptions) (string, error) {
		return filepath.Join(parent, "docker-compose.yml"), nil
	})

	path, err := app.ExportCompose([]string{"archy"}, "arm64")
	if err == nil {
		t.Fatalf("ExportCompose reported %q saved, but nothing could be written there", path)
	}
	if !strings.Contains(err.Error(), "compose file") {
		t.Errorf("error = %v, want it to say which file failed", err)
	}
}

// The dialog is never opened for a file that cannot be generated: an unknown service
// is refused before the user is asked where to put it.
func TestExportComposeRefusesBeforeAskingWhereToSaveIt(t *testing.T) {
	asked := false
	app := exportApp(t, func(wailsruntime.SaveDialogOptions) (string, error) {
		asked = true
		return "", fmt.Errorf("the dialog should not have opened")
	})

	if _, err := app.ExportCompose([]string{"nope"}, "arm64"); err == nil {
		t.Fatal("expected an error for a service the catalog does not have")
	}
	if asked {
		t.Error("the save dialog opened for a file that could never be written")
	}
}

// "This machine" means this machine.
//
// The dropdown's default sends no architecture, and an empty architecture used to
// mean "let the image manifest decide" — which for the entries whose ARM builds
// Docker cannot see writes the x86-64 build. On an Apple silicon Mac or a Raspberry
// Pi that container cannot start, so the box the user exported ON is the one box the
// file did not work for.
func TestExportComposeForThisMachineWritesThisMachinesBuild(t *testing.T) {
	target := filepath.Join(t.TempDir(), "docker-compose.yml")
	app := exportApp(t, func(wailsruntime.SaveDialogOptions) (string, error) { return target, nil })

	if _, err := app.ExportCompose([]string{"archy"}, ""); err != nil {
		t.Fatalf("ExportCompose error: %v", err)
	}
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}

	want := "archy/client:manifest"
	if family := compose.HostFamily(stdruntime.GOARCH); family != "" {
		want = "archy/client:" + family
	}
	if !strings.Contains(string(body), want) {
		t.Errorf("the file exported for this machine (%s) does not run %s:\n%s", stdruntime.GOARCH, want, body)
	}
}
