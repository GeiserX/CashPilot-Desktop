package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

const gpuTestDigest = "@sha256:1111111111111111111111111111111111111111111111111111111111111111"

// fakeSysfs builds a sysfs tree: pci devices (name -> class, vendor), render nodes
// (renderDN -> vendor) and, with kfd, /sys/class/kfd/kfd. It points the reader at
// it, on a Linux daemon that shares this machine's kernel.
func fakeSysfs(t *testing.T, pci map[string][2]string, render map[string]string, kfd bool) daemonInfo {
	t.Helper()
	root := t.TempDir()
	write := func(path, value string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(root, "bus", "pci", "devices"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, cv := range pci {
		// A real PCI address holds ':', which Windows forbids in a file name. The
		// reader never parses the name, so the tree swaps it for '_' on every OS.
		name = strings.ReplaceAll(name, ":", "_")
		write(filepath.Join(root, "bus", "pci", "devices", name, "class"), cv[0])
		write(filepath.Join(root, "bus", "pci", "devices", name, "vendor"), cv[1])
	}
	for node, vendor := range render {
		write(filepath.Join(root, "class", "drm", node, "device", "vendor"), vendor)
	}
	// A card node is a display control node: never passed in, whoever it belongs to.
	write(filepath.Join(root, "class", "drm", "card0", "device", "vendor"), "0x1002")
	if kfd {
		write(filepath.Join(root, "class", "kfd", "kfd", "dev"), "511:0")
	}
	release := filepath.Join(root, "osrelease")
	write(release, "6.8.0-generic")
	oldRoot, oldRelease, oldOS, oldHost := sysfsRoot, osReleasePath, gpuHostOS, localHostname
	sysfsRoot, osReleasePath, gpuHostOS = root, release, "linux"
	localHostname = func() (string, error) { return "minerbox", nil }
	t.Cleanup(func() { sysfsRoot, osReleasePath, gpuHostOS, localHostname = oldRoot, oldRelease, oldOS, oldHost })
	return daemonInfo{OSType: "linux", Hostname: "minerbox", DaemonHost: "unix:///var/run/docker.sock"}
}

// localFakeDaemon makes the test's fake daemon, which listens on loopback tcp,
// count as this computer's own socket.
func localFakeDaemon(t *testing.T) {
	t.Helper()
	old := daemonHostLocal
	daemonHostLocal = func(string) bool { return true }
	t.Cleanup(func() { daemonHostLocal = old })
}

// watchtower's shape: three Intel render nodes (SR-IOV functions) and the ASPEED
// BMC display chip, which is not a GPU vendor CashPilot knows.
func watchtowerSysfs(t *testing.T) daemonInfo {
	return fakeSysfs(t,
		map[string][2]string{
			"0000:00:02.0": {"0x030000", "0x8086"},
			"0000:00:02.1": {"0x030000", "0x8086"},
			"0000:09:00.0": {"0x030000", "0x1a03"},
			"0000:00:1f.3": {"0x040300", "0x8086"}, // audio, not a display class
		},
		map[string]string{"renderD128": "0x8086", "renderD129": "0x8086", "renderD130": "0x8086"},
		false)
}

// writeSysfs writes one attribute into the fake tree fakeSysfs set up.
func writeSysfs(t *testing.T, rel, value string) {
	t.Helper()
	path := filepath.Join(sysfsRoot, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gpuService(vendor string, mutate ...func(*catalog.Service)) catalog.Service {
	svc := catalog.Service{
		Slug:         "gpu-miner",
		Name:         "GPU Miner",
		Requirements: catalog.Requirements{GPU: true},
		Docker: catalog.DockerConfig{
			Image:   "example/gpu-miner" + gpuTestDigest,
			Runtime: "runc",
			GPU:     &catalog.GPU{Vendor: vendor},
		},
	}
	for _, m := range mutate {
		m(&svc)
	}
	return svc
}

func TestReadHostGPUsReadsOnlyKnownVendors(t *testing.T) {
	facts := watchtowerSysfs(t)
	host := readHostGPUs(facts)
	if !host.Conclusive {
		t.Fatalf("read not conclusive: %s", host.Reason)
	}
	if len(host.Vendors) != 1 || host.Vendors["intel"] == nil {
		t.Fatalf("vendors = %v, want intel only (the ASPEED chip is not a GPU)", host.Vendors)
	}
	if got := host.Vendors["intel"]; got.PCI != 2 || !slices.Equal(got.RenderNodes, []string{"/dev/dri/renderD128", "/dev/dri/renderD129", "/dev/dri/renderD130"}) {
		t.Fatalf("intel = %+v", got)
	}
	if host.describe() != "Intel" {
		t.Fatalf("describe = %q", host.describe())
	}
}

func TestReadHostGPUsIsInconclusiveWhenItCannotSeeTheDaemonsMachine(t *testing.T) {
	cases := map[string]func(*daemonInfo){
		"not linux": func(*daemonInfo) { gpuHostOS = "darwin" },
		"wsl": func(*daemonInfo) {
			_ = os.WriteFile(osReleasePath, []byte("5.15.153.1-microsoft-standard-WSL2\n"), 0o644)
		},
		"daemon in a vm":          func(f *daemonInfo) { f.Hostname = "docker-desktop" },
		"daemon did not say":      func(f *daemonInfo) { f.Hostname = "" },
		"no hostname here":        func(*daemonInfo) { localHostname = func() (string, error) { return "", errors.New("no name") } },
		"same name, other kernel": func(f *daemonInfo) { f.KernelVersion = "6.1.0-rpi7" },
		"sysfs not readable":      func(*daemonInfo) { sysfsRoot = filepath.Join(sysfsRoot, "missing") },
		// A remote daemon can report this computer's own name and kernel release.
		"tcp endpoint, same name and kernel": func(f *daemonInfo) {
			f.DaemonHost, f.KernelVersion = "tcp://10.0.0.5:2376", "6.8.0-generic"
		},
		"ssh endpoint, same name": func(f *daemonInfo) { f.DaemonHost = "ssh://me@minerbox" },
		"pipe on another machine": func(f *daemonInfo) { f.DaemonHost = "npipe:////server/pipe/docker_engine" },
		"endpoint not said":       func(f *daemonInfo) { f.DaemonHost = "" },
		"pci class empty":         func(*daemonInfo) { writeSysfs(t, "bus/pci/devices/0000_00_1f.3/class", "") },
		"pci class missing": func(*daemonInfo) {
			_ = os.Remove(filepath.Join(sysfsRoot, "bus", "pci", "devices", "0000_00_1f.3", "class"))
		},
		"pci vendor empty":         func(*daemonInfo) { writeSysfs(t, "bus/pci/devices/0000_00_02.0/vendor", "") },
		"render node vendor empty": func(*daemonInfo) { writeSysfs(t, "class/drm/renderD129/device/vendor", "") },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			facts := watchtowerSysfs(t)
			mutate(&facts)
			host := readHostGPUs(facts)
			if host.Conclusive || len(host.Vendors) != 0 || host.Reason == "" {
				t.Fatalf("host = %+v, want inconclusive with a reason", host)
			}
			// Inconclusive never refuses: only a sure read says "no".
			if got := host.missing(catalog.GPUGrant{Vendor: "nvidia"}); got != "" {
				t.Fatalf("inconclusive read refused: %s", got)
			}
		})
	}
}

// A listing or a stat that fails for another reason than "not there" is not a sure
// "none". ENOTDIR has no form on Windows, which reports such a path as missing.
func TestReadHostGPUsIsInconclusiveWhenAListingFails(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("a path through a file reads as not-exist on Windows")
	}
	cases := map[string]string{
		"drm listing fails": "class/drm",
		"kfd stat fails":    "class/kfd",
	}
	for name, rel := range cases {
		t.Run(name, func(t *testing.T) {
			facts := watchtowerSysfs(t)
			_ = os.RemoveAll(filepath.Join(sysfsRoot, filepath.FromSlash(rel)))
			writeSysfs(t, rel, "a file where a directory belongs")
			host := readHostGPUs(facts)
			if host.Conclusive || len(host.Vendors) != 0 || !strings.Contains(host.Reason, "could not be read") {
				t.Fatalf("host = %+v, want inconclusive with the failure", host)
			}
		})
	}
}

// A machine with no GPU driver loaded has no drm class and no kfd: that read is
// complete, so it stays sure and still refuses a vendor it lacks. A render node of
// a GPU off the PCI bus has no vendor id and is not a failure either.
func TestReadHostGPUsStaysConclusiveWhenEveryReadSucceeds(t *testing.T) {
	facts := fakeSysfs(t, map[string][2]string{"0000:00:1f.3": {"0x040300", "0x8086"}}, nil, false)
	if err := os.RemoveAll(filepath.Join(sysfsRoot, "class", "drm")); err != nil {
		t.Fatal(err)
	}
	host := readHostGPUs(facts)
	if !host.Conclusive || host.describe() != "no GPU" {
		t.Fatalf("host = %+v, want a sure \"no GPU\"", host)
	}
	if got := host.missing(catalog.GPUGrant{Vendor: "nvidia"}); !strings.Contains(got, "no NVIDIA GPU") {
		t.Fatalf("missing = %q, want the refusal", got)
	}
	if err := os.MkdirAll(filepath.Join(sysfsRoot, "class", "drm", "renderD128", "device"), 0o755); err != nil {
		t.Fatal(err)
	}
	if host := readHostGPUs(facts); !host.Conclusive || len(host.Vendors) != 0 {
		t.Fatalf("a render node with no vendor id: %+v", host)
	}
}

func TestLocalDaemonHost(t *testing.T) {
	for host, want := range map[string]bool{
		"unix:///var/run/docker.sock":               true,
		"unix:///run/user/1000/podman/podman.sock":  true,
		"npipe:////./pipe/docker_engine":            true,
		"NPIPE:////./pipe/dockerDesktopLinuxEngine": true,
		"npipe:////server/pipe/docker_engine":       false,
		"tcp://127.0.0.1:2375":                      false,
		"tcp://10.0.0.5:2376":                       false,
		"ssh://me@box":                              false,
		"http://docker.example":                     false,
		"":                                          false,
	} {
		if got := localDaemonHost(host); got != want {
			t.Errorf("localDaemonHost(%q) = %v, want %v", host, got, want)
		}
	}
}

// Through the real client: the fake daemon listens on tcp and names this computer,
// so it is not read as local. NVIDIA goes ahead for Docker to decide; Intel is
// refused, since its render node cannot be read.
func TestATCPEndpointNamingThisComputerIsNotReadAsLocal(t *testing.T) {
	watchtowerSysfs(t)
	fake := &fakeDocker{infoName: "minerbox"}
	fake.start(t)
	spec := deploySpec("gpu-miner")
	spec.Service = gpuService("nvidia")
	if _, err := (&DockerProvider{}).Deploy(context.Background(), spec, nil); err != nil {
		t.Fatalf("Deploy = %v, want the NVIDIA request to go out (sysfs here is not the daemon's)", err)
	}
	if host, _ := createHostConfig(t, fake); len(host.DeviceRequests) != 1 {
		t.Fatalf("DeviceRequests = %+v", host.DeviceRequests)
	}
	spec.Service = gpuService("intel")
	if _, err := (&DockerProvider{}).Deploy(context.Background(), spec, nil); err == nil || !strings.Contains(err.Error(), "network endpoint") {
		t.Fatalf("Deploy = %v, want Intel refused on a network endpoint", err)
	}
}

func TestHostGPUDevicesIsWhatTheDeployPassesIn(t *testing.T) {
	facts := fakeSysfs(t,
		map[string][2]string{"0000:00:02.0": {"0x030000", "0x8086"}, "0000:03:00.0": {"0x030000", "0x1002"}},
		map[string]string{"renderD128": "0x8086", "renderD129": "0x1002"}, true)
	got, err := hostGPUDevices(facts, catalog.GPUGrant{Vendor: "amd", ROCm: true})
	if err != nil || !slices.Equal(got, []string{"/dev/dri/renderD129", catalog.KFDDevice}) {
		t.Fatalf("amd = %v, %v (want the AMD node, not the Intel renderD128)", got, err)
	}
	if got, err := hostGPUDevices(facts, catalog.GPUGrant{Vendor: "intel"}); err != nil || !slices.Equal(got, []string{"/dev/dri/renderD128"}) {
		t.Fatalf("intel = %v, %v", got, err)
	}
	if _, err := hostGPUDevices(facts, catalog.GPUGrant{Vendor: "nvidia"}); err == nil {
		t.Fatal("an NVIDIA grant got device paths")
	}
	facts.DaemonHost = "tcp://10.0.0.5:2376"
	if _, err := hostGPUDevices(facts, catalog.GPUGrant{Vendor: "amd"}); err == nil || !strings.Contains(err.Error(), "network endpoint") {
		t.Fatalf("remote endpoint: %v", err)
	}
	facts = watchtowerSysfs(t)
	if _, err := hostGPUDevices(facts, catalog.GPUGrant{Vendor: "amd"}); err == nil || !strings.Contains(err.Error(), "no AMD GPU") {
		t.Fatalf("missing vendor: %v", err)
	}
}

// The export's entry point asks the daemon the client reaches: the fake answers on
// tcp, so the node is refused as unknowable, and as this computer's own socket it
// is the AMD card's renderD129.
func TestHostGPUDevicesAsksTheDaemon(t *testing.T) {
	fakeSysfs(t,
		map[string][2]string{"0000:00:02.0": {"0x030000", "0x8086"}, "0000:03:00.0": {"0x030000", "0x1002"}},
		map[string]string{"renderD128": "0x8086", "renderD129": "0x1002"}, false)
	fake := &fakeDocker{infoName: "minerbox"}
	fake.start(t)
	if _, err := HostGPUDevices(context.Background(), catalog.GPUGrant{Vendor: "amd"}); err == nil || !strings.Contains(err.Error(), "network endpoint") {
		t.Fatalf("HostGPUDevices over tcp = %v, want refused", err)
	}
	localFakeDaemon(t)
	if got, err := HostGPUDevices(context.Background(), catalog.GPUGrant{Vendor: "amd"}); err != nil || !slices.Equal(got, []string{"/dev/dri/renderD129"}) {
		t.Fatalf("HostGPUDevices = %v, %v", got, err)
	}
}

func TestBuildGPUGivesNothingToAServiceWithoutTheBlock(t *testing.T) {
	facts := watchtowerSysfs(t)
	got, err := buildGPU(catalog.Service{Name: "Plain"}, "plain", 1, facts)
	if err != nil || got.NVIDIA || got.DeviceRequests != nil || got.Devices != nil {
		t.Fatalf("buildGPU = %+v, %v", got, err)
	}
}

func TestBuildGPUNvidiaRequest(t *testing.T) {
	for _, tc := range []struct {
		count any
		want  int
	}{{nil, 1}, {1, 1}, {"all", -1}} {
		facts := fakeSysfs(t, map[string][2]string{"0000:01:00.0": {"0x030000", "0x10de"}}, nil, false)
		svc := gpuService("nvidia", func(s *catalog.Service) { s.Docker.GPU.Count = tc.count })
		got, err := buildGPU(svc, svc.Slug, 1, facts)
		if err != nil {
			t.Fatalf("count %v: %v", tc.count, err)
		}
		want := []container.DeviceRequest{{Driver: "nvidia", Count: tc.want, Capabilities: [][]string{{"gpu"}}}}
		if !got.NVIDIA || len(got.Devices) != 0 || len(got.DeviceRequests) != 1 ||
			got.DeviceRequests[0].Driver != want[0].Driver || got.DeviceRequests[0].Count != want[0].Count ||
			len(got.DeviceRequests[0].Capabilities) != 1 || !slices.Equal(got.DeviceRequests[0].Capabilities[0], []string{"gpu"}) {
			t.Fatalf("count %v: got %+v, want %+v", tc.count, got, want)
		}
	}
}

func TestBuildGPUNvidiaOnDockerDesktopLeavesItToDocker(t *testing.T) {
	// Docker Desktop with WSL 2 passes NVIDIA through, and its VM is not this
	// machine's sysfs, so the request goes out and Docker decides at start.
	facts := watchtowerSysfs(t)
	facts.Hostname = "docker-desktop"
	got, err := buildGPU(gpuService("nvidia"), "gpu-miner", 1, facts)
	if err != nil || len(got.DeviceRequests) != 1 {
		t.Fatalf("buildGPU = %+v, %v", got, err)
	}
}

func TestBuildGPURefusals(t *testing.T) {
	amdIntel := func(t *testing.T, kfd bool) daemonInfo {
		return fakeSysfs(t,
			map[string][2]string{"0000:00:02.0": {"0x030000", "0x8086"}, "0000:03:00.0": {"0x030000", "0x1002"}},
			map[string]string{"renderD128": "0x8086", "renderD129": "0x1002"}, kfd)
	}
	cases := []struct {
		name    string
		svc     catalog.Service
		slug    string
		inst    int
		facts   func(*testing.T) daemonInfo
		wantErr string
	}{
		{"no nvidia on a sure read", gpuService("nvidia"), "gpu-miner", 1, watchtowerSysfs, "no NVIDIA GPU CashPilot can pass in (it sees: Intel)"},
		{"no amd on a sure read", gpuService("amd"), "gpu-miner", 1, watchtowerSysfs, "no AMD GPU"},
		{"rocm without kfd", gpuService("amd", func(s *catalog.Service) { s.Docker.GPU.ROCm = true }), "gpu-miner", 1, func(t *testing.T) daemonInfo { return amdIntel(t, false) }, "/dev/kfd"},
		{"render node past the ceiling", gpuService("amd"), "gpu-miner", 1, func(t *testing.T) daemonInfo {
			return fakeSysfs(t, map[string][2]string{"0000:03:00.0": {"0x030000", "0x1002"}}, map[string]string{"renderD140": "0x1002"}, false)
		}, "no render node"},
		{"intel in a vm", gpuService("intel"), "gpu-miner", 1, func(t *testing.T) daemonInfo {
			f := watchtowerSysfs(t)
			f.Hostname = "docker-desktop"
			return f
		}, "own Linux kernel"},
		{"custom slug", gpuService("nvidia"), "custom-mine", 1, watchtowerSysfs, "custom service"},
		{"custom service", gpuService("nvidia", func(s *catalog.Service) { s.Custom = true }), "gpu-miner", 1, watchtowerSysfs, "custom service"},
		{"second copy", gpuService("intel"), "gpu-miner", 2, watchtowerSysfs, "only one copy"},
		{"windows containers", gpuService("intel"), "gpu-miner", 1, func(t *testing.T) daemonInfo {
			f := watchtowerSysfs(t)
			f.OSType = "windows"
			return f
		}, "Windows containers"},
		{"gvisor", gpuService("intel", func(s *catalog.Service) { s.Docker.Runtime = "" }), "gpu-miner", 1, watchtowerSysfs, "docker.runtime: runc"},
		{"image not pinned", gpuService("intel", func(s *catalog.Service) { s.Docker.Image = "example/gpu-miner:1.0" }), "gpu-miner", 1, watchtowerSysfs, "pinned by digest"},
		{"bad count", gpuService("intel", func(s *catalog.Service) { s.Docker.GPU.Count = 2 }), "gpu-miner", 1, watchtowerSysfs, "count"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := buildGPU(tc.svc, tc.slug, tc.inst, tc.facts(t))
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
}

func TestBuildGPURenderNodesOfThatVendorOnly(t *testing.T) {
	facts := fakeSysfs(t,
		map[string][2]string{"0000:00:02.0": {"0x030000", "0x8086"}, "0000:03:00.0": {"0x030000", "0x1002"}, "0000:04:00.0": {"0x030000", "0x1002"}},
		map[string]string{"renderD128": "0x8086", "renderD130": "0x1002", "renderD129": "0x1002"}, true)
	paths := func(devices []container.DeviceMapping) []string {
		var out []string
		for _, d := range devices {
			if d.PathOnHost != d.PathInContainer || d.CgroupPermissions != "rwm" {
				t.Fatalf("device %+v is not mapped 1:1 rwm", d)
			}
			out = append(out, d.PathOnHost)
		}
		return out
	}
	one, err := buildGPU(gpuService("amd"), "gpu-miner", 1, facts)
	if err != nil || one.NVIDIA || len(one.DeviceRequests) != 0 || !slices.Equal(paths(one.Devices), []string{"/dev/dri/renderD129"}) {
		t.Fatalf("amd count 1 = %+v, %v (want the first AMD node, never the Intel one or card0)", one, err)
	}
	all, err := buildGPU(gpuService("amd", func(s *catalog.Service) { s.Docker.GPU.Count = "all"; s.Docker.GPU.ROCm = true }), "gpu-miner", 1, facts)
	if err != nil || !slices.Equal(paths(all.Devices), []string{"/dev/dri/renderD129", "/dev/dri/renderD130", "/dev/kfd"}) {
		t.Fatalf("amd all + rocm = %+v, %v", all, err)
	}
	intel, err := buildGPU(gpuService("intel"), "gpu-miner", 1, facts)
	if err != nil || !slices.Equal(paths(intel.Devices), []string{"/dev/dri/renderD128"}) {
		t.Fatalf("intel = %+v, %v", intel, err)
	}
	host := &container.HostConfig{Resources: container.Resources{Devices: []container.DeviceMapping{{PathOnHost: "/dev/net/tun", PathInContainer: "/dev/net/tun"}}}}
	applyGPU(host, all)
	if !slices.Equal(paths(host.Resources.Devices[1:]), []string{"/dev/dri/renderD129", "/dev/dri/renderD130", "/dev/kfd"}) || host.Resources.Devices[0].PathOnHost != "/dev/net/tun" || host.Resources.DeviceRequests != nil {
		t.Fatalf("applyGPU kept the entry's devices wrong: %+v", host.Resources)
	}
}

func TestStampGPUEnv(t *testing.T) {
	env := map[string]string{"WALLET": "x"}
	if err := stampGPUEnv(catalog.Service{Name: "Plain"}, env, false); err != nil || env[nvidiaVisible] != "void" {
		t.Fatalf("no grant: env %v, %v; want the void stamp", env, err)
	}
	env = map[string]string{"WALLET": "x"}
	if err := stampGPUEnv(catalog.Service{Name: "Miner"}, env, true); err != nil || len(env) != 1 {
		t.Fatalf("nvidia grant: env %v, %v; want no stamp, Docker sets the value", env, err)
	}
	env = map[string]string{"nvidia_visible_devices": "all", "NVIDIA_DRIVER_CAPABILITIES": "compute"}
	err := stampGPUEnv(catalog.Service{Name: "Sneaky"}, env, false)
	if err == nil || !strings.Contains(err.Error(), "NVIDIA_DRIVER_CAPABILITIES, nvidia_visible_devices") {
		t.Fatalf("NVIDIA_ keys: %v", err)
	}
}

// pinGPUHostOS makes withGPUHint and the sysfs read act as on goos, whatever OS
// runs the test: the NVIDIA hint differs on a Mac.
func pinGPUHostOS(t *testing.T, goos string) {
	t.Helper()
	old := gpuHostOS
	gpuHostOS = goos
	t.Cleanup(func() { gpuHostOS = old })
}

func TestWithGPUHint(t *testing.T) {
	pinGPUHostOS(t, "linux")
	// Docker's own messages, recorded on a daemon with no GPU (web specs/gpu.md).
	cases := map[string]string{
		`Error response from daemon: could not select device driver "nvidia" with capabilities: [[gpu]]`:                 "NVIDIA Container Toolkit",
		"Error response from daemon: failed to discover GPU vendor from CDI: no known GPU vendor found":                  "NVIDIA Container Toolkit",
		`error gathering device information while adding custom device "/dev/dri/renderD128": no such file or directory`: "render node is gone",
		`error gathering device information while adding custom device "/dev/kfd": no such file or directory`:            "ROCm driver",
	}
	for msg, want := range cases {
		got := withGPUHint(errors.New(msg))
		if !strings.Contains(got.Error(), want) || !strings.HasPrefix(got.Error(), msg) {
			t.Errorf("%q -> %q, want the message kept and %q added", msg, got, want)
		}
	}
	plain := errors.New("port is already allocated")
	if withGPUHint(plain) != plain || withGPUHint(nil) != nil {
		t.Fatal("an unrelated error changed")
	}
	if got := withGPUHint(errors.New(`error gathering device information while adding custom device "/dev/net/tun": x`)); strings.Contains(got.Error(), "ROCm") || strings.Contains(got.Error(), "render node") {
		t.Fatalf("a non-GPU device got a GPU hint: %v", got)
	}
}

func TestRenderNodeCeilingIsExactNodesOnly(t *testing.T) {
	for path, want := range map[string]bool{"/dev/dri/renderD128": true, "/dev/dri/renderD135": true, "/dev/kfd": false, "/dev/dri/renderD136": false, "/dev/dri/card0": false, "/dev/dri": false, "/dev/net/tun": false} {
		if renderNodeCeiling[path] != want {
			t.Errorf("renderNodeCeiling[%q] = %v", path, !want)
		}
	}
}

// createHostConfig is the HostConfig and env of the container a deploy asked the
// fake daemon to create.
func createHostConfig(t *testing.T, fake *fakeDocker) (container.HostConfig, []string) {
	t.Helper()
	fake.mu.Lock()
	body := fake.createBody
	fake.mu.Unlock()
	var req struct {
		Env        []string
		HostConfig container.HostConfig
	}
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("create body %s: %v", body, err)
	}
	return req.HostConfig, req.Env
}

// THE RULE, through the real deploy path: a catalog GPU entry is created with its
// GPU, and a service without the block gets none and the void stamp. Both read the
// create request the daemon received.
func TestDeployCreatesAGPUEntryWithItsGPU(t *testing.T) {
	// A daemon in a VM (Docker Desktop): the request goes out, Docker decides.
	fake := &fakeDocker{infoName: "docker-desktop"}
	fake.start(t)
	spec := deploySpec("gpu-miner")
	spec.Service = gpuService("nvidia")
	if _, err := (&DockerProvider{}).Deploy(context.Background(), spec, nil); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	host, env := createHostConfig(t, fake)
	if len(host.DeviceRequests) != 1 || host.DeviceRequests[0].Driver != "nvidia" || host.DeviceRequests[0].Count != 1 || host.Runtime != "runc" {
		t.Fatalf("HostConfig.DeviceRequests = %+v (runtime %q), want one nvidia request on runc", host.DeviceRequests, host.Runtime)
	}
	if slices.ContainsFunc(env, func(e string) bool { return strings.HasPrefix(e, nvidiaVisible) }) {
		t.Fatalf("an NVIDIA grant got the void stamp: %v", env)
	}
}

func TestDeployGivesAServiceWithoutTheBlockNoGPU(t *testing.T) {
	fake := &fakeDocker{}
	fake.start(t)
	if _, err := (&DockerProvider{}).Deploy(context.Background(), deploySpec("orderly"), nil); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	host, env := createHostConfig(t, fake)
	if len(host.DeviceRequests) != 0 || len(host.Devices) != 0 {
		t.Fatalf("a service without docker.gpu got %+v / %+v", host.DeviceRequests, host.Devices)
	}
	if !slices.Contains(env, nvidiaVisible+"=void") {
		t.Fatalf("env %v lacks the void stamp", env)
	}
}

func TestDeployRefusesAGPUTheMachineLacksBeforeTouchingAnything(t *testing.T) {
	watchtowerSysfs(t)
	localFakeDaemon(t)
	fake := &fakeDocker{infoName: "minerbox"}
	fake.start(t)
	spec := deploySpec("gpu-miner")
	spec.Service = gpuService("nvidia")
	_, err := (&DockerProvider{}).Deploy(context.Background(), spec, nil)
	if err == nil || !strings.Contains(err.Error(), "no NVIDIA GPU") || !strings.Contains(err.Error(), "not touched") {
		t.Fatalf("Deploy = %v, want the missing-GPU refusal", err)
	}
	for _, call := range fake.recorded() {
		if strings.HasPrefix(call, "POST /images/create") || strings.HasPrefix(call, "POST /containers/") || strings.HasPrefix(call, "DELETE ") {
			t.Fatalf("a refused GPU deploy still reached %s (calls %v)", call, fake.recorded())
		}
	}
}

func TestDeployExplainsAMissingNvidiaToolkit(t *testing.T) {
	pinGPUHostOS(t, "linux")
	fake := &fakeDocker{infoName: "docker-desktop", startRefusal: `could not select device driver "nvidia" with capabilities: [[gpu]]`}
	fake.start(t)
	spec := deploySpec("gpu-miner")
	spec.Service = gpuService("nvidia")
	_, err := (&DockerProvider{}).Deploy(context.Background(), spec, nil)
	if err == nil || !strings.Contains(err.Error(), "nvidia-ctk runtime configure") {
		t.Fatalf("Deploy = %v, want the toolkit hint", err)
	}
	if !slices.Contains(fake.recorded(), "DELETE /containers/deadbeef") {
		t.Fatalf("the refused container was not removed: %v", fake.recorded())
	}
}

func TestDeployRefusesAnNvidiaVariableFromTheEntry(t *testing.T) {
	fake := &fakeDocker{}
	fake.start(t)
	spec := deploySpec("orderly")
	spec.Service.Docker.Env = []catalog.EnvVar{{Key: "NVIDIA_VISIBLE_DEVICES", Default: "all"}}
	_, err := (&DockerProvider{}).Deploy(context.Background(), spec, nil)
	if err == nil || !strings.Contains(err.Error(), "NVIDIA_VISIBLE_DEVICES") || slices.Contains(fake.recorded(), "POST /containers/create") {
		t.Fatalf("Deploy = %v (calls %v)", err, fake.recorded())
	}
}

// A proxied copy gets no GPU, whatever the entry says, and the void stamp.
func TestAProxiedCopyGetsNoGPUAndTheVoidStamp(t *testing.T) {
	opts, err := earnerCreate(deployRequest(), "sidecar-id", "example/earner:1.0", "linux")
	if err != nil {
		t.Fatal(err)
	}
	if opts.HostConfig.Resources.DeviceRequests != nil || !slices.Contains(opts.Config.Env, nvidiaVisible+"=void") {
		t.Fatalf("requests %v env %v", opts.HostConfig.Resources.DeviceRequests, opts.Config.Env)
	}
	req := deployRequest()
	svc := req.Service
	svc.Docker.GPU = &catalog.GPU{Vendor: "nvidia"}
	req.Service = svc
	if _, err := earnerCreate(req, "sidecar-id", "example/earner:1.0", "linux"); err == nil || !strings.Contains(err.Error(), "GPU") {
		t.Fatalf("a GPU entry through a proxy: %v", err)
	}
	req = deployRequest()
	svc = req.Service
	svc.Docker.Env = append(slices.Clone(svc.Docker.Env), catalog.EnvVar{Key: "NVIDIA_VISIBLE_DEVICES", Default: "all"})
	req.Service = svc
	if _, err := earnerCreate(req, "sidecar-id", "example/earner:1.0", "linux"); err == nil || !strings.Contains(err.Error(), "NVIDIA_VISIBLE_DEVICES") {
		t.Fatalf("an NVIDIA_ variable through a proxy: %v", err)
	}
}

func TestDriftKnowsTheGPUGrant(t *testing.T) {
	svc := gpuService("amd")
	host := &container.HostConfig{Resources: container.Resources{Devices: []container.DeviceMapping{
		{PathOnHost: "/dev/dri/renderD129", PathInContainer: "/dev/dri/renderD129"},
	}}}
	shape := shapeOf(container.InspectResponse{Config: &container.Config{Image: svc.Docker.Image}, HostConfig: host})
	if found := ShapeDrift(svc, shape, ""); len(found) != 0 {
		t.Fatalf("a render node the grant picked is drift: %q", found)
	}
	// The same node on a service without the grant is still flagged.
	plain := svc
	plain.Docker.GPU = nil
	if found := strings.Join(ShapeDrift(plain, shape, ""), " | "); !strings.Contains(found, "still has device /dev/dri/renderD129") {
		t.Fatalf("ShapeDrift = %q", found)
	}
	// A container made before the grant has neither the request nor the devices.
	bare := shapeOf(container.InspectResponse{Config: &container.Config{Image: svc.Docker.Image}, HostConfig: &container.HostConfig{}})
	if found := strings.Join(ShapeDrift(svc, bare, ""), " | "); !strings.Contains(found, "missing GPU, which the catalog now passes in") {
		t.Fatalf("ShapeDrift = %q", found)
	}
	nvidia := gpuService("nvidia")
	withRequest := shapeOf(container.InspectResponse{Config: &container.Config{Image: nvidia.Docker.Image}, HostConfig: &container.HostConfig{Resources: container.Resources{
		DeviceRequests: []container.DeviceRequest{{Driver: "nvidia", Count: 1}},
	}}})
	if !slices.Equal(withRequest.GPU, []string{"nvidia"}) || len(ShapeDrift(nvidia, withRequest, "")) != 0 {
		t.Fatalf("shape %v drift %q", withRequest.GPU, ShapeDrift(nvidia, withRequest, ""))
	}
	// The fingerprint changes when the grant changes, and not for an entry without one.
	before := ShapeFingerprint(plain)
	if ShapeFingerprint(svc) == before {
		t.Fatal("adding docker.gpu kept the fingerprint, so an Ignore would hide it")
	}
	again := svc
	again.Docker.GPU = &catalog.GPU{Vendor: "amd", Count: "all"}
	if ShapeFingerprint(again) == ShapeFingerprint(svc) {
		t.Fatal("changing the GPU count kept the fingerprint")
	}
}

// A grant the catalog removed or changed leaves GPU access a redeploy takes away,
// and an unchanged valid grant is no drift.
func TestDriftReportsGPUAccessTheCatalogNoLongerGrants(t *testing.T) {
	shape := func(svc catalog.Service, requests []string, devices ...string) ContainerShape {
		host := &container.HostConfig{}
		for _, d := range requests {
			host.DeviceRequests = append(host.DeviceRequests, container.DeviceRequest{Driver: d, Count: 1})
		}
		for _, d := range devices {
			host.Devices = append(host.Devices, container.DeviceMapping{PathOnHost: d, PathInContainer: d})
		}
		return shapeOf(container.InspectResponse{Config: &container.Config{Image: svc.Docker.Image}, HostConfig: host})
	}
	plain := gpuService("nvidia", func(s *catalog.Service) { s.Docker.GPU = nil })
	rocm := gpuService("amd", func(s *catalog.Service) { s.Docker.GPU.ROCm = true })
	amd := gpuService("amd")
	nvidia := gpuService("nvidia")
	cases := []struct {
		name string
		svc  catalog.Service
		got  ContainerShape
		want []string
	}{
		{"nvidia grant removed", plain, shape(plain, []string{"nvidia"}), []string{"still has GPU access (nvidia device request), which the catalog no longer grants"}},
		{"amd grant removed", plain, shape(plain, nil, "/dev/dri/renderD129", catalog.KFDDevice), []string{"still has device /dev/dri/renderD129, /dev/kfd, which the catalog no longer passes in"}},
		{"nvidia changed to amd", amd, shape(amd, []string{"nvidia"}), []string{"still has GPU access (nvidia device request), which the catalog no longer grants", "missing GPU, which the catalog now passes in"}},
		{"amd changed to nvidia", nvidia, shape(nvidia, nil, "/dev/dri/renderD129"), []string{"still has device /dev/dri/renderD129, which the catalog no longer passes in", "missing GPU, which the catalog now passes in"}},
		{"rocm removed", amd, shape(amd, nil, "/dev/dri/renderD129", catalog.KFDDevice), []string{"still has device /dev/kfd, which the catalog no longer passes in"}},
		{"nvidia unchanged", nvidia, shape(nvidia, []string{"nvidia"}), nil},
		{"amd rocm unchanged", rocm, shape(rocm, nil, "/dev/dri/renderD129", catalog.KFDDevice), nil},
		{"no grant, no gpu", plain, shape(plain, nil), nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ShapeDrift(tc.svc, tc.got, ""); !slices.Equal(got, tc.want) {
				t.Fatalf("ShapeDrift = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestSameKernelAcceptsAMatchingRelease(t *testing.T) {
	facts := watchtowerSysfs(t)
	facts.KernelVersion = "6.8.0-generic"
	if ok, why := sameKernel(facts); !ok {
		t.Fatalf("same name and release: %s", why)
	}
}

func TestTheNvidiaHintOnAMacSaysNoToolkitHelps(t *testing.T) {
	pinGPUHostOS(t, "darwin")
	for _, msg := range []string{
		`could not select device driver "nvidia" with capabilities: [[gpu]]`,
		"failed to discover GPU vendor from CDI: no known GPU vendor found",
	} {
		got := withGPUHint(errors.New(msg)).Error()
		if !strings.HasPrefix(got, msg) || !strings.Contains(got, "Docker on a Mac cannot pass an NVIDIA GPU") || strings.Contains(got, "nvidia-ctk") {
			t.Fatalf("hint = %s", got)
		}
	}
}

// Windows has no hint of its own: the toolkit hint carries the Docker Desktop
// with WSL 2 step.
func TestTheNvidiaHintOnWindowsNamesWSL2(t *testing.T) {
	pinGPUHostOS(t, "windows")
	got := withGPUHint(errors.New(`could not select device driver "nvidia" with capabilities: [[gpu]]`)).Error()
	if !strings.Contains(got, "NVIDIA Container Toolkit") || !strings.Contains(got, "WSL 2 backend") || strings.Contains(got, "Docker on a Mac") {
		t.Fatalf("hint = %s", got)
	}
}

// Docker can refuse at create too; that error gets the hint as the start error does.
func TestDeployExplainsAGPURefusedAtCreate(t *testing.T) {
	pinGPUHostOS(t, "linux")
	fake := &fakeDocker{infoName: "docker-desktop", createRefusal: "failed to discover GPU vendor from CDI: no known GPU vendor found"}
	fake.start(t)
	spec := deploySpec("gpu-miner")
	spec.Service = gpuService("nvidia")
	_, err := (&DockerProvider{}).Deploy(context.Background(), spec, nil)
	if err == nil || !strings.Contains(err.Error(), "NVIDIA Container Toolkit") {
		t.Fatalf("Deploy = %v, want the toolkit hint", err)
	}
}

func TestARestoreStartGetsTheGPUHint(t *testing.T) {
	pinGPUHostOS(t, "linux")
	r := newRestoreRun()
	r.startErr = errors.New(`could not select device driver "nvidia" with capabilities: [[gpu]]`)
	_, err := r.restore(t, keeper(false), map[string][]byte{"keeper": keeperArchive(t)})
	if err == nil || !strings.Contains(err.Error(), "NVIDIA Container Toolkit") {
		t.Fatalf("restore = %v, want the toolkit hint", err)
	}
}

type envRecordingSeed struct {
	*fakeSeedDaemon
	envs map[string][]string
}

func (e *envRecordingSeed) ContainerCreate(ctx context.Context, o client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	e.envs[o.Name] = o.Config.Env
	return e.fakeSeedDaemon.ContainerCreate(ctx, o)
}

// The wallet generator runs the service's own image, so it gets the stamp too.
func TestTheWalletGeneratorGetsTheVoidStamp(t *testing.T) {
	f := &envRecordingSeed{newFakeSeedDaemon(), map[string][]string{}}
	item, ok := seedService().SeedVar()
	if !ok {
		t.Fatal("the seed service has no seed file")
	}
	_, _, _ = generateWallet(context.Background(), f, "img", "n-wallet-new", *item.SeedFile)
	if env, made := f.envs["n-wallet-new"]; !made || !slices.Contains(env, nvidiaVisible+"=void") {
		t.Fatalf("generator env = %v (created %v)", env, made)
	}
}
