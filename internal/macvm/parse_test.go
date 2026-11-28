package macvm

import (
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/i18n"
	"github.com/GeiserX/CashPilot-Desktop/internal/securevm"
)

func TestParseBridge(t *testing.T) {
	out := "bridge100: flags=8a63<UP> mtu 1500\n" +
		"\tinet 192.168.64.1 netmask 0xffffff00 broadcast 192.168.64.255\n" +
		"\tmember: vmenet0 flags=10803<LEARNING>\n" +
		"\tmember: vmenet1 flags=10803<LEARNING>\n" +
		"\tAddress cache:\n" +
		"\t    5a:94:ef:e4:c:72 Vlan1 vmenet0 1200 flags=0<>\n"
	b := ParseBridge("bridge100", out)
	if b.Addr != netip.MustParseAddr("192.168.64.1") || b.Prefix != netip.MustParsePrefix("192.168.64.0/24") {
		t.Fatalf("address: %v %v", b.Addr, b.Prefix)
	}
	if !b.HasMember("vmenet1") || b.HasMember("vmenet2") {
		t.Fatalf("members: %v", b.Members)
	}
	if _, port, ok := GuestPort([]Bridge{b}, "5A:94:EF:E4:0C:72"); !ok || port != "vmenet0" {
		t.Fatalf("GuestPort: %q %v", port, ok)
	}
	if IsVMNetBridge("bridge0") || !IsVMNetBridge("bridge101") {
		t.Fatal("bridge0 is Thunderbolt's, bridge1NN is vmnet's")
	}
}

func TestRulesRefuseBadInput(t *testing.T) {
	gw := netip.MustParseAddr("192.168.64.1")
	if _, err := Rules("bridge0", gw, nil, nil); err == nil {
		t.Fatal("a non-vmnet bridge must be refused")
	}
	if _, err := Rules("bridge100", gw, []netip.Addr{netip.MustParseAddr("2001:db8::1")}, nil); err == nil {
		t.Fatal("an IPv6 deny address must be refused")
	}
	r, err := Rules("bridge100", gw, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	// The order is the fence: the holes, then the blocks, then the internet.
	hostGW := strings.Index(r, `"cp-host-gw"`)
	deny := strings.Index(r, `"cp-deny"`)
	internet := strings.Index(r, `"cp-internet"`)
	if hostGW < 0 || deny < 0 || internet < 0 || !(hostGW < internet && deny < internet) {
		t.Fatalf("the blocks must come before the internet pass:\n%s", r)
	}
	if strings.Contains(r, "cp-hole") {
		t.Fatal("production rules must have no proof hole")
	}
}

func TestRuleCountersAndBlocked(t *testing.T) {
	out := `block drop in quick log on bridge100 inet from any to 192.168.64.1 label "cp-host-gw"
  [ Evaluations: 10  Packets: 3  Bytes: 180  States: 0 ]
  [ Inserted: uid 0 pid 1 State Creations: 0 ]
block drop in quick on bridge100 inet6 all
  [ Evaluations: 10  Packets: 50  Bytes: 4000  States: 0 ]
block drop in quick log on bridge100 inet from any to <cp_deny> label "cp-deny"
  [ Evaluations: 10  Packets: 4  Bytes: 240  States: 0 ]
pass in quick on bridge100 inet all flags S/SA keep state label "cp-internet"
  [ Evaluations: 10  Packets: 900  Bytes: 9000  States: 2 ]`
	c := RuleCounters(out)
	if c["cp-host-gw"] != 3 || c["cp-deny"] != 4 || c["cp-internet"] != 900 {
		t.Fatalf("counters: %v", c)
	}
	if n, ok := Blocked(out); !ok || n != 7 {
		t.Fatalf("Blocked must sum the drop rules only: %d %v", n, ok)
	}
}

func TestDenyRoundTrip(t *testing.T) {
	in := []netip.Addr{netip.MustParseAddr("203.0.113.7"), netip.MustParseAddr("198.51.100.2"), netip.MustParseAddr("203.0.113.7")}
	got, err := ParseDeny(FormatDeny(in))
	if err != nil || len(got) != 2 {
		t.Fatalf("round trip: %v %v", got, err)
	}
	if b := SoftnetBlock(got); !strings.HasPrefix(b, "@host,") || !strings.Contains(b, "203.0.113.7/32") {
		t.Fatalf("softnet block: %s", b)
	}
}

func TestProofValid(t *testing.T) {
	inst := Install{Tart: "2.42.0", Softnet: "0.26.0"}
	pr := Proven{Green: true, HostBuild: "25G83", Tart: "2.42.0", Softnet: "0.26.0"}
	if ok, f, _ := ProofValid(pr, inst, "25G83"); !ok || f != FenceHeld {
		t.Fatal("a matching proof must be valid")
	}
	for name, mod := range map[string]func(*Proven, *Install, *string){
		"red":         func(p *Proven, _ *Install, _ *string) { p.Green = false },
		"host build":  func(_ *Proven, _ *Install, b *string) { *b = "25H1" },
		"unread host": func(_ *Proven, _ *Install, b *string) { *b = "" },
		"tart":        func(_ *Proven, i *Install, _ *string) { i.Tart = "2.43.0" },
		"softnet":     func(_ *Proven, i *Install, _ *string) { i.Softnet = "0.27.0" },
	} {
		p, i, b := pr, inst, "25G83"
		mod(&p, &i, &b)
		if ok, _, _ := ProofValid(p, i, b); ok {
			t.Errorf("%s: the proof must not be valid", name)
		}
	}
}

func TestKCPassword(t *testing.T) {
	// The XOR key is fixed, so XORing twice gives the padded password back.
	b := kcPassword("hunter2")
	if len(b) != 12 {
		t.Fatalf("padding to a multiple of 12: %d", len(b))
	}
	key := []byte{0x7D, 0x89, 0x52, 0x23, 0xD2, 0xBC, 0xDD, 0xEA, 0xA3, 0xB9, 0x1F}
	for i := range b {
		b[i] ^= key[i%len(key)]
	}
	if string(b[:7]) != "hunter2" || b[7] != 0 {
		t.Fatalf("decoded %q", b)
	}
	if len(kcPassword(strings.Repeat("a", 12))) != 24 {
		t.Fatal("a 12-byte password needs a full block of padding")
	}
}

func TestCountGuests(t *testing.T) {
	ps := `/sbin/launchd
/System/Library/Frameworks/Virtualization.framework/Versions/A/XPCServices/com.apple.Virtualization.VirtualMachine.xpc/Contents/MacOS/com.apple.Virtualization.VirtualMachine
/System/Library/PrivateFrameworks/AppleVirtualPlatform.framework/Versions/A/PlugIns/com.apple.AppleVirtualPlatform.Identity.Virtio.vzplugin/Contents/MacOS/com.apple.Virtualization.AppleVirtualPlatformIdentity
/System/Library/Frameworks/Virtualization.framework/Versions/A/XPCServices/com.apple.Virtualization.VirtualMachine.xpc/Contents/MacOS/com.apple.Virtualization.VirtualMachine`
	// Two VMs, one of them macOS: a Linux VM (Docker Desktop) has no identity process.
	if n := countGuests(ps); n != 1 {
		t.Fatalf("macOS guests: %d", n)
	}
}

func TestHostMajor(t *testing.T) {
	for in, want := range map[string]int{"26.6.1\n": 26, "15.7": 15, "": 0} {
		if got := hostMajor(in); got != want {
			t.Errorf("hostMajor(%q) = %d", in, got)
		}
	}
}

func TestSafeOwnerPath(t *testing.T) {
	// safeOwnerPath resolves the real parent folder, so the home is a real
	// folder of the test and not a path on the machine that runs it.
	root := t.TempDir()
	home := filepath.Join(root, "a")
	if err := os.MkdirAll(filepath.Join(home, ".tart", "vms"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !safeOwnerPath(filepath.Join(home, ".tart", "vms", "CashPilot-VM"), home, "") {
		t.Fatal("inside the home is safe")
	}
	for _, p := range []string{home, filepath.Join(root, "ab", "x"), home + "/../b/x", "/", "/Library"} {
		if safeOwnerPath(p, home, "") {
			t.Errorf("%s must not be deletable", p)
		}
	}
}

func TestLock(t *testing.T) {
	for _, c := range []struct {
		refused  string
		hasGuest bool
		ok       bool
		why      string
	}{
		{"", true, true, ""},
		{"terms", true, false, i18n.T("go.securevm.lockTerms")},
		{"unsigned", false, false, i18n.T("go.securevm.lockUnsigned")},
		{"", false, false, i18n.T("go.securevm.lockNoGuest")},
	} {
		ok, why := securevm.Lock(c.refused, c.hasGuest)
		if ok != c.ok || why != c.why {
			t.Errorf("Lock(%q, %v) = %v %q", c.refused, c.hasGuest, ok, why)
		}
	}
}

func TestErrLineIsOneShortLine(t *testing.T) {
	long := strings.Repeat("chown: /Users/earner/Library/x: Operation not permitted\n", 50) + "could not create the earner account\n"
	if got := errLine(long); got != "could not create the earner account" {
		t.Fatalf("the script's last line must win: %q", got)
	}
	if got := errLine(strings.Repeat("é", 400)); len([]rune(got)) != 301 {
		t.Fatalf("a long line is cut at 300 runes: %d", len([]rune(got)))
	}
}

// A status written before this boot must not show the VM as online.
func TestStatusFromBeforeBootIsDropped(t *testing.T) {
	boot := time.Date(2026, 11, 28, 0, 31, 58, 0, time.UTC)
	n := uint64(447)
	fresh := func() securevm.Status {
		return securevm.Status{Network: NetConnected, Fence: FenceHeld, Blocked: &n, StartError: "x"}
	}
	st := fresh()
	fromBeforeBoot(&st, boot.Add(-30*time.Second), boot)
	if st.Network != NetOff || st.Blocked != nil || st.StartError != "" || st.Reason != i18n.T("go.macvm.guardBooting") {
		t.Fatalf("a status from before the boot stayed: %+v", st)
	}
	if st.Fence != FenceHeld {
		t.Fatalf("the fence verdict changed: %q", st.Fence)
	}
	st = fresh()
	fromBeforeBoot(&st, boot.Add(time.Minute), boot)
	if st.Network != NetConnected || st.Blocked == nil || st.Reason != "" {
		t.Fatalf("a status from after the boot changed: %+v", st)
	}
}

func TestStagedNameDropsTheQuery(t *testing.T) {
	for in, want := range map[string]string{
		"https://example.com/dl/Theta%20Edge.dmg":          "Theta Edge.dmg",
		"https://example.com/dl/Wipter.pkg?channel=stable": "Wipter.pkg",
		"https://example.com/dl/UpRock.zip#latest":         "UpRock.zip",
	} {
		if got, err := stagedName(in); err != nil || got != want {
			t.Errorf("stagedName(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := stagedName("https://example.com/"); err == nil {
		t.Error("a URL with no file must be refused")
	}
}

func TestInstallResultNeedsACleanExit(t *testing.T) {
	if err := installResult("RESULT ok\n", nil, "Wipter"); err != nil {
		t.Fatalf("a clean ok must pass: %v", err)
	}
	if err := installResult("RESULT ok\n", errors.New("signal: killed"), "Wipter"); err == nil {
		t.Fatal("ok followed by a Tart error is not confirmed")
	}
	if err := installResult("RESULT refused: wrong team\n", errors.New("exit status 1"), "Wipter"); err == nil || !strings.Contains(err.Error(), "wrong team") {
		t.Fatalf("a refusal keeps its reason: %v", err)
	}
}
