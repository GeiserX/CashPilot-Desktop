package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
)

// twoManagers is the window and the background helper on the same files. The
// helper loads first, so it has never seen a pairing.
func twoManagers(t *testing.T) (window, helper *Manager) {
	t.Helper()
	keyring.MockInit()
	t.Setenv("CASHPILOT_DESKTOP_DATA_DIR", t.TempDir())
	helper, err := NewManager()
	if err != nil {
		t.Fatal(err)
	}
	window, err = NewManager()
	if err != nil {
		t.Fatal(err)
	}
	return window, helper
}

func publish(t *testing.T, m *Manager, server, worker string) Pairing {
	t.Helper()
	current, err := m.Pairing()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.PublishPairing(current.Revision, server, false, PairingCredential{Worker: worker, Enrolment: "shared"}); err != nil {
		t.Fatalf("publish %s: %v", server, err)
	}
	p, err := m.Pairing()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func configFile(t *testing.T, m *Manager) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(m.AppDir(), "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func decodedConfigFile(t *testing.T, m *Manager) AppConfig {
	t.Helper()
	var cfg AppConfig
	if err := json.Unmarshal(configFile(t, m), &cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

// editConfigFile rewrites config.json as another program would, field by field.
func editConfigFile(t *testing.T, m *Manager, change func(map[string]any)) {
	t.Helper()
	var fields map[string]any
	if err := json.Unmarshal(configFile(t, m), &fields); err != nil {
		t.Fatal(err)
	}
	change(fields)
	raw, err := json.MarshalIndent(fields, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.AppDir(), "config.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

const brokenConfig = `{"upstreamUrl":"http://192.168.1.20:8080","pairingRev`

// A Save whose config.json cannot be parsed writes nothing: this Manager's
// older copy would put its marker over the file's, and the caller can retry.
// A missing file is a first save and works.
func TestSaveStopsWhenTheConfigCannotBeRead(t *testing.T) {
	window, helper := twoManagers(t)
	publish(t, window, "http://192.168.1.10:8080", "worker-a")
	path := filepath.Join(window.AppDir(), "config.json")
	if err := os.WriteFile(path, []byte(brokenConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := helper.Config()
	cfg.DisplayCurrency = "EUR"
	if err := helper.Save(cfg); err == nil {
		t.Error("Save over a config.json it cannot parse returned no error")
	}
	if raw := configFile(t, window); string(raw) != brokenConfig {
		t.Errorf("Save wrote over a config.json it could not parse:\n%s", raw)
	}

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := helper.Save(cfg); err != nil {
		t.Fatalf("Save with no config.json: %v", err)
	}
	if got := decodedConfigFile(t, window); got.DisplayCurrency != "EUR" {
		t.Errorf("first save wrote currency %q, want EUR", got.DisplayCurrency)
	}
}

// 1.x saved server B over a revision, which drops the marker. A helper that
// loaded before the first join has never seen a pairing; its Save must still
// keep B, because pairing.json says a pairing exists.
func TestSaveKeepsAPendingAddressFromAHelperThatNeverSawAPairing(t *testing.T) {
	window, helper := twoManagers(t)
	publish(t, window, "http://192.168.1.10:8080", "worker-a")
	const serverB = "http://192.168.1.20:8080"
	editConfigFile(t, window, func(f map[string]any) {
		delete(f, "pairingRevision")
		f["upstreamUrl"], f["mode"] = serverB, ModeFollower
	})

	cfg := helper.Config()
	cfg.DisplayCurrency = "EUR"
	if err := helper.Save(cfg); err != nil {
		t.Fatal(err)
	}
	got := decodedConfigFile(t, window)
	if got.UpstreamURL != serverB || got.Mode != ModeFollower || got.PairingRevision != "" || got.DisplayCurrency != "EUR" {
		t.Errorf("config.json after the helper's save: server %q mode %q marker %q currency %q; want B, follower, no marker, EUR",
			got.UpstreamURL, got.Mode, got.PairingRevision, got.DisplayCurrency)
	}
}

// A snapshot taken while server commands were on for A, saved after a
// carry-over published B with them off, must not turn them back on for B.
func TestSaveKeepsTheServerCommandsOfANewerPairing(t *testing.T) {
	window, helper := twoManagers(t)
	publish(t, window, "http://192.168.1.10:8080", "worker-a")
	if _, err := helper.Reload(); err != nil {
		t.Fatal(err)
	}
	stale := helper.Config()
	if stale.ServerCommandsOff || stale.ServerCommandsChosen {
		t.Fatalf("setup: off=%t chosen=%t, want the default", stale.ServerCommandsOff, stale.ServerCommandsChosen)
	}
	if err := window.Update(func(c *AppConfig) { c.ServerCommandsOff, c.ServerCommandsChosen = true, true }); err != nil {
		t.Fatal(err)
	}
	b := publish(t, window, "http://192.168.1.20:8080", "worker-b")

	stale.DisplayCurrency = "EUR"
	if err := helper.Save(stale); err != nil {
		t.Fatal(err)
	}
	got := decodedConfigFile(t, window)
	if !got.ServerCommandsOff || !got.ServerCommandsChosen {
		t.Errorf("the stale save set server commands off=%t chosen=%t, want true/true", got.ServerCommandsOff, got.ServerCommandsChosen)
	}
	if got.PairingRevision != b.Revision || got.UpstreamURL != b.ServerURL || got.DisplayCurrency != "EUR" {
		t.Errorf("config.json after the stale save: marker %q server %q currency %q", got.PairingRevision, got.UpstreamURL, got.DisplayCurrency)
	}
}

// A publication whose config.json cannot be parsed fails and changes nothing:
// pairing.json still names the old revision, config.json is as it was, and
// the old revision's credential still works.
func TestPublishStopsWhenTheConfigCannotBeRead(t *testing.T) {
	window, _ := twoManagers(t)
	a := publish(t, window, "http://192.168.1.10:8080", "worker-a")
	path := filepath.Join(window.AppDir(), "config.json")
	if err := os.WriteFile(path, []byte(brokenConfig), 0o600); err != nil {
		t.Fatal(err)
	}

	err := window.PublishPairing(a.Revision, "http://192.168.1.20:8080", false, PairingCredential{Worker: "worker-b", Enrolment: "shared"})
	if err == nil {
		t.Error("a publication over a config.json it cannot parse returned no error")
	}
	if p, err := window.Pairing(); err != nil || p != a {
		t.Errorf("pairing.json after the failed publication: %+v, %v; want %+v", p, err, a)
	}
	if raw := configFile(t, window); string(raw) != brokenConfig {
		t.Errorf("the failed publication wrote config.json:\n%s", raw)
	}
	if _, key, err := window.ReadPairingCredential(false); err != nil || key.Worker != "worker-a" {
		t.Errorf("A's credential after the failed publication: %q, %v", key.Worker, err)
	}
}

// A publication stopped between its two writes: config.json holds the next
// revision's marker and pairing.json still names A. A helper that saw that on
// disk rejects A, and keeps rejecting it when config.json then cannot be read,
// even though its copy in memory now holds a marker.
func TestAnInterruptedPublicationStaysRejectedWhenTheConfigTurnsUnreadable(t *testing.T) {
	window, helper := twoManagers(t)
	a := publish(t, window, "http://192.168.1.10:8080", "worker-a")
	editConfigFile(t, window, func(f map[string]any) {
		f["pairingRevision"], f["upstreamUrl"] = "NEXT-REVISION-NEVER-PUBLISHED", "http://192.168.1.20:8080"
	})
	if _, err := helper.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := helper.ReadPairingCredential(false); !errors.Is(err, ErrPairingSuperseded) {
		t.Fatalf("readable config.json: %v, want ErrPairingSuperseded", err)
	}

	path := filepath.Join(window.AppDir(), "config.json")
	if err := os.WriteFile(path, []byte(brokenConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if p, key, err := helper.ReadPairingCredential(false); !errors.Is(err, ErrPairingSuperseded) {
		t.Errorf("unreadable config.json: revision %s gave key %q, %v; want ErrPairingSuperseded", p.Revision, key.Worker, err)
	}

	// The window keeps A again once the file reads: A is live for the helper.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := window.KeepPairing(a.Revision); err != nil {
		t.Fatal(err)
	}
	if _, key, err := helper.ReadPairingCredential(false); err != nil || key.Worker != "worker-a" {
		t.Errorf("after the window kept A: %q, %v", key.Worker, err)
	}
}

// The helper's only read of the interrupted state is a Reload: it caches the
// next revision's marker while pairing.json still names A, and config.json
// turns unreadable before any credential read. A's credential stays refused.
func TestAReloadOfAnInterruptedPublicationStaysRejectedWhenTheConfigTurnsUnreadable(t *testing.T) {
	window, helper := twoManagers(t)
	publish(t, window, "http://192.168.1.10:8080", "worker-a")
	editConfigFile(t, window, func(f map[string]any) {
		f["pairingRevision"], f["upstreamUrl"] = "NEXT-REVISION-NEVER-PUBLISHED", "http://192.168.1.20:8080"
	})
	if _, err := helper.Reload(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(window.AppDir(), "config.json"), []byte(brokenConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if p, key, err := helper.ReadPairingCredential(false); !errors.Is(err, ErrPairingSuperseded) {
		t.Errorf("unreadable config.json: revision %s gave key %q, %v; want ErrPairingSuperseded", p.Revision, key.Worker, err)
	}
}

// The helper read A as superseded after a 1.x save, then the window kept A and
// the helper reloaded the marked file. When config.json turns unreadable after
// that, A stays live: the reload saw A's own marker.
func TestAKeptRevisionStaysLiveWhenTheConfigTurnsUnreadable(t *testing.T) {
	window, helper := twoManagers(t)
	a := publish(t, window, "http://192.168.1.10:8080", "worker-a")
	editConfigFile(t, window, func(f map[string]any) { delete(f, "pairingRevision") })
	if _, _, err := helper.ReadPairingCredential(false); !errors.Is(err, ErrPairingSuperseded) {
		t.Fatalf("after the 1.x save: %v, want ErrPairingSuperseded", err)
	}
	if err := window.KeepPairing(a.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := helper.Reload(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(window.AppDir(), "config.json"), []byte(brokenConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, key, err := helper.ReadPairingCredential(false); err != nil || key.Worker != "worker-a" {
		t.Errorf("unreadable config.json after the window kept A: key %q, %v; want worker-a", key.Worker, err)
	}
}

// A missing config.json is not a read. A revision the helper read as
// superseded stays superseded when the file is gone at its next Reload.
func TestASupersededRevisionStaysRejectedWhenTheConfigIsRemoved(t *testing.T) {
	window, helper := twoManagers(t)
	publish(t, window, "http://192.168.1.10:8080", "worker-a")
	if _, err := helper.Reload(); err != nil {
		t.Fatal(err)
	}
	editConfigFile(t, window, func(f map[string]any) { delete(f, "pairingRevision") })
	if _, _, err := helper.ReadPairingCredential(false); !errors.Is(err, ErrPairingSuperseded) {
		t.Fatalf("after the 1.x save: %v, want ErrPairingSuperseded", err)
	}
	if err := os.Remove(filepath.Join(window.AppDir(), "config.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := helper.Reload(); err != nil {
		t.Fatal(err)
	}
	if p, key, err := helper.ReadPairingCredential(false); !errors.Is(err, ErrPairingSuperseded) {
		t.Errorf("config.json removed: revision %s gave key %q, %v; want ErrPairingSuperseded", p.Revision, key.Worker, err)
	}
}

// Reload decides on the marker it read from disk, not on the copy in memory:
// the file can change between reloadLocked and that read. Here the copy in
// memory still holds A's marker while config.json on disk has none, as when
// another process rewrites the file in between. A stays superseded.
func TestReloadDecidesOnTheMarkerReadFromDisk(t *testing.T) {
	window, helper := twoManagers(t)
	a := publish(t, window, "http://192.168.1.10:8080", "worker-a")
	if _, err := helper.Reload(); err != nil {
		t.Fatal(err)
	}
	editConfigFile(t, window, func(f map[string]any) { delete(f, "pairingRevision") })
	if _, _, err := helper.ReadPairingCredential(false); !errors.Is(err, ErrPairingSuperseded) {
		t.Fatalf("after the 1.x save: %v, want ErrPairingSuperseded", err)
	}
	// reloadLocked sees an unchanged snapshot, so the copy in memory keeps A.
	helper.mu.Lock()
	helper.saved = configFile(t, helper)
	helper.cfg.PairingRevision = a.Revision
	helper.mu.Unlock()
	if _, err := helper.Reload(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(window.AppDir(), "config.json"), []byte(brokenConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if p, key, err := helper.ReadPairingCredential(false); !errors.Is(err, ErrPairingSuperseded) {
		t.Errorf("unreadable config.json: revision %s gave key %q, %v; want ErrPairingSuperseded", p.Revision, key.Worker, err)
	}
}

// A credential read that read A's marker cannot undo a Reload that read a
// newer file: the read and the state change share one lock. The hook holds the
// credential read between its read and its state change while a publication
// stops between its two writes (the next revision's marker over A) and a
// Reload runs. Without the lock, the Reload finishes inside the hook, the older
// read clears its state, and the cached next marker lets A through once
// config.json cannot be read.
func TestAnOlderMarkerReadCannotUndoANewerReload(t *testing.T) {
	window, helper := twoManagers(t)
	publish(t, window, "http://192.168.1.10:8080", "worker-a")
	if _, err := helper.Reload(); err != nil {
		t.Fatal(err)
	}
	reloaded := make(chan struct{})
	afterMarkerReadForTest = func() {
		afterMarkerReadForTest = nil
		editConfigFile(t, window, func(f map[string]any) {
			f["pairingRevision"], f["upstreamUrl"] = "NEXT-REVISION-NEVER-PUBLISHED", "http://192.168.1.20:8080"
		})
		go func() {
			_, _ = helper.Reload()
			close(reloaded)
		}()
		select {
		case <-reloaded:
		case <-time.After(2 * time.Second):
		}
	}
	t.Cleanup(func() { afterMarkerReadForTest = nil })
	if _, _, err := helper.ReadPairingCredential(false); err != nil {
		t.Fatalf("A's own marker was read first: %v", err)
	}
	<-reloaded
	if err := os.WriteFile(filepath.Join(window.AppDir(), "config.json"), []byte(brokenConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if p, key, err := helper.ReadPairingCredential(false); !errors.Is(err, ErrPairingSuperseded) {
		t.Errorf("unreadable config.json after the newer Reload: revision %s gave key %q, %v; want ErrPairingSuperseded", p.Revision, key.Worker, err)
	}
}
