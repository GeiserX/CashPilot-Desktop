// The guard reads macOS paths and commands; on Windows the package only has to
// build, and filepath would write these fixtures with backslashes.
//go:build !windows

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
	brFails   bool // bridge100 is listed but cannot be read
	cutFails  bool // deletem fails
	calls     []string
	dropCount int
	failCall  string // this call prints its normal output and still fails
	portGone  bool   // the recorded port no longer exists
	vmRuns    bool   // tart list shows the VM running
	stopFails bool   // tart stop leaves the VM running
	psCalls   int
	onPS      map[int]func(h *fakeHost) // runs before the n-th process read
}

const (
	testMAC  = "5a:94:ef:e4:0c:72"
	testGW   = "192.168.64.1"
	testBin  = "/Library/Application Support/CashPilot/SecureVM/bin"
	testPort = "vmenet0"
)

func (h *fakeHost) Run(ctx context.Context, name string, args ...string) (string, error) {
	call := strings.TrimSpace(name + " " + strings.Join(args, " "))
	h.calls = append(h.calls, call)
	out, err := h.run(call)
	if err == nil && call == h.failCall {
		return out, errors.New("fake host: " + call + " failed")
	}
	return out, err
}

func (h *fakeHost) run(call string) (string, error) {
	switch {
	case call == "/sbin/ifconfig -l":
		return "lo0 en0 bridge0 bridge100 vmenet0", nil
	case call == "/sbin/ifconfig bridge100":
		if h.brFails {
			return "", errors.New("ifconfig: ioctl failed")
		}
		out := "bridge100: flags=8a63<UP> mtu 1500\n\tinet " + testGW + " netmask 0xffffff00 broadcast 192.168.64.255\n"
		if h.member {
			out += "\tmember: vmenet0 flags=10803<LEARNING,DISCOVER,PRIVATE,CSUM>\n"
		}
		if h.cache {
			out += "\tAddress cache:\n\t    5a:94:ef:e4:c:72 Vlan1 vmenet0 1200 flags=0<>\n"
		}
		return out, nil
	case call == "/sbin/ifconfig vmenet0":
		if h.portGone {
			return "", errors.New("ifconfig: interface vmenet0 does not exist")
		}
		return "vmenet0: flags=8963<UP>", nil
	case strings.HasPrefix(call, "/bin/launchctl ") && strings.HasSuffix(call, " list --format json"):
		return fmt.Sprintf(`[{"Name":%q,"Running":%t}]`, VMName, h.vmRuns), nil
	case strings.HasPrefix(call, "/bin/launchctl ") && strings.HasSuffix(call, " stop "+VMName):
		if !h.stopFails {
			h.vmRuns = false
		}
		return "", nil
	case call == "/sbin/ifconfig bridge100 deletem vmenet0":
		if h.cutFails {
			return "", errors.New("ifconfig: deletem failed")
		}
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
		h.psCalls++
		if f := h.onPS[h.psCalls]; f != nil {
			f(h)
		}
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
	if err := WriteAtomic(filepath.Join(p.State, DenyFile), []byte(FormatDeny(deny)), 0o644); err != nil {
		t.Fatal(err)
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
	// The bridges may be read to find the port, but no pf or softnet read
	// may come before the cut.
	firstCheck := slices.IndexFunc(h.calls, func(c string) bool {
		return strings.HasPrefix(c, "/sbin/pfctl") || strings.HasPrefix(c, "/bin/ps")
	})
	if cut < 0 || firstCheck < 0 || add < 0 || cut >= firstCheck || firstCheck >= add {
		t.Fatalf("boot must cut first, then check, then connect: %v", h.calls)
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

func TestPassCutsWhenAPFReadFails(t *testing.T) {
	for what, call := range map[string]string{
		"info":         "/sbin/pfctl -s info",
		"main-ruleset": "/sbin/pfctl -sr",
		"rules":        "/sbin/pfctl -a " + Anchor + " -sr",
		"table":        "/sbin/pfctl -a " + Anchor + " -t cp_deny -T show",
	} {
		t.Run(what, func(t *testing.T) {
			d, h := guardFixture(t)
			h.member = false
			h.failCall = call
			st := d.Pass(context.Background(), false)
			if st.Network == NetConnected || h.member || !slices.Contains(st.Changes, "unread-"+what) {
				t.Fatalf("a failed %s read must keep the port cut: %+v %v", what, st, h.calls)
			}
		})
	}
}

func TestEnablePFSavesTheTokenFromStandardError(t *testing.T) {
	root := t.TempDir()
	p := PathsAt(root, filepath.Join(root, "plist"))
	if err := os.MkdirAll(p.State, 0o755); err != nil {
		t.Fatal(err)
	}
	// Exec returns only stdout on success, and pfctl -E prints its token on
	// stderr, so the token reaches the output only through the shell's 2>&1.
	d := &Daemon{P: p, R: runnerFunc(func(call string) (string, error) {
		switch call {
		case "/bin/sh -c /sbin/pfctl -E 2>&1":
			return "No ALTQ support in kernel\npf enabled\nToken : 4242\n", nil
		case "/sbin/pfctl -E", "/sbin/pfctl -s References":
			return "", nil
		case "/sbin/pfctl -s info":
			return "Status: Enabled for 0 days", nil
		}
		return "", fmt.Errorf("unexpected %q", call)
	})}
	if err := d.enablePF(context.Background()); err != nil {
		t.Fatal(err)
	}
	if tok, err := os.ReadFile(filepath.Join(p.State, PFToken)); err != nil || string(tok) != "4242\n" {
		t.Fatalf("the pf reference token was not saved: %q %v", tok, err)
	}
}

type runnerFunc func(call string) (string, error)

func (f runnerFunc) Run(_ context.Context, name string, args ...string) (string, error) {
	return f(strings.TrimSpace(name + " " + strings.Join(args, " ")))
}

func TestPassStopsAVMWhosePortIsLost(t *testing.T) {
	d, h := guardFixture(t)
	h.member, h.cache, h.portGone, h.vmRuns = false, false, true, true
	st := d.Pass(context.Background(), false)
	if st.Network != NetOff || h.vmRuns {
		t.Fatalf("a running VM with no port found must be stopped before it shows as off: %+v %v", st, h.calls)
	}

	d, h = guardFixture(t)
	h.member, h.cache, h.portGone, h.vmRuns, h.stopFails = false, false, true, true, true
	st = d.Pass(context.Background(), false)
	if st.Network == NetOff || st.Network == NetDisconnected || st.Fence != FenceUnknown {
		t.Fatalf("a VM that did not stop must not show as isolated: %+v", st)
	}
}

func TestPassCutsAPortPutBackDuringTheCheck(t *testing.T) {
	d, h := guardFixture(t)
	h.member = false // the start left the port cut
	// Between the check and the connect, something puts the port back and
	// the second read fails.
	h.onPS = map[int]func(*fakeHost){
		1: func(h *fakeHost) { h.member = true }, // after the first read's bridges
		2: func(h *fakeHost) { h.psFails = true },
	}
	st := d.Pass(context.Background(), false)
	if h.member || st.Network == NetConnected {
		t.Fatalf("a port put back during the check must be cut: %+v %v", st, h.calls)
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

// A new own IP changes deny.list; until a start applies it, the guest stays cut.
func TestPassCutsUntilTheDenyListIsApplied(t *testing.T) {
	d, h := guardFixture(t)
	more := []netip.Addr{netip.MustParseAddr("198.51.100.9"), netip.MustParseAddr("203.0.113.7")}
	if err := WriteAtomic(d.state(DenyFile), []byte(FormatDeny(more)), 0o644); err != nil {
		t.Fatal(err)
	}
	st := d.Pass(context.Background(), false)
	if st.Network == NetConnected || h.member || !slices.Contains(st.Changes, "own-ip") {
		t.Fatalf("a deny list the fence does not apply must keep the guest cut: %+v", st)
	}
}

// A bridge that is listed but cannot be read may hold the guest: fail closed
// on the port the start recorded, never report the guest as absent.
func TestPassFailsClosedOnAnUnreadableBridge(t *testing.T) {
	d, h := guardFixture(t)
	h.brFails = true
	st := d.Pass(context.Background(), false)
	if st.Network == NetOff || st.Network == NetConnected || st.Fence != FenceCut || !h.did("/sbin/ifconfig bridge100 deletem vmenet0") {
		t.Fatalf("an unreadable bridge must cut the recorded port: %+v", st)
	}
}

// A cut that fails is not reported as a cut.
func TestPassReportsAFailedCut(t *testing.T) {
	d, h := guardFixture(t)
	h.pfOn = false // drift
	h.cutFails = true
	h.vmRuns, h.stopFails = true, true // and the VM does not stop either
	st := d.Pass(context.Background(), false)
	if st.Network == NetDisconnected || !strings.Contains(st.Reason, "could not cut") {
		t.Fatalf("a failed cut must not say disconnected: %+v", st)
	}
}

// A rule moved to another interface passes the normalized compare, which
// drops interface names, so every rule must name the guest's bridge.
func TestPassCutsARuleOnAnotherBridge(t *testing.T) {
	d, h := guardFixture(t)
	h.member = false
	h.rules = strings.Replace(h.rules, " on bridge100 ", " on bridge101 ", 1)
	st := d.Pass(context.Background(), false)
	if st.Network == NetConnected || !slices.Contains(st.Changes, "wrong-bridge") {
		t.Fatalf("a rule on another bridge must count as drift: %+v", st)
	}
}

// A verdict that could not be cleared before a new proof connects nothing.
func TestPassIgnoresADistrustedVerdict(t *testing.T) {
	d, h := guardFixture(t)
	h.member = false
	d.distrust = true
	if st := d.Pass(context.Background(), false); st.Network == NetConnected || h.member {
		t.Fatalf("a distrusted verdict must not connect: %+v", st)
	}
}

func TestOtherNewMember(t *testing.T) {
	before := map[string]bool{"bridge100 vmenet0": true}
	after := map[string]bool{"bridge100 vmenet0": true, "bridge101 vmenet1": true}
	if got := OtherNewMember(before, after, "bridge101 vmenet1"); got != "" {
		t.Fatalf("only the cut port is new, got %q", got)
	}
	after["bridge102 vmenet2"] = true
	if got := OtherNewMember(before, after, "bridge101 vmenet1"); got != "bridge102 vmenet2" {
		t.Fatalf("a second new port must be found, got %q", got)
	}
}

// A symlinked parent folder must not lead a root delete out of the home.
func TestSafeOwnerPathRefusesASymlinkedParent(t *testing.T) {
	root := t.TempDir()
	home, outside := filepath.Join(root, "home"), filepath.Join(root, "outside")
	for _, dir := range []string{filepath.Join(home, "Library", "Caches"), filepath.Join(outside, "keep")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(home, "link")); err != nil {
		t.Fatal(err)
	}
	if safeOwnerPath(filepath.Join(home, "link", "keep"), home, "") {
		t.Fatal("a path through a symlink out of the home passed")
	}
	if !safeOwnerPath(filepath.Join(home, "Library", "Caches", "CashPilot"), home, "") {
		t.Fatal("a plain path inside the home was refused")
	}
}

// A new own IP that cannot be saved is not blocked yet: the guest stays cut,
// and the next pass asks again.
func TestPassCutsWhenANewOwnIPCannotBeSaved(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes into a read-only folder")
	}
	d, h := guardFixture(t)
	d.PublicIPs = func(context.Context) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("198.51.100.9")}, nil
	}
	if err := os.Chmod(d.P.State, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(d.P.State, 0o755)
	st := d.Pass(context.Background(), false)
	if st.Network == NetConnected || h.member || !slices.Contains(st.Changes, "own-ip") {
		t.Fatalf("an own IP that could not be saved must keep the guest cut: %+v", st)
	}
	if !d.lastOwnIP.IsZero() {
		t.Fatal("a failed save must make the next pass ask again")
	}
}

// Only a plain file that could be removed is a request, so an entry that
// stays never runs again at every poll.
func TestTakeRequestsOnlyRemovedPlainFiles(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root removes files from a read-only folder")
	}
	root := t.TempDir()
	d := &Daemon{P: PathsAt(root, filepath.Join(root, "plist"))}
	if err := os.MkdirAll(d.P.Request, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/hosts", filepath.Join(d.P.Request, RequestRestart)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.P.Request, RequestStart), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if got := d.takeRequests(); !slices.Equal(got, []string{RequestStart}) {
		t.Fatalf("only the plain file is a request, got %v", got)
	}
	// A request that cannot be removed was never claimed.
	if err := os.WriteFile(filepath.Join(d.P.Request, RequestProve), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(d.P.Request, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(d.P.Request, 0o755)
	for i := 0; i < 2; i++ {
		if got := d.takeRequests(); len(got) != 0 {
			t.Fatalf("poll %d ran a request it could not remove: %v", i, got)
		}
	}
}

// A retry needs less free space by what the restore image already holds.
func TestKeptIPSWCountsAPartialDownload(t *testing.T) {
	home := t.TempDir()
	if got := keptIPSW(home); got != 0 {
		t.Fatalf("nothing kept, got %d", got)
	}
	part := IPSWPath(home) + ".part"
	if err := os.MkdirAll(filepath.Dir(part), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(part, make([]byte, 4096), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := keptIPSW(home); got != 4096 {
		t.Fatalf("a 4096-byte partial, got %d", got)
	}
}

func TestRemoveWaitsForTheInstallLock(t *testing.T) {
	root := filepath.Join(t.TempDir(), "SecureVM")
	p := PathsAt(root, filepath.Join(t.TempDir(), "plist"))
	unlock, err := rootLock(filepath.Dir(root))
	if err != nil {
		t.Fatal(err)
	}
	d := &Daemon{P: p, R: runnerFunc(func(string) (string, error) { return "", errors.New("fake host: no commands") })}
	done := make(chan struct{})
	go func() { d.Remove(context.Background(), RemoveRequest{}); close(done) }()
	select {
	case <-done:
		unlock()
		t.Fatal("Remove ran while an install held the lock")
	case <-time.After(300 * time.Millisecond):
	}
	unlock()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Remove did not run after the lock was released")
	}
}
