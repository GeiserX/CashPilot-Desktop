package runtime

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// Bitping containers made before the catalog added NET_RAW ran three months with
// every probe failing ("Operation not permitted ... create new socket") while the
// dashboard showed them healthy. A redeploy fixed them. These tests hold the check
// that says so.

func bitping(capAdd ...string) catalog.Service {
	return catalog.Service{
		Name: "Bitping",
		Slug: "bitping",
		Docker: catalog.DockerConfig{
			Image: "bitping/bitpingd@sha256:6f923c4f5e0fe7fa188f621f3dde0cf62e5d3a28848c08f3f84f1a3d8d875dd9",
			Env: []catalog.EnvVar{
				{Key: "BITPING_EMAIL", Required: true},
				{Key: "BITPING_PASSWORD", Required: true, Secret: true},
			},
			Volumes: []string{"bitping-data:/root/.bitping"},
			CapAdd:  capAdd,
		},
	}
}

// deployedShape is the shape of the container Deploy builds from svc, through the
// same builders Deploy uses, read back the way docker inspect reports it.
func deployedShape(t *testing.T, svc catalog.Service, creds map[string]string) ContainerShape {
	t.Helper()
	env := buildEnv(svc, creds)
	_, bindings, err := buildServicePorts(svc.Docker, env)
	if err != nil {
		t.Fatalf("buildServicePorts: %v", err)
	}
	mounts := buildMounts(svc.Docker.Volumes, env)
	host, err := buildHostConfig(svc, bindings, mounts, "linux")
	if err != nil {
		t.Fatalf("buildHostConfig: %v", err)
	}
	var points []container.MountPoint
	for _, m := range mounts {
		points = append(points, container.MountPoint{Source: m.Source, Destination: m.Target})
	}
	env["PATH"] = "/usr/bin" // the image's own ENV is there too; an unknown key is not drift
	return shapeOf(container.InspectResponse{
		Config:     &container.Config{Image: ImageForArch(svc.Docker, ""), Env: envSlice(env), Entrypoint: entrypointFor(svc)},
		HostConfig: host,
		Mounts:     points,
		Platform:   "linux",
	})
}

var bitpingCreds = map[string]string{"BITPING_EMAIL": "a@b.c", "BITPING_PASSWORD": "pw"}

func TestABitpingContainerMadeWithoutNetRawIsFlagged(t *testing.T) {
	old := deployedShape(t, bitping(), bitpingCreds)
	found := ShapeDrift(bitping("NET_RAW"), old, "")
	if len(found) != 1 || !strings.Contains(found[0], "missing NET_RAW") {
		t.Fatalf("ShapeDrift = %q, want one finding naming the missing NET_RAW", found)
	}
}

func TestAContainerFreshlyDeployedFromTheCatalogMatchesIt(t *testing.T) {
	svc := catalog.Service{
		Name: "Demo",
		Docker: catalog.DockerConfig{
			Image:      "demo/demo:1.0",
			Env:        []catalog.EnvVar{{Key: "TOKEN", Required: true}, {Key: "OPTIONAL"}, {Key: "MODE", Default: "fast"}},
			Ports:      []string{"9000:9000", "9001:9001/udp"},
			Volumes:    []string{"demo-data:/data/", "/srv/x:/x:ro"},
			Devices:    []string{"/dev/net/tun"},
			CapAdd:     []string{"NET_ADMIN"},
			Entrypoint: []string{"/bin/run", "--fast"},
			Command:    "run --token ${TOKEN}",
		},
	}
	if found := ShapeDrift(svc, deployedShape(t, svc, map[string]string{"TOKEN": "t"}), ""); len(found) != 0 {
		t.Fatalf("a container built from the catalog drifts: %q", found)
	}
	if found := ShapeDrift(bitping("NET_RAW"), deployedShape(t, bitping("NET_RAW"), bitpingCreds), ""); len(found) != 0 {
		t.Fatalf("a Bitping made with NET_RAW drifts: %q", found)
	}
}

func TestEachMissingSettingIsNamed(t *testing.T) {
	now := catalog.Service{Docker: catalog.DockerConfig{
		Image:      "demo/demo:1.0",
		Env:        []catalog.EnvVar{{Key: "TOKEN", Required: true}, {Key: "OPTIONAL"}, {Key: "REGION", Default: "eu"}},
		Ports:      []string{"9000:9000"},
		Volumes:    []string{"demo-data:/data"},
		Devices:    []string{"/dev/net/tun"},
		Entrypoint: []string{"/bin/run"},
	}}
	before := catalog.Service{Docker: catalog.DockerConfig{
		Image: "demo/demo:1.0",
		Env:   []catalog.EnvVar{{Key: "TOKEN", Required: true}},
	}}
	found := strings.Join(ShapeDrift(now, deployedShape(t, before, map[string]string{"TOKEN": "t"}), ""), " | ")
	for _, want := range []string{"/dev/net/tun", "9000/tcp", "/data", "REGION", "entrypoint"} {
		if !strings.Contains(found, want) {
			t.Errorf("findings %q do not name %s", found, want)
		}
	}
	// An optional key with no default is set to nothing by a redeploy.
	if strings.Contains(found, "OPTIONAL") {
		t.Errorf("findings %q name OPTIONAL, which a redeploy does not set", found)
	}
}

func TestWhatTheCatalogNoLongerGrantsIsNamed(t *testing.T) {
	old := deployedShape(t, catalog.Service{Docker: catalog.DockerConfig{Image: "demo/demo", CapAdd: []string{"NET_ADMIN"}, Devices: []string{"/dev/net/tun"}}}, nil)
	old.Privileged = true
	found := strings.Join(ShapeDrift(catalog.Service{Docker: catalog.DockerConfig{Image: "demo/demo"}}, old, ""), " | ")
	for _, want := range []string{"still has NET_ADMIN", "still has device /dev/net/tun", "runs privileged"} {
		if !strings.Contains(found, want) {
			t.Errorf("findings %q do not say %q", found, want)
		}
	}
}

func TestHostNetworkingHidesNoPortDifference(t *testing.T) {
	host := catalog.Service{Docker: catalog.DockerConfig{Image: "demo/demo", NetworkMode: "host", Ports: []string{"9000:9000"}}}
	shape := deployedShape(t, host, nil)
	shape.Ports = nil
	if found := ShapeDrift(host, shape, ""); len(found) != 0 {
		t.Fatalf("host networking drifts: %q", found)
	}
	bridged := deployedShape(t, catalog.Service{Docker: catalog.DockerConfig{Image: "demo/demo", Ports: []string{"9000:9000"}}}, nil)
	if found := ShapeDrift(host, bridged, ""); len(found) != 1 || found[0] != "uses its own network, but should share the host's" {
		t.Fatalf("ShapeDrift = %q, want the network mode named", found)
	}
}

// A container made before the hardening holds Docker's default capabilities, NET_RAW
// among them, and a Windows-mode daemon takes none: neither is "missing NET_RAW".
func TestCapabilitiesAreJudgedOnlyOnAHardenedContainer(t *testing.T) {
	shape := deployedShape(t, bitping(), bitpingCreds)
	shape.CapDrop = nil
	if found := ShapeDrift(bitping("NET_RAW"), shape, ""); len(found) != 0 {
		t.Fatalf("an unhardened container is judged on capabilities: %q", found)
	}
}

func TestDockerReportingCapPrefixedNamesIsTheSameCapability(t *testing.T) {
	shape := shapeOf(container.InspectResponse{HostConfig: &container.HostConfig{CapAdd: []string{"CAP_NET_RAW"}, CapDrop: []string{"ALL"}}})
	shape.Mounts = []string{"/root/.bitping"}
	shape.EnvKeys = []string{"BITPING_EMAIL", "BITPING_PASSWORD"}
	if found := ShapeDrift(bitping("NET_RAW"), shape, ""); len(found) != 0 {
		t.Fatalf("CAP_NET_RAW read as different from NET_RAW: %q", found)
	}
}

func TestImages(t *testing.T) {
	own := catalog.Service{Docker: catalog.DockerConfig{Image: "drumsergio/cashpilot-earnapp:1.5.0-r2@sha256:abc"}}
	shapeOn := func(image string) ContainerShape { return ContainerShape{Image: image} }

	// Provider images follow their default tag on purpose.
	if found := ShapeDrift(catalog.Service{Docker: catalog.DockerConfig{Image: bitping().Docker.Image}}, shapeOn("bitping/bitpingd:26.8.17-1"), ""); len(found) != 0 {
		t.Errorf("a provider image on another tag drifts: %q", found)
	}
	found := ShapeDrift(own, shapeOn("drumsergio/cashpilot-earnapp:0.0.1-r1"), "")
	if want := "runs drumsergio/cashpilot-earnapp:0.0.1-r1; the catalog now pins 1.5.0-r2"; len(found) != 1 || found[0] != want {
		t.Errorf("our own image on an older tag: %q, want [%q]", found, want)
	}
	if found := ShapeDrift(own, shapeOn(own.Docker.Image), ""); len(found) != 0 {
		t.Errorf("our own image on the pinned tag drifts: %q", found)
	}
	// An image the user chose is not drift.
	if found := ShapeDrift(own, shapeOn("drumsergio/cashpilot-earnapp:0.0.1-r1"), "drumsergio/cashpilot-earnapp:0.0.1-r1"); len(found) != 0 {
		t.Errorf("a chosen image drifts: %q", found)
	}
	// Another repository is "update available", reported there and not twice.
	if found := ShapeDrift(own, shapeOn("someone/else:1"), ""); len(found) != 0 {
		t.Errorf("another repository is reported as drift too: %q", found)
	}
}

func TestTheFingerprintFollowsJudgedFieldsOnly(t *testing.T) {
	base := bitping()
	mark := ShapeFingerprint(base)
	reworded := bitping()
	reworded.Description, reworded.Docker.Notes = "new words", "new notes"
	reworded.Docker.Env[0].Description = "new help"
	if ShapeFingerprint(reworded) != mark {
		t.Error("a text-only edit changed the fingerprint, so it would bring an ignored warning back")
	}
	if ShapeFingerprint(bitping("NET_RAW")) == mark {
		t.Error("a new capability left the fingerprint alone, so an ignored warning would hide the fix")
	}
}

// fakeShapeClient lists and inspects canned containers.
type fakeShapeClient struct {
	fakeInfoClient
	items    []container.Summary
	inspects map[string]container.InspectResponse
}

func (f fakeShapeClient) ContainerList(context.Context, client.ContainerListOptions) (client.ContainerListResult, error) {
	return client.ContainerListResult{Items: f.items}, nil
}

func (f fakeShapeClient) ContainerInspect(_ context.Context, id string, _ client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	ctr, ok := f.inspects[id]
	if !ok {
		return client.ContainerInspectResult{}, errors.New("no such container")
	}
	return client.ContainerInspectResult{Container: ctr}, nil
}

// Only containers this app made are judged: one from the exported compose file, or
// one a CashPilot worker made on the same daemon, carries cashpilot.deployed-by.
func TestOnlyContainersThisAppMadeAreRead(t *testing.T) {
	inspect := container.InspectResponse{Config: &container.Config{Image: "bitping/bitpingd"}, HostConfig: &container.HostConfig{}}
	cli := fakeShapeClient{
		items: []container.Summary{
			{ID: "mine-2", Labels: map[string]string{LabelManaged: "true", LabelService: "bitping", LabelInstance: "2"}},
			{ID: "mine", Labels: map[string]string{LabelManaged: "true", LabelService: "bitping"}},
			{ID: "compose", Labels: map[string]string{LabelManaged: "true", LabelService: "honeygain", labelDeployedBy: "compose"}},
			{ID: "worker", Labels: map[string]string{LabelManaged: "true", LabelService: "earnapp", labelDeployedBy: "worker"}},
		},
		inspects: map[string]container.InspectResponse{"mine": inspect, "mine-2": inspect, "compose": inspect, "worker": inspect},
	}
	shapes, err := containerShapes(context.Background(), cli)
	if err != nil {
		t.Fatal(err)
	}
	if len(shapes) != 2 || shapes[0].Slug != "bitping" || shapes[0].Instance != 1 || shapes[1].Instance != 2 {
		t.Fatalf("shapes = %+v, want bitping copies 1 and 2 only", shapes)
	}
}

// podmanShape is a hardened container as Podman's inspect reports it, given the
// capabilities it was created with (cap_drop ALL plus kept): CapDrop is Podman's
// default set minus what it kept, CapAdd only what it kept beyond that set, both
// with the CAP_ prefix (libpod container_inspect_linux.go).
func podmanShape(t *testing.T, svc catalog.Service, creds map[string]string, kept ...string) ContainerShape {
	t.Helper()
	shape := deployedShape(t, svc, creds)
	keptSet := toSet(kept)
	var drop, add []string
	for _, c := range podmanDefaultCaps {
		if !keptSet[c] {
			drop = append(drop, "CAP_"+c)
		}
	}
	for _, c := range kept {
		if !slices.Contains(podmanDefaultCaps, c) {
			add = append(add, "CAP_"+c)
		}
	}
	shape.CapDrop, shape.CapAdd = capNames(drop), capNames(add)
	return shape
}

// Podman never reports cap_drop ALL, so the headline case must be read from its own
// way of listing capabilities.
func TestABitpingContainerMadeWithoutNetRawIsFlaggedOnPodman(t *testing.T) {
	found := ShapeDrift(bitping("NET_RAW"), podmanShape(t, bitping(), bitpingCreds), "")
	if len(found) != 1 || !strings.Contains(found[0], "missing NET_RAW") {
		t.Fatalf("ShapeDrift = %q, want one finding naming the missing NET_RAW", found)
	}
	if found := ShapeDrift(bitping("NET_RAW"), podmanShape(t, bitping("NET_RAW"), bitpingCreds, "NET_RAW"), ""); len(found) != 0 {
		t.Fatalf("a Bitping made with NET_RAW on Podman drifts: %q", found)
	}
}

// Mysterium keeps SETUID and SETGID, which are in Podman's default set, so Podman
// leaves them out of CapAdd. That is not "missing".
func TestAKeptDefaultCapabilityOnPodmanIsNotMissing(t *testing.T) {
	svc := catalog.Service{Docker: catalog.DockerConfig{Image: "demo/demo", CapAdd: []string{"NET_ADMIN", "SETUID", "SETGID"}}}
	if found := ShapeDrift(svc, podmanShape(t, svc, nil, "NET_ADMIN", "SETUID", "SETGID"), ""); len(found) != 0 {
		t.Fatalf("a Podman container with the catalog's capabilities drifts: %q", found)
	}
	old := podmanShape(t, svc, nil, "NET_ADMIN")
	if found := strings.Join(ShapeDrift(svc, old, ""), " | "); !strings.Contains(found, "missing SETGID, SETUID") {
		t.Fatalf("findings %q do not name the dropped SETGID, SETUID", found)
	}
}

// A required key with no default is not in the stored settings either, so a redeploy
// fails validation instead of setting it; it is not offered as drift.
func TestARequiredKeyWithNoDefaultIsNotOfferedAsRedeployable(t *testing.T) {
	now := bitping()
	now.Docker.Env = append(now.Docker.Env, catalog.EnvVar{Key: "BITPING_TOKEN", Required: true})
	if found := ShapeDrift(now, deployedShape(t, bitping(), bitpingCreds), ""); len(found) != 0 {
		t.Fatalf("ShapeDrift = %q, want nothing: a redeploy cannot set BITPING_TOKEN", found)
	}
}

// "Still has" reads only what Podman lists as added. With a custom
// default_capabilities that leaves CHOWN out, CapDrop never names CHOWN, so a guess
// from Podman's stock default set would wrongly say the container still holds it.
func TestAPodmanDefaultCapabilityAbsentFromCapDropIsNotStillHeld(t *testing.T) {
	shape := podmanShape(t, bitping("NET_RAW"), bitpingCreds, "NET_RAW")
	shape.CapDrop = slices.DeleteFunc(shape.CapDrop, func(c string) bool { return c == "CHOWN" })
	if found := ShapeDrift(bitping("NET_RAW"), shape, ""); len(found) != 0 {
		t.Fatalf("ShapeDrift = %q, want nothing: CHOWN is in neither of Podman's lists", found)
	}
}
