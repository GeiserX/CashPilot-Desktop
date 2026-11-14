package main

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/services"
	"github.com/GeiserX/CashPilot-Desktop/internal/statebackup"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const keeperEntry = `name: Keeper
slug: keeper
category: depin
status: active
docker:
  image: keeper/node:1.0.0
  volumes:
    - "keeper-data:/var/lib/keeper"
  critical_volumes:
    - target: "/var/lib/keeper/"
      holds: "The node identity. Lose it and the held balance goes with it."
`

const plainEntry = `name: Plain
slug: plain
category: bandwidth
status: active
docker:
  image: plain/client:1.0.0
`

// backupApp is an App with the catalog above, both services recorded as deployed,
// and a save dialog that counts how often it was opened.
func backupApp(t *testing.T, dialogs *int) *App {
	t.Helper()
	t.Setenv("CASHPILOT_DESKTOP_DATA_DIR", t.TempDir())
	cfg, err := config.NewManager()
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.DataDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cat, err := catalog.LoadEmbedded(fstest.MapFS{
		"services/depin/keeper.yml":    {Data: []byte(keeperEntry)},
		"services/bandwidth/plain.yml": {Data: []byte(plainEntry)},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, slug := range []string{"keeper", "plain"} {
		if err := st.UpsertDeployment(store.Deployment{Slug: slug, Name: "cashpilot-" + slug, Status: "running", Runtime: "existing-docker"}); err != nil {
			t.Fatal(err)
		}
	}
	provider := runtime.NewDockerProvider()
	return &App{
		cfg: cfg, store: st, catalog: cat, runtime: provider,
		services: services.NewManager(provider, cat, st),
		ctx:      context.Background(),
		saveDialog: func(wailsruntime.SaveDialogOptions) (string, error) {
			*dialogs++
			return "", nil
		},
	}
}

// Only the service with critical volumes gets a row, with the catalog's words, and
// it reads "none" until the user confirms, then "confirmed" until they undo it.
func TestStateBackupRowsFollowConfirmations(t *testing.T) {
	var dialogs int
	app := backupApp(t, &dialogs)
	rows := app.stateBackups(app.store.ListDeployments())
	if len(rows) != 1 || rows[0].Slug != "keeper" {
		t.Fatalf("rows = %+v, want keeper only", rows)
	}
	r := rows[0]
	if r.State != statebackup.StateNone || len(r.Volumes) != 1 || r.Volumes[0].Target != "/var/lib/keeper" || !strings.Contains(r.Volumes[0].Holds, "node identity") {
		t.Fatalf("row = %+v", r)
	}
	if err := app.ConfirmStateBackup("keeper"); err != nil {
		t.Fatal(err)
	}
	if got := app.stateBackups(app.store.ListDeployments())[0]; got.State != statebackup.StateConfirmed || got.Method != "manual" {
		t.Fatalf("after confirm = %+v", got)
	}
	if err := app.UndoStateBackup("keeper"); err != nil {
		t.Fatal(err)
	}
	if got := app.stateBackups(app.store.ListDeployments())[0]; got.State != statebackup.StateNone {
		t.Fatalf("after undo = %+v", got)
	}
}

func TestConfirmRefusesAServiceWithNothingToBackUp(t *testing.T) {
	var dialogs int
	app := backupApp(t, &dialogs)
	if err := app.ConfirmStateBackup("plain"); err == nil {
		t.Fatal("confirmed a backup of a service with no irreplaceable state")
	}
	if err := app.ConfirmStateBackup("absent"); err == nil {
		t.Fatal("confirmed a backup of a service that is not deployed")
	}
}

// A bad secret is refused before the save dialog opens or anything is read.
func TestExportRefusesBadSecretsBeforeAsking(t *testing.T) {
	var dialogs int
	app := backupApp(t, &dialogs)
	for name, args := range map[string][3]string{
		"passphrases differ": {"correct horse battery", "correct horse batterY", ""},
		"too short":          {"short", "short", ""},
		"both given":         {"correct horse battery", "correct horse battery", strings.Repeat("ab", 32)},
		"neither given":      {"", "", ""},
	} {
		if _, err := app.ExportStateBackup("keeper", args[0], args[1], args[2]); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := app.ExportStateBackup("plain", "correct horse battery", "correct horse battery", ""); err == nil {
		t.Error("exported a service with no irreplaceable state")
	}
	if dialogs != 0 {
		t.Errorf("the save dialog opened %d times for a refused export", dialogs)
	}
	// Control: good input reaches the dialog, and dismissing it is not an error.
	if path, err := app.ExportStateBackup("keeper", "correct horse battery", "correct horse battery", ""); err != nil || path != "" || dialogs != 1 {
		t.Fatalf("good input: path %q, err %v, dialogs %d", path, err, dialogs)
	}
	if c, _ := app.store.StateBackup("keeper"); c != nil {
		t.Errorf("a dismissed export was recorded as a backup: %+v", c)
	}
}

// stateStub is the Docker provider with a fixed state archive in place of the
// daemon's.
type stateStub struct {
	*runtime.DockerProvider
	payload []byte
}

func (s stateStub) StateFingerprint(context.Context, string, []string) (string, error) {
	return "fp", nil
}

func (s stateStub) ReadState(context.Context, string, []string, int64) ([]byte, []string, error) {
	return s.payload, []string{"/var/lib/keeper"}, nil
}

func dirsTar(t *testing.T, n int) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for i := 0; i < n; i++ {
		if err := w.WriteHeader(&tar.Header{Name: fmt.Sprintf("keeper/%d/", i), Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// An export that restore would refuse (here: past the entry cap) is not saved and
// not recorded as a backup; one that restore accepts is both.
func TestExportRefusesWhatRestoreRefuses(t *testing.T) {
	var dialogs int
	app := backupApp(t, &dialogs)
	dest := filepath.Join(t.TempDir(), "keeper.cpbackup")
	app.saveDialog = func(wailsruntime.SaveDialogOptions) (string, error) { return dest, nil }
	export := func(payload []byte) (string, error) {
		app.services = services.NewManager(stateStub{runtime.NewDockerProvider(), payload}, app.catalog, app.store)
		return app.ExportStateBackup("keeper", "correct horse battery", "correct horse battery", "")
	}

	if _, err := export(dirsTar(t, statebackup.MaxArchiveEntries+1)); err == nil || !strings.Contains(err.Error(), "entry cap") {
		t.Fatalf("exported a backup restore refuses: %v", err)
	}
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("the refused backup was written: %v", err)
	}
	if c, _ := app.store.StateBackup("keeper"); c != nil {
		t.Fatalf("a refused export was recorded as a backup: %+v", c)
	}

	// Control: a restorable payload is saved and confirmed.
	if path, err := export(dirsTar(t, 3)); err != nil || path != dest {
		t.Fatalf("good payload: path %q, err %v", path, err)
	}
	if c, _ := app.store.StateBackup("keeper"); c == nil || c.Method != "export" {
		t.Fatalf("the export was not recorded: %+v", c)
	}
}
