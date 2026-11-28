// Package securevm is the one shape the app sees for a Secure VM, whatever
// engine builds it: internal/macvm (Tart, macOS) today, internal/winvm
// (Hyper-V, Windows) once it is wired. The Security page renders this and
// nothing engine-specific.
//
// The Secure VM is an opt-in layer. It has three actions, Install, Verify and
// Undo, and each earner is placed either on this computer or in the VM.
package securevm

import (
	"context"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/i18n"
)

// Network and Fence values. They are the guard's own words, which winvm's
// status.json and macvm's status.json both write.
const (
	NetConnected    = "connected"
	NetDisconnected = "disconnected"
	NetOff          = "off"
	NetAbsent       = "absent"

	FenceHeld     = "held"
	FenceCut      = "cut"
	FenceReprove  = "reprove-needed"
	FenceUnproven = "unproven"
	FenceUnknown  = "unknown"
)

// Tri is yes, no or unknown. Unknown is never shown as no.
type Tri string

const (
	Yes     Tri = "yes"
	No      Tri = "no"
	Unknown Tri = "unknown"
)

// Status is one reading of the Secure VM.
type Status struct {
	// Platform is "macos" or "windows".
	Platform string `json:"platform"`
	// Available is whether this computer can have a Secure VM at all; when it
	// cannot, Unavailable says why in one line.
	Available   bool   `json:"available"`
	Unavailable string `json:"unavailable"`
	// Installed is whether the Install action completed (the admin step ran).
	Installed bool `json:"installed"`
	// UpdateNeeded is set when the installed VM's root side came from another
	// version of the app; Install runs again to update it, and re-proves.
	UpdateNeeded bool `json:"updateNeeded"`
	// Unfinished is set when Install made the VM and its fence but stopped
	// before the guest was ready; Install runs again to finish it.
	Unfinished bool `json:"unfinished"`
	// Leftovers is set when no install completed but an earlier try left
	// files or a VM behind; Remove clears them.
	Leftovers bool   `json:"leftovers"`
	Network   string `json:"network"`
	Fence     string `json:"fence"`
	// Reason is one line for the user about Network and Fence.
	Reason   string    `json:"reason"`
	Changes  []string  `json:"changes"`
	ProvedAt time.Time `json:"provedAt"`
	Proving  bool      `json:"proving"`
	// Blocked is how many packets the fence dropped since BlockedSince; nil
	// is unknown, never shown as 0.
	Blocked      *uint64   `json:"blocked"`
	BlockedSince time.Time `json:"blockedSince"`
	// Busy is the action running now ("install", "verify", "undo") and Step
	// its current step, with Fraction 0..1 or -1 when unknown.
	Busy     string  `json:"busy"`
	Step     string  `json:"step"`
	Fraction float64 `json:"fraction"`
	// LastError is the last action's failure, in words for the user.
	LastError string `json:"lastError"`
	// StartError is why the VM did not start; GuestLimit marks macOS's limit
	// of two macOS guests per Mac.
	StartError string `json:"startError"`
	GuestLimit bool   `json:"guestLimit"`
	// FreeDisk is the free space where the VM's disk lives; 0 is unknown.
	FreeDisk int64 `json:"freeDisk"`
	LowDisk  bool  `json:"lowDisk"`
	// Cost is what the VM costs this computer, for the Install screen.
	Cost Cost `json:"cost"`
	// Proof is the last proof, step by step.
	Proof *Proof `json:"proof"`
	// Apps is every earner the VM can hold, with where it runs and what it got.
	Apps []App `json:"apps"`
}

// Cost is the measured cost of the VM.
type Cost struct {
	MemoryGB float64 `json:"memoryGb"`
	Cores    float64 `json:"cores"`
	DiskGB   float64 `json:"diskGb"`
	// DownloadGB is what Install downloads first.
	DownloadGB float64 `json:"downloadGb"`
}

// Proof step kinds and outcomes, winvm's words.
const (
	KindRed   = "red"
	KindGreen = "green"
	KindCheck = "check"
	// KindFunction is the last step: the earner's own path really works.
	KindFunction = "function"

	WentRed     = "went-red"
	StayedGreen = "stayed-green"
	Held        = "held"
	Broken      = "broken"
	Leak        = "leak"
	NotProven   = "unproven"
	// NotApplicable is a step that does not apply on this computer, such
	// as a tunnel step with no tunnel.
	NotApplicable = "n/a"
)

// Proof is one run of Verify.
type Proof struct {
	At    time.Time   `json:"at"`
	Green bool        `json:"green"`
	Steps []ProofStep `json:"steps"`
	// Why is the verdict in one line.
	Why string `json:"why"`
}

// ProofStep is one step: a red step opens a hole and must see the probe pass,
// a green step must see nothing pass.
type ProofStep struct {
	ID      string `json:"id"`
	Kind    string `json:"kind"`
	Outcome string `json:"outcome"`
	Detail  string `json:"detail"`
}

// Placement is where an earner runs.
type Placement string

const (
	Host Placement = "host" // this computer
	VM   Placement = "vm"   // the Secure VM
)

// App is one earner as the Security page shows it.
type App struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
	// Placement is where it runs now ("" when it runs nowhere CashPilot knows).
	Placement Placement `json:"placement"`
	// CanVM is whether it may go into the VM; when not, Locked says why.
	CanVM  bool   `json:"canVm"`
	Locked string `json:"locked"`
	// In the VM: installed, running, signed in.
	Installed Tri    `json:"installed"`
	Running   Tri    `json:"running"`
	SignedIn  Tri    `json:"signedIn"`
	Detail    string `json:"detail"`
	// Note is what the user should know before placing it (it runs other
	// people's compute jobs, it needs Rosetta).
	Note string `json:"note"`
	// Layers lists the protection layers this earner really got.
	Layers []string `json:"layers"`
}

// Layer names an earner can get.
const (
	LayerFence    = "lan-fence"
	LayerSecureVM = "secure-vm"
	LayerVMFence  = "vm-fence"
)

// RemoveResult is what Undo removed, kept and left, each item with its reason.
// Kept is what Undo leaves on purpose, each with how to remove it by hand; the
// page shows it beside Left. An Undo that refuses to touch anything (a VM from
// an older release whose fence differs) returns an error and an empty result.
type RemoveResult struct {
	Removed []string `json:"removed"`
	Kept    []string `json:"kept"`
	Left    []string `json:"left"`
}

// Progress reports a long step.
type Progress func(step string, fraction float64)

// Engine is a Secure VM engine.
type Engine interface {
	Status(ctx context.Context) Status
	Install(ctx context.Context, progress Progress) error
	Verify(ctx context.Context, progress Progress) (Proof, error)
	Undo(ctx context.Context, progress Progress) (RemoveResult, error)
	// Place installs an earner in the VM (VM) or takes it out (Host).
	Place(ctx context.Context, slug string, where Placement, progress Progress) error
	// OpenScreen shows the VM's screen with the earner's app in front, so the
	// user can sign in.
	OpenScreen(ctx context.Context, slug string) error
	// Start and Stop run or stop the VM; it keeps its fence either way.
	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}

// GuestLayers are the layers an earner in the VM gets.
func GuestLayers() []string { return []string{LayerSecureVM, LayerVMFence} }

// Lock is the shared rule, and its reason is shown beside a disabled
// "Secure VM" choice. Lock is the shared rule for whether an earner may go into the VM. refused
// is the catalog's guest.refused ("terms" or "unsigned"; a service whose terms
// forbid containers counts as "terms"); hasGuest is whether this platform's
// guest block exists.
func Lock(refused string, hasGuest bool) (canVM bool, locked string) {
	switch {
	case refused == "terms":
		return false, i18n.T("go.securevm.lockTerms")
	case refused == "unsigned":
		return false, i18n.T("go.securevm.lockUnsigned")
	case !hasGuest:
		return false, i18n.T("go.securevm.lockNoGuest")
	}
	return true, ""
}
