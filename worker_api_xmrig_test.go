package main

import (
	"net/http"
	"slices"
	"testing"
)

// A deploy the joined CashPilot sends runs the catalog's CPU cap wrapper for XMRig,
// exactly as the Deploy button does: the server names the service and its values,
// and the entrypoint comes from this computer's catalog untouched.
func TestServerDeployOfXMRigKeepsTheCPUCapWrapper(t *testing.T) {
	app := joinedApp(t)
	svc, ok := app.catalog.Get("xmrig")
	if !ok || len(svc.Docker.Entrypoint) != 4 {
		t.Fatalf("the xmrig entry with its CPU cap wrapper is not in the catalog: %q", svc.Docker.Entrypoint)
	}
	spec := map[string]any{
		"image": imageRepository(svc.Docker.Image) + ":6.27.0-r1",
		"env": map[string]string{
			"WALLET":      "44AFFq5kSiGBoZ4NMDwYtN18obc8AemS33DBLWs3H7otXft3XjrpDtQGv7SqSsaBYBb98uNbr2VBBEt7f2wfn3RVGQBEP3A",
			"POOL_URL":    "stratum+ssl://gulf.moneroocean.stream:20128",
			"WORKER_NAME": "rig1",
			"CPU_PERCENT": "17",
		},
		"instance": 1,
	}
	if w := call(t, app, http.MethodPost, "/api/containers/xmrig/deploy", testWorkerKey, spec); w.Code != http.StatusOK {
		t.Fatalf("deploy: %d %s", w.Code, w.Body.String())
	}
	c, ok := app.Docker.Container("cashpilot-xmrig")
	if !ok {
		t.Fatal("no cashpilot-xmrig container after the server's deploy")
	}
	if !slices.Equal(c.Entrypoint, svc.Docker.Entrypoint) {
		t.Errorf("entrypoint = %q\nwant the catalog's %q", c.Entrypoint, svc.Docker.Entrypoint)
	}
	if !slices.Contains(c.Cmd, "--cpu-max-threads-hint=17") || !slices.Contains(c.Env, "CPU_PERCENT=17") {
		t.Errorf("cmd = %q env = %q, want the server's CPU share in both", c.Cmd, c.Env)
	}
}
