package runtime

import (
	"slices"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// THE RULE: every managed container runs with the kernel surface its own catalog entry
// asks for, and not one capability more.
//
// These images are third-party and closed-source. Without this they ran as in-container
// root with Docker's whole default capability set, which includes NET_RAW — raw sockets
// on the bridge network, i.e. ARP and DNS spoofing against everything else on it — plus
// MKNOD, SETUID and SYS_CHROOT. Nothing in the app surfaced that, so there was no way
// for a user to know.
//
// The assertions are on the HostConfig the daemon is actually sent, never on the source
// text: a policy written in a comment and dropped on the way to ContainerCreate would
// read exactly the same.
func TestBuildHostConfigDropsAllCapabilities(t *testing.T) {
	svc := catalog.Service{
		Name:   "plain earner",
		Docker: catalog.DockerConfig{Image: "example/image:1.0.0"},
	}

	hostConfig, err := buildHostConfig(svc, nil, nil, "linux")
	if err != nil {
		t.Fatalf("buildHostConfig: %v", err)
	}
	if !slices.Equal(hostConfig.CapDrop, []string{"ALL"}) {
		t.Fatalf("expected every capability dropped, got CapDrop=%v", hostConfig.CapDrop)
	}
	if len(hostConfig.CapAdd) != 0 {
		t.Fatalf("a service that declares no cap_add was given %v", hostConfig.CapAdd)
	}
	if !slices.Contains(hostConfig.SecurityOpt, "no-new-privileges:true") {
		t.Fatalf("expected no-new-privileges, got SecurityOpt=%v", hostConfig.SecurityOpt)
	}
	if hostConfig.PidsLimit == nil || *hostConfig.PidsLimit != defaultPidsLimit {
		t.Fatalf("expected a PID ceiling of %d, got %v", defaultPidsLimit, hostConfig.PidsLimit)
	}
	if hostConfig.Privileged {
		t.Fatal("a service that does not ask for privileged was given it")
	}
}

// A capability the entry declares is added back on top of the empty set — and nothing
// else is. Mysterium is the live case: it runs sudo to configure its interface, and
// without SETUID and SETGID every session dies at setup while the node keeps
// registering and looking perfectly healthy.
func TestBuildHostConfigAddsBackOnlyDeclaredCapabilities(t *testing.T) {
	svc := catalog.Service{
		Name: "Mysterium",
		Docker: catalog.DockerConfig{
			Image:  "mysteriumnetwork/myst:1.0.0",
			CapAdd: []string{"NET_ADMIN", "SETUID", "SETGID"},
		},
	}

	hostConfig, err := buildHostConfig(svc, nil, nil, "linux")
	if err != nil {
		t.Fatalf("buildHostConfig: %v", err)
	}
	if !slices.Equal(hostConfig.CapDrop, []string{"ALL"}) {
		t.Fatalf("declaring cap_add must not skip the drop, got CapDrop=%v", hostConfig.CapDrop)
	}
	if !slices.Equal(hostConfig.CapAdd, []string{"NET_ADMIN", "SETUID", "SETGID"}) {
		t.Fatalf("CapAdd = %v, want exactly what the entry declares", hostConfig.CapAdd)
	}
	// The one capability the default set grants and nobody asks for.
	if slices.Contains(hostConfig.CapAdd, "NET_RAW") {
		t.Fatal("NET_RAW was granted to a service that does not declare it")
	}
}

// privileged is the catalog's call and nothing else's: an entry that does not ask for
// it never gets it, and an entry that does is not silently downgraded into a container
// that starts and cannot work.
func TestBuildHostConfigPrivilegedFollowsTheCatalog(t *testing.T) {
	svc := catalog.Service{
		Name:   "needs the host",
		Docker: catalog.DockerConfig{Image: "example/image:1.0.0", Privileged: true},
	}
	hostConfig, err := buildHostConfig(svc, nil, nil, "linux")
	if err != nil {
		t.Fatalf("buildHostConfig: %v", err)
	}
	if !hostConfig.Privileged {
		t.Fatal("an entry that declares privileged was not given it")
	}
}

// Docker Desktop on Windows can be switched to Windows containers, and that daemon
// REFUSES a create carrying cap_add or cap_drop outright ("adding or dropping kernel
// capabilities is not supported on Windows"). Sending the Linux-only knobs there would
// turn every deploy into an error the user cannot act on, so they are left off.
func TestBuildHostConfigSkipsLinuxOnlyKnobsOnAWindowsDaemon(t *testing.T) {
	svc := catalog.Service{
		Name:   "plain earner",
		Docker: catalog.DockerConfig{Image: "example/image:1.0.0", CapAdd: []string{"NET_ADMIN"}},
	}

	hostConfig, err := buildHostConfig(svc, nil, nil, "windows")
	if err != nil {
		t.Fatalf("buildHostConfig: %v", err)
	}
	if len(hostConfig.CapDrop) != 0 || len(hostConfig.CapAdd) != 0 {
		t.Fatalf("a Windows daemon was sent capabilities: CapDrop=%v CapAdd=%v", hostConfig.CapDrop, hostConfig.CapAdd)
	}
	if len(hostConfig.SecurityOpt) != 0 {
		t.Fatalf("a Windows daemon was sent SecurityOpt=%v", hostConfig.SecurityOpt)
	}
	if hostConfig.PidsLimit != nil {
		t.Fatalf("a Windows daemon was sent PidsLimit=%v", *hostConfig.PidsLimit)
	}
}

// A daemon that cannot say what it is gets the hardening anyway: failing open on
// security is the wrong direction, and every catalog image is a Linux image.
func TestBuildHostConfigHardensWhenTheDaemonOSIsUnknown(t *testing.T) {
	hostConfig, err := buildHostConfig(catalog.Service{Docker: catalog.DockerConfig{Image: "x:1"}}, nil, nil, "")
	if err != nil {
		t.Fatalf("buildHostConfig: %v", err)
	}
	if !slices.Equal(hostConfig.CapDrop, []string{"ALL"}) {
		t.Fatalf("an unknown daemon OS skipped the capability drop: CapDrop=%v", hostConfig.CapDrop)
	}
}

// The PID ceiling is overridable, because a service could legitimately need more — but
// only in the direction that keeps a ceiling. 0 and -1 mean "unlimited" to Docker, so
// accepting them would switch off the limit while looking like configuration.
func TestPidsLimitEnvOverride(t *testing.T) {
	cases := []struct {
		name string
		env  string
		want int64
	}{
		{"unset keeps the default", "", defaultPidsLimit},
		{"a real number is used", "4096", 4096},
		{"a typo falls back", "512m", defaultPidsLimit},
		{"zero would mean unlimited", "0", defaultPidsLimit},
		{"minus one would mean unlimited", "-1", defaultPidsLimit},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(pidsLimitEnv, tc.env)
			if got := pidsLimit(); got != tc.want {
				t.Fatalf("pidsLimit() with %s=%q = %d, want %d", pidsLimitEnv, tc.env, got, tc.want)
			}
		})
	}
}

// Devices stay on the runtime's own allow-list. buildHostConfig is the path a deploy
// takes, so the ceiling has to hold there and not only in buildDevices.
func TestBuildHostConfigRefusesDevicesOutsideTheAllowList(t *testing.T) {
	svc := catalog.Service{
		Name:   "greedy",
		Docker: catalog.DockerConfig{Image: "example/image:1.0.0", Devices: []string{"/dev/mem"}},
	}
	if _, err := buildHostConfig(svc, nil, nil, "linux"); err == nil {
		t.Fatal("a device outside the allow-list was accepted")
	}
}

// A catalog entrypoint reaches the container config as a copy; an entry without one
// keeps the image's own (nil, not an empty list, which Docker would read as "none").
func TestEntrypointForComesFromTheCatalog(t *testing.T) {
	wrapper := []string{"/bin/sh", "-c", "exec /entrypoint \"$@\"", "--"}
	svc := catalog.Service{Docker: catalog.DockerConfig{Image: "storjlabs/storagenode", Entrypoint: wrapper}}
	got := entrypointFor(svc)
	if !slices.Equal(got, wrapper) {
		t.Fatalf("entrypointFor = %v, want %v", got, wrapper)
	}
	got[0] = "/changed"
	if svc.Docker.Entrypoint[0] != "/bin/sh" {
		t.Fatal("the container config aliases the catalog's slice")
	}
	if got := entrypointFor(catalog.Service{Docker: catalog.DockerConfig{Image: "x"}}); got != nil {
		t.Fatalf("an entry without an entrypoint got %v, want nil", got)
	}
}
