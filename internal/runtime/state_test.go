package runtime

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"
)

// fakeStateDaemon stands in for Docker: a container with mounts, archives per
// path, and a record of every pause, unpause and read.
type fakeStateDaemon struct {
	running    bool
	managed    bool
	mounts     []container.MountPoint
	volumes    map[string]string // name -> CreatedAt
	archives   map[string][]byte // path -> archive bytes
	unpauseErr error
	calls      []string
}

func (f *fakeStateDaemon) ContainerInspect(_ context.Context, _ string, _ client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	labels := map[string]string{}
	if f.managed {
		labels[LabelManaged] = "true"
	}
	return client.ContainerInspectResult{Container: container.InspectResponse{
		ID:     "c0ffee",
		State:  &container.State{Running: f.running},
		Config: &container.Config{Labels: labels},
		Mounts: f.mounts,
	}}, nil
}

func (f *fakeStateDaemon) VolumeInspect(_ context.Context, name string, _ client.VolumeInspectOptions) (client.VolumeInspectResult, error) {
	created, ok := f.volumes[name]
	if !ok {
		return client.VolumeInspectResult{}, errors.New("no such volume")
	}
	return client.VolumeInspectResult{Volume: volume.Volume{Name: name, CreatedAt: created}}, nil
}

func (f *fakeStateDaemon) ContainerPause(context.Context, string, client.ContainerPauseOptions) (client.ContainerPauseResult, error) {
	f.calls = append(f.calls, "pause")
	return client.ContainerPauseResult{}, nil
}

func (f *fakeStateDaemon) ContainerUnpause(context.Context, string, client.ContainerUnpauseOptions) (client.ContainerUnpauseResult, error) {
	f.calls = append(f.calls, "unpause")
	return client.ContainerUnpauseResult{}, f.unpauseErr
}

func (f *fakeStateDaemon) CopyFromContainer(_ context.Context, _ string, opts client.CopyFromContainerOptions) (client.CopyFromContainerResult, error) {
	f.calls = append(f.calls, "read "+opts.SourcePath)
	data, ok := f.archives[opts.SourcePath]
	if !ok {
		return client.CopyFromContainerResult{}, errors.New("no such path")
	}
	return client.CopyFromContainerResult{Content: io.NopCloser(bytes.NewReader(data))}, nil
}

func newFakeDaemon() *fakeStateDaemon {
	return &fakeStateDaemon{
		running: true,
		managed: true,
		archives: map[string][]byte{
			"/app/identity":           []byte("identity-archive"),
			"/var/lib/mysterium-node": []byte("keystore-archive"),
			"/var/lib/storage":        []byte("terabytes"),
		},
	}
}

// Only the critical targets are read, in order, between a pause and an unpause.
func TestReadStatePausesAndReadsOnlyTheTargets(t *testing.T) {
	d := newFakeDaemon()
	payload, read, err := readState(context.Background(), d, "mysterium", "cashpilot-mysterium",
		[]string{"/var/lib/mysterium-node/", "/app/identity"}, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "identity-archivekeystore-archive" {
		t.Errorf("payload = %q", payload)
	}
	if strings.Join(read, ",") != "/app/identity,/var/lib/mysterium-node" {
		t.Errorf("read = %v", read)
	}
	want := "pause,read /app/identity,read /var/lib/mysterium-node,unpause"
	if got := strings.Join(d.calls, ","); got != want {
		t.Errorf("calls = %s, want %s", got, want)
	}
}

func TestReadStateLeavesAStoppedContainerAlone(t *testing.T) {
	d := newFakeDaemon()
	d.running = false
	if _, _, err := readState(context.Background(), d, "s", "n", []string{"/app/identity"}, 1<<20); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(d.calls, ","); got != "read /app/identity" {
		t.Errorf("calls = %s: a stopped container must not be paused or unpaused", got)
	}
}

// A storage folder is refused, and the node is unpaused even so.
func TestReadStateRefusesTooMuchAndStillUnpauses(t *testing.T) {
	d := newFakeDaemon()
	_, _, err := readState(context.Background(), d, "storj", "n", []string{"/var/lib/storage"}, 4)
	if err == nil || !strings.Contains(err.Error(), "too big to export") {
		t.Fatalf("err = %v, want the size refusal", err)
	}
	if d.calls[len(d.calls)-1] != "unpause" {
		t.Errorf("calls = %v: the node was left paused", d.calls)
	}
}

// A good read next to a node that stays paused must not report success.
func TestReadStateReportsAFailedUnpause(t *testing.T) {
	d := newFakeDaemon()
	d.unpauseErr = errors.New("daemon hiccup")
	payload, _, err := readState(context.Background(), d, "s", "cashpilot-s", []string{"/app/identity"}, 1<<20)
	if err == nil || !strings.Contains(err.Error(), "docker unpause cashpilot-s") {
		t.Fatalf("err = %v, want the unpause failure", err)
	}
	if payload != nil {
		t.Errorf("a payload was returned with the node left paused")
	}
}

func TestReadStateRefusesAContainerItDidNotCreate(t *testing.T) {
	d := newFakeDaemon()
	d.managed = false
	if _, _, err := readState(context.Background(), d, "s", "n", []string{"/app/identity"}, 1<<20); err == nil {
		t.Fatal("read state from a container CashPilot does not manage")
	}
	if len(d.calls) != 0 {
		t.Errorf("calls = %v", d.calls)
	}
}

func TestStateFingerprintFollowsTheVolume(t *testing.T) {
	d := newFakeDaemon()
	d.mounts = []container.MountPoint{{Type: mount.TypeVolume, Name: "myst-data", Destination: "/var/lib/mysterium-node"}}
	d.volumes = map[string]string{"myst-data": "2026-01-01T00:00:00Z"}
	ctx := context.Background()
	first, err := stateFingerprint(ctx, d, "n", []string{"/var/lib/mysterium-node/"})
	if err != nil || first == "" {
		t.Fatalf("fingerprint = %q, %v", first, err)
	}
	d.volumes["myst-data"] = "2026-09-09T00:00:00Z"
	again, _ := stateFingerprint(ctx, d, "n", []string{"/var/lib/mysterium-node"})
	if again == first {
		t.Error("a recreated volume kept the fingerprint")
	}
	delete(d.volumes, "myst-data")
	if got, err := stateFingerprint(ctx, d, "n", []string{"/var/lib/mysterium-node"}); err == nil || got != "" {
		t.Errorf("an unreadable volume gave %q, %v; want unknown", got, err)
	}
}
