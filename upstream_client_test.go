package main

import (
	stdruntime "runtime"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
	"github.com/GeiserX/CashPilot-Desktop/internal/upstream"
)

// newPayloadTestApp builds a minimal App over a temp config + store, seeded with
// the given deployments. Mirrors newFleetTestApp in fleet_server_test.go, which
// also supplies the keyring mock via TestMain.
func newPayloadTestApp(t *testing.T, deployments []store.Deployment) *App {
	t.Helper()
	t.Setenv("CASHPILOT_DESKTOP_DATA_DIR", t.TempDir())
	cfg, err := config.NewManager()
	if err != nil {
		t.Fatalf("config.NewManager error: %v", err)
	}
	st, err := store.Open(cfg.DataDir())
	if err != nil {
		t.Fatalf("store.Open error: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	for _, d := range deployments {
		if err := st.UpsertDeployment(d); err != nil {
			t.Fatalf("UpsertDeployment(%+v): %v", d, err)
		}
	}
	return &App{cfg: cfg, store: st}
}

// The payload is the whole contract with the CashPilot server, and getting a
// field name or shape wrong fails at the wire rather than at compile time --
// Desktop's own inbound workerHeartbeat types Apps as []string while the server
// declares it as a list of OBJECTS, so the two are easy to conflate.

func TestUpstreamPayloadCarriesASlugPerDeployment(t *testing.T) {
	// The server discovers what a worker runs via containers[*].slug. Without a
	// slug the machine appears on the fleet page running nothing, and its
	// platforms get no earnings attributed.
	app := newPayloadTestApp(t, []store.Deployment{
		{Slug: "earnapp", Name: "cashpilot-earnapp", Status: "running"},
		{Slug: "grass", Name: "cashpilot-grass", Status: "exited"},
	})
	p := app.upstreamPayload()
	if len(p.Containers) != 2 {
		t.Fatalf("containers = %d, want 2: %+v", len(p.Containers), p.Containers)
	}
	got := map[string]string{}
	for _, c := range p.Containers {
		if c.Slug == "" {
			t.Fatalf("a container went out with no slug: %+v", c)
		}
		got[c.Slug] = c.Status
	}
	if got["earnapp"] != "running" || got["grass"] != "exited" {
		t.Fatalf("status did not survive: %v", got)
	}
}

func TestUpstreamPayloadSkipsSluglessDeployments(t *testing.T) {
	// A slugless entry is not identifiable by the server, and sending it would
	// add an empty-slug member the earnings lookup would then try to match.
	app := newPayloadTestApp(t, []store.Deployment{
		{Slug: "", Name: "orphan", Status: "running"},
		{Slug: "  ", Name: "whitespace", Status: "running"},
		{Slug: "earnapp", Status: "running"},
	})
	p := app.upstreamPayload()
	if len(p.Containers) != 1 || p.Containers[0].Slug != "earnapp" {
		t.Fatalf("slugless deployments were not skipped: %+v", p.Containers)
	}
}

func TestUpstreamPayloadIdentifiesTheMachineStably(t *testing.T) {
	// client_id is what the server keys a worker on. Using the display name
	// would enrol a SECOND worker whenever the user renames the machine in
	// settings, splitting this device's history in two.
	app := newPayloadTestApp(t, nil)
	first := app.upstreamPayload()

	cfg := app.cfg.Config()
	cfg.HostnamePrefix = "renamed-in-settings"
	if err := app.cfg.Save(cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	second := app.upstreamPayload()

	if second.ClientID != first.ClientID {
		t.Fatalf("client_id changed on rename: %q -> %q", first.ClientID, second.ClientID)
	}
	// HostnamePrefix is the CONTAINER name prefix, not a machine name, and it
	// defaults to "cashpilot" -- an earlier draft used it here, which would have
	// reported every paired Desktop on every fleet as a worker called
	// "cashpilot". It must not influence the reported name at all.
	if second.Name != first.Name {
		t.Fatalf("the container-name prefix leaked into the worker name: %q -> %q", first.Name, second.Name)
	}
	if second.Name == "renamed-in-settings" || second.Name == "cashpilot" {
		t.Fatalf("worker name = %q, want the machine hostname", second.Name)
	}
}

func TestUpstreamPayloadFallsBackToTheHostnameForItsName(t *testing.T) {
	app := newPayloadTestApp(t, nil)
	p := app.upstreamPayload()
	if strings.TrimSpace(p.Name) == "" {
		t.Fatal("a worker with no name is rejected by the server")
	}
	if p.Name != p.SystemInfo.Hostname {
		t.Fatalf("name = %q, want the hostname %q when no prefix is set", p.Name, p.SystemInfo.Hostname)
	}
}

func TestUpstreamPayloadDeclaresItselfADesktop(t *testing.T) {
	// The server groups clients by device_type; without it a Desktop is
	// indistinguishable from a Docker worker on the fleet page.
	p := newPayloadTestApp(t, nil).upstreamPayload()
	if p.SystemInfo.DeviceType != "desktop" {
		t.Fatalf("device_type = %q", p.SystemInfo.DeviceType)
	}
	if p.SystemInfo.OS != stdruntime.GOOS || p.SystemInfo.Arch != stdruntime.GOARCH {
		t.Fatalf("os/arch not reported: %+v", p.SystemInfo)
	}
}

func TestUpstreamPayloadSendsNoVersionRatherThanAFakeOne(t *testing.T) {
	// This binary genuinely does not know its version -- wails.json has
	// productVersion but nothing injects it into Go. An ABSENT version reads as
	// unknown on the server; a made-up one would read as a match and hide a
	// stale install, which is exactly how Android devices sat on an old release
	// unnoticed. Omitted, not guessed.
	p := newPayloadTestApp(t, nil).upstreamPayload()
	if p.SystemInfo.Version != "" {
		t.Fatalf("version = %q, want empty until a real one is injected", p.SystemInfo.Version)
	}
}

func TestUpstreamPayloadNeverSendsANilSlice(t *testing.T) {
	// A nil slice marshals to null; the server declares these as lists and
	// pydantic rejects null for a list field with a 422.
	p := newPayloadTestApp(t, nil).upstreamPayload()
	if p.Containers == nil {
		t.Error("containers is nil, which marshals to null")
	}
	if p.Apps == nil {
		t.Error("apps is nil, which marshals to null")
	}
}

func TestStopUpstreamIsSafeWhenNeverStarted(t *testing.T) {
	// Shutdown calls this unconditionally, and standalone is the default -- so
	// the overwhelmingly common path is stopping something that never ran.
	app := newPayloadTestApp(t, nil)
	app.stopUpstream()
	app.stopUpstream()
}

func TestPairingIsOffByDefault(t *testing.T) {
	// The product rule: standalone loses nothing. An unconfigured Desktop must
	// not contact anything.
	app := newPayloadTestApp(t, nil)
	if got := strings.TrimSpace(app.cfg.Config().UpstreamURL); got != "" {
		t.Fatalf("UpstreamURL defaults to %q, want empty (standalone)", got)
	}
}

func TestAuthTokenAndKeyRulesAreTheSharedOnes(t *testing.T) {
	// Guards against the wiring growing its own copy of the enrolment rules,
	// which is how two implementations drift apart.
	if upstream.AuthToken("worker", "shared") != "worker" {
		t.Error("the per-worker key must win once issued")
	}
	if upstream.KeyToPersist("same", "same") != "" {
		t.Error("an unchanged key must not be rewritten")
	}
}
