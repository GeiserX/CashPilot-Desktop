package main

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/statebackup"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// Some services keep a node identity, a keystore or a wallet on this machine's disk
// and nowhere else. The removal guard already refuses to delete those volumes; these
// bindings tell the user to keep a copy, let them say they have one, and save an
// encrypted one. Desktop port of CashPilot's state backups (web PR #530).

// stateBackups is the card's rows: every deployed service whose catalog entry has
// critical volumes, with whether its backup is confirmed.
func (a *App) stateBackups(deployments []store.Deployment) []statebackup.Row {
	rows := []statebackup.Row{}
	now := time.Now()
	for _, dep := range deployments {
		volumes := a.services.CriticalVolumes(dep.Slug)
		if len(volumes) == 0 {
			continue
		}
		live, _ := a.services.StateFingerprint(a.ctx, dep.Slug)
		found, err := a.store.StateBackup(dep.Slug)
		if err != nil {
			// Unknown means "not confirmed", never "confirmed".
			found = nil
		}
		row := statebackup.Row{
			Slug:          dep.Slug,
			Name:          a.serviceName(dep.Slug),
			Volumes:       volumes,
			State:         statebackup.Judge(found, live, now),
			TracksChanges: live != "",
		}
		if found != nil {
			row.ConfirmedAt = found.ConfirmedAt.Format(time.RFC3339)
			row.Method = found.Method
		}
		rows = append(rows, row)
	}
	return rows
}

// backupTarget refuses a service that is not deployed here or keeps nothing
// irreplaceable: a confirmation of either would be a confirmation of nothing.
func (a *App) backupTarget(slug string) error {
	if len(a.services.CriticalVolumes(slug)) == 0 {
		return fmt.Errorf("%s keeps no irreplaceable state to back up", slug)
	}
	if _, ok, err := a.store.GetDeployment(slug); err != nil || !ok {
		return fmt.Errorf("%s is not deployed on this machine", slug)
	}
	return nil
}

// ConfirmStateBackup records the user's word that slug's state is backed up, with
// the fingerprint it has now, so a new volume later voids it.
func (a *App) ConfirmStateBackup(slug string) error {
	if err := a.ready(); err != nil {
		return err
	}
	if err := a.backupTarget(slug); err != nil {
		return err
	}
	live, _ := a.services.StateFingerprint(a.ctx, slug)
	return a.store.ConfirmStateBackup(slug, live, "manual", time.Now())
}

// UndoStateBackup takes a confirmation back.
func (a *App) UndoStateBackup(slug string) error {
	if err := a.ready(); err != nil {
		return err
	}
	return a.store.ClearStateBackup(slug)
}

var unsafeFileChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// ExportStateBackup saves an encrypted backup of slug's critical volumes where the
// user chooses, and counts as a confirmation. Exactly one of passphrase (12+
// characters, typed twice) or recipientPublicKey (X25519, hex) is given. Neither is
// kept or logged. Returns the path written, or "" when the dialog was dismissed.
func (a *App) ExportStateBackup(slug, passphrase, passphraseRepeat, recipientPublicKey string) (string, error) {
	if err := a.ready(); err != nil {
		return "", err
	}
	recipientPublicKey = strings.TrimSpace(recipientPublicKey)
	if (passphrase == "") == (recipientPublicKey == "") {
		return "", errors.New("give either a passphrase or a recipient public key, not both")
	}
	if passphrase != "" {
		// A typo gives a file nobody can open, and the export marks the service
		// as backed up. Compared, never echoed.
		if subtle.ConstantTimeCompare([]byte(passphrase), []byte(passphraseRepeat)) != 1 {
			return "", errors.New("the two passphrases differ; type it again")
		}
		if len([]rune(passphrase)) < statebackup.MinPassphraseLength {
			return "", fmt.Errorf("the passphrase must be at least %d characters", statebackup.MinPassphraseLength)
		}
	}
	if err := a.backupTarget(slug); err != nil {
		return "", err
	}

	host := strings.Trim(unsafeFileChars.ReplaceAllString(runtime.DeviceHostname(), "-"), "-")
	if host == "" {
		host = "desktop"
	}
	path, err := a.askWhereToSave(wailsruntime.SaveDialogOptions{
		Title:                "Save encrypted backup",
		DefaultFilename:      fmt.Sprintf("cashpilot-%s-%s-%s.cpbackup", slug, host, time.Now().UTC().Format("20060102")),
		CanCreateDirectories: true,
		Filters: []wailsruntime.FileFilter{
			{DisplayName: "CashPilot backups (*.cpbackup)", Pattern: "*.cpbackup"},
		},
	})
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(path) == "" {
		return "", nil
	}

	payload, targets, err := a.services.ReadState(a.ctx, slug)
	if err != nil {
		return "", err
	}
	// Run the restore's own checks now, while the user can still act: a backup
	// that cpbackup would refuse must not be saved and counted as one.
	if _, err := statebackup.SplitArchives(payload); err != nil {
		return "", fmt.Errorf("the backup was not saved, because a restore would refuse it: %w", err)
	}
	bundle, err := statebackup.Seal(payload, passphrase, recipientPublicKey, map[string]any{
		"slug":    slug,
		"worker":  runtime.DeviceHostname(),
		"targets": targets,
	})
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, bundle, 0o600); err != nil {
		return "", fmt.Errorf("could not save the backup: %w", err)
	}
	live, _ := a.services.StateFingerprint(a.ctx, slug)
	if err := a.store.ConfirmStateBackup(slug, live, "export", time.Now()); err != nil {
		return path, fmt.Errorf("the backup was saved to %s, but could not be recorded as confirmed: %w", path, err)
	}
	return path, nil
}
