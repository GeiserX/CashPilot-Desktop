package main

import (
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

// The Proxies section reads what the bindings return: a proxy without its secrets,
// each deployed service with why it cannot go behind a proxy, and a plain reason when
// this build has no tunnel image yet.
func TestProxiesStateNeverCarriesSecretsAndSaysWhatCannotRun(t *testing.T) {
	app := newAppUnderTest(t)
	t.Setenv("CASHPILOT_TUNNEL_IMAGE", "")
	for _, slug := range []string{"honeygain", "mysterium"} {
		if err := app.store.UpsertDeployment(store.Deployment{Slug: slug, Name: "cashpilot-" + slug, Status: "running", Runtime: "existing-docker"}); err != nil {
			t.Fatal(err)
		}
	}
	view, err := app.SaveProxy(0, map[string]string{"name": "Gate", "kind": "socks5", "host": "gate.example.net", "port": "7000", "username": "u-SECRET", "password": "p-SECRET"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.SaveProxy(0, map[string]string{"name": "Bad", "kind": "socks5", "host": "gate.example.net", "port": "0"}); err == nil {
		t.Fatal("a port of 0 is refused")
	}
	state, err := app.GetProxiesState()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Proxies) != 1 || state.Proxies[0].ID != view.ID || !state.Proxies[0].Secrets["password"] {
		t.Fatalf("proxies: %+v", state.Proxies)
	}
	refusals := map[string]string{}
	for _, c := range state.Candidates {
		refusals[c.Slug] = c.Refusal
	}
	if refusals["honeygain"] != "" {
		t.Fatalf("honeygain can run behind a proxy, got %q", refusals["honeygain"])
	}
	if !strings.Contains(refusals["mysterium"], "cannot") {
		t.Fatalf("mysterium needs the host's network and cannot, got %q", refusals["mysterium"])
	}
	if state.TunnelImage != "" || !strings.Contains(state.TunnelImageError, "CASHPILOT_TUNNEL_IMAGE") {
		t.Fatalf("with no pin the section says so: %+v", state)
	}
	if err := app.DeleteProxy(view.ID); err != nil {
		t.Fatal(err)
	}
}
