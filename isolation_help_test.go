package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

func dockerService(slug, name string, volumes ...string) catalog.Service {
	svc := catalog.Service{Slug: slug, Name: name}
	svc.Docker.Image = "example/" + slug + ":1.0.0"
	svc.Docker.Volumes = volumes
	return svc
}

// A container earner reads on disk only what the deploy mounts. The help names the
// services that mount a folder of this computer, and only those.
func TestHostFolderServices(t *testing.T) {
	services := []catalog.Service{
		dockerService("honeygain", "Honeygain"),
		dockerService("earnapp", "EarnApp", "earnapp-data:/etc/earnapp"),
		dockerService("sia-hostd", "Sia (hostd)", "sia-hostd-data:/data", "${SIA_STORAGE_DIR}:/storage"),
		dockerService("storj", "Storj", "${IDENTITY_DIR}:/app/identity", "${STORAGE_DIR}:/app/config"),
		dockerService("presearch", "Presearch", "/data/presearch-node:/app/node"),
		{Slug: "grass", Name: "Grass"}, // no image: never deployed as a container
	}
	got := hostFolderServices(services)
	if want := []string{"Sia (hostd)", "Storj", "Presearch"}; !slices.Equal(got, want) {
		t.Fatalf("hostFolderServices = %v, want %v", got, want)
	}
	help := isolationHelp(services)
	if !strings.Contains(help, "for Sia (hostd), Storj and Presearch the folders you choose for them") {
		t.Errorf("help does not name the services that mount a folder: %q", help)
	}
	if !strings.Contains(help, "not fenced and can read what your account can") {
		t.Errorf("help does not say an app outside Docker is not fenced: %q", help)
	}
	if strings.Contains(help, "EarnApp") || strings.Contains(help, "Honeygain") {
		t.Errorf("help names a service that mounts no folder: %q", help)
	}
	plain := isolationHelp(services[:2])
	if !strings.Contains(plain, "none of your files") {
		t.Errorf("with no folder mounts the help must say no files are seen: %q", plain)
	}
}

// The real catalog: what the walk's Settings page would show.
func TestIsolationHelpNamesTheCatalogFolderServices(t *testing.T) {
	cat, err := catalog.LoadEmbedded(serviceFiles)
	if err != nil {
		t.Fatal(err)
	}
	got := hostFolderServices(cat.ListVisible())
	for _, name := range []string{"Storj", "Sia (hostd)"} {
		if !slices.Contains(got, name) {
			t.Errorf("%s mounts a folder of this computer but is not named: %v", name, got)
		}
	}
}
