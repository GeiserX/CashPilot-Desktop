package runtime

import (
	"strings"
	"testing"
)

// winDown is a Windows PC whose Docker engine did not answer, with Docker Desktop
// installed and everything else ready; each case breaks one thing.
func winDown(edit func(*dockerEvidence)) dockerEvidence {
	ev := dockerEvidence{
		goos:     "windows",
		endpoint: endpointAbsent,
		tools:    true,
		host: HostHints{
			DockerInstalled: HintYes,
			DockerStarted:   HintYes,
			DockerRunning:   HintNo,
			Virtualization:  HintYes,
			WSL:             "ready",
		},
	}
	if edit != nil {
		edit(&ev)
	}
	return ev
}

func TestClassifyGivesEachDockerStateItsOwnReason(t *testing.T) {
	cases := []struct {
		name   string
		ev     dockerEvidence
		reason string
		says   string // a phrase the message must carry
	}{
		{"running", dockerEvidence{goos: "windows", pingOK: true, osType: "linux"}, ReasonRunning, "Connected"},
		{"daemon that sends no Ostype counts as Linux", dockerEvidence{goos: "linux", pingOK: true}, ReasonRunning, "Connected"},
		{"Windows-containers mode is not ready", dockerEvidence{goos: "windows", pingOK: true, osType: "windows"}, ReasonWindowsContainers, "Switch to Linux containers"},
		{"not installed: no Docker Desktop, no CLI, no pipe", winDown(func(ev *dockerEvidence) {
			ev.tools, ev.host.DockerInstalled = false, HintNo
		}), ReasonNotInstalled, "Docker Desktop is not installed"},
		{"not installed before the host was read: no CLI and no pipe", winDown(func(ev *dockerEvidence) {
			ev.tools, ev.host = false, HostHints{}
		}), ReasonNotInstalled, "not installed"},
		{"a CLI on PATH is not 'not installed'", winDown(func(ev *dockerEvidence) {
			ev.host.DockerInstalled = HintNo
		}), ReasonUnreachable, "no Docker engine answers"},
		{"Docker Desktop.exe runs before its pipe exists, install unread", winDown(func(ev *dockerEvidence) {
			ev.tools, ev.host.DockerInstalled, ev.host.DockerRunning = false, "unknown", HintYes
		}), ReasonNotRunning, "engine is not running yet"},
		{"installed, engine not running", winDown(nil), ReasonNotRunning, "Open Docker Desktop"},
		{"Docker Desktop open, engine still starting", winDown(func(ev *dockerEvidence) {
			ev.host.DockerRunning = HintYes
		}), ReasonNotRunning, "terms"},
		{"access denied, not in docker-users", winDown(func(ev *dockerEvidence) {
			ev.endpoint, ev.host.DockerUsersListed, ev.host.DockerUsersToken = endpointDenied, HintNo, HintNo
		}), ReasonAccessDenied, "docker-users"},
		{"access denied, in the group but not in this sign-in", winDown(func(ev *dockerEvidence) {
			ev.endpoint, ev.host.DockerUsersListed, ev.host.DockerUsersToken = endpointDenied, HintYes, HintNo
		}), ReasonAccessDenied, "Sign out and back in"},
		{"WSL missing", winDown(func(ev *dockerEvidence) { ev.host.WSL = "missing" }), ReasonWSLMissing, "wsl --install --no-distribution"},
		{"WSL too old", winDown(func(ev *dockerEvidence) {
			ev.host.WSL, ev.host.WSLVersion = "outdated", "2.0.14.0"
		}), ReasonWSLOutdated, "version 2.0.14.0"},
		{"Hyper-V backend is never told to install WSL", winDown(func(ev *dockerEvidence) {
			ev.host.DockerBackend, ev.host.WSL = "hyperv", "missing"
		}), ReasonNotRunning, "Open Docker Desktop"},
		{"Hyper-V backend is never told to update WSL", winDown(func(ev *dockerEvidence) {
			ev.host.DockerBackend, ev.host.WSL, ev.host.WSLVersion = "hyperv", "outdated", "2.0.9.0"
		}), ReasonNotRunning, "Open Docker Desktop"},
		{"an unread backend still hears about WSL", winDown(func(ev *dockerEvidence) {
			ev.host.DockerBackend, ev.host.WSL = "", "missing"
		}), ReasonWSLMissing, "wsl --install"},
		{"virtualization off", winDown(func(ev *dockerEvidence) { ev.host.Virtualization = HintNo }), ReasonVirtualizationOff, "VT-x"},
		{"virtualization off wins over WSL", winDown(func(ev *dockerEvidence) {
			ev.host.Virtualization, ev.host.WSL = HintNo, "missing"
		}), ReasonVirtualizationOff, "firmware"},
		{"never opened on this account", winDown(func(ev *dockerEvidence) { ev.host.DockerStarted = HintNo }), ReasonFirstStart, "accept Docker's terms"},
		{"restart pending is said", winDown(func(ev *dockerEvidence) {
			ev.host.WSL, ev.host.RestartPending = "missing", HintYes
		}), ReasonWSLMissing, "waiting for a restart"},
		{"a remote DOCKER_HOST is never 'not installed' and gets no Windows advice", winDown(func(ev *dockerEvidence) {
			ev.hostSet, ev.tools, ev.endpoint, ev.host.DockerInstalled, ev.host.WSL = true, false, endpointUnknown, HintNo, "missing"
		}), ReasonUnreachable, "DOCKER_HOST"},
		{"Windows with no conclusive install evidence", dockerEvidence{goos: "windows"}, ReasonUnreachable, "cannot reach"},
		{"macOS with only a CLI", dockerEvidence{goos: "darwin", endpoint: endpointAbsent, tools: true}, ReasonUnreachable, "cannot reach"},
		{"linux: nothing anywhere", dockerEvidence{goos: "linux", endpoint: endpointAbsent}, ReasonNotInstalled, "Choose one of the supported runtimes"},
		{"linux: a stale socket file", dockerEvidence{goos: "linux", endpoint: endpointRefused}, ReasonNotRunning, "not running yet"},
		{"macOS: Docker.app installed, PATH empty", dockerEvidence{goos: "darwin", endpoint: endpointAbsent, install: true}, ReasonNotRunning, "not running yet"},
		{"linux: socket denied", dockerEvidence{goos: "linux", endpoint: endpointDenied, tools: true}, ReasonAccessDenied, "docker group"},
	}
	for _, c := range cases {
		reason, message := classify(c.ev)
		if reason != c.reason {
			t.Errorf("%s: reason %q, want %q (message %q)", c.name, reason, c.reason, message)
			continue
		}
		if !strings.Contains(message, c.says) {
			t.Errorf("%s: message %q does not say %q", c.name, message, c.says)
		}
	}
}

// The one message every failure used to share must be gone from every Windows
// state that has its own reason: it called a missing Docker "installed".
func TestClassifyNeverCallsAMissingRuntimeInstalled(t *testing.T) {
	ev := winDown(func(ev *dockerEvidence) { ev.tools, ev.host.DockerInstalled = false, HintNo })
	_, message := classify(ev)
	if strings.Contains(message, "is installed") {
		t.Errorf("a PC without Docker is told %q", message)
	}
	// A docker CLI on PATH without Docker Desktop must not be told Docker
	// Desktop is installed either.
	ev = winDown(func(ev *dockerEvidence) { ev.host.DockerInstalled = HintNo })
	if _, message = classify(ev); strings.Contains(message, "Docker Desktop is installed") {
		t.Errorf("a PC with only a docker CLI is told %q", message)
	}
}
