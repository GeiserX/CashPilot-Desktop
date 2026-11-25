package winvm

import (
	"fmt"
	"strings"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
)

// The Docker checklist on Windows: one step per thing Docker Desktop needs from
// this PC, in the order a person fixes them (A1 to A12 in the setup research).
// Every step is derived from the facts of one reading and the runtime's reason
// code. A step whose fact is unknown says so and is never done.
//
// An action that changes the PC names its change in Op and runs only from its
// confirm screen: ActionElevated with one Windows admin prompt (RunElevated),
// ActionRun as the user. setupsteps.go builds each change and reads how it ended.

// Step states.
const (
	StepDone     = "done"
	StepNeedsYou = "needs-you"
	StepBlocked  = "blocked"
	StepUnknown  = "unknown"
	// StepNote is worth knowing but proven to block nothing: the summary does
	// not count it as a step that needs the user.
	StepNote = "note"
)

// Action kinds. Only ActionElevated needs admin rights.
const (
	ActionNone          = ""
	ActionURL           = "url"            // open URL in the browser
	ActionWindowsUpdate = "windows-update" // open Settings at Windows Update
	ActionStartDocker   = "start-docker"   // open Docker Desktop, then wait for its engine
	ActionShowDocker    = "show-docker"    // bring Docker Desktop's window forward
	ActionFirmware      = "firmware"       // restart into the BIOS, after a confirm screen
	ActionElevated      = "elevated"       // an admin change, after its confirm screen
	ActionRun           = "run"            // a change made as the user, after its confirm screen
)

// The changes an ActionElevated or ActionRun step asks for (setupsteps.go).
const (
	OpWSLInstall       = "wsl-install"
	OpWSLUpdate        = "wsl-update"
	OpDockerUsers      = "docker-users"
	OpLanmanServer     = "lanmanserver-auto"
	OpDockerInstall    = "docker-install"
	OpLinuxEngine      = "linux-engine"
	OpHypervisorLaunch = "hypervisor-launch"
)

// DockerDownloadURL is Docker Desktop's download page.
const DockerDownloadURL = "https://www.docker.com/products/docker-desktop/"

// VirtualizationHelpURL is Microsoft's page on turning on virtualization.
const VirtualizationHelpURL = "https://support.microsoft.com/en-us/windows/enable-virtualization-on-windows-c5578302-6e43-4b4b-a12d-29d6d7b86a1c"

// DockerTermsSentence says what the user is asked to accept, and that CashPilot
// never accepts it for them (https://docs.docker.com/subscription/desktop-license/).
const DockerTermsSentence = "Docker Desktop waits for you to accept its terms in its own window; CashPilot never accepts them for you. It is free for personal use and for companies with fewer than 250 staff and less than $10 million a year in revenue."

// BIOSHelp is the vendor help text the virtualization step shows.
const BIOSHelp = "The setting is called Intel Virtualization Technology, Intel VT-x, AMD-V or SVM Mode. It usually sits under Advanced, CPU Configuration or Security. The key that opens the BIOS at power-on is often F2 or Del; HP uses F10 or Esc, and some Lenovo PCs use F1. Turn the setting on, then save and exit (often F10)."

// Windows builds Docker Desktop supports
// (https://docs.docker.com/desktop/setup/install/windows-install/).
const (
	minWindows10Build = 19045 // Windows 10 22H2
	minWindows11Build = 22631 // Windows 11 23H2
)

// StepAction is the next safe thing to do for a step.
type StepAction struct {
	Kind  string `json:"kind"`
	Label string `json:"label"`
	URL   string `json:"url,omitempty"`
	// Op names the change of an ActionElevated or ActionRun step.
	Op string `json:"op,omitempty"`
	// Manual says how to make that change by hand, for a PC where the app cannot.
	Manual string `json:"manual,omitempty"`
}

// Step is one row of the checklist.
type Step struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	State string `json:"state"`
	// Text says, in plain words, what the state means for this PC.
	Text string `json:"text"`
	// BlockedBy is the ID of the earlier step that must be done first.
	BlockedBy string     `json:"blockedBy,omitempty"`
	Action    StepAction `json:"action"`
	// Help and HelpURL are extra guidance, shown folded.
	Help    string `json:"help,omitempty"`
	HelpURL string `json:"helpUrl,omitempty"`
}

// stepAfter lists, per step, the earlier steps it waits for.
var stepAfter = map[string][]string{
	"a4": {"a3"},
	"a5": {"a1"},
	"a6": {"a2", "a3", "a4", "a5"},
	"a7": {"a5"},
	"a9": {"a6"},
}

// DockerSteps derives the checklist from one reading of this PC and the runtime's
// reason code. local says the engine the app reached is this PC's own Docker
// Desktop (runtime.LocalDockerDesktop): no DOCKER_HOST and no Docker context that
// names another engine. Outside Windows it returns nothing.
func DockerSteps(f HostFacts, reason string, local bool) []Step {
	if !f.Supported || f.OS != "windows" {
		return nil
	}
	reason = desktopReason(f, reason, local)
	proven := reason == runtime.ReasonRunning || reason == runtime.ReasonWindowsContainers
	// Windows containers do not prove the virtualization or WSL rows.
	linuxProven := reason == runtime.ReasonRunning
	wslBackend := f.DockerBackend != BackendHyperV

	steps := []Step{windowsStep(f, proven), virtualizationStep(f, linuxProven)}
	if wslBackend {
		steps = append(steps, wslFeatureStep(f, linuxProven), wslVersionStep(f, linuxProven))
	}
	steps = append(steps,
		installedStep(f, proven),
		runningStep(f, proven),
		termsStep(f, reason, proven),
	)
	if reason == runtime.ReasonAccessDenied {
		steps = append(steps, dockerUsersStep(f))
	}
	steps = append(steps,
		linuxContainersStep(reason),
		otherHypervisorStep(f, linuxProven),
		virtualMachineStep(f, linuxProven),
		lanmanStep(f, linuxProven),
	)
	return markBlocked(steps)
}

// LocalLinuxEngine says this PC's own Docker Desktop runs Linux containers: the
// engine answers in Linux mode, it is this PC's Docker Desktop that answers
// (local, from runtime.LocalDockerDesktop), and Docker Desktop is seen running
// here. Only then does the engine prove facts about this PC, and only then is a
// guided setup finished.
func LocalLinuxEngine(f HostFacts, reason string, local bool) bool {
	return desktopReason(f, reason, local) == runtime.ReasonRunning
}

// desktopReason separates the setup checklist from the engine used for deploys.
func desktopReason(f HostFacts, reason string, local bool) string {
	if f.desktopEngineReason != "" {
		return f.desktopEngineReason
	}
	if !local || ((reason == runtime.ReasonRunning || reason == runtime.ReasonWindowsContainers) && f.DockerDesktopRunning != Yes) {
		return ""
	}
	return reason
}

// markBlocked turns a step that is not done into "blocked" when an earlier step
// it waits for needs the user or is blocked itself. A blocked step offers no
// action: its next step is the earlier one.
func markBlocked(steps []Step) []Step {
	state := map[string]string{}
	for i := range steps {
		s := &steps[i]
		if s.State != StepDone {
			for _, dep := range stepAfter[s.ID] {
				if st, ok := state[dep]; ok && (st == StepNeedsYou || st == StepBlocked) {
					s.State, s.BlockedBy, s.Action = StepBlocked, dep, StepAction{}
					break
				}
			}
		}
		state[s.ID] = s.State
	}
	return steps
}

func windowsStep(f HostFacts, proven bool) Step {
	s := Step{ID: "a1", Title: "Windows version"}
	if f.Build == 0 {
		s.State, s.Text = StepUnknown, "CashPilot could not read which Windows version this PC runs."
		return s
	}
	win11 := f.Build >= Windows11Build
	name := "Windows 10"
	if win11 {
		name = "Windows 11"
	}
	supported := f.Build >= minWindows11Build || (!win11 && f.Build >= minWindows10Build)
	if supported || proven {
		s.State = StepDone
		if win11 {
			s.Text = fmt.Sprintf("This PC runs Windows 11 (build %d), which Docker supports.", f.Build)
		} else {
			// The Windows 10 note, shown once: here and nowhere else.
			s.Text = fmt.Sprintf("This PC runs Windows 10 (build %d). Docker still runs on it, but Windows 10 stopped getting security updates in October 2025.", f.Build)
		}
		return s
	}
	need := minWindows10Build
	if win11 {
		need = minWindows11Build
	}
	s.State = StepNeedsYou
	s.Text = fmt.Sprintf("This PC runs %s build %d, and Docker needs build %d or newer. Install the Windows updates, restart, then click Check again.", name, f.Build, need)
	s.Action = StepAction{Kind: ActionWindowsUpdate, Label: "Open Windows Update"}
	return s
}

func virtualizationStep(f HostFacts, proven bool) Step {
	s := Step{ID: "a2", Title: "Virtualization in the BIOS"}
	switch {
	case proven:
		s.State, s.Text = StepDone, "Virtualization is on: Docker's engine runs on this PC."
	case f.IsVM == Yes:
		// Inside a VM, HypervisorPresent describes the outer machine.
		s.State, s.Text = StepUnknown, "This PC is itself a virtual machine, so CashPilot cannot tell whether virtualization reaches it. See the virtual machine step below."
	case f.Virtualization == No:
		s.State = StepNeedsYou
		s.Text = "Virtualization is turned off in this PC's BIOS (UEFI), and Docker needs it. Restart into the BIOS and turn it on; CashPilot never changes BIOS settings itself."
		s.Action = StepAction{Kind: ActionFirmware, Label: "Restart into BIOS"}
		s.Help, s.HelpURL = BIOSHelp, VirtualizationHelpURL
	case f.Virtualization == Yes && f.HypervisorPresent == No && f.SLAT == No:
		// SLAT reads false whenever a hypervisor runs, so it means something only
		// when none does.
		s.State = StepNeedsYou
		s.Text = "This processor lacks SLAT (Second Level Address Translation), which Docker needs, so Docker cannot run on this PC. You can still use the apps that run without Docker."
	case f.Virtualization == Yes:
		s.State, s.Text = StepDone, "Virtualization is on."
	default:
		s.State, s.Text = StepUnknown, "CashPilot could not read whether virtualization is on."
	}
	return s
}

func wslFeatureStep(f HostFacts, proven bool) Step {
	s := Step{ID: "a3", Title: "WSL 2 backend: Windows features"}
	switch {
	case proven:
		s.State, s.Text = StepDone, "Docker's engine runs, so the Windows features it needs are on."
	case f.WSL == WSLMissing:
		s.State = StepNeedsYou
		s.Text = "Docker's WSL 2 backend needs WSL, the Linux layer built into Windows, and it is off. Turning it on needs an administrator and one restart."
		s.Action = StepAction{Kind: ActionElevated, Op: OpWSLInstall, Label: "Turn on WSL",
			Manual: "Open PowerShell as administrator, run wsl --install --no-distribution, then restart the PC."}
	case f.VMPlatformFeature == Yes:
		s.State, s.Text = StepDone, "The Virtual Machine Platform feature that WSL 2 needs is on."
	default:
		s.State, s.Text = StepUnknown, "CashPilot could not read whether the Windows features WSL 2 needs are on."
	}
	return s
}

func wslVersionStep(f HostFacts, proven bool) Step {
	s := Step{ID: "a4", Title: "WSL 2 backend: WSL version"}
	switch {
	case proven:
		s.State, s.Text = StepDone, "Docker's engine runs, so WSL is new enough."
	case f.WSL == WSLReady:
		s.State, s.Text = StepDone, fmt.Sprintf("WSL is version %s; Docker needs %s or newer.", f.WSLVersion, MinWSLVersion)
	case f.WSL == WSLOutdated:
		have := "too old"
		if f.WSLVersion != "" {
			have = "version " + f.WSLVersion
		}
		s.State = StepNeedsYou
		s.Text = fmt.Sprintf("WSL on this PC is %s, and Docker needs %s or newer.", have, MinWSLVersion)
		s.Action = StepAction{Kind: ActionElevated, Op: OpWSLUpdate, Label: "Update WSL",
			Manual: "Open PowerShell, run wsl --update, then open Docker Desktop again."}
	case f.WSL == WSLMissing:
		s.State, s.Text = StepNeedsYou, "WSL is not installed yet."
	default:
		s.State, s.Text = StepUnknown, "CashPilot could not read which WSL version this PC has."
	}
	return s
}

func installedStep(f HostFacts, proven bool) Step {
	s := Step{ID: "a5", Title: "Docker Desktop installed"}
	switch {
	case proven || f.DockerDesktopInstalled == Yes:
		s.State, s.Text = StepDone, "Docker Desktop is installed."
	case f.DockerDesktopInstalled == No:
		s.State = StepNeedsYou
		s.Text = "Docker Desktop is not installed, and CashPilot runs most apps inside it. CashPilot can install it with winget, Windows' app installer, or you can download it yourself. The installer may ask you to restart."
		// URL is the download page: the second button, and the fallback when
		// this PC has no winget.
		s.Action = StepAction{Kind: ActionRun, Op: OpDockerInstall, Label: "Install Docker Desktop", URL: DockerDownloadURL}
	default:
		s.State, s.Text = StepUnknown, "CashPilot could not read whether Docker Desktop is installed."
	}
	return s
}

func runningStep(f HostFacts, engineUp bool) Step {
	s := Step{ID: "a6", Title: "Docker Desktop running"}
	switch {
	case engineUp:
		s.State, s.Text = StepDone, "Docker's engine answers."
	case f.DockerDesktopRunning == Yes:
		s.State = StepNeedsYou
		s.Text = "Docker Desktop is open, but its engine is not running yet. Starting can take a minute or two; if Docker shows a message, read it in its window."
		s.Action = StepAction{Kind: ActionShowDocker, Label: "Show Docker Desktop"}
	case f.DockerDesktopRunning == No:
		s.State, s.Text = StepNeedsYou, "Docker Desktop is not running."
		s.Action = StepAction{Kind: ActionStartDocker, Label: "Start Docker Desktop"}
	default:
		s.State, s.Text = StepUnknown, "CashPilot could not read whether Docker Desktop is running."
	}
	return s
}

func termsStep(f HostFacts, reason string, engineUp bool) Step {
	s := Step{ID: "a7", Title: "Docker's terms"}
	open := StepAction{Kind: ActionStartDocker, Label: "Start Docker Desktop"}
	if f.DockerDesktopRunning == Yes {
		open = StepAction{Kind: ActionShowDocker, Label: "Show Docker Desktop"}
	}
	switch {
	case engineUp:
		s.State, s.Text = StepDone, "Docker's terms are accepted: its engine runs."
	case reason == runtime.ReasonFirstStart || (f.DockerDesktopInstalled == Yes && f.DockerDesktopStarted == No):
		s.State, s.Text, s.Action = StepNeedsYou, "Docker Desktop has not been opened on this account yet. "+DockerTermsSentence, open
	default:
		// Docker writes its settings file on its first start, which may come before
		// the terms are accepted, so nothing here proves they were.
		s.State = StepUnknown
		s.Text = "CashPilot cannot read whether Docker's terms are accepted. If Docker Desktop shows them, read and accept them there."
		if f.DockerDesktopRunning == Yes {
			s.Action = open
		}
	}
	return s
}

// dockerUsersStep appears only when the engine refused this account: the WSL 2
// backend with Linux containers does not need the group.
func dockerUsersStep(f HostFacts) Step {
	s := Step{ID: "a8", Title: "docker-users group", State: StepNeedsYou}
	if f.DockerUsersListed == Yes && f.DockerUsersToken != Yes {
		s.Text = "Your account is in the docker-users group, but Windows applies a new group only after you sign in again. Save your work, sign out and back in, then click Check again."
		return s
	}
	if f.DockerUsersToken == Yes {
		// This session carries the group, so the group is not what refuses it:
		// adding the account again would change nothing.
		s.Text = "Your account is in the docker-users group, and this session carries it, yet Docker still refuses it. Restart Docker Desktop; if that does not help, reinstall it."
		return s
	}
	s.Text = "Docker runs, but your Windows account may not use it. This happens when another account installed Docker Desktop. Your account must join the docker-users group, then sign out and back in."
	s.Action = StepAction{Kind: ActionElevated, Op: OpDockerUsers, Label: "Add my account to docker-users",
		Manual: "Ask an administrator of this PC to add your account to the docker-users group, then sign out and back in."}
	return s
}

func linuxContainersStep(reason string) Step {
	s := Step{ID: "a9", Title: "Linux containers"}
	switch reason {
	case runtime.ReasonRunning:
		s.State, s.Text = StepDone, "Docker runs Linux containers, which the apps need."
	case runtime.ReasonWindowsContainers:
		// Switching affects the user's other Docker work, so the user does it.
		s.State = StepNeedsYou
		s.Text = "Docker Desktop is set to Windows containers, and the apps CashPilot runs are Linux containers."
		s.Action = StepAction{Kind: ActionRun, Op: OpLinuxEngine, Label: "Switch to Linux containers",
			Manual: "Right-click the Docker icon next to the clock, choose Switch to Linux containers, then click Check again."}
	default:
		s.State, s.Text = StepUnknown, "CashPilot reads this once Docker's engine runs."
	}
	return s
}

func otherHypervisorStep(f HostFacts, linuxUp bool) Step {
	s := Step{ID: "a10", Title: "Other virtual machine software"}
	names := strings.Join(f.OtherHypervisorNames, " and ")
	if names == "" {
		names = "Other virtual machine software"
	}
	switch {
	case f.OtherHypervisors == Yes && linuxUp:
		s.State, s.Text = StepDone, names+" is installed, and Docker runs next to it."
	case f.OtherHypervisors == Yes && hypervisorOffAtBoot(f):
		// The boot option itself needs admin rights to read, so this row cannot say
		// it is off: the change reads it first and changes nothing when it is auto.
		s.State = StepNeedsYou
		s.Text = names + " is installed, and Windows' hypervisor is not running, although virtualization and the Virtual Machine Platform feature are on. Older versions of " + names + " may have turned it off at boot, and Docker needs it. CashPilot reads that boot option first and changes it only when it is not set to start the hypervisor."
		s.Action = StepAction{Kind: ActionElevated, Op: OpHypervisorLaunch, Label: "Check and turn Windows' hypervisor back on",
			Manual: "Open PowerShell as administrator and run bcdedit /enum \"{current}\" (PowerShell needs the quotes). If hypervisorlaunchtype is not Auto, run bcdedit /set hypervisorlaunchtype auto, then restart the PC."}
	case f.OtherHypervisors == Yes:
		// Nothing here proves it is what stops Docker, so it is a note, not a step.
		s.State = StepNote
		s.Text = names + " is installed. It works next to WSL 2 but can run slower, and old versions can stop Windows' hypervisor at boot. If Docker does not start, update " + names + " to its newest version."
	case f.OtherHypervisors == No:
		s.State, s.Text = StepDone, "No VirtualBox or VMware drivers are installed."
	default:
		s.State, s.Text = StepUnknown, "CashPilot could not read whether VirtualBox or VMware is installed."
	}
	return s
}

// hypervisorOffAtBoot is the one case the hypervisor fix may be for: no hypervisor
// runs, yet the firmware flag (meaningful only when none runs) says virtualization
// is on, the Virtual Machine Platform feature is on, and no restart is pending
// that would start it anyway. Inside a VM the facts describe the outer machine.
func hypervisorOffAtBoot(f HostFacts) bool {
	return f.HypervisorPresent == No && f.Firmware == Yes && f.VMPlatformFeature == Yes &&
		f.RestartPending == No && f.IsVM != Yes
}

func virtualMachineStep(f HostFacts, linuxUp bool) Step {
	s := Step{ID: "a11", Title: "This PC is a virtual machine"}
	switch {
	case f.IsVM == Yes && linuxUp:
		s.State, s.Text = StepDone, "This PC is a virtual machine, and Docker runs in it."
	case f.IsVM == Yes:
		s.State = StepNeedsYou
		s.Text = "This PC is itself a virtual machine. Docker needs nested virtualization turned on in the machine that runs it. On Hyper-V, its owner turns this VM off and runs Set-VMProcessor -VMName <name> -ExposeVirtualizationExtensions $true."
	case f.IsVM == No:
		s.State, s.Text = StepDone, "This PC is not a virtual machine."
	default:
		s.State, s.Text = StepUnknown, "CashPilot could not read whether this PC is a virtual machine."
	}
	return s
}

func lanmanStep(f HostFacts, linuxUp bool) Step {
	s := Step{ID: "a12", Title: "Windows Server service"}
	if linuxUp && (f.LanmanServer == "manual" || f.LanmanServer == "disabled" || f.LanmanServer == "absent") {
		// Docker runs, so the service blocks nothing now.
		word := map[string]string{"manual": "is set to Manual", "disabled": "is turned off", "absent": "is missing"}[f.LanmanServer]
		s.State = StepNote
		s.Text = "Docker runs, but the Windows Server service (LanmanServer) " + word + ". If Docker stops starting after a restart, this is a place to look."
		return s
	}
	switch f.LanmanServer {
	case "auto":
		s.State, s.Text = StepDone, "The Server service (LanmanServer) starts automatically, as Docker needs."
	case "absent":
		s.State, s.Text = StepNeedsYou, "The Windows Server service (LanmanServer) is missing from this PC, and Docker needs it. Some clean-up tools remove it; Windows' own repair (Settings, System, Recovery) brings it back."
	case "manual", "disabled":
		word := map[string]string{"manual": "set to Manual", "disabled": "turned off"}[f.LanmanServer]
		s.State = StepNeedsYou
		s.Text = "The Windows Server service (LanmanServer) is " + word + ", and Docker needs it set to Automatic. Some clean-up tools turn it off."
		s.Action = StepAction{Kind: ActionElevated, Op: OpLanmanServer, Label: "Set it to Automatic",
			Manual: "Open PowerShell as administrator and run Set-Service LanmanServer -StartupType Automatic."}
	default:
		s.State, s.Text = StepUnknown, "CashPilot could not read how the Windows Server service (LanmanServer) starts."
	}
	return s
}
