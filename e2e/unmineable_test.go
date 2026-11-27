package e2e

import (
	"slices"
	"strings"
	"testing"
)

// A deploy logs in with the catalog's referral code after the worker name, and
// keeps the XMRig CPU cap wrapper. Checked on what the daemon was sent.
func TestUnMineableDeployLogsInWithTheReferralCode(t *testing.T) {
	e := newEnv(t)
	svc, ok := e.Catalog.Get("unmineable")
	if !ok {
		t.Fatal("unmineable is missing from the catalog")
	}
	wallet := "0x" + strings.Repeat("ab", 20)
	creds := map[string]string{"PAYOUT_COIN": "SHIB", "WALLET": wallet, "WORKER_NAME": "rig-1", "CPU_PERCENT": "17"}
	if _, err := e.Manager.Deploy(e.ctx(), "unmineable", creds); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	c, ok := e.Docker.Container("cashpilot-unmineable")
	if !ok {
		t.Fatal("the daemon has no cashpilot-unmineable container")
	}
	if want := "--user=SHIB:" + wallet + ".rig-1#awk6-j1z0"; !slices.Contains(c.Cmd, want) {
		t.Errorf("cmd = %q, want %s", c.Cmd, want)
	}
	if !slices.Contains(c.Cmd, "--url=stratum+ssl://rx.unmineable.com:443") || !slices.Contains(c.Cmd, "--cpu-max-threads-hint=17") {
		t.Errorf("cmd = %q, want the fixed pool and the CPU hint", c.Cmd)
	}
	if !slices.Equal(c.Entrypoint, svc.Docker.Entrypoint) || len(c.Entrypoint) != 4 {
		t.Errorf("entrypoint = %q, want the catalog's CPU cap wrapper %q", c.Entrypoint, svc.Docker.Entrypoint)
	}
	if !slices.Contains(c.Env, "CPU_PERCENT=17") {
		t.Errorf("env = %q, want CPU_PERCENT=17 for the wrapper to read", c.Env)
	}
}
