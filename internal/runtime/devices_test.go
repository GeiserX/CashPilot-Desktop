package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// THE RULE: a device the catalog declares reaches the container.
//
// Mysterium's entry asks for /dev/net/tun and explains why: without the TUN device
// the node starts, registers, is listed in discovery and carries no traffic. It looks
// healthy in every view CashPilot has and earns nothing. The catalog carried the
// field, the loader parsed it, and the runtime built its HostConfig without it — so
// the fix that was written down in the entry never actually happened.
func TestDeclaredDevicesReachTheContainer(t *testing.T) {
	svc := catalog.Service{Name: "Mysterium"}
	svc.Docker.Devices = []string{"/dev/net/tun"}

	hostConfig, err := buildHostConfig(svc, nil, nil)
	if err != nil {
		t.Fatalf("buildHostConfig: %v", err)
	}
	if len(hostConfig.Resources.Devices) != 1 {
		t.Fatalf("declared devices did not reach the container: %+v", hostConfig.Resources.Devices)
	}
	got := hostConfig.Resources.Devices[0]
	if got.PathOnHost != "/dev/net/tun" || got.PathInContainer != "/dev/net/tun" {
		t.Errorf("device mapped as %+v; want /dev/net/tun on both sides", got)
	}
	if got.CgroupPermissions == "" {
		t.Error("device mapped with no cgroup permissions; the container cannot use it")
	}
}

// The real entry is what has to work, not a hand-written fixture.
func TestTheCatalogsMysteriumEntryGetsItsTunDevice(t *testing.T) {
	cat, err := catalog.LoadEmbedded(os.DirFS(filepath.Join("..", "..")))
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	svc, ok := cat.Get("mysterium")
	if !ok {
		t.Fatal("the catalog has no mysterium entry")
	}
	hostConfig, err := buildHostConfig(svc, nil, nil)
	if err != nil {
		t.Fatalf("buildHostConfig(mysterium): %v", err)
	}
	var paths []string
	for _, device := range hostConfig.Resources.Devices {
		paths = append(paths, device.PathOnHost)
	}
	if !contains(paths, "/dev/net/tun") {
		t.Errorf("mysterium deployed without /dev/net/tun (devices: %v); the node would register and carry no traffic", paths)
	}
	// The capabilities its entry asks for have to survive the same trip.
	for _, capability := range []string{"NET_ADMIN", "SETUID", "SETGID"} {
		if !contains(hostConfig.CapAdd, capability) {
			t.Errorf("mysterium deployed without %s (cap_add: %v); every session dies at sudo", capability, hostConfig.CapAdd)
		}
	}
}

// A device is a direct line to the kernel. The ceiling lives in code, so a catalog
// entry cannot widen it by itself — and a device outside it fails the deploy loudly
// rather than producing a container that cannot do its job but looks fine.
func TestADeviceOutsideTheCeilingIsRefused(t *testing.T) {
	svc := catalog.Service{Name: "Greedy"}
	svc.Docker.Devices = []string{"/dev/net/tun", "/dev/mem"}

	hostConfig, err := buildHostConfig(svc, nil, nil)
	if err == nil {
		t.Fatalf("a container asking for /dev/mem was accepted: %+v", hostConfig.Resources.Devices)
	}
	if !strings.Contains(err.Error(), "/dev/mem") {
		t.Errorf("the refusal does not name the device: %v", err)
	}
	if hostConfig != nil {
		t.Error("a refused deploy still produced a HostConfig")
	}
}

// A declaration with no host path is a half-written mapping, not a device.
// ":/dev/net/tun" names where the device should appear inside the container and
// never says which host device to map, and it used to be skipped: the deploy
// succeeded and the container came up without the device its entry asked for. That
// is the Mysterium failure exactly — healthy in every view, carrying no traffic —
// reached from the other direction, so it is refused like a blocked device.
func TestADeviceWithNoHostPathIsRefused(t *testing.T) {
	for _, entry := range []string{":/dev/net/tun", ":/dev/net/tun:rwm", "/:/dev/net/tun"} {
		svc := catalog.Service{Name: "Halfwritten"}
		svc.Docker.Devices = []string{entry}

		hostConfig, err := buildHostConfig(svc, nil, nil)
		if err == nil {
			t.Errorf("the device declaration %q was accepted and mapped %+v", entry, hostConfig.Resources.Devices)
			continue
		}
		if !strings.Contains(err.Error(), entry) {
			t.Errorf("the refusal of %q does not name the declaration: %v", entry, err)
		}
	}
}

// Docker's host:container:perms form, and the empty case.
func TestDeviceMappingForms(t *testing.T) {
	devices, err := buildDevices([]string{" /dev/net/tun:/dev/net/tun:rw ", "", "/dev/net/tun/"})
	if err != nil {
		t.Fatalf("buildDevices: %v", err)
	}
	if len(devices) != 2 {
		t.Fatalf("got %d devices; want 2 (the blank entry is skipped)", len(devices))
	}
	if devices[0].CgroupPermissions != "rw" {
		t.Errorf("explicit permissions dropped: %+v", devices[0])
	}
	if devices[1].PathOnHost != "/dev/net/tun" {
		t.Errorf("a trailing slash changed the device path: %+v", devices[1])
	}
	empty, err := buildDevices(nil)
	if err != nil || empty != nil {
		t.Errorf("a service declaring no devices got %+v (err %v); want nil", empty, err)
	}
}

func contains(haystack []string, needle string) bool {
	for _, item := range haystack {
		if item == needle {
			return true
		}
	}
	return false
}
