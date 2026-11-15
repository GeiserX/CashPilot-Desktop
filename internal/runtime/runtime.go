package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"regexp"
	goruntime "runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

const (
	LabelManaged = "cashpilot.managed"
	LabelService = "cashpilot.service"
	LabelVersion = "cashpilot.version"
	LabelRuntime = "cashpilot.runtime"
)

type Provider interface {
	Status(ctx context.Context) Status
	Deploy(ctx context.Context, spec DeploySpec, progress func(string)) (ContainerInfo, error)
	Start(ctx context.Context, slug string) error
	// Stop and Restart take the grace period the service's catalog entry asks for,
	// in seconds. 0 means the caller had no entry to read (a service the catalog has
	// dropped), and the provider falls back to what the running unit itself says.
	// The value has to be passed in because only the caller holds the catalog, and
	// reading it off the container instead would pin every service already deployed
	// to the number its entry carried on the day it was created.
	Stop(ctx context.Context, slug string, stopTimeoutSeconds int) error
	Restart(ctx context.Context, slug string, stopTimeoutSeconds int) error
	// Remove removes the running unit. It destroys stored data only when
	// RemoveOptions says so; see RemoveOptions for why the default is to keep it.
	Remove(ctx context.Context, slug string, opts RemoveOptions) error
	// PlanRemoval reports what removing this service would delete, so the question
	// put to the user names the actual data. critical maps container paths the
	// catalog marks unrecoverable to what is lost; nil means no catalog entry could
	// be consulted, which counts as "assume all of it matters".
	PlanRemoval(ctx context.Context, slug string, critical map[string]string) (RemovalPlan, error)
	Logs(ctx context.Context, slug string, lines int) (string, error)
	List(ctx context.Context) ([]ContainerInfo, error)
}

type Status struct {
	Available bool `json:"available"`
	// NativeAvailable reports whether the always-on native process runtime is
	// registered (and therefore available: it has no external dependency, no daemon
	// or socket to reach). It is an ADDITIVE signal set by the app layer from the
	// services manager — Available keeps its Docker-only meaning, which other code
	// still reads — so onboarding can proceed on Docker OR native.
	NativeAvailable bool              `json:"nativeAvailable"`
	Kind            string            `json:"kind"`
	Message         string            `json:"message"`
	Version         string            `json:"version"`
	Context         string            `json:"context"`
	Tools           map[string]string `json:"tools"`
}

type InstallGuide struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Platforms   []string `json:"platforms"`
	URL         string   `json:"url"`
	Commands    []string `json:"commands"`
	Notes       []string `json:"notes"`
}

type DeploySpec struct {
	Slug    string
	Service catalog.Service
	Env     map[string]string
	// Instance is which copy of the service this is. 0 and 1 both mean the first,
	// which keeps the name and labels it always had; see LabelInstance.
	Instance int
}

type ContainerInfo struct {
	Slug        string  `json:"slug"`
	ContainerID string  `json:"containerId"`
	Name        string  `json:"name"`
	Image       string  `json:"image"`
	Status      string  `json:"status"`
	CPUPercent  float64 `json:"cpuPercent"`
	MemoryMB    float64 `json:"memoryMb"`
	// Instance is which copy of its service this unit is: 1 for the first (and for
	// every unit of a runtime that has no copies), N for cashpilot-<slug>-N.
	Instance int `json:"instance"`
	// What the usage graphs (internal/usage) need beyond the two numbers above.
	// CPUKnown and MemoryKnown say whether each was actually read: a failed stats
	// read leaves 0 in the field, and 0 is not a reading. NetRxBytes and NetTxBytes
	// are the cumulative network counters since the container started, nil when
	// none exist (host networking, a native program): unknown, never 0.
	CPUKnown    bool    `json:"-"`
	MemoryKnown bool    `json:"-"`
	NetRxBytes  *uint64 `json:"-"`
	NetTxBytes  *uint64 `json:"-"`
}

type DockerProvider struct {
	// fence keeps bridge earners off the LAN (lanfence.go). The zero value works:
	// with no setting wired in, the setting reads as auto.
	fence fence
	// replacing holds a *sync.Mutex per container name: a deploy holds it from
	// settling a set-aside leftover to its end, and recovery takes it to settle one,
	// so recovery never takes a replacement a deploy has made and not yet started.
	// Always taken after fence.reconcileMu, never before.
	replacing sync.Map
}

// lockReplacing takes name's replacement lock (replacing) and returns its release.
func (p *DockerProvider) lockReplacing(name string) func() {
	mu, _ := p.replacing.LoadOrStore(name, new(sync.Mutex))
	mu.(*sync.Mutex).Lock()
	return mu.(*sync.Mutex).Unlock
}

func NewDockerProvider() *DockerProvider {
	return &DockerProvider{}
}

// SetIsolationSetting wires in where the LAN isolation setting (auto, on, off) is read
// from. Call it before the first deploy.
func (p *DockerProvider) SetIsolationSetting(read func() string) {
	p.fence.setting = read
}

// SetFenceGated wires in which services the catalog marks fence_gated, so the fence
// also covers a container made before LabelFenceGated existed (a Mysterium deployed
// on the host's network by an older version). Call it before the first deploy.
func (p *DockerProvider) SetFenceGated(gated func(slug string) bool) {
	p.fence.gatedSlug = gated
}

// SetIsolationMemory wires in where "isolation is on" is kept, so a worker stays
// fenced after Docker loses every trace of it. Call it before the first deploy.
func (p *DockerProvider) SetIsolationMemory(read func() bool, write func(on bool)) {
	p.fence.remembered = read
	p.fence.remember = write
}

// SetEgressLookup wires in how this machine's public IP is read, so the fence also
// drops it and an earner cannot reach a port the router forwards back into the LAN.
// Call it before the first deploy.
func (p *DockerProvider) SetEgressLookup(lookup func(ctx context.Context) string) {
	p.fence.lookupEgress = lookup
}

// FenceStatus is the cached LAN isolation status. No Docker call.
func (p *DockerProvider) FenceStatus() FenceStatus {
	return p.fence.Status()
}

// ReconcileFence puts the fence back if it went and refreshes its status: at startup
// and every FenceRecheck. Never fails; a failure shows in the status.
func (p *DockerProvider) ReconcileFence(ctx context.Context) {
	cli, err := dockerClient()
	if err != nil {
		p.fence.setStatus(func(s *FenceStatus) { *s = FenceStatus{State: "n/a"} })
		return
	}
	defer cli.Close()
	if _, err := cli.Ping(ctx, client.PingOptions{NegotiateAPIVersion: true}); err != nil {
		p.fence.setStatus(func(s *FenceStatus) { *s = FenceStatus{State: "n/a"} })
		return
	}
	if facts := daemonFacts(ctx, cli); facts.OSType != "linux" {
		p.fence.setStatus(func(s *FenceStatus) { *s = FenceStatus{State: "n/a"} })
		return
	}
	p.fence.reconcile(ctx, cli)
}

// EnableFence turns isolation on for an upgraded install: applies the fence and
// returns the status, whose Outside lists the running earners still to redeploy.
func (p *DockerProvider) EnableFence(ctx context.Context) (FenceStatus, error) {
	cli, err := dockerClient()
	if err != nil {
		return p.fence.Status(), err
	}
	defer cli.Close()
	return p.fence.enable(ctx, cli)
}

func (p *DockerProvider) Status(ctx context.Context) Status {
	tools := detectTools()
	cli, err := dockerClient()
	if err != nil {
		return Status{
			Available: false,
			Kind:      "existing-docker",
			Message:   friendlyRuntimeMessage(tools),
			Tools:     tools,
		}
	}
	defer cli.Close()

	ping, err := cli.Ping(ctx, client.PingOptions{NegotiateAPIVersion: true})
	if err != nil {
		return Status{
			Available: false,
			Kind:      "existing-docker",
			Message:   "A container runtime is installed, but it is not running yet. Start your runtime and try again.",
			Tools:     tools,
		}
	}
	version, _ := cli.ServerVersion(ctx, client.ServerVersionOptions{})
	return Status{
		Available: true,
		Kind:      "existing-docker",
		Message:   "Connected to a Docker-compatible runtime.",
		Version:   ping.APIVersion + " / " + version.Version,
		Context:   dockerContext(),
		Tools:     tools,
	}
}

func (p *DockerProvider) Deploy(ctx context.Context, spec DeploySpec, progress func(string)) (ContainerInfo, error) {
	cli, err := dockerClient()
	if err != nil {
		return ContainerInfo{}, err
	}
	defer cli.Close()

	svc := spec.Service
	if svc.Docker.Image == "" {
		return ContainerInfo{}, fmt.Errorf("%s has no Docker image", svc.Name)
	}
	// Two copies would share the same host directories and fight over the same host
	// ports. The manager refuses this first; this is the backstop.
	if spec.Instance > 1 && (len(svc.Docker.Volumes) > 0 || svc.Docker.PublishesPorts()) {
		return ContainerInfo{}, fmt.Errorf("%s has volumes or published ports, so it runs once", svc.Name)
	}

	// Everything that can fail on the catalog entry alone is built and checked
	// BEFORE the running container is removed. A redeploy removes the existing
	// cashpilot-<slug> container, so a port string, a blocked device or a malformed
	// mem_limit discovered after that point would leave the user with no container
	// at all: an entry-level mistake would take down an earner that was working a
	// second earlier, and the deploy that replaced it never existed. The pull is on
	// this side of the line for the same reason — a registry outage or a typo'd
	// image must not cost the running container either.
	// Which CPU the containers run on is the DAEMON's answer, not this process's.
	// On Docker Desktop and Podman Desktop the app is a macOS or Windows binary while
	// the containers run in a Linux VM, and it is that VM's architecture an image
	// manifest is matched against — so runtime.GOARCH would be the wrong question.
	facts := daemonFacts(ctx, cli)
	image := ImageForArch(svc.Docker, facts.Architecture)

	env, err := copyEnv(svc, buildEnv(svc, spec.Env), spec.Instance)
	if err != nil {
		return ContainerInfo{}, fmt.Errorf("%s copy %d: %w", svc.Name, spec.Instance, err)
	}
	ports, bindings, err := buildServicePorts(svc.Docker, env)
	if err != nil {
		return ContainerInfo{}, fmt.Errorf("%s was not deployed: %w. The running container was not touched", svc.Name, err)
	}
	if err := checkRuntime(svc, facts.Runtimes); err != nil {
		return ContainerInfo{}, err
	}
	mounts := buildMounts(svc.Docker.Volumes, env)

	stopTimeout := StopTimeoutSeconds(svc)
	name := copyName(spec.Slug, spec.Instance)
	config := &container.Config{
		Image:        image,
		Env:          envSlice(env),
		ExposedPorts: ports,
		// The grace period travels with the container, so stopping it later does not
		// need the catalog to find it again.
		StopTimeout: &stopTimeout,
		Labels:      gatedLabels(containerLabels(spec.Slug, spec.Instance), svc),
		Hostname:    name,
	}
	if svc.Docker.Command != "" {
		config.Cmd = buildCommandArgs(svc.Docker.Command, env)
	}
	config.Entrypoint = entrypointFor(svc)

	hostConfig, err := buildHostConfig(svc, bindings, mounts, facts.OSType)
	if err != nil {
		return ContainerInfo{}, err
	}

	// An entry that publishes no build for this machine still deploys: the user is
	// told plainly and the pull decides. Refusing here would block a machine whose
	// entry merely has an out-of-date platform list.
	if runs, known := HasBuildFor(svc.Docker, facts.Architecture); known && !runs && progress != nil {
		progress(fmt.Sprintf("%s does not publish a %s build, so it may not start on this machine.", svc.Name, ArchLabel(ArchFamily(facts.Architecture))))
	}

	if progress != nil {
		progress("Pulling " + image)
	}
	if err := pullImage(ctx, cli, image, progress); err != nil {
		return ContainerInfo{}, err
	}
	// With the image here, what it would run as PID 1 is known. An image that
	// cannot run under the hardening every deploy gets is refused now, on this
	// side of the line, not left to restart forever with empty logs. A daemon
	// that cannot describe the image it just pulled stops the deploy too: there
	// is nothing to be sure of, and the running container keeps earning.
	inspected, err := cli.ImageInspect(ctx, image)
	if err != nil {
		return ContainerInfo{}, fmt.Errorf("could not read what %s runs: %w", image, err)
	}
	if inspected.Config == nil {
		return ContainerInfo{}, fmt.Errorf("could not read what %s runs: the daemon returned no image config", image)
	}
	if bootsInitSystem(inspected.Config.Entrypoint, inspected.Config.Cmd, config.Entrypoint, config.Cmd) {
		return ContainerInfo{}, fmt.Errorf("%s runs an init system as its main process (/sbin/init, usually systemd), which needs a privileged container with the host's cgroup tree. CashPilot never grants that, so the container would exit at once and restart forever. Use the image's non-init tag (often ':lite') or the catalog image", image)
	}

	// Which network a bridge earner joins is settled on this side of the line too: if
	// the LAN fence cannot be applied, the deploy is refused and the running container
	// keeps earning. A Windows-containers daemon has no Linux bridge to fence.
	if svc.Docker.FenceGated {
		// From the fence check to the start: a reconcile that turns isolation off
		// in between would reduce the fence under a node about to start.
		p.fence.reconcileMu.Lock()
		defer p.fence.reconcileMu.Unlock()
	}
	fenced := ""
	if hostConfig.NetworkMode == "bridge" && facts.OSType == "linux" {
		fenced, err = p.fence.networkForDeploy(ctx, cli, svc.Docker.FenceGated)
		if err != nil {
			return ContainerInfo{}, err
		}
		isolate(hostConfig, fenced)
	}
	if problem := fenceGateProblem(svc, hostConfig, fenced, facts.OSType, p.fence.Status().Mode); problem != "" {
		return ContainerInfo{}, errors.New(problem)
	}
	defer p.fence.refreshOutside(context.Background(), cli)

	// From here to the end no other deploy of this name, and no recovery, runs:
	// either would take this deploy's replacement for a leftover. After the
	// reconcile lock, as recovery takes them.
	defer p.lockReplacing(name)()

	// A deploy that did not finish may have left the old container set aside
	// (setaside.go). It is settled first, so the name below is the one that counts.
	if note, err := recoverSetAside(ctx, cli, name); err != nil {
		return ContainerInfo{}, fmt.Errorf("%v. The running container was not touched", err)
	} else if note != "" && progress != nil {
		progress(note)
	}

	// Past this point the deploy is destructive: the existing container is set aside
	// until its replacement runs (setaside.go).
	// Where the running container keeps its data beats where the catalog thinks it
	// is: a replacement that mounts the spec's volume over a node whose identity
	// lives elsewhere comes back as a different node. Read it BEFORE the removal,
	// which is the only moment the fact still exists.
	custom := svc.Custom || catalog.IsCustomSlug(spec.Slug)
	// An inspect that fails for any reason but "no such container" stops here: past
	// it the ownership check would be skipped and the forced removal below would take
	// whatever holds the name.
	live, inspectErr := cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if inspectErr != nil && !cerrdefs.IsNotFound(inspectErr) {
		return ContainerInfo{}, fmt.Errorf("could not check what holds %s, so it was left as it is: %w", name, inspectErr)
	}
	aside := name + setAsideSuffix
	setAside, wasRunning := false, false
	if inspectErr == nil {
		if live.Container.Config != nil {
			if owner := otherOwner(live.Container.Config.Labels, spec.Slug, spec.Instance); owner != "" {
				return ContainerInfo{}, fmt.Errorf("%s already belongs to %s, not to copy %d of %s. It was left running; remove it or deploy under another name", name, owner, max(spec.Instance, 1), spec.Slug)
			}
		}
		// recoverSetAside settled a leftover of ours, so whatever holds the name the
		// old container is set aside under is not ours to remove.
		if holder, err := cli.ContainerInspect(ctx, aside, client.ContainerInspectOptions{}); err == nil {
			owner := "a container CashPilot did not deploy"
			if holder.Container.Config != nil && holder.Container.Config.Labels[LabelService] != "" {
				owner = holder.Container.Config.Labels[LabelService]
			}
			return ContainerInfo{}, fmt.Errorf("%s was not replaced: %s, the name it is set aside under while its replacement starts, belongs to %s. It was left running; remove or rename that container, then deploy again", name, aside, owner)
		} else if !cerrdefs.IsNotFound(err) {
			return ContainerInfo{}, fmt.Errorf("could not check %s, so %s was left as it is: %w", aside, name, err)
		}
		// A custom service mounts only its own named volume, never a host directory, so
		// it does not inherit what the container before it had mounted.
		var kept []KeptMount
		if !custom {
			mounts, kept = keepLiveMounts(mounts, svc.Docker.Volumes, env, live.Container)
			hostConfig.Mounts = mounts
		}
		for _, k := range kept {
			if progress != nil {
				progress(fmt.Sprintf("Keeping %s at %s, where its data already is (the catalog asked for %s).", k.Kept, k.Target, k.Requested))
			}
		}
		// Stop it the way Stop does, with its own grace period: a storage node gets
		// the chance to flush (Storj asks for 300 seconds for exactly that). The stop
		// also frees its published ports for the replacement. One that still runs
		// after a failed stop is not replaced: the replacement would run beside it.
		wasRunning = live.Container.State != nil && (live.Container.State.Running || live.Container.State.Restarting)
		if progress != nil {
			progress(fmt.Sprintf("Stopping %s (up to %ds to shut down cleanly)", name, stopTimeout))
		}
		if _, err := cli.ContainerStop(ctx, live.Container.ID, client.ContainerStopOptions{Timeout: &stopTimeout}); err != nil {
			// A fresh context: a cancelled deploy's stop may still have happened.
			still, ierr := cli.ContainerInspect(context.Background(), live.Container.ID, client.ContainerInspectOptions{})
			// The daemon goes on with a stop the client gave up on, for up to the grace
			// period: wait for it, so the node is not left stopped behind our back.
			if ierr == nil && ctx.Err() != nil && runsOrRestarts(still.Container) {
				still, ierr = waitStopped(cli, live.Container.ID, time.Duration(stopTimeout)*time.Second+10*time.Second)
			}
			if ierr != nil {
				// Whether it stopped is not known: one that ran is started again.
				return ContainerInfo{}, refusedReplacement{
					err:      fmt.Errorf("%s could not be stopped (%v), so it was not replaced", name, err),
					restored: p.putBack(cli, live.Container, wasRunning, svc.Docker.FenceGated, name),
				}
			}
			if still.Container.State == nil || runsOrRestarts(still.Container) {
				return ContainerInfo{}, fmt.Errorf("%s could not be stopped (%v), so it was not replaced and is still running. Try again in a minute", name, err)
			}
			if ctx.Err() != nil {
				// The deploy was cancelled and the node did stop: one that ran starts again.
				return ContainerInfo{}, refusedReplacement{
					err:      fmt.Errorf("%s was not replaced: %w", name, ctx.Err()),
					restored: p.putBack(cli, live.Container, wasRunning, svc.Docker.FenceGated, name),
				}
			}
		}
		if progress != nil {
			progress(fmt.Sprintf("Setting %s aside as %s until its replacement runs", name, aside))
		}
		if _, err := cli.ContainerRename(ctx, live.Container.ID, client.ContainerRenameOptions{NewName: aside}); err != nil {
			return ContainerInfo{}, refusedReplacement{
				err:      fmt.Errorf("%s could not be set aside: %w", name, err),
				restored: p.putBack(cli, live.Container, wasRunning, svc.Docker.FenceGated, name),
			}
		}
		setAside = true
	}
	// rollBack undoes a replacement Docker refused: the refused container goes (its
	// id, "" when the create failed) and the old one comes back.
	rollBack := func(err error, createdID string) error {
		if createdID != "" {
			// Best-effort, so a failed deploy does not orphan a managed container in
			// "created" state. A fresh context, so a cancelled deploy still cleans up.
			_, rmErr := cli.ContainerRemove(context.Background(), createdID, client.ContainerRemoveOptions{Force: true})
			if rmErr != nil && !cerrdefs.IsNotFound(rmErr) && setAside {
				return refusedReplacement{err: err, restored: fmt.Sprintf("The previous container is kept, stopped, as %s, with its data: the refused one still holds %s (%v). Remove %s, then deploy again", aside, name, rmErr, name)}
			}
		}
		if !setAside {
			return err
		}
		return refusedReplacement{err: err, restored: p.putBack(cli, live.Container, wasRunning, svc.Docker.FenceGated, name)}
	}

	if progress != nil {
		progress("Creating " + name)
	}
	created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     config,
		HostConfig: hostConfig,
		Name:       name,
	})
	if err != nil {
		// A create can fail after Docker made the container (a reply that never
		// arrived). One of ours under the name that does not run is that one.
		madeID := ""
		if setAside {
			if made, ierr := cli.ContainerInspect(context.Background(), name, client.ContainerInspectOptions{}); ierr == nil &&
				made.Container.ID != live.Container.ID && made.Container.Config != nil && made.Container.State != nil &&
				!made.Container.State.Running && !made.Container.State.Restarting &&
				made.Container.Config.Labels[LabelService] == config.Labels[LabelService] &&
				instanceOf(made.Container.Config.Labels) == instanceOf(config.Labels) {
				madeID = made.Container.ID
			}
		}
		return ContainerInfo{}, rollBack(err, madeID)
	}
	if progress != nil {
		progress("Starting " + name)
	}
	if _, err := cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return ContainerInfo{}, rollBack(err, created.ID)
	}
	if setAside {
		// The replacement runs, so the old container goes. Its named volumes stay; a
		// custom service's anonymous ones (an image's VOLUME with no data path) go,
		// since the replacement has fresh ones. One that cannot be removed only
		// stays as a leftover: the next deploy or app start removes it.
		if _, err := cli.ContainerRemove(context.Background(), live.Container.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: custom}); err != nil && !cerrdefs.IsNotFound(err) && progress != nil {
			progress(fmt.Sprintf("Could not remove %s, the container this one replaced (%v). The next deploy or app start removes it.", aside, err))
		}
	}
	if svc.Docker.FenceGated {
		// A deploy is a request to run it: a stop recorded earlier no longer holds.
		// A record that cannot be cleared only keeps a crashed node down until the
		// next Start, so it does not fail the deploy.
		_ = p.fence.markStopped(name, false)
		p.fence.startedByUser(name)
	}

	return ContainerInfo{
		Slug:        spec.Slug,
		ContainerID: created.ID,
		Name:        name,
		Image:       image,
		Status:      "running",
		Instance:    max(spec.Instance, 1),
	}, nil
}

func (p *DockerProvider) Stop(ctx context.Context, slug string, stopTimeoutSeconds int) error {
	cli, err := dockerClient()
	if err != nil {
		return err
	}
	defer cli.Close()
	name := containerName(slug)
	timeout := stopTimeoutForContainer(ctx, cli, name, stopTimeoutSeconds)
	got, inspectErr := cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	gated := inspectErr == nil && got.Container.Config != nil && p.fence.isGated(got.Container.Config.Labels)
	recorded := false // this call wrote the stop record
	if gated || inspectErr != nil {
		// CashPilot starts a fence-gated node itself, so the stop is recorded first or
		// the next check would start it again. Under the reconcile lock, so a check
		// that has already decided to start it cannot do so after this stop.
		p.fence.reconcileMu.Lock()
		defer p.fence.reconcileMu.Unlock()
		before, _ := p.fence.userStopped()
		if err := p.fence.markStopped(name, true); err != nil && gated {
			return fmt.Errorf("%s was not stopped: the stop could not be recorded (%v), and without that record CashPilot would start it again behind the LAN fence", name, err)
		}
		recorded = !before[name]
	}
	_, err = cli.ContainerStop(ctx, name, client.ContainerStopOptions{Timeout: &timeout})
	if err != nil && recorded {
		// The stop did not happen, so the record this call wrote goes: kept, it
		// would leave the node down after its next crash. Still under the lock.
		_ = p.fence.markStopped(name, false)
	}
	return err
}

func (p *DockerProvider) Start(ctx context.Context, slug string) error {
	cli, err := dockerClient()
	if err != nil {
		return err
	}
	defer cli.Close()
	name := containerName(slug)
	return p.startGuarded(ctx, cli, name, func() error {
		_, err := cli.ContainerStart(ctx, name, client.ContainerStartOptions{})
		return err
	})
}

// startGuarded runs start for name. A fence-gated container is started only behind
// the fence (gatedStartProblem), with the reconcile lock held from the check to the
// start so a reconcile cannot reduce the fence between them. Other containers take
// no lock, so their starts never wait on the fence helper.
func (p *DockerProvider) startGuarded(ctx context.Context, cli *client.Client, name string, start func() error) error {
	got, err := cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if err != nil {
		// Whether it is fence-gated cannot be known, so the start fails closed.
		return fmt.Errorf("could not check %s before starting it: %w", name, err)
	}
	if got.Container.Config == nil || !p.fence.isGated(got.Container.Config.Labels) {
		return start()
	}
	p.fence.reconcileMu.Lock()
	defer p.fence.reconcileMu.Unlock()
	if err := p.gatedStartProblem(ctx, cli, name); err != nil {
		return err
	}
	if err := start(); err != nil {
		return err
	}
	// Started by the user: a stop recorded earlier no longer holds, so CashPilot
	// brings it back after a crash or a reboot again. A record that cannot be cleared
	// only keeps a crashed node down until the next Start.
	_ = p.fence.markStopped(name, false)
	p.fence.startedByUser(name)
	return nil
}

// gatedStartProblem refuses to start a fence-gated container unless the fence is on
// and has just been applied: a stop while isolation was off must stay a stop.
// The caller holds p.fence.reconcileMu. It inspects again under the lock, and a
// container that cannot be inspected is refused.
func (p *DockerProvider) gatedStartProblem(ctx context.Context, cli *client.Client, name string) error {
	got, err := cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if err != nil {
		return fmt.Errorf("could not check %s before starting it: %w", name, err)
	}
	var labels map[string]string
	if got.Container.Config != nil {
		labels = got.Container.Config.Labels
	}
	if !p.fence.isGated(labels) {
		return nil
	}
	fenced, err := p.fence.networkForDeploy(ctx, cli, true)
	if err != nil {
		return err
	}
	if fenced != FenceNetwork {
		return fmt.Errorf("%s runs only behind the LAN fence, and LAN isolation is %s here. Set it to auto or on in Settings first", name, p.fence.Status().Mode)
	}
	if problem := onlyOnFence(got.Container); problem != "" {
		return fmt.Errorf("%s runs only behind the LAN fence, but %s. Redeploy it from the dashboard, which puts it back on the fence", name, problem)
	}
	return nil
}

// onlyOnFence is why a container is not on the fenced network alone, or "": a node
// made before the fence (host networking) or moved to another network would start
// with access the fence does not cover.
func onlyOnFence(c container.InspectResponse) string {
	if c.HostConfig == nil || string(c.HostConfig.NetworkMode) != FenceNetwork {
		mode := ""
		if c.HostConfig != nil {
			mode = string(c.HostConfig.NetworkMode)
		}
		return fmt.Sprintf("its network mode is %q", mode)
	}
	if c.NetworkSettings == nil {
		return "Docker did not say which networks it is attached to"
	}
	for name := range c.NetworkSettings.Networks {
		if name != FenceNetwork {
			return "it is also attached to " + name
		}
	}
	if _, ok := c.NetworkSettings.Networks[FenceNetwork]; !ok {
		return "it is not attached to " + FenceNetwork
	}
	return ""
}

func (p *DockerProvider) Restart(ctx context.Context, slug string, stopTimeoutSeconds int) error {
	cli, err := dockerClient()
	if err != nil {
		return err
	}
	defer cli.Close()
	name := containerName(slug)
	timeout := stopTimeoutForContainer(ctx, cli, name, stopTimeoutSeconds)
	return p.startGuarded(ctx, cli, name, func() error {
		_, err := cli.ContainerRestart(ctx, name, client.ContainerRestartOptions{Timeout: &timeout})
		return err
	})
}

// runsOrRestarts reports whether Docker says c runs or is restarting.
func runsOrRestarts(c container.InspectResponse) bool {
	return c.State != nil && (c.State.Running || c.State.Restarting)
}

// waitStopped polls container id, with a context of its own, until it no longer runs
// or limit passes, and returns the last inspect.
func waitStopped(cli *client.Client, id string, limit time.Duration) (client.ContainerInspectResult, error) {
	deadline := time.Now().Add(limit)
	for {
		now, err := cli.ContainerInspect(context.Background(), id, client.ContainerInspectOptions{})
		if err != nil || !runsOrRestarts(now.Container) || time.Now().After(deadline) {
			return now, err
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// PlanRemoval reports what removing this service would delete.
func (p *DockerProvider) PlanRemoval(ctx context.Context, slug string, critical map[string]string) (RemovalPlan, error) {
	cli, err := dockerClient()
	if err != nil {
		return RemovalPlan{}, err
	}
	defer cli.Close()
	name := containerName(slug)
	mounts, err := managedContainerMounts(ctx, cli, name)
	if cerrdefs.IsNotFound(err) {
		// Only its set-aside container may be left (setaside.go).
		if left, ok, aerr := onlySetAside(ctx, cli, name, slug, 1); aerr != nil {
			return RemovalPlan{}, aerr
		} else if ok {
			return planFromMounts(slug, name+setAsideSuffix, left.Mounts, critical), nil
		}
	}
	if err != nil {
		return RemovalPlan{}, err
	}
	return planFromMounts(slug, name, mounts, critical), nil
}

// Remove removes the service's container. The data it kept survives unless opts asks
// for it to go, and the volumes the catalog marks unrecoverable need the separate yes
// in opts.AllowCritical on top of that — see RemoveOptions.
func (p *DockerProvider) Remove(ctx context.Context, slug string, opts RemoveOptions) error {
	cli, err := dockerClient()
	if err != nil {
		return err
	}
	defer cli.Close()
	name := containerName(slug)
	// Not while a deploy of the same container runs: between setting the old node
	// aside and starting its replacement the name is free, and the removal would take
	// the node set aside while the deploy brings the service back.
	defer p.lockReplacing(name)()
	mounts, err := managedContainerMounts(ctx, cli, name)
	// A roll-back that could not rename the old container back leaves only it, set
	// aside: it is the service, and goes with the same data choices.
	asideOnly := false
	if cerrdefs.IsNotFound(err) {
		left, ok, aerr := onlySetAside(ctx, cli, name, slug, 1)
		if aerr != nil {
			return aerr
		}
		if ok {
			mounts, err, asideOnly = left.Mounts, nil, true
		}
	}
	if err != nil {
		return err
	}
	plan := planFromMounts(slug, name, mounts, opts.Critical)

	// Refuse BEFORE anything is removed. Refusing afterwards would already have
	// destroyed the container this data belongs to.
	if err := RefuseCriticalLoss(slug, plan, opts); err != nil {
		return err
	}

	// Stop it cleanly first, with its own grace period. Data that is being kept has
	// to be left consistent, and a SIGKILL mid-write is how a keystore or a node
	// database ends up half written.
	timeout := stopTimeoutForContainer(ctx, cli, name, opts.StopTimeout)
	_, _ = cli.ContainerStop(ctx, name, client.ContainerStopOptions{Timeout: &timeout})

	// The container a deploy that did not finish set aside goes too: it mounts the
	// same volumes, and the next app start would give it the free name back. It goes
	// first, so a removal that fails here can be tried again: with the service's own
	// container gone, a retry would no longer find the service.
	if err := removeSetAside(ctx, cli, name, opts.DeleteData); err != nil {
		return err
	}
	// RemoveVolumes only ever deletes ANONYMOUS volumes, never the named ones below.
	// It is tied to the same choice: an anonymous volume is still the service's data,
	// and nothing can say it is not the one thing that mattered.
	if !asideOnly {
		if _, err := cli.ContainerRemove(ctx, name, client.ContainerRemoveOptions{Force: true, RemoveVolumes: opts.DeleteData}); err != nil {
			return err
		}
	}
	if !opts.DeleteData {
		return nil
	}
	for _, volume := range plan.Volumes {
		if _, err := cli.VolumeRemove(ctx, volume.Source, client.VolumeRemoveOptions{Force: true}); err != nil {
			return fmt.Errorf("container removed, but volume %s could not be deleted: %w", volume.Source, err)
		}
	}
	return nil
}

// maxLogBytes caps how much of a container's log stream Logs reads into memory.
// ContainerLogs' Tail bounds the NUMBER of lines but not their length, so a
// container emitting one enormous line with no newline would otherwise be pulled
// in full by io.ReadAll. 8 MiB mirrors the collectors' response cap and is far more
// than a Tail of normal logs needs; the read is simply truncated (not an error) and
// stripDockerLogHeaders tolerates a partial trailing frame.
const maxLogBytes = 8 << 20

func (p *DockerProvider) Logs(ctx context.Context, slug string, lines int) (string, error) {
	return containerLogs(ctx, containerName(slug), lines)
}

// ProxiedLogs is Logs for the earner of slug's proxied copy through proxyID.
func (p *DockerProvider) ProxiedLogs(ctx context.Context, slug string, proxyID int64, lines int) (string, error) {
	return containerLogs(ctx, proxiedName(slug, proxyID), lines)
}

func containerLogs(ctx context.Context, name string, lines int) (string, error) {
	cli, err := dockerClient()
	if err != nil {
		return "", err
	}
	defer cli.Close()
	if lines <= 0 {
		lines = 200
	}
	reader, err := cli.ContainerLogs(ctx, name, client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       fmt.Sprintf("%d", lines),
		Timestamps: true,
	})
	if err != nil {
		return "", err
	}
	defer reader.Close()
	// Bound the read so a single huge line (Tail caps the line COUNT, not the line
	// LENGTH) cannot load an unbounded amount into memory — mirrors the pull path's
	// maxPullLogLine hardening and the collectors' io.LimitReader response cap.
	raw, err := io.ReadAll(io.LimitReader(reader, maxLogBytes))
	if err != nil {
		return "", err
	}
	return stripDockerLogHeaders(raw), nil
}

// LogsMatching returns the newest `lines` log lines of the last `since` that match
// `match`. The window is streamed and filtered here, so megabytes of a chatty
// service's logs are never held at once.
func (p *DockerProvider) LogsMatching(ctx context.Context, slug string, since time.Duration, match *regexp.Regexp, lines int) (string, error) {
	cli, err := dockerClient()
	if err != nil {
		return "", err
	}
	defer cli.Close()
	reader, err := cli.ContainerLogs(ctx, containerName(slug), client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Since:      strconv.FormatInt(time.Now().Add(-since).Unix(), 10),
		Timestamps: true,
	})
	if err != nil {
		return "", err
	}
	defer reader.Close()
	pr, pw := io.Pipe()
	go func() {
		_, err := stdcopy.StdCopy(pw, pw, reader)
		pw.CloseWithError(err)
	}()
	defer pr.Close()
	return filterLines(pr, match, lines)
}

// maxFilterLine is how much of one log line filterLines looks at. A longer line is
// cut, not an error: one oversized record must not stop the window being judged.
const maxFilterLine = 64 << 10

// filterLines keeps the newest `lines` lines of r that match.
func filterLines(r io.Reader, match *regexp.Regexp, lines int) (string, error) {
	var kept []string
	reader := bufio.NewReaderSize(r, maxFilterLine)
	var line []byte
	for {
		chunk, more, err := reader.ReadLine()
		if room := maxFilterLine - len(line); room > 0 {
			line = append(line, chunk[:min(len(chunk), room)]...)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return strings.Join(kept, "\n"), err
		}
		if more {
			continue
		}
		if match.Match(line) {
			kept = append(kept, string(line))
			if len(kept) > lines {
				kept = kept[1:]
			}
		}
		line = line[:0]
	}
	return strings.Join(kept, "\n"), nil
}

func (p *DockerProvider) List(ctx context.Context) ([]ContainerInfo, error) {
	cli, err := dockerClient()
	if err != nil {
		return nil, err
	}
	defer cli.Close()
	list, err := cli.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: make(client.Filters).Add("label", LabelManaged+"=true"),
	})
	if err != nil {
		return nil, err
	}
	// Proxied copies and their tunnels are listed on their own (ProxiedStatus): they
	// leave from another IP and are not copies of the service, so nothing that reads
	// the first copy or counts copies may see them.
	items := list.Items[:0:0]
	for _, c := range list.Items {
		if !IsProxied(c.Labels) {
			items = append(items, c)
		}
	}
	return p.statsForContainers(ctx, cli, withoutSetAside(items)), nil
}

// statsForContainers builds the ContainerInfo list, sampling every container's CPU
// and memory concurrently. stats() waits cpuSampleInterval between its two samples,
// so a serial loop would make this O(N * interval); with one goroutine per container
// the added latency stays ~one interval regardless of N. Each goroutine owns its own
// out[i] slot, so no locking is needed. It takes a statsClient (not *client.Client)
// so the concurrency and mapping can be unit-tested with a fake, no Docker daemon.
func (p *DockerProvider) statsForContainers(ctx context.Context, cli statsClient, containers []container.Summary) []ContainerInfo {
	out := make([]ContainerInfo, len(containers))
	var wg sync.WaitGroup
	for i := range containers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			c := containers[i]
			r := p.read(ctx, cli, c.ID, c.State == "running")
			info := toContainerInfo(c, r.cpu, r.mem)
			info.CPUKnown, info.MemoryKnown = r.cpuOK, r.memOK
			info.NetRxBytes, info.NetTxBytes = r.rx, r.tx
			out[i] = info
		}(i)
	}
	wg.Wait()
	return out
}

// toContainerInfo maps a Docker container summary plus its sampled CPU% and memory
// into a ContainerInfo. Split out so the field mapping is unit-testable.
func toContainerInfo(c container.Summary, cpu, mem float64) ContainerInfo {
	name := ""
	if len(c.Names) > 0 {
		name = strings.TrimPrefix(c.Names[0], "/")
	}
	return ContainerInfo{
		Slug:        c.Labels[LabelService],
		ContainerID: c.ID,
		Name:        name,
		Image:       c.Image,
		// container.Summary.State is a defined string type (container.ContainerState)
		// in the moby API module; ContainerInfo.Status stays a plain string.
		Status:     string(c.State),
		CPUPercent: cpu,
		MemoryMB:   mem,
		Instance:   instanceOf(c.Labels),
	}
}

// cpuSampleInterval is the delay between the two container-stats samples used to
// compute a live CPU percentage. Docker's one-shot stats endpoint zeroes
// PreCPUStats, so a single sample makes cpuDelta the container's entire lifetime
// CPU time and systemDelta the entire system CPU time — a lifetime average, not
// current load. Two time-separated samples give a true "CPU% right now".
const cpuSampleInterval = 1 * time.Second

// containerSample holds the raw counters read from one ContainerStatsOneShot call.
type containerSample struct {
	cpuTotal   uint64  // CPUStats.CPUUsage.TotalUsage
	systemCPU  uint64  // CPUStats.SystemUsage
	onlineCPUs float64 // CPUStats.OnlineCPUs, falling back to len(PercpuUsage)
	memoryMB   float64 // docker-stats-style memory (Usage minus inactive_file)
	// Network counters summed over every interface, cumulative since the container
	// started. hasNet is false when the response has no networks at all, which is
	// what Docker sends for a container on the host network.
	netRx, netTx uint64
	hasNet       bool
}

// statsClient is the small subset of *client.Client that the sampling code needs.
// Narrowing it to an interface lets stats(), sampleStats() and statsForContainers()
// be unit-tested with a fake that returns canned stats, with no Docker daemon.
type statsClient interface {
	ContainerStats(ctx context.Context, containerID string, options client.ContainerStatsOptions) (client.ContainerStatsResult, error)
}

// stats returns the container's current CPU percentage and memory in MB. It reads
// two stats samples cpuSampleInterval apart and derives the percentage from the
// delta between them; a single one-shot sample would report a meaningless lifetime
// average (see cpuSampleInterval). If ctx is cancelled during the wait it returns
// 0 CPU with sample A's memory, which is still a valid reading.
func (p *DockerProvider) stats(ctx context.Context, cli statsClient, containerID string, running bool) (float64, float64) {
	r := p.read(ctx, cli, containerID, running)
	return r.cpu, r.mem
}

// statsReading is what read gathers from a container's two stats samples: the
// numbers stats returns, whether each was actually read, and the network counters
// (nil when the container has none).
type statsReading struct {
	cpu, mem     float64
	cpuOK, memOK bool
	rx, tx       *uint64
}

// read is stats plus what the usage graphs need: which numbers were really read,
// and the cumulative network counters, taken from the latest sample that has them.
func (p *DockerProvider) read(ctx context.Context, cli statsClient, containerID string, running bool) statsReading {
	if !running {
		return statsReading{}
	}
	a, ok := sampleStats(ctx, cli, containerID)
	if !ok {
		return statsReading{}
	}
	r := statsReading{mem: a.memoryMB, memOK: true}
	r.rx, r.tx = netCounters(a)
	select {
	case <-ctx.Done():
		return r
	case <-time.After(cpuSampleInterval):
	}
	b, ok := sampleStats(ctx, cli, containerID)
	if !ok {
		return r
	}
	r.cpu, r.mem = combineSamples(a, b)
	r.cpuOK = true
	if b.hasNet {
		r.rx, r.tx = netCounters(b)
	}
	return r
}

// netCounters returns a sample's network counters, nil for both when it has none.
func netCounters(s containerSample) (*uint64, *uint64) {
	if !s.hasNet {
		return nil, nil
	}
	rx, tx := s.netRx, s.netTx
	return &rx, &tx
}

// sampleStats reads one one-shot stats sample and extracts its counters via
// sampleFromResponse. ok is false if the sample cannot be read or decoded. The
// zero ContainerStatsOptions is a one-shot read (Stream false, no previous
// sample), which is what the old ContainerStatsOneShot call did.
func sampleStats(ctx context.Context, cli statsClient, containerID string) (containerSample, bool) {
	reader, err := cli.ContainerStats(ctx, containerID, client.ContainerStatsOptions{})
	if err != nil {
		return containerSample{}, false
	}
	defer reader.Body.Close()
	var stats container.StatsResponse
	if err := json.NewDecoder(reader.Body).Decode(&stats); err != nil {
		return containerSample{}, false
	}
	return sampleFromResponse(stats), true
}

// sampleFromResponse extracts the counters needed to compute CPU% and memory from an
// already-decoded stats response. It is pure (no IO), so the extraction — including
// the OnlineCPUs==0 → len(PercpuUsage) fallback and the inactive_file memory
// adjustment via memoryMB — is unit-testable without a Docker daemon.
func sampleFromResponse(stats container.StatsResponse) containerSample {
	onlineCPUs := float64(stats.CPUStats.OnlineCPUs)
	if onlineCPUs == 0 {
		onlineCPUs = float64(len(stats.CPUStats.CPUUsage.PercpuUsage))
	}
	out := containerSample{
		cpuTotal:   stats.CPUStats.CPUUsage.TotalUsage,
		systemCPU:  stats.CPUStats.SystemUsage,
		onlineCPUs: onlineCPUs,
		memoryMB:   memoryMB(stats.MemoryStats),
	}
	for _, n := range stats.Networks {
		out.hasNet = true
		out.netRx += n.RxBytes
		out.netTx += n.TxBytes
	}
	return out
}

// combineSamples derives the current CPU percentage and memory (MB) from two samples
// taken cpuSampleInterval apart: a is the earlier sample, b the later one. CPU% comes
// from the A→B delta (see cpuPercent, which applies the guards) and memory from the
// more recent sample b. It is pure so the two-sample combination is unit-testable
// without a Docker daemon.
func combineSamples(a, b containerSample) (float64, float64) {
	return cpuPercent(a.cpuTotal, b.cpuTotal, a.systemCPU, b.systemCPU, b.onlineCPUs), b.memoryMB
}

// cpuPercent computes the Docker-style CPU percentage from two samples:
//
//	(cpuDelta / systemDelta) * onlineCPUs * 100
//
// cpuDelta and systemDelta are the differences between the current (cur) and
// previous (pre) counters. Deltas are computed in float64 so that a counter which
// appears to move backwards produces a non-positive delta and trips the guard
// instead of underflowing an unsigned subtraction. It returns 0 when either delta
// is non-positive or onlineCPUs is non-positive.
//
// NOTE: passing a single one-shot sample (pre counters = 0) reproduces the original
// bug — cpuDelta becomes lifetime CPU and systemDelta lifetime system time, so the
// result is a lifetime average that does not reflect current load.
func cpuPercent(preTotal, curTotal, preSystem, curSystem uint64, onlineCPUs float64) float64 {
	cpuDelta := float64(curTotal) - float64(preTotal)
	systemDelta := float64(curSystem) - float64(preSystem)
	if systemDelta <= 0 || cpuDelta <= 0 || onlineCPUs <= 0 {
		return 0
	}
	return (cpuDelta / systemDelta) * onlineCPUs * 100
}

// memoryMB converts Docker's MemoryStats to megabytes the way `docker stats` does.
// MemoryStats.Usage includes reclaimable page cache, so inactive_file is subtracted
// when the key is present and not larger than Usage; otherwise raw Usage is used.
func memoryMB(mem container.MemoryStats) float64 {
	usage := mem.Usage
	if inactive, ok := mem.Stats["inactive_file"]; ok && inactive <= usage {
		usage -= inactive
	}
	return float64(usage) / 1024 / 1024
}

// dockerClient builds a Docker API client. With no host argument (or an empty one)
// it reads the connection from the environment via client.FromEnv — the default
// Docker context / DOCKER_HOST — exactly as before. A non-empty host is a seam for
// a future bundled-runtime provider that must talk to a specific socket/endpoint
// instead of the ambient context; no current caller passes one, so today's behavior
// is unchanged.
//
// There is no WithAPIVersionNegotiation option any more: the moby client negotiates
// the API version by default, lazily on the first versioned request, so the old
// explicit option is a deprecated no-op.
// NewDockerClient is a client for the same daemon every other call here uses.
func NewDockerClient() (*client.Client, error) { return dockerClient() }

func dockerClient(host ...string) (*client.Client, error) {
	opts := []client.Opt{client.FromEnv}
	if len(host) > 0 && host[0] != "" {
		opts = append(opts, client.WithHost(host[0]))
	}
	return client.NewClientWithOpts(opts...)
}

// maxPullLogLine caps a single progress line at 1 MiB. bufio.Scanner's default max
// token size is 64 KiB, so without this a single line longer than that would stop
// the scan early with bufio.ErrTooLong and fail the pull; 1 MiB is generous for a
// JSON progress line while still bounding memory.
const maxPullLogLine = 1 << 20

func pullImage(ctx context.Context, cli *client.Client, imageName string, progress func(string)) error {
	reader, err := cli.ImagePull(ctx, imageName, client.ImagePullOptions{})
	if err != nil {
		return err
	}
	defer reader.Close()
	return streamPullProgress(reader, progress)
}

// streamPullProgress decodes the Docker image-pull progress stream (one JSON object
// per line), forwarding status lines to progress and returning the first embedded
// error. It is split out of pullImage so it can be unit-tested against an io.Reader,
// and raises bufio.Scanner's token buffer so a long line does not truncate the pull.
func streamPullProgress(reader io.Reader, progress func(string)) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), maxPullLogLine)
	for scanner.Scan() {
		var msg struct {
			Status string `json:"status"`
			ID     string `json:"id"`
			Error  string `json:"error"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &msg); err != nil {
			continue
		}
		if msg.Error != "" {
			return errors.New(msg.Error)
		}
		if progress != nil && msg.Status != "" {
			if msg.ID != "" {
				progress(msg.ID + ": " + msg.Status)
			} else {
				progress(msg.Status)
			}
		}
	}
	return scanner.Err()
}

// containerLabels are the labels a managed container is created with. Copy 1 gets
// exactly what every container had before copies existed; copy N adds LabelInstance.
func containerLabels(slug string, instance int) map[string]string {
	labels := map[string]string{
		LabelManaged: "true",
		LabelService: slug,
		LabelVersion: "1",
		LabelRuntime: "existing-docker",
	}
	if instance > 1 {
		labels[LabelInstance] = strconv.Itoa(instance)
	}
	if catalog.IsCustomSlug(slug) {
		labels[LabelCustom] = "true"
	}
	return labels
}

// LabelCustom is "true" on a container deployed from a service the user added from
// an image reference (internal/services/custom.go) rather than from the reviewed
// catalog. The web worker uses the same label.
const LabelCustom = "cashpilot.custom"

// LabelFenceGated marks a container whose catalog entry is fence_gated. The fence
// stops it whenever isolation is not on, and Start refuses it then (lanfence.go
// stopGated), without needing the catalog to find it again.
const LabelFenceGated = "cashpilot.fence-gated"

func gatedLabels(labels map[string]string, svc catalog.Service) map[string]string {
	if svc.Docker.FenceGated {
		labels[LabelFenceGated] = "true"
	}
	return labels
}

// otherOwner reports, for the container already holding a deploy's name, whose it is
// when that is not this copy of this service: "" when it is ours to replace.
//
// The name alone is no proof. Copy 2 of "foo" and copy 1 of a service slugged
// "foo-2" are both cashpilot-foo-2, and replacing another service's container would
// stop it and start this one under its name. An unlabelled container keeps the old
// behaviour, replaced, so a name taken by hand never blocks a deploy.
func otherOwner(labels map[string]string, slug string, instance int) string {
	owner := labels[LabelService]
	if owner == "" {
		return ""
	}
	if owner != slug || instanceOf(labels) != max(instance, 1) {
		return fmt.Sprintf("%s (copy %d)", owner, instanceOf(labels))
	}
	return ""
}

func containerName(slug string) string {
	return "cashpilot-" + slug
}

// DeviceHostname returns this machine's hostname for {hostname} expansion, falling
// back to "desktop" when the OS can't report one. Exported so the app layer can offer
// the same value as the form default the deploy path will substitute.
func DeviceHostname() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "desktop"
}

func buildEnv(svc catalog.Service, overrides map[string]string) map[string]string {
	env := make(map[string]string)
	declared := make(map[string]bool, len(svc.Docker.Env))
	for _, item := range svc.Docker.Env {
		declared[item.Key] = true
		if item.Default != "" {
			env[item.Key] = item.Default
		}
	}
	// Only catalog-declared keys reach the container: an orphaned stored credential
	// (e.g. a pre-migration USER_ID/DEVICE_NAME left in the blob) must not leak into
	// the environment of a service whose schema no longer includes it.
	// The variables ui_port and udp_port_range publish from keep their default when
	// left blank or whitespace, as the deploy validation assumes: a blank UDP_PORTS would publish
	// nothing and hand the node an empty --udp.ports.
	published := map[string]bool{}
	if ui := svc.Docker.UIPort; ui != nil {
		published[ui.Env] = true
	}
	if rng := svc.Docker.UDPPortRange; rng != nil {
		published[rng.Env] = true
	}
	for key, value := range overrides {
		keepsDefault := published[key] && env[key] != "" && strings.TrimSpace(value) == ""
		if declared[key] && !keepsDefault {
			env[key] = value
		}
	}
	// Expand {hostname} on BOTH defaults and overrides to the real hostname: an unedited
	// form submits the raw "cashpilot-{hostname}" default back as an override, which the
	// old default-only substitution left as a literal container name.
	hostname := DeviceHostname()
	for key, value := range env {
		env[key] = substitute(strings.ReplaceAll(value, "{hostname}", hostname), env)
	}
	return env
}

func buildPorts(raw []string) (network.PortSet, network.PortMap, error) {
	ports := network.PortSet{}
	bindings := network.PortMap{}
	for _, mapping := range raw {
		// The schema is ports: ["host:container"], so anything else is a typo in a
		// service definition. Skipping it used to publish no port and start the
		// container anyway, which looks like a working deploy until someone tries
		// to reach the service.
		parts := strings.Split(mapping, ":")
		if len(parts) != 2 {
			return nil, nil, fmt.Errorf("invalid port mapping %q: want \"host:container\"", mapping)
		}
		host := parts[0]
		containerPort := parts[1]
		if !strings.Contains(containerPort, "/") {
			containerPort += "/tcp"
		}
		// network.Port is a parsed value type (the old nat.Port was a raw string),
		// so a malformed "host:container" entry now surfaces as an error here
		// instead of being handed to the daemon and failing the create.
		port, err := network.ParsePort(containerPort)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid port mapping %q: %w", mapping, err)
		}
		ports[port] = struct{}{}
		bindings[port] = []network.PortBinding{{HostPort: host}}
	}
	return ports, bindings, nil
}

// buildServicePorts is everything the container publishes: the static ports, the
// WebUI port (ui_port) on each address its env var lists, and the UDP range
// (udp_port_range) its env var holds, 1:1. env is what the container gets, defaults
// included. A bad address or a range that is reversed or wider than its cap is an
// error, raised before the running container is touched.
func buildServicePorts(dc catalog.DockerConfig, env map[string]string) (network.PortSet, network.PortMap, error) {
	ports, bindings, err := buildPorts(dc.Ports)
	if err != nil {
		return nil, nil, err
	}
	if ui := dc.UIPort; ui != nil {
		if ui.Port < 1 || ui.Port > 65535 {
			return nil, nil, fmt.Errorf("ui_port %d is not a port", ui.Port)
		}
		addrs, err := catalog.UIBindings(env[ui.Env])
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", ui.Env, err)
		}
		port, err := network.ParsePort(fmt.Sprintf("%d/tcp", ui.Port))
		if err != nil {
			return nil, nil, err
		}
		ports[port] = struct{}{}
		for _, addr := range addrs {
			bindings[port] = append(bindings[port], network.PortBinding{HostIP: addr, HostPort: strconv.Itoa(ui.Port)})
		}
	}
	if rng := dc.UDPPortRange; rng != nil {
		udp, err := catalog.ExpandUDPRange(env[rng.Env], rng.Cap())
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", rng.Env, err)
		}
		for _, p := range udp {
			port, err := network.ParsePort(fmt.Sprintf("%d/udp", p))
			if err != nil {
				return nil, nil, err
			}
			ports[port] = struct{}{}
			bindings[port] = []network.PortBinding{{HostPort: strconv.Itoa(p)}}
		}
	}
	return ports, bindings, nil
}

// checkRuntime refuses a service whose catalog entry names a runtime the daemon does
// not provide, before the running container is touched. runtimes nil means the
// daemon did not say, which is refused too: the create would fail after the removal.
func checkRuntime(svc catalog.Service, runtimes map[string]bool) error {
	want := strings.TrimSpace(svc.Docker.Runtime)
	if want == "" {
		return nil
	}
	if runtimes == nil {
		return fmt.Errorf("%s runs only under the %s runtime, and the container runtime did not say which runtimes it has. The running container was not touched", svc.Name, want)
	}
	if !runtimes[want] {
		have := make([]string, 0, len(runtimes))
		for name := range runtimes {
			have = append(have, name)
		}
		slices.Sort(have)
		return fmt.Errorf("%s runs only under the %s runtime, which this container runtime does not provide (it has %s). The running container was not touched", svc.Name, want, strings.Join(have, ", "))
	}
	return nil
}

// fenceGateProblem is why a fence-gated service cannot be deployed with this host
// config, or "". Such a service runs only on the fenced network: anywhere else its
// NET_ADMIN namespace sits on a bridge that reaches the LAN, this machine and the
// tailnet (web app/lan_fence.py mode_problem). fenced is the network the fence
// handed out after applying its rules ("" when it applied none), so an entry that
// names the fenced network itself does not skip the fence. mode is the fence mode
// the deploy saw.
func fenceGateProblem(svc catalog.Service, host *container.HostConfig, fenced, osType, mode string) string {
	if !svc.Docker.FenceGated || (fenced == FenceNetwork && string(host.NetworkMode) == FenceNetwork) {
		return ""
	}
	why := ""
	switch {
	case osType != "linux":
		why = "the container runtime runs Windows containers, which have no LAN fence"
	case string(host.NetworkMode) != "bridge" && string(host.NetworkMode) != FenceNetwork:
		why = fmt.Sprintf("its network mode %q is outside the fence", host.NetworkMode)
	case mode == FenceModeUnsupported:
		why = podmanUnsupported
	case mode == FenceModeLegacy:
		why = "LAN isolation is not on yet on this machine. Press \"Turn on isolation\" in the dashboard's notice, which moves the other earners behind the fence, then deploy it again"
	default:
		why = "LAN isolation is off in Settings. Set it to auto or on, then deploy it again"
	}
	return fmt.Sprintf("%s runs only behind the LAN fence: %s. The running container was not touched", svc.Name, why)
}

func buildMounts(raw []string, env map[string]string) []mount.Mount {
	mounts := make([]mount.Mount, 0, len(raw))
	for _, mapping := range raw {
		parts := strings.Split(mapping, ":")
		if len(parts) < 2 {
			continue
		}
		source := substitute(parts[0], env)
		target := parts[1]
		mode := "rw"
		if len(parts) > 2 {
			mode = parts[2]
		}
		mnt := mount.Mount{
			Type:   mount.TypeBind,
			Source: source,
			Target: target,
		}
		if isNamedVolume(source) {
			mnt.Type = mount.TypeVolume
		}
		if mode == "ro" {
			mnt.ReadOnly = true
		}
		mounts = append(mounts, mnt)
	}
	return mounts
}

// buildHostConfig assembles everything the catalog entry asks the container runtime
// for: published ports, mounts, restart policy, network mode, capabilities, host
// devices and resource limits.
//
// It is a function rather than a literal inside Deploy so it can be tested without a
// Docker daemon. That matters because the failure it guards against is invisible from
// the outside: a field the catalog declares and the runtime forgets to pass produces a
// container that starts, stays "running" and earns nothing. Mysterium's /dev/net/tun
// was exactly that — declared in the entry, parsed by the loader, never mapped.
// osType is the DAEMON's operating system, which decides whether the Linux-only parts
// of the hardening can be sent at all (see applyHardening).
func buildHostConfig(svc catalog.Service, bindings network.PortMap, mounts []mount.Mount, osType string) (*container.HostConfig, error) {
	hostConfig := &container.HostConfig{
		PortBindings:  bindings,
		Mounts:        mounts,
		RestartPolicy: restartPolicyFor(svc),
		NetworkMode:   container.NetworkMode(svc.Docker.NetworkMode),
		// The runtime the entry names wins over the daemon's default (Deploy has
		// checked the daemon has it); none leaves the daemon's default.
		Runtime: strings.TrimSpace(svc.Docker.Runtime),
	}
	applyHardening(hostConfig, svc, osType)
	// "" and "default" both name the default bridge on a Linux daemon, so normalise
	// them to bridge and the LAN fence (Linux only) catches them. A Windows-containers
	// daemon is the exception: there "default" is nat and "bridge" is a user-defined
	// network that need not exist, so leave the mode as the catalog declared it.
	if osType != "windows" && (svc.Docker.NetworkMode == "" || svc.Docker.NetworkMode == "default") {
		hostConfig.NetworkMode = "bridge"
	}
	if err := applyResourceLimits(hostConfig, svc.Docker.Resources); err != nil {
		return nil, err
	}
	devices, err := buildDevices(svc.Docker.Devices)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", svc.Name, err)
	}
	hostConfig.Resources.Devices = devices
	return hostConfig, nil
}

// restartPolicyFor is unless-stopped for every earner but a fence-gated one, which
// gets none: Docker would start it after a reboot before the LAN fence is back.
// CashPilot starts it itself behind the fence (fence_gated_start.go).
func restartPolicyFor(svc catalog.Service) container.RestartPolicy {
	if svc.Docker.FenceGated {
		return container.RestartPolicy{Name: container.RestartPolicyDisabled}
	}
	return container.RestartPolicy{Name: container.RestartPolicyUnlessStopped}
}

// allowedDevices is the hard ceiling on host devices a service may be given. A
// device is a direct line to the kernel, so widening this is a deliberate change
// here, never something a catalog entry can do on its own — the same ceiling the
// CashPilot worker enforces (app/worker_api.py `_ALLOWED_DEVICES`).
//
// /dev/net/tun is on it because Mysterium genuinely cannot carry wireguard traffic
// without it: deployed without the device the node starts, registers, appears in
// discovery and earns nothing, which is the failure this exists to close.
var allowedDevices = map[string]bool{"/dev/net/tun": true}

// AllowedDevice reports whether a host device is inside the ceiling. The compose
// export checks against this same list, so a file CashPilot writes can never map
// a device the app itself would refuse to deploy.
func AllowedDevice(host string) bool {
	return allowedDevices[host]
}

// buildDevices maps a service's declared docker.devices onto the container, and
// refuses anything outside the ceiling or any entry that names no host device.
//
// Refusing is deliberate: dropping an unknown device silently would deploy a
// container that looks healthy and cannot do the job it was deployed for, which is
// the exact shape of bug this whole path is about. A device entry may carry Docker's
// "host:container:perms" form; the host path is what is checked.
func buildDevices(declared []string) ([]container.DeviceMapping, error) {
	devices := make([]container.DeviceMapping, 0, len(declared))
	var blocked []string
	for _, raw := range declared {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		parts := strings.Split(entry, ":")
		host := strings.TrimRight(strings.TrimSpace(parts[0]), "/")
		if host == "" {
			// ":/dev/net/tun" declares a container path and no host path. Skipping it
			// deploys a container missing a device its entry asked for, which is the
			// failure this whole path exists to prevent, so it is refused the same way
			// a device outside the ceiling is. A wholly blank entry is still skipped
			// above: that is a stray list item, not a half-written mapping.
			return nil, fmt.Errorf("device %q declares no host path", entry)
		}
		if !allowedDevices[host] {
			blocked = append(blocked, host)
			continue
		}
		inContainer := host
		if len(parts) > 1 && strings.TrimSpace(parts[1]) != "" {
			inContainer = strings.TrimSpace(parts[1])
		}
		perms := "rwm"
		if len(parts) > 2 && strings.TrimSpace(parts[2]) != "" {
			perms = strings.TrimSpace(parts[2])
		}
		devices = append(devices, container.DeviceMapping{
			PathOnHost:        host,
			PathInContainer:   inContainer,
			CgroupPermissions: perms,
		})
	}
	if len(blocked) > 0 {
		return nil, fmt.Errorf("blocked devices: %s", strings.Join(blocked, ", "))
	}
	if len(devices) == 0 {
		return nil, nil
	}
	return devices, nil
}

// applyResourceLimits sets the optional memory and OOM-priority knobs from a
// service's docker.resources block onto the container HostConfig. Each limit is
// applied only when present in the YAML: an empty MemLimit/MemReservation leaves
// Docker's default (unlimited) in place, and a nil OomScoreAdj leaves the daemon
// default. Memory strings use Docker's binary units ("768m" = 768 MiB, "2g" =
// 2 GiB), matching `docker run --memory` / compose mem_limit. memory-swap is left
// unset (0) on purpose so Docker derives it from Memory rather than pinning swap.
// It returns an error for a malformed size string so a bad service definition
// fails fast at deploy instead of silently running unbounded.
func applyResourceLimits(hostConfig *container.HostConfig, res catalog.ResourceLimits) error {
	if res.MemLimit != "" {
		bytes, err := parseMemoryBytes(res.MemLimit)
		if err != nil {
			return fmt.Errorf("invalid mem_limit %q: %w", res.MemLimit, err)
		}
		hostConfig.Memory = bytes
	}
	if res.MemReservation != "" {
		bytes, err := parseMemoryBytes(res.MemReservation)
		if err != nil {
			return fmt.Errorf("invalid mem_reservation %q: %w", res.MemReservation, err)
		}
		hostConfig.MemoryReservation = bytes
	}
	if res.OomScoreAdj != nil {
		hostConfig.OomScoreAdj = *res.OomScoreAdj
	}
	if res.CPUShares != 0 {
		// 2..262144 is the kernel's cpu.shares range (MIN_SHARES..MAX_SHARES);
		// daemons vary at the edges (modern ones clamp rather than error), so a
		// value outside it never does what the catalog author meant and fails
		// fast here instead of being silently clamped at create time. Same
		// guard as the web worker's _validate_resources.
		if res.CPUShares < 2 || res.CPUShares > 262144 {
			return fmt.Errorf("invalid cpu_shares %d: must be between 2 and 262144", res.CPUShares)
		}
		hostConfig.CPUShares = res.CPUShares
	}
	return nil
}

// parseMemoryBytes converts a Docker-style memory size string into a byte count
// using binary units, matching `docker run --memory` and compose mem_limit: a bare
// number is bytes, and a k/m/g/t suffix (case-insensitive, with an optional
// trailing "b", e.g. "768m" or "2gb") multiplies by 1024, 1024^2, 1024^3 or
// 1024^4. So "768m" is 768*1024*1024 = 805306368 bytes and "2g" is 2147483648. A
// fractional mantissa ("1.5g") is allowed and truncated toward zero. It returns an
// error for an empty string, a non-positive value, an unparseable number, or a
// value that would overflow a 64-bit byte count.
//
// This is the single canonical size parser for catalog.ResourceLimits.MemLimit /
// MemReservation, shared by both the Docker path (this file's
// applyResourceLimits, which fails fast on error) and the native process path
// (resource_limits_linux.go / resource_limits_windows.go, which treat an error as
// "skip the cap" and keep the earner running best-effort).
func parseMemoryBytes(s string) (int64, error) {
	raw := strings.ToLower(strings.TrimSpace(s))
	if raw == "" {
		return 0, errors.New("empty memory value")
	}
	// An explicit trailing "b" ("768mb") is treated the same as the bare unit.
	raw = strings.TrimSuffix(raw, "b")
	if raw == "" {
		return 0, fmt.Errorf("%q has no numeric value", s)
	}
	var multiplier int64 = 1
	switch raw[len(raw)-1] {
	case 'k':
		multiplier = 1 << 10
	case 'm':
		multiplier = 1 << 20
	case 'g':
		multiplier = 1 << 30
	case 't':
		multiplier = 1 << 40
	}
	if multiplier != 1 {
		raw = raw[:len(raw)-1]
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a valid size: %w", s, err)
	}
	if value <= 0 {
		return 0, fmt.Errorf("%q must be a positive size", s)
	}
	// Guard against overflowing a 64-bit byte count before the float->int64
	// conversion, which would otherwise silently produce an unspecified/wrapped
	// result for very large mantissas or unit multipliers (e.g. "1e300t").
	bytes := value * float64(multiplier)
	if bytes > math.MaxInt64 {
		return 0, fmt.Errorf("%q overflows a 64-bit byte count", s)
	}
	return int64(bytes), nil
}

// managedContainerMounts returns everything a CashPilot-managed container has mounted,
// refusing a container this app did not create. It is the raw fact the removal plan and
// the removal itself are both built from, so the question the user answers and the
// action taken cannot describe different things.
func managedContainerMounts(ctx context.Context, cli inspectClient, name string) ([]container.MountPoint, error) {
	inspect, err := cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if err != nil {
		return nil, err
	}
	ctr := inspect.Container
	if ctr.Config == nil || ctr.Config.Labels[LabelManaged] != "true" {
		return nil, fmt.Errorf("%s is not managed by CashPilot", name)
	}
	return ctr.Mounts, nil
}

func isNamedVolume(source string) bool {
	return source != "" && !strings.HasPrefix(source, "/") && !strings.HasPrefix(source, ".") && !strings.HasPrefix(source, "~")
}

// substituteVarPattern matches a single ${VAR} placeholder. Compiled once at package
// scope (rather than per substitute call) since it never changes between calls.
var substituteVarPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// substitute expands every ${VAR} placeholder in value using env, in a single pass
// over the ORIGINAL string. This is deliberate: env is a map, so ranging over it
// iterates in random order, and repeatedly doing strings.ReplaceAll over an
// accumulating result would let a substituted value that itself contains a literal
// "${OTHER}" get re-expanded on a later iteration — a correctness bug whose outcome
// depended on map iteration order. A single regexp pass expands each placeholder
// exactly once from the original text, so a replacement value is never re-scanned.
// A placeholder naming a var not present in env is left unchanged (matches prior
// ReplaceAll behavior, which only ever touched placeholders for keys present in env).
func substitute(value string, env map[string]string) string {
	return substituteVarPattern.ReplaceAllStringFunc(value, func(match string) string {
		name := match[2 : len(match)-1] // strip "${" and "}"
		if val, ok := env[name]; ok {
			return val
		}
		return match
	})
}

// entrypointFor is the entrypoint override the catalog declares for svc, or nil to
// keep the image's own. A copy, so the container config never aliases the catalog.
// initSystems is the main process of an image built to boot like a machine.
// Almost always systemd, which needs a privileged container with the host's
// cgroup tree; under the capability drop in buildHostConfig it exits 255 at once,
// with nothing in the logs, and the restart policy starts it again forever.
// /sbin/init is BusyBox init on Alpine, which has no more business as PID 1 of an
// earner, so the path is refused without naming the implementation.
var initSystems = map[string]bool{
	"/sbin/init":               true,
	"/usr/sbin/init":           true,
	"/lib/systemd/systemd":     true,
	"/usr/lib/systemd/systemd": true,
	"systemd":                  true,
}

// bootsInitSystem reports whether a container built from the image would start
// an init system as PID 1. The catalog's entrypoint, when it declares one,
// replaces the image's; the catalog's command replaces the image's CMD but never
// its ENTRYPOINT. Only the first word of what finally runs matters.
func bootsInitSystem(imageEntrypoint, imageCmd, entrypoint, cmd []string) bool {
	var first string
	switch {
	case len(entrypoint) > 0:
		first = entrypoint[0]
	case len(imageEntrypoint) > 0:
		first = imageEntrypoint[0]
	case len(cmd) > 0:
		first = cmd[0]
	case len(imageCmd) > 0:
		first = imageCmd[0]
	}
	return initSystems[first]
}

func entrypointFor(svc catalog.Service) []string {
	if len(svc.Docker.Entrypoint) == 0 {
		return nil
	}
	return slices.Clone(svc.Docker.Entrypoint)
}

// buildCommandArgs turns a maintainer command template into the argv slice passed to
// the container as Docker Cmd (which Docker appends to the image ENTRYPOINT, so argv
// ["-email","x","-pass","y"] runs the entrypoint binary with those flags).
//
// SECURITY (fix S3, CWE-78): the template is tokenized FIRST — its token boundaries
// are trusted because a template contains only static flags and ${VAR} placeholders,
// never user data — and each resulting token is substituted individually. A ${VAR}
// therefore expands into exactly ONE argv element even when the credential value
// contains shell metacharacters (;, $(), backticks, quotes, &, |) or whitespace. The
// argv is exec'd directly with no shell, so those characters are inert data and can
// never be re-split or interpreted. This replaces the old sh -c "<substituted>" form,
// where a credential holding shell syntax could break the deploy or inject commands
// into the container. Returns nil for a template that tokenizes to nothing (so an
// all-whitespace command leaves Cmd unset, keeping the image default, as before).
func buildCommandArgs(template string, env map[string]string) []string {
	tokens := tokenizeCommand(template)
	if len(tokens) == 0 {
		return nil
	}
	args := make([]string, len(tokens))
	for i, tok := range tokens {
		args[i] = substitute(tok, env)
	}
	return args
}

// tokenizeCommand splits a command template into argv tokens the way a POSIX shell
// word-splits, but WITHOUT any expansion: runs of unquoted whitespace separate
// tokens, while single-quoted ('...') and double-quoted ("...") groups are kept as
// one token with the surrounding quotes stripped. Adjacent quoted and unquoted
// segments concatenate into a single token (foo"a b" -> `fooa b`), and a quoted empty
// string ("") yields an empty token. There is no backslash escaping and no ${VAR}
// expansion here by design — expansion is applied per-token AFTER tokenizing (see
// buildCommandArgs), so a value's own quotes/metacharacters can never change token
// boundaries. Templates are maintainer-authored (static flags + ${VAR} placeholders),
// so an unterminated quote is not expected; if one occurs the accumulated text is
// still emitted as a final token rather than dropped.
func tokenizeCommand(s string) []string {
	var tokens []string
	var cur strings.Builder
	started := false // a token is in progress (distinguishes "" from no token)
	inSingle := false
	inDouble := false
	for _, r := range s {
		switch {
		case inSingle:
			if r == '\'' {
				inSingle = false
			} else {
				cur.WriteRune(r)
			}
		case inDouble:
			if r == '"' {
				inDouble = false
			} else {
				cur.WriteRune(r)
			}
		case r == '\'':
			inSingle = true
			started = true
		case r == '"':
			inDouble = true
			started = true
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			if started {
				tokens = append(tokens, cur.String())
				cur.Reset()
				started = false
			}
		default:
			cur.WriteRune(r)
			started = true
		}
	}
	if started {
		tokens = append(tokens, cur.String())
	}
	return tokens
}

func envSlice(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for key, value := range env {
		out = append(out, key+"="+value)
	}
	return out
}

func detectTools() map[string]string {
	tools := map[string]string{}
	for _, name := range []string{"docker", "podman", "colima", "limactl", "nerdctl"} {
		if path, err := exec.LookPath(name); err == nil {
			tools[name] = path
		}
	}
	return tools
}

func dockerContext() string {
	out, err := exec.Command("docker", "context", "show").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func friendlyRuntimeMessage(tools map[string]string) string {
	if _, ok := tools["docker"]; ok {
		return "Docker is installed, but the engine is not reachable yet. Start your runtime and try again."
	}
	return "Choose one of the supported runtimes below so CashPilot can run earning services on this machine."
}

func stripDockerLogHeaders(raw []byte) string {
	if len(raw) == 0 {
		return ""
	}
	var builder strings.Builder
	for i := 0; i < len(raw); {
		if i+8 <= len(raw) && raw[i] <= 2 {
			size := int(raw[i+4])<<24 | int(raw[i+5])<<16 | int(raw[i+6])<<8 | int(raw[i+7])
			i += 8
			if i+size <= len(raw) {
				builder.Write(raw[i : i+size])
				i += size
				continue
			}
		}
		builder.WriteByte(raw[i])
		i++
	}
	return builder.String()
}

func InstallGuides() []InstallGuide {
	osName := goruntime.GOOS
	guides := []InstallGuide{
		{
			ID:          "docker-desktop-macos",
			Name:        "Docker Desktop",
			Description: "The easiest option on macOS. Best compatibility if you already use Docker.",
			Platforms:   []string{"darwin"},
			URL:         "https://www.docker.com/products/docker-desktop/",
			Notes:       []string{"Recommended for most first-time users.", "Commercial use in larger organizations may need a paid Docker subscription."},
		},
		{
			ID:          "docker-desktop-windows",
			Name:        "Docker Desktop",
			Description: "The easiest option on Windows. Best compatibility if you already use Docker.",
			Platforms:   []string{"windows"},
			URL:         "https://www.docker.com/products/docker-desktop/",
			Notes:       []string{"Recommended for most first-time users.", "Requires WSL2 on Windows.", "Commercial use in larger organizations may need a paid Docker subscription."},
		},
		{
			ID:          "docker-engine",
			Name:        "Docker Engine",
			Description: "Native Linux container engine. Best first choice on Linux hosts.",
			Platforms:   []string{"linux"},
			URL:         "https://docs.docker.com/engine/install/",
			Commands:    linuxCommands(osName),
			Notes:       []string{"After installation, add your user to the docker group or use rootless mode.", "Restart your session before retrying CashPilot Desktop."},
		},
		{
			ID:          "colima",
			Name:        "Colima",
			Description: "Lightweight Docker-compatible runtime for macOS, built on Lima.",
			Platforms:   []string{"darwin"},
			URL:         "https://colima.run/",
			Commands:    []string{"brew install colima docker", "colima start --runtime docker"},
			Notes:       []string{"Good Docker Desktop alternative on macOS.", "CashPilot Desktop will use the Docker context Colima activates."},
		},
		{
			ID:          "lima",
			Name:        "Lima",
			Description: "Linux VM manager for macOS. Useful if you want more control over the VM layer.",
			Platforms:   []string{"darwin"},
			URL:         "https://lima-vm.io/",
			Commands:    []string{"brew install lima docker", "limactl start template://docker"},
			Notes:       []string{"More technical than Colima.", "Good stepping stone toward CashPilot's future managed VM runtime."},
		},
		{
			ID:          "podman",
			Name:        "Podman",
			Description: "Rootless-first container runtime with a Docker-compatible API for many workflows.",
			Platforms:   []string{"darwin", "windows", "linux"},
			URL:         "https://podman.io/docs/installation",
			Commands:    []string{"podman machine init --now", "podman system service --time=0"},
			Notes:       []string{"Docker API compatibility is good but not perfect.", "Some services that expect Docker-specific behavior may need testing."},
		},
	}
	filtered := make([]InstallGuide, 0, len(guides))
	for _, guide := range guides {
		if guide.Supports(osName) {
			filtered = append(filtered, guide)
		}
	}
	return filtered
}

func (g InstallGuide) Supports(osName string) bool {
	for _, platform := range g.Platforms {
		if platform == osName {
			return true
		}
	}
	return false
}

func linuxCommands(osName string) []string {
	if osName != "linux" {
		return nil
	}
	return []string{
		"Ubuntu/Debian: sudo apt-get install docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin",
		"Fedora: sudo dnf install docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin",
		"Arch: sudo pacman -S docker docker-compose",
	}
}

type ManagedRuntimePlan struct {
	Summary   string   `json:"summary"`
	Phases    []string `json:"phases"`
	Risks     []string `json:"risks"`
	Providers []string `json:"providers"`
}

func ManagedRuntimeRoadmap() ManagedRuntimePlan {
	return ManagedRuntimePlan{
		Summary:   "CashPilot will first support existing Docker-compatible runtimes, then add a managed VM appliance so untrusted passive-income containers can run away from the host OS.",
		Providers: []string{"cashpilot-vm-docker", "cashpilot-vm-containerd", "rootless-linux"},
		Phases: []string{
			"macOS: Lima-style VM using Apple Virtualization where available.",
			"Windows: WSL2 distro appliance with a private Docker-compatible daemon.",
			"Linux: rootless runtime first, rootful opt-in for services that need host networking or capabilities.",
		},
		Risks: []string{
			"VM lifecycle, networking, disk growth, image updates, and support bundles become product responsibilities.",
			"Privileged containers may still require explicit compatibility warnings.",
			"Bundling Docker Desktop itself remains out of scope; the managed runtime uses open building blocks.",
		},
	}
}
