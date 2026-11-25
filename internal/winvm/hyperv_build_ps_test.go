package winvm

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const tOwner = "0123456789abcdef0123456789abcdef"

func newBuildSeam(answers ...string) (psBuilder, *fakeRun) {
	h, r, _ := newSeam(answers...)
	return psBuilder{h}, r
}

// TestBuildSeamScriptsAndArguments runs each write-side method once against
// a canned answer and checks it ran its own script with the values it was
// given, and read the answer.
func TestBuildSeamScriptsAndArguments(t *testing.T) {
	ctx := context.Background()
	n := ProductNames()
	sub := netip.MustParsePrefix("172.31.250.0/29")
	p := seamPlan(t)
	p.Owner = tOwner
	ok := `{"ok":true}`
	cases := []struct {
		name   string
		answer string
		want   []string // fragments of the JSON argument
		call   func(b psBuilder) error
	}{
		{"Inventory", `{"vms":[{"id":"` + tVMID + `","owner":"` + tOwner + `"}],"vtpmVms":["` + tVMID + `"],"switches":[{"id":"` + tSwitchID + `","owner":"","ports":["` + tVMID + `"]}],"nats":[{"name":"CashPilot-VM","prefix":"172.31.250.0/29"},{"name":"v6","prefix":"fd00::/64"}],"firewall":[{"id":"CashPilotVM-block-in","owner":"` + tOwner + `"}],"task":{"id":"\\CashPilot\\VMGuard","owner":"` + tOwner + `"},"guardian":true,"hypervAdmin":false,"enhancedSession":true,"gcs":["0000aaaa-facb-11e6-bd58-64006a7986d3"],"hostBuild":"26100.1","installedRam":34359738368,"freeRam":17179869184}`,
			[]string{`"vm":"CashPilot-VM"`, `"taskPath":"\\CashPilot\\"`, `"taskName":"VMGuard"`, `"sid":"S-1-5-21-1-2-3-1001"`},
			func(b psBuilder) error {
				inv, err := b.Inventory(ctx, n, "S-1-5-21-1-2-3-1001")
				if err == nil && (len(inv.VMs) != 1 || inv.Task == nil || len(inv.NATs) != 2 || len(inv.Switches[0].Ports) != 1) {
					t.Errorf("inventory %+v", inv)
				}
				return err
			}},
		{"EnsureSwitch", `{"switchId":"` + tSwitchID + `","ifIndex":20}`, []string{`"owner":"` + tOwner + `"`, `"mac":"00155D000002"`, `"gateway":"172.31.250.1"`, `"prefixLen":29`},
			func(b psBuilder) error {
				_, _, err := b.EnsureSwitch(ctx, n, sub, tOwner, "00-15-5D-00-00-02")
				return err
			}},
		{"EnsureNAT", ok, []string{`"prefix":"172.31.250.0/29"`}, func(b psBuilder) error { return b.EnsureNAT(ctx, n, sub) }},
		{"CreateVM", `{"vmId":"` + tVMID + `","adapterId":"` + tAdapterID + `"}`, []string{`"iso":"D:\\iso\\w.iso"`, `"owner":"` + tOwner + `"`},
			func(b psBuilder) error {
				_, _, err := b.CreateVM(ctx, p, `D:\iso\w.iso`, `C:\ProgramData\CashPilot\answer.iso`)
				return err
			}},
		{"SetIntegration", ok, []string{`"want":{`}, func(b psBuilder) error { return b.SetIntegration(ctx, tVMID, Integration()) }},
		{"SetBindings", ok, []string{`"off":["ms_tcpip6"`}, func(b psBuilder) error { return b.SetBindings(ctx, 20, BindingsOff()) }},
		{"EnsureFirewall", ok, []string{`"group":"CashPilotVM"`, `"ifIndex":20`}, func(b psBuilder) error { return b.EnsureFirewall(ctx, n, 20, tOwner) }},
		{"SetHostNeighbor", ok, []string{`"mac":"00-15-5D-0A-00-02"`}, func(b psBuilder) error {
			return b.SetHostNeighbor(ctx, 20, netip.MustParseAddr("172.31.250.2"), "00155D0A0002")
		}},
		{"SetEnhancedSession", `{"was":true}`, []string{`"on":false`}, func(b psBuilder) error {
			was, err := b.SetEnhancedSession(ctx, false)
			if err == nil && !was {
				t.Error("was not read")
			}
			return err
		}},
		{"EnsureGuardian", `{"created":true}`, []string{`{}`}, func(b psBuilder) error { _, err := b.EnsureGuardian(ctx); return err }},
		{"AddHyperVAdmin", `{"added":true}`, []string{`"sid":"S-1-5-21-1-2-3-1001"`}, func(b psBuilder) error {
			_, err := b.AddHyperVAdmin(ctx, "S-1-5-21-1-2-3-1001")
			return err
		}},
		{"WriteAnswerISO", ok, []string{`"xml":"PHg+"`}, func(b psBuilder) error {
			return b.WriteAnswerISO(ctx, `C:\ProgramData\CashPilot\answer.iso`, []byte("<x>"))
		}},
		{"BootKeys", ok, []string{tVMID}, func(b psBuilder) error { return b.BootKeys(ctx, tVMID) }},
		{"EjectDVDs", `{"left":0}`, []string{tVMID}, func(b psBuilder) error { return b.EjectDVDs(ctx, tVMID) }},
		{"RegisterGuardTask", ok, []string{`"exe":"C:\\ProgramData\\CashPilot\\guard\\CashPilot-guard.exe"`, `"delay":900`, `"dir":"C:\\ProgramData\\CashPilot"`},
			func(b psBuilder) error { return b.RegisterGuardTask(ctx, n, n.GuardExe(), tOwner) }},
		{"StartGuardTask", ok, []string{`"taskName":"VMGuard"`}, func(b psBuilder) error { return b.StartGuardTask(ctx, n) }},
		{"Guest", `{"out":"{\"ok\":true}"}`, []string{`"user":"cpadmin"`, `"arg":"{}"`}, func(b psBuilder) error {
			var out struct {
				OK bool `json:"ok"`
			}
			err := b.Guest(ctx, tVMID, Cred{User: "cpadmin", Password: "Adm1n-Pass-0123456789"}, scriptGuestPing, map[string]any{}, &out)
			if err == nil && !out.OK {
				t.Error("guest answer not read")
			}
			return err
		}},
		{"UnregisterGuardTask", ok, []string{`"owner":"` + tOwner + `"`}, func(b psBuilder) error { return b.UnregisterGuardTask(ctx, n, tOwner) }},
		{"RemoveVM", `{"left":[]}`, []string{`"vhdxDir":"D:\\VMs"`}, func(b psBuilder) error { _, err := b.RemoveVM(ctx, tVMID, `D:\VMs`); return err }},
		{"RemoveSwitch", ok, []string{tSwitchID}, func(b psBuilder) error { return b.RemoveSwitch(ctx, tSwitchID) }},
		{"RemoveNAT", ok, []string{`"prefix":"172.31.250.0/29"`}, func(b psBuilder) error { return b.RemoveNAT(ctx, n, sub) }},
		{"RemoveFirewall", ok, []string{`"owner":"` + tOwner + `"`}, func(b psBuilder) error { return b.RemoveFirewall(ctx, n, tOwner) }},
		{"RemoveGuardian", ok, []string{`{}`}, func(b psBuilder) error { return b.RemoveGuardian(ctx) }},
		{"RemoveHyperVAdmin", ok, []string{`"sid":"S-1-5-21-1-2-3-1001"`}, func(b psBuilder) error { return b.RemoveHyperVAdmin(ctx, "S-1-5-21-1-2-3-1001") }},
	}
	seen := map[string]bool{}
	for _, c := range cases {
		seen[c.name] = true
		t.Run(c.name, func(t *testing.T) {
			b, r := newBuildSeam(c.answer)
			if err := c.call(b); err != nil {
				t.Fatal(err)
			}
			if len(r.scripts) != 1 || r.scripts[0] != buildScripts[c.name] || !strings.HasPrefix(r.scripts[0], hvPrelude) {
				t.Fatalf("%s did not run its own script", c.name)
			}
			for _, w := range c.want {
				if !strings.Contains(r.args[0], w) {
					t.Errorf("argument %s lacks %s", r.args[0], w)
				}
			}
		})
	}
	for name := range buildScripts {
		if !seen[name] {
			t.Errorf("script %s has no case", name)
		}
	}
}

// TestBuildSeamRefusesBadInput: values that could reach PowerShell as code,
// or name an object that is not ours, never reach a script.
func TestBuildSeamRefusesBadInput(t *testing.T) {
	ctx := context.Background()
	n := ProductNames()
	sub := netip.MustParsePrefix("172.31.250.0/29")
	p := seamPlan(t)
	p.Owner = tOwner
	cases := map[string]func(b psBuilder) error{
		"owner not hex": func(b psBuilder) error {
			_, _, err := b.EnsureSwitch(ctx, n, sub, "x'; Remove-VM *", "00155D000002")
			return err
		},
		"lab names elsewhere": func(b psBuilder) error {
			m := n
			m.VM = "Other"
			return b.EnsureNAT(ctx, m, sub)
		},
		"ISO path with a quote": func(b psBuilder) error {
			_, _, err := b.CreateVM(ctx, p, `D:\iso\w'.iso`, `C:\a.iso`)
			return err
		},
		"relative ISO path":    func(b psBuilder) error { _, _, err := b.CreateVM(ctx, p, `w.iso`, `C:\a.iso`); return err },
		"integration not GUID": func(b psBuilder) error { return b.SetIntegration(ctx, tVMID, map[string]bool{"x": true}) },
		"binding not planned":  func(b psBuilder) error { return b.SetBindings(ctx, 20, []string{"ms_tcpip"}) },
		"public guest address": func(b psBuilder) error {
			return b.SetHostNeighbor(ctx, 20, netip.MustParseAddr("8.8.8.8"), "00155D0A0002")
		},
		"SID not a SID":      func(b psBuilder) error { _, err := b.AddHyperVAdmin(ctx, "Everyone"); return err },
		"empty answer file":  func(b psBuilder) error { return b.WriteAnswerISO(ctx, `C:\a.iso`, nil) },
		"guard exe relative": func(b psBuilder) error { return b.RegisterGuardTask(ctx, n, `guard.exe`, tOwner) },
		"guest script unknown": func(b psBuilder) error {
			return b.Guest(ctx, tVMID, Cred{User: "cpadmin", Password: "Adm1n-Pass-0123456789"}, GuestScript{Name: "x", Text: "Remove-Item C:\\"}, nil, nil)
		},
		"guest user not admin": func(b psBuilder) error {
			return b.Guest(ctx, tVMID, Cred{User: "earner", Password: "Adm1n-Pass-0123456789"}, scriptGuestPing, nil, nil)
		},
		"remove VM not a GUID":  func(b psBuilder) error { _, err := b.RemoveVM(ctx, "*", `D:\VMs`); return err },
		"remove switch by name": func(b psBuilder) error { return b.RemoveSwitch(ctx, "CashPilot-VM") },
		"remove firewall no owner": func(b psBuilder) error {
			return b.RemoveFirewall(ctx, n, "")
		},
		"unregister no owner": func(b psBuilder) error { return b.UnregisterGuardTask(ctx, n, "") },
	}
	for name, call := range cases {
		b, r := newBuildSeam()
		if err := call(b); err == nil {
			t.Errorf("%s: accepted", name)
		}
		if len(r.scripts) != 0 {
			t.Errorf("%s: a script ran", name)
		}
	}
}

// pcapng builds a little-endian pcapng file with one Ethernet interface and
// one packet per frame.
func pcapng(frames ...[]byte) []byte { return pcapngOrder(binary.LittleEndian, frames...) }

// pcapngOrder builds the same file in the byte order le.
func pcapngOrder(le binary.ByteOrder, frames ...[]byte) []byte {
	var b bytes.Buffer
	block := func(typ uint32, body []byte) {
		for len(body)%4 != 0 {
			body = append(body, 0)
		}
		n := uint32(12 + len(body))
		binary.Write(&b, le, typ)
		binary.Write(&b, le, n)
		b.Write(body)
		binary.Write(&b, le, n)
	}
	shb := make([]byte, 16)
	le.PutUint32(shb, 0x1A2B3C4D)
	le.PutUint16(shb[4:], 1)
	for i := 8; i < 16; i++ {
		shb[i] = 0xff
	}
	block(0x0A0D0D0A, shb)
	idb := make([]byte, 8)
	le.PutUint16(idb, 1)
	block(1, idb)
	for _, f := range frames {
		epb := make([]byte, 20, 20+len(f))
		le.PutUint32(epb[12:], uint32(len(f)))
		le.PutUint32(epb[16:], uint32(len(f)))
		block(6, append(epb, f...))
	}
	return b.Bytes()
}

// ipv4TCP is an Ethernet frame with an IPv4 TCP header to dst:port.
func ipv4TCP(src, dst netip.Addr, port uint16) []byte {
	f := make([]byte, 14+20+4)
	binary.BigEndian.PutUint16(f[12:], 0x0800)
	ip := f[14:]
	ip[0], ip[9] = 0x45, 6
	s, d := src.As4(), dst.As4()
	copy(ip[12:], s[:])
	copy(ip[16:], d[:])
	binary.BigEndian.PutUint16(ip[22:], port)
	return f
}

func TestParsePcapng(t *testing.T) {
	src, dst := netip.MustParseAddr("192.0.2.10"), netip.MustParseAddr("192.0.2.1")
	for _, bo := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		ks, err := parsePcapng(pcapngOrder(bo, ipv4TCP(src, dst, 45000), []byte{0, 1, 2}))
		if err != nil || len(ks) != 1 || ks[0].Dst != dst || ks[0].Port != 45000 || ks[0].Proto != "TCP" {
			t.Fatalf("%v: %+v %v", bo, ks, err)
		}
	}
	good := pcapng(ipv4TCP(src, dst, 1))
	for name, b := range map[string][]byte{"cut short": good[:len(good)-3], "bad length": append(good[:4:4], 0xff, 0xff, 0xff, 0x7f)} {
		if _, err := parsePcapng(b); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestParsePktmonComponents(t *testing.T) {
	cs, err := parsePktmonComponents([]byte(`[{"Group":"Adapters","Components":[{"Id":4,"Name":"Ethernet","Properties":[{"Name":"IfIndex","Value":"7"}]},{"ID":9,"Name":"vEthernet","ifIndex":20},{"Id":11,"Name":"filter"}]}]`))
	if err != nil {
		t.Fatal(err)
	}
	if cs[4].IfIndex != 7 || cs[9].IfIndex != 20 || cs[11].IfIndex != 0 || len(cs) != 3 {
		t.Fatalf("%+v", cs)
	}
	if _, err := parsePktmonComponents([]byte(`{"nothing":[]}`)); err == nil {
		t.Fatal("a list with no component was accepted")
	}
}

// TestProberSeam runs each Prober script once: the holes, the canary with
// its firewall rule, and a pktmon capture read from a pcapng.
func TestProberSeam(t *testing.T) {
	ctx := context.Background()
	n := ProductNames()
	ok := `{"ok":true}`
	h, r, _ := newSeam()
	var ln net.Listener
	p := &psProber{h: h, n: n, program: n.GuardExe(), tmp: t.TempDir(), listen: func(string, string) (net.Listener, error) {
		var err error
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		return ln, err
	}}
	q := netip.MustParsePrefix("192.0.2.10/32")
	steps := []struct {
		script  string
		answers []string
		call    func() error
	}{
		{"SetL3", []string{ok}, func() error { return p.SetL3(ctx, n, false) }},
		{"SetBinding", []string{ok}, func() error { return p.SetBinding(ctx, 20, "ms_tcpip6", true) }},
		{"Allow", []string{`{"allow":["192.0.2.10"]}`}, func() error { return p.AddAllow(ctx, tVMID, q) }},
		{"Allow", []string{`{"allow":[]}`}, func() error { return p.RemoveAllow(ctx, tVMID, q) }},
		{"LinkLocal", []string{`{"addrs":["fe80::1"]}`}, func() error { _, err := p.LinkLocal(ctx, 20); return err }},
		{"CanaryRule", []string{ok, ok}, func() error {
			c, err := p.Canary(ctx, netip.MustParseAddr("192.0.2.10"), 45000, "")
			if err != nil {
				return err
			}
			conn, err := net.Dial("tcp", ln.Addr().String())
			if err != nil {
				return err
			}
			conn.Write([]byte("cpd-0123456789abcdef"))
			conn.Close()
			for i := 0; i < 100 && len(c.Arrived()) == 0; i++ {
				time.Sleep(10 * time.Millisecond)
			}
			if got := c.Arrived(); len(got) != 1 || got[0] != "cpd-0123456789abcdef" {
				t.Errorf("canary heard %v", got)
			}
			return c.Close()
		}},
	}
	for _, s := range steps {
		r.answers = append(r.answers, s.answers...)
		m := len(r.scripts)
		if err := s.call(); err != nil {
			t.Fatalf("%s: %v", s.script, err)
		}
		for _, sc := range r.scripts[m:] {
			if sc != proberScripts[s.script] {
				t.Fatalf("%s ran another script", s.script)
			}
		}
	}
	// pktmon: start, then stop, list, read the host, find our switch, export.
	dst := netip.MustParseAddr("198.51.100.7")
	r.answers = append(r.answers, ok)
	c, err := p.Pktmon(ctx, []PktFilter{{Addr: dst, Proto: "TCP", Port: 45000}})
	if err != nil {
		t.Fatal(err)
	}
	dir := c.(*psCapture).dir
	out := filepath.Join(dir, "c4.pcapng")
	vnic := filepath.Join(dir, "c9.pcapng")
	os.WriteFile(out, pcapng(ipv4TCP(netip.MustParseAddr("192.0.2.10"), dst, 45000), ipv4TCP(netip.MustParseAddr("192.0.2.10"), dst, 1)), 0o600)
	os.WriteFile(vnic, pcapng(ipv4TCP(netip.MustParseAddr("172.31.250.2"), dst, 45000)), 0o600)
	list := `[{"Components":[{"Id":4,"Name":"Ethernet","ifIndex":7},{"Id":9,"Name":"vEthernet","ifIndex":20}]}]`
	r.answers = append(r.answers, `{"list":`+jsonQuote(list)+`}`, cannedHostNet, `{"ids":["`+tSwitchID+`"]}`,
		`{"files":[{"id":4,"path":`+jsonQuote(out)+`},{"id":9,"path":`+jsonQuote(vnic)+`}]}`)
	recs, err := c.Stop(ctx)
	if err != nil {
		t.Fatal(err)
	}
	classes := map[string]int{}
	for _, rec := range recs {
		classes[rec.Class]++
	}
	if len(recs) != 2 || classes["physical"] != 1 || classes["vnic"] != 1 {
		t.Fatalf("records %+v", recs)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("the capture folder was left")
	}
	for name := range proberScripts {
		if !strings.Contains(strings.Join(r.scripts, "\x00"), proberScripts[name]) {
			t.Errorf("Prober script %s never ran", name)
		}
	}
	// The proof only ever flips ms_tcpip6 and opens one address at a time.
	if err := p.SetBinding(ctx, 20, "ms_server", true); err == nil {
		t.Error("SetBinding took another component")
	}
	if err := p.AddAllow(ctx, tVMID, netip.MustParsePrefix("192.0.2.0/24")); err == nil {
		t.Error("AddAllow took a whole range")
	}
}

func jsonQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, c := range s {
		switch c {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(c)
		default:
			b.WriteRune(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}
