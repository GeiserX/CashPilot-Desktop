package macvm

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeHost answers the commands the guard runs, from a small model of the
// host: one vmnet bridge, the guest's port on it or not, pf and softnet.
type fakeHost struct {
	member    bool   // vmenet0 is on bridge100
	cache     bool   // the bridge has learned the guest's MAC
	pfOn      bool   // pf is enabled
	mainRule  bool   // the main ruleset has the com.apple anchor
	rules     string // the anchor's rules, as pfctl -sr prints them
	table     string
	softnet   string // the softnet command line ("" = none)
	psFails   bool
	calls     []string
	dropCount int
}

const (
	testMAC  = "5a:94:ef:e4:0c:72"
	testGW   = "192.168.64.1"
	testBin  = "/Library/Application Support/CashPilot/SecureVM/bin"
	testPort = "vmenet0"
)

func (h *fakeHost) Run(_ context.Context, name string, args ...string) (string, error) {
	call := strings.TrimSpace(name + " " + strings.Join(args, " "))
	h.calls = append(h.calls, call)
	switch {
	case call == "/sbin/ifconfig -l":
		return "lo0 en0 bridge0 bridge100 vmenet0", nil
	case call == "/sbin/ifconfig bridge100":
		out := "bridge100: flags=8a63<UP> mtu 1500\n\tinet " + testGW + " netmask 0xffffff00 broadcast 192.168.64.255\n"
		if h.member {
			out += "\tmember: vmenet0 flags=10803<LEARNING,DISCOVER,PRIVATE,CSUM>\n"
		}
		if h.cache {
			out += "\tAddress cache:\n\t    5a:94:ef:e4:c:72 Vlan1 vmenet0 1200 flags=0<>\n"
		}
		return out, nil
	case call == "/sbin/ifconfig vmenet0":
		return "vmenet0: flags=8963<UP>", nil
	case call == "/sbin/ifconfig bridge100 deletem vmenet0":
		h.member = false
		return "", nil
	case call == "/sbin/ifconfig bridge100 addm vmenet0":
		h.member = true
		return "", nil
	case call == "/sbin/pfctl -s info":
		if h.pfOn {
			return "Status: Enabled for 0 days", nil
		}
		return "Status: Disabled", nil
	case call == "/sbin/pfctl -sr":
		if h.mainRule {
			return "scrub-anchor \"com.apple/*\" all fragment reassemble\nanchor \"com.apple/*\" all", nil
		}
		return "", nil
	case call == "/sbin/pfctl -a "+Anchor+" -sr":
		return h.rules, nil
	case call == "/sbin/pfctl -a "+Anchor+" -vsr":
		var b strings.Builder
		for _, l := range strings.Split(strings.TrimSpace(h.rules), "\n") {
			fmt.Fprintf(&b, "%s\n  [ Evaluations: 9  Packets: %d  Bytes: 0  States: 0 ]\n", l, h.dropCount)
		}
		return b.String(), nil
	case call == "/sbin/pfctl -a "+Anchor+" -t cp_deny -T show":
		return h.table, nil
	case call == "/bin/ps -axww -o args=":
		if h.psFails {
			return "", errors.New("ps failed")
		}
		return "/usr/bin/login\n" + h.softnet + "\n", nil
	}
	return "", fmt.Errorf("fake host: unexpected command %q", call)
}

func (h *fakeHost) did(call string) bool { return slices.Contains(h.calls, call) }

// guardFixture is a daemon with a proven fence that matches a healthy host.
func guardFixture(t *testing.T) (*Daemon, *fakeHost) {
	t.Helper()
	root := t.TempDir()
	p := PathsAt(root, filepath.Join(root, "plist"))
	p.Bin = testBin
	if err := os.MkdirAll(p.State, 0o755); err != nil {
		t.Fatal(err)
	}
	gw := netip.MustParseAddr(testGW)
	deny := []netip.Addr{netip.MustParseAddr("203.0.113.7")}
	raw, err := Rules("bridge100", gw, deny, nil)
	if err != nil {
		t.Fatal(err)
	}
	table := strings.Join(append(append([]string{}, FixedDeny...), "203.0.113.7"), "\n")
	block := SoftnetBlock(deny)
	h := &fakeHost{
		member: true, cache: true, pfOn: true, mainRule: true,
		rules: raw, table: table,
		softnet: filepath.Join(testBin, SoftnetExe) + " --vm-fd 3 --vm-mac-address " + testMAC + " --block " + block,
	}
	inst := Install{Owner: "earnerowner", OwnerUID: 501, TartHome: root, MAC: testMAC, Tart: TartPin.Version, Softnet: SoftnetPin.Version}
	desired := Desired{
		Bridge: "bridge100", Port: testPort, Gateway: gw, Subnet: netip.MustParsePrefix("192.168.64.0/24"),
		SoftnetBlock: block, Rules: NormalizeRules(raw), Table: NormalizeTable(table), StartedAt: time.Now(),
	}
	proven := Proven{At: time.Now(), HostBuild: "25G83", Tart: TartPin.Version, Softnet: SoftnetPin.Version, Green: true}
	for name, v := range map[string]any{InstallFile: inst, DesiredFile: desired, VerdictFile: proven} {
		if err := WriteJSON(filepath.Join(p.State, name), v, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	d := &Daemon{P: p, R: h, HostBuild: func(context.Context) (string, error) { return "25G83", nil }}
	return d, h
}

func TestPassConnectsOnlyAnExactProvenMatch(t *testing.T) {
	d, h := guardFixture(t)
	h.member = false // a start leaves the port cut
	st := d.Pass(context.Background(), false)
	if st.Network != NetConnected || st.Fence != FenceHeld || len(st.Changes) != 0 {
		t.Fatalf("an exact proven match must connect: %+v", st)
	}
	if !h.did("/sbin/ifconfig bridge100 addm vmenet0") {
		t.Fatalf("the port was not added back: %v", h.calls)
	}
	got, err := ReadStatus(d.P)
	if err != nil || got.Network != NetConnected {
		t.Fatalf("status.json was not written: %+v %v", got, err)
	}
}

func TestPassAtBootCutsBeforeItChecks(t *testing.T) {
	d, h := guardFixture(t)
	d.Pass(context.Background(), true)
	cut := slices.Index(h.calls, "/sbin/ifconfig bridge100 deletem vmenet0")
	add := slices.Index(h.calls, "/sbin/ifconfig bridge100 addm vmenet0")
	if cut < 0 || add < 0 || cut > add {
		t.Fatalf("boot must cut first, then connect after the check: %v", h.calls)
	}
}

func TestPassCutsOnEveryDrift(t *testing.T) {
	cases := map[string]func(h *fakeHost){
		"pf-off":       func(h *fakeHost) { h.pfOn = false },
		"main-ruleset": func(h *fakeHost) { h.mainRule = false },
		"rules": func(h *fakeHost) {
			h.rules = strings.Replace(h.rules, "block drop in quick log on bridge100 inet from any to <cp_deny>", "pass in quick on bridge100 inet from any to <cp_deny>", 1)
		},
		"table":         func(h *fakeHost) { h.table += "\n198.51.100.1" },
		"softnet":       func(h *fakeHost) { h.softnet += " --allow 192.168.1.0/24" },
		"wrong-gateway": func(h *fakeHost) { h.rules = strings.ReplaceAll(h.rules, " to "+testGW+" ", " to 192.168.64.9 ") },
	}
	for want, breakIt := range cases {
		t.Run(want, func(t *testing.T) {
			d, h := guardFixture(t)
			breakIt(h)
			st := d.Pass(context.Background(), false)
			if st.Network != NetDisconnected || st.Fence != FenceCut {
				t.Fatalf("drift %s must cut: %+v", want, st)
			}
			if !slices.Contains(st.Changes, want) {
				t.Fatalf("drift %s not reported: %v", want, st.Changes)
			}
			if !h.did("/sbin/ifconfig bridge100 deletem vmenet0") || h.member {
				t.Fatalf("drift %s left the port on the bridge: %v", want, h.calls)
			}
		})
	}
}

func TestPassTwoSoftnetsForOurMACIsDrift(t *testing.T) {
	d, h := guardFixture(t)
	h.softnet += "\n" + h.softnet
	if st := d.Pass(context.Background(), false); !slices.Contains(st.Changes, "softnet") || h.member {
		t.Fatalf("a second softnet for our MAC must cut: %+v", st)
	}
}

func TestPassNeedsAProofForThisHost(t *testing.T) {
	d, h := guardFixture(t)
	d.HostBuild = func(context.Context) (string, error) { return "25H1", nil }
	st := d.Pass(context.Background(), false)
	if st.Fence != FenceReprove || st.Network != NetDisconnected || h.member {
		t.Fatalf("a proof from another macOS build must not connect: %+v", st)
	}

	d, h = guardFixture(t)
	if err := os.Remove(filepath.Join(d.P.State, VerdictFile)); err != nil {
		t.Fatal(err)
	}
	st = d.Pass(context.Background(), false)
	if st.Fence != FenceUnproven || h.member {
		t.Fatalf("no proof must not connect: %+v", st)
	}
}

func TestPassFailsClosedWhenItCannotRead(t *testing.T) {
	d, h := guardFixture(t)
	h.psFails = true
	st := d.Pass(context.Background(), false)
	if st.Fence != FenceCut || h.member {
		t.Fatalf("an unreadable fence must cut: %+v %v", st, h.calls)
	}
}

func TestPassReportsDropsAndUnknownWhenNoRules(t *testing.T) {
	d, h := guardFixture(t)
	h.dropCount = 7
	st := d.Pass(context.Background(), false)
	if st.Blocked == nil || *st.Blocked == 0 {
		t.Fatalf("the drop counters were not read: %+v", st)
	}
	if n, ok := Blocked(""); ok || n != 0 {
		t.Fatalf("no rules must be unknown, got %d %v", n, ok)
	}
}

func TestPassWithoutInstallIsAbsent(t *testing.T) {
	d, _ := guardFixture(t)
	if err := os.Remove(filepath.Join(d.P.State, InstallFile)); err != nil {
		t.Fatal(err)
	}
	if st := d.Pass(context.Background(), false); st.Network != NetAbsent {
		t.Fatalf("no install must read absent: %+v", st)
	}
}

func TestSoftnetMatches(t *testing.T) {
	exe := filepath.Join(testBin, SoftnetExe)
	block := "@host,203.0.113.7/32"
	for args, want := range map[string]bool{
		exe + " --vm-mac-address " + testMAC + " --block " + block:                      true,
		"/opt/homebrew/bin/softnet --vm-mac-address " + testMAC + " --block " + block:   false,
		"/Library/Application Support/CashPilot/SecureVM/bin/softnet2 --block " + block: false,
		exe + "x --block " + block:                        false,
		exe + " --block @host":                            false,
		exe + " --block " + block + " --allow 10.0.0.0/8": false,
		exe + " --block " + block + " --expose 22:22":     false,
		exe + " --block " + block + " --control-fd 4":     false,
	} {
		if got := SoftnetMatches(args, testBin, block); got != want {
			t.Errorf("SoftnetMatches(%q) = %v, want %v", args, got, want)
		}
	}
}

type psRunner string

func (p psRunner) Run(context.Context, string, ...string) (string, error) { return string(p), nil }

func TestDevicesOffReadsTartThroughItsWrapper(t *testing.T) {
	tart := TartExe(testBin)
	wrapper := "/usr/bin/sudo -u owner -H /usr/bin/env -i PATH=" + testBin + " HOME=/Users/owner " + tart + " run " + VMName + " --vnc-experimental --no-audio --no-clipboard --net-softnet-block @host"
	own := tart + " run " + VMName + " --vnc-experimental --no-audio --no-clipboard --net-softnet-block @host"
	for name, c := range map[string]struct {
		ps string
		ok bool
	}{
		"ours, after its sudo":   {wrapper + "\n" + own, true},
		"another Tart":           {"/opt/homebrew/bin/tart run " + VMName + " --no-audio --no-clipboard", false},
		"audio on":               {own[:strings.Index(own, " --no-audio")] + " --no-clipboard x", false},
		"a shared folder":        {own + " --dir=/Users", false},
		"not running":            {"/sbin/launchd", false},
		"another VM of the name": {tart + " run " + VMName + "2 --no-audio --no-clipboard", false},
	} {
		d := &Daemon{P: Paths{Bin: testBin}, R: psRunner(c.ps)}
		if ok, why := d.devicesOff(context.Background(), Install{}); ok != c.ok {
			t.Errorf("%s: devicesOff = %v (%s), want %v", name, ok, why, c.ok)
		}
	}
}
