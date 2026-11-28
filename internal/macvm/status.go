package macvm

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// StatusVersion is the status.json format this code writes.
const StatusVersion = 1

// Status.Network values. They are winvm's, so the app reads both alike.
const (
	NetConnected    = "connected"    // the guest's port is on its bridge
	NetDisconnected = "disconnected" // the guest runs with its port cut
	NetOff          = "off"          // the guest is not running
	NetAbsent       = "absent"       // there is no Secure VM
)

// Status.Fence values. Only held is green.
const (
	FenceHeld     = "held"           // the live fence is exactly the proven one
	FenceCut      = "cut"            // the fence changed or could not be read, so the guest is cut off
	FenceReprove  = "reprove-needed" // macOS or a tool changed since the proof
	FenceUnproven = "unproven"       // no green proof yet, or one is running
	FenceUnknown  = "unknown"        // the app's own reading: the guard's report is too old to trust
)

// Status is what the daemon last saw and did. It writes it after every pass;
// the app reads it as the user.
type Status struct {
	Version   int       `json:"version"`
	WrittenAt time.Time `json:"writtenAt"`
	VM        string    `json:"vm"`
	Network   string    `json:"network"`
	Fence     string    `json:"fence"`
	Reason    string    `json:"reason"`
	Changes   []string  `json:"changes"`
	ProvedAt  time.Time `json:"provedAt"`
	// Proving is set while a proof runs.
	Proving bool `json:"proving"`
	// StartError is why the last start failed, in words the app can show; ""
	// when it did not fail. GuestLimit marks macOS's two-guest limit.
	StartError string `json:"startError"`
	GuestLimit bool   `json:"guestLimit"`
	// FreeDisk is the free space on the volume of the VM's disk, in bytes; 0
	// when it could not be read.
	FreeDisk int64 `json:"freeDisk"`
	// Blocked is how many packets the fence dropped since BlockedSince (the
	// last start, which loads the rules afresh); nil is unknown.
	Blocked      *uint64   `json:"blocked"`
	BlockedSince time.Time `json:"blockedSince"`
}

// Install is what the admin step recorded: whose VM it is and where.
type Install struct {
	Owner       string    `json:"owner"`
	OwnerUID    int       `json:"ownerUid"`
	OwnerHome   string    `json:"ownerHome"`
	TartHome    string    `json:"tartHome"`
	MAC         string    `json:"mac"`
	InstalledAt time.Time `json:"installedAt"`
	// Versions are the tools the daemon runs, so a change asks for a new proof.
	Tart    string `json:"tart"`
	Softnet string `json:"softnet"`
	App     string `json:"app"`
}

// Desired is the fence the last start loaded, which the guard compares the live
// one with. The proof's verdict lives beside it: the guard connects only when
// Proof matches this host and these tools.
type Desired struct {
	Bridge       string       `json:"bridge"`
	Port         string       `json:"port"`
	Gateway      netip.Addr   `json:"gateway"`
	Subnet       netip.Prefix `json:"subnet"`
	SoftnetBlock string       `json:"softnetBlock"`
	Rules        string       `json:"rules"`
	Table        string       `json:"table"`
	StartedAt    time.Time    `json:"startedAt"`
}

// Proven is what a green proof was proven on. The guard compares it with the
// host before it connects.
type Proven struct {
	At        time.Time `json:"at"`
	HostBuild string    `json:"hostBuild"`
	Tart      string    `json:"tart"`
	Softnet   string    `json:"softnet"`
	Green     bool      `json:"green"`
}

// ReadJSON reads a regular file of at most 1 MiB into v.
func ReadJSON(path string, v any) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil {
		return err
	}
	if len(b) > 1<<20 {
		return errors.New(path + " is over 1 MiB")
	}
	return json.Unmarshal(b, v)
}

// WriteJSON writes v to path atomically with mode perm.
func WriteJSON(path string, v any, perm os.FileMode) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return WriteAtomic(path, append(b, '\n'), perm)
}

// WriteAtomic writes b to a temporary file beside path, then renames it.
func WriteAtomic(path string, b []byte, perm os.FileMode) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Chmod(perm); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	// The rename is durable only once its folder is synced: Remove needs some
	// of these files after a power loss.
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// ReadStatus reads status.json.
func ReadStatus(p Paths) (Status, error) {
	var s Status
	err := ReadJSON(filepath.Join(p.State, StatusFile), &s)
	return s, err
}
