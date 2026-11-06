package activity

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"errors"
	"io"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// ProcnetScript is app/procnet.py of the web repository, byte for byte
// (`make catalog-sync` copies it; a test compares it when ../CashPilot exists).
//
//go:embed procnet.py
var ProcnetScript string

// HelperImage is the helper's image, pinned by its multi-arch index digest.
// It is pulled once, on the first round after the owner switches Activity on.
const HelperImage = "python:3.15-alpine@sha256:20a82dbc2783112ddb91d1534603f802d222a3e63e593cd002eb2ac534e70211"

// ProbeSpec is one helper: which earner, how to reach its sockets, and what to tell the script.
type ProbeSpec struct {
	Name    string
	OwnerID string
	Kind    string // "netns" or "cgroup"
	Worker  string
	Context NetworkContext
}

// ProbeName is the helper's container name: one per app and earner, so two
// apps on one Docker host never remove each other's helper.
func ProbeName(worker, ownerID string) string {
	sum := sha256.Sum256([]byte(worker))
	o := ownerID
	if len(o) > 12 {
		o = o[:12]
	}
	return ProbePrefix + hex.EncodeToString(sum[:4]) + "-" + o
}

// ProbeConfig is the helper's container config and host config: user 65534, no
// capabilities, read-only, no new privileges, 32 processes and 48 MiB at most, a
// 64 KiB tmpfs at /data, no mounts, a json-file log. A host-network earner's helper joins the
// host network and the host cgroup namespace (to find the earner's cgroup in
// the read-only /sys/fs/cgroup); every other helper joins the earner's network
// namespace.
func ProbeConfig(spec ProbeSpec) (*container.Config, *container.HostConfig, error) {
	ctxJSON, err := jsonMarshal(spec.Context)
	if err != nil {
		return nil, nil, err
	}
	env := []string{
		"PROBE_SNAPSHOTS=" + strconv.Itoa(ProbeSnapshots),
		"PROBE_GAP=" + strconv.Itoa(ProbeGap),
		"PROBE_CTX=" + string(ctxJSON),
	}
	pids := int64(32)
	hc := &container.HostConfig{
		NetworkMode:    container.NetworkMode("container:" + spec.OwnerID),
		CapDrop:        []string{"ALL"},
		SecurityOpt:    []string{"no-new-privileges:true"},
		Privileged:     false,
		ReadonlyRootfs: true,
		Tmpfs:          map[string]string{"/data": "size=64k"},
		Resources:      container.Resources{PidsLimit: &pids, Memory: 48 << 20},
		// The output holds remote addresses. With the daemon's default driver
		// (journald, syslog, a remote one) they would outlive the helper; a
		// json-file log is deleted with it.
		LogConfig: container.LogConfig{Type: "json-file", Config: map[string]string{"max-size": "1m"}},
	}
	if spec.Kind == "cgroup" {
		env = append(env, "PROBE_MODE=cgroup", "PROBE_CONTAINER="+spec.OwnerID)
		hc.NetworkMode = "host"
		hc.CgroupnsMode = container.CgroupnsModeHost
	}
	cfg := &container.Config{
		Image:       HelperImage,
		Entrypoint:  []string{"python", "-c", ProcnetScript},
		Env:         env,
		User:        "65534:65534",
		Labels:      map[string]string{ProbeLabel: ProbeKind, WorkerLabel: spec.Worker},
		Healthcheck: &container.HealthConfig{Test: []string{"NONE"}},
	}
	return cfg, hc, nil
}

// ProbeRef is a helper found on the host.
type ProbeRef struct {
	ID     string
	Name   string
	Labels map[string]string
	State  string
	// Since is when a stopped helper finished, or was created for one that
	// never started; zero when Docker did not say.
	Since time.Time
}

// ShouldSweep says whether a sweep removes this helper: any of ours, a
// stale helper of another app, and a helper from an app that labelled no
// worker (an older release) once it has stopped. Another app's helper is
// removed once it has sat finished (exited or dead), or only created, for
// longer than ProbeTimeout. Its owner reads the log right after the helper
// exits, so a fresh one is left for it; a stale one is never mid-round, and
// its log keeps that round's addresses, so it must not outlive the app that
// started it. A created helper younger than that may be about to start.
func ShouldSweep(p ProbeRef, worker string) bool {
	if p.Labels[ProbeLabel] != ProbeKind {
		return false
	}
	owner, labelled := p.Labels[WorkerLabel]
	if labelled {
		if owner == worker {
			return true
		}
		if p.State != "exited" && p.State != "dead" && p.State != "created" {
			return false
		}
		// An age Docker did not report counts as old: a leaked log is the worse outcome.
		return p.Since.IsZero() || time.Since(p.Since) > ProbeTimeout
	}
	return p.State != "running" && p.State != "restarting"
}

// Engine is the Docker side of a round, so the round itself can be tested.
type Engine interface {
	Earners(ctx context.Context) ([]Container, error)
	Inspect(ctx context.Context, idOrName string) (Container, bool)
	Runtimes(ctx context.Context) map[string]RuntimeSpec
	Network(ctx context.Context, id string, earners map[string]bool) NetworkInfo
	Probes(ctx context.Context) ([]ProbeRef, error)
	Remove(ctx context.Context, id string) error
	EnsureImage(ctx context.Context, image string) error
	// RunProbe runs one helper and returns what it printed. It removes the
	// helper on every path; leftover is true when Docker would not.
	RunProbe(ctx context.Context, spec ProbeSpec) (out []byte, leftover bool, err error)
}

// ProbeError is a helper that produced no reading. Reason is a fixed code.
type ProbeError struct{ Reason string }

func (e *ProbeError) Error() string { return e.Reason }

// DockerEngine is Engine over a Docker (or Podman) socket.
type DockerEngine struct {
	Client *client.Client
}

func toContainer(r container.InspectResponse) Container {
	c := Container{ID: r.ID, Name: strings.TrimPrefix(r.Name, "/"), Labels: map[string]string{}}
	if r.Config != nil {
		for k, v := range r.Config.Labels {
			c.Labels[k] = v
		}
	}
	if r.HostConfig != nil {
		c.NetworkMode = string(r.HostConfig.NetworkMode)
		c.Runtime = r.HostConfig.Runtime
		for _, d := range r.HostConfig.DNS {
			c.DNS = append(c.DNS, d.String())
		}
	}
	if r.NetworkSettings != nil {
		for _, n := range r.NetworkSettings.Networks {
			if n == nil {
				continue
			}
			net := Network{ID: n.NetworkID, IPPrefixLen: n.IPPrefixLen, IPv6PrefixLen: n.GlobalIPv6PrefixLen}
			if n.Gateway.IsValid() {
				net.Gateway = n.Gateway.String()
			}
			if n.IPAddress.IsValid() {
				net.IPAddress = n.IPAddress.String()
			}
			if n.IPv6Gateway.IsValid() {
				net.IPv6Gateway = n.IPv6Gateway.String()
			}
			if n.GlobalIPv6Address.IsValid() {
				net.GlobalIPv6 = n.GlobalIPv6Address.String()
			}
			c.Networks = append(c.Networks, net)
		}
	}
	return c
}

// Earners is every running container this app manages.
func (e *DockerEngine) Earners(ctx context.Context) ([]Container, error) {
	list, err := e.Client.ContainerList(ctx, client.ContainerListOptions{
		Filters: make(client.Filters).Add("label", "cashpilot.managed=true").Add("status", "running"),
	})
	if err != nil {
		return nil, err
	}
	var out []Container
	for _, s := range list.Items {
		if c, ok := e.Inspect(ctx, s.ID); ok {
			out = append(out, c)
		}
	}
	return out, nil
}

func (e *DockerEngine) Inspect(ctx context.Context, idOrName string) (Container, bool) {
	r, err := e.Client.ContainerInspect(ctx, idOrName, client.ContainerInspectOptions{})
	if err != nil {
		return Container{}, false
	}
	return toContainer(r.Container), true
}

func (e *DockerEngine) Runtimes(ctx context.Context) map[string]RuntimeSpec {
	out := map[string]RuntimeSpec{}
	info, err := e.Client.Info(ctx, client.InfoOptions{})
	if err != nil {
		return out
	}
	for name, r := range info.Info.Runtimes {
		out[name] = RuntimeSpec{Path: r.Path, Args: r.Args}
	}
	return out
}

func (e *DockerEngine) Network(ctx context.Context, id string, earners map[string]bool) NetworkInfo {
	r, err := e.Client.NetworkInspect(ctx, id, client.NetworkInspectOptions{})
	if err != nil {
		return NetworkInfo{}
	}
	info := NetworkInfo{Driver: r.Network.Driver}
	for _, m := range r.Network.Containers {
		if !earners[m.Name] {
			continue
		}
		for _, p := range []netip.Prefix{m.IPv4Address, m.IPv6Address} {
			if p.IsValid() {
				info.EarnerIPs = append(info.EarnerIPs, p.Addr().Unmap().String())
			}
		}
	}
	return info
}

// inspectTimeout bounds the one inspect a sweep makes per finished helper.
const inspectTimeout = 5 * time.Second

func (e *DockerEngine) Probes(ctx context.Context) ([]ProbeRef, error) {
	list, err := e.Client.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: make(client.Filters).Add("label", ProbeLabel+"="+ProbeKind),
	})
	if err != nil {
		return nil, err
	}
	var out []ProbeRef
	for _, s := range list.Items {
		name := ""
		if len(s.Names) > 0 {
			name = strings.TrimPrefix(s.Names[0], "/")
		}
		ref := ProbeRef{ID: s.ID, Name: name, Labels: s.Labels, State: string(s.State)}
		switch ref.State {
		case "created":
			if s.Created > 0 {
				ref.Since = time.Unix(s.Created, 0)
			}
		case "exited", "dead":
			// The list carries no finish time. The inspect is bounded so a stalled
			// daemon cannot hold the sweep; on any failure the age stays unknown.
			ictx, cancel := context.WithTimeout(ctx, inspectTimeout)
			r, err := e.Client.ContainerInspect(ictx, s.ID, client.ContainerInspectOptions{})
			cancel()
			if err == nil && r.Container.State != nil {
				if t, err := time.Parse(time.RFC3339Nano, r.Container.State.FinishedAt); err == nil && t.Year() > 1 {
					ref.Since = t
				}
			}
		}
		out = append(out, ref)
	}
	return out, nil
}

func (e *DockerEngine) Remove(ctx context.Context, id string) error {
	_, err := e.Client.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
	if err != nil && isNotFound(err) {
		return nil
	}
	return err
}

func isNotFound(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "no such container")
}

func (e *DockerEngine) EnsureImage(ctx context.Context, image string) error {
	if _, err := e.Client.ImageInspect(ctx, image); err == nil {
		return nil
	}
	reader, err := e.Client.ImagePull(ctx, image, client.ImagePullOptions{})
	if err != nil {
		return err
	}
	defer reader.Close()
	// The pull stream reports a failure in-band: Wait reads it to the end.
	if err := reader.Wait(ctx); err != nil {
		return errors.New("image_unavailable")
	}
	if _, err := e.Client.ImageInspect(ctx, image); err != nil {
		return errors.New("image_unavailable")
	}
	return nil
}

func (e *DockerEngine) RunProbe(ctx context.Context, spec ProbeSpec) (out []byte, leftover bool, err error) {
	cfg, hc, cerr := ProbeConfig(spec)
	if cerr != nil {
		return nil, false, &ProbeError{"run_error"}
	}
	// A helper of ours left from a crashed round would hold the name.
	_ = e.Remove(ctx, spec.Name)
	created, err := e.Client.ContainerCreate(ctx, client.ContainerCreateOptions{Config: cfg, HostConfig: hc, Name: spec.Name})
	id := created.ID
	defer func() {
		target := id
		if target == "" {
			target = spec.Name
		}
		rctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if rerr := e.Remove(rctx, target); rerr != nil {
			leftover = true
		}
	}()
	if err != nil {
		return nil, false, &ProbeError{"run_error"}
	}
	if _, err := e.Client.ContainerStart(ctx, id, client.ContainerStartOptions{}); err != nil {
		return nil, false, &ProbeError{"run_error"}
	}
	wctx, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()
	wait := e.Client.ContainerWait(wctx, id, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case res := <-wait.Result:
		if res.StatusCode != 0 {
			return nil, false, &ProbeError{"exit_nonzero"}
		}
	case <-wait.Error:
		if wctx.Err() != nil {
			return nil, false, &ProbeError{"timeout"}
		}
		return nil, false, &ProbeError{"wait_error"}
	}
	logs, err := e.Client.ContainerLogs(ctx, id, client.ContainerLogsOptions{ShowStdout: true})
	if err != nil {
		return nil, false, &ProbeError{"bad_output"}
	}
	defer logs.Close()
	var stdout bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, io.Discard, io.LimitReader(logs, 1<<20)); err != nil {
		return nil, false, &ProbeError{"bad_output"}
	}
	return stdout.Bytes(), leftover, nil
}
