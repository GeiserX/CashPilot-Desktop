// Package macvm builds, fences, proves, guards and removes the macOS Secure VM:
// one Tart guest on an Apple silicon Mac that runs the desktop-only earners
// (docs/design/secure-macos-vm.md). It is the macOS twin of internal/winvm and
// writes the same status vocabulary, so the app shows both alike.
//
// Two sides run the code:
//
//   - The app, as the Mac's user (the VM's owner). It downloads the pinned tools
//     and Apple's restore image, creates the VM, provisions the guest and its
//     earners through the guest agent (`tart exec`), and reads the status files.
//     It never starts the VM and never touches pf.
//   - The daemon, as root (one LaunchDaemon, installed by the one admin step).
//     It starts the VM with the fence at boot and on request, runs the proof,
//     and guards the fence every 60 seconds. It never reads the guest except
//     during the proof, and then only as evidence that can fail the proof,
//     never pass it.
package macvm

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
)

// ErrUnsupported is returned off macOS or on an Intel Mac.
var ErrUnsupported = errors.New("the macOS Secure VM needs a Mac with Apple silicon")

// VMName is the guest's Tart name. There is one Secure VM per Mac.
const VMName = "CashPilot-VM"

// Label is the root LaunchDaemon's label.
const Label = "org.cashpilot.securevm"

// Anchor is the pf anchor the fence lives in. The stock /etc/pf.conf already
// evaluates every child of com.apple, so /etc/pf.conf never changes. The
// name is the product's own: the lab's scripts used com.apple/cashpilot.
const Anchor = "com.apple/cashpilot-vm"

// Paths are where the root side keeps its files. Root owns every one of them
// except Request and Owner, which belong to the VM's owner.
type Paths struct {
	// Root holds everything below. root:wheel 755.
	Root string
	// Bin holds the daemon's copy of the app, Tart and the setuid softnet.
	// root:wheel 755, so the owner cannot swap the code root runs.
	Bin string
	// State holds what the daemon proved and saw. root:wheel 755; the files
	// the app reads are 644, the Tart log (it holds the VNC password) is 600.
	State string
	// Request is where the app drops a request file for the daemon. The owner
	// owns it (700); the daemon reads file names only, never contents.
	Request string
	// Owner holds what the daemon hands the owner: the VNC address of the
	// guest's screen. The owner owns it (700), the file is 600.
	Owner string
	// Plist is the LaunchDaemon's plist.
	Plist string
}

// RootDir is the product's root folder.
const RootDir = "/Library/Application Support/CashPilot/SecureVM"

// ProductPaths are the product's paths.
func ProductPaths() Paths { return PathsAt(RootDir, "/Library/LaunchDaemons/"+Label+".plist") }

// PathsAt lays the folders out under root, for tests.
func PathsAt(root, plist string) Paths {
	return Paths{
		Root:    root,
		Bin:     filepath.Join(root, "bin"),
		State:   filepath.Join(root, "state"),
		Request: filepath.Join(root, "request"),
		Owner:   filepath.Join(root, "owner"),
		Plist:   plist,
	}
}

// Files in Paths.Bin.
const (
	DaemonExe  = "cashpilot-securevm"
	SoftnetExe = "softnet"
	TartApp    = "tart.app"
)

// TartExe is the Tart binary inside a tart.app folder.
func TartExe(dir string) string { return filepath.Join(dir, TartApp, "Contents", "MacOS", "tart") }

// Files in Paths.State.
const (
	InstallFile = "install.json" // who owns the VM and where it lives
	DesiredFile = "desired.json" // the live fence the last start recorded
	StatusFile  = "status.json"  // what the daemon last saw and did
	ProofFile   = "proof.json"   // the last proof, step by step
	DenyFile    = "deny.list"    // the owner's public IPs, one per line
	GuardLog    = "guard.log"
	TartLog     = "tart.log"
	RulesFile   = "rules.pf"
	PFToken     = "pf-token"
	// SharingOrig is the macOS Internet Sharing preference as it was before the
	// install, which softnet rewrites; "absent" when there was none.
	SharingOrig = "internet-sharing.orig"
)

// SharingPlist is the preference softnet writes its DHCP lease time into.
const SharingPlist = "/Library/Preferences/SystemConfiguration/com.apple.InternetSharing.default.plist"

// VNCFile is the file in Paths.Owner that holds the guest screen's vnc:// URL.
const VNCFile = "vnc.url"

// Requests the app may drop in Paths.Request. A file with any other name is
// deleted and ignored.
const (
	RequestProve   = "prove"   // run the proof again
	RequestStart   = "start"   // start the VM with the fence
	RequestRestart = "restart" // stop the VM, then start it with the fence
)

var requestNames = map[string]bool{RequestProve: true, RequestStart: true, RequestRestart: true}

// IsRequest is whether name is a request the daemon acts on.
func IsRequest(name string) bool { return requestNames[name] }

// ownerName is a macOS short user name we accept: the daemon passes it to
// launchctl, sudo and dscl.
var ownerName = regexp.MustCompile(`^[a-z_][a-z0-9_.-]{0,31}$`)

// ValidOwner is whether name is a user name the daemon may act for.
func ValidOwner(name string) bool { return ownerName.MatchString(name) && name != "root" }

// TartHome is the owner's Tart folder: TART_HOME when the owner set it, else
// ~/.tart. The daemon reads it from install.json, never from its own
// environment.
func TartHome(home string) string {
	if h := os.Getenv("TART_HOME"); h != "" {
		return h
	}
	return filepath.Join(home, ".tart")
}

// VMDir is where Tart keeps the VM.
func VMDir(tartHome string) string { return filepath.Join(tartHome, "vms", VMName) }

// PreparedFile marks a guest whose earner account is ready. It lives in the
// VM's own folder, so removing the VM removes it.
func PreparedFile(tartHome string) string { return filepath.Join(VMDir(tartHome), "cashpilot-prepared") }
