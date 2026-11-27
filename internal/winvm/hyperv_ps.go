package winvm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"slices"
	"strings"
	"time"
)

// This file is the PowerShell seam: one fixed script per method, the checks on
// everything that goes in, and the decoding of what comes out. It builds on
// every OS, so its tests run everywhere. hyperv_windows.go only supplies the
// process that runs a script, and the PDH read of the drop counter.

// hvArgEnv names the environment variable that carries a script's one
// argument: base64 of a JSON document. Base64 has no character PowerShell
// treats as code, and the script text itself never changes, so no host value
// can become part of a script.
const hvArgEnv = "CPD_HV_ARG"

const (
	// maxHVOutput caps the JSON document a script returns.
	maxHVOutput = 1 << 20
	// maxHVRaw caps what a script may print: the base64 form of maxHVOutput,
	// plus room for a line ending.
	maxHVRaw = (maxHVOutput+2)/3*4 + 64
	// maxHVArg caps the JSON argument.
	maxHVArg = 64 << 10
	// maxSamples caps the egress samples one reading asks about.
	maxSamples = 64
	// dropRetry is how long DropCounters waits before it asks CIM again.
	dropRetry = 2 * time.Second
)

// ErrNoDropCounter means neither CIM nor PDH had the drop counter of our
// adapter. The count is unknown, never 0.
var ErrNoDropCounter = errors.New("no drop counter answered for this adapter")

// psRunner runs one script with one JSON argument and returns the JSON document
// it printed, already base64-decoded and capped.
type psRunner func(ctx context.Context, script string, arg []byte) ([]byte, error)

// DropInstance is one instance of the Hyper-V drop-reason counters, as CIM or
// PDH names it.
type DropInstance struct {
	Name string `json:"name"`
	Out  uint64 `json:"out"`
	In   uint64 `json:"in"`
}

// psHost implements Host over a psRunner.
type psHost struct {
	run psRunner
	// pdh reads every instance of the drop counters through PDH's English
	// names. Nil where PDH is not available.
	pdh   func(ctx context.Context) ([]DropInstance, error)
	sleep func(ctx context.Context, d time.Duration) error
}

var _ Host = (*psHost)(nil)

// hvPrelude starts every script. It stops on the first error, reads the one
// argument, and defines Write-Doc, which prints the answer as base64 of UTF-8
// JSON so the console's code page cannot change a byte of it.
const hvPrelude = `$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
trap { [Console]::Error.WriteLine($_.Exception.Message); exit 1 }
$in = [Text.Encoding]::UTF8.GetString([Convert]::FromBase64String($env:CPD_HV_ARG)) | ConvertFrom-Json
function Write-Doc($o) {
  $j = ConvertTo-Json -InputObject $o -Compress -Depth 8
  [Console]::Out.Write([Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes($j)))
}
function Get-Owner([string]$s) {
  if ($s -cmatch '(?:^| )cashpilot-owner=([0-9a-f]{32})$') { return $Matches[1] }
  return ''
}
function Find-OurVM($id) { Get-VM | Where-Object { $_.Id.ToString() -eq $id } }
function Get-OwnedVM([string]$id, [string]$owner) {
  $vm = @(Find-OurVM $id)
  if (-not $owner -or $vm.Count -ne 1 -or (Get-Owner ([string]$vm[0].Notes)) -cne $owner) { throw 'The owned VM was not found.' }
  $vm[0]
}
function Get-RecordedVMAdapter([string]$id, [string]$owner, [string]$adapterId, [bool]$only) {
  $vm = Get-OwnedVM $id $owner
  $all = @(Get-VMNetworkAdapter -VM $vm)
  $a = @($all | Where-Object { (($_.Id -split '\\')[-1]) -eq $adapterId })
  if (-not $adapterId -or $a.Count -ne 1) { throw 'The recorded VM adapter was not found and was left unchanged.' }
  if ($only -and $all.Count -ne 1) { throw 'The VM has another adapter and was left unchanged.' }
  $a[0]
}
function Find-ManagementAdapter($mo) {
  $dev = ([string]$mo.DeviceId).Trim('{}')
  $guid = ([string]$mo.InterfaceGuid).Trim('{}')
  @(Get-NetAdapter -IncludeHidden | Where-Object {
    ($dev -and (([string]$_.DeviceId).Trim('{}') -eq $dev -or ([string]$_.InterfaceGuid).Trim('{}') -eq $dev)) -or
    ($guid -and ([string]$_.InterfaceGuid).Trim('{}') -eq $guid)
  })
}
`

// scriptReadLive reads everything Diff compares. Each part has its own try, so a
// part that fails is left out of the answer, and readLive turns a missing part
// into a value Diff flags. Enums are written with ToString, so the answer holds
// their English names, never numbers or translated text. Integration services
// are keyed by their ID.
// netTransPS lists the component IDs of every installed protocol (the
// NetTrans class). They are under Control\Network: the Control\Class key of
// the same GUID refuses Administrators and, read as SYSTEM, holds no
// components (live run 5).
const netTransPS = `function Get-NetTransComponents {
  @(Get-ChildItem -LiteralPath 'HKLM:\SYSTEM\CurrentControlSet\Control\Network\{4D36E975-E325-11CE-BFC1-08002BE10318}' -ErrorAction Stop |
    ForEach-Object { [string]$_.GetValue('ComponentId') } | Where-Object { $_ })
}
`

const scriptReadLive = portACLPrelude + firewallIdentityPS + netTransPS + `$o = [ordered]@{}
$vm = @(Find-OurVM $in.vm)
$o.vmExists = ($vm.Count -eq 1)
if ($vm.Count -ne 1) { Write-Doc $o; exit 0 }
$vm = $vm[0]
try { $o.vmState = $vm.State.ToString() } catch { }
try {
  $o.adapters = @(Get-VMNetworkAdapter -VM $vm | ForEach-Object {
    $sid = ''
    if ($_.SwitchId) { $sid = $_.SwitchId.ToString() }
    [ordered]@{ id = ($_.Id -split '\\')[-1]; switchId = $sid; mac = [string]$_.MacAddress; dynamicMac = [bool]$_.DynamicMacAddressEnabled; dhcpGuard = $_.DhcpGuard.ToString(); routerGuard = $_.RouterGuard.ToString(); macSpoofing = $_.MacAddressSpoofing.ToString() }
  })
} catch { }
try {
  $o.acls = @(Get-VMNetworkAdapterAcl -VM $vm | ForEach-Object {
    [ordered]@{ action = $_.Action.ToString(); direction = $_.Direction.ToString(); remote = [string]$_.RemoteAddress; local = [string]$_.LocalAddress }
  })
} catch { }
try {
  $o.portAcls = @(Get-VMNetworkAdapter -VM $vm | ForEach-Object { Get-PortACLs $_ } | ForEach-Object { Export-PortACL $_ })
} catch { }
try { $o.extAcls = @(Get-VMNetworkAdapterExtendedAcl -VM $vm).Count } catch { }
try {
  $p = Get-VMProcessor -VM $vm
  $o.procCount = [int]$p.Count
  $o.procMax = [int]$p.Maximum
  $o.procWeight = [int]$p.RelativeWeight
  $o.exposeVirt = [bool]$p.ExposeVirtualizationExtensions
} catch { }
try {
  $m = Get-VMMemory -VM $vm
  $o.memoryBytes = [uint64]$m.Startup
  $o.dynamicMemory = [bool]$m.DynamicMemoryEnabled
} catch { }
try { $o.checkpoints = $vm.CheckpointType.ToString() } catch { }
try { $o.autoStart = $vm.AutomaticStartAction.ToString() } catch { }
try {
  $is = @{}
  foreach ($s in @(Get-VMIntegrationService -VM $vm)) { $is[(($s.Id -split '\\')[-1]).ToUpper()] = [bool]$s.Enabled }
  $o.integration = $is
} catch { }
try { $o.dvds = @(Get-VMDvdDrive -VM $vm).Count } catch { }
try { $o.disks = @(Get-VMHardDiskDrive -VM $vm).Count } catch { }
$pt = @()
foreach ($c in @('Get-VMGpuPartitionAdapter', 'Get-VMAssignableDevice')) {
  if (Get-Command $c -ErrorAction SilentlyContinue) {
    try { if (@(& $c -VM $vm).Count -gt 0) { $pt += $c } } catch { $pt += 'unread:' + $c }
  }
}
try { foreach ($p in @(Get-VMComPort -VM $vm)) { if ($p.Path) { $pt += 'com:' + $p.Path } } } catch { $pt += 'unread:Get-VMComPort' }
$o.passthrough = @($pt)
$idx = 0
try {
  $sw = @(Get-VMSwitch | Where-Object { $_.Id.ToString() -eq $in.switchId })
  if ($sw.Count -eq 1) {
    $sw = $sw[0]
    $o.switchType = $sw.SwitchType.ToString()
    $o.switchPorts = @(Get-VMNetworkAdapter -All | Where-Object { (-not $_.IsManagementOs) -and ($_.SwitchId -eq $sw.Id) }).Count
    $mo = @(Get-VMNetworkAdapter -ManagementOS | Where-Object { $_.SwitchId -eq $sw.Id })
    if ($mo.Count -eq 1) {
      $na = @(Find-ManagementAdapter $mo[0])
      if ($na.Count -eq 1) {
        $idx = [int]$na[0].ifIndex
        $o.managementAdapterId = [string]$mo[0].Id
        $ipif = @(Get-NetIPInterface -InterfaceIndex $idx -AddressFamily IPv4)
        $o.managementDhcp = ($ipif.Count -ne 1 -or $ipif[0].Dhcp.ToString() -ne 'Disabled')
        $o.vnicIfIndex = $idx
        $o.vnicMac = [string]$na[0].MacAddress
      }
    }
  }
} catch { }
if ($idx -ne 0) {
  try {
    $na = @(Find-ManagementAdapter $mo[0])[0]
    $b = @(Get-NetAdapterBinding -Name $na.Name -IncludeHidden -AllBindings)
    $bm = @{}
    foreach ($x in $b) { $bm[([string]$x.ComponentID).ToLower()] = [bool]$x.Enabled }
    $o.bindings = $bm
    $nt = @(Get-NetTransComponents)
    if ($nt.Count -gt 0) { $o.protocolsOn = @($b | Where-Object { $_.Enabled -and ($nt -contains $_.ComponentID) } | ForEach-Object { ([string]$_.ComponentID).ToLower() }) }
  } catch { }
  try {
    $o.neighbors = @(Get-NetNeighbor -InterfaceIndex $idx -AddressFamily IPv4 -ErrorAction SilentlyContinue | Where-Object { $_.State.ToString() -eq 'Permanent' } | ForEach-Object { [ordered]@{ ip = [string]$_.IPAddress; mac = [string]$_.LinkLayerAddress; state = $_.State.ToString() } })
  } catch { }
}
try { $o.nats = @(Get-NetNat -ErrorAction SilentlyContinue | ForEach-Object { [ordered]@{ id = [string]$_.InstanceID; name = [string]$_.Name; prefix = [string]$_.InternalIPInterfaceAddressPrefix } }) } catch { }
try {
  $o.firewall = @(Get-NetFirewallRule -Group $in.group -ErrorAction SilentlyContinue | ForEach-Object {
    $r = $_
    $fi = 0
    $f = @(Get-NetFirewallInterfaceFilter -AssociatedNetFirewallRule $r)
    if ($f.Count -eq 1) {
      $al = @($f[0].InterfaceAlias)
      if ($al.Count -eq 1) {
        $na = @(Get-NetAdapter -IncludeHidden | Where-Object { $_.Name -eq $al[0] })
        if ($na.Count -eq 1) { $fi = [int]$na[0].ifIndex }
      }
    }
    [ordered]@{ direction = $r.Direction.ToString(); action = $r.Action.ToString(); enabled = ($r.Enabled.ToString() -eq 'True'); ifIndex = $fi; identity = (Get-FirewallIdentity $r) }
  })
} catch { }
try { $o.enhancedSession = [bool](Get-VMHost).EnableEnhancedSessionMode } catch { }
try {
  $g = 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Virtualization\GuestCommunicationServices'
  if (Test-Path -LiteralPath $g) { $o.gcs = @(Get-ChildItem -LiteralPath $g | ForEach-Object { $_.PSChildName.ToUpper() }) } else { $o.gcs = @() }
} catch { }
try {
  $cv = Get-ItemProperty -LiteralPath 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion'
  $o.hostBuild = [string]$cv.CurrentBuild + '.' + [string]$cv.UBR
} catch { }
Write-Doc $o
`

// scriptReadHostNet reads the interfaces, the IPv4 routes, where the host's own
// route lookup sends each sample, and the host's IPv4 addresses. An interface
// that only MSFT_NetIPInterface knows (a RAS PPP link) is marked hidden. The
// physical list is Get-NetAdapter -Physical, for the cross-check in
// readHostNet.
const scriptReadHostNet = hvPrelude + `$o = [ordered]@{}
$phys = @(Get-NetAdapter -Physical | ForEach-Object { [int]$_.ifIndex })
$o.physical = $phys
$physG = @{}
foreach ($a in @(Get-NetAdapter -Physical)) { $physG[([string]$a.InterfaceGuid).Trim('{}').ToLower()] = $true }
$mos = @{}
$sws = @{}
try {
  foreach ($m in @(Get-VMNetworkAdapter -ManagementOS)) {
    if ($m.SwitchId) {
      $na = @(Find-ManagementAdapter $m)
      if ($na.Count -eq 1) { $mos[[int]$na[0].ifIndex] = $m.SwitchId.ToString() }
    }
  }
  foreach ($s in @(Get-VMSwitch)) { $sws[$s.Id.ToString()] = $s }
} catch { }
$types = @{}
try {
  foreach ($n in [System.Net.NetworkInformation.NetworkInterface]::GetAllNetworkInterfaces()) {
    $p = $n.GetIPProperties().GetIPv4Properties()
    if ($p) { $types[[int]$p.Index] = [int]$n.NetworkInterfaceType }
  }
} catch { }
$seen = @{}
$ifs = @()
foreach ($a in @(Get-NetAdapter -IncludeHidden)) {
  $seen[[int]$a.ifIndex] = $true
  $sid = ''
  $st = ''
  $ext = $false
  if ($mos.ContainsKey([int]$a.ifIndex)) { $sid = $mos[[int]$a.ifIndex] }
  if ($sid) {
    if ($sws.ContainsKey($sid)) {
      $s = $sws[$sid]
      $st = $s.SwitchType.ToString()
      if ($st -eq 'External') {
        foreach ($gd in @($s.NetAdapterInterfaceGuid)) { if ($gd -and $physG.ContainsKey($gd.ToString().ToLower())) { $ext = $true } }
      }
    }
  }
  $ifs += [ordered]@{ ifIndex = [int]$a.ifIndex; ifType = [uint32]$a.InterfaceType; physicalMedium = [uint32]$a.NdisPhysicalMedium; hardwareInterface = [bool]$a.HardwareInterface; connectorPresent = [bool]$a.ConnectorPresent; hidden = [bool]$a.Hidden; componentId = [string]$a.ComponentID; description = [string]$a.InterfaceDescription; vmSwitchId = $sid; switchType = $st; externalOverPhysical = $ext }
}
foreach ($i in @(Get-NetIPInterface -AddressFamily IPv4)) {
  $x = [int]$i.ifIndex
  if (-not $seen.ContainsKey($x)) {
    $seen[$x] = $true
    $t = 0
    if ($types.ContainsKey($x)) { $t = $types[$x] }
    $ifs += [ordered]@{ ifIndex = $x; ifType = [uint32]$t; hidden = $true; description = [string]$i.InterfaceAlias }
  }
}
$o.interfaces = @($ifs)
$o.routes = @(Get-NetRoute -AddressFamily IPv4 -PolicyStore ActiveStore | ForEach-Object { [ordered]@{ prefix = [string]$_.DestinationPrefix; ifIndex = [int]$_.ifIndex; nextHop = [string]$_.NextHop; metric = [uint32]$_.RouteMetric } })
$eg = @()
foreach ($s in @($in.samples)) {
  try {
    $r = @(Find-NetRoute -RemoteIPAddress $s | Where-Object { $_.DestinationPrefix })
    if ($r.Count -ge 1) { $eg += [ordered]@{ dest = [string]$s; ifIndex = [int]$r[0].ifIndex } }
  } catch { }
}
$o.egress = @($eg)
$o.addrs = @(Get-NetIPAddress -AddressFamily IPv4 | ForEach-Object { [string]$_.IPAddress + '/' + [string]$_.PrefixLength })
Write-Doc $o
`

// scriptReadNetwork names the network the host is on: the MAC of the gateway of
// its best default route, and the profile ID Windows gives that network. The
// cost comes from WinRT's connection profile.
const scriptReadNetwork = hvPrelude + `$o = [ordered]@{}
$best = $null
$bm = [uint64]::MaxValue
foreach ($r in @(Get-NetRoute -AddressFamily IPv4 -DestinationPrefix '0.0.0.0/0' -PolicyStore ActiveStore -ErrorAction SilentlyContinue)) {
  if ([string]$r.NextHop -eq '0.0.0.0') { continue }
  $im = [uint64]::MaxValue / 2
  try { $im = [uint64]@(Get-NetIPInterface -AddressFamily IPv4 -InterfaceIndex $r.ifIndex)[0].InterfaceMetric } catch { }
  $m = [uint64]$r.RouteMetric + $im
  if ($m -lt $bm) { $bm = $m; $best = $r }
}
if ($best) {
  try {
    $n = @(Get-NetNeighbor -IPAddress $best.NextHop -InterfaceIndex $best.ifIndex -ErrorAction SilentlyContinue)
    if ($n.Count -eq 1) { $o.gatewayMac = [string]$n[0].LinkLayerAddress }
  } catch { }
  try { $o.profileId = [string](Get-NetConnectionProfile -InterfaceIndex $best.ifIndex).InstanceID } catch { }
}
try {
  $null = [Windows.Networking.Connectivity.NetworkInformation, Windows.Networking.Connectivity, ContentType = WindowsRuntime]
  $cp = [Windows.Networking.Connectivity.NetworkInformation]::GetInternetConnectionProfile()
  if ($cp) {
    $c = $cp.GetConnectionCost()
    $o.cost = $c.NetworkCostType.ToString()
    $o.roaming = [bool]$c.Roaming
    $o.overDataLimit = [bool]$c.OverDataLimit
  }
} catch { }
Write-Doc $o
`

// scriptDropCounters reads the drop-reason counters of every instance whose
// name ends in _<VM ID>--<adapter ID>, and the VM's name. Go checks the match
// again, prefix included. The middle of the name is the adapter's name in the
// host's language, so it is never matched.
const scriptDropCounters = hvPrelude + `$o = [ordered]@{}
$vm = @(Find-OurVM $in.vm)
if ($vm.Count -eq 1) { $o.vmName = [string]$vm[0].Name }
try {
  $suffix = '*_' + $in.vm + '--' + $in.adapter
  $o.instances = @(Get-CimInstance -ClassName Win32_PerfRawData_NvspNicDropReasonsStats_HyperVVirtualNetworkAdapterDropReasons | Where-Object { $_.Name -like $suffix } | ForEach-Object { [ordered]@{ name = [string]$_.Name; out = [uint64]$_.OutgoingFailedSecurityPolicy; in = [uint64]$_.IncomingFailedSecurityPolicy } })
} catch { }
Write-Doc $o
`

// scriptUsage reads what the host measures of the VM, with no resource
// metering: Enable-VMResourceMetering may add Meter ACLs.
const scriptUsage = hvPrelude + `$o = [ordered]@{}
$vm = @(Find-OurVM $in.vm)
if ($vm.Count -ne 1) { throw 'The VM was not found.' }
$vm = $vm[0]
$o.cpuPercent = [int]$vm.CPUUsage
$o.memoryBytes = [uint64]$vm.MemoryAssigned
$o.uptimeSeconds = [uint64][math]::Floor($vm.Uptime.TotalSeconds)
$d = [uint64]0
foreach ($h in @(Get-VMHardDiskDrive -VM $vm)) { if ($h.Path) { $d += [uint64](Get-VHD -Path $h.Path).FileSize } }
$o.diskBytes = $d
try {
  $a = @(Get-VMNetworkAdapter -VM $vm)
  if ($a.Count -eq 1) {
    $suffix = '*_' + $in.vm + '--' + ($a[0].Id -split '\\')[-1]
    $n = @(Get-CimInstance -ClassName Win32_PerfRawData_NvspNicStats_HyperVVirtualNetworkAdapter | Where-Object { $_.Name -like $suffix })
    if ($n.Count -eq 1) { $o.bytesSent = [uint64]$n[0].BytesSentPersec }
  }
} catch { }
Write-Doc $o
`

// scriptDisconnect disconnects only the adapter recorded at Setup.
const scriptDisconnect = hvPrelude + `$a = Get-RecordedVMAdapter $in.vm $in.owner $in.adapter $false
Disconnect-VMNetworkAdapter -VMNetworkAdapter $a
$vm = Get-OwnedVM $in.vm $in.owner
$all = @(Get-VMNetworkAdapter -VM $vm)
$left = @($all | Where-Object { $_.SwitchName }).Count
Write-Doc ([ordered]@{ adapters = $all.Count; connected = $left })
`

// scriptConnect connects the VM's one adapter to the one Internal switch with
// the recorded ID. It refuses a VM with more adapters, a missing or doubled
// switch, and a switch of any other type.
const scriptConnect = hvPrelude + `$a = Get-RecordedVMAdapter $in.vm $in.owner $in.adapter $true
$sw = @(Get-VMSwitch | Where-Object { $_.Id.ToString() -eq $in.switchId })
if ($sw.Count -ne 1 -or (Get-Owner ([string]$sw[0].Notes)) -cne $in.owner) { throw 'The owned switch was not found.' }
if ($sw[0].SwitchType.ToString() -ne 'Internal') { throw 'The switch is not an Internal switch.' }
Connect-VMNetworkAdapter -VMNetworkAdapter $a -VMSwitch $sw[0]
Write-Doc ([ordered]@{ switchId = $sw[0].Id.ToString() })
`

// scriptStart starts the VM unless it already runs, and reports its state.
const scriptStart = hvPrelude + `$vm = Get-OwnedVM $in.vm $in.owner
if ($vm.State.ToString() -ne 'Running') { Start-VM -VM $vm }
$vm = Get-OwnedVM $in.vm $in.owner
Write-Doc ([ordered]@{ state = $vm.State.ToString() })
`

// hvScripts is every script the seam runs, by method, for the parser test.
var hvScripts = map[string]string{
	"ReadLive":       scriptReadLive,
	"ReadHostNet":    scriptReadHostNet,
	"ReadNetwork":    scriptReadNetwork,
	"DropCounters":   scriptDropCounters,
	"Usage":          scriptUsage,
	"Disconnect":     scriptDisconnect,
	"Connect":        scriptConnect,
	"AddPortACLs":    scriptAddPortACLs,
	"RemovePortACLs": scriptRemovePortACLs,
	"Start":          scriptStart,
}

// checkVMID accepts only a GUID.
func checkVMID(what, id string) error {
	if !guidRe.MatchString(id) {
		return fmt.Errorf("%s %q is not a GUID", what, id)
	}
	return nil
}

// checkSamples accepts only unicast public-looking IPv4 addresses, at most
// maxSamples of them.
func checkSamples(samples []netip.Addr) ([]string, error) {
	if len(samples) > maxSamples {
		return nil, fmt.Errorf("%d egress samples, over the cap of %d", len(samples), maxSamples)
	}
	out := make([]string, 0, len(samples))
	for _, s := range samples {
		if !s.IsValid() || !s.Is4() || s.IsUnspecified() || s.IsMulticast() || s.IsLoopback() {
			return nil, fmt.Errorf("egress sample %v is not a unicast IPv4 address", s)
		}
		out = append(out, s.String())
	}
	return out, nil
}

// call runs one script with one argument and decodes its answer strictly.
func (h *psHost) call(ctx context.Context, script string, arg, out any) error {
	a, err := json.Marshal(arg)
	if err != nil {
		return err
	}
	if len(a) > maxHVArg {
		return fmt.Errorf("script argument is %d bytes, over the cap of %d", len(a), maxHVArg)
	}
	doc, err := h.run(ctx, script, a)
	if err != nil {
		return err
	}
	return decodeDoc(doc, out)
}

// decodeDoc decodes one JSON document of at most maxHVOutput bytes, with no
// unknown field and nothing after it.
func decodeDoc(doc []byte, out any) error {
	if len(doc) > maxHVOutput {
		return fmt.Errorf("the answer is %d bytes, over the cap of %d", len(doc), maxHVOutput)
	}
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("could not read the answer: %w", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("the answer has data after the document")
	}
	return nil
}

// decodeOutput turns what a script printed into its JSON document: base64 of
// UTF-8, capped before and after decoding.
func decodeOutput(raw []byte) ([]byte, error) {
	if len(raw) > maxHVRaw {
		return nil, fmt.Errorf("the script printed %d bytes, over the cap of %d", len(raw), maxHVRaw)
	}
	doc, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(raw)))
	if err != nil {
		return nil, fmt.Errorf("the script's answer is not base64: %w", err)
	}
	if len(doc) > maxHVOutput {
		return nil, fmt.Errorf("the answer is %d bytes, over the cap of %d", len(doc), maxHVOutput)
	}
	return doc, nil
}

// cappedBuffer keeps at most max bytes and notes that more came. It never
// fails a write, so a chatty child is never blocked on a full pipe.
type cappedBuffer struct {
	max      int
	buf      bytes.Buffer
	overflow bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	room := c.max - c.buf.Len()
	if len(p) > room {
		c.overflow = true
		if room > 0 {
			c.buf.Write(p[:room])
		}
		return len(p), nil
	}
	c.buf.Write(p)
	return len(p), nil
}

// hvArg is the environment entry that carries one JSON argument.
func hvArg(arg []byte) string { return hvArgEnv + "=" + base64.StdEncoding.EncodeToString(arg) }

// ReadLive reads the live state of our VM, switch, vNIC, NAT, firewall group
// and host settings.
func (h *psHost) ReadLive(ctx context.Context, n Names, vmID, switchID string) (LiveState, error) {
	if err := n.Validate(); err != nil {
		return LiveState{}, err
	}
	if err := checkVMID("VM ID", vmID); err != nil {
		return LiveState{}, err
	}
	if err := checkVMID("switch ID", switchID); err != nil {
		return LiveState{}, err
	}
	var w liveWire
	arg := struct {
		VM     string `json:"vm"`
		Switch string `json:"switchId"`
		Group  string `json:"group"`
	}{vmID, switchID, n.FirewallGroup}
	if err := h.call(ctx, scriptReadLive, arg, &w); err != nil {
		return LiveState{}, err
	}
	return w.live()
}

// liveWire is scriptReadLive's answer. A part the script could not read is
// absent, and live() turns it into a value Diff flags.
type liveWire struct {
	VMExists            *bool           `json:"vmExists"`
	VMState             *string         `json:"vmState"`
	Adapters            []adapterWire   `json:"adapters"`
	PortACLs            []PortACL       `json:"portAcls"`
	ACLs                []aclWire       `json:"acls"`
	ExtACLs             *int            `json:"extAcls"`
	ProcCount           *int            `json:"procCount"`
	ProcMax             *int            `json:"procMax"`
	ProcWeight          *int            `json:"procWeight"`
	ExposeVirt          *bool           `json:"exposeVirt"`
	MemoryBytes         *uint64         `json:"memoryBytes"`
	DynamicMemory       *bool           `json:"dynamicMemory"`
	Checkpoints         *string         `json:"checkpoints"`
	AutoStart           *string         `json:"autoStart"`
	Integration         map[string]bool `json:"integration"`
	DVDs                *int            `json:"dvds"`
	Disks               *int            `json:"disks"`
	Passthrough         []string        `json:"passthrough"`
	SwitchType          *string         `json:"switchType"`
	SwitchPorts         *int            `json:"switchPorts"`
	ManagementDHCP      *bool           `json:"managementDhcp"`
	ManagementAdapterID string          `json:"managementAdapterId"`
	VNICIfIndex         *int            `json:"vnicIfIndex"`
	VNICMAC             *string         `json:"vnicMac"`
	Bindings            map[string]bool `json:"bindings"`
	ProtocolsOn         []string        `json:"protocolsOn"`
	Neighbors           []neighborWire  `json:"neighbors"`
	NATs                []natWire       `json:"nats"`
	Firewall            []LiveRule      `json:"firewall"`
	Enhanced            *bool           `json:"enhancedSession"`
	GCS                 []string        `json:"gcs"`
	HostBuild           *string         `json:"hostBuild"`
}

type adapterWire struct {
	ID          string `json:"id"`
	SwitchID    string `json:"switchId"`
	MAC         string `json:"mac"`
	DynamicMAC  bool   `json:"dynamicMac"`
	DhcpGuard   string `json:"dhcpGuard"`
	RouterGuard string `json:"routerGuard"`
	MacSpoofing string `json:"macSpoofing"`
}

type aclWire struct {
	Action    string `json:"action"`
	Direction string `json:"direction"`
	Remote    string `json:"remote"`
	Local     string `json:"local"`
}

type neighborWire struct {
	IP    string `json:"ip"`
	MAC   string `json:"mac"`
	State string `json:"state"`
}

type natWire struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Prefix string `json:"prefix"`
}

// unread marks a part the reader could not fill.
const unread = "unread"

// live turns the answer into a LiveState. Every part that is missing gets a
// value that Diff reports as changed, so a broken reader fails closed: the
// fields whose planned value is false or zero become true or -1 here.
func (w liveWire) live() (LiveState, error) {
	if w.VMExists == nil {
		return LiveState{}, errors.New("the answer does not say whether the VM exists")
	}
	if !*w.VMExists {
		return LiveState{}, nil
	}
	l := LiveState{VMExists: true}
	l.VMState = strOr(w.VMState, unread)

	// Adapters, and the port settings Diff compares on the one adapter. A guard
	// counts as on only when every adapter has it; MAC spoofing counts as on
	// when any adapter has it, or when the adapters were not read.
	l.MacSpoofing = w.Adapters == nil
	if w.Adapters != nil {
		l.Adapters = []LiveAdapter{}
		l.DhcpGuard, l.RouterGuard = len(w.Adapters) > 0, len(w.Adapters) > 0
	}
	for _, a := range w.Adapters {
		l.Adapters = append(l.Adapters, LiveAdapter{ID: a.ID, SwitchID: a.SwitchID, MAC: a.MAC})
		l.DhcpGuard = l.DhcpGuard && strings.EqualFold(a.DhcpGuard, "On")
		l.RouterGuard = l.RouterGuard && strings.EqualFold(a.RouterGuard, "On")
		if !strings.EqualFold(a.MacSpoofing, "Off") {
			l.MacSpoofing = true
		}
	}
	if len(w.Adapters) > 0 && !w.Adapters[0].DynamicMAC {
		l.StaticMAC = w.Adapters[0].MAC
	}

	// ACL rows as Hyper-V lists them; Diff folds an Inbound and Outbound pair
	// into the Both rule it is. A row that is not a remote IP prefix (a MAC
	// row, or one with a local address) gets no prefix, so it never matches a
	// planned row.
	if w.ACLs == nil {
		l.ACLs = []ACL{{Action: unread, Direction: "Both"}}
	}
	for _, a := range w.ACLs {
		l.ACLs = append(l.ACLs, ACL{Action: a.Action, Direction: a.Direction, Remote: aclRemote(a)})
	}

	l.PortACLs = w.PortACLs
	l.ExtACLs = intOr(w.ExtACLs, -1)
	l.ProcCount = intOr(w.ProcCount, -1)
	l.ProcMax = intOr(w.ProcMax, -1)
	l.ProcWeight = intOr(w.ProcWeight, -1)
	l.ExposeVirt = boolOr(w.ExposeVirt, true)
	if w.MemoryBytes != nil {
		l.MemoryBytes = *w.MemoryBytes
	}
	l.DynamicMemory = boolOr(w.DynamicMemory, true)
	l.Checkpoints = strOr(w.Checkpoints, unread)
	l.AutoStart = strOr(w.AutoStart, unread)
	if w.Integration != nil {
		l.Integration = map[string]bool{}
		for id, on := range w.Integration {
			l.Integration[strings.ToUpper(strings.Trim(id, "{}"))] = on
		}
	}
	l.DVDs = intOr(w.DVDs, -1)
	l.Disks = intOr(w.Disks, -1)
	l.Passthrough = w.Passthrough
	if l.Passthrough == nil {
		l.Passthrough = []string{unread}
	}
	l.SwitchType = strOr(w.SwitchType, unread)
	l.SwitchPorts = intOr(w.SwitchPorts, -1)
	l.ManagementDHCP = boolOr(w.ManagementDHCP, true)
	l.ManagementAdapterID = w.ManagementAdapterID
	l.VNICIfIndex = intOr(w.VNICIfIndex, 0)
	l.VNICMAC = strOr(w.VNICMAC, "")
	if w.Bindings != nil {
		l.Bindings = map[string]bool{}
		for c, on := range w.Bindings {
			l.Bindings[strings.ToLower(c)] = on
		}
	}
	for _, c := range w.ProtocolsOn {
		l.ProtocolsOn = append(l.ProtocolsOn, strings.ToLower(c))
	}
	l.HostNeighbor = pickNeighbor(w.Neighbors)
	for _, n := range w.NATs {
		p, _ := parsePrefixOrAddr(n.Prefix)
		l.NATs = append(l.NATs, LiveNAT{ID: n.ID, Name: n.Name, Prefix: p})
	}
	if w.NATs != nil && l.NATs == nil {
		l.NATs = []LiveNAT{}
	}
	l.Firewall = w.Firewall
	l.Enhanced = boolOr(w.Enhanced, true)
	l.GCS = w.GCS
	l.HostBuild = strOr(w.HostBuild, "")
	return l, nil
}

// aclRemote is the remote prefix of one ACL row. Hyper-V writes a /32 as a bare
// address.
func aclRemote(a aclWire) netip.Prefix {
	if strings.TrimSpace(a.Local) != "" {
		return netip.Prefix{}
	}
	p, err := parsePrefixOrAddr(a.Remote)
	if err != nil {
		return netip.Prefix{}
	}
	return p
}

// parsePrefixOrAddr reads "10.0.0.0/8", "::/0" or a bare address as its
// full-length prefix.
func parsePrefixOrAddr(s string) (netip.Prefix, error) {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, err
		}
		return p, nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// pickNeighbor is the host's static entry for the guest: the one permanent
// unicast entry on our vNIC. Windows also lists broadcast and multicast entries
// as permanent; their MACs are multicast, so NormalizeMAC drops them. More than
// one unicast entry is reported as such, and Diff flags it.
func pickNeighbor(ns []neighborWire) *Neighbor {
	var keep []Neighbor
	for _, n := range ns {
		ip, err := netip.ParseAddr(strings.TrimSpace(n.IP))
		if err != nil {
			continue
		}
		if _, err := NormalizeMAC(n.MAC); err != nil {
			continue
		}
		keep = append(keep, Neighbor{IP: ip, MAC: n.MAC, State: n.State})
	}
	switch len(keep) {
	case 0:
		return nil
	case 1:
		return &keep[0]
	}
	slices.SortFunc(keep, func(a, b Neighbor) int { return a.IP.Compare(b.IP) })
	n := keep[0]
	n.State = fmt.Sprintf("%s, and %d more", n.State, len(keep)-1)
	return &n
}

func strOr(p *string, def string) string {
	if p == nil {
		return def
	}
	return *p
}

func intOr(p *int, def int) int {
	if p == nil {
		return def
	}
	return *p
}

func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// ReadHostNet reads the host's interfaces, routes, egress for each sample and
// IPv4 addresses.
func (h *psHost) ReadHostNet(ctx context.Context, samples []netip.Addr) (HostNet, error) {
	ss, err := checkSamples(samples)
	if err != nil {
		return HostNet{}, err
	}
	var w hostNetWire
	arg := struct {
		Samples []string `json:"samples"`
	}{ss}
	if err := h.call(ctx, scriptReadHostNet, arg, &w); err != nil {
		return HostNet{}, err
	}
	return w.hostNet()
}

type hostNetWire struct {
	Physical   []int       `json:"physical"`
	Interfaces []Interface `json:"interfaces"`
	Routes     []routeWire `json:"routes"`
	Egress     []egrWire   `json:"egress"`
	Addrs      []string    `json:"addrs"`
}

type routeWire struct {
	Prefix  string `json:"prefix"`
	IfIndex int    `json:"ifIndex"`
	NextHop string `json:"nextHop"`
	Metric  uint32 `json:"metric"`
}

type egrWire struct {
	Dest    string `json:"dest"`
	IfIndex int    `json:"ifIndex"`
}

// hostNet turns the answer into a HostNet. An adapter counts as physical only
// when Get-NetAdapter -Physical lists it too: one it leaves out loses its
// hardware flag here, so Classify calls it a tunnel. Classify already calls a
// listed adapter without both flags a tunnel, so the two never disagree in the
// open direction.
func (w hostNetWire) hostNet() (HostNet, error) {
	h := HostNet{Interfaces: []Interface{}, Routes: []Route{}, Egress: []Egress{}, Addrs: []netip.Prefix{}}
	for _, i := range w.Interfaces {
		if !slices.Contains(w.Physical, i.IfIndex) {
			i.HardwareInterface = false
		}
		h.Interfaces = append(h.Interfaces, i)
	}
	for _, r := range w.Routes {
		p, err := netip.ParsePrefix(strings.TrimSpace(r.Prefix))
		if err != nil {
			return HostNet{}, fmt.Errorf("route %q: %w", r.Prefix, err)
		}
		var nh netip.Addr
		if s := strings.TrimSpace(r.NextHop); s != "" {
			if nh, err = netip.ParseAddr(s); err != nil {
				return HostNet{}, fmt.Errorf("next hop %q: %w", r.NextHop, err)
			}
		}
		h.Routes = append(h.Routes, Route{Prefix: p, IfIndex: r.IfIndex, NextHop: nh, Metric: r.Metric})
	}
	for _, e := range w.Egress {
		a, err := netip.ParseAddr(strings.TrimSpace(e.Dest))
		if err != nil {
			return HostNet{}, fmt.Errorf("egress %q: %w", e.Dest, err)
		}
		h.Egress = append(h.Egress, Egress{Dest: a, IfIndex: e.IfIndex})
	}
	for _, s := range w.Addrs {
		p, err := netip.ParsePrefix(strings.TrimSpace(s))
		if err != nil {
			return HostNet{}, fmt.Errorf("address %q: %w", s, err)
		}
		h.Addrs = append(h.Addrs, p)
	}
	return h, nil
}

// ReadNetwork names the network the host is on now.
func (h *psHost) ReadNetwork(ctx context.Context) (CurrentNetwork, error) {
	var w networkWire
	if err := h.call(ctx, scriptReadNetwork, struct{}{}, &w); err != nil {
		return CurrentNetwork{}, err
	}
	return w.network(), nil
}

type networkWire struct {
	GatewayMAC    string `json:"gatewayMac"`
	ProfileID     string `json:"profileId"`
	Cost          string `json:"cost"`
	Roaming       bool   `json:"roaming"`
	OverDataLimit bool   `json:"overDataLimit"`
}

// network maps the WinRT cost. Fixed and Variable plans, roaming and an
// exceeded data limit are metered. A cost that was not read leaves the network
// unidentified, so NetworkOK says it is unknown and the VM stays off.
func (w networkWire) network() CurrentNetwork {
	n := CurrentNetwork{ID: NetworkID{GatewayMAC: w.GatewayMAC, ProfileID: w.ProfileID}}
	switch {
	case w.Roaming || w.OverDataLimit || strings.EqualFold(w.Cost, "Fixed") || strings.EqualFold(w.Cost, "Variable"):
		n.Metered = true
	case strings.EqualFold(w.Cost, "Unrestricted"):
	default:
		n.ID.ProfileID = ""
	}
	return n
}

// DropCounters reads the drop counter of one adapter: CIM first, once more
// after dropRetry when no instance matched, then PDH with the English counter
// names. It says which source answered.
func (h *psHost) DropCounters(ctx context.Context, vmID, adapterID string) (DropSample, error) {
	if err := checkVMID("VM ID", vmID); err != nil {
		return DropSample{}, err
	}
	if err := checkVMID("adapter ID", adapterID); err != nil {
		return DropSample{}, err
	}
	arg := struct {
		VM      string `json:"vm"`
		Adapter string `json:"adapter"`
	}{vmID, adapterID}
	vmName := ""
	for try := 0; try < 2; try++ {
		if try > 0 {
			if err := h.sleep(ctx, dropRetry); err != nil {
				return DropSample{}, err
			}
		}
		var w struct {
			VMName    string         `json:"vmName"`
			Instances []DropInstance `json:"instances"`
		}
		if err := h.call(ctx, scriptDropCounters, arg, &w); err != nil {
			continue
		}
		if w.VMName != "" {
			vmName = w.VMName
		}
		if s, ok := pickDropInstance(w.Instances, vmName, vmID, adapterID); ok {
			s.Source = "wmi"
			return s, nil
		}
	}
	if h.pdh != nil && vmName != "" {
		if all, err := h.pdh(ctx); err == nil {
			if s, ok := pickDropInstance(all, vmName, vmID, adapterID); ok {
				s.Source = "pdh"
				return s, nil
			}
		}
	}
	return DropSample{}, ErrNoDropCounter
}

// pickDropInstance finds the one instance named <VM name>_<adapter name>_<VM
// ID>--<adapter ID>, case-insensitively. No match, or more than one, is no
// answer.
func pickDropInstance(all []DropInstance, vmName, vmID, adapterID string) (DropSample, bool) {
	if vmName == "" {
		return DropSample{}, false
	}
	suffix := strings.ToLower("_" + vmID + "--" + adapterID)
	prefix := strings.ToLower(vmName + "_")
	var found []DropInstance
	for _, i := range all {
		n := strings.ToLower(i.Name)
		if strings.HasSuffix(n, suffix) && strings.HasPrefix(n, prefix) && len(n) > len(prefix)+len(suffix) {
			found = append(found, i)
		}
	}
	if len(found) != 1 {
		return DropSample{}, false
	}
	return DropSample{Out: found[0].Out, In: found[0].In}, true
}

// Usage reads what the host measures of the VM.
func (h *psHost) Usage(ctx context.Context, vmID string) (Usage, error) {
	if err := checkVMID("VM ID", vmID); err != nil {
		return Usage{}, err
	}
	var u Usage
	arg := struct {
		VM string `json:"vm"`
	}{vmID}
	if err := h.call(ctx, scriptUsage, arg, &u); err != nil {
		return Usage{}, err
	}
	return u, nil
}

// Disconnect takes every adapter of the VM off its switch.
func (h *psHost) Disconnect(ctx context.Context, vmID, owner, adapterID string) error {
	if err := checkVMID("VM ID", vmID); err != nil {
		return err
	}
	var w struct {
		Adapters  int `json:"adapters"`
		Connected int `json:"connected"`
	}
	if err := checkVMID("VM adapter ID", adapterID); err != nil {
		return err
	}
	if err := checkOwner(owner); err != nil {
		return err
	}
	arg := map[string]string{"vm": vmID, "owner": owner, "adapter": adapterID}
	if err := h.call(ctx, scriptDisconnect, arg, &w); err != nil {
		return err
	}
	if w.Connected != 0 {
		return fmt.Errorf("%d of %d adapters are still connected", w.Connected, w.Adapters)
	}
	return nil
}

// Connect puts the VM's one adapter on the recorded Internal switch.
func (h *psHost) Connect(ctx context.Context, vmID, switchID, owner, adapterID string) error {
	if err := checkVMID("VM ID", vmID); err != nil {
		return err
	}
	if err := checkVMID("switch ID", switchID); err != nil {
		return err
	}
	if err := checkVMID("VM adapter ID", adapterID); err != nil {
		return err
	}
	if err := checkOwner(owner); err != nil {
		return err
	}
	var w struct {
		SwitchID string `json:"switchId"`
	}
	arg := map[string]string{"vm": vmID, "switchId": switchID, "owner": owner, "adapter": adapterID}
	if err := h.call(ctx, scriptConnect, arg, &w); err != nil {
		return err
	}
	if !strings.EqualFold(w.SwitchID, switchID) {
		return fmt.Errorf("the connect reported switch ID %q, want %q", w.SwitchID, switchID)
	}
	return nil
}

// Start starts the VM and checks that it runs.
func (h *psHost) Start(ctx context.Context, vmID, owner string) error {
	if err := checkVMID("VM ID", vmID); err != nil {
		return err
	}
	var w struct {
		State string `json:"state"`
	}
	if err := checkOwner(owner); err != nil {
		return err
	}
	arg := map[string]string{"vm": vmID, "owner": owner}
	if err := h.call(ctx, scriptStart, arg, &w); err != nil {
		return err
	}
	if w.State != "Running" {
		return fmt.Errorf("the VM did not start: it is %q", w.State)
	}
	return nil
}

// maxDenyRows caps how many rows one write script touches. AddDeny and
// RemoveDeny send a longer list in batches of this size, so a split tunnel
// with many routes still gets its Denies.
const maxDenyRows = 256

// maxDenyTotal caps how many rows one AddDeny or RemoveDeny call may touch.
const maxDenyTotal = 16 * maxDenyRows

// checkDenies accepts only masked IPv4 prefixes, at most maxDenyTotal, with no
// repeat, and returns each one as Hyper-V writes it: a /32 as a bare address.
// The guard only manages its own IPv4 rows; IPv6 is one static row, ::/0,
// which setup places and nothing removes.
func checkDenies(ps []netip.Prefix) ([]string, error) {
	if len(ps) == 0 || len(ps) > maxDenyTotal {
		return nil, fmt.Errorf("%d prefixes; one call takes 1 to %d", len(ps), maxDenyTotal)
	}
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		if p == allIPv6 {
			out = append(out, p.String())
			continue
		}
		if !p.IsValid() || !p.Addr().Is4() || p != p.Masked() {
			return nil, fmt.Errorf("prefix %v is not a masked IPv4 prefix", p)
		}
		s := aclArg(p)
		if slices.Contains(out, s) {
			return nil, fmt.Errorf("prefix %v is listed twice", p)
		}
		out = append(out, s)
	}
	return out, nil
}

// allIPv6 is the static Deny over all of IPv6.
var allIPv6 = netip.MustParsePrefix("::/0")

// aclArg is a prefix as Get-VMNetworkAdapterAcl lists it: a /32 as a bare
// address, anything else in CIDR form.
func aclArg(p netip.Prefix) string {
	if p.Bits() == 32 {
		return p.Addr().String()
	}
	return p.String()
}

// sleepCtx waits d, or until ctx ends.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
