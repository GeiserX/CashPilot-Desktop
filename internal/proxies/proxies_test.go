package proxies

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

func u(v uint64) *uint64   { return &v }
func f(v float64) *float64 { return &v }
func i(v int) *int         { return &v }

func TestCleanValidatesEachKind(t *testing.T) {
	good, err := Clean(map[string]string{"name": "Gate", "kind": "SOCKS5", "host": "gate.example.net", "port": "7000", "username": "u", "password": "p"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if good.Kind != "socks5" || good.Port != 7000 || good.CapThresholdPct != DefaultThresholdPct || good.CapGB != nil {
		t.Fatalf("unexpected definition: %+v", good)
	}
	bad := []map[string]string{
		{"kind": "socks5", "host": "h", "port": "1"},
		{"name": "x", "kind": "ftp", "host": "h", "port": "1"},
		{"name": "x", "kind": "http", "host": "h h", "port": "1"},
		{"name": "x", "kind": "http", "host": "h", "port": "70000"},
		{"name": "x", "kind": "http", "host": "h", "port": "1", "cap_gb": "-1"},
		{"name": "x", "kind": "http", "host": "h", "port": "1", "cap_threshold_pct": "5"},
		{"name": "x", "kind": "http", "host": "h", "port": "1", "reset_day": "31"},
		{"name": "x", "kind": "tailscale", "exit_node": "box"},
		{"name": "x", "kind": "tailscale", "auth_key": "k", "exit_node": "bad node"},
		{"name": "x", "kind": "tailscale", "auth_key": "k", "exit_node": "box", "login_server": "http://hs"},
	}
	for _, values := range bad {
		if _, err := Clean(values, nil); err == nil || !IsInputError(err) {
			t.Errorf("Clean(%v) = %v, want an input error", values, err)
		}
	}
}

func TestCleanKeepsAndClearsSecretsOnEdit(t *testing.T) {
	stored := Definition{Name: "Gate", Kind: "http", Host: "h", Port: 8080, Username: "user", Password: "pw", CapThresholdPct: 90}
	kept, err := Clean(map[string]string{"name": "Gate 2", "password": ""}, &stored)
	if err != nil {
		t.Fatal(err)
	}
	if kept.Username != "user" || kept.Password != "pw" || kept.Name != "Gate 2" || kept.Port != 8080 {
		t.Fatalf("a blank secret must keep the stored one: %+v", kept)
	}
	cleared, err := Clean(map[string]string{"clear_password": "true", "clear_username": "true"}, &stored)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Password != "" || cleared.Username != "" {
		t.Fatalf("clear_<field> must drop the stored secret: %+v", cleared)
	}
	ts, err := Clean(map[string]string{"kind": "tailscale", "auth_key": "tskey-1", "exit_node": "exit-node-a"}, &stored)
	if err != nil {
		t.Fatal(err)
	}
	if ts.Username != "" || ts.Password != "" || ts.Host != "" || ts.Port != 0 {
		t.Fatalf("a tailscale proxy keeps no host or login: %+v", ts)
	}
}

// A stored secret goes only to the server it was entered for (CashPilot #738).
func TestCleanRefusesToMoveAStoredSecretToANewServer(t *testing.T) {
	stored := Definition{Name: "Gate", Kind: "socks5", Host: "gate.example.net", Port: 7000, Username: "user", Password: "pw", CapThresholdPct: 90}
	for _, over := range []map[string]string{{"host": "evil.example.org"}, {"port": "1080"}} {
		_, err := Clean(over, &stored)
		if err == nil || !IsInputError(err) || !strings.Contains(err.Error(), "type the user and password again") {
			t.Errorf("Clean(%v) = %v, want a refusal that names the user and password", over, err)
		}
	}
	_, err := Clean(map[string]string{"host": "gate2.example.net", "username": "user"}, &stored)
	if err == nil || !strings.Contains(err.Error(), "type the password again") || strings.Contains(err.Error(), "user and") {
		t.Errorf("a retyped user still needs the password: %v", err)
	}
	moved, err := Clean(map[string]string{"host": "gate2.example.net", "username": "user", "password": "pw2"}, &stored)
	if err != nil || moved.Host != "gate2.example.net" || moved.Password != "pw2" || moved.Username != "user" {
		t.Fatalf("a move with the secrets typed again is saved: %+v %v", moved, err)
	}
	cleared, err := Clean(map[string]string{"host": "gate2.example.net", "clear_username": "true", "clear_password": "true"}, &stored)
	if err != nil || cleared.Username != "" || cleared.Password != "" {
		t.Fatalf("a move with the secrets removed is saved: %+v %v", cleared, err)
	}
	for _, over := range []map[string]string{{"name": "Renamed"}, {"host": "GATE.Example.NET"}, {"kind": "http"}, {"cap_gb": "5", "rotating": "true"}} {
		kept, err := Clean(over, &stored)
		if err != nil || kept.Username != "user" || kept.Password != "pw" {
			t.Errorf("Clean(%v) keeps the server, so it keeps the secret: %+v %v", over, kept, err)
		}
	}
	bare := Definition{Name: "Gate", Kind: "socks5", Host: "gate.example.net", Port: 7000, CapThresholdPct: 90}
	if d, err := Clean(map[string]string{"host": "gate2.example.net"}, &bare); err != nil || d.Host != "gate2.example.net" {
		t.Errorf("a proxy with no stored secret moves freely: %+v %v", d, err)
	}
}

func TestCleanNeedsTheAuthKeyAgainForANewLoginServer(t *testing.T) {
	stored := Definition{Name: "Exit", Kind: "tailscale", AuthKey: "tskey-1", ExitNode: "exit-node-a", CapThresholdPct: 90}
	_, err := Clean(map[string]string{"login_server": "https://headscale.example.org"}, &stored)
	if err == nil || !strings.Contains(err.Error(), "login server changed, so type the auth key again") {
		t.Fatalf("a new login server needs the auth key: %v", err)
	}
	moved, err := Clean(map[string]string{"login_server": "https://headscale.example.org", "auth_key": "tskey-2"}, &stored)
	if err != nil || moved.AuthKey != "tskey-2" {
		t.Fatalf("a new login server with a new key is saved: %+v %v", moved, err)
	}
	// The key goes to the control server whatever exit node is picked.
	renamed, err := Clean(map[string]string{"exit_node": "exit-node-b", "name": "Exit B"}, &stored)
	if err != nil || renamed.AuthKey != "tskey-1" {
		t.Fatalf("a new exit node keeps the key: %+v %v", renamed, err)
	}
	socks, err := Clean(map[string]string{"kind": "socks5", "host": "gate.example.net", "port": "7000"}, &stored)
	if err != nil || socks.AuthKey != "" {
		t.Fatalf("a kind change never carries the other kind's secret: %+v %v", socks, err)
	}
}

func TestPublicViewNeverCarriesSecrets(t *testing.T) {
	d := Definition{Name: "Gate", Kind: "socks5", Host: "h", Port: 1, Username: "SECRET-USER", Password: "SECRET-PW", AuthKey: "SECRET-KEY"} // ggignore: made-up test values
	raw, err := json.Marshal(PublicView(1, d, State{}, nil))
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"SECRET-USER", "SECRET-PW", "SECRET-KEY"} {
		if strings.Contains(string(raw), s) {
			t.Fatalf("view leaks %s: %s", s, raw)
		}
	}
	if !strings.Contains(string(raw), `"password":true`) {
		t.Fatalf("view must say the password is set: %s", raw)
	}
}

func TestRefusal(t *testing.T) {
	base := catalog.Service{Name: "Earner", Slug: "earner"}
	if r := Refusal(base); r != "" {
		t.Fatalf("a plain earner must be allowed, got %q", r)
	}
	cases := map[string]func(*catalog.Service){
		"host's network":    func(s *catalog.Service) { s.Docker.NetworkMode = "host" },
		"another container": func(s *catalog.Service) { s.Docker.NetworkMode = "container:x" },
		"publishes a port":  func(s *catalog.Service) { s.Docker.Ports = []string{"1:1"} },
		"LAN fence":         func(s *catalog.Service) { s.Docker.FenceGated = true },
		"volume":            func(s *catalog.Service) { s.Docker.Volumes = []string{"v:/d"} },
		"device":            func(s *catalog.Service) { s.Docker.Devices = []string{"/dev/net/tun"} },
		"privileged":        func(s *catalog.Service) { s.Docker.Privileged = true },
		"NET_RAW":           func(s *catalog.Service) { s.Docker.CapAdd = []string{"cap_net_raw", "SETUID"} },
		"terms forbid": func(s *catalog.Service) {
			s.Requirements.ProxyProhibited = &catalog.ProxyProhibited{Source: "https://example.com/tos"}
		},
	}
	for want, mutate := range cases {
		svc := base
		mutate(&svc)
		if r := Refusal(svc); !strings.Contains(r, want) {
			t.Errorf("Refusal = %q, want it to mention %q", r, want)
		}
	}
	// Ports published from the env are published ports too.
	for _, mutate := range []func(*catalog.Service){
		func(s *catalog.Service) { s.Docker.UIPort = &catalog.UIPort{Env: "A", Port: 1} },
		func(s *catalog.Service) { s.Docker.UDPPortRange = &catalog.UDPPortRange{Env: "B"} },
	} {
		svc := base
		mutate(&svc)
		if r := Refusal(svc); !strings.Contains(r, "publishes a port") {
			t.Errorf("Refusal = %q, want it to mention the published port", r)
		}
	}
	ok := base
	ok.Docker.CapAdd = []string{"SETUID"}
	if r := Refusal(ok); r != "" {
		t.Fatalf("a harmless capability must be allowed, got %q", r)
	}
}

func TestAccountCountsEveryByteOnceAndCapsOnce(t *testing.T) {
	d := Definition{Name: "Gate", CapGB: f(1), CapThresholdPct: 90}
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	s, crossed := Account(State{}, d, []Traffic{{TunnelID: "t1", StartedAt: "a", Rx: u(100), Tx: u(50)}}, now)
	if s.UsedBytes != 150 || crossed {
		t.Fatalf("first sighting counts everything: %d crossed=%v", s.UsedBytes, crossed)
	}
	s, _ = Account(s, d, []Traffic{{TunnelID: "t1", StartedAt: "a", Rx: u(300), Tx: u(60)}}, now.Add(time.Minute))
	if s.UsedBytes != 360 {
		t.Fatalf("same start counts the difference: %d", s.UsedBytes)
	}
	s, _ = Account(s, d, []Traffic{{TunnelID: "t1", StartedAt: "b", Rx: u(10), Tx: u(5)}}, now.Add(2*time.Minute))
	if s.UsedBytes != 375 {
		t.Fatalf("a restart counts from zero: %d", s.UsedBytes)
	}
	// A restart whose new counter is already above the old one still counts from
	// zero: the start time says it is a new counter, not 4 more bytes.
	r, _ := Account(State{Counters: map[string]Counter{"t9": {Rx: 300, Tx: 0, SeenAt: now.Unix(), StartedAt: "a"}}}, d, []Traffic{{TunnelID: "t9", StartedAt: "b", Rx: u(304), Tx: u(0)}}, now)
	if r.UsedBytes != 304 {
		t.Fatalf("a new start time is a new counter: %d", r.UsedBytes)
	}
	s, _ = Account(s, d, []Traffic{{TunnelID: "t1", StartedAt: "b", Rx: nil, Tx: u(999)}}, now.Add(3*time.Minute))
	if s.UsedBytes != 375 {
		t.Fatalf("an unknown counter is not zero and adds nothing: %d", s.UsedBytes)
	}
	s, crossed = Account(s, d, []Traffic{{TunnelID: "t1", StartedAt: "b", Rx: u(900_000_000), Tx: u(5), ExitIP: "203.0.113.9", ExitCountry: "NL"}}, now.Add(4*time.Minute))
	if !crossed || s.CappedAt == "" || !strings.Contains(s.CapReason, "90% of its 1 GB cap on 2026-11-04") {
		t.Fatalf("crossing 90%% of 1 GB must cap: crossed=%v %+v", crossed, s)
	}
	if s.LastExitIP != "203.0.113.9" || s.LastCountry != "NL" {
		t.Fatalf("a public exit is recorded: %+v", s)
	}
	_, crossed = Account(s, d, []Traffic{{TunnelID: "t1", StartedAt: "b", Rx: u(950_000_000), Tx: u(5)}}, now.Add(5*time.Minute))
	if crossed {
		t.Fatal("a cap already hit is not crossed again")
	}
	old, _ := Account(State{}, d, []Traffic{{TunnelID: "gone", Rx: u(1), Tx: u(1)}}, now)
	old, _ = Account(old, d, nil, now.Add(BaselineKeep+time.Hour))
	if _, ok := old.Counters["gone"]; ok {
		t.Fatal("a baseline unseen for a month is dropped")
	}
}

func TestCapChange(t *testing.T) {
	today := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	none := Definition{Name: "Gate", CapThresholdPct: 90}
	ten := Definition{Name: "Gate", CapGB: f(10), CapThresholdPct: 90}
	s, release := CapChange(none, ten, State{UsedBytes: 99 * GB}, today)
	if s.UsedBytes != 0 || s.PeriodStart != "2026-11-04" || release {
		t.Fatalf("a cap turned on starts a fresh period: %+v release=%v", s, release)
	}
	capped := State{UsedBytes: 9 * GB, CappedAt: "x", CapReason: "r"}
	s, release = CapChange(ten, none, capped, today)
	if !release || s.CappedAt != "" {
		t.Fatalf("a cap turned off lifts the stop: %+v", s)
	}
	s, release = CapChange(ten, Definition{Name: "Gate", CapGB: f(100), CapThresholdPct: 90}, capped, today)
	if !release || s.CappedAt != "" {
		t.Fatalf("a cap raised above use lifts the stop: %+v", s)
	}
	s, release = CapChange(ten, Definition{Name: "Gate", CapGB: f(5), CapThresholdPct: 90}, capped, today)
	if release || s.CappedAt == "" || !strings.Contains(s.CapReason, "5 GB") {
		t.Fatalf("still over: keep the stop with the new numbers: %+v", s)
	}
}

func TestResetDue(t *testing.T) {
	d := Definition{ResetDay: i(5)}
	cases := []struct {
		today, start string
		want         bool
	}{
		{"2026-11-04", "2026-10-05", false},
		{"2026-11-05", "2026-10-05", true},
		{"2026-11-05", "2026-11-05", false},
		{"2026-01-03", "2025-12-05", false},
		{"2026-01-03", "2025-12-04", true},
		{"2026-11-04", "", true},
	}
	for _, c := range cases {
		today, _ := time.Parse("2006-01-02", c.today)
		if got := ResetDue(d, State{PeriodStart: c.start}, today); got != c.want {
			t.Errorf("ResetDue(today %s, start %q) = %v, want %v", c.today, c.start, got, c.want)
		}
	}
	if ResetDue(Definition{}, State{}, time.Now()) {
		t.Fatal("no reset day, never due")
	}
}

func TestPublicIPAndSameExit(t *testing.T) {
	for _, ip := range []string{"10.0.0.1", "192.168.1.1", "100.64.1.1", "127.0.0.1", "fe80::1", "nope", ""} {
		if PublicIP(ip) != "" {
			t.Errorf("%q is not a public exit", ip)
		}
	}
	if PublicIP("203.0.113.7") != "203.0.113.7" {
		t.Fatal("a public address is kept")
	}
	others := []Exit{{Slug: "earner", ProxyID: 2, ExitIP: "203.0.113.7"}, {Slug: "earner", ProxyID: 3, ExitIP: "203.0.113.7", Rotating: true}, {Slug: "other", ProxyID: 4, ExitIP: "203.0.113.7"}}
	got := SameExit("earner", "203.0.113.7", "203.0.113.7", true, others, 1)
	if len(got) != 2 || got[0] != "this machine's own connection" || got[1] != "proxy 2" {
		t.Fatalf("SameExit = %v", got)
	}
	if SameExit("earner", "10.0.0.1", "", false, others, 1) != nil {
		t.Fatal("an unknown exit is never shared")
	}
}

// The view shows login_server back, so a URL with a user name or password in it would
// leak through it; a query or fragment has no business there either.
func TestALoginServerWithASecretPartIsRefused(t *testing.T) {
	for _, server := range []string{"https://u:p@hs.example", "https://@hs.example", "https://hs.example/?token=x", "https://hs.example/#f"} {
		values := map[string]string{"name": "T", "kind": "tailscale", "auth_key": "k", "exit_node": "box", "login_server": server}
		if _, err := Clean(values, nil); err == nil || !IsInputError(err) {
			t.Errorf("login server %q was accepted", server)
		}
	}
	d, err := Clean(map[string]string{"name": "T", "kind": "tailscale", "auth_key": "k", "exit_node": "box", "login_server": "https://hs.example:8443/x"}, nil)
	if err != nil || d.LoginServer != "https://hs.example:8443/x" {
		t.Fatalf("a plain login server: %+v %v", d, err)
	}
}

// A cap that rounds to zero bytes would stop every copy on the first tick.
func TestACapTooSmallToMeasureIsRefused(t *testing.T) {
	if _, err := Clean(map[string]string{"name": "x", "kind": "http", "host": "h", "port": "1", "cap_gb": "0.0000000001"}, nil); err == nil {
		t.Fatal("a cap of a fraction of a byte was accepted")
	}
	d, err := Clean(map[string]string{"name": "x", "kind": "http", "host": "h", "port": "1", "cap_gb": "0.001"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if th, _ := ThresholdBytes(d); th == 0 {
		t.Fatal("the smallest cap must still measure something")
	}
}

// Both ends of the range are refused with a message naming both bounds.
func TestACapOutOfRangeNamesBothBounds(t *testing.T) {
	for _, gb := range []string{"0.0000000001", "2000000"} {
		_, err := Clean(map[string]string{"name": "x", "kind": "http", "host": "h", "port": "1", "cap_gb": gb}, nil)
		if err == nil || !strings.Contains(err.Error(), "between 0.001 and 1,000,000 GB") {
			t.Fatalf("cap %s: %v", gb, err)
		}
	}
}
