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
	if got := StopTimeoutSeconds(storj); got != 300 {
		t.Fatalf("StopTimeoutSeconds(storj) = %d, want 300", got)
	}
	// An entry that says nothing gets the default, which matches the web worker.
	if got := StopTimeoutSeconds(catalog.Service{}); got != defaultStopTimeout {
		t.Fatalf("StopTimeoutSeconds(silent) = %d, want %d", got, defaultStopTimeout)
	}
	// A nonsense value is not a grace period.
	if got := StopTimeoutSeconds(catalog.Service{Docker: catalog.DockerConfig{StopTimeout: -5}}); got != defaultStopTimeout {
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

// THE RULE: the grace period a stop uses is the one the CATALOG asks for today, not
// the one the container happened to be built with.
//
// A Storj node deployed before this branch carries no stop timeout of its own. If the
// fallback were the shared 30 seconds, every stop, restart and remove of that node
// would SIGKILL it 270 seconds early — and the only way to get the 300 it asks for
// would be to redeploy it first, which is the one thing a user stopping a node is not
// doing.
func TestStopTimeoutForContainerPrefersTheCatalogThenTheContainer(t *testing.T) {
	// A container that predates the feature: nothing recorded on it, and the catalog
	// is the only thing that can say 300.
	bare := fakeInspectClient{response: container.InspectResponse{Config: &container.Config{}}}
	if got := stopTimeoutForContainer(context.Background(), bare, "storj", 300); got != 300 {
		t.Fatalf("an existing container got %d seconds, want the catalog's 300", got)
	}

	// The catalog still wins when the container carries an older, smaller number: an
	// entry raised to 300 has to reach the node already running.
	stale := fakeInspectClient{response: container.InspectResponse{
		Config: &container.Config{StopTimeout: intPtr(30)},
	}}
	if got := stopTimeoutForContainer(context.Background(), stale, "storj", 300); got != 300 {
		t.Fatalf("the catalog's 300 lost to the container's recorded 30 (got %d)", got)
	}

	// No catalog entry (the service was dropped): the container's own value is the
	// next best answer, and it beats the daemon's 10-second default.
	own := fakeInspectClient{response: container.InspectResponse{
		Config: &container.Config{StopTimeout: intPtr(300)},
	}}
	if got := stopTimeoutForContainer(context.Background(), own, "cashpilot-storj", 0); got != 300 {
		t.Fatalf("stopTimeoutForContainer = %d, want the container's own 300", got)
	}

	// Neither source can say anything: the shared default, never zero.
	if got := stopTimeoutForContainer(context.Background(), bare, "old", 0); got != defaultStopTimeout {
		t.Fatalf("an unset stop timeout produced %d, want %d", got, defaultStopTimeout)
	}

	// An unreachable daemon still has to produce a usable number.
	down := fakeInspectClient{err: errors.New("no such container")}
	if got := stopTimeoutForContainer(context.Background(), down, "gone", 0); got != defaultStopTimeout {
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

// THE RULE: a redeploy reuses the path the user typed, never the daemon's translation
// of it.
//
// Docker Desktop and Podman Desktop run the daemon in a Linux VM, and inspect's
// Mounts[].Source answers with the path as that VM sees it: /Users/sergio/storj comes
// back as /host_mnt/Users/sergio/storj on macOS, and C:\storj as
// /run/desktop/mnt/host/c/storj on Windows. Handing that back to a create either shows
// the user a path they never typed or is refused outright ("mounts denied") — and by
// then the old container has already been stopped and removed, so a Storj node is left
// down with no container at all. HostConfig records what was actually asked for.
//
// On a plain Linux daemon the two are the same string, which is why this cannot be
// caught by the integration test that runs here.
func TestKeepLiveMountsUsesTheRequestedPathNotTheVMPath(t *testing.T) {
	const typed = "/Users/sergio/storj"
	spec := []mount.Mount{{Type: mount.TypeBind, Source: typed, Target: "/app/identity"}}
	declared := []string{"${IDENTITY_DIR}:/app/identity"}
	live := container.InspectResponse{
		Config: &container.Config{Env: []string{"IDENTITY_DIR=" + typed}},
		// What Docker Desktop reports back.
		Mounts: []container.MountPoint{{
			Type: mount.TypeBind, Source: "/host_mnt/Users/sergio/storj", Destination: "/app/identity", RW: true,
		}},
		// What the container was actually created with.
		HostConfig: &container.HostConfig{
			Mounts: []mount.Mount{{Type: mount.TypeBind, Source: typed, Target: "/app/identity"}},
		},
	}

	got, kept := keepLiveMounts(spec, declared, map[string]string{"IDENTITY_DIR": typed}, live)
	if got[0].Source != typed {
		t.Fatalf("the redeploy would mount %q, the path inside the daemon's VM, not %q", got[0].Source, typed)
	}
	// Nothing moved, so nothing should be announced as kept somewhere else.
	if len(kept) != 0 {
		t.Fatalf("an unchanged mount was reported as moved: %+v", kept)
	}
}

// A mount the create did not ask for by name — someone ran "docker run -v" by hand —
// has nothing in HostConfig.Mounts to read, and still has to keep its data where it is.
func TestKeepLiveMountsFallsBackToTheReportedSource(t *testing.T) {
	spec := []mount.Mount{{Type: mount.TypeVolume, Source: "svc-data", Target: "/data"}}
	live := container.InspectResponse{
		Config: &container.Config{},
		Mounts: []container.MountPoint{{
			Type: mount.TypeBind, Source: "/home/sergio/by-hand", Destination: "/data", RW: true,
		}},
		HostConfig: &container.HostConfig{},
	}

	got, _ := keepLiveMounts(spec, []string{"svc-data:/data"}, map[string]string{}, live)
	if got[0].Source != "/home/sergio/by-hand" {
		t.Fatalf("a hand-made mount was moved onto the catalog's volume: %+v", got[0])
	}
}
