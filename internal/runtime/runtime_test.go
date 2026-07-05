package runtime

import (
	"context"
	"fmt"
	"math"
	"os/exec"
	goruntime "runtime"
	"strings"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
)

// TestStreamPullProgressHandlesLongLine pins the bufio.Scanner buffer raise: a
// single progress line larger than Scanner's default 64 KiB token size must not
// stop the scan early with bufio.ErrTooLong (which would fail/truncate the pull).
func TestStreamPullProgressHandlesLongLine(t *testing.T) {
	longStatus := strings.Repeat("x", 200*1024) // 200 KiB, well over the 64 KiB default
	stream := `{"status":"` + longStatus + `","id":"layer1"}` + "\n" +
		`{"status":"Pull complete","id":"layer2"}` + "\n"

	var count int
	var last string
	err := streamPullProgress(strings.NewReader(stream), func(s string) {
		count++
		last = s
	})
	if err != nil {
		t.Fatalf("streamPullProgress returned error on a >64 KiB line: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 progress callbacks (a long line must not stop the scan), got %d", count)
	}
	if last != "layer2: Pull complete" {
		t.Fatalf("final progress = %q, want %q", last, "layer2: Pull complete")
	}
}

func TestInstallGuidesCoverSupportedRuntimeChoices(t *testing.T) {
	guides := InstallGuides()
	if len(guides) == 0 {
		t.Fatal("expected install guides for current platform")
	}
	for _, guide := range guides {
		if !guide.Supports(goruntime.GOOS) {
			t.Fatalf("guide %q does not support current platform %q", guide.ID, goruntime.GOOS)
		}
	}
}

func TestManagedRuntimeRoadmapNamesDeferredProviders(t *testing.T) {
	plan := ManagedRuntimeRoadmap()
	if plan.Summary == "" {
		t.Fatal("expected roadmap summary")
	}
	if len(plan.Providers) < 2 {
		t.Fatalf("expected deferred providers, got %#v", plan.Providers)
	}
}

func TestBuildMountsSeparatesNamedVolumesFromHostPaths(t *testing.T) {
	svcEnv := map[string]string{
		"HOST_DIR": "/Users/example/cashpilot-data",
	}
	mounts := buildMounts([]string{
		"cashpilot-data:/var/lib/app",
		"${HOST_DIR}:/data:ro",
	}, svcEnv)
	if len(mounts) != 2 {
		t.Fatalf("expected 2 mounts, got %d", len(mounts))
	}
	if mounts[0].Type != mount.TypeVolume {
		t.Fatalf("expected named volume, got %s", mounts[0].Type)
	}
	if mounts[1].Type != mount.TypeBind {
		t.Fatalf("expected bind mount, got %s", mounts[1].Type)
	}
	if !mounts[1].ReadOnly {
		t.Fatal("expected read-only bind mount")
	}
}

func TestBuildEnvSubstitutesDefaultsAndOverrides(t *testing.T) {
	env := buildEnv(catalog.Service{Docker: catalog.DockerConfig{Env: []catalog.EnvVar{
		{Key: "DEVICE_ID", Default: "cashpilot-{hostname}"},
		{Key: "DEVICE_NAME", Default: "Device-${DEVICE_ID}"},
	}}}, map[string]string{"DEVICE_ID": "desktop-1"})
	if env["DEVICE_NAME"] != "Device-desktop-1" {
		t.Fatalf("expected substituted default, got %q", env["DEVICE_NAME"])
	}
}

// TestCPUPercentTwoSampleDeltaYieldsExpectedPercent pins the arithmetic of the
// two-sample fix with a known answer: cpuDelta = 1e9, systemDelta = 10e9,
// onlineCPUs = 4 -> (1e9 / 10e9) * 4 * 100 = 40%.
func TestCPUPercentTwoSampleDeltaYieldsExpectedPercent(t *testing.T) {
	got := cpuPercent(0, 1_000_000_000, 0, 10_000_000_000, 4)
	if got != 40 {
		t.Fatalf("cpuPercent = %v, want 40", got)
	}
}

// TestCPUPercentGuardsReturnZero verifies every guard clause returns 0 rather than
// a NaN, Inf, or huge underflowed value.
func TestCPUPercentGuardsReturnZero(t *testing.T) {
	cases := []struct {
		name                                     string
		preTotal, curTotal, preSystem, curSystem uint64
		onlineCPUs                               float64
	}{
		{"zero system delta", 0, 1_000_000_000, 5_000_000_000, 5_000_000_000, 4},
		{"zero cpu delta", 500_000_000, 500_000_000, 0, 10_000_000_000, 4},
		{"zero online cpus", 0, 1_000_000_000, 0, 10_000_000_000, 0},
		{"backwards cpu counter", 2_000_000_000, 1_000_000_000, 0, 10_000_000_000, 4},
		{"backwards system counter", 0, 1_000_000_000, 10_000_000_000, 5_000_000_000, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cpuPercent(tc.preTotal, tc.curTotal, tc.preSystem, tc.curSystem, tc.onlineCPUs)
			if got != 0 {
				t.Fatalf("cpuPercent = %v, want 0", got)
			}
		})
	}
}

// TestCPUPercentSingleSampleBugIsWrong locks the intent of the two-sample fix. A
// single one-shot sample has pre counters = 0, so cpuPercent computes a lifetime
// average that does NOT reflect current load. Here a container pegging one core
// over the sample window (delta 1e9 / 1e9 = 100%) has only used 5e9 ns of CPU
// across 500e9 ns of lifetime system time: the buggy single-sample call reports
// 1%, while the correct two-sample delta reports 100%.
func TestCPUPercentSingleSampleBugIsWrong(t *testing.T) {
	const onlineCPUs = 1

	// OLD behaviour: one one-shot sample, pre counters zero -> lifetime average.
	buggy := cpuPercent(0, 5_000_000_000, 0, 500_000_000_000, onlineCPUs)
	if buggy != 1 {
		t.Fatalf("single-sample lifetime average = %v, want 1", buggy)
	}

	// NEW behaviour: two samples one interval apart -> true current load.
	correct := cpuPercent(5_000_000_000, 6_000_000_000, 500_000_000_000, 501_000_000_000, onlineCPUs)
	if correct != 100 {
		t.Fatalf("two-sample current load = %v, want 100", correct)
	}

	if buggy == correct {
		t.Fatal("single-sample and two-sample results must differ; the bug has to be observable")
	}
}

// TestMemoryMBSubtractsInactiveFile checks that reclaimable page cache
// (inactive_file) is excluded, matching `docker stats`.
func TestMemoryMBSubtractsInactiveFile(t *testing.T) {
	const mib = 1024 * 1024
	got := memoryMB(container.MemoryStats{
		Usage: 200 * mib,
		Stats: map[string]uint64{"inactive_file": 50 * mib},
	})
	if got != 150 {
		t.Fatalf("memoryMB = %v, want 150", got)
	}
}

// TestMemoryMBFallsBackToUsage checks the guards: no inactive_file key, or an
// inactive_file larger than Usage, both fall back to raw Usage without underflow.
func TestMemoryMBFallsBackToUsage(t *testing.T) {
	const mib = 1024 * 1024
	if got := memoryMB(container.MemoryStats{Usage: 128 * mib}); got != 128 {
		t.Fatalf("memoryMB without inactive_file = %v, want 128", got)
	}
	if got := memoryMB(container.MemoryStats{
		Usage: 64 * mib,
		Stats: map[string]uint64{"inactive_file": 999 * mib},
	}); got != 64 {
		t.Fatalf("memoryMB with oversized inactive_file = %v, want 64", got)
	}
}

// dialTestDocker returns a Docker client pointed at a live daemon, or skips the
// test. It first tries dockerClient() (the same env/default-socket path the app
// uses) and, if that cannot be pinged, falls back to the endpoint the docker CLI's
// current context resolves to — e.g. Colima, when /var/run/docker.sock is not
// wired to the running daemon. The caller owns the returned client.
func dialTestDocker(t *testing.T) *client.Client {
	t.Helper()
	if cli, err := dockerClient(); err == nil {
		if _, perr := cli.Ping(context.Background()); perr == nil {
			return cli
		}
		cli.Close()
	}
	out, err := exec.Command("docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}").Output()
	if err != nil {
		t.Skipf("docker not available (context inspect failed): %v", err)
	}
	host := strings.TrimSpace(string(out))
	if host == "" {
		t.Skip("docker not available (empty context endpoint)")
	}
	cli, err := client.NewClientWithOpts(client.WithHost(host), client.WithAPIVersionNegotiation())
	if err != nil {
		t.Skipf("docker not available (client init failed): %v", err)
	}
	if _, err := cli.Ping(context.Background()); err != nil {
		cli.Close()
		t.Skipf("docker daemon not reachable at %s: %v", host, err)
	}
	return cli
}

// TestDockerStatsIntegrationReportsLiveCPU exercises the real two-sample stats()
// against a throwaway busy-loop container. It runs only when a Docker daemon is
// reachable and not in -short mode, so CI without Docker stays green. It never
// touches any pre-existing container (e.g. unrelated k3d-* containers on this
// host): it creates, samples, and force-removes only its own uniquely-named
// throwaway.
func TestDockerStatsIntegrationReportsLiveCPU(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping docker integration test in -short mode")
	}
	cli := dialTestDocker(t)
	defer cli.Close()

	ctx := context.Background()

	const img = "busybox:1.37.0" // pinned tag, never :latest
	if err := pullImage(ctx, cli, img, nil); err != nil {
		t.Skipf("could not pull %s: %v", img, err)
	}

	name := fmt.Sprintf("cashpilot-cputest-%d", time.Now().UnixNano())
	created, err := cli.ContainerCreate(ctx, &container.Config{
		Image: img,
		Cmd:   []string{"sh", "-c", "while true; do :; done"},
	}, &container.HostConfig{}, nil, nil, name)
	if err != nil {
		t.Fatalf("create throwaway container %s: %v", name, err)
	}
	// ALWAYS clean up our own container, even on failure. Use a fresh context so
	// removal still runs if ctx has been cancelled.
	defer func() {
		_ = cli.ContainerRemove(context.Background(), created.ID, container.RemoveOptions{Force: true})
	}()

	if err := cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		t.Fatalf("start throwaway container %s: %v", name, err)
	}

	// Let the busy loop spin up before the two samples are taken.
	time.Sleep(500 * time.Millisecond)

	// Read onlineCPUs directly to bound the assertion's ceiling.
	sample, ok := sampleStats(ctx, cli, created.ID)
	if !ok {
		t.Fatal("could not read a container stats sample")
	}
	onlineCPUs := sample.onlineCPUs
	if onlineCPUs <= 0 {
		onlineCPUs = float64(goruntime.NumCPU())
	}

	p := &DockerProvider{}
	cpu, mem := p.stats(ctx, cli, created.ID, true)
	t.Logf("live docker stats: CPU%%=%.2f memory=%.2f MB onlineCPUs=%.0f container=%s", cpu, mem, onlineCPUs, name)

	if math.IsNaN(cpu) || math.IsInf(cpu, 0) {
		t.Fatalf("CPU%% is not finite: %v", cpu)
	}
	if cpu <= 0 {
		t.Fatalf("expected CPU%% > 0 for a busy-loop container, got %v", cpu)
	}
	ceiling := onlineCPUs*100 + 50 // slack for scheduling/measurement noise
	if cpu > ceiling {
		t.Fatalf("CPU%% %v exceeds ceiling %v (onlineCPUs=%.0f)", cpu, ceiling, onlineCPUs)
	}
	if mem <= 0 {
		t.Fatalf("expected memory > 0 MB for a running container, got %v", mem)
	}
}
