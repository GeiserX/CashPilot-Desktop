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
	"strings"

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
		config.Cmd = []string{"sh", "-c", substitute(svc.Docker.Command, env)}
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
	raw, err := io.ReadAll(reader)
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
	out := make([]ContainerInfo, 0, len(containers))
	for _, c := range containers {
		slug := c.Labels[LabelService]
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		cpu, mem := p.stats(ctx, cli, c.ID, c.State == "running")
		out = append(out, ContainerInfo{
			Slug:        slug,
			ContainerID: c.ID,
			Name:        name,
			Image:       c.Image,
			Status:      c.State,
			CPUPercent:  cpu,
			MemoryMB:    mem,
		})
	}
	return out, nil
}

func (p *DockerProvider) stats(ctx context.Context, cli *client.Client, containerID string, running bool) (float64, float64) {
	if !running {
		return 0, 0
	}
	reader, err := cli.ContainerStatsOneShot(ctx, containerID)
	if err != nil {
		return 0, 0
	}
	defer reader.Body.Close()
	var stats container.StatsResponse
	if err := json.NewDecoder(reader.Body).Decode(&stats); err != nil {
		return 0, 0
	}
	memoryMB := float64(stats.MemoryStats.Usage) / 1024 / 1024
	cpuDelta := float64(stats.CPUStats.CPUUsage.TotalUsage - stats.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := float64(stats.CPUStats.SystemUsage - stats.PreCPUStats.SystemUsage)
	onlineCPUs := float64(stats.CPUStats.OnlineCPUs)
	if onlineCPUs == 0 {
		onlineCPUs = float64(len(stats.CPUStats.CPUUsage.PercpuUsage))
	}
	if systemDelta <= 0 || cpuDelta <= 0 || onlineCPUs <= 0 {
		return 0, memoryMB
	}
	return (cpuDelta / systemDelta) * onlineCPUs * 100, memoryMB
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
