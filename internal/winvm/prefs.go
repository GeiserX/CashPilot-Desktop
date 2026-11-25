package winvm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
)

// maxPrefsBytes caps prefs.json.
const maxPrefsBytes = 16 << 10

// maxUploadMbps is the highest upload limit prefs.json may hold.
const maxUploadMbps = 100000

// Prefs is the one guard input the user can write: %ProgramData%\CashPilot\
// prefs.json. It can only take away. KeepOff keeps the VM off the network, and
// an approved network still never earns while it is metered (NetworkOK checks
// that first). The guard does not use UploadLimitMbps: a cap only restricts, so
// the app sets it on the adapter itself.
type Prefs struct {
	KeepOff          bool        `json:"keepOff"`
	ApprovedNetworks []NetworkID `json:"approvedNetworks"`
	UploadLimitMbps  int         `json:"uploadLimitMbps"`
}

// ParsePrefs decodes prefs.json strictly: 16 KB at most, no unknown field,
// nothing after the document, every approved network fully identified, and an
// upload limit from 0 (no limit) to maxUploadMbps.
func ParsePrefs(b []byte) (Prefs, error) {
	if len(b) > maxPrefsBytes {
		return Prefs{}, fmt.Errorf("prefs is %d bytes, over the %d byte cap", len(b), maxPrefsBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var p Prefs
	if err := dec.Decode(&p); err != nil {
		return Prefs{}, fmt.Errorf("prefs: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return Prefs{}, errors.New("prefs: trailing data after the document")
	}
	for _, n := range p.ApprovedNetworks {
		if _, ok := normalizeNetwork(n); !ok {
			return Prefs{}, fmt.Errorf("prefs: approved network %+v needs a gateway MAC and a profile ID", n)
		}
	}
	if p.UploadLimitMbps < 0 || p.UploadLimitMbps > maxUploadMbps {
		return Prefs{}, fmt.Errorf("prefs: upload limit %d is outside 0..%d", p.UploadLimitMbps, maxUploadMbps)
	}
	return p, nil
}

// LoadPrefs reads dir\prefs.json. No file means the defaults: no network
// approved, so the VM stays off the network until the user approves one.
func LoadPrefs(dir string) (Prefs, error) {
	b, err := readCapped(filepath.Join(dir, PrefsFile), maxPrefsBytes, false)
	if errors.Is(err, fs.ErrNotExist) {
		return Prefs{}, nil
	}
	if err != nil {
		return Prefs{}, err
	}
	return ParsePrefs(b)
}
