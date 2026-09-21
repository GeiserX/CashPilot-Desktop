package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// THE RULE: a service gets the shutdown grace period its own entry asks for.
//
// The old 20 seconds was hardcoded, and Storj asks for 300 because a storage node has
// to flush before it goes. Twenty seconds in, it gets SIGKILL, and the next start pays
// for it with an unclean-shutdown recovery.
func TestStopTimeoutComesFromTheCatalog(t *testing.T) {
	storj := catalog.Service{Docker: catalog.DockerConfig{StopTimeout: 300}}
	if got := stopTimeoutSeconds(storj); got != 300 {
		t.Fatalf("stopTimeoutSeconds(storj) = %d, want 300", got)
	}
	// An entry that says nothing gets the default, which matches the web worker.
	if got := stopTimeoutSeconds(catalog.Service{}); got != defaultStopTimeout {
		t.Fatalf("stopTimeoutSeconds(silent) = %d, want %d", got, defaultStopTimeout)
	}
	// A nonsense value is not a grace period.
	if got := stopTimeoutSeconds(catalog.Service{Docker: catalog.DockerConfig{StopTimeout: -5}}); got != defaultStopTimeout {
		t.Fatalf("a negative stop_timeout produced %d", got)
	}
}

// fakeInspectClient answers ContainerInspect with a canned response or an error.
type fakeInspectClient struct {
	response container.InspectResponse
	err      error
}

func (f fakeInspectClient) ContainerInspect(context.Context, string, client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	if f.err != nil {
		return client.ContainerInspectResult{}, f.err
	}
	return client.ContainerInspectResult{Container: f.response}, nil
}

func intPtr(v int) *int { return &v }

// Stop and Restart read the grace period back off the container, so it works for a
// container this app deployed AND for one deployed before the value was recorded.
func TestStopTimeoutForContainerReadsItBackOffTheContainer(t *testing.T) {
	cli := fakeInspectClient{response: container.InspectResponse{
		Config: &container.Config{StopTimeout: intPtr(300)},
	}}
	if got := stopTimeoutForContainer(context.Background(), cli, "cashpilot-storj"); got != 300 {
		t.Fatalf("stopTimeoutForContainer = %d, want the container's own 300", got)
	}

	// A container from before this existed carries nothing, and must not fall to the
	// daemon's 10-second default by accident.
	bare := fakeInspectClient{response: container.InspectResponse{Config: &container.Config{}}}
	if got := stopTimeoutForContainer(context.Background(), bare, "old"); got != defaultStopTimeout {
		t.Fatalf("an unset stop timeout produced %d, want %d", got, defaultStopTimeout)
	}

	// An unreachable daemon still has to produce a usable number.
	down := fakeInspectClient{err: errors.New("no such container")}
	if got := stopTimeoutForContainer(context.Background(), down, "gone"); got != defaultStopTimeout {
		t.Fatalf("a failed inspect produced %d, want %d", got, defaultStopTimeout)
	}
}

// THE RULE: a redeploy keeps a service's data where it already is.
//
// Where a running container keeps its data is a fact; the spec is a guess. Seen live on
// the web side: a redeploy moved a Mysterium node off its bind-mounted directory onto
// the catalog's mysterium-data volume, and it came back as a DIFFERENT node — new
// identity, no reputation, earnings held against the old one stranded.
func TestKeepLiveMountsKeepsDataWhereItIs(t *testing.T) {
	spec := []mount.Mount{{Type: mount.TypeVolume, Source: "mysterium-data", Target: "/var/lib/mysterium-node"}}
	declared := []string{"mysterium-data:/var/lib/mysterium-node"}
	live := container.InspectResponse{
		Config: &container.Config{},
		Mounts: []container.MountPoint{{
			Type:        mount.TypeBind,
			Source:      "/home/sergio/myst",
			Destination: "/var/lib/mysterium-node",
			RW:          true,
		}},
	}

	got, kept := keepLiveMounts(spec, declared, map[string]string{}, live)
	if len(got) != 1 || got[0].Source != "/home/sergio/myst" || got[0].Type != mount.TypeBind {
		t.Fatalf("the redeploy moved the node's data: %+v", got)
	}
	if len(kept) != 1 || kept[0].Requested != "mysterium-data" || kept[0].Kept != "/home/sergio/myst" {
		t.Fatalf("the move was not reported: %+v", kept)
	}
}

// The one exception: the user typed a new path for it this deploy. Storj's identity
// directory is a form field, and a changed value is a move they asked for.
func TestKeepLiveMountsHonoursAMoveTheUserAskedFor(t *testing.T) {
	spec := []mount.Mount{{Type: mount.TypeBind, Source: "/mnt/new", Target: "/app/identity"}}
	declared := []string{"${IDENTITY_DIR}:/app/identity"}
	live := container.InspectResponse{
		Config: &container.Config{Env: []string{"IDENTITY_DIR=/mnt/old"}},
		Mounts: []container.MountPoint{{
			Type: mount.TypeBind, Source: "/mnt/old", Destination: "/app/identity", RW: true,
		}},
	}

	got, kept := keepLiveMounts(spec, declared, map[string]string{"IDENTITY_DIR": "/mnt/new"}, live)
	if got[0].Source != "/mnt/new" {
		t.Fatalf("a move the user typed was overridden: %+v", got)
	}
	if len(kept) != 0 {
		t.Fatalf("a deliberate move was reported as kept: %+v", kept)
	}

	// The same field UNCHANGED, with the container mounted somewhere else (deployed by
	// hand, or by another tool), keeps what is live: nothing asked for a move.
	same := keepLiveMountsSources(t, spec, declared, map[string]string{"IDENTITY_DIR": "/mnt/new"},
		container.InspectResponse{
			Config: &container.Config{Env: []string{"IDENTITY_DIR=/mnt/new"}},
			Mounts: []container.MountPoint{{Type: mount.TypeBind, Source: "/mnt/actual", Destination: "/app/identity", RW: true}},
		})
	if same[0] != "/mnt/actual" {
		t.Fatalf("an unchanged field did not keep the live mount: %v", same)
	}
}

// keepLiveMountsSources runs keepLiveMounts and returns just the sources, for the cases
// that only care where the data ended up.
func keepLiveMountsSources(t *testing.T, spec []mount.Mount, declared []string, env map[string]string, live container.InspectResponse) []string {
	t.Helper()
	got, _ := keepLiveMounts(spec, declared, env, live)
	out := make([]string, len(got))
	for i, m := range got {
		out[i] = m.Source
	}
	return out
}

// A mount that is read-only now stays read-only: a replacement that could write to data
// that was protected is a change too.
func TestKeepLiveMountsKeepsReadOnly(t *testing.T) {
	spec := []mount.Mount{{Type: mount.TypeBind, Source: "/data", Target: "/app/data"}}
	live := container.InspectResponse{
		Config: &container.Config{},
		Mounts: []container.MountPoint{{Type: mount.TypeBind, Source: "/data", Destination: "/app/data", RW: false}},
	}

	got, kept := keepLiveMounts(spec, []string{"/data:/app/data"}, map[string]string{}, live)
	if !got[0].ReadOnly {
		t.Fatalf("a read-only mount came back writable: %+v", got[0])
	}
	if len(kept) != 1 {
		t.Fatalf("the change was not reported: %+v", kept)
	}
}

// A target the live container does not have, and a spec that already matches, both pass
// straight through — the common case must not be rewritten.
func TestKeepLiveMountsLeavesMatchingAndNewMountsAlone(t *testing.T) {
	spec := []mount.Mount{
		{Type: mount.TypeVolume, Source: "svc-data", Target: "/data"},
		{Type: mount.TypeBind, Source: "/new", Target: "/extra"},
	}
	live := container.InspectResponse{
		Config: &container.Config{},
		Mounts: []container.MountPoint{{Type: mount.TypeVolume, Name: "svc-data", Destination: "/data", RW: true}},
	}

	got, kept := keepLiveMounts(spec, []string{"svc-data:/data", "/new:/extra"}, map[string]string{}, live)
	if got[0].Source != "svc-data" || got[1].Source != "/new" {
		t.Fatalf("an unchanged spec was rewritten: %+v", got)
	}
	if len(kept) != 0 {
		t.Fatalf("nothing changed, yet %d mounts were reported as kept", len(kept))
	}
}

// A first deploy has no live container at all.
func TestKeepLiveMountsWithNoLiveContainerIsTheSpec(t *testing.T) {
	spec := []mount.Mount{{Type: mount.TypeVolume, Source: "svc-data", Target: "/data"}}
	got, kept := keepLiveMounts(spec, []string{"svc-data:/data"}, map[string]string{}, container.InspectResponse{})
	if len(got) != 1 || got[0].Source != "svc-data" || len(kept) != 0 {
		t.Fatalf("a first deploy was altered: %+v %+v", got, kept)
	}
}

// A trailing slash on either side must not lose the match — the comparison that decides
// whether data moves cannot be an exact string compare.
func TestKeepLiveMountsNormalisesTargets(t *testing.T) {
	spec := []mount.Mount{{Type: mount.TypeVolume, Source: "svc-data", Target: "/data/"}}
	live := container.InspectResponse{
		Config: &container.Config{},
		Mounts: []container.MountPoint{{Type: mount.TypeBind, Source: "/elsewhere", Destination: "/data", RW: true}},
	}
	got, _ := keepLiveMounts(spec, []string{"svc-data:/data/"}, map[string]string{}, live)
	if got[0].Source != "/elsewhere" {
		t.Fatalf("a trailing slash lost the match: %+v", got[0])
	}
}
