package main

import (
	"context"
	"log"
	"os"
	goruntime "runtime"
	"sync"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/winvm"
)

// HostCheckResult is what the HostCheck binding returns: the facts about this PC,
// the runtime status read with them, whose Reason says why Docker is or is not
// ready, and the Docker checklist derived from both (Windows only).
type HostCheckResult struct {
	Facts         winvm.HostFacts `json:"facts"`
	Runtime       runtime.Status  `json:"runtime"`
	Steps         []winvm.Step    `json:"steps"`
	RuntimeNotice *Notification   `json:"runtimeNotice"`
	// SetupFinished is true when this reading ended the guided setup: this PC's
	// own Docker Desktop runs Linux containers (winvm.LocalLinuxEngine), and the
	// journal and RunOnce value are gone.
	SetupFinished bool `json:"setupFinished"`
}

// HostCheck reads this PC afresh (Windows edition, build and CPU, virtualization,
// WSL, Docker Desktop, docker-users, a pending restart) and the runtime status. It
// returns within winvm.ProbeTimeout; a probe that does not answer in time leaves
// its facts unknown. Outside Windows the facts say Supported false.
func (a *App) HostCheck() (HostCheckResult, error) {
	if err := a.ready(); err != nil {
		return HostCheckResult{}, err
	}
	facts := a.hostCache().refresh(a.ctx)
	status := a.engineStatus(a.catalog.ListVisible())
	steps := dockerSteps(facts, status)
	status.SetupTask = dockerSetupTask(steps)
	finished := winvm.LocalLinuxEngine(facts, status.Reason, localEngine(status))
	if finished {
		// This PC's Docker Desktop runs Linux containers: a guided setup that
		// waited for a restart is finished. An engine elsewhere, or Windows
		// containers, finishes nothing here. A setup whose journal could not be
		// cleared is not finished: the banner stays with it.
		if err := a.endSetup(); err != nil {
			log.Printf("setup: %v", err)
			finished = false
		}
	}
	return HostCheckResult{
		Facts: facts, Runtime: status, Steps: steps,
		RuntimeNotice: runtimeNotification(status), SetupFinished: finished,
	}, nil
}

// dockerSteps is the checklist for one reading. The engine answering proves the
// rows only when it is this PC's own Docker Desktop, so DOCKER_HOST and the
// Docker context go in with it.
func dockerSteps(facts winvm.HostFacts, status runtime.Status) []winvm.Step {
	return winvm.DockerSteps(facts, status.Reason, localEngine(status))
}

func dockerSetupTask(steps []winvm.Step) string {
	var unknown string
	for _, step := range steps {
		if step.State == winvm.StepNeedsYou {
			return step.Text
		}
		if step.ID == "a6" && step.State == winvm.StepUnknown {
			unknown = step.Text
		}
	}
	return unknown
}

// localEngine says the engine status reached is this PC's own Docker Desktop,
// read from DOCKER_HOST and the Docker context.
func localEngine(status runtime.Status) bool {
	return runtime.LocalDockerDesktop(os.Getenv("DOCKER_HOST"), status.Context)
}

// runtimeStatus is the Docker status with the native runtime's part added: which
// catalog services it can run here, and NativeAvailable only when that is at least
// one. Native registration alone used to count as ready, which told a Windows user
// with no Docker that the machine was ready when only one app could run.
func (a *App) runtimeStatus(visible []catalog.Service) runtime.Status {
	status := a.engineStatus(visible)
	steps := dockerSteps(a.hostCache().cached(), status)
	status.SetupTask = dockerSetupTask(steps)
	if goruntime.GOOS == "windows" && len(steps) == 0 && status.Available {
		status.SetupTask = "CashPilot is checking Docker Desktop on this PC."
	}
	return status
}

func (a *App) engineStatus(visible []catalog.Service) runtime.Status {
	status := a.runtime.Status(a.ctx)
	status.NativeServices = a.services.NativeServices(visible)
	status.NativeAvailable = a.services.HasNativeRuntime() && len(status.NativeServices) > 0
	return status
}

// refreshHostSetup starts a stale Windows reading even when the configured
// engine answers. GetAppState still returns the cached facts immediately.
func (a *App) refreshHostSetup() {
	h := a.hostCache()
	if goruntime.GOOS == "windows" || h.cached().OS == "windows" {
		h.hints(a.ctx)
	}
}

// hostFactsTTL is how long a background reading serves Status. "Check again" and
// HostCheck read afresh. The TTL bounds how often app state starts PowerShell.
const hostFactsTTL = 10 * time.Minute

// hostFacts caches the last reading of this PC for Status, which runs on every
// refresh and must never wait for PowerShell.
type hostFacts struct {
	mu         sync.Mutex
	facts      winvm.HostFacts
	at         time.Time
	refreshing bool
	read       func(context.Context) winvm.HostFacts
	updated    func()
	// started numbers each reading as it begins; stored is the number of the
	// reading in facts. A reading that started earlier never replaces a newer one.
	started, stored uint64
}

func newHostFacts() *hostFacts {
	return &hostFacts{read: winvm.Snapshot}
}

// hostCache returns the app's cache, creating it on first use so an App built in a
// test works without Startup.
func (a *App) hostCache() *hostFacts {
	a.hostOnce.Do(func() {
		if a.host == nil {
			a.host = newHostFacts()
		}
		a.host.updated = func() { a.emitEvent("runtime:host-checked") }
	})
	return a.host
}

// refresh reads the PC now and returns the reading. It stores it only when no
// reading that started later has been stored already: a slow background reading
// must not overwrite what HostCheck just read.
func (h *hostFacts) refresh(ctx context.Context) winvm.HostFacts {
	h.mu.Lock()
	h.started++
	n := h.started
	h.mu.Unlock()
	f := h.read(ctx)
	h.mu.Lock()
	if n > h.stored {
		h.facts, h.at, h.stored = f, time.Now(), n
	}
	h.mu.Unlock()
	return f
}

// cached returns the last reading, or an empty one before the first.
func (h *hostFacts) cached() winvm.HostFacts {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.facts
}

// hints returns the cached reading as runtime hints at once, and starts one
// background reading when the cache is empty or stale.
func (h *hostFacts) hints(ctx context.Context) runtime.HostHints {
	h.mu.Lock()
	f, at := h.facts, h.at
	stale := at.IsZero() || time.Since(at) > hostFactsTTL
	if stale && !h.refreshing {
		h.refreshing = true
		go func() {
			h.refresh(ctx)
			h.mu.Lock()
			h.refreshing = false
			h.mu.Unlock()
			if h.updated != nil {
				h.updated()
			}
		}()
	}
	h.mu.Unlock()
	if at.IsZero() {
		return runtime.HostHints{}
	}
	return hostHints(f)
}

// hostHints copies the facts Status needs. The Tri values are the same words the
// runtime package uses ("yes", "no", "unknown").
func hostHints(f winvm.HostFacts) runtime.HostHints {
	return runtime.HostHints{
		DockerInstalled:   string(f.DockerDesktopInstalled),
		DockerStarted:     string(f.DockerDesktopStarted),
		DockerRunning:     string(f.DockerDesktopRunning),
		Virtualization:    string(f.Virtualization),
		WSL:               f.WSL,
		WSLVersion:        f.WSLVersion,
		DockerBackend:     f.DockerBackend,
		DockerUsersListed: string(f.DockerUsersListed),
		DockerUsersToken:  string(f.DockerUsersToken),
		RestartPending:    string(f.RestartPending),
	}
}

// wireHostFacts hands the Docker provider the cached host facts on Windows and
// starts the first reading, so it is usually done before first run asks.
func (a *App) wireHostFacts(ctx context.Context, docker *runtime.DockerProvider) {
	if goruntime.GOOS != "windows" {
		return
	}
	h := a.hostCache()
	docker.SetHostHints(func() runtime.HostHints { return h.hints(ctx) })
	h.hints(ctx) // the cache is empty, so this starts the first reading
}
