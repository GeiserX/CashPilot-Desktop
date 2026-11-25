package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// FirewallChange records a Windows rule change before Desktop requests elevation.
type FirewallChange struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	OldProgram string `json:"oldProgram"`
	Action     string `json:"action"`
	NewProgram string `json:"newProgram,omitempty"`
}

func (m *Manager) FirewallChangePath() string {
	return filepath.Join(m.appDir, "server-commands-firewall.json")
}

func (m *Manager) ReadFirewallChange() (*FirewallChange, error) {
	raw, err := os.ReadFile(m.FirewallChangePath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var change FirewallChange
	if err := json.Unmarshal(raw, &change); err != nil {
		return nil, err
	}
	return &change, nil
}

func (m *Manager) SaveFirewallChange(change *FirewallChange) error {
	path := m.FirewallChangePath()
	if change == nil {
		err := os.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	raw, err := json.Marshal(change)
	if err != nil {
		return err
	}
	return writeFileAtomic(path, raw)
}
