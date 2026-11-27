package winvm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strconv"
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
		{"Inventory", `{"vms":[{"id":"` + tVMID + `","owner":"` + tOwner + `"}],"guardianVms":["` + tVMID + `"],"switches":[{"id":"` + tSwitchID + `","owner":"","ports":["` + tVMID + `"]}],"nats":[{"name":"CashPilot-VM","prefix":"172.31.250.0/29"},{"name":"v6","prefix":"fd00::/64"}],"firewall":[{"id":"CashPilotVM-block-in","owner":"` + tOwner + `"}],"task":{"id":"\\CashPilot\\VMGuard","owner":"` + tOwner + `"},"guardian":true,"hypervAdmin":false,"enhancedSession":true,"gcs":["0000aaaa-facb-11e6-bd58-64006a7986d3"],"hostBuild":"26100.1","installedRam":34359738368,"freeRam":17179869184}`,
			[]string{`"vm":"CashPilot-VM"`, `"taskPath":"\\CashPilot\\"`, `"taskName":"VMGuard"`, `"sid":"S-1-5-21-1-2-3-1001"`, `"vmId":"` + tVMID + `"`, `"switchId":"` + tSwitchID + `"`},
			func(b psBuilder) error {
				inv, err := b.Inventory(ctx, n, KnownIDs{VM: tVMID, Switch: tSwitchID}, "S-1-5-21-1-2-3-1001")
				if err == nil && (len(inv.VMs) != 1 || inv.Task == nil || len(inv.NATs) != 2 || len(inv.Switches[0].Ports) != 1 || len(inv.GuardianVMs) != 1) {
					t.Errorf("inventory %+v", inv)
				}
				return err
			}},
		{"EnsureSwitch", `{"switchId":"` + tSwitchID + `","managementId":"` + tAdapterID + `","mac":"00155D000002","ifIndex":20}`, []string{`"owner":"` + tOwner + `"`, `"mac":"00155D000002"`, `"gateway":"172.31.250.1"`, `"prefixLen":29`, `"switchId":"` + tSwitchID + `"`},
			func(b psBuilder) error {
				_, _, _, _, err := b.EnsureSwitch(ctx, n, sub, tOwner, "00-15-5D-00-00-02", tSwitchID, tAdapterID)
				return err
			}},
		{"EnsureNAT", `{"id":"` + tSwitchID + `"}`, []string{`"prefix":"172.31.250.0/29"`}, func(b psBuilder) error { _, err := b.EnsureNAT(ctx, n, sub, ""); return err }},
		{"CreateVM", `{"vmId":"` + tVMID + `","adapterId":"` + tAdapterID + `","disk":{"path":"D:\\VMs\\CashPilot-VM.vhdx","id":"` + strings.Repeat("1", 48) + `"}}`, []string{`"iso":"D:\\iso\\w.iso"`, `"owner":"` + tOwner + `"`},
			func(b psBuilder) error {
				_, _, _, _, err := b.CreateVM(ctx, p, `D:\iso\w.iso`, `C:\ProgramData\CashPilot\answer.iso`)
				return err
			}},
		{"SetIntegration", ok, []string{`"want":{`}, func(b psBuilder) error { return b.SetIntegration(ctx, tVMID, tOwner, Integration()) }},
		{"SetBindings", `{"bindings":{"ms_tcpip":true}}`, []string{`"off":["ms_tcpip6"`}, func(b psBuilder) error { _, err := b.SetBindings(ctx, p, 20); return err }},
		{"EnsureFirewall", `{"identity":"` + strings.Repeat("1", 64) + `"}`, []string{`"group":"CashPilotVM"`, `"ifIndex":20`}, func(b psBuilder) error { _, err := b.EnsureFirewall(ctx, p, 20); return err }},
		{"SetHostNeighbor", ok, []string{`"mac":"00-15-5D-0A-00-02"`}, func(b psBuilder) error {
			return b.SetHostNeighbor(ctx, 20, tSwitchID, tOwner, tAdapterID, netip.MustParseAddr("172.31.250.2"), "00155D0A0002")
		}},
		{"EnsureGuardian", `{"id":"` + strings.Repeat("1", 64) + `"}`, []string{`{}`}, func(b psBuilder) error { _, err := b.EnsureGuardian(ctx); return err }},
		{"AddHyperVAdmin", `{"added":true}`, []string{`"sid":"S-1-5-21-1-2-3-1001"`}, func(b psBuilder) error {
			_, err := b.AddHyperVAdmin(ctx, "S-1-5-21-1-2-3-1001")
			return err
		}},
		{"WriteAnswerISO", ok, []string{`"xml":"PHg+"`}, func(b psBuilder) error {
			return b.WriteAnswerISO(ctx, `C:\ProgramData\CashPilot\answer.iso`, []byte("<x>"))
		}},
		{"BootKeys", ok, []string{tVMID}, func(b psBuilder) error { return b.BootKeys(ctx, tVMID, tOwner) }},
		{"EjectDVDs", `{"left":0}`, []string{tVMID}, func(b psBuilder) error { return b.EjectDVDs(ctx, tVMID, tOwner, nil) }},
		{"RegisterGuardTask", ok, []string{`"exe":"C:\\ProgramData\\CashPilot\\guard\\CashPilot-guard.exe"`, `"delay":900`, `"dir":"C:\\ProgramData\\CashPilot"`},
			func(b psBuilder) error { return b.RegisterGuardTask(ctx, n, n.GuardExe(), tOwner) }},
		{"StartGuardTask", ok, []string{`"taskName":"VMGuard"`}, func(b psBuilder) error { return b.StartGuardTask(ctx, n, tOwner) }},
		{"Guest", `{"out":"{\"ok\":true}"}`, []string{`"user":"cpadmin"`, `"arg":"{}"`, `"guest":"ping"`}, func(b psBuilder) error {
			var out struct {
				OK bool `json:"ok"`
			}
			err := b.Guest(ctx, tVMID, tOwner, Cred{User: "cpadmin", Password: testPassword(1)}, scriptGuestPing, map[string]any{}, &out)
			if err == nil && !out.OK {
				t.Error("guest answer not read")
			}
			return err
		}},
		{"UnregisterGuardTask", ok, []string{`"owner":"` + tOwner + `"`}, func(b psBuilder) error { return b.UnregisterGuardTask(ctx, n, tOwner) }},
		{"RemoveVM", `{"left":[]}`, []string{`"owner":"` + tOwner + `"`}, func(b psBuilder) error { _, err := b.RemoveVM(ctx, tVMID, p); return err }},
		{"RemoveSwitch", `{"kept":""}`, []string{tSwitchID}, func(b psBuilder) error { _, err := b.RemoveSwitch(ctx, p); return err }},
		{"RemoveNAT", `{"kept":""}`, []string{`"prefix":"172.31.250.0/29"`}, func(b psBuilder) error { _, err := b.RemoveNAT(ctx, n, sub, tSwitchID); return err }},
		{"RemoveFirewall", `{"kept":""}`, []string{`"owner":"` + tOwner + `"`}, func(b psBuilder) error { _, err := b.RemoveFirewall(ctx, p); return err }},
		{"RemoveGuardian", ok, nil, func(b psBuilder) error { return b.RemoveGuardian(ctx, strings.Repeat("1", 64)) }},
		{"ClearProof", `{"left":["the proof pktmon session"]}`, []string{`"group":"CashPilotVM-proof"`, `"elementPrefix":"CashPilot Secure VM proof CashPilot-VM."`},
			func(b psBuilder) error {
				got, err := b.ClearProof(ctx, n, tOwner)
				if err == nil && len(got.Left) != 1 {
					t.Errorf("left %v", got)
				}
				return err
			}},
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
			_, _, _, _, err := b.EnsureSwitch(ctx, n, sub, "x'; Remove-VM *", "00155D000002", "", tAdapterID)
			return err
		},
		"lab names elsewhere": func(b psBuilder) error {
			m := n
			m.VM = "Other"
			_, err := b.EnsureNAT(ctx, m, sub, "")
			return err
		},
		"ISO path with a quote": func(b psBuilder) error {
			_, _, _, _, err := b.CreateVM(ctx, p, `D:\iso\w'.iso`, `C:\a.iso`)
			return err
		},
		"relative ISO path":    func(b psBuilder) error { _, _, _, _, err := b.CreateVM(ctx, p, `w.iso`, `C:\a.iso`); return err },
		"integration not GUID": func(b psBuilder) error { return b.SetIntegration(ctx, tVMID, tOwner, map[string]bool{"x": true}) },
		"binding not planned": func(b psBuilder) error {
			q := p
			q.BindingsOff = []string{"ms_tcpip"}
			_, err := b.SetBindings(ctx, q, 20)
			return err
		},
		"public guest address": func(b psBuilder) error {
			return b.SetHostNeighbor(ctx, 20, tSwitchID, tOwner, tAdapterID, netip.MustParseAddr("8.8.8.8"), "00155D0A0002")
		},
		"SID not a SID":      func(b psBuilder) error { _, err := b.AddHyperVAdmin(ctx, "Everyone"); return err },
		"empty answer file":  func(b psBuilder) error { return b.WriteAnswerISO(ctx, `C:\a.iso`, nil) },
		"guard exe relative": func(b psBuilder) error { return b.RegisterGuardTask(ctx, n, `guard.exe`, tOwner) },
		"guest script unknown": func(b psBuilder) error {
			return b.Guest(ctx, tVMID, tOwner, Cred{User: "cpadmin", Password: testPassword(1)}, GuestScript{Name: "x", Text: "Remove-Item C:\\"}, nil, nil)
		},
		"guest user not admin": func(b psBuilder) error {
			return b.Guest(ctx, tVMID, tOwner, Cred{User: "earner", Password: testPassword(1)}, scriptGuestPing, nil, nil)
		},
		"remove VM not a GUID": func(b psBuilder) error { _, err := b.RemoveVM(ctx, "*", p); return err },
		"remove switch by name": func(b psBuilder) error {
			q := p
			q.SwitchID = "CashPilot-VM"
			_, err := b.RemoveSwitch(ctx, q)
			return err
		},
		"remove firewall no owner": func(b psBuilder) error {
			q := p
			q.Owner = ""
			_, err := b.RemoveFirewall(ctx, q)
			return err
		},
		"unregister no owner": func(b psBuilder) error { return b.UnregisterGuardTask(ctx, n, "") },
		"inventory VM ID not a GUID": func(b psBuilder) error {
			_, err := b.Inventory(ctx, n, KnownIDs{VM: "*"}, "")
			return err
		},
		"ensure switch ID not a GUID": func(b psBuilder) error {
			_, _, _, _, err := b.EnsureSwitch(ctx, n, sub, tOwner, "00155D000002", "x' -or 1", tAdapterID)
			return err
		},
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

// TestBootKeysStartsTheVMAndPressesAtOnce: the boot prompt shows for about
// 5 seconds. On live run 5 the VM was started by one script and the keys
// came from a second one, which once started too late; the install then sat
// at "No operating system was loaded" and Setup waited its full two hours.
// The key script now starts the VM itself and presses for 20 seconds.
func TestBootKeysStartsTheVMAndPressesAtOnce(t *testing.T) {
	const mock = `
param($vmId, $owner)
$global:log = @()
function Get-VM { [pscustomobject]@{ Id = $vmId; Notes = ('cashpilot-owner=' + $owner); State = $(if ($global:started) { 'Running' } else { 'Off' }) } }
function Get-CimInstance { [CmdletBinding()] param($Namespace, $ClassName, $Filter) [pscustomobject]@{ Name = 'vm' } }
function Start-VM { param($VM) $global:started = $true; $global:log += 'start' }
function Get-CimAssociatedInstance { [CmdletBinding()] param($InputObject, $ResultClassName) if ($global:started) { [pscustomobject]@{ Kind = 'keyboard' } } }
function Invoke-CimMethod { [CmdletBinding()] param($InputObject, $MethodName, $Arguments) $global:log += 'key' }
function Start-Sleep { param([int]$Milliseconds, [int]$Seconds) $global:log += ('sleep' + ($Milliseconds + 1000 * $Seconds)) }
`
	check := scriptBootKeys + "\nWrite-Host ('LOG ' + ($global:log -join ','))\n"
	code, out := runOwnedScriptWithFake(t, mock, check, map[string]any{"vm": tVMID, "owner": tOwner}, tVMID, tOwner)
	s := string(out)
	i := strings.Index(s, "LOG ")
	if code != 0 || i < 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	// The builder decodes the answer strictly, so an extra field fails the
	// install as it did on live run 5 ("unknown field").
	var w okWire
	doc, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s[:i]))
	if err == nil {
		err = decodeDoc(doc, &w)
	}
	if err != nil || !w.OK {
		t.Fatalf("the answer does not decode as the builder reads it: %v (%q)", err, s[:i])
	}
	log := strings.Split(strings.TrimSpace(s[i+4:]), ",")
	if len(log) < 2 || log[0] != "start" || log[1] != "key" {
		t.Fatalf("the VM must start and get its first key with no wait in between: %v", log)
	}
	keys, waited := 0, 0
	for _, e := range log {
		if e == "key" {
			keys++
		} else if ms, ok := strings.CutPrefix(e, "sleep"); ok {
			n, _ := strconv.Atoi(ms)
			waited += n
		}
	}
	if keys < 30 || waited < 15000 || waited/keys > 1000 {
		t.Fatalf("%d keys over %d ms; want a key at least every second for 15 s", keys, waited)
	}
}

func TestParsePktmonComponents(t *testing.T) {
	cs, err := parsePktmonComponents([]byte(`[{"Group":"Adapters","Components":[{"Id":4,"Name":"Ethernet","Type":2,"Properties":[{"Name":"IfIndex","Value":"7"}]},{"ID":9,"Name":"vEthernet","Type":6,"ifIndex":20},{"Id":11,"Name":"filter","Type":3}]}]`))
	if err != nil {
		t.Fatal(err)
	}
	if cs[4].IfIndex != 7 || cs[9].IfIndex != 20 || len(cs) != 2 {
		t.Fatalf("%+v", cs)
	}
	if _, err := parsePktmonComponents([]byte(`{"nothing":[]}`)); err == nil {
		t.Fatal("a list with no component was accepted")
	}
}

// pktmonListLive has the shape of pktmon list --json on Windows 11 (live run
// 5, addresses replaced): each NIC has its own entry and an NDIS twin, type
// Miniport with a SecondaryId, under the same ID.
const pktmonListLive = `[{"Group":"Adapters","Components":[
{"Name":"VMS ExtMp #4","DriverName":"vmswitch.sys","Type":"Extension Miniport","Id":152,"Properties":[{"Name":"ifIndex","Value":28},{"Name":"Switch name","Value":"cpd-svm-1"}],"Counters":[]},
{"Name":"VMS ExtMp #4","DriverName":"vmswitch.sys","Type":"Miniport","Id":152,"SecondaryId":149,"Properties":[{"Name":"NdisMedium","Value":"Ethernet"},{"Name":"ifIndex","Value":28}],"Counters":[]},
{"Name":"VMS Ext Filter","DriverName":"vmswitch.sys","Type":"Filter","Id":150,"Properties":[{"Name":"ifIndex","Value":29}],"Counters":[]},
{"Name":"vEthernet (cpd-svm-1)","DriverName":"vmswitch.sys","Type":"Host vNic","Id":168,"Properties":[{"Name":"Ext ifIndex","Value":28},{"Name":"Switch name","Value":"cpd-svm-1"},{"Name":"ifIndex","Value":30}],"Counters":[]},
{"Name":"Host vNic #4","DriverName":"VmsProxyHNic.sys","Type":"Miniport","Id":168,"SecondaryId":153,"Properties":[{"Name":"NdisMedium","Value":"Ethernet"},{"Name":"ifIndex","Value":30}],"Counters":[]}]},
{"Group":"Adapters","Components":[
{"Name":"Microsoft NetVsc Nic","DriverName":"NetVsc.sys","Type":"NetVsc","Id":74,"Properties":[{"Name":"ifIndex","Value":11}],"Counters":[]},
{"Name":"Microsoft NetVsc Nic","DriverName":"netvsc.sys","Type":"Miniport","Id":74,"SecondaryId":5,"Properties":[{"Name":"ifIndex","Value":11}],"Counters":[]}]}]`

// TestReconcilePktmonWithWindowsOutput: on live run 5 every proof step broke
// with "pktmon trace NIC 74 (NetVsc.sys) differs from the pre-capture
// inventory". The list names a component in words and adds an NDIS twin; the
// ETL names it by number, driver file and a description. Both must agree on
// ID, type, driver and interface, and a changed interface must still fail.
func TestReconcilePktmonWithWindowsOutput(t *testing.T) {
	want, err := parsePktmonComponents([]byte(pktmonListLive))
	if err != nil {
		t.Fatal(err)
	}
	// Event 20 and 30 of the ETL the proof captured, as scriptPktmonStop sends them.
	etl := []pktComp{
		{ID: 168, Type: 6, Driver: "vmswitch.sys", Name: "cpd-svm-1", IfIndex: 30},
		{ID: 152, Type: 7, Driver: "vmswitch.sys", Name: "", IfIndex: 28},
		{ID: 74, Type: 9, Driver: "NetVsc.sys", Name: "Microsoft Hyper-V Network Adapter", IfIndex: 11},
		{ID: 169, Type: 10, Driver: "http.sys", Name: "HTTP", IfIndex: 30},
	}
	if err := reconcilePktmon(want, etl); err != nil {
		t.Fatalf("the same NICs did not reconcile: %v (inventory %+v)", err, want)
	}
	moved := slices.Clone(etl)
	moved[2].IfIndex = 12
	if err := reconcilePktmon(want, moved); err == nil {
		t.Fatal("a NIC on another interface was accepted")
	}
	other := slices.Clone(etl)
	other[0].Driver = "other.sys"
	if err := reconcilePktmon(want, other); err == nil {
		t.Fatal("a NIC with another driver was accepted")
	}
	if err := reconcilePktmon(want, etl[1:]); err == nil {
		t.Fatal("a trace missing a NIC was accepted")
	}
	// Setup 1i: the trace also named the host vNIC's NDIS twin by its
	// SecondaryId, which the list gives no entry of its own.
	twin := append(slices.Clone(etl), pktComp{ID: 153, Type: 2, Driver: "VmsProxyHNic.sys", Name: "Hyper-V Virtual Ethernet Adapter #4", IfIndex: 30})
	if err := reconcilePktmon(want, twin); err != nil {
		t.Fatalf("the host vNIC's NDIS twin did not reconcile: %v", err)
	}
	twin[len(twin)-1].IfIndex = 12
	if err := reconcilePktmon(want, twin); err == nil {
		t.Fatal("an NDIS twin on another interface was accepted")
	}
	for id, c := range want {
		if c.Twin && id != 153 && id != 149 && id != 5 {
			t.Errorf("component %d is marked a twin: %+v", id, c)
		}
	}
	if c := want[168]; c.Twin || c.Type != 6 {
		t.Errorf("the host vNIC lost its own entry: %+v", c)
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
	p := &psProber{h: h, n: n, program: n.GuardExe(), tmp: filepath.Join(t.TempDir(), proofDir), owner: tOwner, switchID: tSwitchID, listen: func(string, string) (net.Listener, error) {
		var err error
		ln, err = net.Listen("tcp", "127.0.0.1:0")
		return ln, err
	}}
	steps := []struct {
		script  string
		answers []string
		call    func() error
	}{
		{"SetL3", []string{ok}, func() error { return p.SetL3(ctx, n, tOwner, false) }},
		{"SetBinding", []string{ok}, func() error { return p.SetBinding(ctx, 20, "ms_tcpip6", true) }},
		{"LinkLocal", []string{`{"addrs":["fe80::1"]}`}, func() error { _, err := p.LinkLocal(ctx, 20); return err }},
		{"CanaryRule", []string{`{"identity":"` + strings.Repeat("1", 64) + `"}`, `{"identity":""}`}, func() error {
			c, err := p.Canary(ctx, netip.MustParseAddr("192.0.2.10"), 45000, netip.MustParsePrefix("192.0.2.11/32"))
			if err != nil {
				return err
			}
			if a := r.args[len(r.args)-1]; !strings.Contains(a, `"remote":"192.0.2.11/32"`) {
				t.Errorf("the canary rule does not take only the guest's address: %s", a)
			}
			if a := r.args[len(r.args)-1]; !strings.Contains(a, `"desc":"CashPilot Secure VM proof canary. cashpilot-owner=`+tOwner+`"`) {
				t.Errorf("the canary rule does not carry the owner token: %s", a)
			}
			if _, err := p.forPlan(Plan{}).Canary(ctx, netip.MustParseAddr("192.0.2.10"), 45002, netip.MustParsePrefix("192.0.2.11/32")); err == nil {
				t.Error("a canary with no owner token was opened")
			}
			if _, err := p.forPlan(Plan{}).HVListener(ctx, tVMID, tVMID, true); err == nil {
				t.Error("an hv_sock test key with no owner token was registered")
			}
			if _, err := p.Canary(ctx, netip.MustParseAddr("192.0.2.10"), 45001, netip.Prefix{}); err == nil {
				t.Error("a canary with no guest address was opened")
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
	// The component and host snapshots must precede the capture.
	mockCaptureStart(h, r)
	dst := netip.MustParseAddr("198.51.100.7")
	list := `[{"Components":[{"Id":4,"Name":"Ethernet","Type":2,"ifIndex":7},{"Id":9,"Name":"vEthernet","Type":6,"ifIndex":20}]}]`
	r.answers = append(r.answers, `{"list":`+jsonQuote(list)+`}`, cannedHostNet, ok)
	c, err := p.Pktmon(ctx, []PktFilter{{Addr: dst, Proto: "TCP", Port: 45000}})
	if err != nil {
		t.Fatal(err)
	}
	dir := c.(*psCapture).dir
	out := filepath.Join(dir, "c4.pcapng")
	vnic := filepath.Join(dir, "c9.pcapng")
	os.WriteFile(out, pcapng(ipv4TCP(netip.MustParseAddr("192.0.2.10"), dst, 45000), ipv4TCP(netip.MustParseAddr("192.0.2.10"), dst, 1)), 0o600)
	os.WriteFile(vnic, pcapng(ipv4TCP(netip.MustParseAddr("172.31.250.2"), dst, 45000)), 0o600)
	r.answers = append(r.answers, `{"components":[{"id":4,"type":2,"name":"Ethernet","ifIndex":7},{"id":9,"type":6,"name":"vEthernet","ifIndex":20}]}`,
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

func mockCaptureStart(h *psHost, r *fakeRun) {
	h.run = func(ctx context.Context, script string, arg []byte) ([]byte, error) {
		if script == scriptPktmonStart {
			var in map[string]string
			if err := json.Unmarshal(arg, &in); err != nil {
				return nil, err
			}
			if err := os.WriteFile(in["etl"], []byte("test capture"), 0o600); err != nil {
				return nil, err
			}
		}
		return r.run(ctx, script, arg)
	}
}

func TestPktmonRefusesMissingComponentExport(t *testing.T) {
	ctx := context.Background()
	const list = `[{"Components":[{"Id":4,"Name":"Ethernet","Type":2,"ifIndex":7},{"Id":9,"Name":"vEthernet","Type":6,"ifIndex":20}]}]`
	h, r, _ := newSeam(`{"list":`+jsonQuote(list)+`}`, cannedHostNet, `{"ok":true}`)
	mockCaptureStart(h, r)
	p := &psProber{h: h, owner: tOwner, switchID: tSwitchID, tmp: filepath.Join(t.TempDir(), proofDir)}
	capture, err := p.Pktmon(ctx, []PktFilter{{Addr: netip.MustParseAddr("192.0.2.50")}})
	if err != nil {
		t.Fatal(err)
	}
	dir := capture.(*psCapture).dir
	out := filepath.Join(dir, "c4.pcapng")
	packet := ipv4TCP(netip.MustParseAddr("192.0.2.10"), netip.MustParseAddr("192.0.2.20"), 12345)
	if err := os.WriteFile(out, pcapng(packet), 0o600); err != nil {
		t.Fatal(err)
	}
	// Stop does not supply a new inventory. A capture with the component
	// present at start must fail if that component has no export.
	r.answers = append(r.answers, `{"components":[{"id":4,"type":2,"name":"Ethernet","ifIndex":7},{"id":9,"type":6,"name":"vEthernet","ifIndex":20}]}`, `{"files":[{"id":4,"path":`+jsonQuote(out)+`}]}`)
	if _, err := capture.Stop(ctx); err == nil || !strings.Contains(err.Error(), "component 9") {
		t.Fatalf("Stop = %v, want a missing export error for component 9", err)
	}
}

// TestPktmonStopDeletesTheCaptureWindowsRewrote: pktmon stop writes the
// capture to a new file at the same path (live run 5: the 16 MiB file from
// start became a 34 KiB file with a new file ID). Stop must record the new
// file, or it is never deleted: each proof left its capture folder behind,
// and the next Setup's proof refused the folder its journal did not know.
func TestPktmonStopDeletesTheCaptureWindowsRewrote(t *testing.T) {
	ctx := context.Background()
	const list = `[{"Components":[{"Id":4,"Name":"Ethernet","Type":2,"ifIndex":7}]}]`
	h, r, _ := newSeam(`{"list":`+jsonQuote(list)+`}`, cannedHostNet, `{"ok":true}`)
	mockCaptureStart(h, r)
	start := h.run
	h.run = func(ctx context.Context, script string, arg []byte) ([]byte, error) {
		if script == scriptPktmonStop {
			var in map[string]string
			if err := json.Unmarshal(arg, &in); err != nil {
				return nil, err
			}
			// The old file lives on under another name, so the new one
			// cannot reuse its ID.
			if err := os.Rename(in["etl"], in["etl"]+".old"); err != nil {
				return nil, err
			}
			defer os.Remove(in["etl"] + ".old")
			if err := os.WriteFile(in["etl"], []byte("final capture"), 0o600); err != nil {
				return nil, err
			}
		}
		return start(ctx, script, arg)
	}
	p := &psProber{h: h, owner: tOwner, switchID: tSwitchID, tmp: filepath.Join(t.TempDir(), proofDir)}
	capture, err := p.Pktmon(ctx, []PktFilter{{Addr: netip.MustParseAddr("192.0.2.50")}})
	if err != nil {
		t.Fatal(err)
	}
	dir := capture.(*psCapture).dir
	out := filepath.Join(dir, "c4.pcapng")
	packet := ipv4TCP(netip.MustParseAddr("192.0.2.10"), netip.MustParseAddr("192.0.2.20"), 12345)
	if err := os.WriteFile(out, pcapng(packet), 0o600); err != nil {
		t.Fatal(err)
	}
	r.answers = append(r.answers, `{"components":[{"id":4,"type":2,"name":"Ethernet","ifIndex":7}]}`, `{"files":[{"id":4,"path":`+jsonQuote(out)+`}]}`)
	if _, err := capture.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
		left, _ := os.ReadDir(dir)
		t.Fatalf("Stop left its capture folder: %v %v", err, left)
	}
}

func TestHeaderOnlyPktmonExportLeavesProofUnproven(t *testing.T) {
	ctx := context.Background()
	const list = `[{"Components":[{"Id":4,"Name":"Ethernet","Type":2,"ifIndex":7},{"Id":9,"Name":"vEthernet","Type":6,"ifIndex":20}]}]`
	const metadata = `{"components":[{"id":4,"type":2,"name":"Ethernet","ifIndex":7},{"id":9,"type":6,"name":"vEthernet","ifIndex":20}]}`
	dst := netip.MustParseAddr("192.0.2.50")
	unrelated := pcapng(ipv4TCP(netip.MustParseAddr("192.0.2.10"), netip.MustParseAddr("192.0.2.20"), 45000))
	for _, tc := range []struct {
		name  string
		data  []byte
		green bool
	}{
		{"both headers and no packets", pcapng(), false},
		{"packets outside the proof filter", unrelated, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, r, _ := newSeam(`{"list":`+jsonQuote(list)+`}`, cannedHostNet, `{"ok":true}`)
			mockCaptureStart(h, r)
			p := &psProber{h: h, owner: tOwner, switchID: tSwitchID, tmp: filepath.Join(t.TempDir(), proofDir)}
			capture, err := p.Pktmon(ctx, []PktFilter{{Addr: dst, Proto: "TCP", Port: 45000}})
			if err != nil {
				t.Fatal(err)
			}
			dir := capture.(*psCapture).dir
			physical, vnic := filepath.Join(dir, "c4.pcapng"), filepath.Join(dir, "c9.pcapng")
			if err := os.WriteFile(physical, tc.data, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(vnic, unrelated, 0o600); err != nil {
				t.Fatal(err)
			}
			r.answers = append(r.answers, metadata, `{"files":[{"id":4,"path":`+jsonQuote(physical)+`},{"id":9,"path":`+jsonQuote(vnic)+`}]}`)
			run := proofRun{Runner: Runner{Sleep: func(context.Context, time.Duration) error { return nil }}}
			probe := probeSet{r: &run, capture: capture}
			evidence := Evidence{DropDelta: 1}
			probe.collect(ctx, &evidence)

			var results []StepResult
			for _, step := range Steps(Plan{}, HostNet{}, ProofTargets{LANIP: dst, Router: dst, OwnIP: dst, VNICIfIndex: 20}) {
				if step.ID == StepGreen {
					results = append(results, Judge(step, evidence))
					continue
				}
				witness := Evidence{DropDelta: 1, Delivered: true}
				if step.Kind == Red {
					witness.Arrived = []string{"red control"}
				}
				results = append(results, Judge(step, witness))
			}
			green, why := Verdict(results)
			if green != tc.green || (!tc.green && !strings.Contains(why, "component 4")) {
				t.Fatalf("proof green = %v (%s), want %v; capture evidence: %+v", green, why, tc.green, evidence)
			}
		})
	}
}

func TestProofFirewallKeepsForeignRules(t *testing.T) {
	const mock = `
function Get-NetFirewallRule {
  param($Name, $Group, $ErrorAction)
  [pscustomobject]@{ Name = 'owned'; Group = $g; Description = $desc; Enabled = 'True' }
  if (-not $Name) { [pscustomobject]@{ Name = 'foreign'; Group = $g; Description = 'some other rule' } }
}
function Remove-NetFirewallRule {
  [CmdletBinding()]
  param([Parameter(ValueFromPipeline)]$InputObject, $Name)
  process {
    if ($InputObject.Name -eq 'foreign' -or $Name -eq 'foreign') { throw 'removed a foreign rule' }
  }
}
function Set-NetFirewallRule {
  [CmdletBinding()]
  param([Parameter(ValueFromPipeline)]$InputObject, $Enabled)
  process {
    if ($InputObject.Name -eq 'foreign') { throw 'toggled a foreign rule' }
  }
}
`
	for _, tc := range []struct {
		name, script, group, desc string
		arg                       map[string]any
	}{
		{"toggle", scriptSetL3, "CashPilotVM", "CashPilot Secure VM. " + ownerTag + tOwner, map[string]any{"on": false}},
		{"teardown", scriptCanaryRule, "CashPilotVM-proof", proofCanaryDesc(tOwner), map[string]any{"add": false, "port": 42000}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.arg["group"], tc.arg["desc"], tc.arg["owner"], tc.arg["id"] = tc.group, tc.desc, tOwner, "owned"
			tc.arg["identity"] = "recorded-fingerprint"
			s := "param($g, $desc)\n" + mock
			if code, out := runOwnedScriptWithFake(t, s,
				firewallPrelude+"\nfunction Get-FirewallIdentity($r) { 'recorded-fingerprint' }\n"+strings.TrimPrefix(tc.script, firewallPrelude),
				tc.arg, tc.group, tc.desc); code != 0 {
				t.Fatalf("exit %d: %s", code, out)
			}
		})
	}
}

func TestPktmonKeepsForeignFilters(t *testing.T) {
	const mock = `
function Get-EtwTraceSession {}
function Join-Path { 'Invoke-FakePktmon' }
function Start-Sleep {}
function Invoke-FakePktmon {
  if ($args[0] -eq 'filter') { throw 'changed a host pktmon filter' }
  $global:LASTEXITCODE = 0
}
`
	if code, out := runOwnedScriptWithFake(t, mock, scriptPktmonStart, map[string]any{"etl": "capture.etl"}); code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
}

func TestPktmonStopReadsETLMetadataByProviderID(t *testing.T) {
	const mock = `
param($filtered)
function Get-EtwTraceSession { [pscustomobject]@{ LocalFilePath = 'capture.etl' } }
function Join-Path { 'Invoke-FakePktmon' }
function Invoke-FakePktmon {
  if ($args[0] -ne 'stop') { throw 'expected pktmon stop' }
  $global:LASTEXITCODE = 0
}
function New-Metadata([guid]$provider, [int]$id, [object[]]$values) {
  [pscustomobject]@{ ProviderId = $provider; Id = $id; Properties = @($values | ForEach-Object { [pscustomobject]@{ Value = $_ } }) }
}
function Get-WinEvent {
  [CmdletBinding()]
  param([hashtable]$FilterHashtable, [switch]$Oldest)
  if ($FilterHashtable.ContainsKey('ProviderName')) { throw 'No events were found that match the specified selection criteria.' }
  if (-not $Oldest -or $FilterHashtable.Path -ne 'capture.etl' -or ($FilterHashtable.Id -join ',') -ne '20,30,100,110') {
    throw 'unexpected query for an ETL metadata reading'
  }
  $pktmon = '4d4f80d9-c8bd-4d73-bb5b-19c90402c5ac'
  $other = '9b79ee91-b5fd-41c0-a243-4248e266e9d0'
  New-Metadata $pktmon 20 @(4, 2, 'e1d.sys', 'Ethernet')
  New-Metadata $pktmon 30 @(4, 1, 7)
  New-Metadata $other 30 @(4, 1, 99)
  New-Metadata $other 100 @(1)
  if ($filtered -eq 'yes') { New-Metadata $pktmon 110 @(1) }
}
`
	const fakeDisk = `Add-Type 'public static class CpdDisk { public static string Identify(string path) { return "capture-id"; } }'
`
	script := strings.Replace(scriptPktmonStop, diskIdentityPrelude, fakeDisk, 1)
	for _, filtered := range []string{"no", "yes"} {
		t.Run(filtered, func(t *testing.T) {
			code, out := runOwnedScriptWithFake(t, mock, script, map[string]any{"etl": "capture.etl", "etlId": "capture-id"}, filtered)
			doc, err := decodeOutput(out)
			if code != 0 || err != nil {
				t.Fatalf("stop exit %d, decode %v: %s", code, err, out)
			}
			var got struct {
				Components []pktComp
				Filtered   bool
			}
			if err := decodeDoc(doc, &got); err != nil {
				t.Fatal(err)
			}
			want := []pktComp{{ID: 4, Type: 2, Driver: "e1d.sys", Name: "Ethernet", IfIndex: 7}}
			if !slices.Equal(got.Components, want) || got.Filtered != (filtered == "yes") {
				t.Fatalf("metadata = %+v, want components %+v and filtered=%s", got, want, filtered)
			}
		})
	}
}

func runOwnedScriptWithFake(t *testing.T, mock, script string, arg map[string]any, args ...string) (int, []byte) {
	t.Helper()
	raw, err := json.Marshal(arg)
	if err != nil {
		t.Fatal(err)
	}
	return runFakePowerShell(t,
		mock+"\n$env:CPD_HV_ARG = '"+base64.StdEncoding.EncodeToString(raw)+"'\n"+script, args...)
}

func TestEnsureNATScriptChecksCurrentInventory(t *testing.T) {
	const mock = `
param($mine, $foreign, $unreadable)
function Get-NetNat {
  [CmdletBinding()] param()
  if ($unreadable) { Write-Error 'NAT inventory unavailable' }
  if ($mine) { [pscustomobject]@{ Name = 'CashPilot-VM'; InstanceID = 'owned-id'; InternalIPInterfaceAddressPrefix = '172.31.250.0/29' } }
  if ($foreign) { [pscustomobject]@{ Name = 'OtherWorkload'; InstanceID = 'foreign-id'; InternalIPInterfaceAddressPrefix = '10.250.255.0/30' } }
}
function New-NetNat { [pscustomobject]@{ InstanceID = 'created-id' } }
`
	for _, tc := range []struct {
		name, mine, foreign, unreadable string
		want                            string
		ok                              bool
	}{
		{name: "empty inventory", want: "created-id", ok: true},
		{name: "owned NAT", mine: "yes", want: "owned-id", ok: true},
		{name: "foreign NAT", foreign: "yes", want: "OtherWorkload"},
		{name: "owned and foreign NATs", mine: "yes", foreign: "yes", want: "OtherWorkload"},
		{name: "unreadable inventory", unreadable: "yes", want: "NAT inventory unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out := runOwnedScriptWithFake(t, mock, scriptEnsureNAT,
				map[string]any{"nat": "CashPilot-VM", "prefix": "172.31.250.0/29", "id": "owned-id"},
				tc.mine, tc.foreign, tc.unreadable)
			if tc.ok && code == 0 {
				decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
				if err != nil {
					t.Fatalf("decode NAT identity: %v", err)
				}
				out = decoded
			}
			if (code == 0) != tc.ok || !strings.Contains(string(out), tc.want) {
				t.Fatalf("exit = %d, output = %s, want success = %t and %q", code, out, tc.ok, tc.want)
			}
		})
	}
}

// TestEnsureNATScriptAcceptsItsOwnNameKeyedNAT: the live provider keys every
// NAT's ID by its name. A Setup rerun reads back the NAT it recorded; one
// with another prefix is refused.
func TestEnsureNATScriptAcceptsItsOwnNameKeyedNAT(t *testing.T) {
	const mock = `
param($prefix)
function Get-NetNat {
  [pscustomobject]@{ Name = 'CashPilot-VM'; InstanceID = 'CashPilot-VM;0'; InternalIPInterfaceAddressPrefix = $prefix }
}
function New-NetNat { throw 'would create a second NAT' }
`
	for _, tc := range []struct {
		prefix string
		ok     bool
	}{{"172.31.250.0/29", true}, {"172.31.251.0/29", false}} {
		code, out := runOwnedScriptWithFake(t, mock, scriptEnsureNAT,
			map[string]any{"nat": "CashPilot-VM", "id": "CashPilot-VM;0", "prefix": "172.31.250.0/29"}, tc.prefix)
		if tc.ok {
			doc, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
			if code != 0 || err != nil || !strings.Contains(string(doc), "CashPilot-VM;0") {
				t.Fatalf("%s: exit = %d, output = %s, want the recorded NAT read back", tc.prefix, code, out)
			}
			continue
		}
		if code == 0 || !strings.Contains(string(out), `A NAT network named "CashPilot-VM" already exists on 172.31.251.0/29`) {
			t.Fatalf("%s: exit = %d, output = %s, want the foreign NAT refusal", tc.prefix, code, out)
		}
	}
}

func TestPktmonRejectsEmptyExport(t *testing.T) {
	if _, err := parsePcapng(nil); err == nil {
		t.Fatal("the empty export counted as capture evidence")
	}
}

func TestDiskUseIncludesDifferencingParents(t *testing.T) {
	const mock = `
function Get-VM { [pscustomobject]@{ Name = 'other-vm' } }
function Get-VMSnapshot { 'checkpoint' }
function Get-VMHardDiskDrive {
  param($VM, $VMSnapshot)
  if ($VMSnapshot) { [pscustomobject]@{ Path = '/checkpoint.avhdx' } }
  else { [pscustomobject]@{ Path = '/child.vhdx' } }
}
function Get-VHD {
  param($Path)
  if ($Path -eq '/checkpoint.avhdx') {
    [pscustomobject]@{ VhdType = 'Differencing'; ParentPath = '/parent-alias.vhdx' }
  } else { [pscustomobject]@{ VhdType = 'Dynamic' } }
}
`
	code, out := runOwnedScriptWithFake(t, mock, hvPrelude+diskIdentityPrelude+`
function Get-DiskIdentity([string]$path) {
  if ($path -like '*parent*') { return 'parent-id' }
  return $path
}
if (-not (Test-DiskInUse 'parent-id')) { exit 23 }
`, nil)
	if code != 0 {
		t.Fatalf("did not protect the recorded disk behind a checkpoint child: exit %d: %s", code, out)
	}
}

func TestEnsureSwitchAdoptsAssignedManagementMAC(t *testing.T) {
	const mock = `
param($fakeSwitch, $assignedMAC, $effectiveMAC)
function Get-VMSwitch { $script:switch }
function New-VMSwitch($Name, $SwitchType, $Notes) {
  $script:switch = [pscustomobject]@{ Id = $fakeSwitch; Name = $Name; SwitchType = $SwitchType; Notes = $Notes }
  $script:switch
}
function Get-VMNetworkAdapter {
  param([switch]$ManagementOS)
  if (-not $ManagementOS) { throw 'expected a management OS lookup' }
  [pscustomobject]@{ SwitchId = $fakeSwitch; Id = 'management-port'; DeviceId = 'device'; MacAddress = $assignedMAC; IsManagementOs = $true }
}
function Set-VMNetworkAdapter($VMNetworkAdapter, $StaticMacAddress) {
  if ($VMNetworkAdapter.IsManagementOs -and $StaticMacAddress) {
    throw 'The specified parameters cannot be set for a management OS adapter.'
  }
  throw 'unexpected adapter change'
}
function Get-NetAdapter { [pscustomobject]@{ DeviceId = 'device'; ifIndex = 20; MacAddress = $effectiveMAC } }
function Get-NetIPInterface { [pscustomobject]@{ Dhcp = 'Disabled' } }
function Get-NetIPAddress { [pscustomobject]@{ IPAddress = '172.31.250.1'; PrefixLength = 29 } }
`
	for _, tc := range []struct {
		name, assignedMAC, effectiveMAC, wantError string
	}{
		{name: "Hyper-V assignment", assignedMAC: "00155D47E3A1", effectiveMAC: "00-15-5D-47-E3-A1"},
		{name: "unexpected prefix", assignedMAC: "02155D47E3A1", effectiveMAC: "02155D47E3A1", wantError: "00-15-5D"},
		{name: "different effective MAC", assignedMAC: "00155D47E3A1", effectiveMAC: "00155D47E3A2", wantError: "does not match the Hyper-V port MAC"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out := runOwnedScriptWithFake(t, mock, scriptEnsureSwitch, map[string]any{
				"switch": "CashPilot-VM", "owner": tOwner, "mac": "00155D000002",
				"gateway": "172.31.250.1", "prefixLen": 29,
			}, tSwitchID, tc.assignedMAC, tc.effectiveMAC)
			if tc.wantError != "" {
				if code == 0 || !strings.Contains(string(out), tc.wantError) {
					t.Fatalf("exit %d, output %s, want %q", code, out, tc.wantError)
				}
				return
			}
			if code != 0 {
				t.Fatalf("exit %d: %s", code, out)
			}
			raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				MAC string
			}
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if got.MAC != tc.assignedMAC {
				t.Fatalf("reported MAC = %q, want %q", got.MAC, tc.assignedMAC)
			}
		})
	}
}

func TestSwitchGatewayUsesManagementAdapterIdentity(t *testing.T) {
	const mock = `
function Get-VMSwitch { [pscustomobject]@{ Id = $fakeSwitch; Name = 'CashPilot-VM'; SwitchType = 'Internal'; Notes = ('cashpilot-owner=' + $owner) } }
function Get-VMNetworkAdapter {
  param([switch]$ManagementOS, [switch]$All)
  [pscustomobject]@{ SwitchId = $fakeSwitch; Id = "management-port"; DeviceId = 'management-device'; MacAddress = '00155D000002'; IsManagementOs = $true }
}
function Get-NetAdapter {
  param([switch]$IncludeHidden)
  [pscustomobject]@{ DeviceId = 'foreign-device'; ifIndex = 7; MacAddress = '00-15-5D-00-00-02' }
  [pscustomobject]@{ DeviceId = 'management-device'; Name = 'management'; ifIndex = 20; MacAddress = $interfaceMAC }
}
function Get-NetIPInterface { [pscustomobject]@{ Dhcp = "Disabled" } }
function Get-NetIPAddress {
  param($InterfaceIndex, $AddressFamily, $ErrorAction)
  [pscustomobject]@{ IPAddress = '192.0.2.19'; PrefixLength = 24 }
}
function Remove-NetIPAddress { throw 'removed an independently configured address' }
function New-NetIPAddress {
  param($InterfaceIndex, $IPAddress, $PrefixLength)
  if ($InterfaceIndex -ne 20) { throw 'address written to a foreign adapter' }
}
`
	args := map[string]any{"switch": "CashPilot-VM", "switchId": tSwitchID, "managementId": "management-port", "owner": tOwner, "mac": "00155D000002", "gateway": "172.31.250.1", "prefixLen": 29}
	const params = "param($fakeSwitch, $owner, $interfaceMAC)\n"
	code, out := runOwnedScriptWithFake(t, params+mock, scriptEnsureSwitch, args, tSwitchID, tOwner, "00-15-5D-00-00-02")
	if code != 0 {
		t.Fatalf("could not configure the owned adapter without touching other addresses: exit %d: %s", code, out)
	}
	t.Run("Repair refuses effective MAC drift", func(t *testing.T) {
		code, out := runOwnedScriptWithFake(t, params+mock, scriptEnsureSwitch, args, tSwitchID, tOwner, "00-15-5D-00-00-03")
		if code == 0 || !strings.Contains(string(out), "management adapter MAC") {
			t.Fatalf("Repair accepted a changed interface MAC: exit %d: %s", code, out)
		}
	})
	t.Run("Remove preserves switch after effective MAC drift", func(t *testing.T) {
		const removeMock = mock + `
function Get-NetIPAddress {}
function Get-NetNeighbor {}
function Get-NetAdapterBinding { [pscustomobject]@{ Name = 'management'; ComponentID = 'ms_tcpip'; Enabled = $true } }
function Remove-VMSwitch { throw 'would remove the switch with a changed interface MAC' }
`
		args["bindings"] = map[string]bool{"ms_tcpip": true}
		code, out := runOwnedScriptWithFake(t, params+removeMock, scriptRemoveSwitch, args, tSwitchID, tOwner, "00-15-5D-00-00-03")
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
		if code != 0 || err != nil || !strings.Contains(string(raw), "MAC") {
			t.Fatalf("Remove did not report the changed MAC: exit %d: %s", code, out)
		}
	})
}

func TestVMStartAndDisconnectRecheckOwner(t *testing.T) {
	const mock = `
function Get-VM { [pscustomobject]@{ Id = $id; Notes = 'foreign'; State = 'Off' } }
function Get-VMNetworkAdapter { 'foreign-adapter' }
function Start-VM { throw 'mutated foreign VM' }
function Disconnect-VMNetworkAdapter { throw 'mutated foreign VM' }
`
	for name, script := range map[string]string{"start": scriptStart, "disconnect": scriptDisconnect} {
		t.Run(name, func(t *testing.T) {
			code, out := runOwnedScriptWithFake(t, "param($id)\n"+mock, script, map[string]any{"vm": tVMID, "owner": tOwner}, tVMID)
			if code == 0 || strings.Contains(string(out), "mutated foreign VM") || !strings.Contains(string(out), "owned VM") {
				t.Fatalf("exit %d: %s", code, out)
			}
		})
	}
}

func TestVMFilesRequirePrivateDirectoryLease(t *testing.T) {
	const mock = `
param($root, $public)
function Get-VM {}
function Open-CPDDirectory($path, $create, $tag) {
  if ($path -ne $root -and -not $path.StartsWith((Join-Path $root 'vm-'))) {
    throw 'asked to use a public directory'
  }
  $script:lease = $path
  [IO.MemoryStream]::new()
}
function New-VHD($Path, [switch]$Dynamic, $SizeBytes) {
  if (-not $script:lease -or (Split-Path -Parent $Path) -ne $script:lease) {
    throw 'disk used a path without the private directory lease'
  }
  throw 'reached disk creation under the private directory lease'
}
`
	root, public := t.TempDir(), t.TempDir()
	code, out := runOwnedScriptWithFake(t, mock, buildPrelude+scriptCreateVMBody,
		map[string]any{"dataDir": root, "vhdxDir": public, "vm": "CashPilot-VM", "owner": tOwner, "diskBytes": 1}, root, public)
	if code == 0 || !strings.Contains(string(out), "reached disk creation under the private directory lease") {
		t.Fatalf("exit %d: %s", code, out)
	}
}

func TestEjectLeavesUnrecordedDVDDrives(t *testing.T) {
	const mock = `
param($vmId, $owner)
function Get-VM { [pscustomobject]@{ Id = $vmId; Notes = ('cashpilot-owner=' + $owner) } }
$script:drives = @(
  [pscustomobject]@{ Id = 'recorded'; Path = 'windows.iso' },
  [pscustomobject]@{ Id = 'foreign'; Path = 'other.iso' }
)
function Get-VMDvdDrive { return $script:drives }
function Remove-VMDvdDrive {
  [CmdletBinding()]
  param([Parameter(ValueFromPipeline)]$VMDvdDrive)
  process {
    if ($VMDvdDrive.Id -ne 'recorded') { throw 'removed an unrecorded drive' }
    $script:drives = @($script:drives | Where-Object { $_.Id -ne $VMDvdDrive.Id })
  }
}
`
	code, out := runOwnedScriptWithFake(t, mock, scriptEjectDVDs,
		map[string]any{"vm": tVMID, "owner": tOwner, "dvds": []OwnedDVD{{ID: "recorded", Path: "windows.iso"}}}, tVMID, tOwner)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(out)))
	if err != nil || string(raw) != `{"left":1}` {
		t.Fatalf("want one left, got %s: %v", raw, err)
	}
}

func TestRepairKeepsIndependentPermanentNeighbors(t *testing.T) {
	const mock = `
param($fakeSwitch, $owner)
function Get-VMSwitch { [pscustomobject]@{ Id = $fakeSwitch; SwitchType = 'Internal'; Notes = ('cashpilot-owner=' + $owner) } }
function Get-VMNetworkAdapter { [pscustomobject]@{ SwitchId = $fakeSwitch; Id = "management-port"; DeviceId = 'device' } }
function Get-NetAdapter { [pscustomobject]@{ DeviceId = 'device'; ifIndex = 20 } }
function Get-NetNeighbor { [pscustomobject]@{ IPAddress = '172.31.250.3'; State = 'Permanent' } }
function Remove-NetNeighbor { throw 'removed an independently configured entry' }
function New-NetNeighbor {
  param($InterfaceIndex, $IPAddress, $LinkLayerAddress, $State)
  if ($InterfaceIndex -ne 20 -or $IPAddress -ne '172.31.250.2') { throw 'wrote the wrong entry' }
}
`
	code, out := runOwnedScriptWithFake(t, mock, scriptSetHostNeighbor,
		map[string]any{"switchId": tSwitchID, "managementId": "management-port", "owner": tOwner, "ifIndex": 20, "ip": "172.31.250.2", "mac": "00-15-5D-00-00-02"}, tSwitchID, tOwner)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
}
