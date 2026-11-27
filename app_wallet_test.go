package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"
)

// THE RULE: a wallet seed phrase is the wallet, so Desktop never keeps one. Sia's
// hostd reads it from /data/hostd.yml in its own data volume, and that file is the
// only copy: not the app's store, not the container's environment (docker inspect,
// config.v2.json, /proc/<pid>/environ show that in plain text), not the history,
// not the window, not the compose export.
//
// These tests drive only bindings that existed before the seed file did, so they
// run unchanged against the code before it, where every one of them fails.

// The phrases are the public BIP-39 test vectors, never a wallet anybody uses.
const (
	seedPhraseA = "legal winner thank year wave sausage worth useful legal winner thank yellow"
	seedPhraseB = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
	// seedAddressB is the address go.sia.tech/coreutils derives for seedPhraseB.
	seedAddressB = "a2a3773f76136bdb05a0ff79a0f4fcc2826436794f8db36db6408355c5ca32345002db43c2eb"

	siaSlug      = "sia-hostd"
	siaSeedKey   = "HOSTD_WALLET_SEED"
	siaVolume    = "sia-hostd-data"
	siaSeedFile  = "hostd.yml"
	siaContainer = "cashpilot-sia-hostd"
)

// siaSettings are the settings Sia needs besides the seed.
func siaSettings() map[string]string {
	return map[string]string{"HOSTD_API_PASSWORD": "a long dashboard passphrase", "SIA_STORAGE_DIR": "/srv/sia"}
}

// assertSeedNowhere fails when phrase is in any container's settings (the ones a
// deploy removed again included), in the stored credentials, in what the window is
// sent, or anywhere in the app's database file in plain text (history included).
func assertSeedNowhere(t *testing.T, app *appUnderTest, phrase string) {
	t.Helper()
	for _, c := range app.Docker.Created() {
		settings := strings.Join(c.Env, "\n") + strings.Join(c.Cmd, " ") + strings.Join(c.Entrypoint, " ")
		for k, v := range c.Labels {
			settings += k + "=" + v
		}
		if strings.Contains(settings, phrase) {
			t.Errorf("the seed phrase is in the settings of container %s", c.Name)
		}
		for _, kv := range c.Env {
			if strings.HasPrefix(kv, siaSeedKey+"=") {
				t.Errorf("container %s was created with %s in its environment", c.Name, siaSeedKey)
			}
		}
	}
	stored, err := app.store.GetCredentials(siaSlug)
	if err != nil {
		t.Fatalf("GetCredentials (store): %v", err)
	}
	if _, ok := stored[siaSeedKey]; ok {
		t.Errorf("the app's store still holds %s", siaSeedKey)
	}
	shown, err := app.GetCredentials(siaSlug)
	if err != nil {
		t.Fatalf("GetCredentials (binding): %v", err)
	}
	for k, v := range shown.Values {
		if k == siaSeedKey || strings.Contains(v, phrase) {
			t.Errorf("the window is sent the seed phrase (%s)", k)
		}
	}
	db, err := sql.Open("sqlite", filepath.Join(app.cfg.DataDir(), "cashpilot-desktop.db"))
	if err != nil {
		t.Fatalf("open the database: %v", err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT slug, event, detail FROM runtime_events`)
	if err != nil {
		t.Fatalf("read the history: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var slug, event, detail string
		if err := rows.Scan(&slug, &event, &detail); err != nil {
			t.Fatalf("scan the history: %v", err)
		}
		if strings.Contains(detail, phrase) || strings.Contains(detail, strings.Join(strings.Fields(phrase)[:3], " ")) {
			t.Errorf("the history entry %s/%s holds the seed phrase", slug, event)
		}
	}
}

// assertSeedFile fails unless the volume holds the file with phrase, readable by
// root only.
func assertSeedFile(t *testing.T, app *appUnderTest, phrase string) {
	t.Helper()
	f, ok := app.Docker.VolumeFile(siaVolume, siaSeedFile)
	if !ok {
		t.Fatalf("%s has no %s", siaVolume, siaSeedFile)
	}
	if !strings.Contains(string(f.Data), "recoveryPhrase: "+phrase) {
		t.Errorf("%s does not hold the wallet's phrase under recoveryPhrase", siaSeedFile)
	}
	if f.Mode&0o077 != 0 || f.UID != 0 {
		t.Errorf("%s is mode %o owned by %d, want root-only", siaSeedFile, f.Mode, f.UID)
	}
}

// A node deployed before the seed file had its phrase in Desktop's store and in
// its container's environment. The next deploy from the stored settings (the drift
// Redeploy, the isolation redeploy, the importer's deploy all send {}) writes it
// into the file, deploys without the variable and deletes the stored copy.
func TestAStoredSiaSeedMovesIntoTheVolumeAndOutOfEverythingElse(t *testing.T) {
	app := newAppUnderTest(t)
	legacy := siaSettings()
	legacy[siaSeedKey] = seedPhraseA
	if err := app.store.SaveCredentials(siaSlug, legacy); err != nil {
		t.Fatalf("store the old settings: %v", err)
	}
	oldBlob := storedBlob(t, app)

	if _, err := app.DeployService(siaSlug, map[string]string{}, nil, nil); err != nil {
		t.Fatalf("DeployService: %v", err)
	}
	assertBlobGoneFromDisk(t, app, oldBlob)

	c, ok := app.Docker.Container(siaContainer)
	if !ok {
		t.Fatalf("%s was not created", siaContainer)
	}
	if !containsEnv(c.Env, "HOSTD_API_PASSWORD=a long dashboard passphrase") {
		t.Errorf("the dashboard password is an ordinary setting and must still reach the node: %v", c.Env)
	}
	assertSeedFile(t, app, seedPhraseA)
	assertSeedNowhere(t, app, seedPhraseA)
	stored, _ := app.store.GetCredentials(siaSlug)
	if stored["HOSTD_API_PASSWORD"] == "" || stored["SIA_STORAGE_DIR"] == "" {
		t.Errorf("deleting the stored seed took the other settings with it: %v", keysOf(stored))
	}

	// A second redeploy keeps the file and still passes no seed.
	if _, err := app.DeployService(siaSlug, map[string]string{}, nil, nil); err != nil {
		t.Fatalf("second DeployService: %v", err)
	}
	assertSeedFile(t, app, seedPhraseA)
	assertSeedNowhere(t, app, seedPhraseA)
}

// A node whose phrase Desktop no longer holds still has it in the running
// container's environment: that is the wallet holding its funds, so the deploy that
// replaces the container moves it into the file rather than losing it.
func TestTheRunningContainersSeedMovesIntoTheFile(t *testing.T) {
	app := newAppUnderTest(t)
	cli, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		t.Fatalf("docker client: %v", err)
	}
	defer cli.Close()
	if _, err := cli.ContainerCreate(context.Background(), client.ContainerCreateOptions{
		Name: siaContainer,
		Config: &container.Config{
			Image:  "ghcr.io/siafoundation/hostd",
			Env:    []string{siaSeedKey + "=" + seedPhraseA, "HOSTD_API_PASSWORD=x"},
			Labels: map[string]string{"cashpilot.managed": "true", "cashpilot.runtime": "existing-docker", "cashpilot.service": siaSlug},
		},
	}); err != nil {
		t.Fatalf("create the old container: %v", err)
	}
	if err := app.store.SaveCredentials(siaSlug, siaSettings()); err != nil {
		t.Fatalf("store the settings: %v", err)
	}

	if _, err := app.DeployService(siaSlug, map[string]string{}, nil, nil); err != nil {
		t.Fatalf("DeployService: %v", err)
	}
	assertSeedFile(t, app, seedPhraseA)
	got, _ := app.Docker.Container(siaContainer)
	for _, kv := range got.Env {
		if strings.HasPrefix(kv, siaSeedKey+"=") {
			t.Fatalf("the replacement still carries %s", siaSeedKey)
		}
	}
}

// The deploy form is filled from GetCredentials. A seed stored before the file
// existed is never sent to it, and saving the form without it does not delete it
// before a deploy has moved it.
func TestTheWindowNeverGetsTheSeedBack(t *testing.T) {
	app := newAppUnderTest(t)
	legacy := siaSettings()
	legacy[siaSeedKey] = seedPhraseA
	if err := app.store.SaveCredentials(siaSlug, legacy); err != nil {
		t.Fatalf("store the old settings: %v", err)
	}
	shown, err := app.GetCredentials(siaSlug)
	if err != nil {
		t.Fatalf("GetCredentials: %v", err)
	}
	if _, ok := shown.Values[siaSeedKey]; ok || shown.SavedSecrets[siaSeedKey] {
		t.Fatalf("GetCredentials sent %s to the window", siaSeedKey)
	}
	if !shown.SavedSecrets["HOSTD_API_PASSWORD"] || shown.Values["SIA_STORAGE_DIR"] != legacy["SIA_STORAGE_DIR"] {
		t.Fatal("GetCredentials dropped the password status or storage directory")
	}
	if err := app.SaveCredentials(siaSlug, siaSettings()); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}
	stored, _ := app.store.GetCredentials(siaSlug)
	if stored[siaSeedKey] != seedPhraseA {
		t.Fatal("saving the form deleted the only stored copy of the seed before a deploy moved it into the file")
	}
}

// An older window still sends the seed as a field. It is written into the file and
// never stored.
func TestATypedSeedIsWrittenIntoTheFileAndNeverStored(t *testing.T) {
	app := newAppUnderTest(t)
	values := siaSettings()
	values[siaSeedKey] = seedPhraseA
	if err := app.SaveCredentials(siaSlug, values); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}
	if stored, _ := app.store.GetCredentials(siaSlug); stored[siaSeedKey] != "" {
		t.Fatal("SaveCredentials stored a typed seed")
	}
	if _, err := app.DeployService(siaSlug, values, nil, nil); err != nil {
		t.Fatalf("DeployService: %v", err)
	}
	assertSeedFile(t, app, seedPhraseA)
	assertSeedNowhere(t, app, seedPhraseA)
}

// The compose export never tells anyone to put the seed in a .env file, where it
// would land in the container's settings again.
func TestTheComposeExportNeverAsksForTheSeed(t *testing.T) {
	app := newAppUnderTest(t)
	target := filepath.Join(t.TempDir(), "docker-compose.yml")
	app.saveDialog = func(wailsruntime.SaveDialogOptions) (string, error) { return target, nil }
	if _, err := app.ExportCompose([]string{siaSlug}, "amd64"); err != nil {
		t.Fatalf("ExportCompose: %v", err)
	}
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read the export: %v", err)
	}
	out := string(raw)
	if strings.Contains(out, siaSeedKey) {
		t.Fatalf("the compose export asks for %s:\n%s", siaSeedKey, out)
	}
	if !strings.Contains(out, "/data/hostd.yml") || !strings.Contains(out, "recoveryPhrase") {
		t.Fatalf("the compose export does not say where the seed goes:\n%s", out)
	}
	if !strings.Contains(out, "HOSTD_API_PASSWORD") {
		t.Fatalf("the compose export dropped the dashboard password:\n%s", out)
	}
}

// storedBlob is the encrypted credentials blob of Sia as the database holds it.
func storedBlob(t *testing.T, app *appUnderTest) string {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(app.cfg.DataDir(), "cashpilot-desktop.db"))
	if err != nil {
		t.Fatalf("open the database: %v", err)
	}
	defer db.Close()
	var blob string
	if err := db.QueryRow(`SELECT value FROM credentials WHERE slug = ?`, siaSlug).Scan(&blob); err != nil {
		t.Fatalf("read the blob: %v", err)
	}
	return blob
}

// assertBlobGoneFromDisk fails when any part of blob, the encrypted credentials
// that held the seed, is still in the database file or its log: its key stays in
// the keychain, so a leftover copy could still be decrypted.
func assertBlobGoneFromDisk(t *testing.T, app *appUnderTest, blob string) {
	t.Helper()
	var disk []byte
	for _, name := range []string{"cashpilot-desktop.db", "cashpilot-desktop.db-wal", "cashpilot-desktop.db-shm"} {
		raw, err := os.ReadFile(filepath.Join(app.cfg.DataDir(), name))
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("read %s: %v", name, err)
		}
		disk = append(disk, raw...)
	}
	if strings.Contains(string(disk), blob[len(blob)/2:len(blob)/2+40]) {
		t.Error("the encrypted copy of the stored seed is still in the database file")
	}
}

func containsEnv(env []string, want string) bool {
	for _, kv := range env {
		if kv == want {
			return true
		}
	}
	return false
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// historyHas reports whether the app's history holds an event of Sia named event.
func historyHas(t *testing.T, app *appUnderTest, event string) bool {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(app.cfg.DataDir(), "cashpilot-desktop.db"))
	if err != nil {
		t.Fatalf("open the database: %v", err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM runtime_events WHERE slug = ? AND event = ?`, siaSlug, event).Scan(&n); err != nil {
		t.Fatalf("read the history: %v", err)
	}
	return n > 0
}
