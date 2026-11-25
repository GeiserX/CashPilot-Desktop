package winvm

import (
	"bytes"
	"flag"
	"math/rand"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

var update = flag.Bool("update", false, "rewrite the golden desired.json files")

func testOptions(n Names) Options {
	return Options{
		Names:       n,
		Subnet:      testSubnet,
		HostVNICMAC: "00-15-5D-00-00-01",
		GuestMAC:    "00:15:5d:00:00:02",
		ExtraExits:  []netip.Addr{addr("203.0.113.7")},
	}
}

// testPlan is a product plan as setup would leave it, with IDs filled.
func testPlan(t testing.TB, edit func(*Plan)) Plan {
	t.Helper()
	p, err := NewPlan(testOptions(ProductNames()), nil)
	if err != nil {
		t.Fatal(err)
	}
	p.VMID = "aaaaaaaa-0000-4000-8000-000000000001"
	p.AdapterID = "aaaaaaaa-0000-4000-8000-000000000002"
	p.SwitchID = ourSwitchID
	p.SetGCSBaseline([]string{"999e53d4-3d5c-4c3e-8779-bed06ec056e1", "{a5201c21-2770-4c11-a68e-f182edb29220}"})
	p.HostBuild = "26100.1000"
	if edit != nil {
		edit(&p)
	}
	return p
}

func TestGoldenDesiredJSON(t *testing.T) {
	lab, err := LabNames(1)
	if err != nil {
		t.Fatal(err)
	}
	for name, n := range map[string]Names{"product": ProductNames(), "lab1": lab} {
		p, err := NewPlan(testOptions(n), nil)
		if err != nil {
			t.Fatal(err)
		}
		got, err := p.Marshal()
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join("testdata", "desired-"+name+".golden.json")
		if *update {
			if err := os.WriteFile(path, got, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: desired.json differs from %s; run go test -run TestGoldenDesiredJSON -update and review the diff\n%s", name, path, got)
		}
		// The golden file is a plan LoadPlan accepts as it stands.
		if _, err := LoadPlan(want); err != nil {
			t.Errorf("%s: LoadPlan(golden) = %v", name, err)
		}
	}
}

func TestStaticDenyRowsArePresentAndOnlyDeny(t *testing.T) {
	p := testPlan(t, nil)
	acls := p.WantACLs(Dynamic{})
	for _, s := range []string{"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
		"192.0.0.0/24", "192.168.0.0/16", "198.18.0.0/15", "224.0.0.0/4", "240.0.0.0/4", "::/0", "203.0.113.7/32"} {
		if !hasRow(acls, s) {
			t.Errorf("no Deny row for %s", s)
		}
	}
	for _, a := range acls {
		if a.Action != "Deny" || a.Direction != "Both" {
			t.Errorf("row %s is not Deny Both", a)
		}
	}
	if len(acls) != len(StaticDeny())+1 {
		t.Errorf("WantACLs has %d rows, want %d", len(acls), len(StaticDeny())+1)
	}
}

func TestWantACLsPrunesCoveredAndNeverRepeatsAPrefix(t *testing.T) {
	p := testPlan(t, nil)
	d := Dynamic{
		HostAddrs: []netip.Prefix{pfx("192.168.77.20/32"), pfx("203.0.113.7/32"), pfx("198.51.100.9/32")},
		OnLink:    []netip.Prefix{pfx("198.51.100.0/24"), pfx("192.168.77.0/24")},
		OwnIPs:    []netip.Prefix{pfx("203.0.113.7/32"), pfx("203.0.113.8/32"), pfx("203.0.113.8/32")},
		Tunnels:   []netip.Prefix{pfx("10.20.0.0/16"), pfx("198.51.100.128/25"), pfx("172.16.0.0/12"), pfx("::/0")},
	}
	acls := p.WantACLs(d)
	for _, gone := range []string{"192.168.77.20/32", "198.51.100.9/32", "192.168.77.0/24", "10.20.0.0/16", "198.51.100.128/25"} {
		if hasRow(acls, gone) {
			t.Errorf("covered prefix %s kept a row", gone)
		}
	}
	for _, kept := range []string{"198.51.100.0/24", "203.0.113.7/32", "203.0.113.8/32"} {
		if !hasRow(acls, kept) {
			t.Errorf("prefix %s has no row", kept)
		}
	}
	assertUniquePrefixes(t, acls)

	// An IPv4-mapped prefix becomes its IPv4 prefix; one wider than the mapped
	// range has no IPv4 form and is dropped. Neither may leave an invalid row.
	mapped := p.WantACLs(Dynamic{OwnIPs: []netip.Prefix{pfx("::ffff:198.51.100.7/128"), pfx("::ffff:198.51.100.128/121"), pfx("::ffff:0:0/80")}})
	for _, want := range []string{"198.51.100.7/32", "198.51.100.128/25"} {
		if !hasRow(mapped, want) {
			t.Errorf("mapped prefix did not become row %s", want)
		}
	}
	for _, a := range mapped {
		if !a.Remote.IsValid() {
			t.Errorf("WantACLs emitted an invalid row: %+v", a)
		}
	}
	if len(mapped) != len(StaticDeny())+3 {
		t.Errorf("mapped input gave %d rows, want %d", len(mapped), len(StaticDeny())+3)
	}
	assertUniquePrefixes(t, mapped)

	// The same over random input: never two rows with one prefix, every static
	// row always there, and the order stable.
	r := rand.New(rand.NewSource(1))
	pool := []string{"10.0.0.0/8", "10.1.0.0/16", "198.51.100.0/24", "198.51.100.0/24", "198.51.100.1/32",
		"203.0.113.0/24", "203.0.113.5/32", "192.0.2.0/24", "0.0.0.0/0", "::/0", "2001:db8::/32", "172.16.0.0/12"}
	pick := func() []netip.Prefix {
		var out []netip.Prefix
		for i := r.Intn(6); i > 0; i-- {
			out = append(out, pfx(pool[r.Intn(len(pool))]))
		}
		return out
	}
	for i := 0; i < 500; i++ {
		d := Dynamic{HostAddrs: pick(), OnLink: pick(), OwnIPs: pick(), Tunnels: pick()}
		acls := p.WantACLs(d)
		assertUniquePrefixes(t, acls)
		for _, s := range StaticDeny() {
			if !hasRow(acls, s.String()) {
				t.Fatalf("static row %s missing for %+v", s, d)
			}
		}
		if again := p.WantACLs(d); !reflect.DeepEqual(acls, again) {
			t.Fatalf("WantACLs is not stable")
		}
	}
}

func assertUniquePrefixes(t *testing.T, acls []ACL) {
	t.Helper()
	seen := map[netip.Prefix]bool{}
	for _, a := range acls {
		if seen[a.Remote] {
			t.Errorf("two rows share prefix %s (Hyper-V refuses that with 0x800700B7)", a.Remote)
		}
		seen[a.Remote] = true
	}
}

// The Repair trap: setup turned Enhanced Session off, so on Repair the live
// value reads false. The original (true) must survive, or removal would leave
// it off for good.
func TestRepairCarriesIdentity(t *testing.T) {
	prev := testPlan(t, func(p *Plan) { p.CreatedGuardian = true })
	next, err := NewPlan(testOptions(ProductNames()), &prev)
	if err != nil {
		t.Fatal(err)
	}
	if next.VMID != prev.VMID || next.AdapterID != prev.AdapterID || next.SwitchID != prev.SwitchID || !next.CreatedGuardian {
		t.Errorf("Repair lost the VM identity or CreatedGuardian: %+v", next)
	}
	lab, _ := LabNames(2)
	if _, err := NewPlan(testOptions(lab), &prev); err == nil {
		t.Errorf("a plan for another instance was accepted as prev")
	}
}

func TestLabOverridesHaveNoFenceField(t *testing.T) {
	allowed := map[string]bool{"MinHostRAM": true, "GuestMemory": true, "KeepEnhancedSession": true, "Instance": true}
	typ := reflect.TypeOf(LabOverrides{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if !allowed[f.Name] {
			t.Errorf("LabOverrides.%s is not a resource field; the lab must never change the fence", f.Name)
		}
		low := strings.ToLower(f.Name)
		for _, bad := range []string{"acl", "deny", "bind", "firewall", "integration", "guard", "spoof", "nat", "gcs"} {
			if strings.Contains(low, bad) {
				t.Errorf("LabOverrides.%s looks like a fence field", f.Name)
			}
		}
	}

	// And applying one leaves every fence field of the plan as the product's.
	o, err := LabOverrides{GuestMemory: 4 * GiB, KeepEnhancedSession: true, Instance: 1}.Apply(testOptions(ProductNames()))
	if err != nil {
		t.Fatal(err)
	}
	lab, _ := NewPlan(o, nil)
	prod, _ := NewPlan(testOptions(ProductNames()), nil)
	if !slices.Equal(lab.StaticDeny, prod.StaticDeny) || !slices.Equal(lab.BindingsOff, prod.BindingsOff) ||
		!reflect.DeepEqual(lab.Integration, prod.Integration) || lab.DhcpGuard != prod.DhcpGuard ||
		lab.RouterGuard != prod.RouterGuard || lab.MacSpoofing != prod.MacSpoofing || lab.ExposeVirt != prod.ExposeVirt {
		t.Errorf("a lab plan differs from the product in a fence field")
	}
	if lab.MemoryBytes != 4*GiB || lab.EnhancedSession != ESKeep || lab.Names.VM != "cpd-svm-1" {
		t.Errorf("the lab overrides were not applied: %+v", lab)
	}
	if _, err := (LabOverrides{Instance: 0}).Apply(testOptions(ProductNames())); err == nil {
		t.Errorf("lab instance 0 was accepted")
	}
}

func TestLoadPlanRefusesALooserOrBrokenFile(t *testing.T) {
	good, err := testPlan(t, nil).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPlan(good); err != nil {
		t.Fatalf("LoadPlan(good) = %v", err)
	}
	edit := func(f func(*Plan)) []byte {
		b, err := testPlan(t, f).Marshal()
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	cases := map[string][]byte{
		"unknown field":    bytes.Replace(good, []byte(`"version": 1,`), []byte(`"version": 1, "allow": true,`), 1),
		"trailing data":    append(slices.Clone(good), []byte(`{}`)...),
		"oversize":         append(slices.Clone(good), bytes.Repeat([]byte(" "), maxPlanBytes)...),
		"other version":    edit(func(p *Plan) { p.Version = 2 }),
		"mac spoofing":     edit(func(p *Plan) { p.MacSpoofing = true }),
		"no dhcp guard":    edit(func(p *Plan) { p.DhcpGuard = false }),
		"no router guard":  edit(func(p *Plan) { p.RouterGuard = false }),
		"nested virt":      edit(func(p *Plan) { p.ExposeVirt = true }),
		"no ipv6 deny":     edit(func(p *Plan) { p.StaticDeny = p.StaticDeny[:len(p.StaticDeny)-1] }),
		"extra static row": edit(func(p *Plan) { p.StaticDeny = append(p.StaticDeny, pfx("8.8.8.0/24")) }),
		"binding dropped":  edit(func(p *Plan) { p.BindingsOff = p.BindingsOff[1:] }),
		"kvp on":           edit(func(p *Plan) { p.Integration[ISKVP] = true }),
		"checkpoints on":   edit(func(p *Plan) { p.Checkpoints = "Standard" }),
		"autostart":        edit(func(p *Plan) { p.AutoStart = "Start" }),
		"gateway moved":    edit(func(p *Plan) { p.Gateway = addr("172.20.0.14") }),
		"subnet outside":   edit(func(p *Plan) { p.Subnet = pfx("192.0.2.0/29") }),
		"bad names":        edit(func(p *Plan) { p.Names.DataDir = `C:\Users\Public` }),
		"bad vm id":        edit(func(p *Plan) { p.VMID = "x; Remove-VM *" }),
		"unnormalized gcs": edit(func(p *Plan) { p.GCSBaseline = []string{"abc"} }),
	}
	for name, b := range cases {
		if _, err := LoadPlan(b); err == nil {
			t.Errorf("%s: LoadPlan accepted it", name)
		}
	}
}

func TestPlanRoundTrip(t *testing.T) {
	p := testPlan(t, func(p *Plan) { p.ProvedAt = time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC) })
	b, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	q, err := LoadPlan(b)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(p, q) {
		t.Errorf("round trip changed the plan:\n%+v\n%+v", p, q)
	}
}

func TestNewPlanRefusesBadOptions(t *testing.T) {
	cases := map[string]func(*Options){
		"bad names":         func(o *Options) { o.Names.VM = "other-vm" },
		"subnet /24":        func(o *Options) { o.Subnet = pfx("172.20.0.0/24") },
		"subnet unmasked":   func(o *Options) { o.Subnet = pfx("172.20.0.9/29") },
		"subnet off 172/12": func(o *Options) { o.Subnet = pfx("10.0.0.0/29") },
		"bad host mac":      func(o *Options) { o.HostVNICMAC = "nope" },
		"multicast mac":     func(o *Options) { o.GuestMAC = "01-00-5E-00-00-01" },
		"zero mac":          func(o *Options) { o.GuestMAC = "00-00-00-00-00-00" },
		"same mac":          func(o *Options) { o.GuestMAC = o.HostVNICMAC },
		"tiny memory":       func(o *Options) { o.GuestMemory = GiB },
		"odd memory":        func(o *Options) { o.GuestMemory = 4*GiB + 1 },
		"unknown es":        func(o *Options) { o.EnhancedSession = "maybe" },
		"product keeps es":  func(o *Options) { o.EnhancedSession = ESKeep },
		"ipv6 exit":         func(o *Options) { o.ExtraExits = []netip.Addr{addr("2001:db8::1")} },
	}
	for name, edit := range cases {
		o := testOptions(ProductNames())
		edit(&o)
		if _, err := NewPlan(o, nil); err == nil {
			t.Errorf("%s: NewPlan accepted it", name)
		}
	}
	p, err := NewPlan(testOptions(ProductNames()), nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Gateway != addr("172.20.0.9") || p.GuestIP != addr("172.20.0.10") || p.MemoryBytes != DefaultGuestMemory || p.EnhancedSession != ESOff {
		t.Errorf("defaults: %+v", p)
	}
}

func TestPickSubnet(t *testing.T) {
	got, err := PickSubnet(nil)
	if err != nil || got != pfx("172.31.250.0/29") {
		t.Fatalf("PickSubnet(nil) = %v %v", got, err)
	}
	// A NetNat or a Default Switch /20 on the first choice moves it on.
	got, err = PickSubnet([]netip.Prefix{pfx("172.31.240.0/20")})
	if err != nil || got.Overlaps(pfx("172.31.240.0/20")) || !natSpace.Contains(got.Addr()) || got.Bits() != 29 {
		t.Errorf("PickSubnet(/20 in use) = %v %v", got, err)
	}
	// Only an overlap matters: a neighbouring /29 is fine.
	got, _ = PickSubnet([]netip.Prefix{pfx("172.31.250.8/29")})
	if got != pfx("172.31.250.0/29") {
		t.Errorf("a neighbour /29 moved the pick to %v", got)
	}
	// It wraps around to the start of 172.16/12.
	got, _ = PickSubnet([]netip.Prefix{pfx("172.31.0.0/16")})
	if got != pfx("172.16.0.0/29") {
		t.Errorf("PickSubnet did not wrap: %v", got)
	}
	if _, err := PickSubnet([]netip.Prefix{pfx("172.16.0.0/12")}); err == nil {
		t.Errorf("PickSubnet found a /29 in a full 172.16/12")
	}
}

func TestGCSBaselineNormalizes(t *testing.T) {
	got := GCSBaseline([]string{" {abc-1} ", "ABC-1", "", "def-2"})
	if !slices.Equal(got, []string{"ABC-1", "DEF-2"}) {
		t.Errorf("GCSBaseline = %v", got)
	}
	if GCSBaseline(nil) == nil {
		t.Errorf("GCSBaseline(nil) is nil, want an empty list")
	}
}

// An other line's exit keeps its row under a wider dynamic prefix: setup
// placed it and the guard does not manage it.
func TestWantACLsKeepsAnExitUnderATunnel(t *testing.T) {
	p := testPlan(t, nil)
	acls := p.WantACLs(Dynamic{Tunnels: []netip.Prefix{pfx("203.0.113.0/24")}})
	for _, want := range []string{"203.0.113.7/32", "203.0.113.0/24"} {
		if !hasRow(acls, want) {
			t.Errorf("no row for %s", want)
		}
	}
	assertUniquePrefixes(t, acls)
}

// TestEnhancedSessionPolicyChangesRoundTrip: every plan NewPlan writes loads
// again across each change of policy on a lab instance.
func TestEnhancedSessionPolicyChangesRoundTrip(t *testing.T) {
	lab, _ := LabNames(1)
	opts := func(es ESPolicy) Options { o := testOptions(lab); o.EnhancedSession = es; return o }
	roundTrip := func(name string, p Plan) {
		t.Helper()
		b, err := p.Marshal()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		q, err := LoadPlan(b)
		if err != nil {
			t.Fatalf("%s: the plan NewPlan wrote does not load: %v", name, err)
		}
		if !reflect.DeepEqual(p, q) {
			t.Fatalf("%s: round trip changed the plan", name)
		}
	}
	off, err := NewPlan(opts(ESOff), nil)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip("off", off)
	keep, err := NewPlan(opts(ESKeep), nil)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip("keep", keep)
	keepToOff, err := NewPlan(opts(ESOff), &keep)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip("keep then off", keepToOff)
	offToOff, err := NewPlan(opts(ESOff), &off)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip("off then off", offToOff)
	offToKeep, err := NewPlan(opts(ESKeep), &off)
	if err != nil {
		t.Fatal(err)
	}
	roundTrip("off then keep", offToKeep)
}
