package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	goruntime "runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// firewallCalls records what the provider asked of the firewall seams.
type firewallCalls struct {
	ensured  [][]string // slug, program, then the networks, one per call
	removed  []string   // "slug program", one per call
	ensureFn func() error
	removeFn func() error
}

func (f *firewallCalls) install(p *NativeProcessProvider) {
	p.ensureFirewall = func(slug, program string, nets []string) error {
		f.ensured = append(f.ensured, append([]string{slug, program}, nets...))
		if f.ensureFn != nil {
			return f.ensureFn()
		}
		return nil
	}
	p.removeFirewall = func(slug, program string) error {
		f.removed = append(f.removed, slug+" "+program)
		if f.removeFn != nil {
			return f.removeFn()
		}
		return nil
	}
}

// TestNativeBinaryArgsComeFirstAndFirewallRuleFollowsTheService covers what the
// Windows Mysterium entry relies on: the binary's own args precede the shared
// command (so --userspace lands before the "service" subcommand), its firewall rule
// is requested for the extracted binary before every launch, and removing the
// service deletes the rule.
func TestNativeBinaryArgsComeFirstAndFirewallRuleFollowsTheService(t *testing.T) {
	p := newTestNativeProvider(t)
	var fw firewallCalls
	fw.install(p)
	art := tarGzArtifact(t, stubBinName(), stubBinaryBytes(t))
	url := serveArtifact(t, p, art)
	marker := filepath.Join(t.TempDir(), "marker")
	svc := nativeService("fwsvc", url, sha256hex(art), "tar.gz")
	svc.Native.Command = "--shared=1 service --agreed"
	svc.Native.Binaries[0].Args = "--userspace"
	svc.Native.Binaries[0].FirewallBlock = []string{"100.64.0.0/10", "169.254.0.0/16"}

	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: "fwsvc", Service: svc, Env: stubEnv(marker)}, nil); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	e := p.readRegistry().Entries["fwsvc"]
	if e == nil {
		t.Fatal("no registry entry after deploy")
	}
	if want := []string{"--userspace", "--shared=1", "service", "--agreed"}; !slices.Equal(e.Args, want) {
		t.Fatalf("args = %q, want %q", e.Args, want)
	}
	want := []string{"fwsvc", e.BinPath, "100.64.0.0/10", "169.254.0.0/16"}
	if len(fw.ensured) != 1 || !slices.Equal(fw.ensured[0], want) {
		t.Fatalf("firewall requests after deploy = %q, want one %q", fw.ensured, want)
	}

	// Start after a stop relaunches from the registry and checks the rule again.
	if err := p.Stop(context.Background(), "fwsvc", 0); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := p.Start(context.Background(), "fwsvc"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if len(fw.ensured) != 2 || !slices.Equal(fw.ensured[1], want) {
		t.Fatalf("firewall requests after start = %q, want a second %q", fw.ensured, want)
	}

	if err := p.Remove(context.Background(), "fwsvc", RemoveOptions{}); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if !slices.Equal(fw.removed, []string{"fwsvc " + e.BinPath}) {
		t.Fatalf("firewall removals = %q, want the rule for %s", fw.removed, e.BinPath)
	}
	if _, ok := p.readRegistry().Entries["fwsvc"]; ok {
		t.Fatal("registry entry survived Remove")
	}
}

// TestNativeRefusesToStartWithoutItsFirewallRule: when the rule cannot be put in
// place (the admin prompt was declined, the firewall is off), the binary must not run.
func TestNativeRefusesToStartWithoutItsFirewallRule(t *testing.T) {
	p := newTestNativeProvider(t)
	fw := firewallCalls{ensureFn: func() error { return errors.New("the admin prompt was declined") }}
	fw.install(p)
	art := tarGzArtifact(t, stubBinName(), stubBinaryBytes(t))
	url := serveArtifact(t, p, art)
	marker := filepath.Join(t.TempDir(), "marker")
	svc := nativeService("fwsvc", url, sha256hex(art), "tar.gz")
	svc.Native.Binaries[0].FirewallBlock = []string{"100.64.0.0/10"}

	_, err := p.Deploy(context.Background(), DeploySpec{Slug: "fwsvc", Service: svc, Env: stubEnv(marker)}, nil)
	if err == nil || !strings.Contains(err.Error(), "declined") {
		t.Fatalf("Deploy err = %v, want the firewall error", err)
	}
	time.Sleep(300 * time.Millisecond)
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("the binary ran without its firewall rule")
	}
	if _, ok := p.readRegistry().Entries["fwsvc"]; ok {
		t.Fatal("a refused launch was recorded as deployed")
	}
}

// TestNativeRemoveKeepsTheEntryWhileTheRuleStays: a rule that could not be deleted
// must stay known, so the next Remove tries again.
func TestNativeRemoveKeepsTheEntryWhileTheRuleStays(t *testing.T) {
	p := newTestNativeProvider(t)
	fw := firewallCalls{removeFn: func() error { return errors.New("the admin prompt was declined") }}
	fw.install(p)
	art := tarGzArtifact(t, stubBinName(), stubBinaryBytes(t))
	url := serveArtifact(t, p, art)
	svc := nativeService("fwsvc", url, sha256hex(art), "tar.gz")
	svc.Native.Binaries[0].FirewallBlock = []string{"100.64.0.0/10"}
	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: "fwsvc", Service: svc, Env: stubEnv(filepath.Join(t.TempDir(), "m"))}, nil); err != nil {
		t.Fatalf("Deploy: %v", err)
	}

	if err := p.Remove(context.Background(), "fwsvc", RemoveOptions{}); err == nil {
		t.Fatal("Remove reported success with the rule still in place")
	}
	if _, ok := p.readRegistry().Entries["fwsvc"]; !ok {
		t.Fatal("the registry forgot a rule that is still there")
	}
	fw.removeFn = nil
	if err := p.Remove(context.Background(), "fwsvc", RemoveOptions{}); err != nil {
		t.Fatalf("second Remove: %v", err)
	}
	if len(fw.removed) != 2 {
		t.Fatalf("firewall removals = %q, want two attempts", fw.removed)
	}
	if _, ok := p.readRegistry().Entries["fwsvc"]; ok {
		t.Fatal("registry entry survived the second Remove")
	}
}

func TestEnsureFirewallBlockRefusesOutsideWindows(t *testing.T) {
	if goruntime.GOOS == "windows" {
		t.Skip("the real implementation runs PowerShell")
	}
	if err := ensureFirewallBlock("x", "/bin/x", []string{"100.64.0.0/10"}); err == nil {
		t.Fatal("a firewall_block binary must not start where no rule can be made")
	}
}

func TestWindowsRemoteAddressesReadsTheFirewallsOwnForm(t *testing.T) {
	// What HNetCfg.FwPolicy2 returned on Windows 11 for -RemoteAddress
	// 100.64.0.0/10,169.254.0.0/16.
	got, ok := windowsRemoteAddresses("100.64.0.0/255.192.0.0,169.254.0.0/255.255.0.0")
	if !ok || !slices.Equal(got, []string{"100.64.0.0/10", "169.254.0.0/16"}) {
		t.Fatalf("got %q ok=%v", got, ok)
	}
	if got, ok := windowsRemoteAddresses("1.2.3.4"); !ok || !slices.Equal(got, []string{"1.2.3.4/32"}) {
		t.Fatalf("bare address: got %q ok=%v", got, ok)
	}
	for _, s := range []string{"", "*", "LocalSubnet", "10.0.0.1-10.0.0.9"} {
		if _, ok := windowsRemoteAddresses(s); ok {
			t.Errorf("%q must not read as a rule we wrote", s)
		}
	}
}

func TestFirewallStateMatchesOnlyTheRuleWeAskedFor(t *testing.T) {
	prog := `C:\Users\u\AppData\Roaming\CashPilot\native\mysterium\myst.exe`
	nets := []string{"169.254.0.0/16", "100.64.0.0/10"}
	good := firewallState{Found: true, Enabled: true, Direction: 2, Action: 0, Profiles: 0x7FFFFFFF,
		Program: strings.ToUpper(prog), Remote: "100.64.0.0/255.192.0.0,169.254.0.0/255.255.0.0"}
	if !good.matches(prog, nets) {
		t.Fatal("the rule we create does not match itself")
	}
	bad := map[string]func(*firewallState){
		"missing":       func(s *firewallState) { s.Found = false },
		"disabled":      func(s *firewallState) { s.Enabled = false },
		"inbound":       func(s *firewallState) { s.Direction = 1 },
		"allow":         func(s *firewallState) { s.Action = 1 },
		"one profile":   func(s *firewallState) { s.Profiles = 4 },
		"other program": func(s *firewallState) { s.Program = `C:\other\myst.exe` },
		"one range":     func(s *firewallState) { s.Remote = "100.64.0.0/255.192.0.0" },
	}
	for name, mutate := range bad {
		st := good
		mutate(&st)
		if st.matches(prog, nets) {
			t.Errorf("%s: matched", name)
		}
	}
}

func TestFirewallScriptsCarryOnlyCleanValues(t *testing.T) {
	if _, err := validFirewallNetworks([]string{"100.64.0.0/10'; Remove-Item C:\\ -Recurse; '"}); err == nil {
		t.Fatal("a non-CIDR value must never reach PowerShell")
	}
	if _, err := validFirewallNetworks([]string{"fd7a::/48"}); err == nil {
		t.Fatal("only IPv4 is supported")
	}
	got, err := validFirewallNetworks([]string{" 100.64.1.2/10 "})
	if err != nil || !slices.Equal(got, []string{"100.64.0.0/10"}) {
		t.Fatalf("got %q err %v", got, err)
	}
	s := firewallCreateScript(firewallRuleName("mysterium", `C:\Users\O'Brien\myst.exe`), `C:\Users\O'Brien\myst.exe`, got)
	if !strings.Contains(s, `-Program 'C:\Users\O''Brien\myst.exe'`) || !strings.Contains(s, `-RemoteAddress '100.64.0.0/10'`) {
		t.Fatalf("create script quoting is wrong:\n%s", s)
	}
}

func TestPSEncodeIsUTF16LEBase64(t *testing.T) {
	// "a" plus "á" in UTF-16LE is 61 00 e1 00.
	if got := psEncode("aá"); got != "YQDhAA==" {
		t.Fatalf("psEncode = %q", got)
	}
}

func TestParseFirewallStateReadsTheCheckScriptsAnswer(t *testing.T) {
	// The line firewallCheckScript printed on Windows 11 for the rule we create.
	out := []byte(`{"firewallOff":false,"found":true,"program":"C:\\x\\myst.exe","remote":"100.64.0.0/255.192.0.0,169.254.0.0/255.255.0.0","direction":2,"action":0,"enabled":true,"profiles":2147483647}` + "\r\n")
	st, err := parseFirewallState(out)
	if err != nil {
		t.Fatal(err)
	}
	if !st.matches(`C:\x\myst.exe`, []string{"100.64.0.0/10", "169.254.0.0/16"}) {
		t.Fatalf("parsed %+v does not match", st)
	}
	if _, err := parseFirewallState([]byte("#< CLIXML\r\n<Objs/>")); err == nil {
		t.Fatal("output that is not the JSON answer must be an error, never a missing rule")
	}
}

func TestFirewallCheckAndRemoveScriptsQuoteTheName(t *testing.T) {
	name := firewallRuleName("it's", `C:\x\myst.exe`)
	for _, s := range []string{firewallCheckScript(name), firewallRemoveScript(name)} {
		if !strings.Contains(s, `'CashPilot: block it''s from private networks (`) {
			t.Fatalf("name not quoted:\n%s", s)
		}
		if !strings.HasPrefix(s, "$ErrorActionPreference = 'Stop'\n$ProgressPreference = 'SilentlyContinue'\n") {
			t.Fatalf("script must stop on error and keep progress off stderr:\n%s", s)
		}
	}
}

// Two Windows users who both deploy the same service must get two rules, or one
// deploy would replace, and one remove would delete, the other user's block.
func TestFirewallRuleNameIsPerInstallation(t *testing.T) {
	a := firewallRuleName("mysterium", `C:\Users\ana\AppData\Roaming\CashPilot\native\mysterium\myst.exe`)
	b := firewallRuleName("mysterium", `C:\Users\bob\AppData\Roaming\CashPilot\native\mysterium\myst.exe`)
	if a == b {
		t.Fatalf("both installations share the rule %q", a)
	}
	if again := firewallRuleName("mysterium", `c:\users\ANA\AppData\Roaming\CashPilot\native\mysterium\myst.exe`); again != a {
		t.Fatalf("the same path in another case is a different rule: %q vs %q", again, a)
	}
}

// A rule Windows would not apply must stop the launch, before and after creating it.
func TestFirewallStateNotEnforced(t *testing.T) {
	if why := (firewallState{}).notEnforced(); why != "" {
		t.Fatalf("an enforced firewall reported %q", why)
	}
	if (firewallState{FirewallOff: true}).notEnforced() == "" {
		t.Fatal("a firewall that is off must be reported")
	}
	if (firewallState{LocalIgnored: true}).notEnforced() == "" {
		t.Fatal("group policy ignoring local rules must be reported")
	}
}

// A redeploy whose binary no longer declares firewall_block deletes the rule the
// previous deploy made, before the registry entry that remembers it is overwritten.
func TestNativeRedeployWithoutFirewallBlockDeletesTheOldRule(t *testing.T) {
	p := newTestNativeProvider(t)
	var fw firewallCalls
	fw.install(p)
	art := tarGzArtifact(t, stubBinName(), stubBinaryBytes(t))
	url := serveArtifact(t, p, art)
	svc := nativeService("fwsvc", url, sha256hex(art), "tar.gz")
	svc.Native.Binaries[0].FirewallBlock = []string{"100.64.0.0/10"}
	spec := DeploySpec{Slug: "fwsvc", Service: svc, Env: stubEnv(filepath.Join(t.TempDir(), "m"))}
	if _, err := p.Deploy(context.Background(), spec, nil); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	binPath := p.readRegistry().Entries["fwsvc"].BinPath

	// The same firewall_block again keeps the rule.
	if _, err := p.Deploy(context.Background(), spec, nil); err != nil {
		t.Fatalf("redeploy: %v", err)
	}
	if len(fw.removed) != 0 {
		t.Fatalf("a redeploy that keeps firewall_block removed %q", fw.removed)
	}

	spec.Service.Native.Binaries[0].FirewallBlock = nil
	if _, err := p.Deploy(context.Background(), spec, nil); err != nil {
		t.Fatalf("redeploy without firewall_block: %v", err)
	}
	if !slices.Equal(fw.removed, []string{"fwsvc " + binPath}) {
		t.Fatalf("firewall removals = %q, want the old rule for %s", fw.removed, binPath)
	}
	if got := p.readRegistry().Entries["fwsvc"].FirewallBlock; len(got) != 0 {
		t.Fatalf("registry still records firewall_block %v", got)
	}
	_ = p.Remove(context.Background(), "fwsvc", RemoveOptions{})
	if len(fw.removed) != 1 {
		t.Fatalf("Remove asked to delete a rule that no longer exists: %q", fw.removed)
	}
}
