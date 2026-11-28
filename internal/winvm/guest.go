package winvm

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
)

// The guest scripts. Each one runs inside the guest over PowerShell Direct as
// cpadmin, gets one JSON argument as $a, and prints one JSON document. Only the
// install and the proof use them, before any earner exists; the guard never
// reads the guest.

// guestPrelude starts every guest script.
const guestPrelude = `param([string]$json)
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$a = $json | ConvertFrom-Json
`

// scriptGuestPing answers once PowerShell Direct works.
var scriptGuestPing = GuestScript{Name: "ping", Text: guestPrelude + `ConvertTo-Json -Compress -InputObject ([ordered]@{ ok = $true })
`}

// scriptFirstSession is the first PowerShell Direct session (ADR §5 item 8). It
// sets the real passwords of both accounts, stores earner's in the autologon
// LSA secret, sets the static ARP entry for the gateway again, deletes Windows
// Setup's copies of the answer file. It lists the copies it could not delete.
// The VM is off the network here; Windows activates by itself once the guard
// connects it.
var scriptFirstSession = GuestScript{Name: "first-session", Text: guestPrelude + `Add-Type -TypeDefinition @'
using System; using System.Runtime.InteropServices;
public static class CpdLsa {
  [StructLayout(LayoutKind.Sequential)] struct US { public ushort L; public ushort M; public IntPtr B; }
  [StructLayout(LayoutKind.Sequential)] struct OA { public int Length; public IntPtr a; public IntPtr b; public uint c; public IntPtr d; public IntPtr e; }
  [DllImport("advapi32.dll")] static extern uint LsaOpenPolicy(IntPtr s, ref OA o, uint acc, out IntPtr h);
  [DllImport("advapi32.dll")] static extern uint LsaStorePrivateData(IntPtr h, ref US k, ref US v);
  [DllImport("advapi32.dll")] static extern uint LsaClose(IntPtr h);
  static US S(string s) { var u = new US(); u.B = Marshal.StringToHGlobalUni(s); u.L = (ushort)(s.Length * 2); u.M = (ushort)(s.Length * 2 + 2); return u; }
  public static uint Store(string pw) { var o = new OA(); IntPtr h; uint r = LsaOpenPolicy(IntPtr.Zero, ref o, 0x20, out h); if (r != 0) return r; var k = S("DefaultPassword"); var v = S(pw); r = LsaStorePrivateData(h, ref k, ref v); LsaClose(h); return r; }
}
'@
Set-LocalUser -Name 'cpadmin' -Password (ConvertTo-SecureString $a.admin -AsPlainText -Force)
Set-LocalUser -Name 'earner' -Password (ConvertTo-SecureString $a.earner -AsPlainText -Force)
$wl = 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Winlogon'
Set-ItemProperty -LiteralPath $wl -Name AutoAdminLogon -Value '1'
Set-ItemProperty -LiteralPath $wl -Name DefaultUserName -Value 'earner'
Remove-ItemProperty -LiteralPath $wl -Name DefaultPassword -ErrorAction SilentlyContinue
$r = [CpdLsa]::Store([string]$a.earner)
if ($r -ne 0) { throw ('LsaStorePrivateData failed: ' + $r) }
$na = @(Get-NetAdapter | Where-Object { $_.MacAddress -eq $a.guestMac })
if ($na.Count -eq 1) {
  Get-NetNeighbor -InterfaceIndex $na[0].ifIndex -IPAddress $a.gateway -ErrorAction SilentlyContinue | Remove-NetNeighbor -Confirm:$false
  New-NetNeighbor -InterfaceIndex $na[0].ifIndex -IPAddress $a.gateway -LinkLayerAddress $a.hostMac -State Permanent | Out-Null
}
$left = @()
foreach ($p in @('C:\Windows\Panther\unattend.xml', 'C:\Windows\Panther\unattend-original.xml', 'C:\Windows\Panther\Unattend\unattend.xml', 'C:\Windows\System32\Sysprep\unattend.xml')) {
  if (Test-Path -LiteralPath $p) { Remove-Item -LiteralPath $p -Force -ErrorAction SilentlyContinue; if (Test-Path -LiteralPath $p) { $left += $p } }
}
ConvertTo-Json -Compress -InputObject ([ordered]@{ ok = $true; pantherLeft = @($left) })
`}

// guestNIC finds the guest's one network adapter, for the probes.
const guestNIC = `$nic = @(Get-NetAdapter | Where-Object { $_.Status -eq 'Up' })
if ($nic.Count -ne 1) { throw ('the guest has ' + $nic.Count + ' network adapters up') }
$nic = $nic[0]
`

// scriptGuestTCP sends one TCP connect carrying a token. A link-local IPv6
// target gets the guest adapter's zone. It reports whether the connect was
// attempted; what happened to it is for the host to see.
var scriptGuestTCP = GuestScript{Name: "probe-tcp", Text: guestPrelude + guestNIC + `$ip = [Net.IPAddress]::Parse($a.ip)
if ($ip.IsIPv6LinkLocal) { $ip.ScopeId = $nic.ifIndex }
$c = New-Object Net.Sockets.TcpClient ($ip.AddressFamily)
$r = 'timeout'
try {
  $ar = $c.BeginConnect($ip, [int]$a.port, $null, $null)
  if ($ar.AsyncWaitHandle.WaitOne(3000)) {
    try { $c.EndConnect($ar); $s = $c.GetStream(); $b = [Text.Encoding]::ASCII.GetBytes($a.token); $s.Write($b, 0, $b.Length); $s.Flush(); Start-Sleep -Milliseconds 300; $r = 'open' } catch { $r = 'refused' }
  }
} catch { $r = 'noroute' } finally { $c.Close() }
ConvertTo-Json -Compress -InputObject ([ordered]@{ sent = $true; result = $r })
`}

// scriptGuestUDP sends n datagrams carrying a token.
var scriptGuestUDP = GuestScript{Name: "probe-udp", Text: guestPrelude + `$ip = [Net.IPAddress]::Parse($a.ip)
$u = New-Object Net.Sockets.UdpClient ($ip.AddressFamily)
try { $b = [Text.Encoding]::ASCII.GetBytes($a.token); for ($i = 0; $i -lt [int]$a.n; $i++) { [void]$u.Send($b, $b.Length, (New-Object Net.IPEndPoint ($ip, [int]$a.port))) } } finally { $u.Close() }
ConvertTo-Json -Compress -InputObject ([ordered]@{ sent = [int]$a.n })
`}

// scriptGuestDNS asks one server for the A records of a name.
var scriptGuestDNS = GuestScript{Name: "probe-dns", Text: guestPrelude + `$ans = @()
try { $ans = @(Resolve-DnsName -Name $a.name -Server $a.server -Type A -DnsOnly -QuickTimeout -NoHostsFile -ErrorAction Stop | Where-Object { $_.IPAddress } | ForEach-Object { [string]$_.IPAddress }) } catch { }
ConvertTo-Json -Compress -InputObject ([ordered]@{ answers = @($ans) })
`}

// scriptGuestNeighbor sets a static neighbour entry on the guest's adapter.
var scriptGuestNeighbor = GuestScript{Name: "probe-neighbor", Text: guestPrelude + guestNIC + `Get-NetNeighbor -InterfaceIndex $nic.ifIndex -IPAddress $a.ip -ErrorAction SilentlyContinue | Remove-NetNeighbor -Confirm:$false
New-NetNeighbor -InterfaceIndex $nic.ifIndex -IPAddress $a.ip -LinkLayerAddress $a.mac -State Permanent | Out-Null
ConvertTo-Json -Compress -InputObject ([ordered]@{ ok = $true })
`}

// scriptGuestHVSock connects to HV_GUID_PARENT:service over hv_sock and sends
// a token. The C# is the lab's hvsock.cs.
var scriptGuestHVSock = GuestScript{Name: "probe-hvsock", Text: guestPrelude + `Add-Type -TypeDefinition @'
using System; using System.Net; using System.Net.Sockets; using System.Text;
public class CpdHvEP : EndPoint {
  public Guid VmId, ServiceId;
  public CpdHvEP(Guid vm, Guid svc) { VmId = vm; ServiceId = svc; }
  public override AddressFamily AddressFamily { get { return (AddressFamily)34; } }
  public override SocketAddress Serialize() {
    var sa = new SocketAddress((AddressFamily)34, 36);
    byte[] v = VmId.ToByteArray(), s = ServiceId.ToByteArray();
    for (int i = 0; i < 16; i++) { sa[4 + i] = v[i]; sa[20 + i] = s[i]; }
    return sa; }
  public override EndPoint Create(SocketAddress sa) { return this; }
}
public static class CpdHv {
  public static string Try(Guid svc, string token) {
    Socket s = null;
    try {
      s = new Socket((AddressFamily)34, SocketType.Stream, (ProtocolType)1);
      var ar = s.BeginConnect(new CpdHvEP(new Guid("a42e7cda-d03f-480c-9cc2-a4de20abb878"), svc), null, null);
      if (!ar.AsyncWaitHandle.WaitOne(3000)) { return "timeout"; }
      try { s.EndConnect(ar); }
      catch (SocketException e) {
        if (e.SocketErrorCode == SocketError.ConnectionRefused) { return "refused"; }
        throw;
      }
      s.Send(Encoding.ASCII.GetBytes(token));
      return "connected";
    } catch (SocketException e) { return "socket-error:" + e.ErrorCode; }
    finally { if (s != null) { try { s.Close(); } catch { } } }
  }
}
'@
$r = [CpdHv]::Try([Guid]$a.service, [string]$a.token)
ConvertTo-Json -Compress -InputObject ([ordered]@{ connected = ($r -eq 'connected'); result = $r })
`}

// scriptGuestEgress asks an echo service for the guest's public address.
var scriptGuestEgress = GuestScript{Name: "probe-egress", Text: guestPrelude + `$ip = ([string](Invoke-RestMethod -Uri 'https://api.ipify.org' -TimeoutSec 10 -UseBasicParsing)).Trim()
ConvertTo-Json -Compress -InputObject ([ordered]@{ ip = $ip })
`}

// guestScripts is every guest script, for the parser test.
var guestScripts = []GuestScript{scriptGuestPing, scriptFirstSession, scriptGuestTCP, scriptGuestUDP,
	scriptGuestDNS, scriptGuestNeighbor, scriptGuestHVSock, scriptGuestEgress}

// GuestScripts lists every guest script, for the parser test and the lab.
func GuestScripts() []GuestScript { return slices.Clone(guestScripts) }

func guestScriptNamed(name string) (GuestScript, bool) {
	i := slices.IndexFunc(guestScripts, func(g GuestScript) bool { return g.Name == name })
	if i < 0 {
		return GuestScript{}, false
	}
	return guestScripts[i], true
}

// psDirectProbe is the product's GuestProbe: each probe is one fixed guest
// script over PowerShell Direct as cpadmin.
type psDirectProbe struct {
	hv    Builder
	vmID  string
	owner string
	cred  Cred
}

// NewGuestProbe is the product GuestProbe for one VM.
func NewGuestProbe(hv Builder, vmID, owner string, cred Cred) GuestProbe {
	return psDirectProbe{hv: hv, vmID: vmID, owner: owner, cred: cred}
}

var tokenRe = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)

func checkProbe(to netip.AddrPort, token string) error {
	if !to.Addr().IsValid() || to.Port() == 0 || to.Addr().Zone() != "" {
		return fmt.Errorf("probe target %v is not an address and a port", to)
	}
	if !tokenRe.MatchString(token) {
		return fmt.Errorf("probe token %q is not allowed", token)
	}
	return nil
}

func (g psDirectProbe) TCP(ctx context.Context, to netip.AddrPort, token string) (bool, error) {
	if err := checkProbe(to, token); err != nil {
		return false, err
	}
	var out struct {
		Sent   bool   `json:"sent"`
		Result string `json:"result"`
	}
	arg := map[string]any{"ip": to.Addr().String(), "port": to.Port(), "token": token}
	if err := g.hv.Guest(ctx, g.vmID, g.owner, g.cred, scriptGuestTCP, arg, &out); err != nil {
		return false, err
	}
	return out.Sent, nil
}

func (g psDirectProbe) UDP(ctx context.Context, to netip.AddrPort, token string, n int) error {
	if err := checkProbe(to, token); err != nil {
		return err
	}
	if n < 1 || n > 1000 {
		return fmt.Errorf("%d datagrams; one probe sends 1 to 1000", n)
	}
	var out struct {
		Sent int `json:"sent"`
	}
	arg := map[string]any{"ip": to.Addr().String(), "port": to.Port(), "token": token, "n": n}
	return g.hv.Guest(ctx, g.vmID, g.owner, g.cred, scriptGuestUDP, arg, &out)
}

var dnsNameRe = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,62}\.)*[A-Za-z0-9-]{1,63}\.?$`)

func (g psDirectProbe) DNS(ctx context.Context, server netip.Addr, name string) ([]netip.Addr, error) {
	if !server.Is4() || len(name) > 253 || !dnsNameRe.MatchString(name) {
		return nil, fmt.Errorf("DNS probe of %q at %v is not allowed", name, server)
	}
	var out struct {
		Answers []string `json:"answers"`
	}
	if err := g.hv.Guest(ctx, g.vmID, g.owner, g.cred, scriptGuestDNS, map[string]any{"server": server.String(), "name": name}, &out); err != nil {
		return nil, err
	}
	var as []netip.Addr
	for _, s := range out.Answers {
		if a, err := netip.ParseAddr(s); err == nil {
			as = append(as, a)
		}
	}
	if len(as) == 0 {
		return nil, errors.New("no answer")
	}
	return as, nil
}

func (g psDirectProbe) Neighbor(ctx context.Context, ip netip.Addr, mac string) error {
	m, err := NormalizeMAC(mac)
	if err != nil || !ip.IsValid() || ip.Zone() != "" {
		return fmt.Errorf("neighbour entry %v %q is not allowed", ip, mac)
	}
	var out struct {
		OK bool `json:"ok"`
	}
	return g.hv.Guest(ctx, g.vmID, g.owner, g.cred, scriptGuestNeighbor, map[string]any{"ip": ip.String(), "mac": macDash(m)}, &out)
}

func (g psDirectProbe) HVSock(ctx context.Context, service, token string) (bool, error) {
	if !guidRe.MatchString(service) || !tokenRe.MatchString(token) {
		return false, fmt.Errorf("hv_sock probe of %q is not allowed", service)
	}
	var out struct {
		Connected bool   `json:"connected"`
		Result    string `json:"result"`
	}
	if err := g.hv.Guest(ctx, g.vmID, g.owner, g.cred, scriptGuestHVSock, map[string]any{"service": service, "token": token}, &out); err != nil {
		return false, err
	}
	switch {
	case out.Connected && out.Result == "connected":
		return true, nil
	case !out.Connected && out.Result == "refused":
		return false, nil
	default:
		return false, fmt.Errorf("hv_sock did not return a connection or explicit refusal: %q", out.Result)
	}
}

func (g psDirectProbe) Egress(ctx context.Context) (netip.Addr, error) {
	var out struct {
		IP string `json:"ip"`
	}
	if err := g.hv.Guest(ctx, g.vmID, g.owner, g.cred, scriptGuestEgress, map[string]any{}, &out); err != nil {
		return netip.Addr{}, err
	}
	return netip.ParseAddr(out.IP)
}

// RawFrame is not possible in the product guest: it has no capture driver.
func (psDirectProbe) RawFrame(context.Context, uint16, string, []byte, int) error {
	return ErrUnsupported
}
