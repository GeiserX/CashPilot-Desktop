package runtime

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/moby/moby/api/types/container"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// The GPU grant, as the web worker gives it (app/gpu.py):
//
//   - A catalog entry declares the GPU it needs in docker.gpu. Only this computer's
//     own catalog decides it: a deploy request (the app's form, or a joined
//     CashPilot through the server-commands API) carries no GPU field, a custom
//     service never gets one, and a proxied copy is refused one.
//   - NVIDIA goes in as a Docker device request (driver nvidia, capability gpu).
//     The NVIDIA Container Toolkit on the host does the rest.
//   - AMD and Intel go in as that vendor's DRM render nodes, inside
//     renderNodeCeiling, never a card* node (the display control nodes). ROCm also
//     gets /dev/kfd. This needs the daemon to run on this machine's own kernel:
//     Docker Desktop's VM cannot see the host's render nodes.
//   - Every container without an NVIDIA grant gets NVIDIA_VISIBLE_DEVICES=void.
//     CUDA base images set that variable to all, so on a host whose default runtime
//     is nvidia such an image would otherwise get every GPU with no grant at all.

// renderNodeCeiling is every render node a container may ever get. Exact paths,
// never /dev/dri as a prefix: that would also open card*.
var renderNodeCeiling = func() map[string]bool {
	out := map[string]bool{}
	for n := 128; n < 136; n++ {
		out[fmt.Sprintf("/dev/dri/renderD%d", n)] = true
	}
	return out
}()

// gpuVendorIDs are the PCI vendor ids as sysfs writes them.
var gpuVendorIDs = map[string]string{"0x10de": "nvidia", "0x1002": "amd", "0x8086": "intel"}

var gpuVendorNames = map[string]string{"nvidia": "NVIDIA", "amd": "AMD", "intel": "Intel"}

// nvidiaVisible is the variable the void stamp sets.
const nvidiaVisible = "NVIDIA_VISIBLE_DEVICES"

const toolkitURL = "https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html"

// nvidiaHint is what to do when Docker cannot pass an NVIDIA GPU in.
const nvidiaHint = "This computer's Docker cannot pass NVIDIA GPUs into containers: the NVIDIA Container Toolkit is missing, " +
	"or Docker was not restarted after it was installed. Install it (" + toolkitURL + "), run " +
	"`sudo nvidia-ctk runtime configure --runtime=docker && sudo systemctl restart docker` " +
	"(on Windows, update the NVIDIA driver and Docker Desktop with the WSL 2 backend), then deploy again."

// macNvidiaHint replaces nvidiaHint on a Mac, where no toolkit can help: Docker
// Desktop and Podman run containers in a VM that gets no NVIDIA GPU.
const macNvidiaHint = "Docker on a Mac cannot pass an NVIDIA GPU into a container. " +
	"Run this miner on a Linux or Windows computer with an NVIDIA GPU, for example as a CashPilot worker."

// Where the host's GPUs are read. Vars so tests can point them at a fake tree.
var (
	sysfsRoot     = "/sys"
	osReleasePath = "/proc/sys/kernel/osrelease"
	gpuHostOS     = goruntime.GOOS
	localHostname = os.Hostname
	// daemonHostLocal says whether an endpoint is this computer's own socket or
	// pipe. A var so a test's fake daemon, which listens on loopback tcp, can stand
	// in for the local socket.
	daemonHostLocal = localDaemonHost
)

// hostGPUs is what this machine's sysfs says about its GPUs.
type hostGPUs struct {
	// Conclusive is false when the read says nothing about the daemon's machine:
	// not Linux, WSL, a daemon in a VM or on another machine, or sysfs that cannot
	// be read. Then Vendors is empty and must not be read as "none".
	Conclusive bool
	Reason     string
	Vendors    map[string]*gpuVendor
	KFD        bool
}

type gpuVendor struct {
	PCI         int
	RenderNodes []string
}

func readSysfs(path string) string {
	value, _ := readSysfsValue(path)
	return value
}

// readSysfsValue reads one sysfs attribute. A value that reads as empty is an error
// too: every attribute read here always holds one.
func readSysfsValue(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	value := strings.ToLower(strings.TrimSpace(string(raw)))
	if value == "" {
		return "", fmt.Errorf("%s is empty", path)
	}
	return value, nil
}

// localDaemonHost reports whether a Docker endpoint is this computer's own: a unix
// socket, or a named pipe on this machine (npipe:////./pipe/...). A tcp or ssh
// endpoint, or a pipe on another machine, may reach a daemon elsewhere, even one
// that reports this computer's hostname and kernel. An empty endpoint says nothing.
func localDaemonHost(host string) bool {
	lower := strings.ToLower(strings.TrimSpace(host))
	if strings.HasPrefix(lower, "unix://") {
		return true
	}
	if rest, ok := strings.CutPrefix(lower, "npipe:"); ok {
		return strings.HasPrefix(strings.TrimLeft(rest, "/"), "./pipe/")
	}
	return false
}

func renderNumber(path string) int {
	_, n, _ := strings.Cut(path, "renderD")
	v, err := strconv.Atoi(n)
	if err != nil {
		return 1 << 30
	}
	return v
}

// sameKernel reports whether the daemon runs on this machine's own kernel, the only
// case where this process's sysfs describes the daemon's GPUs. The daemon names its
// host in /info; Docker Desktop's VM, a Podman machine and a remote DOCKER_HOST all
// name another one. The endpoint must be this computer's own socket or pipe too: a
// remote daemon can report the same hostname and kernel release.
func sameKernel(facts daemonInfo) (bool, string) {
	if gpuHostOS != "linux" {
		return false, fmt.Sprintf("GPU detection is not implemented for %s; this says nothing about whether one exists", gpuHostOS)
	}
	release := readSysfs(osReleasePath)
	if strings.Contains(release, "microsoft") {
		return false, "this is WSL, where a GPU is not on the PCI bus CashPilot reads"
	}
	if !daemonHostLocal(facts.DaemonHost) {
		return false, "CashPilot reaches the container runtime through a network endpoint, so it may run on another computer, whose GPUs CashPilot cannot read"
	}
	if facts.Hostname == "" {
		return false, "the container runtime did not say which computer it runs on"
	}
	host, err := localHostname()
	// The same name on another box (a homelab default such as "ubuntu") is not the
	// same kernel: the release must match too when the daemon reports it.
	otherKernel := facts.KernelVersion != "" && !strings.EqualFold(facts.KernelVersion, release)
	if err != nil || !strings.EqualFold(strings.TrimSpace(host), facts.Hostname) || otherKernel {
		return false, "the container runtime runs in a virtual machine or on another computer, whose GPUs CashPilot cannot read"
	}
	return true, ""
}

// readHostGPUs reads the daemon's GPUs from sysfs, when the daemon shares this
// machine's kernel. A display chip of another vendor (a server BMC's ASPEED) is
// ignored. A read that fails part way is inconclusive, never "no GPU": only a read
// where every step succeeded may refuse a vendor.
func readHostGPUs(facts daemonInfo) hostGPUs {
	result := hostGPUs{Vendors: map[string]*gpuVendor{}}
	if ok, why := sameKernel(facts); !ok {
		result.Reason = why
		return result
	}
	entry := func(vendor string) *gpuVendor {
		if result.Vendors[vendor] == nil {
			result.Vendors[vendor] = &gpuVendor{}
		}
		return result.Vendors[vendor]
	}
	devices, err := os.ReadDir(filepath.Join(sysfsRoot, "bus", "pci", "devices"))
	if err != nil {
		result.Reason = fmt.Sprintf("this computer's GPU devices could not be read (%v)", err)
		return result
	}
	var failed []string
	for _, dev := range devices {
		dir := filepath.Join(sysfsRoot, "bus", "pci", "devices", dev.Name())
		class, err := readSysfsValue(filepath.Join(dir, "class"))
		if err != nil {
			failed = append(failed, err.Error())
			continue
		}
		if !strings.HasPrefix(class, "0x03") {
			continue
		}
		id, err := readSysfsValue(filepath.Join(dir, "vendor"))
		if err != nil {
			failed = append(failed, err.Error())
			continue
		}
		if vendor := gpuVendorIDs[id]; vendor != "" {
			entry(vendor).PCI++
		}
	}
	// No drm class at all is a machine with no GPU driver loaded: a sure "none".
	nodes, err := os.ReadDir(filepath.Join(sysfsRoot, "class", "drm"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		failed = append(failed, err.Error())
	}
	for _, node := range nodes {
		if !strings.HasPrefix(node.Name(), "renderD") {
			continue
		}
		id, err := readSysfsValue(filepath.Join(sysfsRoot, "class", "drm", node.Name(), "device", "vendor"))
		if errors.Is(err, fs.ErrNotExist) {
			// A render node of a GPU that is not on the PCI bus (an ARM board's own
			// GPU) has no vendor id, and is none of the three vendors.
			continue
		}
		if err != nil {
			failed = append(failed, err.Error())
			continue
		}
		if vendor := gpuVendorIDs[id]; vendor != "" {
			entry(vendor).RenderNodes = append(entry(vendor).RenderNodes, "/dev/dri/"+node.Name())
		}
	}
	for _, v := range result.Vendors {
		sort.Slice(v.RenderNodes, func(i, j int) bool { return renderNumber(v.RenderNodes[i]) < renderNumber(v.RenderNodes[j]) })
	}
	_, err = os.Stat(filepath.Join(sysfsRoot, "class", "kfd", "kfd"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		failed = append(failed, err.Error())
	}
	if len(failed) > 0 {
		return hostGPUs{Vendors: map[string]*gpuVendor{}, Reason: fmt.Sprintf("part of this computer's GPU details could not be read (%s)", strings.Join(failed, "; "))}
	}
	result.KFD = err == nil
	result.Conclusive = true
	return result
}

// describe names the vendors a conclusive read found: "Intel, NVIDIA" or "no GPU".
func (h hostGPUs) describe() string {
	var names []string
	for _, vendor := range catalog.GPUVendors {
		if h.Vendors[vendor] != nil {
			names = append(names, gpuVendorNames[vendor])
		}
	}
	if len(names) == 0 {
		return "no GPU"
	}
	return strings.Join(names, ", ")
}

func (h hostGPUs) renderNodes(vendor string) []string {
	var out []string
	if v := h.Vendors[vendor]; v != nil {
		for _, n := range v.RenderNodes {
			if renderNodeCeiling[n] {
				out = append(out, n)
			}
		}
	}
	return out
}

// missing is why host cannot honour grant, or "". Only a conclusive read refuses.
func (h hostGPUs) missing(grant catalog.GPUGrant) string {
	if !h.Conclusive {
		return ""
	}
	label := gpuVendorNames[grant.Vendor]
	if h.Vendors[grant.Vendor] == nil {
		return fmt.Sprintf("This computer has no %s GPU CashPilot can pass in (it sees: %s)", label, h.describe())
	}
	if grant.Vendor != "nvidia" && len(h.renderNodes(grant.Vendor)) == 0 {
		return fmt.Sprintf("This computer's %s GPU has no render node CashPilot can pass in (/dev/dri/renderD128 to renderD135): its driver is not loaded, or the computer has more GPUs than CashPilot passes in", label)
	}
	if grant.ROCm && !h.KFD {
		return fmt.Sprintf("This computer's %s GPU has no %s: the ROCm driver (amdgpu with KFD) is not loaded", label, catalog.KFDDevice)
	}
	return ""
}

// gpuResources is what a grant adds to a container.
type gpuResources struct {
	DeviceRequests []container.DeviceRequest
	Devices        []container.DeviceMapping
	// NVIDIA is whether the container holds an NVIDIA grant, which is the one case
	// without the void stamp.
	NVIDIA bool
}

// buildGPU is the GPU svc gets on this deploy, refused before anything is touched
// when it cannot be honoured. A service with no docker.gpu gets nothing.
func buildGPU(svc catalog.Service, slug string, instance int, facts daemonInfo) (gpuResources, error) {
	if svc.Docker.GPU == nil {
		return gpuResources{}, nil
	}
	name := svc.Name
	if name == "" {
		name = slug
	}
	if svc.Custom || catalog.IsCustomSlug(slug) {
		return gpuResources{}, fmt.Errorf("%s is a custom service, and only a catalog entry gets a GPU. Nothing was deployed", name)
	}
	if problems := catalog.GPUProblems(svc); len(problems) > 0 {
		return gpuResources{}, fmt.Errorf("%s declares a GPU its catalog entry does not describe correctly (%s), so nothing was deployed", name, strings.Join(problems, "; "))
	}
	grant, _ := svc.Docker.GPU.Grant()
	if instance > 1 {
		return gpuResources{}, fmt.Errorf("%s uses this computer's GPU and can run only one copy. Nothing was deployed", name)
	}
	if facts.OSType != "linux" {
		return gpuResources{}, fmt.Errorf("%s needs a GPU in a Linux container, and this container runtime runs Windows containers. Nothing was deployed", name)
	}
	host := readHostGPUs(facts)
	if problem := host.missing(grant); problem != "" {
		return gpuResources{}, fmt.Errorf("%s. Nothing was deployed; the running container was not touched", problem)
	}
	if grant.Vendor == "nvidia" {
		count := 1
		if grant.All {
			count = -1
		}
		return gpuResources{
			DeviceRequests: []container.DeviceRequest{{Driver: "nvidia", Count: count, Capabilities: [][]string{{"gpu"}}}},
			NVIDIA:         true,
		}, nil
	}
	if !host.Conclusive {
		return gpuResources{}, fmt.Errorf("%s needs this computer's %s GPU, which CashPilot passes in only when the container runtime runs on this computer's own Linux kernel (%s). Nothing was deployed", name, gpuVendorNames[grant.Vendor], host.Reason)
	}
	var devices []container.DeviceMapping
	for _, n := range grantDevices(grant, host) {
		devices = append(devices, container.DeviceMapping{PathOnHost: n, PathInContainer: n, CgroupPermissions: "rwm"})
	}
	return gpuResources{Devices: devices}, nil
}

// grantDevices is the device paths an AMD or Intel grant gets from a conclusive
// read that has the vendor: its first render node (or all of them) and, for ROCm,
// /dev/kfd.
func grantDevices(grant catalog.GPUGrant, host hostGPUs) []string {
	nodes := host.renderNodes(grant.Vendor)
	if !grant.All {
		nodes = nodes[:1]
	}
	if grant.ROCm {
		nodes = append(nodes, catalog.KFDDevice)
	}
	return nodes
}

// HostGPUDevices is the device paths a deploy on this computer would pass in for an
// AMD or Intel grant, for the compose export, which has to name them. It fails when
// they cannot be known: the runtime runs elsewhere, the read is not conclusive, or
// the vendor is missing.
func HostGPUDevices(ctx context.Context, grant catalog.GPUGrant) ([]string, error) {
	cli, err := dockerClient()
	if err != nil {
		return nil, err
	}
	defer cli.Close()
	return hostGPUDevices(daemonFacts(ctx, cli), grant)
}

func hostGPUDevices(facts daemonInfo, grant catalog.GPUGrant) ([]string, error) {
	if grant.Vendor == "nvidia" {
		return nil, errors.New("an NVIDIA GPU is passed in by a device request, not by device paths")
	}
	host := readHostGPUs(facts)
	if !host.Conclusive {
		return nil, errors.New(host.Reason)
	}
	if problem := host.missing(grant); problem != "" {
		return nil, errors.New(problem)
	}
	return grantDevices(grant, host), nil
}

// applyGPU puts a grant on the container's host config, next to the devices the
// entry declares.
func applyGPU(host *container.HostConfig, gpu gpuResources) {
	if len(gpu.DeviceRequests) > 0 {
		host.Resources.DeviceRequests = gpu.DeviceRequests
	}
	host.Resources.Devices = append(host.Resources.Devices, gpu.Devices...)
}

// stampGPUEnv refuses an env that sets an NVIDIA_ variable, then adds the void stamp
// unless the container holds an NVIDIA grant.
func stampGPUEnv(svc catalog.Service, env map[string]string, nvidia bool) error {
	var set []string
	for key := range env {
		if strings.HasPrefix(strings.ToUpper(key), catalog.GPUEnvPrefix) {
			set = append(set, key)
		}
	}
	if len(set) > 0 {
		sort.Strings(set)
		return fmt.Errorf("%s was not deployed: CashPilot sets the %s variables itself, and this service sets %s. The running container was not touched", svc.Name, catalog.GPUEnvPrefix, strings.Join(set, ", "))
	}
	if !nvidia {
		env[nvidiaVisible] = "void"
	}
	return nil
}

// withGPUHint adds what to do to a GPU error Docker gives at create or start.
func withGPUHint(err error) error {
	if err == nil {
		return nil
	}
	reason := err.Error()
	switch {
	case strings.Contains(reason, `could not select device driver "nvidia"`), strings.Contains(reason, "failed to discover GPU vendor from CDI"):
		if gpuHostOS == "darwin" {
			return fmt.Errorf("%w. %s", err, macNvidiaHint)
		}
		return fmt.Errorf("%w. %s", err, nvidiaHint)
	case strings.Contains(reason, "error gathering device information while adding custom device"):
		if strings.Contains(reason, catalog.KFDDevice) {
			return fmt.Errorf("%w. %s is missing on this computer: the ROCm driver (amdgpu with KFD) is not loaded", err, catalog.KFDDevice)
		}
		if strings.Contains(reason, "/dev/dri/") {
			return fmt.Errorf("%w. That render node is gone since CashPilot read it: a driver update or a reboot can number them again. Deploy again", err)
		}
	}
	return err
}
