package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/services"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

// preflightTestApp is a real App over a temporary store and the vendored catalog,
// with the container runtime pointed at a fake daemon reporting arch.
func preflightTestApp(t *testing.T, arch string) *App {
	t.Helper()
	t.Setenv("CASHPILOT_DESKTOP_DATA_DIR", t.TempDir())

	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/version") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"Version":"27.3.1","ApiVersion":"1.47","Os":"linux","Arch":"`+arch+`"}`)
	}))
	t.Cleanup(daemon.Close)
	parsed, err := url.Parse(daemon.URL)
	if err != nil {
		t.Fatalf("parsing %q: %v", daemon.URL, err)
	}
	t.Setenv("DOCKER_HOST", "tcp://"+parsed.Host)
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	t.Setenv("DOCKER_API_VERSION", "")

	cfg, err := config.NewManager()
	if err != nil {
		t.Fatalf("config.NewManager error: %v", err)
	}
	st, err := store.Open(cfg.DataDir())
	if err != nil {
		t.Fatalf("store.Open error: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cat, err := catalog.LoadEmbedded(serviceFiles)
	if err != nil {
		t.Fatalf("catalog.LoadEmbedded error: %v", err)
	}
	provider := runtime.NewDockerProvider()
	return &App{
		cfg:      cfg,
		store:    st,
		catalog:  cat,
		runtime:  provider,
		services: services.NewManager(provider, cat, st),
		ctx:      context.Background(),
	}
}

// The preflight is only as good as what the binding hands it. Both of its inputs
// come from outside the package — the other machines in the fleet, and the CPU the
// container runtime actually runs on — and getting either wrong turns the whole
// report into a confident wrong answer with every unit test still green.
func TestPreflightServiceReadsTheFleetAndTheRuntimeArchitecture(t *testing.T) {
	app := preflightTestApp(t, "aarch64")
	if _, err := app.store.UpsertFleetDevice(store.FleetDevice{
		Name:     "attic-pi",
		Kind:     "worker",
		Status:   "online",
		Services: []string{"honeygain"},
	}); err != nil {
		t.Fatalf("UpsertFleetDevice error: %v", err)
	}

	report, err := app.PreflightService("honeygain")
	if err != nil {
		t.Fatalf("PreflightService error: %v", err)
	}

	// The architecture must be the one the DAEMON reported, not the one this test
	// binary was built for and not a constant.
	if report.MachineArch != "aarch64" {
		t.Fatalf("MachineArch = %q, want the daemon's own answer (aarch64)", report.MachineArch)
	}

	named := false
	for _, finding := range report.Findings {
		if strings.Contains(finding.Message, "attic-pi") {
			named = true
		}
	}
	if !named {
		t.Fatalf("the machine already running honeygain is not mentioned: %+v", report.Findings)
	}
}

// A slug that is not in the catalog has to say so. Returning an empty report would
// render as a clean bill of health for a service nobody can describe.
func TestPreflightServiceRejectsAnUnknownService(t *testing.T) {
	app := preflightTestApp(t, "x86_64")
	if _, err := app.PreflightService("not-a-service"); err == nil {
		t.Fatal("an unknown slug returned no error")
	}
}
