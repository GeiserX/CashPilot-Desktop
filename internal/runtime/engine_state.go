package runtime

import (
	"strings"
	"sync/atomic"
)

// Why the Docker engine is or is not ready, one code per state a person has to act
// on differently. Status.Reason carries one, and Status.Message the plain words for
// it.
const (
	ReasonRunning           = "running"
	ReasonNotInstalled      = "not-installed"
	ReasonNotRunning        = "not-running"
	ReasonUnreachable       = "unreachable"
	ReasonWindowsContainers = "windows-containers"
	ReasonAccessDenied      = "access-denied"
	ReasonWSLMissing        = "wsl-missing"
	ReasonWSLOutdated       = "wsl-outdated"
	ReasonVirtualizationOff = "virtualization-off"
	ReasonFirstStart        = "first-start-pending"
)

// Hint values, the same words internal/winvm uses for a fact that can be unknown.
const (
	HintYes = "yes"
	HintNo  = "no"
)

// HostHints are facts about this PC that tell apart why the engine does not
// answer. internal/winvm reads them on Windows and the app hands them in through
// SetHostHints; this package does not import it, so internal/winvm can later use
// RunElevated. A zero value knows nothing, and every hint not "yes" or "no" is
// unknown.
type HostHints struct {
	DockerInstalled string // Docker Desktop is installed
	DockerStarted   string // Docker Desktop was opened on this account before
	DockerRunning   string // the Docker Desktop app runs
	Virtualization  string // the CPU's virtualization is on
	WSL             string // ready, missing, outdated or unknown
	WSLVersion      string
	// DockerBackend is the engine Docker Desktop's settings choose: "wsl",
	// "hyperv" or unknown. Docker on its Hyper-V backend does not need WSL.
	DockerBackend string
	// docker-users: on the group's member list, and in this session's token.
	DockerUsersListed string
	DockerUsersToken  string
	RestartPending    string
}

// endpointState is what dialling the engine's endpoint itself said, read from the
// error number and never from its text.
type endpointState int

const (
	endpointUnknown endpointState = iota // not a local socket or pipe, or not tried
	endpointPresent                      // something accepted the connection
	endpointAbsent                       // no socket file or pipe
	endpointDenied                       // it exists, and this account may not open it
	endpointRefused                      // a socket file nobody listens on
)

// dockerEvidence is everything Status saw, for classify to turn into a reason.
type dockerEvidence struct {
	goos     string
	pingOK   bool
	osType   string        // the daemon's Ostype header, when the ping answered
	endpoint endpointState // when the ping failed
	hostSet  bool          // DOCKER_HOST or a tcp context names an endpoint, so it is not the default local one
	tools    bool          // a runtime CLI is on PATH
	install  bool          // a runtime's files sit at a known path (macOS and Linux)
	host     HostHints
}

// classify turns what Status saw into a reason and its message. It is pure, so
// every branch is tested without an engine.
func classify(ev dockerEvidence) (string, string) {
	win := ev.goos == "windows"
	if ev.pingOK {
		if strings.EqualFold(ev.osType, "windows") {
			return ReasonWindowsContainers, "Docker Desktop is set to Windows containers. The apps CashPilot runs are Linux containers, so none of them can start there. Right-click the Docker icon next to the clock, choose \"Switch to Linux containers\", then click Check again."
		}
		return ReasonRunning, "Connected to a Docker-compatible runtime."
	}
	h := ev.host
	if ev.endpoint == endpointDenied {
		return ReasonAccessDenied, accessDeniedMessage(win, h)
	}
	if ev.hostSet {
		return ReasonUnreachable, "CashPilot cannot reach the Docker engine that DOCKER_HOST or your Docker context names. Check that it is running and reachable, then click Check again."
	}
	if notInstalled(ev) {
		if win {
			return ReasonNotInstalled, "Docker Desktop is not installed. Install it with the guide below, then click Check again."
		}
		return ReasonNotInstalled, "Choose one of the supported runtimes below so CashPilot can run earning services on this machine."
	}
	const unreachable = "CashPilot cannot reach a Docker-compatible engine on this computer."
	if !win {
		if ev.install || ev.endpoint == endpointRefused {
			return ReasonNotRunning, "A container runtime is installed, but it is not running yet. Start your runtime and try again."
		}
		return ReasonUnreachable, unreachable
	}
	// RestartPending is yes only for Windows Update and component servicing, never
	// for routine file renames alone (internal/winvm restartPending).
	restart := ""
	if h.RestartPending == HintYes {
		restart = " Windows is also waiting for a restart. Restart the PC first."
	}
	wsl := h.DockerBackend != "hyperv"
	switch {
	case h.Virtualization == HintNo:
		return ReasonVirtualizationOff, "Virtualization is turned off in this PC's firmware (BIOS or UEFI), and Docker needs it. Restart into the firmware settings and turn on the setting called Intel Virtualization Technology, VT-x, AMD-V or SVM Mode." + restart
	case wsl && h.WSL == "missing":
		return ReasonWSLMissing, "Docker needs WSL, the Linux layer built into Windows, and it is not installed. Open PowerShell as administrator, run wsl --install --no-distribution, then restart the PC." + restart
	case wsl && h.WSL == "outdated":
		have := "too old"
		if h.WSLVersion != "" {
			have = "version " + h.WSLVersion
		}
		return ReasonWSLOutdated, "WSL on this PC is " + have + ", and Docker needs 2.1.5 or newer. Run wsl --update, then open Docker Desktop again." + restart
	case h.DockerInstalled == HintYes && h.DockerStarted == HintNo && h.DockerRunning != HintYes:
		return ReasonFirstStart, "Docker Desktop is installed but has not been opened on this account yet. Open it and accept Docker's terms. It is free for personal use and for small businesses." + restart
	case h.DockerRunning == HintYes:
		return ReasonNotRunning, "Docker Desktop is open, but its engine is not running yet. It can take a minute or two. If Docker Desktop shows its terms or asks you to sign in, finish that first, then click Check again." + restart
	}
	if h.DockerInstalled == HintNo {
		// A docker CLI is on PATH (scoop, a manual install) but Docker Desktop is not.
		return ReasonUnreachable, "Docker Desktop is not installed and no Docker engine answers. If you use another runtime, start it, then click Check again." + restart
	}
	if h.DockerInstalled != HintYes {
		return ReasonUnreachable, unreachable + restart
	}
	return ReasonNotRunning, "Docker Desktop is installed, but its engine is not running. Open Docker Desktop, wait until it says the engine is running, then click Check again." + restart
}

// notInstalled is true only when nothing suggests a runtime exists: no CLI on
// PATH, nothing at the engine's endpoint and, on Windows, no Docker Desktop. On
// Windows an unread Docker Desktop hint does not count as "not installed" unless
// the endpoint is proven absent too, and a running Docker Desktop app always means
// it is installed (its engine pipe appears only once the engine starts).
func notInstalled(ev dockerEvidence) bool {
	if ev.tools || ev.install {
		return false
	}
	switch ev.endpoint {
	case endpointPresent, endpointRefused, endpointDenied:
		return false
	}
	if ev.goos == "windows" {
		if ev.host.DockerRunning == HintYes {
			return false
		}
		switch ev.host.DockerInstalled {
		case HintYes:
			return false
		case HintNo:
			return true
		}
	}
	return ev.endpoint == endpointAbsent
}

func accessDeniedMessage(win bool, h HostHints) string {
	if !win {
		return "The container runtime refused this account. On Linux, add your user to the docker group (sudo usermod -aG docker $USER), then sign out and back in."
	}
	if h.DockerUsersListed == HintYes && h.DockerUsersToken != HintYes {
		return "Your Windows account is in the docker-users group, but Windows applies a new group only after you sign in again. Sign out and back in, then click Check again."
	}
	return "Docker is running, but your Windows account may not use it. This happens when another account installed Docker Desktop. An administrator must add your account to the docker-users group, then you sign out and back in."
}

// hostHintsNow returns the hints the app handed in, or none.
func (p *DockerProvider) hostHintsNow() HostHints {
	if fn := p.hostHints.Load(); fn != nil {
		return (*fn)()
	}
	return HostHints{}
}

// SetHostHints hands Status a way to read facts about this PC. fn must return at
// once (a cached reading): Status runs on every app refresh.
func (p *DockerProvider) SetHostHints(fn func() HostHints) {
	p.hostHints.Store(&fn)
}

// hostHintsFn is the field type, kept here with its methods.
type hostHintsFn = atomic.Pointer[func() HostHints]
