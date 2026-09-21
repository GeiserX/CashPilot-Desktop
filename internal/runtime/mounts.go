package runtime

import (
	"context"
	"strings"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// defaultStopTimeout is how long a container gets to shut down cleanly when its
// catalog entry does not say. Same default as the web worker.
//
// The old hardcoded 20 seconds was not enough for the services that need the longest:
// Storj asks for 300 because a storage node has to flush before it goes, and killing
// it early costs an unclean-shutdown recovery on the next start.
const defaultStopTimeout = 30

// StopTimeoutSeconds is the grace period a service's own catalog entry asks for.
// Zero or negative means the entry is silent, and the shared default applies.
func StopTimeoutSeconds(svc catalog.Service) int {
	if svc.Docker.StopTimeout > 0 {
		return svc.Docker.StopTimeout
	}
	return defaultStopTimeout
}

// inspectClient is the one call the stop-timeout and mount-preserving code needs,
// narrowed to an interface so both are unit-testable without a daemon.
type inspectClient interface {
	ContainerInspect(ctx context.Context, containerID string, options client.ContainerInspectOptions) (client.ContainerInspectResult, error)
}

// stopTimeoutForContainer decides how long a container gets to shut down cleanly.
//
// The catalog wins when it can be consulted, because it is the current answer: an entry
// raised to 300 seconds has to apply to the Storj node already running, not only to one
// deployed after the change. fromCatalog is 0 when the caller had no entry to read —
// a service dropped from the catalog, or a container someone made by hand.
//
// Then the container's own config, which Deploy writes the catalog value into, so even
// a container whose entry has since disappeared keeps the grace period it was built
// with instead of falling to the daemon's 10-second default. An unreadable or unset
// value falls back to defaultStopTimeout.
func stopTimeoutForContainer(ctx context.Context, cli inspectClient, name string, fromCatalog int) int {
	if fromCatalog > 0 {
		return fromCatalog
	}
	result, err := cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if err != nil {
		return defaultStopTimeout
	}
	cfg := result.Container.Config
	if cfg == nil || cfg.StopTimeout == nil || *cfg.StopTimeout <= 0 {
		return defaultStopTimeout
	}
	return *cfg.StopTimeout
}

// KeptMount records one mount a redeploy kept where it already was, so the deploy can
// say so out loud instead of moving data silently.
type KeptMount struct {
	// Target is the path inside the container.
	Target string
	// Kept is where the data actually lives and stays.
	Kept string
	// Requested is where the catalog entry would have put it.
	Requested string
}

// keepLiveMounts decides where a REPLACED container's data lives.
//
// Where a running container keeps its data is a fact; the spec is a guess. At every
// target the two have in common, the replacement mounts what the container has mounted
// now. Without this, a container whose data lives somewhere the spec does not know
// about — created by hand, deployed before the entry named a volume, or moved by the
// user outside the app — comes back on the catalog's own volume. For Mysterium, Storj
// and every service that keeps an identity on disk, that is a DIFFERENT node: it starts
// clean, earns from zero and the old reputation and held balance are stranded.
//
// The one exception is a move the user asked for. A catalog volume whose host side is a
// form field (Storj's "${IDENTITY_DIR}:/app/identity") moves when the user types a new
// path, and that is compared against the value the live container was deployed with —
// changed means moved on purpose, so the spec wins. A volume with no field in it
// (Mysterium's "mysterium-data:/var/lib/mysterium-node") can never be moved this way,
// so it always keeps what is live.
//
// A mount that is read-only now stays read-only: a replacement that could write to data
// that was protected is a change too.
func keepLiveMounts(spec []mount.Mount, declared []string, deployEnv map[string]string, live container.InspectResponse) ([]mount.Mount, []KeptMount) {
	liveMounts := liveMountsByTarget(live)
	if len(liveMounts) == 0 || len(spec) == 0 {
		return spec, nil
	}
	liveEnv := envMap(configEnv(live))
	hostSide := declaredHostByTarget(declared)

	out := make([]mount.Mount, len(spec))
	copy(out, spec)
	var kept []KeptMount
	for i, m := range out {
		target := normaliseMountTarget(m.Target)
		existing, ok := liveMounts[target]
		if !ok || existing.source == "" {
			continue
		}
		if movedOnPurpose(hostSide[target], deployEnv, liveEnv) {
			continue
		}
		sameSource := existing.source == m.Source
		protected := existing.readOnly && !m.ReadOnly
		if sameSource && !protected {
			continue
		}
		out[i] = mount.Mount{
			Type:     existing.mountType,
			Source:   existing.source,
			Target:   m.Target,
			ReadOnly: m.ReadOnly || existing.readOnly,
		}
		what := existing.source
		if sameSource {
			what = existing.source + " (read-only)"
		}
		kept = append(kept, KeptMount{Target: target, Kept: what, Requested: m.Source})
	}
	return out, kept
}

// liveMount is what a running container has at one target.
type liveMount struct {
	source    string
	mountType mount.Type
	readOnly  bool
}

// liveMountsByTarget indexes a container's mounts by their normalised container path.
// A named volume is keyed by its NAME (what a create takes), a bind by its host path.
func liveMountsByTarget(live container.InspectResponse) map[string]liveMount {
	requested := requestedSourcesByTarget(live.HostConfig)
	out := make(map[string]liveMount, len(live.Mounts))
	for _, m := range live.Mounts {
		target := normaliseMountTarget(m.Destination)
		if target == "" {
			continue
		}
		source := m.Source
		if m.Type == mount.TypeVolume {
			source = m.Name
		} else if asked := requested[target]; asked != "" {
			source = asked
		}
		if source == "" {
			continue
		}
		out[target] = liveMount{source: source, mountType: m.Type, readOnly: !m.RW}
	}
	return out
}

// requestedSourcesByTarget maps a container path to the host path the container was
// CREATED with.
//
// Docker Desktop and Podman Desktop run the daemon inside a Linux VM, and inspect's
// Mounts[].Source reports the path as that VM sees it: a folder the user typed as
// /Users/me/storj comes back as /host_mnt/Users/me/storj on macOS, and a Windows
// C:\storj comes back as /run/desktop/mnt/host/c/storj. Handing that path back to a
// create is at best a path the user never typed shown in the progress line, and at
// worst a create the daemon refuses ("mounts denied") — after the old container has
// already been stopped and removed, which leaves the service down with no container at
// all. HostConfig keeps what was actually asked for, on every platform, so that is what
// a replacement reuses.
//
// Only HostConfig.Mounts is read. Every container CashPilot creates is built from that
// field (see buildMounts), so a container it deployed is always covered. HostConfig.Binds
// is what a hand-written "docker run -v" fills in instead, and it is a single string
// whose separator is also a Windows drive letter's colon; guessing at that is how a
// source gets silently truncated, and a mount missing here simply falls back to
// Mounts[].Source, which is what ran before this existed.
func requestedSourcesByTarget(hostConfig *container.HostConfig) map[string]string {
	if hostConfig == nil {
		return nil
	}
	out := make(map[string]string, len(hostConfig.Mounts))
	for _, m := range hostConfig.Mounts {
		target := normaliseMountTarget(m.Target)
		if target == "" || m.Source == "" {
			continue
		}
		out[target] = m.Source
	}
	return out
}

// declaredHostByTarget maps a catalog volume's container path to its UNSUBSTITUTED host
// side, so the ${VAR} placeholders are still visible.
func declaredHostByTarget(declared []string) map[string]string {
	out := make(map[string]string, len(declared))
	for _, raw := range declared {
		parts := strings.Split(raw, ":")
		if len(parts) < 2 {
			continue
		}
		out[normaliseMountTarget(parts[1])] = parts[0]
	}
	return out
}

// movedOnPurpose reports whether the user relocated this mount on THIS deploy: the
// catalog's host side is built from a form field, and the value they submitted differs
// from the one the live container carries. A field the live container does not carry
// cannot be compared, and the safe answer there is "not moved" — keeping data beats
// guessing that a move was meant.
func movedOnPurpose(declaredHost string, deployEnv, liveEnv map[string]string) bool {
	for _, name := range substituteVarPattern.FindAllStringSubmatch(declaredHost, -1) {
		key := name[1]
		before, ok := liveEnv[key]
		if !ok {
			continue
		}
		if before != deployEnv[key] {
			return true
		}
	}
	return false
}

// configEnv reads a container's environment, tolerating an inspect response with no
// Config block.
func configEnv(live container.InspectResponse) []string {
	if live.Config == nil {
		return nil
	}
	return live.Config.Env
}

// envMap turns Docker's []string{"KEY=value"} into a map. An entry with no "=" is
// skipped.
func envMap(entries []string) map[string]string {
	out := make(map[string]string, len(entries))
	for _, entry := range entries {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		out[key] = value
	}
	return out
}
