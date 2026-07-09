package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
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
	Stop(ctx context.Context, slug string) error
	Restart(ctx context.Context, slug string) error
	Remove(ctx context.Context, slug string) error
	Logs(ctx context.Context, slug string, lines int) (string, error)
	List(ctx context.Context) ([]ContainerInfo, error)
}

type Status struct {
	Available bool              `json:"available"`
	Kind      string            `json:"kind"`
	Message   string            `json:"message"`
	Version   string            `json:"version"`
	Context   string            `json:"context"`
	Tools     map[string]string `json:"tools"`
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
}

type ContainerInfo struct {
	Slug        string  `json:"slug"`
	ContainerID string  `json:"containerId"`
	Name        string  `json:"name"`
	Image       string  `json:"image"`
	Status      string  `json:"status"`
	CPUPercent  float64 `json:"cpuPercent"`
	MemoryMB    float64 `json:"memoryMb"`
}

type DockerProvider struct{}

func NewDockerProvider() *DockerProvider {
	return &DockerProvider{}
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

	ping, err := cli.Ping(ctx)
	if err != nil {
		return Status{
			Available: false,
			Kind:      "existing-docker",
			Message:   "A container runtime is installed, but it is not running yet. Start your runtime and try again.",
			Tools:     tools,
		}
	}
	version, _ := cli.ServerVersion(ctx)
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

	name := containerName(spec.Slug)
	_ = cli.ContainerRemove(ctx, name, container.RemoveOptions{Force: true, RemoveVolumes: false})

	if progress != nil {
		progress("Pulling " + svc.Docker.Image)
	}
	if err := pullImage(ctx, cli, svc.Docker.Image, progress); err != nil {
		return ContainerInfo{}, err
	}

	env := buildEnv(svc, spec.Env)
	ports, bindings, err := buildPorts(svc.Docker.Ports)
	if err != nil {
		return ContainerInfo{}, err
	}
	mounts := buildMounts(svc.Docker.Volumes, env)

	config := &container.Config{
		Image:        svc.Docker.Image,
		Env:          envSlice(env),
		ExposedPorts: ports,
		Labels: map[string]string{
			LabelManaged: "true",
			LabelService: spec.Slug,
			LabelVersion: "1",
			LabelRuntime: "existing-docker",
		},
		Hostname: fmt.Sprintf("cashpilot-%s", spec.Slug),
	}
	if svc.Docker.Command != "" {
		config.Cmd = buildCommandArgs(svc.Docker.Command, env)
	}

	hostConfig := &container.HostConfig{
		PortBindings: bindings,
		Mounts:       mounts,
		RestartPolicy: container.RestartPolicy{
			Name: "unless-stopped",
		},
		NetworkMode: container.NetworkMode(svc.Docker.NetworkMode),
		CapAdd:      svc.Docker.CapAdd,
		Privileged:  svc.Docker.Privileged,
	}
	if svc.Docker.NetworkMode == "" {
		hostConfig.NetworkMode = "bridge"
	}
	if err := applyResourceLimits(hostConfig, svc.Docker.Resources); err != nil {
		return ContainerInfo{}, err
	}

	if progress != nil {
		progress("Creating " + name)
	}
	created, err := cli.ContainerCreate(ctx, config, hostConfig, nil, nil, name)
	if err != nil {
		return ContainerInfo{}, err
	}
	if progress != nil {
		progress("Starting " + name)
	}
	if err := cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		// The container was created (it carries LabelManaged, holds the name, and
		// shows in List()) but never started. Best-effort remove it so a failed
		// deploy does not orphan a managed cashpilot-<slug> container in "created"
		// state. Use a fresh context so a cancelled deploy ctx still cleans up.
		_ = cli.ContainerRemove(context.Background(), created.ID, container.RemoveOptions{Force: true})
		return ContainerInfo{}, err
	}

	return ContainerInfo{
		Slug:        spec.Slug,
		ContainerID: created.ID,
		Name:        name,
		Image:       svc.Docker.Image,
		Status:      "running",
	}, nil
}

func (p *DockerProvider) Stop(ctx context.Context, slug string) error {
	cli, err := dockerClient()
	if err != nil {
		return err
	}
	defer cli.Close()
	timeout := 20
	return cli.ContainerStop(ctx, containerName(slug), container.StopOptions{Timeout: &timeout})
}

func (p *DockerProvider) Start(ctx context.Context, slug string) error {
	cli, err := dockerClient()
	if err != nil {
		return err
	}
	defer cli.Close()
	return cli.ContainerStart(ctx, containerName(slug), container.StartOptions{})
}

func (p *DockerProvider) Restart(ctx context.Context, slug string) error {
	cli, err := dockerClient()
	if err != nil {
		return err
	}
	defer cli.Close()
	timeout := 20
	return cli.ContainerRestart(ctx, containerName(slug), container.StopOptions{Timeout: &timeout})
}

func (p *DockerProvider) Remove(ctx context.Context, slug string) error {
	cli, err := dockerClient()
	if err != nil {
		return err
	}
	defer cli.Close()
	name := containerName(slug)
	volumes, err := managedContainerVolumes(ctx, cli, name)
	if err != nil {
		return err
	}
	if err := cli.ContainerRemove(ctx, name, container.RemoveOptions{Force: true, RemoveVolumes: true}); err != nil {
		return err
	}
	for _, volumeName := range volumes {
		if err := cli.VolumeRemove(ctx, volumeName, true); err != nil {
			return fmt.Errorf("container removed, but volume %s could not be deleted: %w", volumeName, err)
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
	cli, err := dockerClient()
	if err != nil {
		return "", err
	}
	defer cli.Close()
	if lines <= 0 {
		lines = 200
	}
	reader, err := cli.ContainerLogs(ctx, containerName(slug), container.LogsOptions{
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

func (p *DockerProvider) List(ctx context.Context) ([]ContainerInfo, error) {
	cli, err := dockerClient()
	if err != nil {
		return nil, err
	}
	defer cli.Close()
	containers, err := cli.ContainerList(ctx, container.ListOptions{
		All: true,
		Filters: filters.NewArgs(
			filters.Arg("label", LabelManaged+"=true"),
		),
	})
	if err != nil {
		return nil, err
	}
	return p.statsForContainers(ctx, cli, containers), nil
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
			cpu, mem := p.stats(ctx, cli, c.ID, c.State == "running")
			out[i] = toContainerInfo(c, cpu, mem)
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
		Status:      c.State,
		CPUPercent:  cpu,
		MemoryMB:    mem,
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
}

// statsClient is the small subset of *client.Client that the sampling code needs.
// Narrowing it to an interface lets stats(), sampleStats() and statsForContainers()
// be unit-tested with a fake that returns canned stats, with no Docker daemon.
type statsClient interface {
	ContainerStatsOneShot(ctx context.Context, containerID string) (container.StatsResponseReader, error)
}

// stats returns the container's current CPU percentage and memory in MB. It reads
// two stats samples cpuSampleInterval apart and derives the percentage from the
// delta between them; a single one-shot sample would report a meaningless lifetime
// average (see cpuSampleInterval). If ctx is cancelled during the wait it returns
// 0 CPU with sample A's memory, which is still a valid reading.
func (p *DockerProvider) stats(ctx context.Context, cli statsClient, containerID string, running bool) (float64, float64) {
	if !running {
		return 0, 0
	}
	a, ok := sampleStats(ctx, cli, containerID)
	if !ok {
		return 0, 0
	}
	select {
	case <-ctx.Done():
		return 0, a.memoryMB
	case <-time.After(cpuSampleInterval):
	}
	b, ok := sampleStats(ctx, cli, containerID)
	if !ok {
		return 0, a.memoryMB
	}
	return combineSamples(a, b)
}

// sampleStats reads one ContainerStatsOneShot sample and extracts its counters via
// sampleFromResponse. ok is false if the sample cannot be read or decoded.
func sampleStats(ctx context.Context, cli statsClient, containerID string) (containerSample, bool) {
	reader, err := cli.ContainerStatsOneShot(ctx, containerID)
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
	return containerSample{
		cpuTotal:   stats.CPUStats.CPUUsage.TotalUsage,
		systemCPU:  stats.CPUStats.SystemUsage,
		onlineCPUs: onlineCPUs,
		memoryMB:   memoryMB(stats.MemoryStats),
	}
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

func dockerClient() (*client.Client, error) {
	return client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
}

// maxPullLogLine caps a single progress line at 1 MiB. bufio.Scanner's default max
// token size is 64 KiB, so without this a single line longer than that would stop
// the scan early with bufio.ErrTooLong and fail the pull; 1 MiB is generous for a
// JSON progress line while still bounding memory.
const maxPullLogLine = 1 << 20

func pullImage(ctx context.Context, cli *client.Client, imageName string, progress func(string)) error {
	reader, err := cli.ImagePull(ctx, imageName, image.PullOptions{})
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

func containerName(slug string) string {
	return "cashpilot-" + slug
}

func buildEnv(svc catalog.Service, overrides map[string]string) map[string]string {
	env := make(map[string]string)
	for _, item := range svc.Docker.Env {
		if item.Default != "" {
			env[item.Key] = strings.ReplaceAll(item.Default, "{hostname}", "desktop")
		}
	}
	for key, value := range overrides {
		env[key] = value
	}
	for key, value := range env {
		env[key] = substitute(value, env)
	}
	return env
}

func buildPorts(raw []string) (nat.PortSet, nat.PortMap, error) {
	ports := nat.PortSet{}
	bindings := nat.PortMap{}
	for _, mapping := range raw {
		parts := strings.Split(mapping, ":")
		if len(parts) != 2 {
			continue
		}
		host := parts[0]
		containerPort := parts[1]
		if !strings.Contains(containerPort, "/") {
			containerPort += "/tcp"
		}
		port := nat.Port(containerPort)
		ports[port] = struct{}{}
		bindings[port] = []nat.PortBinding{{HostPort: host}}
	}
	return ports, bindings, nil
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
	return nil
}

// parseMemoryBytes converts a Docker-style memory size string into a byte count
// using binary units, matching `docker run --memory` and compose mem_limit: a bare
// number is bytes, and a k/m/g/t suffix (case-insensitive, with an optional
// trailing "b", e.g. "768m" or "2gb") multiplies by 1024, 1024^2, 1024^3 or
// 1024^4. So "768m" is 768*1024*1024 = 805306368 bytes and "2g" is 2147483648. A
// fractional mantissa ("1.5g") is allowed and truncated toward zero. It returns an
// error for an empty string, a non-positive value, or an unparseable number.
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
	return int64(value * float64(multiplier)), nil
}

func managedContainerVolumes(ctx context.Context, cli *client.Client, name string) ([]string, error) {
	inspect, err := cli.ContainerInspect(ctx, name)
	if err != nil {
		return nil, err
	}
	if inspect.Config == nil || inspect.Config.Labels[LabelManaged] != "true" {
		return nil, fmt.Errorf("%s is not managed by CashPilot", name)
	}
	volumes := make([]string, 0, len(inspect.Mounts))
	for _, mnt := range inspect.Mounts {
		if mnt.Type == mount.TypeVolume && mnt.Name != "" {
			volumes = append(volumes, mnt.Name)
		}
	}
	return volumes, nil
}

func isNamedVolume(source string) bool {
	return source != "" && !strings.HasPrefix(source, "/") && !strings.HasPrefix(source, ".") && !strings.HasPrefix(source, "~")
}

func substitute(value string, env map[string]string) string {
	out := value
	for key, val := range env {
		out = strings.ReplaceAll(out, "${"+key+"}", val)
	}
	return out
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
