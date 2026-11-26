package config

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/filelock"
)

// Pairing is the window's current choice. An empty server means Lead. The
// worker and the elevated firewall command both use Revision to reject stale work.
type Pairing struct {
	Revision        string `json:"revision"`
	ServerURL       string `json:"serverUrl"`
	Suspended       bool   `json:"suspended,omitempty"`
	Alternate       bool   `json:"alternate,omitempty"`
	FirewallProgram string `json:"firewallProgram,omitempty"`
	RemoveFirewall  bool   `json:"removeFirewall,omitempty"`
}

type PairingCredential struct {
	Revision  string `json:"revision"`
	Worker    string `json:"worker"`
	Enrolment string `json:"enrolment"`
}

var ErrPairingChanged = errors.New("the pairing changed")

// ErrPairingSuperseded means config.json does not hold the revision's marker
// (AppConfig.PairingRevision): 1.x saved config.json after this version
// published the revision, or a publication stopped between its config.json
// and pairing.json writes. The revision's credential
// stays unused until the window carries the 1.x choice over or keeps the revision.
var ErrPairingSuperseded = errors.New("an older version changed the pairing")

func (m *Manager) PairingPath() string { return filepath.Join(m.appDir, "pairing.json") }

func (m *Manager) lockPairing() (Pairing, func(), error) {
	release, ok := filelock.Acquire(context.Background(), m.PairingPath()+".lock", 10*time.Second)
	if !ok {
		return Pairing{}, release, errors.New("could not lock the pairing")
	}
	p, err := m.Pairing()
	return p, release, err
}

// Pairing reads the durable revision. A missing file means there is no published
// revision, even when legacy settings or a credential still exist.
func (m *Manager) Pairing() (Pairing, error) {
	var p Pairing
	raw, err := os.ReadFile(m.PairingPath())
	if errors.Is(err, os.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return Pairing{}, err
	}
	if p.Revision == "" {
		return Pairing{}, errors.New("invalid pairing revision")
	}
	return p, nil
}

// PublishPairing stages the credential in the unused slot before it publishes
// the revision. On a failed write, the previous revision still has its credential.
func (m *Manager) PublishPairing(previous, serverURL string, removeFirewall bool, key PairingCredential) error {
	current, release, err := m.lockPairing()
	defer release()
	if err != nil {
		return err
	}
	if current.Revision != previous {
		return ErrPairingChanged
	}
	next := Pairing{
		Revision:       rand.Text(),
		ServerURL:      serverURL,
		Alternate:      !current.Alternate,
		RemoveFirewall: removeFirewall || current.RemoveFirewall || current.FirewallProgram != "",
	}
	if previous == "" {
		retired, err := m.retireFirewallPrompt()
		if err != nil {
			return err
		}
		next.RemoveFirewall = next.RemoveFirewall || retired
	}
	key.Revision = next.Revision
	if next.ServerURL != "" || key.Enrolment != "" {
		if err := m.savePairingCredential(next, key); err != nil {
			return err
		}
	}
	// Mark config.json first: a revision is never live without its marker, so
	// a missing marker always means 1.x saved the file. When pairing.json cannot
	// be written, the marker goes back, so a revision that 1.x superseded stays
	// superseded and the next start tries the publication again.
	unmark, err := m.markPairing(next)
	if err != nil {
		return err
	}
	if err := m.savePairing(next); err != nil {
		unmark()
		return err
	}
	m.clearPairingCredential(current)
	if next.ServerURL == "" && key.Enrolment == "" {
		m.clearPairingCredential(next)
	}
	// Retire the pre-revision credentials after every publication: a migration,
	// a Leave, or a pairing 1.x made after a downgrade. This version never writes
	// them. A failed deletion cannot repeat a carry-over, because the marker
	// above already records this revision.
	for _, user := range []string{keyringUserUpstreamEnrol, keyringUserUpstreamWorker} {
		_ = credentials.Delete(keyringService, user)
	}
	for _, name := range []string{workerKeyFile, workerKeyFile + ".generation", ".upstream_enrolment_key"} {
		dropFallback(filepath.Join(m.appDir, name))
	}
	if previous == "" {
		dropFallback(filepath.Join(m.appDir, "server-commands-firewall.json"))
	}
	return nil
}

// KeepPairing marks the revision as this version's again after a 1.x save that
// left the pairing as it was.
func (m *Manager) KeepPairing(revision string) error {
	current, release, err := m.lockPairing()
	defer release()
	if err != nil {
		return err
	}
	if revision == "" || current.Revision != revision {
		return ErrPairingChanged
	}
	_, err = m.markPairing(current)
	return err
}

// markPairing writes the revision's marker into config.json and copies its
// server into upstreamUrl and mode, so 1.x after a downgrade shows the same
// pairing, and a 1.x change to it can be told from a save that kept it. unmark
// puts the three fields back as they were (best effort).
func (m *Manager) markPairing(p Pairing) (unmark func(), err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	release, err := m.lockConfig()
	if err != nil {
		return nil, err
	}
	defer release()
	_, _ = m.reloadLocked()
	was, next := m.cfg, m.cfg
	next.PairingRevision, next.UpstreamURL, next.Mode = p.Revision, p.ServerURL, ModeLead
	if p.ServerURL != "" {
		next.Mode = ModeFollower
	}
	if err := m.persist(next); err != nil {
		return nil, err
	}
	m.cfg = next
	return func() {
		_ = m.Update(func(c *AppConfig) {
			c.PairingRevision, c.UpstreamURL, c.Mode = was.PairingRevision, was.UpstreamURL, was.Mode
		})
	}, nil
}

// markerOnDisk reads the marker from config.json. The background helper keeps
// its own copy of the file, which can predate the window's last publication.
// ok is false when the file cannot be read; the copy in memory is used then.
func (m *Manager) markerOnDisk() (marker string, ok bool) {
	raw, err := os.ReadFile(m.path)
	if err != nil {
		return "", false
	}
	var file struct {
		PairingRevision string `json:"pairingRevision"`
	}
	if json.Unmarshal(raw, &file) != nil {
		return "", false
	}
	return file.PairingRevision, true
}

// UpdatePairing changes only the named revision. The callback can compare the
// current state and do work that must be ordered with an elevated firewall call.
func (m *Manager) UpdatePairing(revision string, change func(*Pairing) error) error {
	current, release, err := m.lockPairing()
	defer release()
	if err != nil {
		return err
	}
	if revision == "" || current.Revision != revision {
		return ErrPairingChanged
	}
	next := current
	if err := change(&next); err != nil {
		return err
	}
	if next == current {
		return nil
	}
	err = m.savePairing(next)
	if err != nil && next.Suspended && !current.Suspended {
		// Revoke the credential if storage could not record the suspension.
		m.clearPairingCredential(current)
	}
	return err
}

// writePairingFile writes pairing.json. Tests replace it to make the write fail.
var writePairingFile = writeFileAtomic

// FailPairingWritesForTest makes every pairing.json write return err until
// restore runs. Not safe while other goroutines publish.
func FailPairingWritesForTest(err error) (restore func()) {
	writePairingFile = func(string, []byte) error { return err }
	return func() { writePairingFile = writeFileAtomic }
}

func (m *Manager) savePairing(p Pairing) error {
	// Match Reload's lock order, so a reload that saw the previous file cannot
	// overwrite the new in-memory view after publication.
	m.mu.Lock()
	defer m.mu.Unlock()
	raw, _ := json.Marshal(p)
	if err := writePairingFile(m.PairingPath(), raw); err != nil {
		return err
	}
	m.pairing = p
	return nil
}

const pairingKey = "upstream-pairing"
const pairingKeyAlt = "upstream-pairing-alt"

func (m *Manager) credentialSlot(p Pairing) (user, path string) {
	user = pairingKey
	if p.Alternate {
		user = pairingKeyAlt
	}
	return user, filepath.Join(m.appDir, "."+user)
}

func (m *Manager) savePairingCredential(p Pairing, key PairingCredential) error {
	user, path := m.credentialSlot(p)
	raw, _ := json.Marshal(key)
	if err := credentials.Set(keyringService, user, string(raw)); err == nil {
		dropFallback(path)
		return nil
	}
	return writeFileAtomic(path, raw)
}

func (m *Manager) clearPairingCredential(p Pairing) {
	user, path := m.credentialSlot(p)
	_ = credentials.Delete(keyringService, user)
	dropFallback(path)
}

// ReadPairingCredential takes one snapshot for authentication. Promotion
// copies the same credential into the keychain when the window can access it.
func (m *Manager) ReadPairingCredential(promote bool) (Pairing, PairingCredential, error) {
	var key PairingCredential
	p, release, err := m.lockPairing()
	defer release()
	if err != nil || p.Revision == "" {
		return p, key, err
	}
	// The pairing lock orders this read after a whole publication, which marks
	// config.json before it writes pairing.json.
	// On disk, only the revision's own marker counts. An empty one means 1.x
	// saved config.json; another revision's means a publication stopped between
	// its two writes. Either way this revision's credential is not used. When
	// config.json cannot be read, the copy in memory can predate the window's
	// last publication, so any marker there counts: 1.x does not make the file
	// unreadable.
	marker, ok := m.markerOnDisk()
	superseded := marker != p.Revision
	if !ok {
		m.mu.RLock()
		superseded = m.cfg.PairingRevision == ""
		m.mu.RUnlock()
	}
	if superseded {
		return p, key, ErrPairingSuperseded
	}
	user, path := m.credentialSlot(p)
	if raw, err := credentials.Get(keyringService, user); err == nil &&
		json.Unmarshal([]byte(raw), &key) == nil && key.Revision == p.Revision {
		return p, key, nil
	}
	raw, err := os.ReadFile(path)
	key = PairingCredential{}
	if err != nil || json.Unmarshal(raw, &key) != nil || key.Revision != p.Revision {
		if p.ServerURL == "" && errors.Is(err, os.ErrNotExist) {
			return p, PairingCredential{}, nil
		}
		return p, PairingCredential{}, errors.New("the key for the current pairing is unavailable")
	}
	if promote && !p.Suspended && credentials.Set(keyringService, user, string(raw)) == nil {
		dropFallback(path)
	}
	return p, key, nil
}

// Earlier builds kept elevation requests in a separate file. A pending
// script from one of those builds must lose permission before the new revision opens.
func (m *Manager) retireFirewallPrompt() (bool, error) {
	path := filepath.Join(m.appDir, "server-commands-firewall.json")
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	release, ok := filelock.Acquire(context.Background(), path+".lock", 10*time.Second)
	if !ok {
		return false, errors.New("could not lock the previous firewall request")
	}
	defer release()
	// Keep the file until publication so another startup still sees the
	// pending cleanup if the process exits between these two writes.
	err := writeFileAtomic(path, []byte("{}"))
	return err == nil, err
}
