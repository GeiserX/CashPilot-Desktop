package main

import "testing"

// The Fleet page must not offer a worker snippet that cannot work. A fresh install
// is the case seen in the walk: hub off, API on 127.0.0.1.
func TestFleetReach(t *testing.T) {
	cases := []struct {
		name                                       string
		hubOn                                      bool
		listen, bind                               string
		port                                       int
		open, hubOff, thisPC, notListening, reload bool
	}{
		{name: "fresh install: hub off, loopback", listen: "127.0.0.1:8085", bind: "127.0.0.1", port: 8085, hubOff: true, thisPC: true},
		{name: "hub on but loopback", hubOn: true, listen: "127.0.0.1:8085", bind: "127.0.0.1", port: 8085, thisPC: true},
		{name: "hub off on every address", listen: "0.0.0.0:8085", bind: "0.0.0.0", port: 8085, hubOff: true},
		{name: "hub on, every address", hubOn: true, listen: "[::]:8085", bind: "0.0.0.0", port: 8085, open: true},
		{name: "hub on, one LAN address", hubOn: true, listen: "192.168.1.10:8085", bind: "192.168.1.10", port: 8085, open: true},
		{name: "hub on, IPv6 loopback", hubOn: true, listen: "[::1]:8085", bind: "::1", port: 8085, thisPC: true},
		{name: "API did not start", hubOn: true, listen: "", bind: "0.0.0.0", port: 8085, notListening: true},
		// Bind changes need a restart: the running listener is the truth.
		{name: "bind saved, not restarted", hubOn: true, listen: "127.0.0.1:8085", bind: "0.0.0.0", port: 8085, thisPC: true, reload: true},
		{name: "port saved, not restarted", hubOn: true, listen: "0.0.0.0:8085", bind: "0.0.0.0", port: 9000, open: true, reload: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := fleetReach(c.hubOn, c.listen, c.bind, c.port)
			if r.Open != c.open || r.HubOff != c.hubOff || r.ThisPCOnly != c.thisPC || r.NotListening != c.notListening || r.RestartPending != c.reload {
				t.Fatalf("fleetReach(%v, %q, %q, %d) = %+v", c.hubOn, c.listen, c.bind, c.port, r)
			}
			if r.Listen != c.listen {
				t.Fatalf("Listen = %q, want %q", r.Listen, c.listen)
			}
		})
	}
}

// The snippet's CASHPILOT_UI_URL names the address the API really answers on.
func TestFleetURLFor(t *testing.T) {
	lan := func() string { return "192.168.1.10" }
	none := func() string { return "" }
	cases := []struct {
		listen string
		lan    func() string
		want   string
	}{
		{"0.0.0.0:8085", lan, "http://192.168.1.10:8085"},
		{"[::]:8085", lan, "http://192.168.1.10:8085"},
		{"0.0.0.0:8085", none, "http://127.0.0.1:8085"},
		// A listener on one address answers only there, even if another LAN address exists.
		{"100.64.0.7:8085", lan, "http://100.64.0.7:8085"},
		{"127.0.0.1:8085", lan, "http://127.0.0.1:8085"},
		{"[fd00::5]:8085", lan, "http://[fd00::5]:8085"},
	}
	for _, c := range cases {
		if got := fleetURLFor(c.listen, c.lan); got != c.want {
			t.Errorf("fleetURLFor(%q) = %q, want %q", c.listen, got, c.want)
		}
	}
}

// The running app reads its reach from the live listener: the test app listens on
// loopback with the hub on, and the URL it gives workers says loopback, not a LAN
// address it does not answer on.
func TestFleetReachNowReadsTheLiveListener(t *testing.T) {
	app := newFleetTestApp(t, "tok")
	if r := app.fleetReachNow(); !r.NotListening || r.Open {
		t.Fatalf("no listener must read as not listening: %+v", r)
	}
	app.fleetAPI = &fleetAPIServer{addr: "127.0.0.1:8085"}
	if r := app.fleetReachNow(); r.Open || !r.ThisPCOnly || r.HubOff {
		t.Fatalf("a loopback listener must read as this PC only: %+v", r)
	}
	if got := app.fleetUIURL(); got != "http://127.0.0.1:8085" {
		t.Fatalf("UI URL must name the loopback address it listens on, got %q", got)
	}
	app.fleetAPI = &fleetAPIServer{addr: "192.168.1.10:8085"}
	if got := app.fleetUIURL(); got != "http://192.168.1.10:8085" {
		t.Fatalf("UI URL must name the address it listens on, got %q", got)
	}
	if r := app.fleetReachNow(); !r.Open || !r.RestartPending {
		t.Fatalf("a LAN listener with the hub on is open, and differs from the saved bind: %+v", r)
	}
}
