package winvm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"time"
)

// The canned answers below have the shape of the lab host's real output: ACL
// rows as Hyper-V lists them (a Both rule as an Inbound and an Outbound row, a
// /32 as a bare address), the drop counter's instance name with a translated
// adapter name in the middle, an External vSwitch over the physical NIC, a
// tunnel adapter with a public route. Names are neutral and addresses come from
// the documentation ranges.

const (
	tVMID      = "5b8e2f0a-3c4d-4e5f-8a9b-0c1d2e3f4a5b"
	tAdapterID = "7A1B2C3D-4E5F-4061-8273-94A5B6C7D8E9"
	tSwitchID  = "c3d4e5f6-a7b8-49c0-8d1e-2f3a4b5c6d7e"
	tGCSKey    = "0000AAAA-FACB-11E6-BD58-64006A7986D3"
)

func seamPlan(t *testing.T) Plan {
	t.Helper()
	p, err := NewPlan(Options{
		Names:       ProductNames(),
		VHDXDir:     `C:\ProgramData\CashPilot\vm`,
		Subnet:      netip.MustParsePrefix("172.31.250.0/29"),
		HostVNICMAC: "00-15-5D-0A-00-01",
		GuestMAC:    "00-15-5D-0A-00-02",
		ExtraExits:  []netip.Addr{netip.MustParseAddr("203.0.113.7")},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	p.VMID, p.AdapterID, p.SwitchID = tVMID, tAdapterID, tSwitchID
	p.SetGCSBaseline([]string{tGCSKey})
	return p
}

const cannedHostNet = `{"physical":[7,9],
"interfaces":[
 {"ifIndex":7,"ifType":6,"physicalMedium":14,"hardwareInterface":true,"connectorPresent":true,"hidden":false,"componentId":"pci\\ven_8086&dev_15f3","description":"Ethernet Controller","vmSwitchId":"","switchType":"","externalOverPhysical":false},
 {"ifIndex":9,"ifType":71,"physicalMedium":9,"hardwareInterface":true,"connectorPresent":true,"hidden":false,"componentId":"pci\\ven_8086&dev_51f1","description":"Wi-Fi Adapter","vmSwitchId":"","switchType":"","externalOverPhysical":false},
 {"ifIndex":12,"ifType":6,"physicalMedium":0,"hardwareInterface":false,"connectorPresent":false,"hidden":false,"componentId":"vms_mp","description":"Hyper-V Virtual Ethernet Adapter #2","vmSwitchId":"11111111-2222-4333-8444-555555555555","switchType":"External","externalOverPhysical":true},
 {"ifIndex":15,"ifType":53,"physicalMedium":0,"hardwareInterface":false,"connectorPresent":false,"hidden":false,"componentId":"wintun","description":"Tunnel Adapter","vmSwitchId":"","switchType":"","externalOverPhysical":false},
 {"ifIndex":20,"ifType":6,"physicalMedium":0,"hardwareInterface":false,"connectorPresent":false,"hidden":false,"componentId":"vms_mp","description":"Hyper-V Virtual Ethernet Adapter #4","vmSwitchId":"c3d4e5f6-a7b8-49c0-8d1e-2f3a4b5c6d7e","switchType":"Internal","externalOverPhysical":false},
 {"ifIndex":25,"ifType":6,"physicalMedium":14,"hardwareInterface":true,"connectorPresent":true,"hidden":false,"componentId":"usb\\vid_0b95&pid_1790","description":"USB Ethernet","vmSwitchId":"","switchType":"","externalOverPhysical":false},
 {"ifIndex":1,"ifType":24,"hidden":true,"description":"Loopback Pseudo-Interface 1"},
 {"ifIndex":30,"ifType":23,"hidden":true,"description":"Work VPN"}],
"routes":[
 {"prefix":"0.0.0.0/0","ifIndex":12,"nextHop":"192.0.2.1","metric":0},
 {"prefix":"192.0.2.0/24","ifIndex":12,"nextHop":"0.0.0.0","metric":256},
 {"prefix":"192.0.2.10/32","ifIndex":12,"nextHop":"0.0.0.0","metric":256},
 {"prefix":"192.0.2.255/32","ifIndex":12,"nextHop":"0.0.0.0","metric":256},
 {"prefix":"172.31.250.0/29","ifIndex":20,"nextHop":"0.0.0.0","metric":256},
 {"prefix":"100.64.0.0/10","ifIndex":15,"nextHop":"0.0.0.0","metric":5},
 {"prefix":"100.100.100.100/32","ifIndex":15,"nextHop":"0.0.0.0","metric":5},
 {"prefix":"198.51.100.0/24","ifIndex":15,"nextHop":"0.0.0.0","metric":5},
 {"prefix":"127.0.0.0/8","ifIndex":1,"nextHop":"0.0.0.0","metric":256},
 {"prefix":"224.0.0.0/4","ifIndex":20,"nextHop":"0.0.0.0","metric":256},
 {"prefix":"255.255.255.255/32","ifIndex":1,"nextHop":"0.0.0.0","metric":256}],
"egress":[
 {"dest":"1.1.1.1","ifIndex":12},{"dest":"9.9.9.9","ifIndex":12},{"dest":"23.0.0.1","ifIndex":12},{"dest":"64.0.0.1","ifIndex":12},
 {"dest":"104.16.0.1","ifIndex":12},{"dest":"151.101.1.1","ifIndex":12},{"dest":"185.1.1.1","ifIndex":12},{"dest":"200.0.0.1","ifIndex":12}],
"addrs":["192.0.2.10/24","172.31.250.1/29","100.64.0.5/32","127.0.0.1/8"]}`

// cannedACLRows are the remotes Hyper-V lists for this plan and host: the
// static Denies, the physical LAN's on-link prefix, the tunnel's public route
// and the other line's exit (a /32, listed bare).
var cannedACLRows = []string{
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
	"192.0.0.0/24", "192.168.0.0/16", "198.18.0.0/15", "224.0.0.0/4", "240.0.0.0/4", "::/0",
	"192.0.2.0/24", "198.51.100.0/24", "203.0.113.7",
}

func cannedACLs(rows []string) string {
	var out []string
	for _, r := range rows {
		for _, d := range []string{"Inbound", "Outbound"} {
			out = append(out, `{"action":"Deny","direction":"`+d+`","remote":"`+r+`","local":""}`)
		}
	}
	return "[" + strings.Join(out, ",") + "]"
}

const cannedLiveTemplate = `{"vmExists":true,"vmState":"Running",
"adapters":[{"id":"7A1B2C3D-4E5F-4061-8273-94A5B6C7D8E9","switchId":"c3d4e5f6-a7b8-49c0-8d1e-2f3a4b5c6d7e","mac":"00155D0A0002","dynamicMac":false,"dhcpGuard":"On","routerGuard":"On","macSpoofing":"Off"}],
"acls":%ACLS%,
"extAcls":0,"procCount":2,"procMax":75,"procWeight":50,"exposeVirt":false,"memoryBytes":6442450944,"dynamicMemory":false,
"checkpoints":"Disabled","autoStart":"Nothing",
"integration":{"84EAAE65-2F2E-45F5-9BB5-0E857DC8EB47":true,"9F8233AC-BE49-4C79-8EE3-E7E1985B2077":true,"2497F4DE-E9FA-4204-80E4-4B75C46419C0":true,"6C09BB55-D683-4DA0-8931-C9BF705F6480":false,"2A34B1C2-FD73-4043-8A5B-DD2159BC743F":false,"5CED1297-4598-4915-A5FC-AD21BB4D02A4":false},
"dvds":0,"disks":1,"passthrough":[],
"switchType":"Internal","switchPorts":1,"vnicIfIndex":20,"vnicMac":"00-15-5D-0A-00-01",
"bindings":{"ms_tcpip":true,"ms_tcpip6":false,"ms_server":false,"ms_msclient":false,"ms_lltdio":false,"ms_rspndr":false,"ms_lldp":false,"ms_implat":false,"ms_pacer":true},
"protocolsOn":["ms_tcpip"],
"neighbors":[{"ip":"172.31.250.2","mac":"00-15-5D-0A-00-02","state":"Permanent"},{"ip":"172.31.250.7","mac":"FF-FF-FF-FF-FF-FF","state":"Permanent"},{"ip":"224.0.0.22","mac":"01-00-5E-00-00-16","state":"Permanent"}],
"nats":[{"name":"CashPilot-VM","prefix":"172.31.250.0/29"},{"name":"Lab-NAT","prefix":"10.99.0.0/24"}],
"firewall":[{"direction":"Inbound","action":"Block","enabled":true,"ifIndex":20}],
"enhancedSession":false,"gcs":["0000AAAA-FACB-11E6-BD58-64006A7986D3"],"hostBuild":"26100.1000"}`

func cannedLive() string {
	return strings.Replace(cannedLiveTemplate, "%ACLS%", cannedACLs(cannedACLRows), 1)
}

const cannedNetwork = `{"gatewayMac":"00-00-5E-00-53-01","profileId":"{6A5D2C1E-0B7F-4C3A-9E21-5F8D7C6B4A30}","cost":"Unrestricted","roaming":false,"overDataLimit":false}`

// The instance name as the lab host wrote it: VM name, the adapter's name in
// the host's language, VM ID and adapter ID, in upper case.
const cannedDrops = `{"vmName":"CashPilot-VM","instances":[{"name":"CashPilot-VM_Adaptador de red_5B8E2F0A-3C4D-4E5F-8A9B-0C1D2E3F4A5B--7A1B2C3D-4E5F-4061-8273-94A5B6C7D8E9","out":1037,"in":2}]}`

const cannedUsage = `{"cpuPercent":4,"memoryBytes":6442450944,"uptimeSeconds":86400,"diskBytes":21474836480,"bytesSent":123456789}`

// twoDenies are a tunnel route and an own IP; the /32 goes to Hyper-V as a
// bare address, the way Get-VMNetworkAdapterAcl lists it.
var twoDenies = []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.9/32")}

// The Deny rows a write script lists afterwards: each Both rule as an Inbound
// and an Outbound row.
const (
	cannedDenyAfterAdd    = `{"deny":[{"direction":"Inbound","remote":"10.0.0.0/8"},{"direction":"Outbound","remote":"10.0.0.0/8"},{"direction":"Inbound","remote":"198.51.100.0/24"},{"direction":"Outbound","remote":"198.51.100.0/24"},{"direction":"Inbound","remote":"203.0.113.9"},{"direction":"Outbound","remote":"203.0.113.9"}]}`
	cannedDenyAfterRemove = `{"deny":[{"direction":"Inbound","remote":"10.0.0.0/8"},{"direction":"Outbound","remote":"10.0.0.0/8"}]}`
)

// fakeRun records what the seam asked PowerShell to run and plays back
// answers.
type fakeRun struct {
	scripts []string
	args    []string
	answers []string
	errs    []error
}

func (f *fakeRun) run(_ context.Context, script string, arg []byte) ([]byte, error) {
	f.scripts = append(f.scripts, script)
	f.args = append(f.args, string(arg))
	i := len(f.scripts) - 1
	if i < len(f.errs) && f.errs[i] != nil {
		return nil, f.errs[i]
	}
	if i >= len(f.answers) {
		return nil, errors.New("no canned answer")
	}
	return []byte(f.answers[i]), nil
}

type sleepRec struct{ waits []time.Duration }

func (s *sleepRec) sleep(_ context.Context, d time.Duration) error {
	s.waits = append(s.waits, d)
	return nil
}

func newSeam(answers ...string) (*psHost, *fakeRun, *sleepRec) {
	r := &fakeRun{answers: answers}
	s := &sleepRec{}
	return &psHost{run: r.run, sleep: s.sleep}, r, s
}

// TestSeamScriptsAndArguments pins, for each method, the one fixed script it
// runs and the exact JSON argument it passes, then reads a canned answer back.
func TestSeamScriptsAndArguments(t *testing.T) {
	ctx := context.Background()
	two := []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("9.9.9.9")}
	cases := []struct {
		name   string
		script string
		arg    string
		answer string
		call   func(h *psHost) (any, error)
	}{
		{"ReadLive", scriptReadLive, `{"vm":"` + tVMID + `","switch":"CashPilot-VM","group":"CashPilotVM"}`, cannedLive(),
			func(h *psHost) (any, error) { return h.ReadLive(ctx, ProductNames(), tVMID) }},
		{"ReadHostNet", scriptReadHostNet, `{"samples":["1.1.1.1","9.9.9.9"]}`, cannedHostNet,
			func(h *psHost) (any, error) { return h.ReadHostNet(ctx, two) }},
		{"ReadNetwork", scriptReadNetwork, `{}`, cannedNetwork,
			func(h *psHost) (any, error) { return h.ReadNetwork(ctx) }},
		{"DropCounters", scriptDropCounters, `{"vm":"` + tVMID + `","adapter":"` + tAdapterID + `"}`, cannedDrops,
			func(h *psHost) (any, error) { return h.DropCounters(ctx, tVMID, tAdapterID) }},
		{"Usage", scriptUsage, `{"vm":"` + tVMID + `"}`, cannedUsage,
			func(h *psHost) (any, error) { return h.Usage(ctx, tVMID) }},
		{"Disconnect", scriptDisconnect, `{"vm":"` + tVMID + `"}`, `{"adapters":1,"connected":0}`,
			func(h *psHost) (any, error) { return nil, h.Disconnect(ctx, tVMID) }},
		{"Connect", scriptConnect, `{"vm":"` + tVMID + `","switch":"CashPilot-VM"}`, `{"switchId":"` + tSwitchID + `"}`,
			func(h *psHost) (any, error) { return nil, h.Connect(ctx, tVMID, "CashPilot-VM") }},
		{"Start", scriptStart, `{"vm":"` + tVMID + `"}`, `{"state":"Running"}`,
			func(h *psHost) (any, error) { return nil, h.Start(ctx, tVMID) }},
		{"AddDeny", scriptAddDeny, `{"vm":"` + tVMID + `","remotes":["198.51.100.0/24","203.0.113.9"]}`, cannedDenyAfterAdd,
			func(h *psHost) (any, error) { return nil, h.AddDeny(ctx, tVMID, twoDenies) }},
		{"RemoveDeny", scriptRemoveDeny, `{"vm":"` + tVMID + `","remotes":["198.51.100.0/24","203.0.113.9"]}`, cannedDenyAfterRemove,
			func(h *psHost) (any, error) { return nil, h.RemoveDeny(ctx, tVMID, twoDenies) }},
	}
	seen := map[string]bool{}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, r, _ := newSeam(c.answer)
			if _, err := c.call(h); err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			if len(r.scripts) != 1 {
				t.Fatalf("%s ran %d scripts, want 1", c.name, len(r.scripts))
			}
			if r.scripts[0] != c.script || hvScripts[c.name] != c.script {
				t.Errorf("%s ran another script than its own", c.name)
			}
			if !strings.HasPrefix(r.scripts[0], hvPrelude) {
				t.Errorf("%s script does not start with the prelude that reads the argument", c.name)
			}
			if r.args[0] != c.arg {
				t.Errorf("%s argument\n got %s\nwant %s", c.name, r.args[0], c.arg)
			}
		})
		seen[c.name] = true
	}
	for name := range hvScripts {
		if _, build := buildScripts[name]; build || strings.HasPrefix(name, "Prober.") {
			continue // TestBuildSeamScriptsAndArguments
		}
		if !seen[name] {
			t.Errorf("script %s has no seam case", name)
		}
	}
}

// Every script fits on one Windows command line as -EncodedCommand.
func TestSeamScriptsFitTheCommandLine(t *testing.T) {
	for name, s := range hvScripts {
		if n := len(psEncode(s)); n > 30000 {
			t.Errorf("%s is %d characters encoded, over a Windows command line", name, n)
		}
	}
}

// The argument travels as base64 JSON in one environment variable.
func TestSeamArgumentEnvironment(t *testing.T) {
	got := hvArg([]byte(`{"vm":"x'; Remove-VM *"}`))
	want := "CPD_HV_ARG=" + base64.StdEncoding.EncodeToString([]byte(`{"vm":"x'; Remove-VM *"}`))
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if strings.ContainsAny(strings.TrimPrefix(got, "CPD_HV_ARG="), "'\";$`&|() ") {
		t.Fatal("the encoded argument holds a character PowerShell could read as code")
	}
}

// A canned reading of a host that matches the plan gives no change at all.
// This ties the reader to Diff: a Both rule listed as two rows, a bare /32,
// the broadcast and multicast neighbour entries Windows adds, and another NAT
// off our /29 are all read as Diff expects.
func TestReadLiveMatchesPlan(t *testing.T) {
	p := seamPlan(t)
	l, h := readCanned(t, cannedLive())
	if cs := Diff(p, l, dynamicFor(p, h)); len(cs) != 0 {
		t.Fatalf("a matching host shows changes: %+v", cs)
	}
	if l.HostNeighbor == nil || l.HostNeighbor.IP != netip.MustParseAddr("172.31.250.2") {
		t.Fatalf("host neighbour %+v, want the guest's unicast entry only", l.HostNeighbor)
	}
}

func readCanned(t *testing.T, live string) (LiveState, HostNet) {
	t.Helper()
	h, _, _ := newSeam(live, cannedHostNet)
	l, err := h.ReadLive(context.Background(), ProductNames(), tVMID)
	if err != nil {
		t.Fatal(err)
	}
	hn, err := h.ReadHostNet(context.Background(), EgressSamples)
	if err != nil {
		t.Fatal(err)
	}
	return l, hn
}

func dynamicFor(p Plan, h HostNet) Dynamic {
	return Dynamic{HostAddrs: HostDenies(h, p.SwitchID), Tunnels: TunnelPrefixes(h, p.SwitchID, StaticDeny())}
}

// A part the script could not read is left out of its answer. Each one must
// show as a change, so a broken reader keeps the VM off. vmState and hostBuild
// are not compared by Diff (the guard reads hostBuild for the re-proof).
func TestReadLiveFailsClosedOnEveryMissingPart(t *testing.T) {
	p := seamPlan(t)
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(cannedLive()), &doc); err != nil {
		t.Fatal(err)
	}
	for k := range doc {
		if k == "vmExists" || k == "vmState" || k == "hostBuild" {
			continue
		}
		t.Run(k, func(t *testing.T) {
			cut := map[string]json.RawMessage{}
			for kk, v := range doc {
				if kk != k {
					cut[kk] = v
				}
			}
			b, _ := json.Marshal(cut)
			l, h := readCanned(t, string(b))
			if cs := Diff(p, l, dynamicFor(p, h)); len(cs) == 0 {
				t.Fatalf("without %q the reading still matches the plan", k)
			}
		})
	}
}

// Single values that would read as safe if they were taken at face value.
func TestReadLiveFailsClosedOnBadValues(t *testing.T) {
	p := seamPlan(t)
	cases := map[string][2]string{
		"MAC spoofing not read":        {`"macSpoofing":"Off"`, `"macSpoofing":""`},
		"DHCP guard off":               {`"dhcpGuard":"On"`, `"dhcpGuard":"Off"`},
		"dynamic MAC":                  {`"dynamicMac":false`, `"dynamicMac":true`},
		"ms_implat not listed":         {`"ms_implat":false,`, ``},
		"ms_implat bound":              {`"ms_implat":false`, `"ms_implat":true`},
		"a third-party protocol":       {`"protocolsOn":["ms_tcpip"]`, `"protocolsOn":["ms_tcpip","npcap_proto"]`},
		"no ms_tcpip in the protocols": {`"protocolsOn":["ms_tcpip"]`, `"protocolsOn":[]`},
		"firewall on any interface":    {`"ifIndex":20}]`, `"ifIndex":0}]`},
		"a Meter row":                  {`"acls":[`, `"acls":[{"action":"Meter","direction":"Outbound","remote":"0.0.0.0/1","local":""},`},
		"a MAC ACL row":                {`"acls":[`, `"acls":[{"action":"Allow","direction":"Inbound","remote":"00-15-5D-00-00-09","local":""},`},
		"a row with a local address":   {`{"action":"Deny","direction":"Inbound","remote":"10.0.0.0/8","local":""}`, `{"action":"Deny","direction":"Inbound","remote":"10.0.0.0/8","local":"172.31.250.2"}`},
		"a one-way Deny":               {`{"action":"Deny","direction":"Outbound","remote":"192.0.2.0/24","local":""}`, `{"action":"Allow","direction":"Outbound","remote":"192.0.2.0/24","local":""}`},
		"two unicast neighbours":       {`{"ip":"172.31.250.7","mac":"FF-FF-FF-FF-FF-FF","state":"Permanent"}`, `{"ip":"172.31.250.3","mac":"00-15-5D-0A-00-09","state":"Permanent"}`},
		"a passthrough device":         {`"passthrough":[]`, `"passthrough":["Get-VMAssignableDevice"]`},
		"a new hv_sock service":        {`"gcs":["0000AAAA-FACB-11E6-BD58-64006A7986D3"]`, `"gcs":["0000AAAA-FACB-11E6-BD58-64006A7986D3","9D6C2B11-0000-4000-8000-000000000001"]`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			live := cannedLive()
			if !strings.Contains(live, c[0]) {
				t.Fatalf("the canned answer has no %s", c[0])
			}
			l, h := readCanned(t, strings.Replace(live, c[0], c[1], 1))
			if cs := Diff(p, l, dynamicFor(p, h)); len(cs) == 0 {
				t.Fatalf("%s still matches the plan", name)
			}
		})
	}
}

func TestReadLiveVMAbsent(t *testing.T) {
	h, _, _ := newSeam(`{"vmExists":false}`)
	l, err := h.ReadLive(context.Background(), ProductNames(), tVMID)
	if err != nil || l.VMExists {
		t.Fatalf("got %+v, %v", l, err)
	}
	h, _, _ = newSeam(`{}`)
	if _, err := h.ReadLive(context.Background(), ProductNames(), tVMID); err == nil {
		t.Fatal("an answer that does not say whether the VM exists was accepted")
	}
}

func TestReadHostNet(t *testing.T) {
	h, _, _ := newSeam(cannedHostNet)
	hn, err := h.ReadHostNet(context.Background(), EgressSamples)
	if err != nil {
		t.Fatal(err)
	}
	if full, why := FullTunnel(hn, tSwitchID); full {
		t.Fatalf("full tunnel on a host that leaves through its External vSwitch: %s", why)
	}
	if got := TunnelPrefixes(hn, tSwitchID, StaticDeny()); !slices.Equal(got, []netip.Prefix{netip.MustParsePrefix("198.51.100.0/24")}) {
		t.Fatalf("tunnel prefixes %v, want the public route only", got)
	}
	class := map[int]IfClass{}
	for _, i := range hn.Interfaces {
		class[i.IfIndex] = Classify(i, tSwitchID)
	}
	want := map[int]IfClass{7: IfPhysical, 9: IfPhysical, 12: IfExternalVSwitch, 15: IfTunnel, 20: IfOurs, 25: IfTunnel, 1: IfLoopback, 30: IfTunnel}
	for idx, c := range want {
		if class[idx] != c {
			t.Errorf("ifIndex %d is %s, want %s", idx, class[idx], c)
		}
	}
}

// An adapter counts as physical only when Get-NetAdapter -Physical lists it.
// With that list unread, no adapter is physical, and the host's sample traffic
// that leaves through a physical NIC counts as a tunnel.
func TestReadHostNetPhysicalCrossCheck(t *testing.T) {
	noList := strings.Replace(cannedHostNet, `"physical":[7,9],`, `"physical":null,`, 1)
	direct := strings.ReplaceAll(noList, `"ifIndex":12}`, `"ifIndex":7}`)
	h, _, _ := newSeam(direct)
	hn, err := h.ReadHostNet(context.Background(), EgressSamples)
	if err != nil {
		t.Fatal(err)
	}
	for _, i := range hn.Interfaces {
		if Classify(i, tSwitchID) == IfPhysical {
			t.Errorf("ifIndex %d is physical without Get-NetAdapter -Physical listing it", i.IfIndex)
		}
	}
	if full, _ := FullTunnel(hn, tSwitchID); !full {
		t.Fatal("egress through an adapter -Physical does not list was trusted")
	}
}

func TestReadNetworkCost(t *testing.T) {
	approved := []NetworkID{{GatewayMAC: "00-00-5E-00-53-01", ProfileID: "{6A5D2C1E-0B7F-4C3A-9E21-5F8D7C6B4A30}"}}
	cases := map[string]struct {
		from, to string
		why      string
	}{
		"unrestricted": {"", "", ""},
		"fixed plan":   {`"cost":"Unrestricted"`, `"cost":"Fixed"`, NetworkMetered},
		"variable":     {`"cost":"Unrestricted"`, `"cost":"Variable"`, NetworkMetered},
		"roaming":      {`"roaming":false`, `"roaming":true`, NetworkMetered},
		"over limit":   {`"overDataLimit":false`, `"overDataLimit":true`, NetworkMetered},
		"cost unread":  {`"cost":"Unrestricted",`, ``, NetworkUnknown},
		"cost Unknown": {`"cost":"Unrestricted"`, `"cost":"Unknown"`, NetworkUnknown},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			h, _, _ := newSeam(strings.Replace(cannedNetwork, c.from, c.to, 1))
			n, err := h.ReadNetwork(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			ok, why := NetworkOK(n, approved)
			if why != c.why || ok != (c.why == "") {
				t.Fatalf("NetworkOK = %v %q, want %q", ok, why, c.why)
			}
		})
	}
}

// The drop counter: CIM first, CIM once more after 2 s, then PDH; the source
// that answered is named; with no answer it is an error, never a zero sample.
func TestDropCounterSources(t *testing.T) {
	ctx := context.Background()
	empty := `{"vmName":"CashPilot-VM","instances":[]}`
	pdhInst := []DropInstance{
		{Name: "OtherVM_Network Adapter_11111111-2222-4333-8444-555555555555--7A1B2C3D-4E5F-4061-8273-94A5B6C7D8E9", Out: 9, In: 9},
		{Name: "CashPilot-VM_Netzwerkkarte_5B8E2F0A-3C4D-4E5F-8A9B-0C1D2E3F4A5B--7A1B2C3D-4E5F-4061-8273-94A5B6C7D8E9", Out: 500, In: 1},
	}
	pdhOK := func(context.Context) ([]DropInstance, error) { return pdhInst, nil }
	pdhNone := func(context.Context) ([]DropInstance, error) { return nil, errors.New("PDH_CSTATUS_NO_OBJECT") }
	cases := []struct {
		name    string
		answers []string
		errs    []error
		pdh     func(context.Context) ([]DropInstance, error)
		want    *DropSample
		runs    int
		waits   int
	}{
		{"CIM answers", []string{cannedDrops}, nil, pdhOK, &DropSample{Out: 1037, In: 2, Source: "wmi"}, 1, 0},
		{"CIM answers on the retry", []string{empty, cannedDrops}, nil, pdhOK, &DropSample{Out: 1037, In: 2, Source: "wmi"}, 2, 1},
		{"PDH answers", []string{empty, empty}, nil, pdhOK, &DropSample{Out: 500, In: 1, Source: "pdh"}, 2, 1},
		{"neither answers", []string{empty, empty}, nil, pdhNone, nil, 2, 1},
		{"no PDH on this host", []string{empty, empty}, nil, nil, nil, 2, 1},
		{"the CIM script fails, so the VM name is unknown", nil, []error{errors.New("x"), errors.New("x")}, pdhOK, nil, 2, 1},
		{"another VM's instance with our IDs is no match", []string{strings.Replace(cannedDrops, `"name":"CashPilot-VM_`, `"name":"Imposter_`, 1), empty}, nil, pdhNone, nil, 2, 1},
		{"two matching instances are no answer", []string{strings.Replace(cannedDrops, `}]}`, `},{"name":"CashPilot-VM_x_5B8E2F0A-3C4D-4E5F-8A9B-0C1D2E3F4A5B--7A1B2C3D-4E5F-4061-8273-94A5B6C7D8E9","out":1,"in":1}]}`, 1), empty}, nil, pdhNone, nil, 2, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h, r, s := newSeam(c.answers...)
			r.errs = c.errs
			h.pdh = c.pdh
			got, err := h.DropCounters(ctx, tVMID, tAdapterID)
			if c.want == nil {
				if !errors.Is(err, ErrNoDropCounter) {
					t.Fatalf("got %+v, %v; want ErrNoDropCounter", got, err)
				}
				if got != (DropSample{}) {
					t.Fatalf("an unanswered read returned a sample %+v", got)
				}
			} else if err != nil || got != *c.want {
				t.Fatalf("got %+v, %v; want %+v", got, err, *c.want)
			}
			if len(r.scripts) != c.runs {
				t.Errorf("CIM ran %d times, want %d", len(r.scripts), c.runs)
			}
			if len(s.waits) != c.waits || (c.waits > 0 && s.waits[0] != 2*time.Second) {
				t.Errorf("waits %v, want %d of 2s", s.waits, c.waits)
			}
		})
	}
}

// Values that would reach a script are checked first; a refused call never
// runs PowerShell.
func TestSeamRefusesBadInput(t *testing.T) {
	ctx := context.Background()
	bad := ProductNames()
	bad.Switch = "CashPilot-VM'; Remove-VM -Name * -Force; '"
	other := ProductNames()
	other.VM = "bunker"
	cases := map[string]func(h *psHost) error{
		"an injected switch name": func(h *psHost) error { _, err := h.ReadLive(ctx, bad, tVMID); return err },
		"a name outside our sets": func(h *psHost) error { _, err := h.ReadLive(ctx, other, tVMID); return err },
		"a VM ID that is no GUID": func(h *psHost) error {
			_, err := h.ReadLive(ctx, ProductNames(), "' + (Stop-Computer) + '")
			return err
		},
		"a braced VM ID":             func(h *psHost) error { _, err := h.Usage(ctx, "{"+tVMID+"}"); return err },
		"an empty VM ID":             func(h *psHost) error { return h.Disconnect(ctx, "") },
		"a bad adapter ID":           func(h *psHost) error { _, err := h.DropCounters(ctx, tVMID, "*"); return err },
		"a switch name with a space": func(h *psHost) error { return h.Connect(ctx, tVMID, "Default Switch") },
		"a switch name too long":     func(h *psHost) error { return h.Connect(ctx, tVMID, strings.Repeat("a", 33)) },
		"a switch name with a quote": func(h *psHost) error { return h.Connect(ctx, tVMID, "a'b") },
		"an IPv6 sample": func(h *psHost) error {
			_, err := h.ReadHostNet(ctx, []netip.Addr{netip.MustParseAddr("2001:db8::1")})
			return err
		},
		"an unspecified sample": func(h *psHost) error {
			_, err := h.ReadHostNet(ctx, []netip.Addr{netip.IPv4Unspecified()})
			return err
		},
		"a zero sample": func(h *psHost) error { _, err := h.ReadHostNet(ctx, []netip.Addr{{}}); return err },
		"a multicast sample": func(h *psHost) error {
			_, err := h.ReadHostNet(ctx, []netip.Addr{netip.MustParseAddr("224.0.0.1")})
			return err
		},
		"too many samples": func(h *psHost) error {
			_, err := h.ReadHostNet(ctx, slices.Repeat([]netip.Addr{netip.MustParseAddr("1.1.1.1")}, maxSamples+1))
			return err
		},
		"a Start with a bad VM ID": func(h *psHost) error { return h.Start(ctx, "x") },
		"an AddDeny with a bad VM ID": func(h *psHost) error {
			return h.AddDeny(ctx, "'; Remove-VM *", twoDenies)
		},
		"an AddDeny with no prefix": func(h *psHost) error { return h.AddDeny(ctx, tVMID, nil) },
		"an AddDeny with an IPv6 prefix": func(h *psHost) error {
			return h.AddDeny(ctx, tVMID, []netip.Prefix{netip.MustParsePrefix("2001:db8::/32")})
		},
		"an AddDeny with an unmasked prefix": func(h *psHost) error {
			return h.AddDeny(ctx, tVMID, []netip.Prefix{netip.MustParsePrefix("198.51.100.7/24")})
		},
		"an AddDeny with a zero prefix": func(h *psHost) error { return h.AddDeny(ctx, tVMID, []netip.Prefix{{}}) },
		"an AddDeny with a repeat": func(h *psHost) error {
			return h.AddDeny(ctx, tVMID, []netip.Prefix{twoDenies[0], twoDenies[0]})
		},
		"an AddDeny over the row cap": func(h *psHost) error {
			var ps []netip.Prefix
			for i := range maxDenyTotal + 1 {
				ps = append(ps, netip.PrefixFrom(netip.AddrFrom4([4]byte{203, 0, byte(i >> 8), byte(i)}), 32))
			}
			return h.AddDeny(ctx, tVMID, ps)
		},
		"a RemoveDeny of a static Deny": func(h *psHost) error {
			return h.RemoveDeny(ctx, tVMID, []netip.Prefix{twoDenies[0], netip.MustParsePrefix("192.168.0.0/16")})
		},
		"a RemoveDeny with an IPv6 prefix": func(h *psHost) error {
			return h.RemoveDeny(ctx, tVMID, []netip.Prefix{netip.MustParsePrefix("::/0")})
		},
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			h, r, _ := newSeam(`{}`)
			if err := call(h); err == nil {
				t.Fatal("accepted")
			}
			if len(r.scripts) != 0 {
				t.Fatal("PowerShell ran on refused input")
			}
		})
	}
}

// Oversized or malformed output is refused, before and after base64.
func TestSeamRefusesBadOutput(t *testing.T) {
	ok := base64.StdEncoding.EncodeToString([]byte(`{"a":1}`))
	if doc, err := decodeOutput([]byte(ok + "\r\n")); err != nil || string(doc) != `{"a":1}` {
		t.Fatalf("a good answer: %q, %v", doc, err)
	}
	exact := make([]byte, maxHVOutput)
	if _, err := decodeOutput([]byte(base64.StdEncoding.EncodeToString(exact))); err != nil {
		t.Fatalf("an answer of exactly 1 MB was refused: %v", err)
	}
	if _, err := decodeOutput([]byte(base64.StdEncoding.EncodeToString(make([]byte, maxHVOutput+1)))); err == nil {
		t.Fatal("an answer over 1 MB was accepted")
	}
	if _, err := decodeOutput(make([]byte, maxHVRaw+1)); err == nil {
		t.Fatal("printed output over the cap was accepted")
	}
	if _, err := decodeOutput([]byte("WARNING: not base64")); err == nil {
		t.Fatal("text that is not base64 was accepted")
	}

	c := &cappedBuffer{max: 4}
	if n, err := c.Write([]byte("abcdef")); n != 6 || err != nil || !c.overflow || c.buf.String() != "abcd" {
		t.Fatalf("capped buffer: %d %v %v %q", n, err, c.overflow, c.buf.String())
	}

	big := `{"vmExists":true,"vmState":"` + strings.Repeat("x", maxHVOutput) + `"}`
	for name, answer := range map[string]string{
		"over 1 MB":       big,
		"an unknown key":  `{"vmExists":true,"surprise":1}`,
		"trailing data":   `{"vmExists":false} {"vmExists":true}`,
		"not JSON at all": `<Objs Version="1.1.0.1">`,
	} {
		h, _, _ := newSeam(answer)
		if _, err := h.ReadLive(context.Background(), ProductNames(), tVMID); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	h, _, _ := newSeam(`{"adapters":1,"connected":1}`)
	if err := h.Disconnect(context.Background(), tVMID); err == nil {
		t.Error("a disconnect that left an adapter connected was accepted")
	}
	h, _, _ = newSeam(`{"switchId":""}`)
	if err := h.Connect(context.Background(), tVMID, "CashPilot-VM"); err == nil {
		t.Error("a connect that reported no switch was accepted")
	}
	h, _, _ = newSeam(`{"state":"Off"}`)
	if err := h.Start(context.Background(), tVMID); err == nil {
		t.Error("a start that left the VM off was accepted")
	}
	// An add whose row is missing, or there one way only, failed.
	oneWay := strings.Replace(cannedDenyAfterAdd, `{"direction":"Outbound","remote":"203.0.113.9"}`, `{"direction":"Outbound","remote":"203.0.113.10"}`, 1)
	for name, answer := range map[string]string{"missing": cannedDenyAfterRemove, "one way only": oneWay} {
		h, _, _ = newSeam(answer)
		if err := h.AddDeny(context.Background(), tVMID, twoDenies); err == nil {
			t.Errorf("an add with its row %s was accepted", name)
		}
	}
	// A remove that left a row behind failed, whether Hyper-V lists the /32
	// bare or with its /32.
	slash32 := strings.ReplaceAll(cannedDenyAfterAdd, `"203.0.113.9"`, `"203.0.113.9/32"`)
	for name, answer := range map[string]string{"bare": cannedDenyAfterAdd, "with /32": slash32} {
		h, _, _ = newSeam(answer)
		if err := h.RemoveDeny(context.Background(), tVMID, twoDenies); err == nil {
			t.Errorf("a remove that left its rows (%s) was accepted", name)
		}
	}
	// An add whose /32 Hyper-V lists as a.b.c.d/32 is in place.
	h, _, _ = newSeam(slash32)
	if err := h.AddDeny(context.Background(), tVMID, twoDenies); err != nil {
		t.Errorf("an add listed with /32 was refused: %v", err)
	}
}

func TestParsePrefixOrAddr(t *testing.T) {
	for in, want := range map[string]string{"10.0.0.0/8": "10.0.0.0/8", "192.0.2.10": "192.0.2.10/32", "::/0": "::/0", "2001:db8::1": "2001:db8::1/128"} {
		p, err := parsePrefixOrAddr(in)
		if err != nil || p.String() != want {
			t.Errorf("%q: %v %v", in, p, err)
		}
	}
	for _, in := range []string{"", "00-15-5D-00-00-09", "10.0.0.0/33"} {
		if _, err := parsePrefixOrAddr(in); err == nil {
			t.Errorf("%q was accepted", in)
		}
	}
}

func TestNewHostOtherOS(t *testing.T) {
	if _, ok := NewHost().(*psHost); ok {
		return // Windows
	}
	if _, err := NewHost().ReadNetwork(context.Background()); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("got %v, want ErrUnsupported", err)
	}
}

// A list over one script's cap goes in batches: 300 Denies are two writes of
// 256 and 44, for an add and for a remove, and each batch's rows are checked.
func TestSeamDenyBatches(t *testing.T) {
	ctx := context.Background()
	var ps []netip.Prefix
	var rows []string
	for i := range 300 {
		p := netip.PrefixFrom(netip.AddrFrom4([4]byte{203, 0, byte(i >> 8), byte(i)}), 32)
		ps = append(ps, p)
		for _, dir := range []string{"Inbound", "Outbound"} {
			rows = append(rows, `{"direction":"`+dir+`","remote":"`+p.Addr().String()+`"}`)
		}
	}
	listed := `{"deny":[` + strings.Join(rows, ",") + `]}`
	sizes := func(r *fakeRun) []int {
		var out []int
		for _, a := range r.args {
			var w struct {
				Remotes []string `json:"remotes"`
			}
			if err := json.Unmarshal([]byte(a), &w); err != nil {
				t.Fatal(err)
			}
			out = append(out, len(w.Remotes))
		}
		return out
	}
	h, r, _ := newSeam(listed, listed)
	if err := h.AddDeny(ctx, tVMID, ps); err != nil {
		t.Fatalf("an add of 300 Denies: %v", err)
	}
	if got := sizes(r); !slices.Equal(got, []int{256, 44}) {
		t.Fatalf("the add wrote batches of %v, want [256 44]", got)
	}
	h, r, _ = newSeam(`{"deny":[]}`, `{"deny":[]}`)
	if err := h.RemoveDeny(ctx, tVMID, ps); err != nil {
		t.Fatalf("a remove of 300 Denies: %v", err)
	}
	if got := sizes(r); !slices.Equal(got, []int{256, 44}) {
		t.Fatalf("the remove wrote batches of %v, want [256 44]", got)
	}
	// A batch whose rows are not all there fails the add.
	h, _, _ = newSeam(listed, `{"deny":[]}`)
	if err := h.AddDeny(ctx, tVMID, ps); err == nil {
		t.Fatal("an add whose second batch is missing was accepted")
	}
}
