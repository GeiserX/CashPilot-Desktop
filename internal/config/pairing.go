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
	if err := m.savePairing(next); err != nil {
		return err
	}
	m.clearPairingCredential(current)
	if next.ServerURL == "" && key.Enrolment == "" {
		m.clearPairingCredential(next)
	}
	// Retire the pre-revision credentials after a successful migration or Leave.
	if previous == "" {
		for _, user := range []string{keyringUserUpstreamEnrol, keyringUserUpstreamWorker} {
			_ = credentials.Delete(keyringService, user)
		}
		for _, name := range []string{workerKeyFile, workerKeyFile + ".generation", ".upstream_enrolment_key", "server-commands-firewall.json"} {
			dropFallback(filepath.Join(m.appDir, name))
		}
	}
	return nil
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

func (m *Manager) savePairing(p Pairing) error {
	// Match Reload's lock order, so a reload that saw the previous file cannot
	// overwrite the new in-memory view after publication.
	m.mu.Lock()
	defer m.mu.Unlock()
	raw, _ := json.Marshal(p)
	if err := writeFileAtomic(m.PairingPath(), raw); err != nil {
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
