package winvm_test

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/e2e/harness/hvfake"
	"github.com/GeiserX/CashPilot-Desktop/internal/winvm"
)

var buildNow = time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)

const testUserSID = "S-1-5-21-1111111111-2222222222-3333333333-1001"

func setupParams() winvm.SetupParams {
	return winvm.SetupParams{
		Version: winvm.SetupParamsVersion, VHDXDir: `D:\VMs`, ISO: `D:\iso\windows.iso`, ImageIndex: 6,
		AdminPassword: testPassword(1), EarnerPassword: testPassword(2),
		UserSID: testUserSID, ApproveNetwork: true,
	}
}

func buildDeps(t *testing.T, f *hvfake.HyperV, dir string) winvm.BuildDeps {
	t.Helper()
	return winvm.BuildDeps{
		HV: f, Probe: f, Guest: f,
		Echo:      func(context.Context) []netip.Addr { return []netip.Addr{f.PublicIP} },
		Now:       func() time.Time { return buildNow },
		Sleep:     func(ctx context.Context, d time.Duration) error { return ctx.Err() },
		Dir:       dir,
		Exe:       `C:\ProgramData\CashPilot\guard\CashPilot.exe`,
		Logf:      t.Logf,
		ProofWait: time.Millisecond,
	}
}

func readPlan(t *testing.T, dir string) winvm.Plan {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, winvm.PlanFile))
	if err != nil {
		t.Fatal(err)
	}
	p, err := winvm.LoadPlan(b)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func indexOf(calls []string, method string) int {
	return slices.IndexFunc(calls, func(c string) bool {
		return c == method || strings.HasPrefix(c, method+" ") || strings.HasPrefix(c, method+"(")
	})
}

func TestBuildProvesAndConnects(t *testing.T) {
	f := hvfake.New()
	dir := t.TempDir()
	res, err := winvm.Build(context.Background(), buildDeps(t, f, dir), setupParams())
	if err != nil {
		t.Fatalf("Build: %v\nproof: %+v", err, res.Proof)
	}
	p := readPlan(t, dir)
	if !p.ProvedAt.Equal(buildNow) {
		t.Fatalf("ProvedAt = %v, want %v", p.ProvedAt, buildNow)
	}
	if ok, why := winvm.Verdict(res.Proof); !ok {
		t.Logf("verdict: %s", why)
		t.Fatalf("verdict not green: %+v", res.Proof)
	}
	if res.Status.Network != winvm.NetConnected {
		t.Fatalf("after the build the guard says %q (%s), want connected", res.Status.Network, res.Status.Reason)
	}
	if !f.TaskRunning(p.Names.GuardTask) {
		t.Fatal("the guard task is not running after the build")
	}
	if f.PantherLeft(p.VMID) {
		t.Fatal("the guest still holds Windows Setup's copies of the answer file")
	}
	if got := f.GuestAccount(p.VMID, "cpadmin"); got != setupParams().AdminPassword {
		t.Fatal("cpadmin does not have the real password")
	}
	for _, name := range []string{"install.json", "answer.iso", winvm.LockFile} {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s is still there after the build (%v)", name, err)
		}
	}
	calls := f.Calls()
	order := []string{"Inventory", "EnsureSwitch", "EnsureNAT", "SetBindings", "EnsureFirewall", "SetHostNeighbor",
		"EnsureGuardian", "WriteAnswerISO", "CreateVM", "SetIntegration", "AddDeny", "SetEnhancedSession",
		"AddHyperVAdmin", "Start", "Guest", "EjectDVDs", "RegisterGuardTask", "Canary", "StartGuardTask"}
	last := -1
	for _, m := range order {
		i := indexOf(calls, m)
		if i < 0 {
			t.Fatalf("%s was never called; calls: %v", m, calls)
		}
		if i < last {
			t.Fatalf("%s came before %s; calls: %v", m, order[slices.Index(order, m)-1], calls)
		}
		last = i
	}
	if lock, err := os.ReadFile(filepath.Join(dir, winvm.ProofFile)); err != nil || len(lock) == 0 {
		t.Fatalf("proof.json not written: %v", err)
	}
}

// TestBuildRepairKeepsThePlan runs setup twice: the second run is Repair and
// writes the same desired.json except ProvedAt, keeps the Enhanced Session
// value it recorded the first time, and creates nothing new.
func TestBuildRepairKeepsThePlan(t *testing.T) {
	f := hvfake.New()
	dir := t.TempDir()
	d := buildDeps(t, f, dir)
	if _, err := winvm.Build(context.Background(), d, setupParams()); err != nil {
		t.Fatal(err)
	}
	first := readPlan(t, dir)
	if first.EnhancedSessionOriginal == nil || !*first.EnhancedSessionOriginal {
		t.Fatalf("EnhancedSessionOriginal = %v, want true (the fake starts with it on)", first.EnhancedSessionOriginal)
	}
	later := buildNow.Add(48 * time.Hour)
	d.Now = func() time.Time { return later }
	m := len(f.Calls())
	if _, err := winvm.Build(context.Background(), d, setupParams()); err != nil {
		t.Fatalf("Repair: %v", err)
	}
	second := readPlan(t, dir)
	if !second.ProvedAt.Equal(later) {
		t.Fatalf("Repair ProvedAt = %v, want %v", second.ProvedAt, later)
	}
	second.ProvedAt = first.ProvedAt
	a, _ := first.Marshal()
	b, _ := second.Marshal()
	if string(a) != string(b) {
		t.Fatalf("Repair changed desired.json:\n%s\n---\n%s", a, b)
	}
	for _, c := range f.Calls()[m:] {
		switch c {
		case "CreateVM", "WriteAnswerISO", "BootKeys", "EjectDVDs":
			t.Fatalf("Repair called %s", c)
		}
	}
	if n := strings.Count(strings.Join(f.Objects(), "\n"), "\nvm "); n != 0 && !strings.HasPrefix(f.Objects()[0], "vm ") {
		t.Fatal("unexpected VM listing")
	}
}

// TestBuildRefusesForeignObjects: an object with the Secure VM's name that
// this install did not make stops the build before it changes anything.
func TestBuildRefusesForeignObjects(t *testing.T) {
	n := winvm.ProductNames()
	cases := map[string]func(f *hvfake.HyperV){
		"vm":       func(f *hvfake.HyperV) { f.AddForeignVM(n.VM, false) },
		"switch":   func(f *hvfake.HyperV) { f.AddSwitch(n.Switch, "Internal") },
		"nat":      func(f *hvfake.HyperV) { f.AddForeignNAT(n.NAT, netip.MustParsePrefix("172.31.200.0/29")) },
		"firewall": func(f *hvfake.HyperV) { f.AddForeignRule(n.FirewallGroup, "someone-else") },
		"task":     func(f *hvfake.HyperV) { f.AddForeignTask(n.GuardTask) },
	}
	for name, add := range cases {
		t.Run(name, func(t *testing.T) {
			f := hvfake.New()
			add(f)
			before := f.Objects()
			_, err := winvm.Build(context.Background(), buildDeps(t, f, t.TempDir()), setupParams())
			if !errors.Is(err, winvm.ErrNotOwned) {
				t.Fatalf("Build = %v, want ErrNotOwned", err)
			}
			if got := f.Objects(); !slices.Equal(got, before) {
				t.Fatalf("the refused build changed the host:\n%v\n---\n%v", before, got)
			}
			for _, c := range f.Calls() {
				if c != "Inventory" {
					t.Fatalf("the refused build called %s", c)
				}
			}
		})
	}
}

// failAfter lets one call take effect and then report an error, as a
// PowerShell call that timed out after Hyper-V acted would.
type failAfter struct {
	*hvfake.HyperV
	method string
}

func (w *failAfter) hit(m string) bool {
	if w.method == m {
		w.method = ""
		return true
	}
	return false
}

var errAfter = errors.New("the call worked but its answer was lost")

func (w *failAfter) EnsureSwitch(ctx context.Context, n winvm.Names, sub netip.Prefix, owner, mac, switchID string) (string, int, error) {
	id, ifx, err := w.HyperV.EnsureSwitch(ctx, n, sub, owner, mac, switchID)
	if err == nil && w.hit("EnsureSwitch") {
		return "", 0, errAfter
	}
	return id, ifx, err
}

func (w *failAfter) CreateVM(ctx context.Context, p winvm.Plan, iso, answer string) (string, string, error) {
	vm, ad, err := w.HyperV.CreateVM(ctx, p, iso, answer)
	if err == nil && w.hit("CreateVM") {
		return "", "", errAfter
	}
	return vm, ad, err
}

func (w *failAfter) Guest(ctx context.Context, vmID string, c winvm.Cred, s winvm.GuestScript, arg, out any) error {
	err := w.HyperV.Guest(ctx, vmID, c, s, arg, out)
	if err == nil && s.Name == "first-session" && w.hit("Guest") {
		return errAfter
	}
	return err
}

// TestBuildResumesAfterEachStep fails each seam call once, runs setup again,
// and checks the second run finishes with one VM and one switch, and that
// Remove VM then puts the host back exactly as it was.
func TestBuildResumesAfterEachStep(t *testing.T) {
	methods := []string{"Inventory", "ReadHostNet", "EnsureSwitch", "EnsureNAT", "SetBindings", "EnsureFirewall",
		"SetHostNeighbor", "EnsureGuardian", "WriteAnswerISO", "CreateVM", "SetIntegration", "ReadLive", "AddDeny",
		"SetEnhancedSession", "AddHyperVAdmin", "Start", "BootKeys", "EjectDVDs", "RegisterGuardTask",
		"Connect", "Canary", "Pktmon", "AddAllow", "RemoveAllow", "SetL3", "SetBinding", "HVListener",
		"StartGuardTask", "Disconnect"}
	for _, m := range methods {
		for _, after := range []bool{false, true} {
			if after && m != "EnsureSwitch" && m != "CreateVM" && m != "Guest" {
				continue
			}
			name := m
			if after {
				name += "-after"
			}
			t.Run(name, func(t *testing.T) {
				f := hvfake.New()
				before := f.Objects()
				dir := t.TempDir()
				d := buildDeps(t, f, dir)
				if after {
					d.HV = &failAfter{HyperV: f, method: m}
				} else {
					f.FailNext(m, errors.New("injected"))
				}
				_, err1 := winvm.Build(context.Background(), d, setupParams())
				if !slices.Contains(f.Calls(), m) {
					t.Fatalf("%s was never called, so this case tests nothing", m)
				}
				// A red step has two witnesses (the canary and pktmon), so one
				// failing alone still shows the hole; the guard retries its own
				// reads and cuts.
				mayPass := map[string]bool{"ReadLive": true, "Disconnect": true, "Canary": true, "Pktmon": true}
				if err1 == nil && !mayPass[m] {
					t.Fatalf("the first build passed with %s failing", m)
				}
				d.HV = f
				res, err := winvm.Build(context.Background(), d, setupParams())
				if err != nil {
					t.Fatalf("the resumed build failed: %v (first: %v)", err, err1)
				}
				if res.Status.Network != winvm.NetConnected {
					t.Fatalf("after the resumed build the guard says %q: %s", res.Status.Network, res.Status.Reason)
				}
				vms, sws := 0, 0
				for _, o := range f.Objects() {
					if strings.HasPrefix(o, "vm ") {
						vms++
					}
					if strings.HasPrefix(o, "switch ") {
						sws++
					}
				}
				if vms != 1 || sws != 1 {
					t.Fatalf("%d VMs and %d switches after the resume:\n%s", vms, sws, strings.Join(f.Objects(), "\n"))
				}
				if _, err := winvm.Remove(context.Background(), winvm.RemoveDeps{HV: f, Dir: dir, Logf: t.Logf}, winvm.ProductNames()); err != nil {
					t.Fatal(err)
				}
				if got := f.Objects(); !slices.Equal(got, before) {
					t.Fatalf("after Remove the host differs:\n%s\n---\n%s", strings.Join(before, "\n"), strings.Join(got, "\n"))
				}
			})
		}
	}
}

// testPassword is a made-up guest password for tests, built at run time from
// its number so that no password-shaped literal sits in the source.
func testPassword(n int) string { return "t" + strconv.Itoa(n) + strings.Repeat("Qz7", 6) }

// TestBuildRepairFindsRenamedObjects: after the user renames the VM and the
// switch, Repair keeps both by their recorded IDs (it makes no second VM or
// switch), gives the switch our name back for the guard, and connects.
func TestBuildRepairFindsRenamedObjects(t *testing.T) {
	f := hvfake.New()
	dir := t.TempDir()
	d := buildDeps(t, f, dir)
	if _, err := winvm.Build(context.Background(), d, setupParams()); err != nil {
		t.Fatal(err)
	}
	first := readPlan(t, dir)
	if err := f.RenameVM(first.VMID, "my renamed vm"); err != nil {
		t.Fatal(err)
	}
	if err := f.RenameSwitch(first.SwitchID, "my renamed switch"); err != nil {
		t.Fatal(err)
	}
	res, err := winvm.Build(context.Background(), d, setupParams())
	if err != nil {
		t.Fatalf("Repair after a rename: %v", err)
	}
	second := readPlan(t, dir)
	if second.VMID != first.VMID || second.SwitchID != first.SwitchID {
		t.Fatalf("Repair lost the recorded IDs: VM %s -> %s, switch %s -> %s", first.VMID, second.VMID, first.SwitchID, second.SwitchID)
	}
	vms, sws := 0, 0
	for _, o := range f.Objects() {
		if strings.HasPrefix(o, "vm ") {
			vms++
		}
		if strings.HasPrefix(o, "switch ") {
			sws++
			if !strings.Contains(o, " "+first.Names.Switch+" ") {
				t.Errorf("the switch did not get its name back: %s", o)
			}
		}
	}
	if vms != 1 || sws != 1 {
		t.Fatalf("%d VMs and %d switches after Repair:\n%s", vms, sws, strings.Join(f.Objects(), "\n"))
	}
	if res.Status.Network != winvm.NetConnected {
		t.Fatalf("after Repair the guard says %q: %s", res.Status.Network, res.Status.Reason)
	}
}

// TestBuildRepairClearsProofLeftovers: a proof killed in the middle leaves a
// canary rule, its hv_sock test key and a running pktmon session. Repair
// clears them before it lists the host, so the key stays out of the GCS
// baseline and the new proof can capture; Remove VM then ends with the host as
// it was.
func TestBuildRepairClearsProofLeftovers(t *testing.T) {
	f := hvfake.New()
	before := f.Objects()
	dir := t.TempDir()
	d := buildDeps(t, f, dir)
	if _, err := winvm.Build(context.Background(), d, setupParams()); err != nil {
		t.Fatal(err)
	}
	base := readPlan(t, dir).GCSBaseline
	f.LeaveProofLeftovers(winvm.ProductNames(), readPlan(t, dir).Owner)
	if err := os.MkdirAll(filepath.Join(dir, "proof", "pktmon-3"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := winvm.Build(context.Background(), d, setupParams()); err != nil {
		t.Fatalf("Repair with proof leftovers: %v", err)
	}
	if got := readPlan(t, dir).GCSBaseline; !slices.Equal(got, base) {
		t.Fatalf("GCS baseline %v, want %v: a stale proof key entered it", got, base)
	}
	objs := strings.Join(f.Objects(), "\n")
	if strings.Contains(objs, "-proof ") || strings.Contains(objs, "pktmon=true") {
		t.Fatalf("proof leftovers survived Repair:\n%s", objs)
	}
	if _, err := os.Stat(filepath.Join(dir, "proof", "pktmon-3")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the old capture folder survived Repair: %v", err)
	}
	f.LeaveProofLeftovers(winvm.ProductNames(), readPlan(t, dir).Owner)
	if _, err := winvm.Remove(context.Background(), winvm.RemoveDeps{HV: f, Dir: dir, Logf: t.Logf}, winvm.ProductNames()); err != nil {
		t.Fatal(err)
	}
	if got := f.Objects(); !slices.Equal(got, before) {
		t.Fatalf("after Remove the host differs:\n%s\n---\n%s", strings.Join(before, "\n"), strings.Join(got, "\n"))
	}
}

// TestBuildRepairClosesHolesAStoppedProofLeft: a proof stopped before it
// closed its holes leaves an Allow row and the L3 rule off. Repair takes the
// Allow away and turns L3 back on before its own proof, which refuses a fence
// with a hole.
func TestBuildRepairClosesHolesAStoppedProofLeft(t *testing.T) {
	f := hvfake.New()
	dir := t.TempDir()
	d := buildDeps(t, f, dir)
	if _, err := winvm.Build(context.Background(), d, setupParams()); err != nil {
		t.Fatal(err)
	}
	p := readPlan(t, dir)
	ctx := context.Background()
	if err := f.AddAllow(ctx, p.VMID, netip.PrefixFrom(f.LANIP, 32)); err != nil {
		t.Fatal(err)
	}
	if err := f.SetL3(ctx, p.Names, false); err != nil {
		t.Fatal(err)
	}
	res, err := winvm.Build(ctx, d, setupParams())
	if err != nil {
		t.Fatalf("Repair with an Allow row and L3 off: %v", err)
	}
	for _, o := range f.Objects() {
		if strings.Contains(o, "Allow") || (strings.HasPrefix(o, "rule ") && strings.Contains(o, "enabled=false")) {
			t.Fatalf("a hole survived Repair: %s", o)
		}
	}
	if res.Status.Network != winvm.NetConnected {
		t.Fatalf("after Repair the guard says %q: %s", res.Status.Network, res.Status.Reason)
	}
}

// TestBuildWithAFailedProof: a fence that ignores its Deny rows fails the
// proof. Build returns ErrProofFailed, desired.json stays unproven, the VM
// stays off the network, and the guard task still runs to keep it off.
func TestBuildWithAFailedProof(t *testing.T) {
	f := hvfake.New()
	f.SetMutant(true)
	dir := t.TempDir()
	res, err := winvm.Build(context.Background(), buildDeps(t, f, dir), setupParams())
	if !errors.Is(err, winvm.ErrProofFailed) {
		t.Fatalf("Build = %v, want ErrProofFailed", err)
	}
	p := readPlan(t, dir)
	if !p.ProvedAt.IsZero() {
		t.Fatalf("ProvedAt = %v after a failed proof", p.ProvedAt)
	}
	if res.Status.Network == winvm.NetConnected {
		t.Fatalf("the guard connected after a failed proof: %s", res.Status.Reason)
	}
	for _, o := range f.Objects() {
		if strings.HasPrefix(o, "vm ") {
			live, err := f.ReadLive(context.Background(), p.Names, p.VMID)
			if err != nil {
				t.Fatal(err)
			}
			for _, a := range live.Adapters {
				if a.SwitchID != "" {
					t.Fatalf("the VM is on switch %s after a failed proof", a.SwitchID)
				}
			}
		}
	}
	if !f.TaskRunning(p.Names.GuardTask) {
		t.Fatal("the guard task does not run after a failed proof")
	}
}

// adapterSwitch is the switch any adapter of the Secure VM is on, "" for none.
func adapterSwitch(t *testing.T, f *hvfake.HyperV, p winvm.Plan) string {
	t.Helper()
	live, err := f.ReadLive(context.Background(), p.Names, p.VMID)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range live.Adapters {
		if a.SwitchID != "" {
			return a.SwitchID
		}
	}
	return ""
}

// TestBuildRepairThatFailsKeepsTheVMOff: Repair changes the fence, so it
// takes the VM off the network before its first change. setup.ps1 stopped the
// guard task before the exe ran, so a Repair that fails part way starts it
// again; the plan is no longer proven, so the guard keeps the VM off until a
// Repair passes.
func TestBuildRepairThatFailsKeepsTheVMOff(t *testing.T) {
	cases := map[string]func(t *testing.T, f *hvfake.HyperV, p winvm.Plan){
		"host checks": func(t *testing.T, f *hvfake.HyperV, p winvm.Plan) {
			net, _ := f.ReadNetwork(context.Background())
			net.Metered = true
			f.SetNetwork(net)
		},
		"firewall": func(t *testing.T, f *hvfake.HyperV, p winvm.Plan) {
			f.FailNext("EnsureFirewall", errors.New("injected"))
		},
		"planned state": func(t *testing.T, f *hvfake.HyperV, p winvm.Plan) {
			if err := f.AddAdapter(p.VMID, p.Names.Switch); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			f := hvfake.New()
			dir := t.TempDir()
			d := buildDeps(t, f, dir)
			if _, err := winvm.Build(context.Background(), d, setupParams()); err != nil {
				t.Fatal(err)
			}
			p := readPlan(t, dir)
			if adapterSwitch(t, f, p) == "" {
				t.Fatal("the build did not connect the VM, so this case tests nothing")
			}
			f.StopTask(p.Names.GuardTask)
			breakIt(t, f, p)
			m := len(f.Calls())
			if _, err := winvm.Build(context.Background(), d, setupParams()); err == nil {
				t.Fatal("the broken Repair passed")
			}
			calls := f.Calls()[m:]
			dc := indexOf(calls, "Disconnect")
			if dc < 0 {
				t.Fatalf("Repair never took the VM off the network; calls: %v", calls)
			}
			for _, w := range []string{"EnsureSwitch", "EnsureNAT", "SetBindings", "EnsureFirewall", "SetHostNeighbor", "AddDeny", "RemoveDeny"} {
				if i := indexOf(calls, w); i >= 0 && i < dc {
					t.Fatalf("Repair called %s with the VM on the network; calls: %v", w, calls)
				}
			}
			if sw := adapterSwitch(t, f, p); sw != "" {
				t.Fatalf("after the failed Repair the VM is on switch %s", sw)
			}
			if !readPlan(t, dir).ProvedAt.IsZero() {
				t.Fatal("the failed Repair left the plan proven")
			}
			if !f.TaskRunning(p.Names.GuardTask) {
				t.Fatal("the guard task does not run after the failed Repair")
			}
		})
	}
}

// TestBuildRepairWithAFailedProofIsNotProven: a Repair whose proof fails
// never keeps the earlier ProvedAt, so the guard keeps the VM off.
func TestBuildRepairWithAFailedProofIsNotProven(t *testing.T) {
	f := hvfake.New()
	dir := t.TempDir()
	d := buildDeps(t, f, dir)
	if _, err := winvm.Build(context.Background(), d, setupParams()); err != nil {
		t.Fatal(err)
	}
	if readPlan(t, dir).ProvedAt.IsZero() {
		t.Fatal("the first build is not proven, so this case tests nothing")
	}
	f.SetMutant(true)
	res, err := winvm.Build(context.Background(), d, setupParams())
	if !errors.Is(err, winvm.ErrProofFailed) {
		t.Fatalf("Repair = %v, want ErrProofFailed", err)
	}
	p := readPlan(t, dir)
	if !p.ProvedAt.IsZero() {
		t.Fatalf("ProvedAt = %v after a Repair whose proof failed", p.ProvedAt)
	}
	if res.Status.Network == winvm.NetConnected {
		t.Fatalf("the guard connected after a failed proof: %s", res.Status.Reason)
	}
	if sw := adapterSwitch(t, f, p); sw != "" {
		t.Fatalf("the VM is on switch %s after a Repair whose proof failed", sw)
	}
}

// TestBuildRepairFromAnotherAccount: setup records every account it adds to
// Hyper-V Administrators, so Remove VM can take each one out again. A Repair
// from a second account that is not a member adds it and records it too. A
// third account that is already a member is not recorded, and Remove VM
// leaves it in the group.
func TestBuildRepairFromAnotherAccount(t *testing.T) {
	const other = "S-1-5-21-1111111111-2222222222-3333333333-1002"
	const member = "S-1-5-21-1111111111-2222222222-3333333333-1003"
	f := hvfake.New()
	f.SetHyperVAdmin(member, true)
	before := f.Objects()
	dir := t.TempDir()
	d := buildDeps(t, f, dir)
	if _, err := winvm.Build(context.Background(), d, setupParams()); err != nil {
		t.Fatal(err)
	}
	if got := readPlan(t, dir).AddedAdminSIDs; !slices.Equal(got, []string{testUserSID}) {
		t.Fatalf("AddedAdminSIDs = %v, want [%s]", got, testUserSID)
	}
	for _, sid := range []string{other, member, other} {
		sp := setupParams()
		sp.UserSID = sid
		if _, err := winvm.Build(context.Background(), d, sp); err != nil {
			t.Fatalf("Repair from %s: %v", sid, err)
		}
	}
	if got := readPlan(t, dir).AddedAdminSIDs; !slices.Equal(got, []string{testUserSID, other}) {
		t.Fatalf("AddedAdminSIDs = %v after the other accounts' Repairs, want [%s %s]", got, testUserSID, other)
	}
	objs := strings.Join(f.Objects(), "\n")
	if !strings.Contains(objs, "hyperv-admin "+other) {
		t.Fatal("the second account's Repair did not add it to Hyper-V Administrators")
	}
	if _, err := winvm.Remove(context.Background(), winvm.RemoveDeps{HV: f, Dir: dir, Logf: t.Logf}, winvm.ProductNames()); err != nil {
		t.Fatal(err)
	}
	diffObjects(t, before, f.Objects())
}
