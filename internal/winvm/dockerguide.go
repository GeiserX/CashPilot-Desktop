package winvm

import (
	"strconv"
	"strings"

	"github.com/GeiserX/CashPilot-Desktop/internal/i18n"
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

// checkAgainLabel is the checklist's own "Check again" button, as the frontend
// shows it, so a text that names the button says it the same way.
func checkAgainLabel() string { return i18n.T("dockerguide.checkAgain") }

// dockerTermsSentence says what the user is asked to accept, and that CashPilot
// never accepts it for them (https://docs.docker.com/subscription/desktop-license/).
func dockerTermsSentence() string { return i18n.T("go.winvm.guide.termsSentence") }

// biosHelp is the vendor help text the virtualization step shows.
func biosHelp() string { return i18n.T("go.winvm.guide.biosHelp") }

// hypervisorNames joins the names of the other virtual machine software found,
// or names it in general when the probe found no names.
func hypervisorNames(names []string) string {
	switch len(names) {
	case 0:
		return i18n.T("go.winvm.guide.a10.unnamed")
	case 1:
		return names[0]
	}
	return i18n.T("go.winvm.guide.a10.namesAnd", "list", strings.Join(names[:len(names)-1], i18n.T("go.winvm.guide.a10.namesSeparator")), "last", names[len(names)-1])
}

func windowsStep(f HostFacts, proven bool) Step {
	s := Step{ID: "a1", Title: i18n.T("go.winvm.guide.a1.title")}
	if f.Build == 0 {
		s.State, s.Text = StepUnknown, i18n.T("go.winvm.guide.a1.unknown")
		return s
	}
	win11 := f.Build >= Windows11Build
	name := "Windows 10"
	if win11 {
		name = "Windows 11"
	}
	build := strconv.Itoa(f.Build)
	supported := f.Build >= minWindows11Build || (!win11 && f.Build >= minWindows10Build)
	if supported || proven {
		s.State = StepDone
		if win11 {
			s.Text = i18n.T("go.winvm.guide.a1.win11", "build", build)
		} else {
			// The Windows 10 note, shown once: here and nowhere else.
			s.Text = i18n.T("go.winvm.guide.a1.win10", "build", build)
		}
		return s
	}
	need := minWindows10Build
	if win11 {
		need = minWindows11Build
	}
	s.State = StepNeedsYou
	s.Text = i18n.T("go.winvm.guide.a1.tooOld", "name", name, "build", build, "need", strconv.Itoa(need), "check", checkAgainLabel())
	s.Action = StepAction{Kind: ActionWindowsUpdate, Label: i18n.T("go.winvm.guide.a1.action")}
	return s
}

func virtualizationStep(f HostFacts, proven bool) Step {
	s := Step{ID: "a2", Title: i18n.T("go.winvm.guide.a2.title")}
	switch {
	case proven:
		s.State, s.Text = StepDone, i18n.T("go.winvm.guide.a2.done")
	case f.IsVM == Yes:
		// Inside a VM, HypervisorPresent describes the outer machine.
		s.State, s.Text = StepUnknown, i18n.T("go.winvm.guide.a2.inVM")
	case f.Virtualization == No:
		s.State = StepNeedsYou
		s.Text = i18n.T("go.winvm.guide.a2.off")
		s.Action = StepAction{Kind: ActionFirmware, Label: i18n.T("go.winvm.guide.a2.action")}
		s.Help, s.HelpURL = biosHelp(), VirtualizationHelpURL
	case f.Virtualization == Yes && f.HypervisorPresent == No && f.SLAT == No:
		// SLAT reads false whenever a hypervisor runs, so it means something only
		// when none does.
		s.State = StepNeedsYou
		s.Text = i18n.T("go.winvm.guide.a2.noSLAT")
	case f.Virtualization == Yes:
		s.State, s.Text = StepDone, i18n.T("go.winvm.guide.a2.on")
	default:
		s.State, s.Text = StepUnknown, i18n.T("go.winvm.guide.a2.unknown")
	}
	return s
}

func wslFeatureStep(f HostFacts, proven bool) Step {
	s := Step{ID: "a3", Title: i18n.T("go.winvm.guide.a3.title")}
	switch {
	case proven:
		s.State, s.Text = StepDone, i18n.T("go.winvm.guide.a3.proven")
	case f.WSL == WSLMissing:
		s.State = StepNeedsYou
		s.Text = i18n.T("go.winvm.guide.a3.missing")
		s.Action = StepAction{Kind: ActionElevated, Op: OpWSLInstall, Label: i18n.T("go.winvm.guide.a3.action"),
			Manual: i18n.T("go.winvm.guide.a3.manual", "command", "wsl --install --no-distribution")}
	case f.VMPlatformFeature == Yes:
		s.State, s.Text = StepDone, i18n.T("go.winvm.guide.a3.on")
	default:
		s.State, s.Text = StepUnknown, i18n.T("go.winvm.guide.a3.unknown")
	}
	return s
}

func wslVersionStep(f HostFacts, proven bool) Step {
	s := Step{ID: "a4", Title: i18n.T("go.winvm.guide.a4.title")}
	switch {
	case proven:
		s.State, s.Text = StepDone, i18n.T("go.winvm.guide.a4.proven")
	case f.WSL == WSLReady:
		s.State, s.Text = StepDone, i18n.T("go.winvm.guide.a4.ready", "version", f.WSLVersion, "min", MinWSLVersion)
	case f.WSL == WSLOutdated:
		s.State = StepNeedsYou
		if f.WSLVersion != "" {
			s.Text = i18n.T("go.winvm.guide.a4.outdatedVersion", "version", f.WSLVersion, "min", MinWSLVersion)
		} else {
			s.Text = i18n.T("go.winvm.guide.a4.outdated", "min", MinWSLVersion)
		}
		s.Action = StepAction{Kind: ActionElevated, Op: OpWSLUpdate, Label: i18n.T("go.winvm.guide.a4.action"),
			Manual: i18n.T("go.winvm.guide.a4.manual", "command", "wsl --update")}
	case f.WSL == WSLMissing:
		s.State, s.Text = StepNeedsYou, i18n.T("go.winvm.guide.a4.missing")
	default:
		s.State, s.Text = StepUnknown, i18n.T("go.winvm.guide.a4.unknown")
	}
	return s
}

func installedStep(f HostFacts, proven bool) Step {
	s := Step{ID: "a5", Title: i18n.T("go.winvm.guide.a5.title")}
	switch {
	case proven || f.DockerDesktopInstalled == Yes:
		s.State, s.Text = StepDone, i18n.T("go.winvm.guide.a5.done")
	case f.DockerDesktopInstalled == No:
		s.State = StepNeedsYou
		s.Text = i18n.T("go.winvm.guide.a5.missing")
		// URL is the download page: the second button, and the fallback when
		// this PC has no winget.
		s.Action = StepAction{Kind: ActionRun, Op: OpDockerInstall, Label: i18n.T("go.winvm.guide.a5.action"), URL: DockerDownloadURL}
	default:
		s.State, s.Text = StepUnknown, i18n.T("go.winvm.guide.a5.unknown")
	}
	return s
}

func runningStep(f HostFacts, engineUp bool) Step {
	s := Step{ID: "a6", Title: i18n.T("go.winvm.guide.a6.title")}
	switch {
	case engineUp:
		s.State, s.Text = StepDone, i18n.T("go.winvm.guide.a6.done")
	case f.DockerDesktopRunning == Yes:
		s.State = StepNeedsYou
		s.Text = i18n.T("go.winvm.guide.a6.starting")
		s.Action = StepAction{Kind: ActionShowDocker, Label: i18n.T("go.winvm.guide.action.showDocker")}
	case f.DockerDesktopRunning == No:
		s.State, s.Text = StepNeedsYou, i18n.T("go.winvm.guide.a6.stopped")
		s.Action = StepAction{Kind: ActionStartDocker, Label: i18n.T("go.winvm.guide.action.startDocker")}
	default:
		s.State, s.Text = StepUnknown, i18n.T("go.winvm.guide.a6.unknown")
	}
	return s
}

func termsStep(f HostFacts, reason string, engineUp bool) Step {
	s := Step{ID: "a7", Title: i18n.T("go.winvm.guide.a7.title")}
	open := StepAction{Kind: ActionStartDocker, Label: i18n.T("go.winvm.guide.action.startDocker")}
	if f.DockerDesktopRunning == Yes {
		open = StepAction{Kind: ActionShowDocker, Label: i18n.T("go.winvm.guide.action.showDocker")}
	}
	switch {
	case engineUp:
		s.State, s.Text = StepDone, i18n.T("go.winvm.guide.a7.done")
	case reason == runtime.ReasonFirstStart || (f.DockerDesktopInstalled == Yes && f.DockerDesktopStarted == No):
		s.State, s.Text, s.Action = StepNeedsYou, i18n.T("go.winvm.guide.a7.notOpened", "terms", dockerTermsSentence()), open
	default:
		// Docker writes its settings file on its first start, which may come before
		// the terms are accepted, so nothing here proves they were.
		s.State = StepUnknown
		s.Text = i18n.T("go.winvm.guide.a7.unknown")
		if f.DockerDesktopRunning == Yes {
			s.Action = open
		}
	}
	return s
}

// dockerUsersStep appears only when the engine refused this account: the WSL 2
// backend with Linux containers does not need the group.
func dockerUsersStep(f HostFacts) Step {
	s := Step{ID: "a8", Title: i18n.T("go.winvm.guide.a8.title"), State: StepNeedsYou}
	if f.DockerUsersListed == Yes && f.DockerUsersToken != Yes {
		s.Text = i18n.T("go.winvm.guide.a8.signIn", "check", checkAgainLabel())
		return s
	}
	if f.DockerUsersToken == Yes {
		// This session carries the group, so the group is not what refuses it:
		// adding the account again would change nothing.
		s.Text = i18n.T("go.winvm.guide.a8.refused")
		return s
	}
	s.Text = i18n.T("go.winvm.guide.a8.join")
	s.Action = StepAction{Kind: ActionElevated, Op: OpDockerUsers, Label: i18n.T("go.winvm.guide.a8.action"),
		Manual: i18n.T("go.winvm.guide.a8.manual")}
	return s
}

func linuxContainersStep(reason string) Step {
	s := Step{ID: "a9", Title: i18n.T("go.winvm.guide.a9.title")}
	switch reason {
	case runtime.ReasonRunning:
		s.State, s.Text = StepDone, i18n.T("go.winvm.guide.a9.done")
	case runtime.ReasonWindowsContainers:
		// Switching affects the user's other Docker work, so the user does it.
		s.State = StepNeedsYou
		s.Text = i18n.T("go.winvm.guide.a9.windows")
		s.Action = StepAction{Kind: ActionRun, Op: OpLinuxEngine, Label: i18n.T("go.winvm.guide.a9.action"),
			// Docker Desktop's menu is in English, so its item stays in English.
			Manual: i18n.T("go.winvm.guide.a9.manual", "menu", "Switch to Linux containers", "check", checkAgainLabel())}
	default:
		s.State, s.Text = StepUnknown, i18n.T("go.winvm.guide.a9.unknown")
	}
	return s
}

func otherHypervisorStep(f HostFacts, linuxUp bool) Step {
	s := Step{ID: "a10", Title: i18n.T("go.winvm.guide.a10.title")}
	names := hypervisorNames(f.OtherHypervisorNames)
	switch {
	case f.OtherHypervisors == Yes && linuxUp:
		s.State, s.Text = StepDone, i18n.T("go.winvm.guide.a10.coexists", "names", names)
	case f.OtherHypervisors == Yes && hypervisorOffAtBoot(f):
		// The boot option itself needs admin rights to read, so this row cannot say
		// it is off: the change reads it first and changes nothing when it is auto.
		s.State = StepNeedsYou
		s.Text = i18n.T("go.winvm.guide.a10.offAtBoot", "names", names)
		s.Action = StepAction{Kind: ActionElevated, Op: OpHypervisorLaunch, Label: i18n.T("go.winvm.guide.a10.action"),
			Manual: i18n.T("go.winvm.guide.a10.manual", "enum", `bcdedit /enum "{current}"`, "setting", "hypervisorlaunchtype", "value", "Auto", "set", "bcdedit /set hypervisorlaunchtype auto")}
	case f.OtherHypervisors == Yes:
		// Nothing here proves it is what stops Docker, so it is a note, not a step.
		s.State = StepNote
		s.Text = i18n.T("go.winvm.guide.a10.note", "names", names)
	case f.OtherHypervisors == No:
		s.State, s.Text = StepDone, i18n.T("go.winvm.guide.a10.none")
	default:
		s.State, s.Text = StepUnknown, i18n.T("go.winvm.guide.a10.unknown")
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
	s := Step{ID: "a11", Title: i18n.T("go.winvm.guide.a11.title")}
	switch {
	case f.IsVM == Yes && linuxUp:
		s.State, s.Text = StepDone, i18n.T("go.winvm.guide.a11.done")
	case f.IsVM == Yes:
		s.State = StepNeedsYou
		s.Text = i18n.T("go.winvm.guide.a11.nested", "command", "Set-VMProcessor -VMName <name> -ExposeVirtualizationExtensions $true")
	case f.IsVM == No:
		s.State, s.Text = StepDone, i18n.T("go.winvm.guide.a11.notVM")
	default:
		s.State, s.Text = StepUnknown, i18n.T("go.winvm.guide.a11.unknown")
	}
	return s
}

func lanmanStep(f HostFacts, linuxUp bool) Step {
	s := Step{ID: "a12", Title: i18n.T("go.winvm.guide.a12.title")}
	if linuxUp && (f.LanmanServer == "manual" || f.LanmanServer == "disabled" || f.LanmanServer == "absent") {
		// Docker runs, so the service blocks nothing now.
		s.State = StepNote
		s.Text = i18n.T("go.winvm.guide.a12.runs", "start", f.LanmanServer)
		return s
	}
	switch f.LanmanServer {
	case "auto":
		s.State, s.Text = StepDone, i18n.T("go.winvm.guide.a12.auto")
	case "absent":
		s.State, s.Text = StepNeedsYou, i18n.T("go.winvm.guide.a12.absent")
	case "manual", "disabled":
		s.State = StepNeedsYou
		s.Text = i18n.T("go.winvm.guide.a12.needsAuto", "start", f.LanmanServer)
		s.Action = StepAction{Kind: ActionElevated, Op: OpLanmanServer, Label: i18n.T("go.winvm.guide.a12.action"),
			Manual: i18n.T("go.winvm.guide.a12.manual", "command", "Set-Service LanmanServer -StartupType Automatic")}
	default:
		s.State, s.Text = StepUnknown, i18n.T("go.winvm.guide.a12.unknown")
	}
	return s
}
