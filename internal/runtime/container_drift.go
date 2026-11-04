package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// Does a running container still match what a redeploy would build?
//
// A redeploy rebuilds a container from the current catalog and the stored
// credentials. A container made before a catalog fix keeps running without it, and
// nothing said so: Bitping containers ran three months without NET_RAW, which the
// catalog added after they were made, with every probe failing while the dashboard
// showed them healthy. This is the Desktop port of the web console's app/drift.py.
//
// ShapeDrift compares what a container was created with (ContainerShape, read from
// docker inspect) with what Deploy would build from the catalog today, and names each
// difference in plain words. Only differences a redeploy fixes count, so every
// finding has the same remedy. The image tag of a provider image is never drift:
// provider images follow their default tag on purpose. CashPilot's own images are
// pinned in the catalog, so a different tag there is.

// OwnImagePrefix marks the repositories CashPilot builds itself; the catalog pins
// their tag.
const OwnImagePrefix = "drumsergio/cashpilot-"

// labelDeployedBy is set by the compose export ("compose") and by the web worker
// ("worker"). Desktop's own deploy never sets it, so a managed container carrying it
// was made by something else and is not judged here.
const labelDeployedBy = "cashpilot.deployed-by"

// ContainerShape is what a managed container was created with, on the settings that
// decide whether it works. Names only, never values: env keys without their values,
// mount targets without their sources, and no command at all (it carries
// credentials).
type ContainerShape struct {
	Slug     string
	Instance int
	// Arch is the daemon's architecture, which picks the catalog's per-arch image.
	Arch        string
	Image       string
	CapAdd      []string
	CapDrop     []string
	Privileged  bool
	NetworkMode string
	Devices     []string
	Ports       []string
	Mounts      []string
	EnvKeys     []string
	Entrypoint  []string
}

// shapeClient is the part of the Docker client ContainerShapes needs, so the
// listing can be tested without a daemon.
type shapeClient interface {
	infoClient
	inspectClient
	ContainerList(ctx context.Context, options client.ContainerListOptions) (client.ContainerListResult, error)
}

// ContainerShapes reads the shape of every container this app made.
func (p *DockerProvider) ContainerShapes(ctx context.Context) ([]ContainerShape, error) {
	cli, err := dockerClient()
	if err != nil {
		return nil, err
	}
	defer cli.Close()
	return containerShapes(ctx, cli)
}

func containerShapes(ctx context.Context, cli shapeClient) ([]ContainerShape, error) {
	list, err := cli.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: make(client.Filters).Add("label", LabelManaged+"=true"),
	})
	if err != nil {
		return nil, err
	}
	arch := daemonFacts(ctx, cli).Architecture
	var out []ContainerShape
	for _, c := range list.Items {
		// Only what this app made: a container from the exported compose file, or one
		// a CashPilot worker made on the same daemon, is someone else's to shape.
		// A proxied copy and its tunnel are shaped by the proxy, not by the catalog
		// entry, so they are never drift.
		if c.Labels[labelDeployedBy] != "" || c.Labels[LabelService] == "" || IsProxied(c.Labels) {
			continue
		}
		inspected, err := cli.ContainerInspect(ctx, c.ID, client.ContainerInspectOptions{})
		if err != nil {
			continue
		}
		shape := shapeOf(inspected.Container)
		shape.Slug = c.Labels[LabelService]
		shape.Instance = instanceOf(c.Labels)
		shape.Arch = arch
		out = append(out, shape)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Slug != out[j].Slug {
			return out[i].Slug < out[j].Slug
		}
		return out[i].Instance < out[j].Instance
	})
	return out, nil
}

// shapeOf reads a ContainerShape from docker inspect. Nil sections read as empty.
func shapeOf(ctr container.InspectResponse) ContainerShape {
	shape := ContainerShape{}
	if cfg := ctr.Config; cfg != nil {
		shape.Image = cfg.Image
		if len(cfg.Entrypoint) > 0 {
			shape.Entrypoint = slices.Clone(cfg.Entrypoint)
		}
		keys := map[string]bool{}
		for _, item := range cfg.Env {
			key, _, _ := strings.Cut(item, "=")
			keys[key] = true
		}
		shape.EnvKeys = sortedKeys(keys)
	}
	if host := ctr.HostConfig; host != nil {
		shape.CapAdd = capNames(host.CapAdd)
		shape.CapDrop = capNames(host.CapDrop)
		shape.Privileged = host.Privileged
		shape.NetworkMode = string(host.NetworkMode)
		devices := map[string]bool{}
		for _, d := range host.Devices {
			if path := strings.TrimRight(d.PathInContainer, "/"); path != "" {
				devices[path] = true
			}
		}
		shape.Devices = sortedKeys(devices)
		ports := map[string]bool{}
		for port := range host.PortBindings {
			ports[port.String()] = true
		}
		shape.Ports = sortedKeys(ports)
	}
	mounts := map[string]bool{}
	for _, m := range ctr.Mounts {
		if target := strings.TrimRight(m.Destination, "/"); target != "" {
			mounts[target] = true
		}
	}
	shape.Mounts = sortedKeys(mounts)
	return shape
}

// ShapeDrift is each way shape differs from what a redeploy would build from svc
// today. customImage is the image the user chose for this service, if any. An empty
// list means the container matches.
func ShapeDrift(svc catalog.Service, shape ContainerShape, customImage string) []string {
	dc := svc.Docker
	var found []string

	// Capabilities. Judged only on a hardened container, one that drops capabilities:
	// a Windows-mode daemon takes no capabilities at all, and a container made before
	// the hardening holds the runtime's default set with nothing dropped, so naming a
	// missing capability there would be wrong. Desktop only ever drops ALL, so any
	// drop list is its hardening.
	if len(shape.CapDrop) > 0 {
		want, have := toSet(capNames(dc.CapAdd)), heldCaps(shape)
		if missing := minus(want, have); len(missing) > 0 {
			pronoun := "it"
			if len(missing) > 1 {
				pronoun = "them"
			}
			found = append(found, fmt.Sprintf("missing %s, which the catalog now grants; the container was made without %s", strings.Join(missing, ", "), pronoun))
		}
		// Only what the runtime lists as added: on Podman a kept default capability is
		// in neither list, and a custom default set there would make a guess wrong.
		if extra := minus(toSet(shape.CapAdd), want); len(extra) > 0 {
			found = append(found, fmt.Sprintf("still has %s, which the catalog no longer grants", strings.Join(extra, ", ")))
		}
	}

	wantDevices := map[string]bool{}
	for _, raw := range dc.Devices {
		if target := deviceTarget(raw); target != "" {
			wantDevices[target] = true
		}
	}
	haveDevices := toSet(shape.Devices)
	if missing := minus(wantDevices, haveDevices); len(missing) > 0 {
		found = append(found, fmt.Sprintf("missing device %s, which the catalog now passes in", strings.Join(missing, ", ")))
	}
	if extra := minus(haveDevices, wantDevices); len(extra) > 0 {
		found = append(found, fmt.Sprintf("still has device %s, which the catalog no longer passes in", strings.Join(extra, ", ")))
	}

	if shape.Privileged && !dc.Privileged {
		found = append(found, "runs privileged, which CashPilot no longer allows")
	}

	if len(dc.Entrypoint) > 0 && !slices.Equal(shape.Entrypoint, dc.Entrypoint) {
		found = append(found, "starts with a different entrypoint than the catalog declares")
	}

	// Network mode, and the ports only where the container has its own network.
	wantHost := dc.NetworkMode == "host"
	haveHost := shape.NetworkMode == "host"
	switch {
	case wantHost && !haveHost:
		found = append(found, "uses its own network, but should share the host's")
	case !wantHost && haveHost:
		found = append(found, "shares the host's network")
	case !wantHost:
		if missing := minus(catalogPortKeys(dc.Ports), toSet(shape.Ports)); len(missing) > 0 {
			found = append(found, fmt.Sprintf("does not publish port %s", strings.Join(missing, ", ")))
		}
	}

	wantMounts := map[string]bool{}
	for _, raw := range dc.Volumes {
		parts := strings.Split(raw, ":")
		if len(parts) > 1 {
			if target := strings.TrimRight(parts[1], "/"); target != "" {
				wantMounts[target] = true
			}
		}
	}
	if missing := minus(wantMounts, toSet(shape.Mounts)); len(missing) > 0 {
		found = append(found, fmt.Sprintf("has no mount at %s, which the catalog now declares", strings.Join(missing, ", ")))
	}

	// Env: the keys a redeploy sets on its own, the ones with a default. Desktop puts
	// every key in the environment, the ones the command uses included, so a default
	// the catalog renamed shows here even though the command itself is never read. A
	// required key with no default is left out: the stored settings were saved with
	// the deploy that made this container, so they lack it too, and a redeploy would
	// fail validation instead of fixing it. The setup form is the way out there.
	wantEnv := map[string]bool{}
	for _, item := range dc.Env {
		if item.Key != "" && item.Default != "" {
			wantEnv[item.Key] = true
		}
	}
	if missing := minus(wantEnv, toSet(shape.EnvKeys)); len(missing) > 0 {
		found = append(found, fmt.Sprintf("has no %s setting, which the catalog now sets", strings.Join(missing, ", ")))
	}

	// CashPilot's own images are pinned, so a different tag is an image a fix never
	// reached. A different repository or digest is already "update available"
	// (catalog.ImageOutdated), so only the tag is judged here.
	want := customImage
	if want == "" {
		want = ImageForArch(dc, shape.Arch)
	}
	if strings.HasPrefix(want, OwnImagePrefix) && shape.Image != "" {
		wRepo, wTag := splitRef(want)
		hRepo, hTag := splitRef(shape.Image)
		if wRepo == hRepo && wTag != "" && hTag != "" && wTag != hTag {
			found = append(found, fmt.Sprintf("runs %s:%s; the catalog now pins %s", hRepo, hTag, wTag))
		}
	}
	return found
}

// ShapeFingerprint is a short hash of what the catalog says about the fields
// ShapeDrift judges. An Ignore is tied to it, so a later catalog fix to any of them
// shows again, while an edit to a service's notes or description does not.
func ShapeFingerprint(svc catalog.Service) string {
	dc := svc.Docker
	type envField struct {
		Key      string `json:"key"`
		Required bool   `json:"required"`
		Default  string `json:"default"`
	}
	env := make([]envField, 0, len(dc.Env))
	for _, item := range dc.Env {
		env = append(env, envField{Key: item.Key, Required: item.Required, Default: item.Default})
	}
	judged := struct {
		Image       string            `json:"image"`
		ImageByArch map[string]string `json:"image_by_arch"`
		CapAdd      []string          `json:"cap_add"`
		Devices     []string          `json:"devices"`
		Privileged  bool              `json:"privileged"`
		Entrypoint  []string          `json:"entrypoint"`
		NetworkMode string            `json:"network_mode"`
		Ports       []string          `json:"ports"`
		Volumes     []string          `json:"volumes"`
		Env         []envField        `json:"env"`
	}{dc.Image, dc.ImageByArch, dc.CapAdd, dc.Devices, dc.Privileged, dc.Entrypoint, dc.NetworkMode, dc.Ports, dc.Volumes, env}
	blob, _ := json.Marshal(judged) // map keys are sorted by encoding/json
	sum := sha256.Sum256(blob)
	return hex.EncodeToString(sum[:])[:16]
}

// podmanDefaultCaps is Podman's default capability set (containers/common
// pkg/config DefaultCapabilities). Podman's inspect reports capabilities against it
// rather than the way Docker does: CapDrop is this set minus what the container
// kept, and CapAdd only what it holds beyond this set (libpod
// container_inspect_linux.go).
var podmanDefaultCaps = []string{
	"CHOWN", "DAC_OVERRIDE", "FOWNER", "FSETID", "KILL", "NET_BIND_SERVICE",
	"SETFCAP", "SETGID", "SETPCAP", "SETUID", "SYS_CHROOT",
}

// heldCaps is what a hardened container holds. Docker reports the create request
// (cap_drop ALL, then cap_add), so it is CapAdd. Podman never reports ALL: there it
// is CapAdd plus every default capability its CapDrop does not name.
func heldCaps(shape ContainerShape) map[string]bool {
	held := toSet(shape.CapAdd)
	if slices.Contains(shape.CapDrop, "ALL") {
		return held
	}
	dropped := toSet(shape.CapDrop)
	for _, c := range podmanDefaultCaps {
		if !dropped[c] {
			held[c] = true
		}
	}
	return held
}

// deviceTarget is the path inside the container of a catalog device
// ("host[:container[:perms]]"), the same reading buildDevices makes.
func deviceTarget(raw string) string {
	parts := strings.Split(strings.TrimSpace(raw), ":")
	target := strings.TrimSpace(parts[0])
	if len(parts) > 1 && strings.TrimSpace(parts[1]) != "" {
		target = strings.TrimSpace(parts[1])
	}
	return strings.TrimRight(target, "/")
}

// catalogPortKeys are the container ports a catalog entry publishes, written the way
// docker inspect keys them ("9000/tcp"), the same reading buildPorts makes.
func catalogPortKeys(raw []string) map[string]bool {
	keys := map[string]bool{}
	for _, mapping := range raw {
		parts := strings.Split(mapping, ":")
		if len(parts) != 2 || parts[1] == "" {
			continue
		}
		port := parts[1]
		if !strings.Contains(port, "/") {
			port += "/tcp"
		}
		keys[port] = true
	}
	return keys
}

// splitRef is the repository and tag of an image reference: "latest" for no tag, ""
// for a bare digest.
func splitRef(ref string) (string, string) {
	ref, digest, _ := strings.Cut(ref, "@")
	slash := strings.LastIndex(ref, "/")
	if colon := strings.LastIndex(ref, ":"); colon > slash {
		return ref[:colon], ref[colon+1:]
	}
	if digest != "" {
		return ref, ""
	}
	return ref, "latest"
}

func capNames(values []string) []string {
	set := map[string]bool{}
	for _, v := range values {
		if name := strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(v)), "CAP_"); name != "" {
			set[name] = true
		}
	}
	return sortedKeys(set)
}

func toSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, v := range values {
		set[v] = true
	}
	return set
}

// minus is the sorted members of a that b lacks.
func minus(a, b map[string]bool) []string {
	var out []string
	for k := range a {
		if !b[k] {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func sortedKeys(set map[string]bool) []string {
	if len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
