package winvm

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"time"
)

// StatusVersion is the status.json format this code writes and reads strictly.
const StatusVersion = 1

// maxStatusBytes caps status.json on read.
const maxStatusBytes = 256 << 10

// The files the guard keeps in its data folder (Names.DataDir).
const (
	PlanFile   = "desired.json"
	StateFile  = "state.json"
	StatusFile = "status.json"
	LockFile   = "proof.lock"
	PrefsFile  = "prefs.json"
)

// Status.Network values.
const (
	NetConnected    = "connected"    // the adapter is on our switch
	NetDisconnected = "disconnected" // the VM runs with no network
	NetOff          = "off"          // the VM is not running
	NetAbsent       = "absent"       // there is no VM, or no plan to find it by
)

// Status.Fence values. Only held is green.
const (
	FenceHeld     = "held"           // the live fence is exactly the proven one
	FenceCut      = "cut"            // the fence changed, or could not be read, so the VM is cut off
	FenceReprove  = "reprove-needed" // the host's Windows build changed since the proof
	FenceUnproven = "unproven"       // no proof yet, or a proof is running
)

// Status is what the guard last saw and did. The guard writes it every pass;
// the app reads it as the user.
type Status struct {
	Version   int       `json:"version"`
	WrittenAt time.Time `json:"writtenAt"`
	VM        string    `json:"vm"`
	Network   string    `json:"network"`
	Fence     string    `json:"fence"`
	Reason    string    `json:"reason"`
	Changes   []Change  `json:"changes"`
	ProvedAt  time.Time `json:"provedAt"`
	// Blocked is the packets the fence dropped since BlockedSince. Nil means no
	// counter answered this pass: the count is unknown, never 0.
	Blocked         *uint64   `json:"blocked"`
	BlockedSince    time.Time `json:"blockedSince"`
	EnhancedSession string    `json:"enhancedSession"` // "off", "left on (lab)" or "on"
	Usage           *Usage    `json:"usage"`
}

// ReadOnly is whether the status came from another format version. The app
// may show it, but must not act on it, and asks for Repair.
func (s Status) ReadOnly() bool { return s.Version != StatusVersion }

// WriteStatus writes dir\status.json atomically. SYSTEM and Administrators may
// write it; Users may only read it.
func WriteStatus(dir string, s Status) error {
	s.Version = StatusVersion
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(dir, StatusFile), append(b, '\n'), usersRead)
}

// ReadStatus reads a status.json the way the app does: 256 KB at most, a
// regular file only, then ParseStatus.
func ReadStatus(path string) (Status, error) {
	b, err := readCapped(path, maxStatusBytes, false)
	if err != nil {
		return Status{}, err
	}
	return ParseStatus(b)
}

// ParseStatus decodes a status. This version is decoded strictly: no unknown
// field, nothing after the document, and only the known network and fence
// values. A status of another version is decoded without those checks and is
// ReadOnly.
func ParseStatus(b []byte) (Status, error) {
	if len(b) > maxStatusBytes {
		return Status{}, fmt.Errorf("status is %d bytes, over the %d byte cap", len(b), maxStatusBytes)
	}
	var probe struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(b, &probe); err != nil {
		return Status{}, fmt.Errorf("status: %w", err)
	}
	if probe.Version < 1 {
		return Status{}, errors.New("status: no version, so this is not a status file")
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	strict := probe.Version == StatusVersion
	if strict {
		dec.DisallowUnknownFields()
	}
	var s Status
	if err := dec.Decode(&s); err != nil {
		return Status{}, fmt.Errorf("status: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return Status{}, errors.New("status: trailing data after the document")
	}
	if strict {
		if !slices.Contains([]string{NetConnected, NetDisconnected, NetOff, NetAbsent}, s.Network) {
			return Status{}, fmt.Errorf("status: unknown network state %q", s.Network)
		}
		if !slices.Contains([]string{FenceHeld, FenceCut, FenceReprove, FenceUnproven}, s.Fence) {
			return Status{}, fmt.Errorf("status: unknown fence state %q", s.Fence)
		}
	}
	return s, nil
}
