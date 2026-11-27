package runtime

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/statebackup"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"
)

// fakeRestoreDaemon stands in for Docker in a restore: containers by name, volumes
// with their labels, and the archive written into each folder, which it reads back
// as Docker would (or as tamper says).
type fakeRestoreDaemon struct {
	containers map[string]*container.InspectResponse
	volumes    map[string]map[string]string
	written    map[string][]byte // destination folder -> archive
	tamper     func([]byte) []byte
	// onCreateVolume runs after a volume is made (a test replaces it behind the restore's back).
	onCreateVolume func(name string)
	calls          []string
	removeFails    bool
	// createLies names a volume Docker makes but whose answer never arrives: the
	// create returns an error although the volume is there with the restore's label.
	createLies string
}

func newFakeRestoreDaemon() *fakeRestoreDaemon {
	return &fakeRestoreDaemon{containers: map[string]*container.InspectResponse{}, volumes: map[string]map[string]string{}, written: map[string][]byte{}}
}

func (f *fakeRestoreDaemon) find(ref string) *container.InspectResponse {
	if c, ok := f.containers[ref]; ok {
		return c
	}
	for _, c := range f.containers {
		if c.ID == ref {
			return c
		}
	}
	return nil
}

func (f *fakeRestoreDaemon) ContainerInspect(_ context.Context, ref string, _ client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	if c := f.find(ref); c != nil {
		return client.ContainerInspectResult{Container: *c}, nil
	}
	return client.ContainerInspectResult{}, cerrdefs.ErrNotFound
}

func (f *fakeRestoreDaemon) ContainerList(_ context.Context, opts client.ContainerListOptions) (client.ContainerListResult, error) {
	var out []container.Summary
	for name, c := range f.containers {
		for filter := range opts.Filters["label"] {
			k, v, _ := strings.Cut(filter, "=")
			if c.Config != nil && c.Config.Labels[k] == v {
				out = append(out, container.Summary{ID: c.ID, Names: []string{"/" + name}, Labels: c.Config.Labels})
			}
		}
	}
	return client.ContainerListResult{Items: out}, nil
}

func (f *fakeRestoreDaemon) ContainerRemove(_ context.Context, ref string, _ client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	f.calls = append(f.calls, "container-rm")
	if f.removeFails {
		return client.ContainerRemoveResult{}, errors.New("device busy")
	}
	for name, c := range f.containers {
		if name == ref || c.ID == ref {
			delete(f.containers, name)
			return client.ContainerRemoveResult{}, nil
		}
	}
	return client.ContainerRemoveResult{}, cerrdefs.ErrNotFound
}

func (f *fakeRestoreDaemon) VolumeInspect(_ context.Context, name string, _ client.VolumeInspectOptions) (client.VolumeInspectResult, error) {
	labels, ok := f.volumes[name]
	if !ok {
		return client.VolumeInspectResult{}, cerrdefs.ErrNotFound
	}
	return client.VolumeInspectResult{Volume: volume.Volume{Name: name, Labels: labels}}, nil
}

func (f *fakeRestoreDaemon) VolumeCreate(_ context.Context, opts client.VolumeCreateOptions) (client.VolumeCreateResult, error) {
	f.calls = append(f.calls, "volume-create "+opts.Name)
	if _, ok := f.volumes[opts.Name]; !ok {
		f.volumes[opts.Name] = opts.Labels
	}
	if f.onCreateVolume != nil {
		f.onCreateVolume(opts.Name)
	}
	if opts.Name == f.createLies {
		return client.VolumeCreateResult{}, errors.New("unexpected EOF")
	}
	return client.VolumeCreateResult{Volume: volume.Volume{Name: opts.Name, Labels: f.volumes[opts.Name]}}, nil
}

func (f *fakeRestoreDaemon) VolumeRemove(_ context.Context, name string, _ client.VolumeRemoveOptions) (client.VolumeRemoveResult, error) {
	f.calls = append(f.calls, "volume-rm "+name)
	delete(f.volumes, name)
	return client.VolumeRemoveResult{}, nil
}

func (f *fakeRestoreDaemon) CopyToContainer(_ context.Context, _ string, opts client.CopyToContainerOptions) (client.CopyToContainerResult, error) {
	f.calls = append(f.calls, "write "+opts.DestinationPath)
	data, err := io.ReadAll(opts.Content)
	if err != nil {
		return client.CopyToContainerResult{}, err
	}
	f.written[opts.DestinationPath] = data
	return client.CopyToContainerResult{}, nil
}

func (f *fakeRestoreDaemon) CopyFromContainer(_ context.Context, _ string, opts client.CopyFromContainerOptions) (client.CopyFromContainerResult, error) {
	data := f.written[path.Dir(opts.SourcePath)]
	if f.tamper != nil {
		data = f.tamper(data)
	}
	return client.CopyFromContainerResult{Content: io.NopCloser(bytes.NewReader(data))}, nil
}

// keeper is a service shaped like Mysterium: one critical folder on a named volume.
func keeper(gated bool) catalog.Service {
	return catalog.Service{Name: "Keeper", Slug: "keeper", Docker: catalog.DockerConfig{
		Image:           "keeper/node:1.0.0",
		Volumes:         []string{"keeper-data:/var/lib/keeper", "keeper-config:/etc/keeper"},
		CriticalVolumes: []catalog.CriticalVolume{{Target: "/var/lib/keeper/", Holds: "identity"}},
		FenceGated:      gated,
	}}
}

// keeperArchive is the archive SplitArchives gives for the keeper folder.
func keeperArchive(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	at := time.Unix(1767225600, 0)
	for _, h := range []*tar.Header{
		{Typeflag: tar.TypeDir, Name: "keeper/", Mode: 0o700, Uid: 1000, Gid: 1000, ModTime: at},
		{Typeflag: tar.TypeReg, Name: "keeper/identity.key", Mode: 0o600, Uid: 1000, Gid: 1000, Size: 6, ModTime: at},
	} {
		h.Format = tar.FormatPAX
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			_, _ = w.Write([]byte("secret"))
		}
	}
	_ = w.Close()
	return buf.Bytes()
}

type restoreRun struct {
	daemon   *fakeRestoreDaemon
	p        *DockerProvider
	held     map[string]string
	holding  bool // the hold is on record now
	released bool
	created  bool
	started  bool
	startErr error
	// stoppedAtCreate is whether the fence guard was told to leave the node alone
	// while the container existed unchecked.
	stoppedAtCreate bool
}

func (r *restoreRun) restore(t *testing.T, svc catalog.Service, archives map[string][]byte) (RestoreResult, error) {
	t.Helper()
	req := RestoreRequest{
		Spec:     DeploySpec{Slug: svc.Slug, Service: svc},
		Archives: archives,
		Hold: func(v map[string]string) error {
			r.held, r.holding = v, true
			return nil
		},
		Release: func() error {
			r.released = true
			r.holding = false
			return nil
		},
	}
	steps := restoreSteps{
		create: func(context.Context) (ContainerInfo, error) {
			r.created = true
			stopped, _ := r.p.fence.userStopped()
			r.stoppedAtCreate = stopped[containerName(svc.Slug)]
			r.daemon.containers[containerName(svc.Slug)] = &container.InspectResponse{
				ID: "c1", Config: &container.Config{Labels: map[string]string{LabelManaged: "true", LabelRuntime: desktopRuntime, LabelService: svc.Slug}},
				State: &container.State{Status: container.StateCreated},
			}
			return ContainerInfo{ContainerID: "c1", Image: svc.Docker.Image}, nil
		},
		start: func(context.Context) error {
			if r.startErr != nil {
				return r.startErr
			}
			r.started = true
			return nil
		},
	}
	return r.p.restoreState(context.Background(), r.daemon, req, steps)
}

func newRestoreRun() *restoreRun {
	return &restoreRun{daemon: newFakeRestoreDaemon(), p: &DockerProvider{}}
}

func TestRestoreTargets(t *testing.T) {
	got, err := RestoreTargets(keeper(false))
	if err != nil || len(got) != 1 || got["/var/lib/keeper"] != "keeper-data" {
		t.Fatalf("got %v, %v", got, err)
	}
	storj := catalog.Service{Name: "Storj", Docker: catalog.DockerConfig{
		Volumes:         []string{"${IDENTITY_DIR}:/app/identity"},
		CriticalVolumes: []catalog.CriticalVolume{{Target: "/app/identity"}},
	}}
	if _, err := RestoreTargets(storj); err == nil || !strings.Contains(err.Error(), "host folder") {
		t.Fatalf("a host folder must be refused, got %v", err)
	}
	if _, err := RestoreTargets(catalog.Service{Name: "Plain"}); err == nil || !strings.Contains(err.Error(), "keeps nothing") {
		t.Fatalf("a service with nothing critical must be refused, got %v", err)
	}
}

// The whole path: volumes made new with the restore's label, the container created
// and not started, the backup written into the folder above the mount point, read
// back and compared, the hold lifted, and only then the start.
func TestRestoreWritesVerifiesThenStarts(t *testing.T) {
	r := newRestoreRun()
	res, err := r.restore(t, keeper(false), map[string][]byte{"keeper": keeperArchive(t)})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Started || res.Files != 1 || res.ContainerID != "c1" || len(res.Left) != 0 {
		t.Fatalf("result = %+v", res)
	}
	if r.held["keeper-data"] == "" || r.held["keeper-config"] == "" {
		t.Fatalf("the hold must name every volume the restore makes, with its label: %v", r.held)
	}
	if got := r.daemon.volumes["keeper-data"][LabelRestore]; got != r.held["keeper-data"] {
		t.Fatalf("keeper-data carries %q, the hold says %q", got, r.held["keeper-data"])
	}
	if _, ok := r.daemon.written["/var/lib"]; !ok {
		t.Fatalf("the backup must be written into the folder that holds the mount point: %v", r.daemon.calls)
	}
	if !r.released || !r.started {
		t.Fatalf("released=%v started=%v", r.released, r.started)
	}
}

// A read-back that is not the backup undoes everything: nothing starts, the hold is
// not lifted by the restore itself, and the container and the volumes it made go.
func TestRestoreUndoesWhenTheReadBackDiffers(t *testing.T) {
	r := newRestoreRun()
	r.daemon.tamper = func(b []byte) []byte { return bytes.Replace(b, []byte("secret"), []byte("SECRET"), 1) }
	res, err := r.restore(t, keeper(false), map[string][]byte{"keeper": keeperArchive(t)})
	if err == nil || !strings.Contains(err.Error(), "/var/lib/keeper does not read back as the backup: identity.key: different content") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(err.Error(), "The restore was undone") {
		t.Fatalf("err = %v", err)
	}
	if r.started || r.released {
		t.Fatalf("started=%v released=%v after a failed read-back", r.started, r.released)
	}
	if len(r.daemon.containers) != 0 || len(r.daemon.volumes) != 0 {
		t.Fatalf("left behind: containers %v volumes %v", r.daemon.containers, r.daemon.volumes)
	}
	if len(res.Left) != 0 {
		t.Fatalf("left = %v", res.Left)
	}
}

func TestRestoreRefusesWhenTheServiceOrItsVolumeIsThere(t *testing.T) {
	r := newRestoreRun()
	r.daemon.containers["cashpilot-keeper-p3"] = &container.InspectResponse{ID: "x", Config: &container.Config{Labels: map[string]string{LabelService: "keeper"}}}
	if _, err := r.restore(t, keeper(false), map[string][]byte{"keeper": keeperArchive(t)}); err == nil || !strings.Contains(err.Error(), "already has a container on this machine (cashpilot-keeper-p3)") {
		t.Fatalf("err = %v", err)
	}
	r = newRestoreRun()
	r.daemon.volumes["keeper-data"] = map[string]string{}
	if _, err := r.restore(t, keeper(false), map[string][]byte{"keeper": keeperArchive(t)}); err == nil || !strings.Contains(err.Error(), "the volume keeper-data already exists") {
		t.Fatalf("err = %v", err)
	}
	if r.held != nil || r.created || len(r.daemon.calls) != 0 {
		t.Fatalf("a refused restore made something: held=%v created=%v calls=%v", r.held, r.created, r.daemon.calls)
	}
	if r.daemon.volumes["keeper-data"] == nil {
		t.Fatal("the existing volume was touched")
	}
}

func TestRestoreRefusesABackupOfAnotherService(t *testing.T) {
	r := newRestoreRun()
	_, err := r.restore(t, keeper(false), map[string][]byte{"mysterium-node": keeperArchive(t)})
	if err == nil || !strings.Contains(err.Error(), "this backup holds mysterium-node, but Keeper keeps keeper. It is not a backup of Keeper") {
		t.Fatalf("err = %v", err)
	}
	if r.held != nil || len(r.daemon.calls) != 0 {
		t.Fatal("a foreign backup made something")
	}
}

// A fence-gated node is held from before its container exists until its data read
// back, so the fence guard never starts it half-written; a fence that is not ready
// leaves it waiting, not failed.
func TestRestoreHoldsAFenceGatedNodeUntilChecked(t *testing.T) {
	r := newRestoreRun()
	r.startErr = errors.New("LAN isolation is off here")
	res, err := r.restore(t, keeper(true), map[string][]byte{"keeper": keeperArchive(t)})
	if err != nil {
		t.Fatal(err)
	}
	if !r.stoppedAtCreate {
		t.Fatal("the fence guard was free to start the node while its data was unchecked")
	}
	if res.Started || !strings.Contains(res.Waiting, "LAN isolation is off") {
		t.Fatalf("result = %+v", res)
	}
	stopped, _ := r.p.fence.userStopped()
	if stopped["cashpilot-keeper"] {
		t.Fatal("a checked node must be left to the fence guard, not held")
	}

	// And a failed one gets its record back as it was.
	r = newRestoreRun()
	r.daemon.tamper = func([]byte) []byte { return nil }
	if _, err := r.restore(t, keeper(true), map[string][]byte{"keeper": keeperArchive(t)}); err == nil {
		t.Fatal("an empty read-back passed")
	}
	if stopped, _ := r.p.fence.userStopped(); stopped["cashpilot-keeper"] {
		t.Fatal("an undone restore left a stop record that was not there before")
	}
}

// A container the undo cannot remove stays held, and the error says how to clear it.
func TestRestoreReportsWhatTheUndoLeft(t *testing.T) {
	r := newRestoreRun()
	r.daemon.tamper = func([]byte) []byte { return nil }
	r.daemon.removeFails = true
	res, err := r.restore(t, keeper(true), map[string][]byte{"keeper": keeperArchive(t)})
	if err == nil || len(res.Left) == 0 || res.Left[0] != "cashpilot-keeper" {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	if !strings.Contains(err.Error(), "delete the partial restore under Settings") {
		t.Fatalf("err = %v", err)
	}
	if stopped, _ := r.p.fence.userStopped(); !stopped["cashpilot-keeper"] {
		t.Fatal("a half-written node that stayed must stay held from the fence guard")
	}
}

// A start that fails after the hold was lifted, with an undo that cannot remove the
// container, records the hold again: what stayed is checked data nobody started, and
// the Settings card must offer to delete it, as the error says.
func TestRestoreHoldsAgainWhenTheUndoLeavesSomethingAfterTheRelease(t *testing.T) {
	r := newRestoreRun()
	r.startErr = errors.New("port is already allocated")
	r.daemon.removeFails = true
	res, err := r.restore(t, keeper(false), map[string][]byte{"keeper": keeperArchive(t)})
	if err == nil || len(res.Left) == 0 || res.Left[0] != "cashpilot-keeper" {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	if !r.released {
		t.Fatal("the test needs the failure to come after the release")
	}
	if !r.holding || r.held["keeper-data"] == "" {
		t.Fatalf("the hold was not recorded again, held = %v; err = %v", r.held, err)
	}
	if !strings.Contains(err.Error(), "delete the partial restore under Settings") {
		t.Fatalf("err = %v", err)
	}

	// An undo that removed everything leaves no hold behind.
	r = newRestoreRun()
	r.startErr = errors.New("port is already allocated")
	if res, err := r.restore(t, keeper(false), map[string][]byte{"keeper": keeperArchive(t)}); err == nil || len(res.Left) != 0 {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
	if r.holding {
		t.Fatalf("a clean undo recorded a hold again: %v", r.held)
	}
}

// The undo deletes only a volume that still carries the restore's label: one another
// program put under the name is never the restore's to delete.
func TestRestoreUndoLeavesAVolumeAnotherProgramReplaced(t *testing.T) {
	r := newRestoreRun()
	r.daemon.onCreateVolume = func(name string) {
		if name == "keeper-data" {
			r.daemon.onCreateVolume = nil
			r.daemon.volumes["keeper-data"] = map[string]string{"owner": "someone else"}
		}
	}
	_, err := r.restore(t, keeper(false), map[string][]byte{"keeper": keeperArchive(t)})
	if err == nil || !strings.Contains(err.Error(), "already existed") {
		t.Fatalf("err = %v", err)
	}
	if r.daemon.volumes["keeper-data"]["owner"] != "someone else" {
		t.Fatal("another program's volume was deleted")
	}
}

// A volume create that errors may still have made the volume (the answer was cut
// off). The undo removes it when it carries the restore's label, so nothing is left
// behind that blocks the next restore while no hold lists it.
func TestRestoreUndoesAVolumeWhoseCreateErrored(t *testing.T) {
	r := newRestoreRun()
	r.daemon.createLies = "keeper-data"
	res, err := r.restore(t, keeper(false), map[string][]byte{"keeper": keeperArchive(t)})
	if err == nil || !strings.Contains(err.Error(), "could not create the volume keeper-data") {
		t.Fatalf("err = %v", err)
	}
	if _, ok := r.daemon.volumes["keeper-data"]; ok {
		t.Fatalf("the volume the errored create made was left behind: %v", r.daemon.volumes)
	}
	if len(res.Left) != 0 || r.created {
		t.Fatalf("res = %+v, created = %v", res, r.created)
	}
}

func TestDiscardRestoreRemovesOnlyWhatTheRestoreMade(t *testing.T) {
	d := newFakeRestoreDaemon()
	d.containers["cashpilot-keeper"] = &container.InspectResponse{ID: "c1", Config: &container.Config{Labels: map[string]string{LabelManaged: "true", LabelRuntime: desktopRuntime, LabelService: "keeper"}}, State: &container.State{Status: container.StateCreated}}
	d.volumes["keeper-data"] = map[string]string{LabelRestore: "n1"}
	d.volumes["keeper-config"] = map[string]string{LabelRestore: "other"}
	left, err := discardRestore(context.Background(), d, "keeper", map[string]string{"keeper-data": "n1", "keeper-config": "n2", "gone": "n3"})
	if err != nil || len(left) != 0 {
		t.Fatalf("left %v, err %v", left, err)
	}
	if len(d.containers) != 0 {
		t.Fatal("the restore's container stayed")
	}
	if _, ok := d.volumes["keeper-data"]; ok {
		t.Fatal("the restore's volume stayed")
	}
	if _, ok := d.volumes["keeper-config"]; !ok {
		t.Fatal("a volume without the restore's label was deleted")
	}
}

// What the restore writes is the backup, byte for byte in content and owners, once
// Docker has it: the manifest of the written archive is the backup's.
func TestRestoreWritesTheBackupsOwnersAndModes(t *testing.T) {
	r := newRestoreRun()
	archive := keeperArchive(t)
	if _, err := r.restore(t, keeper(false), map[string][]byte{"keeper": archive}); err != nil {
		t.Fatal(err)
	}
	want, _ := statebackup.TreeManifest(archive)
	got, _ := statebackup.TreeManifest(r.daemon.written["/var/lib"])
	if diff := statebackup.ManifestDifferences(want, got); len(diff) > 0 || got[statebackup.Root].UID != 1000 {
		t.Fatalf("diff %v, root %+v", diff, got[statebackup.Root])
	}
}
