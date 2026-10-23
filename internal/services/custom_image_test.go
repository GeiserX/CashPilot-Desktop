package services

import (
	"context"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
)

// archServiceYAML is an entry with everything a chosen image must NOT change
// (capabilities, a device, a port, an entrypoint, a stop timeout) and a per-arch
// override, which must not replace the chosen image on an ARM machine.
const archServiceYAML = `name: Arch Example
slug: arch-example
category: bandwidth
status: active
docker:
  image: example/arch@sha256:` + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" + `
  image_by_arch:
    arm64: example/arch:arm64v8
  cap_add: [NET_ADMIN]
  devices: [/dev/net/tun]
  ports: ["8080:8080"]
  entrypoint: ["/bin/run"]
  stop_timeout: 120
`

func newArchTestCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	cat, err := catalog.LoadEmbedded(fstest.MapFS{
		"services/bandwidth/arch.yml": {Data: []byte(archServiceYAML)},
	})
	if err != nil {
		t.Fatalf("LoadEmbedded error: %v", err)
	}
	return cat
}

func strPtr(s string) *string { return &s }

func TestValidateImage(t *testing.T) {
	for _, ok := range []string{
		"",
		"honeygain/honeygain:0.8.1",
		"  honeygain/honeygain  ",
		"ghcr.io/org/cli@sha256:" + strings.Repeat("a", 64),
		"localhost:5000/org/cli:v1.2_3",
		"docker.io/library/alpine:3.20@sha256:" + strings.Repeat("0", 64),
	} {
		if err := ValidateImage(ok); err != nil {
			t.Errorf("ValidateImage(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{
		"--privileged",
		"https://ghcr.io/org/cli",
		"org/cli:1.0 --rm",
		"Org/Cli",
		"org/cli@sha256:short",
		"org/cli:" + strings.Repeat("a", 129),
		strings.Repeat("a", 256),
	} {
		if err := ValidateImage(bad); err == nil {
			t.Errorf("ValidateImage(%q) = nil, want an error", bad)
		}
	}
}

// A chosen image runs with exactly the catalog's grants, replaces the per-arch
// override, is kept by a redeploy that leaves the box out and by the runtime refresh,
// and goes away when the box is cleared.
func TestDeployImageKeepsTheChoiceUntilCleared(t *testing.T) {
	st := newTestStore(t)
	fake := &fakeProvider{deployResult: runtime.ContainerInfo{ContainerID: "c1", Name: "cashpilot-arch-example", Status: "running"}}
	m := NewManager(fake, newArchTestCatalog(t), st)
	ctx := context.Background()
	catalogSvc, _ := m.catalog.Get("arch-example")

	dep, err := m.DeployImage(ctx, "arch-example", nil, strPtr(" mirror.test/org/arch:2.0 "))
	if err != nil {
		t.Fatalf("DeployImage error: %v", err)
	}
	if dep.CustomImage != "mirror.test/org/arch:2.0" {
		t.Fatalf("returned CustomImage = %q", dep.CustomImage)
	}
	got := fake.deploySpecs[0].Service.Docker
	if got.Image != "mirror.test/org/arch:2.0" || len(got.ImageByArch) != 0 {
		t.Fatalf("runtime got image %q with overrides %v, want only the chosen image", got.Image, got.ImageByArch)
	}
	if strings.Join(got.CapAdd, ",") != "NET_ADMIN" || strings.Join(got.Devices, ",") != "/dev/net/tun" ||
		strings.Join(got.Ports, ",") != "8080:8080" || strings.Join(got.Entrypoint, ",") != "/bin/run" ||
		runtime.StopTimeoutSeconds(fake.deploySpecs[0].Service) != runtime.StopTimeoutSeconds(catalogSvc) {
		t.Fatalf("a chosen image changed what the catalog grants: %+v", got)
	}
	if catalogSvc.Docker.Image == "mirror.test/org/arch:2.0" || len(catalogSvc.Docker.ImageByArch) == 0 {
		t.Fatal("the choice leaked into the catalog entry itself")
	}

	// The runtime refresh rewrites the row on a timer; the choice must survive it.
	fake.listResult = []runtime.ContainerInfo{{Slug: "arch-example", ContainerID: "c1", Status: "running", Image: "mirror.test/org/arch:2.0"}}
	if _, err := m.Refresh(ctx); err != nil {
		t.Fatalf("Refresh error: %v", err)
	}
	if stored, _, _ := st.GetDeployment("arch-example"); stored.CustomImage != "mirror.test/org/arch:2.0" {
		t.Fatalf("after Refresh CustomImage = %q, want the choice kept", stored.CustomImage)
	}

	// A redeploy that leaves the box out keeps the choice.
	if _, err := m.Deploy(ctx, "arch-example", nil); err != nil {
		t.Fatalf("Deploy error: %v", err)
	}
	if img := fake.deploySpecs[1].Service.Docker.Image; img != "mirror.test/org/arch:2.0" {
		t.Fatalf("redeploy without the box ran %q, want the recorded choice", img)
	}

	// Clearing the box goes back to the catalog image and its per-arch overrides.
	dep, err = m.DeployImage(ctx, "arch-example", nil, strPtr(""))
	if err != nil {
		t.Fatalf("DeployImage(\"\") error: %v", err)
	}
	back := fake.deploySpecs[2].Service.Docker
	if back.Image != catalogSvc.Docker.Image || back.ImageByArch["arm64"] != "example/arch:arm64v8" {
		t.Fatalf("cleared box ran %q with overrides %v, want the catalog's", back.Image, back.ImageByArch)
	}
	if stored, _, _ := st.GetDeployment("arch-example"); stored.CustomImage != "" || dep.CustomImage != "" {
		t.Fatalf("cleared box left CustomImage = %q / %q", stored.CustomImage, dep.CustomImage)
	}
}

func TestDeployImageRefusesABadImageBeforeTheRuntime(t *testing.T) {
	st := newTestStore(t)
	fake := &fakeProvider{}
	m := NewManager(fake, newArchTestCatalog(t), st)
	if _, err := m.DeployImage(context.Background(), "arch-example", nil, strPtr("--privileged")); err == nil {
		t.Fatal("expected a bad image to be refused")
	}
	if fake.deployCalls != 0 {
		t.Fatalf("the runtime was called %d times for a refused image", fake.deployCalls)
	}
}

// An image only runs on Docker, so a chosen image keeps a service that also ships a
// native binary off the native runtime.
func TestDeployImageRoutesAChosenImageToDocker(t *testing.T) {
	st := newTestStore(t)
	docker := &fakeProvider{deployResult: runtime.ContainerInfo{ContainerID: "d1", Status: "running"}}
	native := &fakeProvider{deployResult: runtime.ContainerInfo{ContainerID: "n1", Status: "running"}}
	m := NewManager(docker, newNativeTestCatalog(t), st)
	m.Register(nativeRuntimeKind, native)

	dep, err := m.DeployImage(context.Background(), "native-example", nil, strPtr("mirror.test/org/native:1.0"))
	if err != nil {
		t.Fatalf("DeployImage error: %v", err)
	}
	if docker.deployCalls != 1 || native.deployCalls != 0 || dep.Runtime != defaultRuntimeKind {
		t.Fatalf("docker=%d native=%d runtime=%q, want the chosen image on Docker", docker.deployCalls, native.deployCalls, dep.Runtime)
	}
}
