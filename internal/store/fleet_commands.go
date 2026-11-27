package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// What a lead Desktop needs to command a CashPilot Docker worker that reports to it:
// the worker's own key, which the worker checks on every command, and what its last
// heartbeat said (its client id, the copies it runs, what it can do).
//
// The key is kept encrypted with the credential key (AES-256-GCM, like the vendor
// credentials), beside the hash the heartbeat check reads. A worker enrolled before
// this Desktop kept the key has only the hash, so it cannot be commanded until it
// enrols again.

// FleetContainer is one copy a worker's heartbeat listed.
type FleetContainer struct {
	Slug     string `json:"slug"`
	Status   string `json:"status"`
	Instance int    `json:"instance"`
	// Managed is false for a container the worker found by its image but did not
	// deploy ("deployed_by": "external"); it takes no commands.
	Managed bool `json:"managed"`
}

// FleetReport is the part of a worker's last heartbeat the commands read.
type FleetReport struct {
	ClientID   string           `json:"clientId"`
	Containers []FleetContainer `json:"containers"`
	Features   []string         `json:"features"`
	// GPUGrants is the heartbeat's gpu_grants, by slug, each grant as the worker
	// sent it. nil when the heartbeat had none.
	GPUGrants map[string]json.RawMessage `json:"gpuGrants,omitempty"`
}

// SetFleetDeviceIssuedKey stores a freshly issued per-worker key: its hash for the
// heartbeat check and the key itself, encrypted, for the commands. Like
// SetFleetDeviceKey it marks the key unconfirmed and errors when the row is missing,
// so a client is never handed a key that was not stored.
func (s *Store) SetFleetDeviceIssuedKey(kind, name, key string) error {
	sealed, err := s.encrypt([]byte(key))
	if err != nil {
		return err
	}
	res, err := s.execWrite(
		`UPDATE fleet_devices SET api_key_hash = ?, api_key_enc = ?, key_confirmed = 0, updated_at = datetime('now') WHERE kind = ? AND name = ?`,
		HashFleetKey(key), sealed, kind, name,
	)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("set worker key: no fleet_devices row for (%q, %q)", kind, name)
	}
	return nil
}

// FleetCommandKey is the per-worker key of device id and whether the worker
// confirmed it. key is "" when this Desktop does not hold it: the worker enrolled
// before keys were kept, or the stored copy no longer matches the hash.
func (s *Store) FleetCommandKey(id int64) (key string, confirmed bool, err error) {
	var hash, sealed string
	var confirmedInt int
	err = s.db.QueryRow(`SELECT api_key_hash, api_key_enc, key_confirmed FROM fleet_devices WHERE id = ?`, id).Scan(&hash, &sealed, &confirmedInt)
	if err == sql.ErrNoRows {
		return "", false, fmt.Errorf("no fleet device %d", id)
	}
	if err != nil {
		return "", false, err
	}
	if sealed == "" || hash == "" {
		return "", confirmedInt != 0, nil
	}
	raw, err := s.decrypt(sealed)
	if err != nil {
		return "", false, fmt.Errorf("the stored key of this worker does not decrypt: %w", err)
	}
	if HashFleetKey(string(raw)) != hash {
		return "", confirmedInt != 0, nil
	}
	return string(raw), confirmedInt != 0, nil
}

// SetFleetReport stores what device (kind, name)'s last heartbeat reported.
func (s *Store) SetFleetReport(kind, name string, report FleetReport) error {
	raw, err := json.Marshal(report)
	if err != nil {
		return err
	}
	_, err = s.execWrite(`UPDATE fleet_devices SET report = ? WHERE kind = ? AND name = ?`, string(raw), kind, name)
	return err
}

// FleetReports is every device's last report, by device id. A device that never
// reported, or whose report does not read, is left out.
func (s *Store) FleetReports() (map[int64]FleetReport, error) {
	rows, err := s.db.Query(`SELECT id, report FROM fleet_devices WHERE report != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]FleetReport{}
	for rows.Next() {
		var id int64
		var raw string
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		var report FleetReport
		if json.Unmarshal([]byte(raw), &report) == nil {
			out[id] = report
		}
	}
	return out, rows.Err()
}

// MarkFleetCopy writes what a command just did to the stored report of device id, so
// the Fleet page shows it before the worker's next heartbeat replaces the report with
// the worker's own reading. status "" drops the copy, and with instance 1 every copy
// of slug, as removing a service on a worker does. A status for a copy the report
// does not list yet (a first deploy) adds it.
func (s *Store) MarkFleetCopy(id int64, slug string, instance int, status string) error {
	instance = max(instance, 1)
	var raw string
	if err := s.db.QueryRow(`SELECT report FROM fleet_devices WHERE id = ?`, id).Scan(&raw); err != nil {
		return err
	}
	var report FleetReport
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &report); err != nil {
			return nil
		}
	}
	kept := []FleetContainer{}
	found := false
	for _, c := range report.Containers {
		n := max(c.Instance, 1)
		if c.Slug == slug && status == "" && (instance == 1 || n == instance) {
			continue
		}
		if c.Slug == slug && n == instance && status != "" {
			c.Status = status
			found = true
		}
		kept = append(kept, c)
	}
	if status != "" && !found {
		kept = append(kept, FleetContainer{Slug: slug, Status: status, Instance: instance, Managed: true})
	}
	report.Containers = kept
	updated, err := json.Marshal(report)
	if err != nil {
		return err
	}
	_, err = s.execWrite(`UPDATE fleet_devices SET report = ? WHERE id = ?`, string(updated), id)
	return err
}
