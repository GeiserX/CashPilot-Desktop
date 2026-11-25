// Package winvm reads what a Windows PC can run before CashPilot asks anything of
// it: the Windows edition and build, the CPU, whether virtualization is on, WSL,
// Docker Desktop, the docker-users group and a pending restart.
//
// This file holds the facts and every decision made from them, in pure Go that
// builds and tests on every OS. The probes that read the machine sit behind the
// probes interface in probe.go: probe_windows.go reads Windows, probe_other.go
// reports that nothing can be read. The Secure VM (DESKTOP-VM.md §3 step 2) reuses
// the same facts, and CheckVMHost adds the checks only the Secure VM needs.
//
// The Secure VM's fence is decided here too, in pure Go: names.go (the host
// objects), plan.go (desired.json), diff.go (the compare and the one function
// allowed to connect), routes.go and networks.go (the tunnel, host-address and
// network rules), ownip.go and counters.go.
//
// Every fact is read from a value Windows does not translate: registry values, CIM
// numbers and booleans, SIDs and exit codes. The test PC runs Spanish Windows, where
// group names and command output are translated, so nothing here parses text a
// person would read.
package winvm

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// Tri is a fact that can also be unknown. A probe that timed out, failed or does
// not apply leaves its facts Unknown, and no decision treats Unknown as No.
type Tri string

const (
	Unknown Tri = "unknown"
	Yes     Tri = "yes"
	No      Tri = "no"
)

func triOf(b bool) Tri {
	if b {
		return Yes
	}
	return No
}

// triPtr maps a CIM value that may be null to a Tri: null stays Unknown.
func triPtr(b *bool) Tri {
	if b == nil {
		return Unknown
	}
	return triOf(*b)
}

// WSL states, derived from the facts by wslState.
const (
	WSLReady    = "ready"
	WSLMissing  = "missing"
	WSLOutdated = "outdated"
	WSLUnknown  = "unknown"
)

// MinWSLVersion is the oldest WSL package Docker Desktop's WSL 2 backend accepts
// (https://docs.docker.com/desktop/setup/install/windows-install/).
const MinWSLVersion = "2.1.5"

// Windows11Build is the first Windows 11 build. Windows 10 Pro reports the same
// EditionID as Windows 11 Pro, so the build is what tells them apart.
const Windows11Build = 22000

// HostFacts is one reading of the PC. The raw facts come from the probes; the last
// group is derived from them by derive and is what callers should decide on.
type HostFacts struct {
	// OS is the GOOS the app runs on. Supported is false where nothing can be read
	// (every OS but Windows), and then every fact below is Unknown.
	OS        string `json:"os"`
	Supported bool   `json:"supported"`

	// Build and UBR are CurrentBuild and UBR from HKLM\SOFTWARE\Microsoft\Windows
	// NT\CurrentVersion; 0 means unknown. EditionID comes from the same key, never
	// ProductName: Windows 11 still writes "Windows 10" there.
	Build     int    `json:"build"`
	UBR       int    `json:"ubr"`
	EditionID string `json:"editionId"`

	// Arch is the CPU from Win32_Processor.Architecture: x64, arm64, x86, arm or
	// unknown. Never GOARCH: the app ships one x64 exe, which an Arm PC runs
	// emulated, so GOARCH says amd64 there.
	Arch string `json:"arch"`

	// HypervisorPresent is Win32_ComputerSystem.HypervisorPresent. Firmware is
	// Win32_Processor.VirtualizationFirmwareEnabled, which reads false whenever a
	// hypervisor (Hyper-V, WSL 2, Memory Integrity) already runs, so it means
	// something only when HypervisorPresent is No. SLAT is
	// SecondLevelAddressTranslationExtensions, masked the same way: on the test PC,
	// with Hyper-V running, both read false.
	HypervisorPresent Tri `json:"hypervisorPresent"`
	Firmware          Tri `json:"virtualizationFirmware"`
	SLAT              Tri `json:"slat"`

	// WSLFeature and VMPlatformFeature are the optional features
	// Microsoft-Windows-Subsystem-Linux and VirtualMachinePlatform, from
	// Win32_OptionalFeature.InstallState (1 enabled).
	WSLFeature        Tri `json:"wslFeature"`
	VMPlatformFeature Tri `json:"vmPlatformFeature"`
	// WSLInstalled is whether the WSL package (%ProgramFiles%\WSL\wsl.exe) is
	// there. WSLVersion is what its --version printed, digits only; "" is unknown.
	WSLInstalled Tri    `json:"wslInstalled"`
	WSLVersion   string `json:"wslVersion"`

	// DockerDesktopInstalled reads the uninstall key and the install paths.
	// DockerDesktopRunning is whether "Docker Desktop.exe" runs. DockerDesktopStarted
	// is whether Docker Desktop was ever opened on this account: it writes its
	// settings file under %APPDATA%\Docker on its first start. DockerService is the
	// state of com.docker.service: running, stopped, starting, absent or unknown.
	// The WSL 2 backend does not need that service, so stopped there is normal.
	DockerDesktopInstalled Tri    `json:"dockerDesktopInstalled"`
	DockerDesktopRunning   Tri    `json:"dockerDesktopRunning"`
	DockerDesktopStarted   Tri    `json:"dockerDesktopStarted"`
	DockerService          string `json:"dockerService"`
	desktopEngineReason    string // Docker Desktop’s own named pipes

	// UserSID is the signed-in account, read from this process's token. The
	// docker-users facts are by SID: DockerUsersGroup is whether the local group
	// exists, DockerUsersListed whether this account is on its member list, and
	// DockerUsersToken whether this session's token carries it. Listed but not in
	// the token means the account must sign out and back in.
	UserSID           string `json:"userSid"`
	DockerUsersGroup  Tri    `json:"dockerUsersGroup"`
	DockerUsersListed Tri    `json:"dockerUsersListed"`
	DockerUsersToken  Tri    `json:"dockerUsersToken"`

	// RestartPending is whether Windows waits for a restart before a change can
	// take effect, and RestartReasons which marker said so: cbs (Component Based
	// Servicing\RebootPending), windows-update (WindowsUpdate\Auto
	// Update\RebootRequired) or file-rename (Session
	// Manager\PendingFileRenameOperations). Only cbs and windows-update make it Yes:
	// installers and updaters leave file renames behind all the time, and those
	// alone never stop Docker, so a PC with only file-rename reads No and keeps the
	// reason for a support report (restartPending).
	RestartPending Tri      `json:"restartPending"`
	RestartReasons []string `json:"restartReasons"`

	// LanmanServer is the start type of the Server service, which Docker Desktop
	// needs set to Automatic: auto, manual, disabled, absent or unknown.
	LanmanServer string `json:"lanmanServer"`
	// OtherHypervisors is whether another hypervisor's driver service is installed,
	// VBoxSup (VirtualBox) or vmx86 (VMware Workstation), and OtherHypervisorNames
	// names the ones found.
	OtherHypervisors     Tri      `json:"otherHypervisors"`
	OtherHypervisorNames []string `json:"otherHypervisorNames"`
	// Manufacturer and Model are Win32_ComputerSystem's, copied from the firmware
	// tables (SMBIOS), which Windows never translates. IsVM below is read from them.
	Manufacturer string `json:"manufacturer"`
	Model        string `json:"model"`
	// LastBoot is Win32_OperatingSystem.LastBootUpTime in UTC, RFC 3339; "" is
	// unknown. It is the same for every reading in one boot, so a setup that asks
	// for a restart compares it to prove the restart happened.
	LastBoot string `json:"lastBoot"`
	// DockerBackend is the engine Docker Desktop's own settings file chooses: wsl
	// (the WSL 2 backend), hyperv, or unknown when no settings file says.
	DockerBackend string `json:"dockerBackend"`

	// Problems names each probe that failed or timed out, with why. It is for a
	// support report only; no decision reads it.
	Problems map[string]string `json:"problems"`

	// Derived. Virtualization is whether the CPU's virtualization is on.
	// HyperVEdition is whether EditionID is an edition that has Hyper-V. WSL is one
	// of the WSL states. SecureVMHost says whether this PC can host the Secure VM
	// as far as its edition, build and CPU go.
	Virtualization Tri         `json:"virtualization"`
	HyperVEdition  Tri         `json:"hyperVEdition"`
	WSL            string      `json:"wsl"`
	SecureVMHost   HostVerdict `json:"secureVmHost"`
	// IsVM is whether this PC is itself a virtual machine, from Manufacturer and
	// Model. Inside a VM, HypervisorPresent describes the outer hypervisor, so it
	// does not prove that virtualization reaches this PC.
	IsVM Tri `json:"isVm"`
}

// Docker Desktop backends, as DockerBackend reads them.
const (
	BackendWSL     = "wsl"
	BackendHyperV  = "hyperv"
	BackendUnknown = "unknown"
)

// HostVerdict says whether this PC can host the Secure VM, and when it cannot,
// why, as one of the Host* reasons.
type HostVerdict struct {
	Eligible Tri    `json:"eligible"`
	Reason   string `json:"reason"`
}

// Reasons a PC cannot host the Secure VM (DESKTOP-VM.md §3 step 2). Each has its
// own card text: Home offers the Pro upgrade, Windows 10 and Arm do not.
const (
	HostNotWindows     = "not-windows"
	HostArm            = "arm"
	HostWindows10      = "windows-10"
	HostHomeEdition    = "home-edition"
	HostUnknownEdition = "unknown-edition"
	HostUnknownBuild   = "unknown-build"
	HostUnknownArch    = "unknown-arch"
)

// unknownFacts is a reading where nothing is known yet.
func unknownFacts(goos string) HostFacts {
	return HostFacts{
		OS:                     goos,
		Arch:                   "unknown",
		HypervisorPresent:      Unknown,
		Firmware:               Unknown,
		SLAT:                   Unknown,
		WSLFeature:             Unknown,
		VMPlatformFeature:      Unknown,
		WSLInstalled:           Unknown,
		DockerDesktopInstalled: Unknown,
		DockerDesktopRunning:   Unknown,
		DockerDesktopStarted:   Unknown,
		DockerService:          "unknown",
		DockerUsersGroup:       Unknown,
		DockerUsersListed:      Unknown,
		DockerUsersToken:       Unknown,
		RestartPending:         Unknown,
		RestartReasons:         []string{},
		LanmanServer:           "unknown",
		OtherHypervisors:       Unknown,
		OtherHypervisorNames:   []string{},
		DockerBackend:          BackendUnknown,
		Problems:               map[string]string{},
		IsVM:                   Unknown,
	}
}

// derive fills the derived fields from the raw ones.
func derive(f *HostFacts) {
	f.Virtualization = virtualizationOn(f.HypervisorPresent, f.Firmware)
	f.HyperVEdition = HyperVEdition(f.EditionID)
	f.WSL = wslState(*f)
	f.SecureVMHost = secureVMHost(*f)
	f.IsVM = virtualMachine(f.Manufacturer, f.Model)
}

// vmMarkers are words the firmware tables of common hypervisors put in the
// manufacturer or model: Hyper-V ("Microsoft Corporation", "Virtual Machine"),
// VMware, VirtualBox ("innotek GmbH", "VirtualBox"), QEMU and KVM, Xen, Parallels,
// Bochs, and the Amazon and Google clouds. They are vendor strings, never
// translated.
var vmMarkers = []string{"virtual machine", "vmware", "virtualbox", "innotek", "qemu", "kvm", "xen", "parallels", "bochs", "amazon ec2", "google compute engine"}

// virtualMachine says whether a manufacturer and model name a virtual machine.
// Nothing read is Unknown; a read that names no hypervisor is No.
func virtualMachine(manufacturer, model string) Tri {
	text := strings.ToLower(strings.TrimSpace(manufacturer) + " " + strings.TrimSpace(model))
	if strings.TrimSpace(text) == "" {
		return Unknown
	}
	for _, m := range vmMarkers {
		if strings.Contains(text, m) {
			return Yes
		}
	}
	return No
}

// virtualizationOn reads HypervisorPresent first. A running hypervisor proves
// virtualization is on, and it also makes the firmware flag read false, so the
// firmware flag decides only when no hypervisor runs.
func virtualizationOn(hypervisor, firmware Tri) Tri {
	switch {
	case hypervisor == Yes:
		return Yes
	case firmware == Yes:
		return Yes
	case hypervisor == No && firmware == No:
		return No
	}
	return Unknown
}

// HyperVEdition says whether an EditionID names an edition with Hyper-V. Home is
// Core and its variants (CoreN, CoreSingleLanguage, CoreCountrySpecific); Windows 11
// SE is CloudEdition. Pro, Pro for Workstations, Pro Education, Education,
// Enterprise and their N, LTSC and IoT variants have Hyper-V.
func HyperVEdition(editionID string) Tri {
	id := strings.ToLower(strings.TrimSpace(editionID))
	if id == "" {
		return Unknown
	}
	for _, p := range []string{"core", "cloud"} {
		if strings.HasPrefix(id, p) {
			return No
		}
	}
	for _, p := range []string{"professional", "education", "enterprise", "iotenterprise", "server"} {
		if strings.HasPrefix(id, p) {
			return Yes
		}
	}
	return Unknown
}

// wslState decides whether WSL can carry Docker Desktop.
//
//   - VirtualMachinePlatform off: missing. WSL 2 needs it.
//   - No WSL package: outdated when an inbox WSL is there (a feature is on, and
//     "wsl --update" brings the package), else missing.
//   - The package, at a known version below MinWSLVersion: outdated.
//   - The package at MinWSLVersion or newer: ready.
//
// Anything not proven stays unknown.
func wslState(f HostFacts) string {
	if f.VMPlatformFeature == No {
		return WSLMissing
	}
	switch f.WSLInstalled {
	case No:
		if f.WSLFeature == Yes || f.VMPlatformFeature == Yes {
			return WSLOutdated
		}
		if f.WSLFeature == No {
			return WSLMissing
		}
		return WSLUnknown
	case Yes:
		if f.WSLVersion == "" {
			return WSLUnknown
		}
		if versionLess(f.WSLVersion, MinWSLVersion) {
			return WSLOutdated
		}
		return WSLReady
	}
	return WSLUnknown
}

// secureVMHost checks the three facts the Secure VM needs before anything else:
// an x64 CPU, Windows 11, and an edition with Hyper-V. Arm comes first and Windows
// 10 second, because upgrading to Pro fixes neither.
func secureVMHost(f HostFacts) HostVerdict {
	if f.OS != "windows" {
		return HostVerdict{Eligible: No, Reason: HostNotWindows}
	}
	if f.Arch == "arm64" || f.Arch == "arm" {
		return HostVerdict{Eligible: No, Reason: HostArm}
	}
	if f.Build > 0 && f.Build < Windows11Build {
		return HostVerdict{Eligible: No, Reason: HostWindows10}
	}
	if f.HyperVEdition == No {
		return HostVerdict{Eligible: No, Reason: HostHomeEdition}
	}
	switch {
	case f.Arch != "x64":
		return HostVerdict{Eligible: Unknown, Reason: HostUnknownArch}
	case f.Build == 0:
		return HostVerdict{Eligible: Unknown, Reason: HostUnknownBuild}
	case f.HyperVEdition != Yes:
		return HostVerdict{Eligible: Unknown, Reason: HostUnknownEdition}
	}
	return HostVerdict{Eligible: Yes}
}

// archName maps Win32_Processor.Architecture to a name.
func archName(code int) string {
	switch code {
	case 0:
		return "x86"
	case 5:
		return "arm"
	case 9:
		return "x64"
	case 12:
		return "arm64"
	}
	return "unknown"
}

// versionLess compares dotted version numbers, missing parts as 0. A part that is
// not a number counts as 0, so a garbled version never reads as newer.
func versionLess(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x, _ = strconv.Atoi(pa[i])
		}
		if i < len(pb) {
			y, _ = strconv.Atoi(pb[i])
		}
		if x != y {
			return x < y
		}
	}
	return false
}

// MinHostRAM is the installed RAM a PC needs to host the Secure VM (decision 6).
// The reader passes installed memory (GetPhysicallyInstalledSystemMemory), not
// what Windows can see, which is a little lower.
const MinHostRAM = 12 * GiB

// FreeRAMReserve is what the host keeps free on top of the guest at build time.
const FreeRAMReserve = 1 * GiB

// VMHostInput is what the Secure-VM checks read, on top of HostFacts.
type VMHostInput struct {
	NATs         []netip.Prefix // every NetNat's internal prefix
	Subnet       netip.Prefix   // the /29 setup means to use
	Net          HostNet
	OurSwitchID  string // "" before the switch exists
	Network      CurrentNetwork
	InstalledRAM uint64 // 0 means unknown
	FreeRAM      uint64 // 0 means unknown
	GuestMemory  uint64 // 0 means DefaultGuestMemory
	// MinHostRAM is 0 for the product. Only a lab build lowers it.
	MinHostRAM uint64
	// GCS is the GuestCommunicationServices key names; nil means not read.
	GCS []string
}

// VMHostCheck is one Secure-VM check. OK is Unknown when its input was not read.
type VMHostCheck struct {
	ID     string `json:"id"`
	OK     Tri    `json:"ok"`
	Detail string `json:"detail"`
}

// Secure-VM check IDs.
const (
	CheckNATOverlap   = "nat-overlap"
	CheckDefaultRoute = "default-route"
	CheckMetered      = "metered"
	CheckInstalledRAM = "installed-ram"
	CheckFreeRAM      = "free-ram"
	CheckGCSBaseline  = "gcs-baseline"
)

// VMHostReport is the result of CheckVMHost. Ready is true only when every check
// is Yes. GCSBaseline is the list setup records into the plan.
type VMHostReport struct {
	Checks      []VMHostCheck `json:"checks"`
	Ready       bool          `json:"ready"`
	GCSBaseline []string      `json:"gcsBaseline"`
}

// CheckVMHost runs the checks the Secure VM needs before it is built: no NetNat
// on our /29, the internet leaving through a physical-class adapter, a network
// that is not metered, enough installed and free RAM, and a readable list of
// hv_sock services to compare against later.
func CheckVMHost(in VMHostInput) VMHostReport {
	var r VMHostReport
	add := func(id string, ok Tri, detail string) {
		r.Checks = append(r.Checks, VMHostCheck{ID: id, OK: ok, Detail: detail})
	}

	if checkSubnet(in.Subnet) != nil {
		add(CheckNATOverlap, Unknown, "no subnet chosen yet")
	} else if n := overlapping(in.Subnet, in.NATs); n.IsValid() {
		add(CheckNATOverlap, No, fmt.Sprintf("a NAT on this PC (%s) overlaps %s", n, in.Subnet))
	} else {
		add(CheckNATOverlap, Yes, "")
	}

	if len(in.Net.Interfaces) == 0 && len(in.Net.Routes) == 0 && len(in.Net.Egress) == 0 {
		add(CheckDefaultRoute, Unknown, "could not read the routes")
	} else if full, why := FullTunnel(in.Net, in.OurSwitchID); full {
		add(CheckDefaultRoute, No, why)
	} else {
		add(CheckDefaultRoute, Yes, "")
	}

	switch _, why := NetworkOK(in.Network, []NetworkID{in.Network.ID}); why {
	case NetworkMetered:
		add(CheckMetered, No, "this network is metered")
	case NetworkUnknown:
		add(CheckMetered, Unknown, "could not identify this network")
	default:
		add(CheckMetered, Yes, "")
	}

	minRAM := in.MinHostRAM
	if minRAM == 0 {
		minRAM = MinHostRAM
	}
	guest := in.GuestMemory
	if guest == 0 {
		guest = DefaultGuestMemory
	}
	ok, why := ramCheck(in.InstalledRAM, minRAM)
	add(CheckInstalledRAM, ok, why)
	ok, why = ramCheck(in.FreeRAM, guest+FreeRAMReserve)
	add(CheckFreeRAM, ok, why)

	if in.GCS == nil {
		add(CheckGCSBaseline, Unknown, "could not read the hv_sock services")
	} else {
		r.GCSBaseline = GCSBaseline(in.GCS)
		add(CheckGCSBaseline, Yes, fmt.Sprintf("%d registered", len(r.GCSBaseline)))
	}

	r.Ready = true
	for _, c := range r.Checks {
		if c.OK != Yes {
			r.Ready = false
		}
	}
	return r
}

func ramCheck(have, need uint64) (Tri, string) {
	if have == 0 {
		return Unknown, "could not read the RAM"
	}
	if have < need {
		return No, fmt.Sprintf("%.1f GiB, needs %.1f GiB", float64(have)/float64(GiB), float64(need)/float64(GiB))
	}
	return Yes, ""
}

// overlapping is the first prefix of set that overlaps p, or the zero prefix.
func overlapping(p netip.Prefix, set []netip.Prefix) netip.Prefix {
	for _, q := range set {
		if q.IsValid() && q.Overlaps(p) {
			return q
		}
	}
	return netip.Prefix{}
}
