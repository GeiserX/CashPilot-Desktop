package runtime

import (
	"archive/tar"
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/statebackup"
	"github.com/moby/moby/client"
)

// TestRestoreLive proves the restore on a real daemon: Docker writes into the volume
// of a container that was created and never started, the volume's root takes the
// backup's owner and mode, and the read-back matches the backup before the start.
//
// It pulls alpine and creates a container and a volume, so it runs only when asked:
// CASHPILOT_RESTORE_LIVE=1.
func TestRestoreLive(t *testing.T) {
	if os.Getenv("CASHPILOT_RESTORE_LIVE") != "1" {
		t.Skip("set CASHPILOT_RESTORE_LIVE=1 to restore into a real Docker daemon")
	}
	useAmbientDocker(t)
	const slug = "restore-live-test"
	svc := catalog.Service{Name: "Restore live test", Slug: slug, Docker: catalog.DockerConfig{
		Image:           "alpine:3.22",
		Command:         "sleep 600",
		NetworkMode:     "none",
		Volumes:         []string{"cashpilot-restore-live-data:/var/lib/keeper"},
		CriticalVolumes: []catalog.CriticalVolume{{Target: "/var/lib/keeper", Holds: "identity"}},
	}}
	cli, err := dockerClient()
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	cleanup := func() {
		_, _ = cli.ContainerRemove(context.Background(), containerName(slug), client.ContainerRemoveOptions{Force: true})
		_, _ = cli.VolumeRemove(context.Background(), "cashpilot-restore-live-data", client.VolumeRemoveOptions{Force: true})
	}
	cleanup()
	t.Cleanup(cleanup)

	at := time.Unix(1767225600, 0)
	long := "keystore/UTC--2026-01-01T00-00-00.000000000Z--" + strings.Repeat("0123456789abcdef", 5)
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for _, h := range []*tar.Header{
		{Typeflag: tar.TypeDir, Name: "keeper/", Mode: 0o700, Uid: 1000, Gid: 1000},
		{Typeflag: tar.TypeDir, Name: "keeper/keystore/", Mode: 0o750, Uid: 1000, Gid: 1000},
		{Typeflag: tar.TypeReg, Name: "keeper/" + long, Mode: 0o600, Uid: 1000, Gid: 1000, Size: 6},
		{Typeflag: tar.TypeSymlink, Name: "keeper/current", Linkname: long, Mode: 0o777},
	} {
		h.ModTime, h.Format = at, tar.FormatPAX
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Size > 0 {
			_, _ = w.Write([]byte("secret"))
		}
	}
	_ = w.Close()
	archives, err := statebackup.SplitArchives(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}

	p := NewDockerProvider()
	var held map[string]string
	released := false
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	res, err := p.RestoreState(ctx, RestoreRequest{
		Spec:     DeploySpec{Slug: slug, Service: svc},
		Archives: archives,
		Hold:     func(v map[string]string) error { held = v; return nil },
		Release:  func() error { released = true; return nil },
		Progress: func(m string) { t.Log(m) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Started || res.Files != 1 || !released || held["cashpilot-restore-live-data"] == "" {
		t.Fatalf("result %+v released %v held %v", res, released, held)
	}
	vol, err := cli.VolumeInspect(ctx, "cashpilot-restore-live-data", client.VolumeInspectOptions{})
	if err != nil || vol.Volume.Labels[LabelRestore] != held["cashpilot-restore-live-data"] {
		t.Fatalf("volume %+v, %v", vol.Volume.Labels, err)
	}
	// The running container sees the backup, owners and modes included.
	back, err := readBack(ctx, cli, res.ContainerID, "/var/lib/keeper")
	if err != nil {
		t.Fatal(err)
	}
	got, err := statebackup.TreeManifest(back)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := statebackup.TreeManifest(archives["keeper"])
	if diff := statebackup.ManifestDifferences(want, got); len(diff) > 0 {
		t.Fatalf("the running node does not hold the backup: %v", diff)
	}
	if root := got[statebackup.Root]; root.UID != 1000 || root.Mode != 0o700 {
		t.Fatalf("the volume's root is %+v, not the backup's owner and mode", root)
	}

	// A second restore of the same service refuses and touches nothing.
	if _, err := p.RestoreState(ctx, RestoreRequest{
		Spec: DeploySpec{Slug: slug, Service: svc}, Archives: archives,
		Hold:    func(map[string]string) error { t.Fatal("a refused restore recorded a hold"); return nil },
		Release: func() error { return nil },
	}); err == nil || !strings.Contains(err.Error(), "already has a container") {
		t.Fatalf("second restore: %v", err)
	}
}
