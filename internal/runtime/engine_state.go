package runtime

import (
	"strings"
	"sync/atomic"

	"github.com/GeiserX/CashPilot-Desktop/internal/i18n"
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
			return ReasonWindowsContainers, i18n.T("go.runtime.engine.windowsContainers")
		}
		return ReasonRunning, i18n.T("go.runtime.engine.running")
	}
	h := ev.host
	if ev.endpoint == endpointDenied {
		return ReasonAccessDenied, accessDeniedMessage(win, h)
	}
	if ev.hostSet {
		return ReasonUnreachable, i18n.T("go.runtime.engine.hostUnreachable", "var", "DOCKER_HOST")
	}
	if notInstalled(ev) {
		if win {
			return ReasonNotInstalled, i18n.T("go.runtime.engine.desktopNotInstalled")
		}
		return ReasonNotInstalled, i18n.T("go.runtime.engine.chooseRuntime")
	}
	unreachable := i18n.T("go.runtime.engine.unreachable")
	if !win {
		if ev.install || ev.endpoint == endpointRefused {
			return ReasonNotRunning, i18n.T("go.runtime.engine.notRunning")
		}
		return ReasonUnreachable, unreachable
	}
	// RestartPending is yes only for Windows Update and component servicing, never
	// for routine file renames alone (internal/winvm restartPending).
	restart := ""
	if h.RestartPending == HintYes {
		restart = " " + i18n.T("go.runtime.engine.restartPending")
	}
	wsl := h.DockerBackend != "hyperv"
	switch {
	case h.Virtualization == HintNo:
		return ReasonVirtualizationOff, i18n.T("go.runtime.engine.virtualizationOff") + restart
	case wsl && h.WSL == "missing":
		return ReasonWSLMissing, i18n.T("go.runtime.engine.wslMissing", "command", "wsl --install --no-distribution") + restart
	case wsl && h.WSL == "outdated":
		message := i18n.T("go.runtime.engine.wslTooOld", "min", "2.1.5", "command", "wsl --update")
		if h.WSLVersion != "" {
			message = i18n.T("go.runtime.engine.wslOutdated", "version", h.WSLVersion, "min", "2.1.5", "command", "wsl --update")
		}
		return ReasonWSLOutdated, message + restart
	case h.DockerInstalled == HintYes && h.DockerStarted == HintNo && h.DockerRunning != HintYes:
		return ReasonFirstStart, i18n.T("go.runtime.engine.firstStart") + restart
	case h.DockerRunning == HintYes:
		return ReasonNotRunning, i18n.T("go.runtime.engine.engineStarting") + restart
	}
	if h.DockerInstalled == HintNo {
		// A docker CLI is on PATH (scoop, a manual install) but Docker Desktop is not.
		return ReasonUnreachable, i18n.T("go.runtime.engine.noEngine") + restart
	}
	if h.DockerInstalled != HintYes {
		return ReasonUnreachable, unreachable + restart
	}
	return ReasonNotRunning, i18n.T("go.runtime.engine.engineStopped") + restart
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
		return i18n.T("go.runtime.engine.deniedLinux", "command", "sudo usermod -aG docker $USER")
	}
	if h.DockerUsersListed == HintYes && h.DockerUsersToken != HintYes {
		return i18n.T("go.runtime.engine.deniedSignIn", "group", "docker-users")
	}
	return i18n.T("go.runtime.engine.deniedWindows", "group", "docker-users")
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
