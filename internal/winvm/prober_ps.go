package winvm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/netip"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The proof's Prober over PowerShell and Go: the holes it opens are scripts
// like the rest of the seam; the canary is a Go listener behind a temporary
// firewall Allow; pktmon runs through its own CLI and Go reads the capture.

// proofGroupSuffix names the firewall group of the canaries' Allow rules.
const proofGroupSuffix = "-proof"

// proofCanaryDesc is the description of every canary rule. It carries this
// install's owner token, so ClearProof removes only the rules this install's
// proof made, never one matched by its group or a fixed text alone.
func proofCanaryDesc(owner string) string {
	return "CashPilot Secure VM proof canary. " + ownerTag + owner
}

// ProofElementName is the ElementName of the proof's hv_sock test key for
// instance n. It carries this install's owner token, so ClearProof finds a
// key this install's killed proof left, and only that.
func ProofElementName(n Names, owner string) string {
	return "CashPilot Secure VM proof " + n.VM + ". " + ownerTag + owner
}

// scriptSetL3 turns only this install's tagged rule on or off.
const scriptSetL3 = firewallPrelude + `$desc = 'CashPilot Secure VM. cashpilot-owner=' + $in.owner
$rs = @(Get-NetFirewallRule -Group $in.group -ErrorAction SilentlyContinue | Where-Object { $_.Name -ceq $in.id -and $_.Description -ceq $desc })
if ($rs.Count -ne 1 -or -not $in.identity -or (Get-FirewallIdentity $rs[0]) -cne $in.identity) {
  throw 'The recorded firewall rule changed or could not be verified.'
}
$desired = if ($in.on) { 'True' } else { 'False' }
if ($rs[0].Enabled.ToString() -ne $desired) { Set-NetFirewallRule -InputObject $rs[0] -Enabled $desired }
Write-Doc ([ordered]@{ ok = $true })
`

// scriptSetBinding binds or unbinds one component on the host vNIC.
const scriptSetBinding = buildPrelude + `$b = @(Get-HostBinding $in.component)
if ($b.Count -ne 1) { throw 'The host vNIC binding was not found.' }
$null = Get-HostVNIC $in.switchId $in.owner $in.managementId ([int]$in.ifIndex)
if ($in.on) { Enable-NetAdapterBinding -InputObject $b[0] } else { Disable-NetAdapterBinding -InputObject $b[0] }
Write-Doc ([ordered]@{ ok = $true })
`

// scriptLinkLocal reads the host vNIC's IPv6 link-local address.
const scriptLinkLocal = buildPrelude + `$ll = @(Get-NetIPAddress -InterfaceIndex ([int]$in.ifIndex) -AddressFamily IPv6 -ErrorAction SilentlyContinue | Where-Object { ([string]$_.IPAddress).StartsWith('fe80') } | ForEach-Object { ([string]$_.IPAddress) -replace '%.*$', '' })
Write-Doc ([ordered]@{ addrs = @($ll) })
`

// scriptCanaryRule adds or removes the inbound Allow for one canary port, for
// this program and the guest's address only.
const scriptCanaryRule = firewallPrelude + `$name = $in.id
$matches = @(Get-NetFirewallRule -Name $name -ErrorAction SilentlyContinue)
$identity = ''
if ($in.add) {
  if ($matches.Count -ne 0) { throw 'A canary rule with this name already exists.' }
  $r = New-NetFirewallRule -Name $name -DisplayName $name -Group $in.group -Direction Inbound -Action Allow -Protocol TCP -LocalPort ([int]$in.port) -RemoteAddress ([string]$in.remote) -Program $in.program -Description $in.desc
  $identity = Get-FirewallIdentity $r
} elseif ($matches.Count -gt 0) {
  if ($matches.Count -ne 1 -or $matches[0].Group -ne $in.group -or $matches[0].Description -cne $in.desc -or
      -not $in.identity -or (Get-FirewallIdentity $matches[0]) -cne $in.identity -or $matches[0].Enabled.ToString() -ne 'True') {
    throw 'The canary firewall rule changed and was left in place.'
  }
  Remove-NetFirewallRule -InputObject $matches[0]
}
Write-Doc @{ identity = $identity }
`

const scriptPktmonList = buildPrelude + `$pm = Join-Path $env:SystemRoot 'System32\PktMon.exe'
$list = (& $pm list --json) -join [Environment]::NewLine
if ($LASTEXITCODE -ne 0) { throw ('pktmon list failed: ' + $LASTEXITCODE) }
Write-Doc ([ordered]@{ list = $list })
`

// Pktmon filters are shared across the host. Capture without changing them,
// then verify from the ETL metadata that no filter narrowed the capture.
const scriptPktmonStart = buildPrelude + `if (Get-EtwTraceSession -Name 'PktMon' -ErrorAction SilentlyContinue) { throw 'A pktmon session is already running.' }
$pm = Join-Path $env:SystemRoot 'System32\PktMon.exe'
& $pm start --capture --comp nics --pkt-size 128 -f $in.etl | Out-Null
if ($LASTEXITCODE -ne 0) { throw ('pktmon start failed: ' + $LASTEXITCODE) }
Start-Sleep -Seconds 3
Write-Doc ([ordered]@{ ok = $true })
`

// PktMon ETW event 20 describes a component, while 100 and 110
// describe IPv4 and IPv6 packet filters. These event IDs are independent of
// the Windows display language.
const scriptPktmonStop = buildPrelude + diskIdentityPrelude + `$s = @(Get-EtwTraceSession -Name 'PktMon' -ErrorAction SilentlyContinue)
if ($s.Count -ne 1 -or [string]$s[0].LocalFilePath -ne $in.etl -or [CpdDisk]::Identify($in.etl) -cne $in.etlId) { throw 'The running pktmon capture is not the one this proof started.' }
$pm = Join-Path $env:SystemRoot 'System32\PktMon.exe'
& $pm stop | Out-Null
if ($LASTEXITCODE -ne 0) { throw ('pktmon stop failed: ' + $LASTEXITCODE) }
# Windows PowerShell 5.1 can return no matches for the ETL when the
# hashtable includes ProviderName. Check the publisher GUID on the results.
$pktmonProvider = [guid]'4d4f80d9-c8bd-4d73-bb5b-19c90402c5ac'
$metadata = @(Get-WinEvent -FilterHashtable @{ Path = $in.etl; Id = @(20, 30, 100, 110) } -Oldest -ErrorAction Stop |
  Where-Object { $_.ProviderId -eq $pktmonProvider })
if ($metadata.Count -eq 0) { throw 'The capture has no pktmon metadata.' }
$indices = @{}
foreach ($event in @($metadata | Where-Object { $_.Id -eq 30 })) {
  $p = $event.Properties
  if ([int]$p[1].Value -ne 1) { continue } # PktMon IfIndex property
  $id = [int]$p[0].Value
  $value = [int]$p[2].Value
  if ($indices.ContainsKey($id) -and $indices[$id] -ne $value) { throw 'A pktmon interface index changed during capture.' }
  $indices[$id] = $value
}
$components = @(foreach ($event in @($metadata | Where-Object { $_.Id -eq 20 })) {
  $p = $event.Properties
  $id = [int]$p[0].Value
  # Event 20 holds the ID, the type, the driver file and a description.
  $name = ''; if ($p.Count -gt 3) { $name = [string]$p[3].Value }
  @{ id = $id; type = [int]$p[1].Value; driver = [string]$p[2].Value; name = $name; ifIndex = [int]$indices[$id] }
})
Write-Doc ([ordered]@{
  components = $components
  filtered = [bool]($metadata | Where-Object { $_.Id -eq 100 -or $_.Id -eq 110 } | Select-Object -First 1)
})
`

// scriptPktmonExport exports each asked component of a capture to its own
// pcapng.
const scriptPktmonExport = buildPrelude + `$pm = Join-Path $env:SystemRoot 'System32\PktMon.exe'
$out = @()
foreach ($c in @($in.components)) {
  $p = Join-Path $in.dir ('c' + [string]$c + '.pcapng')
  if (Test-Path -LiteralPath $p) { throw ('A pktmon export already exists: ' + $p) }
  & $pm etl2pcap $in.etl --out $p --component-id ([int]$c) | Out-Null
  if ($LASTEXITCODE -eq 0 -and (Test-Path -LiteralPath $p)) { $out += [ordered]@{ id = [int]$c; path = $p } }
}
Write-Doc ([ordered]@{ files = @($out) })
`

var proberScripts = map[string]string{
	"SetL3":        scriptSetL3,
	"SetBinding":   scriptSetBinding,
	"LinkLocal":    scriptLinkLocal,
	"CanaryRule":   scriptCanaryRule,
	"PktmonList":   scriptPktmonList,
	"PktmonStart":  scriptPktmonStart,
	"PktmonStop":   scriptPktmonStop,
	"PktmonExport": scriptPktmonExport,
}

func init() {
	for k, v := range proberScripts {
		hvScripts["Prober."+k] = v
	}
}

// psProber is the Prober for one instance.
type psProber struct {
	h *psHost
	n Names
	// program is this exe, the only program the canary rules let in.
	program string
	// listen opens the canary's listener; net.Listen when nil.
	listen func(network, addr string) (net.Listener, error)
	// hv opens an hv_sock listener whose registration carries the ElementName
	// element; nil where there is none.
	hv func(ctx context.Context, bindVMID, service string, register bool, element string) (HVListener, error)
	// owner is this install's owner token, written into every object the
	// proof makes. Build sets it from desired.json (see forPlan).
	owner            string
	managementID     string
	firewallIdentity string
	switchID         string
	// tmp holds pktmon's capture files.
	tmp string
}

var _ Prober = (*psProber)(nil)

// forPlan scopes the proof to the installation's owner and switch.
func (p *psProber) forPlan(plan Plan) Prober {
	return &psProber{h: p.h, n: p.n, program: p.program, listen: p.listen, hv: p.hv, tmp: p.tmp, owner: plan.Owner, switchID: plan.SwitchID, managementID: plan.ManagementAdapterID, firewallIdentity: plan.FirewallIdentity}
}

// checkOwner refuses to make a proof object without a valid owner token:
// ClearProof could never tell it apart.
func (p *psProber) checkOwner() error {
	if !ownerRe.MatchString(p.owner) {
		return errors.New("the proof has no owner token for the objects it makes")
	}
	return nil
}

// SetL3 turns our group's firewall rule on or off.
func (p *psProber) SetL3(ctx context.Context, n Names, owner string, on bool) error {
	if n != p.n || owner != p.owner {
		return errors.New("SetL3 names another installation")
	}
	if err := p.checkOwner(); err != nil {
		return err
	}
	return p.h.ok(ctx, scriptSetL3, map[string]any{"group": n.FirewallGroup, "id": blockFirewallID(n, owner), "owner": owner, "on": on, "identity": p.firewallIdentity})
}

// SetBinding binds or unbinds ms_tcpip6 on the host vNIC: the only component
// the proof touches.
func (p *psProber) SetBinding(ctx context.Context, ifIndex int, component string, on bool) error {
	if err := checkIfIndex(ifIndex); err != nil {
		return err
	}
	if component != "ms_tcpip6" {
		return fmt.Errorf("the proof never changes %q", component)
	}
	if err := p.checkOwner(); err != nil {
		return err
	}
	if err := checkVMID("switch ID", p.switchID); err != nil {
		return err
	}
	return p.h.ok(ctx, scriptSetBinding, map[string]any{"ifIndex": ifIndex, "switchId": p.switchID, "managementId": p.managementID, "owner": p.owner, "component": component, "on": on})
}

// LinkLocal reads the host vNIC's IPv6 link-local address.
func (p *psProber) LinkLocal(ctx context.Context, ifIndex int) (netip.Addr, error) {
	if err := checkIfIndex(ifIndex); err != nil {
		return netip.Addr{}, err
	}
	var w struct {
		Addrs []string `json:"addrs"`
	}
	if err := p.h.call(ctx, scriptLinkLocal, map[string]int{"ifIndex": ifIndex}, &w); err != nil {
		return netip.Addr{}, err
	}
	for _, s := range w.Addrs {
		if a, err := netip.ParseAddr(s); err == nil && a.Is6() && a.IsLinkLocalUnicast() {
			return a, nil
		}
	}
	return netip.Addr{}, errors.New("the host vNIC has no IPv6 link-local address")
}

// HVListener opens an hv_sock listener.
func (p *psProber) HVListener(ctx context.Context, bindVMID, service string, register bool) (HVListener, error) {
	if !guidRe.MatchString(bindVMID) || !guidRe.MatchString(service) {
		return nil, errors.New("hv_sock IDs must be GUIDs")
	}
	if p.hv == nil {
		return nil, ErrUnsupported
	}
	if err := p.checkOwner(); err != nil {
		return nil, err
	}
	return p.hv(ctx, bindVMID, service, register, ProofElementName(p.n, p.owner))
}

// tokenRead caps what a canary reads from one connection.
const tokenRead = 64

var canaryToken = regexp.MustCompile(`^[A-Za-z0-9:._-]{1,64}$`)

// goCanary is a TCP listener in this process.
type goCanary struct {
	ln      net.Listener
	closeFn func() error
	mu      sync.Mutex
	arrived []string
	once    sync.Once
	closeMu sync.Mutex
	done    chan struct{}
}

func (c *goCanary) serve() {
	defer close(c.done)
	for {
		conn, err := c.ln.Accept()
		if err != nil {
			return
		}
		go func(conn net.Conn) {
			defer conn.Close()
			conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			b, _ := io.ReadAll(io.LimitReader(conn, tokenRead))
			tok := strings.TrimSpace(string(b))
			if !canaryToken.MatchString(tok) {
				tok = "connected"
			}
			c.mu.Lock()
			c.arrived = append(c.arrived, tok)
			c.mu.Unlock()
		}(conn)
	}
}

func (c *goCanary) Arrived() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.arrived...)
}

func (c *goCanary) Close() error {
	c.once.Do(func() {
		c.ln.Close()
		<-c.done
	})
	c.closeMu.Lock()
	defer c.closeMu.Unlock()
	if c.closeFn == nil {
		return nil
	}
	if err := c.closeFn(); err != nil {
		return err
	}
	c.closeFn = nil
	return nil
}

// Canary listens on a host address and port, behind a firewall Allow for this
// program, that port and the guest's prefix from, and records every
// connection: the token it sent, or "connected". The proof decides which
// arrivals count.
func (p *psProber) Canary(ctx context.Context, ip netip.Addr, port uint16, from netip.Prefix) (Canary, error) {
	if !ip.IsValid() || port == 0 {
		return nil, fmt.Errorf("canary %v:%d is not an address and port", ip, port)
	}
	if !from.IsValid() {
		return nil, errors.New("canary: no guest address to take connections from")
	}
	if err := checkWinPath("program", p.program); err != nil {
		return nil, err
	}
	if err := p.checkOwner(); err != nil {
		return nil, err
	}
	group := p.n.FirewallGroup + proofGroupSuffix
	id := randomArtifactName(group+"-", "")
	var identity string
	rule := func(ctx context.Context, add bool) error {
		var w struct {
			Identity string `json:"identity"`
		}
		err := p.h.call(ctx, scriptCanaryRule, map[string]any{
			"group": group, "id": id, "port": port, "remote": from.Masked().String(), "program": p.program,
			"add": add, "desc": proofCanaryDesc(p.owner), "identity": identity,
		}, &w)
		if err == nil && add {
			if !identityDigestRE.MatchString(w.Identity) {
				return errors.New("the firewall did not return a canary rule identity")
			}
			identity = w.Identity
		}
		return err
	}
	if err := rule(ctx, true); err != nil {
		return nil, err
	}
	listen := p.listen
	if listen == nil {
		listen = net.Listen
	}
	ln, err := listen("tcp", netip.AddrPortFrom(ip, port).String())
	if err != nil {
		rule(context.WithoutCancel(ctx), false)
		return nil, err
	}
	c := &goCanary{ln: ln, done: make(chan struct{}), closeFn: func() error {
		cctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		return rule(cctx, false)
	}}
	go c.serve()
	return c, nil
}

// Pktmon starts a capture on every NIC. The proof filters the exported
// packets in Go and leaves the host's filters alone.
func (p *psProber) Pktmon(ctx context.Context, filters []PktFilter) (Capture, error) {
	if len(filters) == 0 || len(filters) > 32 {
		return nil, fmt.Errorf("%d pktmon filters; 1 to 32", len(filters))
	}
	if err := p.checkOwner(); err != nil {
		return nil, err
	}
	for _, f := range filters {
		if !f.Addr.IsValid() || (f.Proto != "" && f.Proto != "TCP" && f.Proto != "UDP") {
			return nil, fmt.Errorf("pktmon filter %+v is not allowed", f)
		}
	}
	if err := checkVMID("proof switch ID", p.switchID); err != nil {
		return nil, err
	}
	var inventory struct {
		List string `json:"list"`
	}
	if err := p.h.call(ctx, scriptPktmonList, nil, &inventory); err != nil {
		return nil, err
	}
	comps, err := parsePktmonComponents([]byte(inventory.List))
	if err != nil {
		return nil, err
	}
	host, err := p.h.ReadHostNet(ctx, nil)
	if err != nil {
		return nil, err
	}
	root, proof := filepath.Dir(p.tmp), filepath.Base(p.tmp)
	j, err := openArtifactLog(root, true)
	if err != nil {
		return nil, err
	}
	defer j.file.Close()
	if err = j.verify(proof); errors.Is(err, fs.ErrNotExist) {
		err = j.makeDir(proof)
	}
	if err != nil {
		return nil, err
	}
	rel := filepath.Join(proof, randomArtifactName("pktmon-"+p.owner+"-", ""))
	if err := j.makeDir(rel); err != nil {
		return nil, err
	}
	dir := filepath.Join(root, rel)
	etlRel := filepath.Join(rel, "capture.etl")
	etl := filepath.Join(root, etlRel)
	if err := j.record(etlRel, ""); err != nil {
		return nil, err
	}
	if err := p.h.ok(ctx, scriptPktmonStart, map[string]string{"etl": etl}); err != nil {
		return nil, err
	}
	id, err := j.note(etlRel)
	if err != nil {
		return nil, err
	}
	return &psCapture{p: p, dir: dir, etl: etl, etlID: id, root: root, rel: rel, filters: filters, comps: comps, host: host}, nil
}

type psCapture struct {
	p       *psProber
	dir     string
	etl     string
	etlID   string
	root    string
	rel     string
	filters []PktFilter
	comps   map[int]pktComp
	host    HostNet
}

// Stop stops the capture, classifies each NIC component by the host's view of
// its interface, and reads the packets each one saw.
func (c *psCapture) Stop(ctx context.Context) ([]PktRecord, error) {
	var lw struct {
		Components []pktComp `json:"components"`
		Filtered   bool      `json:"filtered"`
	}
	if err := c.p.h.call(ctx, scriptPktmonStop, map[string]string{"etl": c.etl, "etlId": c.etlID}, &lw); err != nil {
		return nil, err
	}
	j, err := openArtifactLog(c.root, false)
	if err != nil {
		return nil, err
	}
	defer j.file.Close()
	etlRel := filepath.Join(c.rel, "capture.etl")
	defer func() {
		_ = j.remove(etlRel)
		_ = j.remove(c.rel)
	}()
	// pktmon stop writes the capture to a new file at the same path, with a
	// new file ID. Record it, or the capture is never deleted.
	if _, err := j.note(etlRel); err != nil {
		return nil, err
	}
	if lw.Filtered {
		return nil, errors.New("pktmon did not prove it captured without host filters")
	}
	comps := c.comps
	if err := reconcilePktmon(comps, lw.Components); err != nil {
		return nil, err
	}
	want := []int{}
	for id, c := range comps {
		if !c.Twin {
			want = append(want, id)
		}
	}
	slices.Sort(want)
	for _, id := range want {
		if err := j.record(filepath.Join(c.rel, "c"+strconv.Itoa(id)+".pcapng"), ""); err != nil {
			return nil, err
		}
	}
	var w struct {
		Files []struct {
			ID   int    `json:"id"`
			Path string `json:"path"`
		} `json:"files"`
	}
	if err := c.p.h.call(ctx, scriptPktmonExport, map[string]any{"etl": c.etl, "dir": c.dir, "components": want}, &w); err != nil {
		return nil, err
	}
	files := make(map[int]string, len(w.Files))
	for _, f := range w.Files {
		expected := filepath.Join(c.dir, "c"+strconv.Itoa(f.ID)+".pcapng")
		if _, ok := comps[f.ID]; !ok || f.Path != expected || files[f.ID] != "" {
			return nil, fmt.Errorf("unexpected export for pktmon component %d", f.ID)
		}
		rel := filepath.Join(c.rel, filepath.Base(f.Path))
		if _, err := j.note(rel); err != nil {
			return nil, err
		}
		defer func() { _ = j.remove(rel) }()
		files[f.ID] = f.Path
	}
	var out []PktRecord
	for _, id := range want {
		path := files[id]
		if path == "" {
			return nil, fmt.Errorf("pktmon did not export component %d (%s)", id, comps[id].Name)
		}
		cp := comps[id]
		class := componentClass(c.host, c.p.switchID, cp.IfIndex)
		if err := j.verify(filepath.Join(c.rel, filepath.Base(path))); err != nil {
			return nil, err
		}
		b, err := readCapped(path, 64<<20, true)
		if err != nil {
			return nil, err
		}
		pkts, err := parsePcapng(b)
		if err != nil {
			return nil, fmt.Errorf("component %d: %w", id, err)
		}
		for _, k := range pkts {
			r := PktRecord{Component: cp.Name, Class: class, Src: k.Src, Dst: k.Dst, Proto: k.Proto, Port: k.Port}
			if matchFilters(c.filters, r) {
				out = append(out, r)
			}
		}
	}
	return out, nil
}

// componentClass is the class of the interface a pktmon NIC component sits on:
// our vNIC, a physical or a tunnel NIC, or "" for anything else (which fails
// closed on a green step).
func componentClass(h HostNet, ourSwitch string, ifIndex int) string {
	if ifIndex <= 0 {
		return ""
	}
	for _, i := range h.Interfaces {
		if i.IfIndex == ifIndex && ourSwitch != "" && strings.EqualFold(i.VMSwitchID, ourSwitch) {
			return "vnic"
		}
	}
	switch c, _ := classOf(h, ifIndex, ourSwitch); c {
	case IfPhysical:
		return "physical"
	case IfTunnel:
		return "tunnel"
	}
	return ""
}

func matchFilters(fs []PktFilter, r PktRecord) bool {
	for _, f := range fs {
		if f.Addr.WithZone("") == r.Dst && (f.Proto == "" || f.Proto == r.Proto) && (f.Port == 0 || f.Port == r.Port) {
			return true
		}
	}
	return false
}
