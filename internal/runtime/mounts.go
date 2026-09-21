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

// stopTimeoutSeconds is the grace period a service's own catalog entry asks for.
func stopTimeoutSeconds(svc catalog.Service) int {
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

// stopTimeoutForContainer reads back the grace period the container was created with.
//
// Deploy writes the catalog's stop_timeout into the container's own config, so the
// value travels with the container and Stop/Restart do not need the catalog to find
// it — which also means a container deployed before this existed, or by hand, still
// gets a sane answer instead of the daemon's 10-second default. An unreadable or
// unset value falls back to defaultStopTimeout.
func stopTimeoutForContainer(ctx context.Context, cli inspectClient, name string) int {
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
	liveMounts := liveMountsByTarget(live.Mounts)
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
func liveMountsByTarget(mounts []container.MountPoint) map[string]liveMount {
	out := make(map[string]liveMount, len(mounts))
	for _, m := range mounts {
		target := normaliseMountTarget(m.Destination)
		if target == "" {
			continue
		}
		source := m.Source
		if m.Type == mount.TypeVolume {
			source = m.Name
		}
		if source == "" {
			continue
		}
		out[target] = liveMount{source: source, mountType: m.Type, readOnly: !m.RW}
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
