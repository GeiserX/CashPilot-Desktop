package runtime

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/e2e/harness"
	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// startHarnessDocker points this package's Docker client at the e2e fake daemon.
func startHarnessDocker(t *testing.T) *harness.Docker {
	t.Helper()
	docker := harness.NewDocker()
	t.Cleanup(docker.Close)
	t.Setenv("DOCKER_HOST", docker.Host())
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	t.Setenv("DOCKER_API_VERSION", "")
	return docker
}

// commandService is shaped like Traffmonetizer: the device name is an env value
// derived from {hostname}, and the command passes it on the command line.
var commandService = catalog.Service{
	Name: "Command Example",
	Slug: "cmd-example",
	Docker: catalog.DockerConfig{
		Image: "example/cmd:1.0",
		Env: []catalog.EnvVar{
			{Key: "TOKEN", Required: true},
			{Key: "DEVICE_NAME", Default: "cashpilot-{hostname}"},
		},
		Command: "start --token ${TOKEN} --device-name ${DEVICE_NAME}",
	},
}

func argAfter(args []string, flag string) string {
	if i := slices.Index(args, flag); i >= 0 && i+1 < len(args) {
		return args[i+1]
	}
	return ""
}

// Each copy is its own container, and its command is built from its own env: the
// device name on Traffmonetizer's command line is the suffixed one, or every copy
// would register as the same device.
func TestDockerCopiesRunUnderTheirOwnNamesAndDeviceNames(t *testing.T) {
	docker := startHarnessDocker(t)
	p := NewDockerProvider()
	ctx := context.Background()
	device := "cashpilot-" + DeviceHostname()

	for n := 1; n <= 3; n++ {
		info, err := p.Deploy(ctx, DeploySpec{Slug: "cmd-example", Service: commandService, Env: map[string]string{"TOKEN": "t"}, Instance: n}, nil)
		if err != nil {
			t.Fatalf("Deploy copy %d: %v", n, err)
		}
		if info.Instance != n || info.Name != copyName("cmd-example", n) {
			t.Fatalf("Deploy copy %d returned %+v", n, info)
		}
	}
	for n, want := range map[int]string{1: device, 2: device + "-2", 3: device + "-3"} {
		c, ok := docker.Container(copyName("cmd-example", n))
		if !ok {
			t.Fatalf("copy %d is not in the runtime; have %v", n, docker.Names())
		}
		if got := argAfter(c.Cmd, "--device-name"); got != want {
			t.Errorf("copy %d command passes --device-name %q, want %q (cmd %v)", n, got, want, c.Cmd)
		}
		if !slices.Contains(c.Env, "DEVICE_NAME="+want) {
			t.Errorf("copy %d env %v, want DEVICE_NAME=%s", n, c.Env, want)
		}
	}

	listed, err := p.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	seen := map[int]bool{}
	for _, info := range listed {
		seen[info.Instance] = true
	}
	if len(listed) != 3 || !seen[1] || !seen[2] || !seen[3] {
		t.Fatalf("List = %+v, want copies 1, 2 and 3 each carrying its number", listed)
	}
	// Another service's copy is never counted as this one's, even by a daemon that
	// ignores the label filter (the fake does).
	other := commandService
	other.Slug = "other-example"
	if _, err := p.Deploy(ctx, DeploySpec{Slug: "other-example", Service: other, Env: map[string]string{"TOKEN": "t"}, Instance: 4}, nil); err != nil {
		t.Fatalf("Deploy other copy: %v", err)
	}
	if copies, err := p.Copies(ctx, "cmd-example"); err != nil || !slices.Equal(copies, []int{2, 3}) {
		t.Fatalf("Copies = %v, %v; want [2 3]", copies, err)
	}
}

// Stop, Start, Logs and Remove of a copy reach that copy and nothing else.
func TestDockerCopyActionsHitOnlyThatCopy(t *testing.T) {
	docker := startHarnessDocker(t)
	docker.SetLogs("copy two says hello\n")
	p := NewDockerProvider()
	ctx := context.Background()
	for n := 1; n <= 3; n++ {
		if _, err := p.Deploy(ctx, DeploySpec{Slug: "cmd-example", Service: commandService, Env: map[string]string{"TOKEN": "t"}, Instance: n}, nil); err != nil {
			t.Fatalf("Deploy copy %d: %v", n, err)
		}
	}
	state := func(n int) string {
		c, _ := docker.Container(copyName("cmd-example", n))
		return c.State
	}

	if err := p.StopCopy(ctx, "cmd-example", 2, 5); err != nil {
		t.Fatalf("StopCopy: %v", err)
	}
	if state(1) != "running" || state(2) != "exited" || state(3) != "running" {
		t.Fatalf("after StopCopy(2) states are %s/%s/%s", state(1), state(2), state(3))
	}
	if err := p.StartCopy(ctx, "cmd-example", 2); err != nil || state(2) != "running" {
		t.Fatalf("StartCopy(2) = %v, state %s", err, state(2))
	}
	if logs, err := p.LogsCopy(ctx, "cmd-example", 2, 0); err != nil || !strings.Contains(logs, "copy two says hello") {
		t.Fatalf("LogsCopy(2) = %q, %v", logs, err)
	}
	if err := p.RemoveCopy(ctx, "cmd-example", 3, 5); err != nil {
		t.Fatalf("RemoveCopy: %v", err)
	}
	if _, ok := docker.Container(copyName("cmd-example", 3)); ok {
		t.Fatal("copy 3 survived RemoveCopy")
	}
	if state(1) != "running" || state(2) != "running" {
		t.Fatalf("RemoveCopy(3) touched another copy: %s/%s", state(1), state(2))
	}

	// A copy that does not exist, or copy 1 asked for as a copy, is refused.
	for name, call := range map[string]func() error{
		"StopCopy(9)":   func() error { return p.StopCopy(ctx, "cmd-example", 9, 5) },
		"StartCopy(9)":  func() error { return p.StartCopy(ctx, "cmd-example", 9) },
		"RemoveCopy(9)": func() error { return p.RemoveCopy(ctx, "cmd-example", 9, 5) },
		"LogsCopy(9)":   func() error { _, err := p.LogsCopy(ctx, "cmd-example", 9, 10); return err },
		"StopCopy(1)":   func() error { return p.StopCopy(ctx, "cmd-example", 1, 5) },
	} {
		if err := call(); err == nil {
			t.Errorf("%s succeeded", name)
		}
	}
}

// A service with a published port or a volume is refused as a second copy before
// anything is pulled: the copies would fight over the port or share the data.
func TestDockerRefusesACopyOfAServiceWithPortsOrVolumes(t *testing.T) {
	docker := startHarnessDocker(t)
	p := NewDockerProvider()
	for name, svc := range map[string]catalog.Service{
		"ports":   {Name: "P", Slug: "p", Docker: catalog.DockerConfig{Image: "example/p:1", Ports: []string{"8080:8080"}}},
		"volumes": {Name: "V", Slug: "v", Docker: catalog.DockerConfig{Image: "example/v:1", Volumes: []string{"v-data:/data"}}},
	} {
		if _, err := p.Deploy(context.Background(), DeploySpec{Slug: svc.Slug, Service: svc, Instance: 2}, nil); err == nil {
			t.Errorf("%s: a second copy was deployed", name)
		}
	}
	if images := docker.Images(); len(images) != 0 {
		t.Fatalf("refused copies still pulled %v", images)
	}
}

// With the daemon down every copy call reports it instead of pretending.
func TestDockerCopyCallsFailWithTheDaemonDown(t *testing.T) {
	docker := startHarnessDocker(t)
	docker.SetDown(true)
	p := NewDockerProvider()
	ctx := context.Background()
	if _, err := p.Copies(ctx, "cmd-example"); err == nil {
		t.Error("Copies succeeded with the daemon down")
	}
	if err := p.StopCopy(ctx, "cmd-example", 2, 5); err == nil {
		t.Error("StopCopy succeeded with the daemon down")
	}
}
