package runtime

import (
	"slices"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// Values computed with the web app's copy_device_id (app/proxies.py), so a copy
// deployed from Desktop and one deployed from the web app through the same proxy are
// the same device.
func TestCopyDeviceIDMatchesTheWebApp(t *testing.T) {
	id := strings.Repeat("a3f9c2e1b4d5", 5) + "abcd"
	cases := []struct{ id, seed, want string }{
		{id, "p7", "3432bccd9712e29961e0b647bea26636744660cde90a743ad97f0420689e9bfb"},
		{strings.ToUpper(id), "p7", "087FB2BEDADD2181FA071C86E7FFD426237F306622B8FF59690A301DCAD9F602"},
		{id, "p12", "10277013478688998767c0d26ed4480c60fdde21e90235463319f786c0a96269"},
		{"abc", "p3", "e5c"},
		{"1234567890", "p2", "ca9210f04c"},
		{id, "2", "cf0ecaaa0e300509c26385f03c445733d09df783df4e52777803631c7d493e56"},
	}
	for _, c := range cases {
		got, err := copyDeviceID(c.id, c.seed, "")
		if err != nil || got != c.want {
			t.Errorf("copyDeviceID(%q, %q) = %q, %v; want %q", c.id, c.seed, got, err, c.want)
		}
	}
	for _, bad := range []string{"sdk-node-" + strings.Repeat("0", 32), "aBc1", ""} {
		if got, err := copyDeviceID(bad, "p1", ""); err == nil {
			t.Errorf("copyDeviceID(%q) = %q, want a refusal", bad, got)
		}
	}
	if _, err := copyDeviceID(id, "p7", "[0-9]{64}"); err == nil {
		t.Error("a derived id the catalog pattern refuses must refuse the copy")
	}
}

// proxyrackLike is ProxyRack's shape: the device UUID has no default, so the
// {hostname} rule never touched it, and every copy reused the original's.
var proxyrackLike = catalog.Service{Slug: "proxyrack", Name: "ProxyRack", Docker: catalog.DockerConfig{
	Image: "proxyrack/pop",
	Env: []catalog.EnvVar{
		{Key: "UUID", Required: true, PerDevice: true, Pattern: "[0-9A-F]{64}"},
		{Key: "DEVICE_NAME", Default: "cashpilot-{hostname}"},
		{Key: "API_KEY", Required: true, Secret: true},
	},
}}

var proxyrackUUID = strings.ToUpper(strings.Repeat("a3f9c2e1b4d5", 5) + "abcd")

func TestAProxiedCopyGetsItsOwnDeviceID(t *testing.T) {
	req := ProxiedDeploy{Slug: "proxyrack", ProxyID: 7, Service: proxyrackLike, Proxy: testSpec(), OwnIP: "192.0.2.1",
		Env: map[string]string{"UUID": proxyrackUUID, "DEVICE_NAME": "cashpilot-box-" + proxyrackUUID[:8], "API_KEY": "k"}}
	opts, err := earnerCreate(req, "sidecar-id", "proxyrack/pop:1", "linux")
	if err != nil {
		t.Fatal(err)
	}
	want := "087FB2BEDADD2181FA071C86E7FFD426237F306622B8FF59690A301DCAD9F602"
	if !slices.Contains(opts.Config.Env, "UUID="+want) {
		t.Fatalf("the copy's UUID is not its own: %v", opts.Config.Env)
	}
	for _, e := range opts.Config.Env {
		if strings.Contains(e, proxyrackUUID) {
			t.Fatalf("the original's UUID is still in the copy's env: %q", e)
		}
	}
	if !slices.Contains(opts.Config.Env, "API_KEY=k") {
		t.Fatalf("the account key must stay the same: %v", opts.Config.Env)
	}
	// Stable across redeploys: the same copy is the same device.
	again, _ := earnerCreate(req, "sidecar-id", "proxyrack/pop:1", "linux")
	first, second := slices.Sorted(slices.Values(opts.Config.Env)), slices.Sorted(slices.Values(again.Config.Env))
	if !slices.Equal(first, second) {
		t.Fatal("a redeploy of the copy changed its device id")
	}
}

func TestACopyOnTheBoxGetsItsOwnDeviceID(t *testing.T) {
	env, err := copyEnv(proxyrackLike, map[string]string{"UUID": proxyrackUUID, "API_KEY": "k"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if env["UUID"] == proxyrackUUID || len(env["UUID"]) != 64 || strings.ToUpper(env["UUID"]) != env["UUID"] {
		t.Fatalf("copy 2's UUID = %q, want its own 64 upper-case hex", env["UUID"])
	}
	first, _ := copyEnv(proxyrackLike, map[string]string{"UUID": proxyrackUUID}, 1)
	if first["UUID"] != proxyrackUUID {
		t.Fatal("copy 1 is the service itself and keeps its id")
	}
}

func TestACopyWhoseDeviceIDCannotBeDerivedIsRefused(t *testing.T) {
	earnapp := catalog.Service{Slug: "earnapp", Name: "EarnApp", Docker: catalog.DockerConfig{Image: "x", Env: []catalog.EnvVar{
		{Key: "EARNAPP_UUID", Required: true, PerDevice: true, Pattern: "sdk-node-[0-9a-f]{32}"},
	}}}
	req := ProxiedDeploy{Slug: "earnapp", ProxyID: 7, Service: earnapp, Proxy: testSpec(),
		Env: map[string]string{"EARNAPP_UUID": "sdk-node-" + strings.Repeat("0", 32)}}
	if _, err := earnerCreate(req, "sidecar-id", "x:1", "linux"); err == nil || !strings.Contains(err.Error(), "cannot derive") && !strings.Contains(err.Error(), "derive its own") {
		t.Fatalf("an id a copy cannot derive must refuse the proxied copy, got %v", err)
	}
	if _, err := copyEnv(earnapp, map[string]string{"EARNAPP_UUID": "sdk-node-" + strings.Repeat("0", 32)}, 2); err == nil {
		t.Fatal("an id a copy cannot derive must refuse copy 2 too")
	}
	missing := ProxiedDeploy{Slug: "proxyrack", ProxyID: 7, Service: proxyrackLike, Proxy: testSpec(), Env: map[string]string{"API_KEY": "k"}}
	if _, err := earnerCreate(missing, "sidecar-id", "x:1", "linux"); err == nil || !strings.Contains(err.Error(), "has no UUID") {
		t.Fatalf("a missing required device id must refuse the copy, got %v", err)
	}
}

// A service routed through a proxy from its first deploy is the service, not a copy:
// it keeps the device the deploy registers, so no second (phantom) device appears.
func TestARoutedServiceKeepsItsOwnDevice(t *testing.T) {
	name := "cashpilot-box-" + proxyrackUUID[:8]
	req := ProxiedDeploy{Slug: "proxyrack", ProxyID: 7, Service: proxyrackLike, Proxy: testSpec(), OwnIP: "192.0.2.1", Routed: true,
		Env: map[string]string{"UUID": proxyrackUUID, "DEVICE_NAME": name, "API_KEY": "k"}}
	opts, err := earnerCreate(req, "sidecar-id", "proxyrack/pop:1", "linux")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(opts.Config.Env, "UUID="+proxyrackUUID) || !slices.Contains(opts.Config.Env, "DEVICE_NAME="+name) {
		t.Fatalf("a routed service must run the device its deploy registered: %v", opts.Config.Env)
	}
}
