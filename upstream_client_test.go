package main

import (
	"os"
	"path/filepath"
	stdruntime "runtime"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/services"
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
	// GetSettingsState/SaveSettings go through a.ready(), which requires the
	// whole graph -- cfg, catalog, store, runtime and services. Built here so
	// these tests drive the REAL settings path rather than a narrower helper
	// carved out to make them easy, which would leave the path users actually
	// hit untested.
	cat, err := catalog.LoadEmbedded(serviceFiles)
	if err != nil {
		t.Fatalf("catalog.LoadEmbedded error: %v", err)
	}
	rt := runtime.NewDockerProvider()
	app := &App{cfg: cfg, store: st, catalog: cat, runtime: rt}
	app.services = services.NewManager(rt, cat, st)
	return app
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

// --- pairing via the settings form -------------------------------------------

func TestPairingSettingsAreExposedToTheForm(t *testing.T) {
	// Pairing shipped with no way to configure it: the config field and keychain
	// storage existed, the loop ran, and nothing rendered an input. Working code
	// nobody can reach is the same defect as code nobody wired up.
	app := newPayloadTestApp(t, nil)
	state, err := app.GetSettingsState()
	if err != nil {
		t.Fatalf("GetSettingsState: %v", err)
	}
	keys := map[string]EnvSetting{}
	for _, e := range state.Environment {
		keys[e.Key] = e
	}
	url, ok := keys["CASHPILOT_UPSTREAM_URL"]
	if !ok {
		t.Fatal("no pairing URL field; the feature is unreachable from the UI")
	}
	if url.ReadOnly {
		t.Error("the pairing URL is read-only, so it cannot be set")
	}
	key, ok := keys["CASHPILOT_UPSTREAM_KEY"]
	if !ok {
		t.Fatal("no pairing key field")
	}
	if !key.Secret {
		t.Error("the pairing key is a bearer token and must be marked Secret")
	}
}

func TestTheFormExplainsThatStandaloneIsTheDefault(t *testing.T) {
	// A blank field with no explanation reads as "unconfigured", i.e. broken.
	// It is the supported default and loses nothing, and the help text has to
	// say so or users will pair just to make the warning go away.
	app := newPayloadTestApp(t, nil)
	state, _ := app.GetSettingsState()
	for _, e := range state.Environment {
		if e.Key == "CASHPILOT_UPSTREAM_URL" {
			if !strings.Contains(strings.ToLower(e.Help), "standalone") {
				t.Errorf("help does not mention standalone: %q", e.Help)
			}
			if !strings.Contains(strings.ToLower(e.Help), "empty") {
				t.Errorf("help does not say an empty value is valid: %q", e.Help)
			}
			return
		}
	}
	t.Fatal("field not found")
}

func TestSavingAPairingURLPersistsIt(t *testing.T) {
	app := newPayloadTestApp(t, nil)
	if _, err := app.SaveSettings(map[string]string{"upstreamUrl": "http://cashpilot.example:8080/"}); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	// The trailing slash is trimmed at the boundary so the client does not build
	// //api/workers/heartbeat, which some proxies treat as a different path.
	if got := app.cfg.Config().UpstreamURL; got != "http://cashpilot.example:8080" {
		t.Fatalf("UpstreamURL = %q", got)
	}
}

func TestClearingTheURLUnpairs(t *testing.T) {
	// Empty is MEANINGFUL here, unlike every other settings field: it is how a
	// user goes back to standalone. Keying this on non-emptiness -- the pattern
	// the surrounding fields use -- would make unpairing impossible.
	app := newPayloadTestApp(t, nil)
	if _, err := app.SaveSettings(map[string]string{"upstreamUrl": "http://a.example"}); err != nil {
		t.Fatalf("pair: %v", err)
	}
	if _, err := app.SaveSettings(map[string]string{"upstreamUrl": ""}); err != nil {
		t.Fatalf("unpair: %v", err)
	}
	if got := app.cfg.Config().UpstreamURL; got != "" {
		t.Fatalf("still paired with %q", got)
	}
}

func TestRepointingToAnotherServerDiscardsTheOldWorkerKey(t *testing.T) {
	// The per-worker key belongs to the server that issued it. Presenting it to
	// a DIFFERENT server fails authentication with no obvious cause, and keeping
	// it is a live secret for a machine the user has stopped talking to.
	app := newPayloadTestApp(t, nil)
	if _, err := app.SaveSettings(map[string]string{"upstreamUrl": "http://first.example"}); err != nil {
		t.Fatalf("pair: %v", err)
	}
	if err := config.SetUpstreamWorkerKey(app.cfg.AppDir(), "issued-by-first"); err != nil {
		t.Fatalf("seed key: %v", err)
	}
	if _, err := app.SaveSettings(map[string]string{"upstreamUrl": "http://second.example"}); err != nil {
		t.Fatalf("repair: %v", err)
	}
	got, err := config.UpstreamWorkerKey(app.cfg.AppDir())
	if err != nil {
		t.Fatalf("read key: %v", err)
	}
	if got != "" {
		t.Fatalf("the previous server's key survived a repoint: %q", got)
	}
}

func TestSavingTheSameURLKeepsTheWorkerKey(t *testing.T) {
	// The flip side: an unrelated settings save must not silently re-enrol this
	// machine by throwing away a key it is already authenticating with.
	app := newPayloadTestApp(t, nil)
	if _, err := app.SaveSettings(map[string]string{"upstreamUrl": "http://same.example"}); err != nil {
		t.Fatalf("pair: %v", err)
	}
	if err := config.SetUpstreamWorkerKey(app.cfg.AppDir(), "keep-me"); err != nil {
		t.Fatalf("seed key: %v", err)
	}
	if _, err := app.SaveSettings(map[string]string{"upstreamUrl": "http://same.example"}); err != nil {
		t.Fatalf("resave: %v", err)
	}
	got, _ := config.UpstreamWorkerKey(app.cfg.AppDir())
	if got != "keep-me" {
		t.Fatalf("worker key was discarded on an unrelated save: %q", got)
	}
}

func TestSavingThePairingKeyStoresItOutsideConfigJson(t *testing.T) {
	// It is a bearer token; config.json is not where it belongs.
	app := newPayloadTestApp(t, nil)
	if _, err := app.SaveSettings(map[string]string{"upstreamKey": "shared-enrolment-token"}); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}
	got, err := config.UpstreamEnrolmentKey(app.cfg.AppDir())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != "shared-enrolment-token" {
		t.Fatalf("enrolment key = %q", got)
	}
	raw, err := os.ReadFile(filepath.Join(app.cfg.AppDir(), "config.json"))
	if err == nil && strings.Contains(string(raw), "shared-enrolment-token") {
		t.Error("the pairing token was written into config.json")
	}
}
