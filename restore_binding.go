package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/i18n"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/statebackup"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// Settings > Restore from a backup (issue #298): puts a node identity or wallet back
// on this machine from a .cpbackup file. Desktop port of the web's restore
// (GeiserX/CashPilot #741, #768), with the same rules:
//
//   - It only creates: the service must not be on this machine in any state, and its
//     identity volume must not exist (services.Manager.RestoreRefusal).
//   - Passphrase files only. A file sealed to a key stays on cpbackup: its private
//     key never goes through the app.
//   - A file this app did not export needs the service's name typed, because nobody
//     can tell from the file whose identity is inside. The export records each
//     file's SHA-256 for this (store.RecordExportedBackup).
//   - One identity, one machine: a file this app does not know is refused while the
//     machine named inside it runs the service, as far as this app's fleet list
//     shows (the web's made_on rule). The header is used only to refuse more. A file
//     this app exported needs the name typed while any fleet machine runs the
//     service: the identity may have moved there, and Desktop has no ledger to tell.
//     A fleet list that cannot be read refuses.
//   - The frontend asks the user to confirm, in place of the web's password step-up:
//     Desktop has one user.
//
// The runtime creates without starting, writes, reads back and compares, and starts
// only once the data is the backup (internal/runtime/restore.go).

// restoreFileTTL is how long a chosen file stays usable. The file is never copied;
// only its path and SHA-256 are kept, and the restore reads it again.
const restoreFileTTL = 15 * time.Minute

// restoreFile is a file the user chose and the preview checked.
type restoreFile struct {
	path   string
	sha256 string
	slug   string
	chosen time.Time
}

// RestoreVolume is one volume a restore creates.
type RestoreVolume struct {
	Target string `json:"target"`
	Volume string `json:"volume"`
}

// RestorePreview is what restoring the chosen file would do on this machine.
type RestorePreview struct {
	// Token names the chosen file in RestoreStateBackup. Single use.
	Token   string `json:"token"`
	File    string `json:"file"`
	Slug    string `json:"slug"`
	Service string `json:"service"`
	Bytes   int    `json:"bytes"`
	SHA256  string `json:"sha256"`
	// Known is true when this app exported this exact file. Otherwise the restore
	// needs the service's name typed.
	Known bool `json:"known"`
	// MadeOn is the machine the file's header names. Anyone with the passphrase can
	// write a header, so it is shown and used only to refuse more.
	MadeOn    string          `json:"madeOn"`
	Container string          `json:"container"`
	Creates   []RestoreVolume `json:"creates"`
	// OK is false when this machine refuses the restore now; Why says why.
	OK  bool   `json:"ok"`
	Why string `json:"why"`
	// RunsOn names the fleet machines that report this service now. One of them
	// may run the very identity inside the file, known or not.
	RunsOn []string `json:"runsOn"`
	// NeedsName is true when the restore needs the service's name typed: the file
	// is not known, or a fleet machine runs the service now.
	NeedsName bool `json:"needsName"`
}

// UnfinishedRestore is a restore that is running or did not finish.
type UnfinishedRestore struct {
	Slug      string   `json:"slug"`
	Service   string   `json:"service"`
	Volumes   []string `json:"volumes"`
	StartedAt string   `json:"startedAt"`
}

// askWhichFile opens the open-file dialog, or the stand-in a test installed.
func (a *App) askWhichFile(opts wailsruntime.OpenDialogOptions) (string, error) {
	if a.openDialog != nil {
		return a.openDialog(opts)
	}
	return wailsruntime.OpenFileDialog(a.ctx, opts)
}

// ChooseRestoreFile asks for a .cpbackup file and says what restoring it would do.
// Nil when the dialog was dismissed. Nothing secret is asked for yet: a file sealed
// to a key is refused here.
func (a *App) ChooseRestoreFile() (*RestorePreview, error) {
	if err := a.ready(); err != nil {
		return nil, err
	}
	path, err := a.askWhichFile(wailsruntime.OpenDialogOptions{
		Title: i18n.T("go.app.restore.chooseTitle"),
		Filters: []wailsruntime.FileFilter{
			{DisplayName: i18n.T("go.app.backup.filter", "pattern", "*.cpbackup"), Pattern: "*.cpbackup"},
		},
	})
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	data, header, sum, err := readRestoreFile(path)
	if err != nil {
		return nil, err
	}
	slug := header.MetadataString("slug")
	targets, err := a.services.RestoreVolumes(slug)
	if err != nil {
		return nil, err
	}
	preview := &RestorePreview{
		File:      filepath.Base(path),
		Slug:      slug,
		Service:   a.serviceName(slug),
		Bytes:     len(data),
		SHA256:    sum,
		MadeOn:    header.MetadataString("worker"),
		Container: "cashpilot-" + slug,
	}
	for _, target := range sortedKeys(targets) {
		preview.Creates = append(preview.Creates, RestoreVolume{Target: target, Volume: targets[target]})
	}
	if preview.Known, err = a.exportedHere(sum, slug); err != nil {
		return nil, err
	}
	why, runsOn, err := a.restoreCheck(slug, preview.Known, preview.MadeOn)
	if err != nil {
		return nil, err
	}
	preview.Why, preview.OK, preview.RunsOn = why, why == "", runsOn
	preview.NeedsName = needsName(preview.Known, runsOn)

	token, err := restoreToken()
	if err != nil {
		return nil, err
	}
	preview.Token = token
	a.restoreMu.Lock()
	defer a.restoreMu.Unlock()
	a.dropExpiredRestoreFiles(time.Now())
	if a.restoreFiles == nil {
		a.restoreFiles = map[string]restoreFile{}
	}
	a.restoreFiles[token] = restoreFile{path: path, sha256: sum, slug: slug, chosen: time.Now()}
	return preview, nil
}

// RestoreStateBackup restores the file the preview named by token. passphrase opens
// it; typedName is the service's name or slug, needed when this app did not export
// the file. It is used once and never kept or logged.
func (a *App) RestoreStateBackup(token, passphrase, typedName string) (runtime.RestoreResult, error) {
	if err := a.ready(); err != nil {
		return runtime.RestoreResult{}, err
	}
	a.restoreMu.Lock()
	a.dropExpiredRestoreFiles(time.Now())
	chosen, ok := a.restoreFiles[token]
	// Single use: a second press, or a retry after a failure, chooses the file again
	// and sees a fresh preview.
	delete(a.restoreFiles, token)
	a.restoreMu.Unlock()
	if !ok {
		return runtime.RestoreResult{}, errors.New(i18n.T("go.app.restore.error.expired", "minutes", 15))
	}
	data, header, sum, err := readRestoreFile(chosen.path)
	if err != nil {
		return runtime.RestoreResult{}, err
	}
	slug := header.MetadataString("slug")
	if sum != chosen.sha256 || slug != chosen.slug {
		return runtime.RestoreResult{}, errors.New(i18n.T("go.app.restore.error.changed"))
	}
	known, err := a.exportedHere(sum, slug)
	if err != nil {
		return runtime.RestoreResult{}, err
	}
	why, runsOn, err := a.restoreCheck(slug, known, header.MetadataString("worker"))
	if err != nil {
		return runtime.RestoreResult{}, err
	} else if why != "" {
		return runtime.RestoreResult{}, errors.New(i18n.T("go.app.restore.error.refused", "why", strings.TrimSuffix(why, ".")))
	}
	if needsName(known, runsOn) {
		typed := strings.ToLower(strings.TrimSpace(typedName))
		if typed == "" || (typed != strings.ToLower(slug) && typed != strings.ToLower(a.serviceName(slug))) {
			if known {
				return runtime.RestoreResult{}, errors.New(i18n.T("go.app.restore.error.typeNameKnown", "name", a.serviceName(slug), "machines", strings.Join(runsOn, ", ")))
			}
			return runtime.RestoreResult{}, errors.New(i18n.T("go.app.restore.error.typeNameUnknown", "name", a.serviceName(slug)))
		}
	}
	payload, err := statebackup.Open(data, passphrase, "")
	if err != nil {
		return runtime.RestoreResult{}, err
	}
	archives, err := statebackup.SplitArchives(payload)
	if err != nil {
		return runtime.RestoreResult{}, err
	}
	creds, err := a.store.GetCredentials(slug)
	if err != nil {
		return runtime.RestoreResult{}, err
	}
	res, err := a.services.Restore(a.ctx, slug, creds, archives)
	if err != nil {
		a.emitError("restore", err)
		return res, err
	}
	// The user holds the file this node now runs from, so its backup is confirmed.
	live, _ := a.services.StateFingerprint(a.ctx, slug)
	_ = a.store.ConfirmStateBackup(slug, live, "restore", time.Now())
	if dep, ok, err := a.store.GetDeployment(slug); err == nil && ok {
		a.emitEvent("deployment:changed", dep)
	}
	if a.collectors != nil && a.collectors.Supports(slug) {
		go a.collectOne(a.ctx, slug)
	}
	return res, nil
}

// UnfinishedRestores lists the restores that are running or did not finish. While
// one is listed, Deploy and Start refuse its service.
func (a *App) UnfinishedRestores() ([]UnfinishedRestore, error) {
	if err := a.ready(); err != nil {
		return nil, err
	}
	holds, err := a.services.UnfinishedRestores()
	if err != nil {
		return nil, err
	}
	out := []UnfinishedRestore{}
	for _, h := range holds {
		out = append(out, UnfinishedRestore{
			Slug:      h.Slug,
			Service:   a.serviceName(h.Slug),
			Volumes:   sortedKeys(h.Volumes),
			StartedAt: h.StartedAt.Format(time.RFC3339),
		})
	}
	return out, nil
}

// DiscardPartialRestore deletes what an unfinished restore of slug left: its
// container and the volumes the restore made, never a volume another program put
// under the same name. Then Deploy and Start accept the service again.
func (a *App) DiscardPartialRestore(slug string) error {
	if err := a.ready(); err != nil {
		return err
	}
	if err := a.services.DiscardRestore(a.ctx, slug); err != nil {
		a.emitError("restore", err)
		return err
	}
	return nil
}

// readRestoreFile reads a backup file, refuses one that is not a passphrase file,
// and returns it with its header and SHA-256.
func readRestoreFile(path string) ([]byte, statebackup.Header, string, error) {
	data, err := statebackup.ReadBundleFile(path)
	if err != nil {
		return nil, statebackup.Header{}, "", err
	}
	header, err := statebackup.ReadHeader(data)
	if err != nil {
		return nil, statebackup.Header{}, "", err
	}
	if header.Mode != statebackup.ModePassphrase {
		return nil, statebackup.Header{}, "", errors.New(i18n.T("go.app.restore.error.sealedToKey"))
	}
	sum := sha256.Sum256(data)
	return data, header, hex.EncodeToString(sum[:]), nil
}

// exportedHere is whether this app exported this exact file, of this service.
func (a *App) exportedHere(sum, slug string) (bool, error) {
	exported, ok, err := a.store.ExportedBackup(sum)
	if err != nil {
		return false, fmt.Errorf("%s: %w", i18n.T("go.app.restore.error.readExported"), err)
	}
	return ok && exported == slug, nil
}

// fleetRunning names the fleet machines that report slug, sorted. A list it could
// not read is an error, never an empty fleet.
func (a *App) fleetRunning(slug string) ([]string, error) {
	devices, err := a.store.FleetDevices()
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, device := range devices {
		for _, running := range device.Services {
			if strings.TrimSpace(running) == slug {
				names = append(names, strings.TrimSpace(device.Name))
				break
			}
		}
	}
	sort.Strings(names)
	return names, nil
}

// restoreCheck says why slug cannot be restored here now, or "", and which fleet
// machines run it: every check of the manager and the runtime, then the
// one-identity rule. A fleet list it cannot read refuses, since it cannot tell where
// the identity runs. A file this app does not know is refused while the machine its
// header names runs the service: the file may hold the very identity that machine
// runs. An offline machine counts, since it may come back with the node.
func (a *App) restoreCheck(slug string, known bool, madeOn string) (string, []string, error) {
	creds, err := a.store.GetCredentials(slug)
	if err != nil {
		return "", nil, err
	}
	runsOn, fleetErr := a.fleetRunning(slug)
	if why, err := a.services.RestoreRefusal(a.ctx, slug, creds); why != "" || err != nil {
		return why, runsOn, err
	}
	if fleetErr != nil {
		return i18n.T("go.app.restore.why.fleetUnread", "name", a.serviceName(slug), "error", fleetErr), nil, nil
	}
	madeOn = strings.TrimSpace(madeOn)
	if known || madeOn == "" {
		return "", runsOn, nil
	}
	for _, name := range runsOn {
		if strings.EqualFold(name, madeOn) {
			return i18n.T("go.app.restore.why.stillRuns", "machine", name, "name", a.serviceName(slug)), runsOn, nil
		}
	}
	return "", runsOn, nil
}

// needsName is whether a restore needs the service's name typed. A file this app
// did not export: nobody can tell whose identity is inside. A file it did export
// while a fleet machine runs the service: the export record says only that this app
// wrote the file, not where its identity runs now, and Desktop keeps no fingerprint
// to tell that machine's node from the one in the file.
func needsName(known bool, runsOn []string) bool {
	return !known || len(runsOn) > 0
}

// dropExpiredRestoreFiles forgets chosen files older than restoreFileTTL. The
// caller holds restoreMu.
func (a *App) dropExpiredRestoreFiles(now time.Time) {
	for token, f := range a.restoreFiles {
		if now.Sub(f.chosen) > restoreFileTTL {
			delete(a.restoreFiles, token)
		}
	}
}

func restoreToken() (string, error) {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
