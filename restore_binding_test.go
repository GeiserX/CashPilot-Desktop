package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/services"
	"github.com/GeiserX/CashPilot-Desktop/internal/statebackup"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

const restorePass = "correct horse battery"

// restoreStub is the Docker provider with the restore's runtime side replaced: it
// records what it was asked to restore, holds and releases as the runtime does, and
// fails when told to.
type restoreStub struct {
	*runtime.DockerProvider
	refusal  string
	fail     error
	left     []string
	got      *runtime.RestoreRequest
	calls    *int
	discards *int
}

func (s restoreStub) RestoreRefusal(context.Context, string, catalog.Service) (string, error) {
	return s.refusal, nil
}

func (s restoreStub) RestoreState(_ context.Context, req runtime.RestoreRequest) (runtime.RestoreResult, error) {
	*s.calls++
	*s.got = req
	if err := req.Hold(map[string]string{"keeper-data": "nonce"}); err != nil {
		return runtime.RestoreResult{}, err
	}
	if s.fail != nil {
		return runtime.RestoreResult{Left: s.left}, s.fail
	}
	if err := req.Release(); err != nil {
		return runtime.RestoreResult{}, err
	}
	return runtime.RestoreResult{ContainerID: "c1", Name: "cashpilot-keeper", Image: "keeper/node:1.0.0", Targets: []string{"/var/lib/keeper"}, Files: 1, Started: true}, nil
}

func (s restoreStub) DiscardRestore(context.Context, string, map[string]string) ([]string, error) {
	*s.discards++
	return nil, nil
}

func (s restoreStub) StateFingerprint(context.Context, string, []string) (string, error) {
	return "fp", nil
}

// restoreApp is backupApp with keeper not deployed (a restore only creates) and the
// stub runtime. The file chooser returns *chosen.
type restoreHarness struct {
	app      *App
	stub     *restoreStub
	chosen   string
	calls    int
	discards int
	req      runtime.RestoreRequest
}

func newRestoreHarness(t *testing.T) *restoreHarness {
	t.Helper()
	var dialogs int
	h := &restoreHarness{app: backupApp(t, &dialogs)}
	if err := h.app.store.DeleteDeployment("keeper"); err != nil {
		t.Fatal(err)
	}
	h.stub = &restoreStub{DockerProvider: runtime.NewDockerProvider(), got: &h.req, calls: &h.calls, discards: &h.discards}
	h.use()
	h.app.openDialog = func(wailsruntime.OpenDialogOptions) (string, error) { return h.chosen, nil }
	return h
}

// use installs the stub as it is now.
func (h *restoreHarness) use() {
	h.app.services = services.NewManager(*h.stub, h.app.catalog, h.app.store)
}

// keeperFile seals the keeper folder into a passphrase file the way an export does,
// and makes it the file the chooser returns.
func (h *restoreHarness) keeperFile(t *testing.T, worker string) string {
	t.Helper()
	bundle, err := statebackup.Seal(dirsTar(t, 2), restorePass, "", map[string]any{
		"slug": "keeper", "worker": worker, "targets": []string{"/var/lib/keeper"},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.chosen = filepath.Join(t.TempDir(), "keeper.cpbackup")
	if err := os.WriteFile(h.chosen, bundle, 0o600); err != nil {
		t.Fatal(err)
	}
	return h.chosen
}

func (h *restoreHarness) choose(t *testing.T) *RestorePreview {
	t.Helper()
	p, err := h.app.ChooseRestoreFile()
	if err != nil {
		t.Fatal(err)
	}
	if p == nil {
		t.Fatal("no preview")
	}
	return p
}

// A file this app did not make needs the service's name typed, and the restore then
// runs once: the archives reach the runtime, the deployment is recorded, the backup
// counts as confirmed, and the hold is gone.
func TestRestoreOfAForeignFileNeedsTheNameTyped(t *testing.T) {
	h := newRestoreHarness(t)
	h.keeperFile(t, "some-box")
	p := h.choose(t)
	if p.Known || !p.OK || p.Slug != "keeper" || p.Service != "Keeper" || p.MadeOn != "some-box" ||
		len(p.Creates) != 1 || p.Creates[0] != (RestoreVolume{Target: "/var/lib/keeper", Volume: "keeper-data"}) {
		t.Fatalf("preview = %+v", p)
	}
	if _, err := h.app.RestoreStateBackup(p.Token, restorePass, "plain"); err == nil || !strings.Contains(err.Error(), "type Keeper") {
		t.Fatalf("restored a foreign file without its name: %v", err)
	}
	if h.calls != 0 {
		t.Fatal("the runtime was asked to restore")
	}
	// The token was used: the retry chooses the file again.
	if _, err := h.app.RestoreStateBackup(p.Token, restorePass, "keeper"); err == nil || !strings.Contains(err.Error(), "choose the backup file again") {
		t.Fatalf("a used token restored: %v", err)
	}
	p = h.choose(t)
	res, err := h.app.RestoreStateBackup(p.Token, restorePass, " KEEPER ")
	if err != nil {
		t.Fatal(err)
	}
	if h.calls != 1 || !res.Started || len(h.req.Archives["keeper"]) == 0 || len(h.req.Archives) != 1 {
		t.Fatalf("calls %d, result %+v, archives %v", h.calls, res, len(h.req.Archives))
	}
	if dep, ok, _ := h.app.store.GetDeployment("keeper"); !ok || dep.Status != "running" || dep.ContainerID != "c1" {
		t.Fatalf("deployment = %+v %v", dep, ok)
	}
	if c, _ := h.app.store.StateBackup("keeper"); c == nil || c.Method != "restore" {
		t.Fatalf("backup confirmation = %+v", c)
	}
	if hold, _ := h.app.store.RestoreHeld("keeper"); hold != nil {
		t.Fatalf("hold left after a restore that finished: %+v", hold)
	}
}

// A file this app exported restores with no name typed: the export recorded its hash.
func TestRestoreOfAnExportedFileNeedsNoName(t *testing.T) {
	h := newRestoreHarness(t)
	// Export through the real binding, with keeper deployed and a stub state read.
	if err := h.app.store.UpsertDeployment(store.Deployment{Slug: "keeper", Name: "cashpilot-keeper", Status: "running", Runtime: "existing-docker"}); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "keeper.cpbackup")
	h.app.saveDialog = func(wailsruntime.SaveDialogOptions) (string, error) { return dest, nil }
	h.app.services = services.NewManager(stateStub{runtime.NewDockerProvider(), dirsTar(t, 2)}, h.app.catalog, h.app.store)
	if _, err := h.app.ExportStateBackup("keeper", restorePass, restorePass, ""); err != nil {
		t.Fatal(err)
	}
	if err := h.app.store.DeleteDeployment("keeper"); err != nil {
		t.Fatal(err)
	}
	h.use()
	h.chosen = dest
	p := h.choose(t)
	if !p.Known || !p.OK {
		t.Fatalf("the app's own export is not known: %+v", p)
	}
	if _, err := h.app.RestoreStateBackup(p.Token, restorePass, ""); err != nil {
		t.Fatal(err)
	}
	// The same file with one byte changed is another file: not known.
	data, _ := os.ReadFile(dest)
	data = append(data, 0)
	altered := filepath.Join(t.TempDir(), "altered.cpbackup")
	_ = os.WriteFile(altered, data, 0o600)
	_ = h.app.store.DeleteDeployment("keeper")
	h.chosen = altered
	if p := h.choose(t); p.Known {
		t.Fatal("a changed file counted as the app's own export")
	}
}

// A file this app exported says only that this app wrote those bytes, not where the
// identity inside runs now: it may have moved to a fleet machine since. While one
// reports the service, the preview names it and the restore needs the name typed,
// as for a file this app does not know.
func TestRestoreOfAnExportedFileWhileTheFleetRunsTheService(t *testing.T) {
	h := newRestoreHarness(t)
	if err := h.app.store.UpsertDeployment(store.Deployment{Slug: "keeper", Name: "cashpilot-keeper", Status: "running", Runtime: "existing-docker"}); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "keeper.cpbackup")
	h.app.saveDialog = func(wailsruntime.SaveDialogOptions) (string, error) { return dest, nil }
	h.app.services = services.NewManager(stateStub{runtime.NewDockerProvider(), dirsTar(t, 2)}, h.app.catalog, h.app.store)
	if _, err := h.app.ExportStateBackup("keeper", restorePass, restorePass, ""); err != nil {
		t.Fatal(err)
	}
	if err := h.app.store.DeleteDeployment("keeper"); err != nil {
		t.Fatal(err)
	}
	h.use()
	if _, err := h.app.store.UpsertFleetDevice(store.FleetDevice{Name: "Worker-B", Services: []string{"keeper"}}); err != nil {
		t.Fatal(err)
	}
	h.chosen = dest
	p := h.choose(t)
	if !p.Known || !p.OK || !p.NeedsName || len(p.RunsOn) != 1 || p.RunsOn[0] != "Worker-B" {
		t.Fatalf("preview = %+v", p)
	}
	if _, err := h.app.RestoreStateBackup(p.Token, restorePass, ""); err == nil || !strings.Contains(err.Error(), "runs now on Worker-B") {
		t.Fatalf("restored a known file the fleet may run, with no name typed: %v", err)
	}
	if h.calls != 0 {
		t.Fatal("the runtime was asked to restore")
	}
	p = h.choose(t)
	if _, err := h.app.RestoreStateBackup(p.Token, restorePass, "Keeper"); err != nil {
		t.Fatal(err)
	}
	if h.calls != 1 {
		t.Fatalf("calls = %d", h.calls)
	}
}

// The one-identity rule refuses when it cannot read the fleet list: an unreadable
// list is not a fleet that runs nothing.
func TestRestoreRefusesWhenTheFleetCannotBeRead(t *testing.T) {
	h := newRestoreHarness(t)
	h.keeperFile(t, "Other-Box")
	if p := h.choose(t); !p.OK {
		t.Fatalf("control: %+v", p)
	}
	if _, err := rawDB(t, h.app.cfg.DataDir()).Exec(`DROP TABLE fleet_devices`); err != nil {
		t.Fatal(err)
	}
	p := h.choose(t)
	if p.OK || !strings.Contains(p.Why, "could not read which fleet machines run Keeper") {
		t.Fatalf("preview = %+v", p)
	}
	if _, err := h.app.RestoreStateBackup(p.Token, restorePass, "keeper"); err == nil || !strings.Contains(err.Error(), "could not read which fleet machines") {
		t.Fatalf("restored with the fleet unread: %v", err)
	}
	if h.calls != 0 {
		t.Fatal("the runtime was asked to restore")
	}
}

// A file sealed to a key is refused before anything secret is asked for.
func TestRestoreRefusesAKeySealedFile(t *testing.T) {
	h := newRestoreHarness(t)
	_, public, err := statebackup.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := statebackup.Seal(dirsTar(t, 1), "", public, map[string]any{"slug": "keeper"})
	if err != nil {
		t.Fatal(err)
	}
	h.chosen = filepath.Join(t.TempDir(), "sealed.cpbackup")
	_ = os.WriteFile(h.chosen, bundle, 0o600)
	if p, err := h.app.ChooseRestoreFile(); err == nil || !strings.Contains(err.Error(), "sealed to a key") {
		t.Fatalf("preview %+v, err %v", p, err)
	}
}

// Every refusal stops the restore before the runtime is asked, and the preview says
// why: the service is deployed here, the runtime refuses (its volume exists), the
// machine named in a foreign file runs the service, a wrong passphrase, a file that
// changed since it was chosen, a service the app cannot restore.
func TestRestoreRefusals(t *testing.T) {
	h := newRestoreHarness(t)
	h.keeperFile(t, "other-box")

	// Deployed here.
	_ = h.app.store.UpsertDeployment(store.Deployment{Slug: "keeper", Name: "cashpilot-keeper", Status: "exited", Runtime: "existing-docker"})
	if p := h.choose(t); p.OK || !strings.Contains(p.Why, "already deployed") {
		t.Fatalf("deployed: %+v", p)
	}
	_ = h.app.store.DeleteDeployment("keeper")

	// The runtime's own refusal (a container or the volume is there).
	h.stub.refusal = "the volume keeper-data already exists on this machine"
	h.use()
	p := h.choose(t)
	if p.OK || p.Why != h.stub.refusal {
		t.Fatalf("runtime refusal: %+v", p)
	}
	if _, err := h.app.RestoreStateBackup(p.Token, restorePass, "keeper"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("restored over a refusal: %v", err)
	}
	h.stub.refusal = ""
	h.use()

	// The machine the foreign file names still runs the service.
	if _, err := h.app.store.UpsertFleetDevice(store.FleetDevice{Name: "Other-Box", Services: []string{"keeper"}}); err != nil {
		t.Fatal(err)
	}
	p = h.choose(t)
	if p.OK || !strings.Contains(p.Why, "made on Other-Box, which still runs Keeper") || len(p.RunsOn) != 1 {
		t.Fatalf("identity elsewhere: %+v", p)
	}
	if _, err := h.app.RestoreStateBackup(p.Token, restorePass, "keeper"); err == nil || !strings.Contains(err.Error(), "still runs Keeper") {
		t.Fatalf("restored an identity another machine runs: %v", err)
	}
	// A machine the file does not name only shows up as running the service.
	h.keeperFile(t, "third-box")
	if p := h.choose(t); !p.OK || len(p.RunsOn) != 1 || p.RunsOn[0] != "Other-Box" {
		t.Fatalf("another machine: %+v", p)
	}

	// Wrong passphrase.
	p = h.choose(t)
	if _, err := h.app.RestoreStateBackup(p.Token, "not the passphrase", "keeper"); err == nil || !strings.Contains(err.Error(), "could not decrypt") {
		t.Fatalf("wrong passphrase: %v", err)
	}

	// The file changed after the preview.
	p = h.choose(t)
	h.keeperFile(t, "third-box")
	other := h.chosen
	data, _ := os.ReadFile(other)
	h.app.restoreMu.Lock()
	f := h.app.restoreFiles[p.Token]
	h.app.restoreMu.Unlock()
	_ = os.WriteFile(f.path, data, 0o600) // same path, new contents (a new salt)
	if _, err := h.app.RestoreStateBackup(p.Token, restorePass, "keeper"); err == nil || !strings.Contains(err.Error(), "changed since you chose it") {
		t.Fatalf("restored a file that changed: %v", err)
	}

	// An expired choice.
	p = h.choose(t)
	h.app.restoreMu.Lock()
	f = h.app.restoreFiles[p.Token]
	f.chosen = time.Now().Add(-restoreFileTTL - time.Second)
	h.app.restoreFiles[p.Token] = f
	h.app.restoreMu.Unlock()
	if _, err := h.app.RestoreStateBackup(p.Token, restorePass, "keeper"); err == nil || !strings.Contains(err.Error(), "15 minutes") {
		t.Fatalf("an expired choice restored: %v", err)
	}

	// A service with nothing a backup restores.
	bundle, _ := statebackup.Seal(dirsTar(t, 1), restorePass, "", map[string]any{"slug": "plain"})
	h.chosen = filepath.Join(t.TempDir(), "plain.cpbackup")
	_ = os.WriteFile(h.chosen, bundle, 0o600)
	if _, err := h.app.ChooseRestoreFile(); err == nil || !strings.Contains(err.Error(), "nothing a backup restores") {
		t.Fatalf("plain: %v", err)
	}

	if h.calls != 0 {
		t.Fatalf("the runtime was asked to restore %d times", h.calls)
	}
}

// A restore whose undo could not remove what it made keeps the service held: Deploy
// and Start refuse it, the settings list it, and deleting the partial restore lifts
// the hold. One the undo cleaned up is not held.
func TestAnUnfinishedRestoreHoldsTheService(t *testing.T) {
	h := newRestoreHarness(t)
	h.keeperFile(t, "")

	h.stub.fail = errors.New("read back differs. The restore was undone.")
	h.use()
	p := h.choose(t)
	if _, err := h.app.RestoreStateBackup(p.Token, restorePass, "keeper"); err == nil {
		t.Fatal("a failed restore reported success")
	}
	if hold, _ := h.app.store.RestoreHeld("keeper"); hold != nil {
		t.Fatalf("a restore the undo cleaned up is still held: %+v", hold)
	}

	h.stub.left = []string{"keeper-data"}
	h.use()
	p = h.choose(t)
	if _, err := h.app.RestoreStateBackup(p.Token, restorePass, "keeper"); err == nil {
		t.Fatal("a failed restore reported success")
	}
	list, err := h.app.UnfinishedRestores()
	if err != nil || len(list) != 1 || list[0].Slug != "keeper" || list[0].Service != "Keeper" || len(list[0].Volumes) != 1 || list[0].Volumes[0] != "keeper-data" {
		t.Fatalf("unfinished = %+v, %v", list, err)
	}
	ctx := context.Background()
	if _, err := h.app.services.DeployWith(ctx, "keeper", nil, services.DeployOptions{}); err == nil || !strings.Contains(err.Error(), "half-written") {
		t.Fatalf("deploy of a held service: %v", err)
	}
	if err := h.app.services.Start(ctx, "keeper"); err == nil || !strings.Contains(err.Error(), "half-written") {
		t.Fatalf("start of a held service: %v", err)
	}
	if p := h.choose(t); p.OK || !strings.Contains(p.Why, "did not finish") {
		t.Fatalf("restore of a held service: %+v", p)
	}

	if err := h.app.DiscardPartialRestore("keeper"); err != nil {
		t.Fatal(err)
	}
	if h.discards != 1 {
		t.Fatalf("discards = %d", h.discards)
	}
	if list, _ := h.app.UnfinishedRestores(); len(list) != 0 {
		t.Fatalf("still listed: %+v", list)
	}
	if err := h.app.DiscardPartialRestore("keeper"); err == nil {
		t.Fatal("discarded a restore that is not unfinished")
	}
}

func TestChooseRestoreFileDismissed(t *testing.T) {
	h := newRestoreHarness(t)
	h.chosen = ""
	if p, err := h.app.ChooseRestoreFile(); p != nil || err != nil {
		t.Fatalf("dismissed: %+v, %v", p, err)
	}
}
