package e2e

import (
	"slices"
	"testing"
)

// xmrigCreds is a public donation address (the Monero General Fund) and a CPU share
// that only the catalog's wrapper turns into a real cap.
var xmrigCreds = map[string]string{
	"WALLET":      "44AFFq5kSiGBoZ4NMDwYtN18obc8AemS33DBLWs3H7otXft3XjrpDtQGv7SqSsaBYBb98uNbr2VBBEt7f2wfn3RVGQBEP3A",
	"WORKER_NAME": "e2e",
	"CPU_PERCENT": "17",
}

// XMRig is created with the catalog's CPU cap wrapper exactly as the catalog states
// it: the script's own $ expressions are never filled from the deploy's values (the
// shell in the container reads CPU_PERCENT from the environment), and the command
// still carries the hint. Checked on what the daemon was sent.
func TestXMRigIsCreatedWithItsCPUCapWrapper(t *testing.T) {
	e := newEnv(t)
	svc, ok := e.Catalog.Get("xmrig")
	if !ok {
		t.Fatal("xmrig is missing from the catalog")
	}
	if len(svc.Docker.Entrypoint) != 4 {
		t.Fatalf("the vendored xmrig entry lost its CPU cap wrapper: %q", svc.Docker.Entrypoint)
	}
	if _, err := e.Manager.Deploy(e.ctx(), "xmrig", xmrigCreds); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	c, ok := e.Docker.Container("cashpilot-xmrig")
	if !ok {
		t.Fatal("the daemon has no cashpilot-xmrig container")
	}
	if !slices.Equal(c.Entrypoint, svc.Docker.Entrypoint) {
		t.Errorf("entrypoint = %q\nwant the catalog's %q", c.Entrypoint, svc.Docker.Entrypoint)
	}
	if !slices.Contains(c.Cmd, "--cpu-max-threads-hint=17") || !slices.Contains(c.Cmd, "--pass=e2e") {
		t.Errorf("cmd = %q, want the catalog command with the deploy's values", c.Cmd)
	}
	if !slices.Contains(c.Env, "CPU_PERCENT=17") {
		t.Errorf("env = %q, want CPU_PERCENT=17 for the wrapper to read", c.Env)
	}
}
