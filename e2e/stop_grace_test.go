package e2e

import (
	"slices"
	"testing"
)

// A Storj node is created with the catalog's stop grace as the container's own stop
// timeout, and with the catalog's entrypoint wrapper that gives the node the same
// grace inside the image's supervisord. Checked on what the daemon was sent.
func TestStorjIsCreatedWithItsStopGrace(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx()

	svc, ok := e.Catalog.Get("storj")
	if !ok {
		t.Fatal("storj is missing from the catalog")
	}
	if len(svc.Docker.Entrypoint) == 0 || svc.Docker.StopTimeout != 300 {
		t.Fatalf("the vendored storj entry lost its grace: entrypoint=%v stop_timeout=%d", svc.Docker.Entrypoint, svc.Docker.StopTimeout)
	}

	creds := map[string]string{
		"WALLET":       "0x0000000000000000000000000000000000000000",
		"EMAIL":        "someone@example.test",
		"ADDRESS":      "node.example.test:28967",
		"STORAGE":      "1TB",
		"IDENTITY_DIR": t.TempDir(),
		"STORAGE_DIR":  t.TempDir(),
	}
	if _, err := e.Manager.Deploy(ctx, "storj", creds); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	container, ok := e.Docker.Container("cashpilot-storj")
	if !ok {
		t.Fatal("the daemon has no cashpilot-storj container")
	}
	if container.StopTimeout == nil || *container.StopTimeout != 300 {
		t.Errorf("container stop timeout = %v, want the catalog's 300", container.StopTimeout)
	}
	if !slices.Equal(container.Entrypoint, svc.Docker.Entrypoint) {
		t.Errorf("entrypoint = %v, want the catalog's %v", container.Entrypoint, svc.Docker.Entrypoint)
	}
}

// An entry without an entrypoint keeps the image's, and still gets the runtime's
// default grace rather than the daemon's 10 seconds.
func TestAnEarnerWithoutAWrapperKeepsTheImagesEntrypoint(t *testing.T) {
	e := newEnv(t)
	if _, err := e.Manager.Deploy(e.ctx(), "honeygain", honeygainCreds); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	container, ok := e.Docker.Container("cashpilot-honeygain")
	if !ok {
		t.Fatal("the daemon has no cashpilot-honeygain container")
	}
	if container.Entrypoint != nil {
		t.Errorf("entrypoint = %v, want the image's own (none sent)", container.Entrypoint)
	}
	if container.StopTimeout == nil || *container.StopTimeout != 30 {
		t.Errorf("container stop timeout = %v, want the runtime's default 30", container.StopTimeout)
	}
}

// Mysterium's command now carries ${UI_ADDRESS}. Left blank, the variable's default
// must fill it, or the node starts with an empty --ui.address.
func TestMysteriumsUIAddressFallsBackToItsDefault(t *testing.T) {
	e := newEnv(t)
	if _, err := e.Manager.Deploy(e.ctx(), "mysterium", map[string]string{}); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	container, ok := e.Docker.Container("cashpilot-mysterium")
	if !ok {
		t.Fatal("the daemon has no cashpilot-mysterium container")
	}
	if !slices.Contains(container.Cmd, "--ui.address=127.0.0.1") {
		t.Errorf("command = %v, want --ui.address=127.0.0.1 from the default", container.Cmd)
	}
	if slices.ContainsFunc(container.Cmd, func(arg string) bool { return arg == "--ui.address=" }) {
		t.Errorf("the node was given an empty WebUI address: %v", container.Cmd)
	}
}
