package statebackup

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"
)

// MaxAge is how long a confirmed backup counts before the app asks again. The
// fingerprint catches a new volume or a moved folder; this catches what it cannot
// see, such as an identity regenerated inside the same volume. Same as the web.
const MaxAge = 183 * 24 * time.Hour

// The states a service's backup can be in.
const (
	StateNone      = "none"      // never confirmed
	StateConfirmed = "confirmed" // confirmed, same state, under six months old
	StateChanged   = "changed"   // the state moved or was recreated since
	StateRecheck   = "recheck"   // confirmed when the fingerprint was unknown
	StateExpired   = "expired"   // older than MaxAge
)

// Confirmation is the user's word that a service's state is backed up.
type Confirmation struct {
	Slug string `json:"slug"`
	// StateID is the fingerprint when it was confirmed; "" when it could not be read.
	StateID string `json:"stateId"`
	// Method is "manual" (the button) or "export" (a backup saved from the app).
	Method      string    `json:"method"`
	ConfirmedAt time.Time `json:"confirmedAt"`
}

// Volume is one mount the catalog marks irreplaceable, with the catalog's words.
type Volume struct {
	Target string `json:"target"`
	Holds  string `json:"holds"`
}

// Row is one deployed service with irreplaceable state, as the card shows it.
type Row struct {
	Slug        string   `json:"slug"`
	Name        string   `json:"name"`
	Volumes     []Volume `json:"volumes"`
	State       string   `json:"state"`
	ConfirmedAt string   `json:"confirmedAt"`
	Method      string   `json:"method"`
	// TracksChanges is false when the fingerprint could not be read (the
	// container is gone, Docker is down), so only the time limit re-arms it.
	TracksChanges bool `json:"tracksChanges"`
}

// Judge decides a service's backup state from its confirmation (nil when there is
// none) and the fingerprint it has now ("" when unknown).
func Judge(found *Confirmation, live string, now time.Time) string {
	switch {
	case found == nil:
		return StateNone
	case live != "" && found.StateID == "":
		// Confirmed while the state could not be fingerprinted, so there is
		// nothing to compare: ask once more, now that it can be.
		return StateRecheck
	case live != "" && found.StateID != live:
		return StateChanged
	case found.ConfirmedAt.IsZero() || now.Sub(found.ConfirmedAt) > MaxAge:
		return StateExpired
	default:
		return StateConfirmed
	}
}

// MountFact is what Docker says about the mount at one critical target.
type MountFact struct {
	// Mounted is false when nothing is mounted there: the state lives in the
	// container's own layer and dies with the container.
	Mounted bool
	Type    string // "volume", "bind", ...
	Name    string // a named volume's name
	Source  string // a bind mount's host path
	// Created is a named volume's CreatedAt.
	Created string
}

// Fingerprint is a short hash of WHERE a service's irreplaceable state lives: each
// critical mount's type and source, plus a named volume's creation time, or the
// container id when the state is not on a mount. It changes when a volume is
// removed and made again or the service points somewhere else; it does not change
// when the container is recreated over the same volume (every image update). It
// never reads the files. Same recipe as the web's orchestrator.state_id.
func Fingerprint(facts map[string]MountFact, containerID string) string {
	if len(facts) == 0 {
		return ""
	}
	targets := make([]string, 0, len(facts))
	for t := range facts {
		targets = append(targets, t)
	}
	sort.Strings(targets)
	parts := make([]string, 0, len(targets))
	for _, t := range targets {
		f := facts[t]
		switch {
		case !f.Mounted:
			parts = append(parts, t+"|container|"+containerID)
		case f.Type == "volume":
			parts = append(parts, t+"|volume|"+f.Name+"|"+f.Created)
		default:
			parts = append(parts, t+"|"+f.Type+"|"+f.Source)
		}
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])[:16]
}
