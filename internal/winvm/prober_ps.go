package winvm

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
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

// proofCanaryDesc is the description of every canary rule, so ClearProof
// removes only those in the instance's proof group.
const proofCanaryDesc = "CashPilot Secure VM proof canary"

// ProofElementName is the ElementName of the proof's hv_sock test key for
// instance n, so ClearProof finds a key a killed proof left, and only that.
func ProofElementName(n Names) string { return "CashPilot Secure VM proof " + n.VM }

// scriptSetL3 turns the rules of our group on or off.
const scriptSetL3 = buildPrelude + `$rs = @(Get-NetFirewallRule -Group $in.group -ErrorAction SilentlyContinue)
if ($rs.Count -eq 0) { throw 'The group has no rule.' }
$rs | Set-NetFirewallRule -Enabled ($(if ($in.on) { 'True' } else { 'False' }))
Write-Doc ([ordered]@{ ok = $true })
`

// scriptSetBinding binds or unbinds one component on the host vNIC.
const scriptSetBinding = buildPrelude + `$na = @(Get-NetAdapter -IncludeHidden | Where-Object { $_.ifIndex -eq [int]$in.ifIndex })
if ($na.Count -ne 1) { throw 'The host vNIC was not found.' }
if ($in.on) { Enable-NetAdapterBinding -Name $na[0].Name -ComponentID $in.component -IncludeHidden -AllBindings } else { Disable-NetAdapterBinding -Name $na[0].Name -ComponentID $in.component -IncludeHidden -AllBindings }
Write-Doc ([ordered]@{ ok = $true })
`

// scriptAllow adds or removes one Allow Both row on the VM's one adapter, then
// lists the Allow rows.
const scriptAllow = buildPrelude + `$vm = @(Find-OurVM $in.vm)
if ($vm.Count -ne 1) { throw 'The VM was not found.' }
$a = @(Get-VMNetworkAdapter -VM $vm[0])
if ($a.Count -ne 1) { throw 'The VM must have exactly one network adapter.' }
$r = ([string]$in.remote) -replace '/(32|128)$', ''
if ($in.add) { Add-VMNetworkAdapterAcl -VMNetworkAdapter $a[0] -RemoteIPAddress $r -Direction Both -Action Allow }
else { @(Get-VMNetworkAdapterAcl -VMNetworkAdapter $a[0] | Where-Object { ($_.Action.ToString() -eq 'Allow') -and ((([string]$_.RemoteAddress) -replace '/(32|128)$', '') -eq $r) }) | Remove-VMNetworkAdapterAcl }
Write-Doc ([ordered]@{ allow = @(Get-VMNetworkAdapterAcl -VMNetworkAdapter $a[0] | Where-Object { $_.Action.ToString() -eq 'Allow' } | ForEach-Object { [string]$_.RemoteAddress }) })
`

// scriptLinkLocal reads the host vNIC's IPv6 link-local address.
const scriptLinkLocal = buildPrelude + `$ll = @(Get-NetIPAddress -InterfaceIndex ([int]$in.ifIndex) -AddressFamily IPv6 -ErrorAction SilentlyContinue | Where-Object { ([string]$_.IPAddress).StartsWith('fe80') } | ForEach-Object { ([string]$_.IPAddress) -replace '%.*$', '' })
Write-Doc ([ordered]@{ addrs = @($ll) })
`

// scriptCanaryRule adds or removes the inbound Allow for one canary port, for
// this program and the guest's address only.
const scriptCanaryRule = buildPrelude + `$name = $in.group + '-' + [string]$in.port
Get-NetFirewallRule -Name $name -ErrorAction SilentlyContinue | Remove-NetFirewallRule
if ($in.add) { New-NetFirewallRule -Name $name -DisplayName $name -Group $in.group -Direction Inbound -Action Allow -Protocol TCP -LocalPort ([int]$in.port) -RemoteAddress ([string]$in.remote) -Program $in.program -Description $in.desc | Out-Null }
Write-Doc ([ordered]@{ ok = $true })
`

// scriptPktmonStart starts a pktmon capture on every NIC, with one filter per
// target. It refuses while another pktmon session runs.
const scriptPktmonStart = buildPrelude + `if (Get-EtwTraceSession -Name 'PktMon' -ErrorAction SilentlyContinue) { throw 'A pktmon session is already running.' }
$pm = Join-Path $env:SystemRoot 'System32\PktMon.exe'
& $pm filter remove | Out-Null
foreach ($f in @($in.filters)) {
  $a = @('filter', 'add', 'cpd', '-i', [string]$f.addr)
  if ($f.proto) { $a += @('-t', [string]$f.proto) }
  if ([int]$f.port -gt 0) { $a += @('-p', [string]$f.port) }
  & $pm @a | Out-Null
  if ($LASTEXITCODE -ne 0) { throw ('pktmon filter add failed: ' + $LASTEXITCODE) }
}
& $pm start --capture --comp nics --pkt-size 128 -f $in.etl | Out-Null
if ($LASTEXITCODE -ne 0) { throw ('pktmon start failed: ' + $LASTEXITCODE) }
Start-Sleep -Seconds 3
Write-Doc ([ordered]@{ ok = $true })
`

// scriptPktmonStop stops the capture and lists pktmon's components as JSON.
const scriptPktmonStop = buildPrelude + `$pm = Join-Path $env:SystemRoot 'System32\PktMon.exe'
& $pm stop | Out-Null
& $pm filter remove | Out-Null
$list = (& $pm list --json) -join "` + "`n" + `"
Write-Doc ([ordered]@{ list = $list })
`

// scriptPktmonExport exports each asked component of a capture to its own
// pcapng.
const scriptPktmonExport = buildPrelude + `$pm = Join-Path $env:SystemRoot 'System32\PktMon.exe'
$out = @()
foreach ($c in @($in.components)) {
  $p = Join-Path $in.dir ('c' + [string]$c + '.pcapng')
  & $pm etl2pcap $in.etl --out $p --component-id ([int]$c) | Out-Null
  if ($LASTEXITCODE -eq 0 -and (Test-Path -LiteralPath $p)) { $out += [ordered]@{ id = [int]$c; path = $p } }
}
Write-Doc ([ordered]@{ files = @($out) })
`

// scriptSwitchByName finds our switch's ID by name.
const scriptSwitchByName = buildPrelude + `Write-Doc ([ordered]@{ ids = @(Get-VMSwitch | Where-Object { $_.Name -eq $in.switch } | ForEach-Object { $_.Id.ToString() }) })
`

var proberScripts = map[string]string{
	"SetL3":        scriptSetL3,
	"SetBinding":   scriptSetBinding,
	"Allow":        scriptAllow,
	"LinkLocal":    scriptLinkLocal,
	"CanaryRule":   scriptCanaryRule,
	"PktmonStart":  scriptPktmonStart,
	"PktmonStop":   scriptPktmonStop,
	"PktmonExport": scriptPktmonExport,
	"SwitchByName": scriptSwitchByName,
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
	// hv opens an hv_sock listener; nil where there is none.
	hv func(ctx context.Context, bindVMID, service string, register bool) (HVListener, error)
	// tmp holds pktmon's capture files.
	tmp string

	mu      sync.Mutex
	pktmonN int
}

var _ Prober = (*psProber)(nil)

// SetL3 turns our group's firewall rule on or off.
func (p *psProber) SetL3(ctx context.Context, n Names, on bool) error {
	if n != p.n {
		return errors.New("SetL3 names another instance")
	}
	return p.h.ok(ctx, scriptSetL3, map[string]any{"group": n.FirewallGroup, "on": on})
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
	return p.h.ok(ctx, scriptSetBinding, map[string]any{"ifIndex": ifIndex, "component": component, "on": on})
}

// checkAllow accepts one host address: the proof opens holes one address at a
// time.
func checkAllow(q netip.Prefix) (string, error) {
	if !q.IsValid() || q.Bits() != q.Addr().BitLen() || q.Addr().Zone() != "" {
		return "", fmt.Errorf("allow %v is not one address", q)
	}
	return q.Addr().String(), nil
}

func (p *psProber) allow(ctx context.Context, vmID string, q netip.Prefix, add bool) error {
	if err := checkVMID("VM ID", vmID); err != nil {
		return err
	}
	r, err := checkAllow(q)
	if err != nil {
		return err
	}
	var w struct {
		Allow []string `json:"allow"`
	}
	if err := p.h.call(ctx, scriptAllow, map[string]any{"vm": vmID, "remote": r, "add": add}, &w); err != nil {
		return err
	}
	has := false
	for _, x := range w.Allow {
		if y, err := parsePrefixOrAddr(x); err == nil && y.Masked() == q {
			has = true
		}
	}
	if has != add {
		return fmt.Errorf("the Allow for %s is %s after the change", r, map[bool]string{true: "there", false: "missing"}[has])
	}
	return nil
}

// AddAllow opens an L2 hole for one address.
func (p *psProber) AddAllow(ctx context.Context, vmID string, q netip.Prefix) error {
	return p.allow(ctx, vmID, q, true)
}

// RemoveAllow closes it.
func (p *psProber) RemoveAllow(ctx context.Context, vmID string, q netip.Prefix) error {
	return p.allow(ctx, vmID, q, false)
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
	return p.hv(ctx, bindVMID, service, register)
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
	var err error
	c.once.Do(func() {
		c.ln.Close()
		<-c.done
		if c.closeFn != nil {
			err = c.closeFn()
		}
	})
	return err
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
	group := p.n.FirewallGroup + proofGroupSuffix
	rule := func(ctx context.Context, add bool) error {
		return p.h.ok(ctx, scriptCanaryRule, map[string]any{"group": group, "port": port, "remote": from.Masked().String(), "program": p.program, "add": add, "desc": proofCanaryDesc})
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

// Pktmon starts a capture on every NIC with one filter per target.
func (p *psProber) Pktmon(ctx context.Context, fs []PktFilter) (Capture, error) {
	if len(fs) == 0 || len(fs) > 32 {
		return nil, fmt.Errorf("%d pktmon filters; 1 to 32", len(fs))
	}
	type fw struct {
		Addr  string `json:"addr"`
		Proto string `json:"proto"`
		Port  uint16 `json:"port"`
	}
	var args []fw
	for _, f := range fs {
		if !f.Addr.IsValid() || (f.Proto != "" && f.Proto != "TCP" && f.Proto != "UDP") {
			return nil, fmt.Errorf("pktmon filter %+v is not allowed", f)
		}
		args = append(args, fw{f.Addr.WithZone("").String(), f.Proto, f.Port})
	}
	p.mu.Lock()
	p.pktmonN++
	dir := filepath.Join(p.tmp, "pktmon-"+strconv.Itoa(p.pktmonN))
	p.mu.Unlock()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	etl := filepath.Join(dir, "capture.etl")
	if err := p.h.ok(ctx, scriptPktmonStart, map[string]any{"filters": args, "etl": etl}); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	return &psCapture{p: p, dir: dir, etl: etl, filters: fs}, nil
}

type psCapture struct {
	p       *psProber
	dir     string
	etl     string
	filters []PktFilter
}

// Stop stops the capture, classifies each NIC component by the host's view of
// its interface, and reads the packets each one saw.
func (c *psCapture) Stop(ctx context.Context) ([]PktRecord, error) {
	defer os.RemoveAll(c.dir)
	var lw struct {
		List string `json:"list"`
	}
	if err := c.p.h.call(ctx, scriptPktmonStop, map[string]string{}, &lw); err != nil {
		return nil, err
	}
	comps, err := parsePktmonComponents([]byte(lw.List))
	if err != nil {
		return nil, err
	}
	h, err := c.p.h.ReadHostNet(ctx, nil)
	if err != nil {
		return nil, err
	}
	var sw struct {
		IDs []string `json:"ids"`
	}
	if err := c.p.h.call(ctx, scriptSwitchByName, map[string]string{"switch": c.p.n.Switch}, &sw); err != nil {
		return nil, err
	}
	ourSwitch := ""
	if len(sw.IDs) == 1 {
		ourSwitch = sw.IDs[0]
	}
	want := []int{}
	for id := range comps {
		want = append(want, id)
	}
	slices.Sort(want)
	var w struct {
		Files []struct {
			ID   int    `json:"id"`
			Path string `json:"path"`
		} `json:"files"`
	}
	if err := c.p.h.call(ctx, scriptPktmonExport, map[string]any{"etl": c.etl, "dir": c.dir, "components": want}, &w); err != nil {
		return nil, err
	}
	var out []PktRecord
	for _, f := range w.Files {
		cp, ok := comps[f.ID]
		if !ok || filepath.Dir(f.Path) != c.dir {
			continue
		}
		class := componentClass(h, ourSwitch, cp.IfIndex)
		b, err := readCapped(f.Path, 64<<20, true)
		if err != nil {
			return nil, err
		}
		pkts, err := parsePcapng(b)
		if err != nil {
			return nil, fmt.Errorf("component %d: %w", f.ID, err)
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
