package winvm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
)

// CashPilot changes only objects it created. Shared names, MACs, paths,
// adapters, switches and rule shapes do not establish ownership. Preserve
// changes the administrator made after Setup. A copied secret owner token
// establishes ownership. Byte-identical replacement between steps is out of scope.
//
// The write side of the PowerShell seam (the Builder): setup, Repair and
// Remove VM. Like the read side, each method runs one fixed script with one
// base64 JSON argument, and every value is checked in Go before it goes in.
// Every script is idempotent. A script that finds an object with our name
// that does not carry the owner token refuses, so nothing here touches an
// object this install did not make.

// ownerTag is how the owner token is written into a VM's or a switch's Notes,
// a firewall rule's description and the guard task's description. The scripts
// match the whole tag, never a part of it.
const ownerTag = "cashpilot-owner="

// guestDiskBytes is the guest's dynamic VHDX size.
const guestDiskBytes = 128 * GiB

// hgsGuardian is the guardian a local key protector uses.
var identityDigestRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// hvAdmins is the Hyper-V Administrators group, by SID: its name is
// translated.
const hvAdmins = "S-1-5-32-578"

// buildPrelude adds the helpers the build scripts share to hvPrelude.
const buildPrelude = hvPrelude + `function Get-OwnedSwitch([string]$id, [string]$owner) {
  $sw = @(Get-VMSwitch | Where-Object { $_.Id.ToString() -eq $id })
  if (-not $owner -or $sw.Count -ne 1 -or (Get-Owner ([string]$sw[0].Notes)) -cne $owner -or $sw[0].SwitchType.ToString() -ne 'Internal') {
    throw 'The owned Internal switch was not found.'
  }
  $sw[0]
}
function Get-ManagementPort($sw) {
  Get-VMNetworkAdapter -ManagementOS | Where-Object { $_.SwitchId -eq $sw.Id }
}
function Get-RecordedManagementPort($sw, [string]$managementId) {
  $mo = @(Get-ManagementPort $sw)
  if (-not $managementId -or $mo.Count -ne 1 -or [string]$mo[0].Id -ne $managementId) {
    throw 'The recorded management adapter is missing or the switch has another management port. The ports were left in place.'
  }
  $mo[0]
}
function Get-EffectiveManagementMAC($port, $adapter) {
  $assigned = (([string]$port.MacAddress) -replace '-', '').ToUpperInvariant()
  $effective = (([string]$adapter.MacAddress) -replace '-', '').ToUpperInvariant()
  if ($effective -cmatch '^[0-9A-F]{12}$' -and $assigned -ceq $effective) { return $effective }
  return ''
}
function Get-HostVNIC([string]$id, [string]$owner, [string]$managementId, [int]$index = 0, [string]$mac = '') {
  $sw = Get-OwnedSwitch $id $owner
  $mo = Get-RecordedManagementPort $sw $managementId
  $na = @(Find-ManagementAdapter $mo)
  if ($na.Count -ne 1 -or ($index -and $na[0].ifIndex -ne $index)) { throw 'The host vNIC identity could not be verified.' }
  if ($mac -and (Get-EffectiveManagementMAC $mo $na[0]) -cne $mac) {
    throw 'The recorded management adapter MAC changed and was left in place.'
  }
  $na[0]
}
function Get-HostBindings($na) {
  $name = [System.Management.Automation.WildcardPattern]::Escape($na.Name)
  Get-NetAdapterBinding -Name $name -IncludeHidden -AllBindings | Where-Object { $_.Name -eq $na.Name }
}
function Export-HostBindings($na) {
  $out = @{}
  foreach ($b in @(Get-HostBindings $na)) {
    $id = ([string]$b.ComponentID).ToLowerInvariant()
    if ($out.ContainsKey($id)) { throw 'A binding on the recorded management adapter is ambiguous.' }
    $out[$id] = [bool]$b.Enabled
  }
  if ($out.Count -eq 0) { throw 'Could not read the management adapter bindings.' }
  $out
}
function Test-HostBindings($na, $expected) {
  if (-not $expected) { return $false }
  $live = Export-HostBindings $na
  $settings = @($expected.PSObject.Properties)
  if ($live.Count -ne $settings.Count) { return $false }
  foreach ($s in $settings) {
    if (-not $live.ContainsKey($s.Name) -or $live[$s.Name] -ne [bool]$s.Value) { return $false }
  }
  return $true
}
function Get-HostBinding([string]$component) {
  $na = Get-HostVNIC $in.switchId $in.owner $in.managementId ([int]$in.ifIndex)
  $b = @(Get-HostBindings $na | Where-Object { $_.ComponentID -eq $component })
  if ($b.Count -gt 1) { throw 'The host vNIC binding could not be identified.' }
  $b
}
function Get-GroupSids([string]$sid) {
  $g = Get-LocalGroup -SID $sid
  $adsi = [ADSI]('WinNT://./' + $g.Name + ',group')
  @($adsi.Invoke('Members') | ForEach-Object {
    $b = [byte[]]$_.GetType().InvokeMember('objectSid', 'GetProperty', $null, $_, $null)
    (New-Object Security.Principal.SecurityIdentifier($b, 0)).Value
  })
}
function Test-GuardianUser($v) {
  if ($v.Generation -ne 2) { return $false }
  try {
    $s = Get-VMSecurity -VM $v
    if ($s.TpmEnabled -or $s.EncryptStateAndVmMigrationTraffic -or $s.Shielded) { return $true }
    # A VM with no key protector holds the 4-byte empty one.
    return (@(Get-VMKeyProtector -VM $v).Count -gt 4)
  } catch { return $true }
}
function ConvertTo-GuardianCertificateBase64($certificate, [string]$role) {
  if ($certificate -is [Security.Cryptography.X509Certificates.X509Certificate]) {
    return [Convert]::ToBase64String($certificate.GetRawCertData())
  }
  if ($certificate -is [byte[]]) { return [Convert]::ToBase64String($certificate) }
  throw ('The guardian ' + $role + ' certificate has unsupported type ' + $certificate.GetType().FullName + '.')
}
function Get-GuardianIdentity($g) {
  if ($null -eq $g.SigningCertificate -or $null -eq $g.EncryptionCertificate) { return '' }
  $signing = ConvertTo-GuardianCertificateBase64 $g.SigningCertificate 'signing'
  $encryption = ConvertTo-GuardianCertificateBase64 $g.EncryptionCertificate 'encryption'
  if (-not $signing -or -not $encryption) { return '' }
  $pair = $signing + ':' + $encryption
  $sha = [Security.Cryptography.SHA256]::Create()
  try { return [BitConverter]::ToString($sha.ComputeHash([Text.Encoding]::ASCII.GetBytes($pair))).Replace('-', '').ToLowerInvariant() }
  finally { $sha.Dispose() }
}
`

// scriptInventory lists every object with one of our names, and the VM and the
// switch with the recorded IDs whatever their names are now, the owner token
// each carries, and the host facts setup and Remove VM check.
const scriptInventory = buildPrelude + `$o = [ordered]@{}
$o.vms = @(Get-VM | Where-Object { ($_.Name -eq $in.vm) -or ($in.vmId -and ($_.Id.ToString() -eq $in.vmId)) -or
  (-not $in.vmId -and $in.owner -and (Get-Owner ([string]$_.Notes)) -ceq $in.owner) } | ForEach-Object { [ordered]@{ id = $_.Id.ToString(); owner = (Get-Owner ([string]$_.Notes)) } })
$o.guardianVms = @(Get-VM | Where-Object { Test-GuardianUser $_ } | ForEach-Object { $_.Id.ToString() })
$o.switches = @(Get-VMSwitch | Where-Object { ($_.Name -eq $in.switch) -or ($in.switchId -and ($_.Id.ToString() -eq $in.switchId)) -or
  (-not $in.switchId -and $in.owner -and (Get-Owner ([string]$_.Notes)) -ceq $in.owner) } | ForEach-Object {
  $sw = $_
  $ports = @(Get-VMNetworkAdapter -All | Where-Object { (-not $_.IsManagementOs) -and ($_.SwitchId -eq $sw.Id) } | ForEach-Object { $_.VMId.ToString() } | Select-Object -Unique)
  [ordered]@{ id = $sw.Id.ToString(); owner = (Get-Owner ([string]$sw.Notes)); ports = @($ports) }
})
$o.nats = @(Get-NetNat -ErrorAction Stop | ForEach-Object { [ordered]@{ id = [string]$_.InstanceID; name = [string]$_.Name; prefix = [string]$_.InternalIPInterfaceAddressPrefix } })
$o.firewall = @(Get-NetFirewallRule -Group $in.group -ErrorAction SilentlyContinue | ForEach-Object { [ordered]@{ id = [string]$_.Name; owner = (Get-Owner ([string]$_.Description)) } })
$t = @(Get-ScheduledTask -TaskPath $in.taskPath -TaskName $in.taskName -ErrorAction SilentlyContinue)
$o.task = $null
if ($t.Count -eq 1) { $o.task = [ordered]@{ id = [string]$t[0].TaskPath + [string]$t[0].TaskName; owner = (Get-Owner ([string]$t[0].Description)) } }
$guardian = @(Get-HgsGuardian -Name 'UntrustedGuardian' -ErrorAction SilentlyContinue)
$o.guardian = $guardian.Count -gt 0
if ($guardian.Count -eq 1) { $o.guardianId = Get-GuardianIdentity $guardian[0] }
$o.hypervAdmin = $false
if ($in.sid) { $o.hypervAdmin = ((Get-GroupSids 'S-1-5-32-578') -contains $in.sid) }
$o.enhancedSession = [bool](Get-VMHost).EnableEnhancedSessionMode
$g = 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Virtualization\GuestCommunicationServices'
if (Test-Path -LiteralPath $g) { $o.gcs = @(Get-ChildItem -LiteralPath $g | ForEach-Object { $_.PSChildName.ToUpper() }) } else { $o.gcs = @() }
$cv = Get-ItemProperty -LiteralPath 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion'
$o.hostBuild = [string]$cv.CurrentBuild + '.' + [string]$cv.UBR
$o.installedRam = [uint64](Get-CimInstance -ClassName Win32_ComputerSystem).TotalPhysicalMemory
$o.freeRam = [uint64](Get-CimInstance -ClassName Win32_OperatingSystem).FreePhysicalMemory * 1024
Write-Doc $o
`

// scriptEnsureSwitch checks the switch with the recorded ID or creates an
// Internal switch with the owner tag. It sets the gateway on the host vNIC
// and returns its assigned MAC with the switch and adapter identities.
const scriptEnsureSwitch = buildPrelude + `$fresh = -not $in.switchId
if ($fresh) {
  if (@(Get-VMSwitch | Where-Object { $_.Name -eq $in.switch }).Count -gt 0) { throw 'A switch with this name exists without a recorded ID.' }
  $sw = New-VMSwitch -Name $in.switch -SwitchType Internal -Notes ('CashPilot Secure VM. cashpilot-owner=' + $in.owner)
  $ports = @(Get-ManagementPort $sw)
  if ($ports.Count -ne 1 -or -not $ports[0].Id) { throw 'The new switch did not return its management adapter ID.' }
  $managementId = [string]$ports[0].Id
} else {
  $sw = Get-OwnedSwitch $in.switchId $in.owner
  $managementId = $in.managementId
}
$switchId = $sw.Id.ToString()
$na = $null
for ($i = 0; $i -lt 30; $i++) {
  $mo = Get-RecordedManagementPort (Get-OwnedSwitch $switchId $in.owner) $managementId
  $match = @(Find-ManagementAdapter $mo)
  if ($match.Count -eq 1) { $na = $match[0]; break }
  Start-Sleep -Seconds 1
}
if (-not $na) { throw 'The host vNIC did not appear with its device identity.' }
$mac = Get-EffectiveManagementMAC $mo $na
if (-not $fresh -and $mac -cne $in.mac) {
  throw 'The recorded management adapter MAC changed and was left in place.'
}
if (-not $mac) { throw 'The management adapter MAC does not match the Hyper-V port MAC.' }
if ($fresh -and $mac -cnotmatch '^00155D[0-9A-F]{6}$') {
  throw 'Hyper-V did not assign the management adapter a MAC with the 00-15-5D prefix.'
}
$idx = [int]$na.ifIndex
$int = @(Get-NetIPInterface -InterfaceIndex $idx -AddressFamily IPv4)
if ($int.Count -ne 1) { throw 'Could not verify DHCP on the recorded management adapter.' }
if ($int[0].Dhcp.ToString() -ne 'Disabled') {
  if (-not $fresh) { throw 'DHCP is enabled on the recorded management adapter and was left in place.' }
  $null = Get-HostVNIC $switchId $in.owner $managementId $idx $mac
  Set-NetIPInterface -InputObject $int[0] -Dhcp Disabled
}
$have = @(Get-NetIPAddress -InterfaceIndex $idx -AddressFamily IPv4 -ErrorAction SilentlyContinue | Where-Object { $_.IPAddress -eq $in.gateway })
if ($have.Count -eq 0) {
  $na = Get-HostVNIC $switchId $in.owner $managementId $idx $mac
  $int = @(Get-NetIPInterface -InterfaceIndex $idx -AddressFamily IPv4)
  if ($int.Count -ne 1 -or $int[0].Dhcp.ToString() -ne 'Disabled') { throw 'DHCP must already be disabled on the recorded management adapter.' }
  New-NetIPAddress -InterfaceIndex $na.ifIndex -IPAddress $in.gateway -PrefixLength ([int]$in.prefixLen) | Out-Null
} elseif ($have.Count -ne 1 -or [int]$have[0].PrefixLength -ne [int]$in.prefixLen) {
  throw 'The existing gateway address has a different prefix and was left in place.'
}
if ($sw.Name -ne $in.switch) {
  if (@(Get-VMSwitch | Where-Object { $_.Name -eq $in.switch }).Count -gt 0) { throw 'Another switch has this name.' }
  $null = Get-HostVNIC $switchId $in.owner $managementId $idx $mac
  Rename-VMSwitch -VMSwitch $sw -NewName $in.switch
}
$null = Get-HostVNIC $switchId $in.owner $managementId $idx $mac
Write-Doc ([ordered]@{ switchId = $switchId; managementId = $managementId; mac = $mac; ifIndex = $idx })
`

const natOwnershipPrelude = buildPrelude + `function Test-RecordedNAT($nat, $recorded) {
  $id = [string]$recorded.id
  if (-not $id -or [Text.Encoding]::UTF8.GetByteCount($id) -gt 260 -or $id -match '\p{Cc}') { return $false }
  # Windows keys a NAT's InstanceID by its name and store ("name;0"), so the ID
  # cannot tell a same-name replacement apart. Ownership is the exact record
  # Setup made, and Setup records only a NAT it created.
  return ($nat.Name -eq $recorded.nat -and [string]$nat.InstanceID -ceq $id -and
    [string]$nat.InternalIPInterfaceAddressPrefix -eq $recorded.prefix)
}
`

// scriptEnsureNAT checks the full inventory again just before New-NetNat.
const scriptEnsureNAT = natOwnershipPrelude + `$n = @(Get-NetNat -ErrorAction Stop)
$nat = $null
foreach ($candidate in $n) {
  if ($nat -or -not (Test-RecordedNAT $candidate $in)) {
    $prefix = [string]$candidate.InternalIPInterfaceAddressPrefix
    if (-not $prefix) { $prefix = 'an unknown prefix' }
    throw ('A NAT network named "{0}" already exists on {1}. ` + foreignNATRemedy + `' -f $candidate.Name, $prefix)
  }
  $nat = $candidate
}
if (-not $nat) {
  $nat = New-NetNat -Name $in.nat -InternalIPInterfaceAddressPrefix $in.prefix
}
Write-Doc ([ordered]@{ id = [string]$nat.InstanceID })
`

// scriptCreateVM creates the VM off, with one disconnected adapter: only the
// proof and the guard ever connect it. Gen 2, Secure Boot, a vTPM with a local
// key protector, static memory, the plan's processor shape, no checkpoints,
// no automatic start, the owner tag in its Notes, and the two ISOs.
var scriptCreateVM = buildPrelude + diskIdentityPrelude + runtime.SecureVMDirectoryPS + scriptCreateVMBody

const scriptCreateVMBody = `if (@(Get-VM | Where-Object { $_.Name -eq $in.vm }).Count -gt 0) { throw 'A VM with this name exists.' }
$dataLease = Open-CPDDirectory $in.dataDir $false 'cashpilot-data'
$folderLease = $null
try {
$folder = Join-Path $in.dataDir ('vm-' + [guid]::NewGuid().ToString('N'))
$folderLease = Open-CPDDirectory $folder $true $in.owner
$disk = Join-Path $folder ($in.vm + '.vhdx')
$vm = $null
$createdDisk = New-VHD -Path $disk -Dynamic -SizeBytes ([uint64]$in.diskBytes)
if (-not $createdDisk -or [IO.Path]::GetFullPath($createdDisk.Path) -ne $disk) { throw 'The disk creation did not return the requested file.' }
$diskID = [CpdDisk]::Identify($disk)
if (-not $diskID) { throw 'Could not verify the created disk.' }
try {
  $vm = New-VM -Name $in.vm -Generation 2 -MemoryStartupBytes ([uint64]$in.memory) -VHDPath $disk -Path $folder
  # The owner tag first, in its own call: it is what makes a rerun adopt the VM.
  Set-VM -VM $vm -Notes ('CashPilot Secure VM. cashpilot-owner=' + $in.owner)
  $id = $vm.Id.ToString()
  Set-VM -VM (Get-OwnedVM $id $in.owner) -ProcessorCount ([int]$in.procCount) -StaticMemory -CheckpointType Disabled -AutomaticStartAction Nothing -AutomaticStopAction ShutDown
  Set-VMProcessor -VM (Get-OwnedVM $id $in.owner) -Maximum ([int]$in.procMax) -RelativeWeight ([int]$in.procWeight) -ExposeVirtualizationExtensions $false
  $a = @(Get-VMNetworkAdapter -VM $vm)
  if ($a.Count -ne 1) { throw 'The new VM does not have exactly one network adapter.' }
  if ($a[0].SwitchName) { $null = Get-OwnedVM $id $in.owner; Disconnect-VMNetworkAdapter -VMNetworkAdapter $a[0] }
  $null = Get-OwnedVM $id $in.owner
  Set-VMNetworkAdapter -VMNetworkAdapter $a[0] -StaticMacAddress $in.mac -DhcpGuard On -RouterGuard On -MacAddressSpoofing Off -DeviceNaming Off
  Set-VMFirmware -VM (Get-OwnedVM $id $in.owner) -EnableSecureBoot On -SecureBootTemplate MicrosoftWindows
  Set-VMKeyProtector -VM (Get-OwnedVM $id $in.owner) -NewLocalKeyProtector
  Enable-VMTPM -VM (Get-OwnedVM $id $in.owner)
  $boot = Add-VMDvdDrive -VM (Get-OwnedVM ($vm.Id.ToString()) $in.owner) -Path $in.iso -Passthru
  $answer = Add-VMDvdDrive -VM (Get-OwnedVM ($vm.Id.ToString()) $in.owner) -Path $in.answer -Passthru
  $dvds = @(foreach ($d in @($boot, $answer)) {
    if (-not $d.Id -or -not $d.Path) { throw 'The attached DVD did not return an identity.' }
    @{ id = [string]$d.Id; path = [string]$d.Path }
  })
  Set-VMFirmware -VM (Get-OwnedVM ($vm.Id.ToString()) $in.owner) -FirstBootDevice $boot
} catch {
  if ($vm) {
    $id = $vm.Id.ToString()
    Stop-VM -VM (Get-OwnedVM $id $in.owner) -TurnOff -Force -ErrorAction SilentlyContinue
    Remove-VM -VM (Get-OwnedVM $id $in.owner) -Force -ErrorAction SilentlyContinue
    if (-not (Test-DiskInUse $diskID)) { [CpdDisk]::DeleteOwned($disk, $diskID) | Out-Null }
  }
  throw
}
Write-Doc ([ordered]@{ vmId = $vm.Id.ToString(); adapterId = ($a[0].Id -split '\\')[-1]; disk = @{ path = $disk; id = $diskID; folderId = $folderLease.Id }; dvds = $dvds })
} finally {
  if ($folderLease) { $folderLease.Dispose() }
  $dataLease.Dispose()
}
`

// scriptSetIntegration sets integration services by ID.
const scriptSetIntegration = buildPrelude + `$vm = Get-OwnedVM $in.vm $in.owner
foreach ($p in $in.want.PSObject.Properties) {
  $s = @(Get-VMIntegrationService -VM $vm | Where-Object { (($_.Id -split '\\')[-1]).ToUpper() -eq $p.Name })
  foreach ($x in $s) { $null = Get-OwnedVM $in.vm $in.owner; if ($p.Value) { Enable-VMIntegrationService -VMIntegrationService $x } else { Disable-VMIntegrationService -VMIntegrationService $x } }
}
Write-Doc ([ordered]@{ ok = $true })
`

// scriptSetBindings unbinds components from the host vNIC by component ID.
const scriptSetBindings = buildPrelude + `$na = Get-HostVNIC $in.switchId $in.owner $in.managementId ([int]$in.ifIndex)
if ($in.expected) {
  if (-not (Test-HostBindings $na $in.expected)) { throw 'The management adapter bindings changed and were left in place.' }
} else {
  foreach ($c in @($in.off)) {
    $b = @(Get-HostBinding $c)
    if ($b.Count -eq 1 -and $b[0].Enabled) { Disable-NetAdapterBinding -InputObject $b[0] }
  }
}
Write-Doc @{ bindings = (Export-HostBindings $na) }
`

func blockFirewallID(n Names, owner string) string { return n.FirewallGroup + "-block-in-" + owner }

// firewallPrelude fingerprints the provider ID and the rule's match fields.
// Enabled is checked separately because the proof temporarily changes it.
const firewallIdentityPS = `function Get-FirewallIdentity($r, $iface = $null) {
  if (-not $r.InstanceID) { throw 'The firewall rule did not return a provider ID.' }
  $a = Get-NetFirewallAddressFilter -AssociatedNetFirewallRule $r
  $p = Get-NetFirewallPortFilter -AssociatedNetFirewallRule $r
  $i = Get-NetFirewallInterfaceFilter -AssociatedNetFirewallRule $r
  $app = Get-NetFirewallApplicationFilter -AssociatedNetFirewallRule $r
  $svc = Get-NetFirewallServiceFilter -AssociatedNetFirewallRule $r
  $it = Get-NetFirewallInterfaceTypeFilter -AssociatedNetFirewallRule $r
  $sec = Get-NetFirewallSecurityFilter -AssociatedNetFirewallRule $r
  if (-not $a -or -not $p -or -not $i -or -not $app -or -not $svc -or -not $it -or -not $sec) {
    throw 'Could not read the complete firewall rule.'
  }
  $shape = [ordered]@{
    id = [string]$r.InstanceID; name = [string]$r.Name; group = [string]$r.Group; desc = [string]$r.Description
    direction = [string]$r.Direction; action = [string]$r.Action; profile = [string]$r.Profile
    edge = [string]$r.EdgeTraversalPolicy; loose = [string]$r.LooseSourceMapping; localOnly = [string]$r.LocalOnlyMapping
    local = @($a.LocalAddress | Sort-Object); remote = @($a.RemoteAddress | Sort-Object)
    protocol = [string]$p.Protocol; localPort = @($p.LocalPort | Sort-Object); remotePort = @($p.RemotePort | Sort-Object)
    icmp = @($p.IcmpType | Sort-Object); dynamicTarget = [string]$p.DynamicTarget
    iface = @(if ($null -ne $iface) { $iface } else { $i.InterfaceAlias | Sort-Object }); ifaceType = [string]$it.InterfaceType
    program = [string]$app.Program; package = [string]$app.Package; service = [string]$svc.Service
    auth = [string]$sec.Authentication; encryption = [string]$sec.Encryption; override = [string]$sec.OverrideBlockRules
    localUser = [string]$sec.LocalUser; remoteUser = [string]$sec.RemoteUser; remoteMachine = [string]$sec.RemoteMachine
  }
  $j = ConvertTo-Json -InputObject $shape -Compress -Depth 4
  $sha = [Security.Cryptography.SHA256]::Create()
  try { [BitConverter]::ToString($sha.ComputeHash([Text.Encoding]::UTF8.GetBytes($j))).Replace('-', '').ToLowerInvariant() }
  finally { $sha.Dispose() }
}
`

const firewallPrelude = buildPrelude + firewallIdentityPS

const scriptEnsureFirewall = firewallPrelude + `$rs = @(Get-NetFirewallRule -Group $in.group -ErrorAction SilentlyContinue)
$na = Get-HostVNIC $in.switchId $in.owner $in.managementId ([int]$in.ifIndex)
$alias = [System.Management.Automation.WildcardPattern]::Escape($na.Name)
if ($rs.Count -eq 0) {
  $r = New-NetFirewallRule -Name $in.id -DisplayName ($in.group + ' block inbound') -Group $in.group -Direction Inbound -Action Block -InterfaceAlias $alias -Enabled True -Description ('CashPilot Secure VM. cashpilot-owner=' + $in.owner)
  $identity = Get-FirewallIdentity $r
} elseif ($rs.Count -eq 1 -and $rs[0].Name -ceq $in.id -and (Get-Owner ([string]$rs[0].Description)) -ceq $in.owner) {
  $r = $rs[0]
  $identity = Get-FirewallIdentity $r
  if ($identity -cne $in.identity -or $r.Enabled.ToString() -ne 'True') { throw 'The firewall rule differs from Setup and was left in place.' }
} else { throw 'The firewall group has a rule without a recorded identity.' }
Write-Doc ([ordered]@{ identity = $identity })
`

// scriptSetHostNeighbor adds the guest's entry only when the address is free.
const scriptSetHostNeighbor = buildPrelude + `$na = Get-HostVNIC $in.switchId $in.owner $in.managementId ([int]$in.ifIndex)
$have = @(Get-NetNeighbor -InterfaceIndex ([int]$na.ifIndex) -AddressFamily IPv4 -ErrorAction SilentlyContinue | Where-Object { $_.IPAddress -eq $in.ip })
if ($have.Count -eq 0) {
  $na = Get-HostVNIC $in.switchId $in.owner $in.managementId ([int]$in.ifIndex)
  New-NetNeighbor -InterfaceIndex ([int]$na.ifIndex) -IPAddress $in.ip -LinkLayerAddress $in.mac -State Permanent | Out-Null
} elseif ($have.Count -ne 1 -or $have[0].State.ToString() -ne 'Permanent' -or $have[0].LinkLayerAddress -ne $in.mac) {
  throw 'The existing neighbour entry conflicts with setup and was left in place.'
}
Write-Doc ([ordered]@{ ok = $true })
`

// scriptEnsureGuardian records the fingerprint only when this call creates it.
const scriptEnsureGuardian = buildPrelude + `$id = ''
$gs = @(Get-HgsGuardian -Name 'UntrustedGuardian' -ErrorAction SilentlyContinue)
if ($gs.Count -gt 1) { throw 'More than one UntrustedGuardian matched.' }
if ($gs.Count -eq 0) {
  $g = New-HgsGuardian -Name 'UntrustedGuardian' -GenerateCertificates
  $id = Get-GuardianIdentity $g
  if (-not $id) { throw 'The new guardian did not return both certificates.' }
}
Write-Doc ([ordered]@{ id = $id })
`

// scriptAddHyperVAdmin adds the account to Hyper-V Administrators.
const scriptAddHyperVAdmin = buildPrelude + `$added = $false
if ((Get-GroupSids 'S-1-5-32-578') -notcontains $in.sid) { Add-LocalGroupMember -SID 'S-1-5-32-578' -Member $in.sid; $added = $true }
Write-Doc ([ordered]@{ added = $added })
`

// scriptWriteAnswerISO writes autounattend.xml onto a new ISO through IMAPI2,
// from a staging folder next to the ISO that it deletes again.
var scriptWriteAnswerISO = buildPrelude + runtime.SecureVMDirectoryPS + runtime.SecureVMFilesPS + `if (-not ('CpdIso' -as [type])) { Add-Type -TypeDefinition @'
using System; using System.IO; using System.Runtime.InteropServices; using System.Runtime.InteropServices.ComTypes;
public static class CpdIso { public static void Write(object stream, string path) {
  IStream s = (IStream)stream; using (var f = new FileStream(path, FileMode.CreateNew, FileAccess.Write)) { byte[] buf = new byte[65536]; IntPtr p = Marshal.AllocHGlobal(4);
  try { while (true) { s.Read(buf, buf.Length, p); int n = Marshal.ReadInt32(p); if (n <= 0) break; f.Write(buf, 0, n); } } finally { Marshal.FreeHGlobal(p); } } } }
'@ }
$root = [IO.Path]::GetDirectoryName($in.path)
$lease = Open-CPDDirectory $root $false 'cashpilot-data'
try {
  $j = Open-CPDFileJournal $root
  $stageRel = '.answer-stage.' + [guid]::NewGuid().ToString('N')
  $stage = Join-Path $root $stageRel
  Write-CPDFileRecord $j $stageRel ''
  $stageLease = Open-CPDDirectory $stage $true 'cashpilot-answer'
  $stageID = $stageLease.Id
  $markerID = [CpdDirectory]::FileID((Join-Path $stage '.cashpilot-directory'))
  $answerRel = Join-Path $stageRel 'autounattend.xml'
  $answerID = $null
  try {
    Write-CPDFileRecord $j $stageRel $stageID
    Write-CPDFileRecord $j (Join-Path $stageRel '.cashpilot-directory') $markerID
    $answerID = Write-CPDNewFile $j $answerRel ([Convert]::FromBase64String($in.xml))
    $fs = New-Object -ComObject IMAPI2FS.MsftFileSystemImage
    $fs.FileSystemsToCreate = 3
    $fs.VolumeName = 'CPDANSWER'
    # AddTree includes every file, so remove the private marker first.
    Remove-CPDFile $j (Join-Path $stageRel '.cashpilot-directory') $markerID
    $fs.Root.AddTree($stage, $false)
    $rel = [IO.Path]::GetFileName($in.path)
    Write-CPDFileRecord $j $rel ''
    [CpdIso]::Write($fs.CreateResultImage().ImageStream, $in.path)
    Write-CPDFileRecord $j $rel ([CpdDirectory]::FileID($in.path))
  } finally {
    Remove-CPDFile $j $answerRel $answerID
    $stageLease.Dispose()
    Remove-CPDFile $j $stageRel $stageID
  }
} finally { $lease.Dispose() }
Write-Doc ([ordered]@{ ok = $true })
`

// scriptBootKeys starts the VM and presses Enter every half second for 20
// seconds through its synthetic keyboard, for the "press any key to boot from
// CD" prompt. On live run 5 a separate start and key script missed the prompt
// once in four: PowerShell took longer to start than the prompt stays up.
const scriptBootKeys = buildPrelude + `$vm = Get-OwnedVM $in.vm $in.owner
$cs = Get-CimInstance -Namespace root\virtualization\v2 -ClassName Msvm_ComputerSystem -Filter ("Name='" + $in.vm + "'")
if (-not $cs) { throw 'The VM was not found.' }
if ($vm.State.ToString() -ne 'Running') { Start-VM -VM $vm }
$kb = $null
for ($i = 0; $i -lt 40; $i++) {
  if ($i % 4 -eq 3) { $null = Get-OwnedVM $in.vm $in.owner }
  if (-not $kb) { $kb = Get-CimAssociatedInstance -InputObject $cs -ResultClassName Msvm_Keyboard -ErrorAction SilentlyContinue }
  if ($kb) { try { Invoke-CimMethod -InputObject $kb -MethodName TypeKey -Arguments @{ keyCode = [uint32]13 } | Out-Null } catch { } }
  Start-Sleep -Milliseconds 500
}
Write-Doc ([ordered]@{ ok = $true })
`

// scriptEjectDVDs removes only the journaled attachments on the owned VM.
const scriptEjectDVDs = buildPrelude + `$vm = Get-OwnedVM $in.vm $in.owner
foreach ($d in @($in.dvds)) {
  $vm = Get-OwnedVM $in.vm $in.owner
  $matched = @(Get-VMDvdDrive -VM $vm | Where-Object { [string]$_.Id -eq $d.id -and [string]$_.Path -eq $d.path })
  if ($matched.Count -eq 1) {
    $null = Get-OwnedVM $in.vm $in.owner
    Remove-VMDvdDrive -VMDvdDrive $matched[0]
  }
}
Write-Doc ([ordered]@{ left = @(Get-VMDvdDrive -VM $vm).Count })
`

// scriptRegisterGuardTask registers the guard task as SYSTEM, after stopping a
// running one. Triggers: startup, resume (Power-Troubleshooter event 1), and
// every 5 minutes from delay seconds on. IgnoreNew keeps one guard at a time.
const scriptRegisterGuardTask = buildPrelude + `$t = @(Get-ScheduledTask -TaskPath $in.taskPath -TaskName $in.taskName -ErrorAction SilentlyContinue)
if ($t.Count -gt 1) { throw 'More than one scheduled task matched.' }
if ($t.Count -eq 1) {
  if ($t[0].Description -cne ('CashPilot Secure VM guard. cashpilot-owner=' + $in.owner)) { throw 'A scheduled task with this name was not made by this install.' }
  if ($t[0].State.ToString() -eq 'Running') {
    Stop-ScheduledTask -InputObject $t[0] | Out-Null
    for ($i = 0; $i -lt 30; $i++) {
      if ((Get-ScheduledTask -TaskPath $in.taskPath -TaskName $in.taskName).State.ToString() -ne 'Running') { break }
      Start-Sleep -Seconds 1
    }
  }
}
$action = New-ScheduledTaskAction -Execute $in.exe -Argument ('--vm-guard "' + $in.dir + '"')
$boot = New-ScheduledTaskTrigger -AtStartup
$rep = New-ScheduledTaskTrigger -Once -At ((Get-Date).AddSeconds([int]$in.delay)) -RepetitionInterval (New-TimeSpan -Minutes 5)
$cls = Get-CimClass -Namespace Root/Microsoft/Windows/TaskScheduler -ClassName MSFT_TaskEventTrigger
$resume = New-CimInstance -CimClass $cls -ClientOnly
$resume.Enabled = $true
$resume.Subscription = '<QueryList><Query Id="0" Path="System"><Select Path="System">*[System[Provider[@Name=''Microsoft-Windows-Power-Troubleshooter''] and EventID=1]]</Select></Query></QueryList>'
$set = New-ScheduledTaskSettingsSet -MultipleInstances IgnoreNew -ExecutionTimeLimit ([TimeSpan]::Zero) -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable
$pr = New-ScheduledTaskPrincipal -UserId 'S-1-5-18' -LogonType ServiceAccount -RunLevel Highest
$replace = $t.Count -eq 1
if ($replace) {
  $live = @(Get-ScheduledTask -TaskPath $in.taskPath -TaskName $in.taskName -ErrorAction SilentlyContinue)
  if ($live.Count -ne 1 -or $live[0].Description -cne ('CashPilot Secure VM guard. cashpilot-owner=' + $in.owner) -or $live[0].State.ToString() -eq 'Running') {
    throw 'The guard task changed before registration.'
  }
}
Register-ScheduledTask -TaskPath $in.taskPath -TaskName $in.taskName -Action $action -Trigger @($boot, $rep, $resume) -Settings $set -Principal $pr -Description ('CashPilot Secure VM guard. cashpilot-owner=' + $in.owner) -Force:$replace | Out-Null
Write-Doc ([ordered]@{ ok = $true })
`

// scriptStartGuardTask starts the guard task.
const scriptStartGuardTask = buildPrelude + `$t = @(Get-ScheduledTask -TaskPath $in.taskPath -TaskName $in.taskName -ErrorAction SilentlyContinue)
if ($t.Count -ne 1 -or $t[0].Description -cne ('CashPilot Secure VM guard. cashpilot-owner=' + $in.owner)) {
  throw 'The owned guard task was not found.'
}
Start-ScheduledTask -InputObject $t[0] | Out-Null
Write-Doc ([ordered]@{ ok = $true })
`

// scriptUnregisterGuardTask stops and removes the recorded task.
const scriptUnregisterGuardTask = buildPrelude + `function Get-OwnedGuardTask {
  $t = @(Get-ScheduledTask -TaskPath $in.taskPath -TaskName $in.taskName -ErrorAction SilentlyContinue)
  if ($t.Count -eq 0) { return }
  if ($t.Count -ne 1 -or $t[0].Description -cne ('CashPilot Secure VM guard. cashpilot-owner=' + $in.owner)) {
    throw 'The scheduled task was not made by this install.'
  }
  return $t[0]
}
$t = Get-OwnedGuardTask
if ($t -and $t.State.ToString() -eq 'Running') {
  Stop-ScheduledTask -InputObject $t | Out-Null
  for ($i = 0; $i -lt 30; $i++) {
    $t = Get-OwnedGuardTask
    if (-not $t -or $t.State.ToString() -ne 'Running') { break }
    Start-Sleep -Seconds 1
  }
}
$t = Get-OwnedGuardTask
if ($t) {
  if ($t.State.ToString() -eq 'Running') { throw 'The guard task did not stop.' }
  Unregister-ScheduledTask -InputObject $t -Confirm:$false
}
Write-Doc ([ordered]@{ ok = $true })
`

// scriptRemoveVM removes the owned VM and the recorded disk file. Any
// other attached VHDX is reported and kept.
var scriptRemoveVM = portACLPrelude + diskIdentityPrelude + runtime.SecureVMDirectoryPS + vmFolderPS + `$vm = @()
if ($in.vm) { $vm = @(Find-OurVM $in.vm) }
if ($vm.Count -gt 1) { throw 'More than one VM has the recorded ID.' }
$disks = @()
if ($vm.Count -eq 1) {
  $vm = $vm[0]
  if ((Get-Owner ([string]$vm.Notes)) -cne $in.owner) { throw 'The VM was not made by this install.' }
  $a = Get-RecordedVMAdapter $in.vm $in.owner $in.adapter $true
  if ((Get-PortACLDigest $a) -cne $in.aclDigest -or @(Get-VMNetworkAdapterExtendedAcl -VMNetworkAdapter $a).Count -ne 0) {
    throw 'The VM has independent port ACLs and was left in place.'
  }
  if (@(Get-VMSnapshot -VM $vm -ErrorAction Stop).Count -ne 0) { throw 'The VM has checkpoints, so it was left in place.' }
  $disks = @(Get-VMHardDiskDrive -VM $vm | Where-Object { $_.Path } | ForEach-Object { [string]$_.Path })
  if ($vm.State.ToString() -ne 'Off') { Stop-VM -VM (Get-OwnedVM $in.vm $in.owner) -TurnOff -Force }
  Remove-VM -VM (Get-OwnedVM $in.vm $in.owner) -Force
}
$left = @()
if ($in.disk) {
  $path = $in.disk.path
  $deleted = $false
  $lease = $null
  if ($in.disk.folderId -and (Test-Path -LiteralPath $path)) {
    try {
      $lease = Open-CPDVMFolder ([IO.Path]::GetDirectoryName($path)) $in.owner
      if ($lease.Id -ceq $in.disk.folderId -and -not (Test-DiskInUse $in.disk.id)) {
        $deleted = [CpdDisk]::DeleteOwned($path, $in.disk.id)
      }
    } catch { $deleted = $false }
    finally { if ($lease) { $lease.Dispose() } }
  }
  if ($deleted) { $disks = @($disks | Where-Object { $_ -ne $path }) }
  elseif (Test-Path -LiteralPath $path) { $disks += $path }
}
$left = @($disks | Select-Object -Unique)
Write-Doc ([ordered]@{ left = @($left) })
`

// vmFolderPS opens the private folder Setup made for the VM. New-VM -Path
// adds Hyper-V's own rules to it: CREATOR OWNER, the Virtual Machines group
// (S-1-5-83-0) and the VM worker capability. The private-folder check refuses
// any rule beyond SYSTEM and Administrators, so after New-VM it could never
// pass and Remove kept the disk (live run 5). This accepts exactly those
// Hyper-V principals beside the two private rules, then checks the identity
// Setup recorded on the folder.
const vmFolderPS = `
function Open-CPDVMFolder([string]$path, [string]$owner) {
  $lease = [CpdDirectory]::Pin($path)
  try {
    $acl = Get-Acl -LiteralPath $path
    $private = @('S-1-5-18', 'S-1-5-32-544')
    $hyperV = @('S-1-3-0', 'S-1-5-83-0', 'S-1-15-3-1024-2268835264-3721307629-241982045-173645152-1490879176-104643441-2915960892-1612460704')
    $rules = @($acl.GetAccessRules($true, $true, [Security.Principal.SecurityIdentifier]))
    $inherit = [Security.AccessControl.InheritanceFlags]'ContainerInherit, ObjectInherit'
    $base = @($rules | Where-Object {
      $_.IdentityReference.Value -in $private -and $_.AccessControlType -eq 'Allow' -and $_.FileSystemRights -eq 'FullControl' -and
      $_.InheritanceFlags -eq $inherit -and $_.PropagationFlags -eq 'None'
    })
    if ($acl.GetOwner([Security.Principal.SecurityIdentifier]).Value -notin $private -or -not $acl.AreAccessRulesProtected -or
        @($base | ForEach-Object { $_.IdentityReference.Value } | Select-Object -Unique).Count -ne 2 -or
        @($rules | Where-Object { $_.AccessControlType -ne 'Allow' -or $_.IsInherited -or $_.IdentityReference.Value -notin ($private + $hyperV) }).Count) {
      throw ('The VM folder has untrusted access rules: ' + $path)
    }
    $record = Join-Path $path '.cashpilot-directory'
    $null = [CpdDirectory]::FileID($record)
    if ([IO.File]::ReadAllText($record) -cne ($owner + ':' + $lease.Id)) { throw ('The directory identity was not recorded: ' + $path) }
    return $lease
  } catch {
    $lease.Dispose()
    throw
  }
}
`

// scriptRemoveSwitch leaves the switch when deleting it would take another
// port or independent host configuration with it.
const scriptRemoveSwitch = buildPrelude + `$sw = @(Get-VMSwitch | Where-Object { $_.Id.ToString() -eq $in.switchId })
if ($sw.Count -eq 0) { Write-Doc @{ kept = '' }; exit 0 }
$sw = Get-OwnedSwitch $in.switchId $in.owner
if (@(Get-VMNetworkAdapter -All | Where-Object { (-not $_.IsManagementOs) -and ($_.SwitchId -eq $sw.Id) }).Count -gt 0) {
  Write-Doc @{ kept = 'a VM still has an adapter on it' }; exit 0
}
$ports = @(Get-ManagementPort $sw)
if ($ports.Count -ne 1 -or -not $in.managementId -or [string]$ports[0].Id -ne $in.managementId) {
  Write-Doc @{ kept = 'the management port inventory differs from Setup' }; exit 0
}
$na = Get-HostVNIC $in.switchId $in.owner $in.managementId
$idx = [int]$na.ifIndex
$ipif = Get-NetIPInterface -InterfaceIndex $idx -AddressFamily IPv4
if (-not $in.mac -or (Get-EffectiveManagementMAC $ports[0] $na) -cne $in.mac -or $ipif.Dhcp.ToString() -ne 'Disabled') {
  Write-Doc @{ kept = 'the recorded management port MAC or DHCP setting changed' }; exit 0
}
$addresses = @(Get-NetIPAddress -InterfaceIndex $idx | Where-Object {
  -not ($_.IPAddress -eq $in.gateway -and [int]$_.PrefixLength -eq [int]$in.prefixLen) -and
  -not ($_.IPAddress -like 'fe80:*' -and $_.SuffixOrigin.ToString() -eq 'Link')
})
# Windows adds Permanent entries of its own: IPv4 multicast (224.0.0.0/4),
# the limited and the subnet broadcast, and IPv6 multicast.
$neighbors = @(Get-NetNeighbor -InterfaceIndex $idx | Where-Object {
  $ip = [string]$_.IPAddress
  $_.State.ToString() -eq 'Permanent' -and
  -not ($ip -eq $in.guest -and [string]$_.LinkLayerAddress -eq $in.guestMac) -and
  $ip -notmatch '^(22[4-9]|23[0-9])\.' -and
  $ip -ne '255.255.255.255' -and
  -not ($ip -eq $in.broadcast -and [string]$_.LinkLayerAddress -eq 'FF-FF-FF-FF-FF-FF') -and
  $ip -notlike 'ff*'
})
if ($addresses.Count -gt 0 -or $neighbors.Count -gt 0 -or -not (Test-HostBindings $na $in.bindings)) {
  Write-Doc @{ kept = 'the management port has independent addresses, neighbours or bindings' }; exit 0
}
$null = Get-HostVNIC $in.switchId $in.owner $in.managementId $idx $in.mac
Remove-VMSwitch -VMSwitch $sw -Force
Write-Doc @{ kept = '' }
`

// scriptRemoveNAT removes the exact recorded provider object.
const scriptRemoveNAT = natOwnershipPrelude + `$n = @(Get-NetNat -ErrorAction SilentlyContinue | Where-Object { $_.Name -eq $in.nat })
if ($n.Count -gt 0) {
  if ($n.Count -ne 1 -or -not (Test-RecordedNAT $n[0] $in)) {
    throw 'The NAT changed since inventory.'
  }
  # WinNAT adds external address port pools of its own to every NAT, two per
  # host IPv4 address, so they say nothing about who uses it. A static mapping
  # is what a person adds to forward ports through it.
  if (@(Get-NetNatStaticMapping | Where-Object { $_.NatName -eq $in.nat }).Count -gt 0) {
    Write-Doc @{ kept = 'it has independent NAT mappings' }; exit 0
  }
  Remove-NetNat -InputObject $n[0] -Confirm:$false
}
Write-Doc @{ kept = '' }
`

const scriptRemoveFirewall = firewallPrelude + `$rs = @(Get-NetFirewallRule -Group $in.group -ErrorAction SilentlyContinue |
  Where-Object { $_.Name -ceq $in.id -and (Get-Owner ([string]$_.Description)) -ceq $in.owner })
if ($rs.Count -gt 0) {
  $identity = ''
  if ($rs.Count -eq 1) {
    $identity = Get-FirewallIdentity $rs[0]
    # Removing the switch makes Windows rewrite the rule's interface to the
    # GUID of the deleted vNIC. Only then, compare with the alias it had.
    if ($identity -cne $in.identity -and $in.alias) {
      $ifs = @((Get-NetFirewallInterfaceFilter -AssociatedNetFirewallRule $rs[0]).InterfaceAlias)
      $g = [guid]::Empty
      if ($ifs.Count -eq 1 -and [guid]::TryParse([string]$ifs[0], [ref]$g) -and
          @(Get-NetAdapter -IncludeHidden -ErrorAction Stop | Where-Object { $x = [guid]::Empty; [guid]::TryParse([string]$_.InterfaceGuid, [ref]$x) -and $x -eq $g }).Count -eq 0) {
        $identity = Get-FirewallIdentity $rs[0] $in.alias
      }
    }
  }
  if ($rs.Count -ne 1 -or -not $in.identity -or $identity -cne $in.identity -or $rs[0].Enabled.ToString() -ne 'True') {
    Write-Doc @{ kept = 'the firewall rule differs from Setup' }; exit 0
  }
  Remove-NetFirewallRule -InputObject $rs[0]
}
Write-Doc @{ kept = '' }
`

// scriptRemoveGuardian rechecks the fingerprint and live VM dependencies.
const scriptRemoveGuardian = buildPrelude + `$g = @(Get-HgsGuardian -Name 'UntrustedGuardian' -ErrorAction SilentlyContinue)
if ($g.Count -gt 0) {
  if ($g.Count -ne 1 -or (Get-GuardianIdentity $g[0]) -cne $in.id) { throw 'The guardian changed since inventory.' }
  if (@(Get-VM | Where-Object { Test-GuardianUser $_ }).Count -gt 0) { throw 'A VM still needs the guardian.' }
  # Remove-HgsGuardian warns that it leaves the guardian's two certificates.
  # A non-interactive PowerShell prints the warning on standard output, where
  # only the answer document may go. The certificates stay: a copy or backup
  # of the VM needs them to start, and Remove reports them as kept.
  $null = Remove-HgsGuardian -InputObject $g[0] -Confirm:$false -WarningAction SilentlyContinue
}
Write-Doc ([ordered]@{ ok = $true })
`

// scriptClearProof inventories leftovers. An interrupted proof has no live
// handles for its rules, registrations or trace session.
const scriptClearProof = buildPrelude + `$left = @()
foreach ($r in @(Get-NetFirewallRule -Group $in.group -ErrorAction SilentlyContinue)) {
  $left += 'the proof firewall rule ' + [string]$r.Name + ' (ownership could not be verified)'
}
$k = 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Virtualization\GuestCommunicationServices'
if (Test-Path -LiteralPath $k) {
  foreach ($c in @(Get-ChildItem -LiteralPath $k)) {
    $element = [string]$c.GetValue('ElementName')
    if ($element.StartsWith($in.elementPrefix, [StringComparison]::Ordinal)) {
      $left += 'the unverified proof hv_sock key ' + [string]$c.PSChildName
    }
  }
}
$s = @(Get-EtwTraceSession -Name 'PktMon' -ErrorAction SilentlyContinue)
if ($s.Count -gt 0) {
  $left += 'the running pktmon session at ' + [string]$s[0].LocalFilePath + ' (ownership could not be verified)'
}
Write-Doc ([ordered]@{ cleared = @(); left = @($left); unverifiedCapture = ($s.Count -gt 0) })
`

// scriptGuest runs a verified local script file through PowerShell Direct.
// The guest receives its JSON argument separately from the script.
const scriptGuest = buildPrelude + `$pw = ConvertTo-SecureString ([string]$in.password) -AsPlainText -Force
$cred = New-Object Management.Automation.PSCredential ([string]$in.user, $pw)
$null = Get-OwnedVM $in.vm $in.owner
$out = Invoke-Command -VMId $in.vm -Credential $cred -ErrorAction Stop -FilePath $env:CPD_HV_GUEST_FILE -ArgumentList ([string]$in.arg)
Write-Doc ([ordered]@{ out = (@($out) -join "` + "`n" + `") })
`

// buildScripts is every write-side script, by method, for the parser test.
var buildScripts = map[string]string{
	"Inventory":           scriptInventory,
	"EnsureSwitch":        scriptEnsureSwitch,
	"EnsureNAT":           scriptEnsureNAT,
	"CreateVM":            scriptCreateVM,
	"SetIntegration":      scriptSetIntegration,
	"SetBindings":         scriptSetBindings,
	"EnsureFirewall":      scriptEnsureFirewall,
	"SetHostNeighbor":     scriptSetHostNeighbor,
	"EnsureGuardian":      scriptEnsureGuardian,
	"AddHyperVAdmin":      scriptAddHyperVAdmin,
	"WriteAnswerISO":      scriptWriteAnswerISO,
	"BootKeys":            scriptBootKeys,
	"EjectDVDs":           scriptEjectDVDs,
	"RegisterGuardTask":   scriptRegisterGuardTask,
	"StartGuardTask":      scriptStartGuardTask,
	"UnregisterGuardTask": scriptUnregisterGuardTask,
	"RemoveVM":            scriptRemoveVM,
	"RemoveSwitch":        scriptRemoveSwitch,
	"RemoveNAT":           scriptRemoveNAT,
	"RemoveFirewall":      scriptRemoveFirewall,
	"RemoveGuardian":      scriptRemoveGuardian,
	"ClearProof":          scriptClearProof,
	"Guest":               scriptGuest,
}

func init() {
	for k, v := range buildScripts {
		hvScripts[k] = v
	}
}

// psBuilder is the Builder over PowerShell: the read side and guard writes of
// psHost, and the scripts above.
type psBuilder struct{ *psHost }

var _ Builder = psBuilder{}

func checkOwner(owner string) error {
	if !ownerRe.MatchString(owner) {
		return errors.New("the owner token is not 32 lowercase hex digits")
	}
	return nil
}

func checkIfIndex(i int) error {
	if i <= 0 || i > 1<<24 {
		return fmt.Errorf("interface index %d is out of range", i)
	}
	return nil
}

// checkWinPath accepts an absolute Windows path with none of the characters
// that could end a quoted argument or start an expression.
func checkWinPath(what, p string) error {
	if !winPathRe.MatchString(p) || strings.Contains(p, "..") || len(p) > 240 {
		return fmt.Errorf("%s %q is not an absolute Windows path", what, p)
	}
	return nil
}

// taskParts splits \Folder\Name into the task path (\Folder\) and name.
func taskParts(task string) (string, string, error) {
	i := strings.LastIndexByte(task, '\\')
	if i < 0 || !strings.HasPrefix(task, `\`) {
		return "", "", fmt.Errorf("task %q is not a full task path", task)
	}
	path, name := task[:i+1], task[i+1:]
	if !objectName.MatchString(name) || strings.ContainsAny(path, `"'$;&|<>*?`+"`") {
		return "", "", fmt.Errorf("task %q is not allowed", task)
	}
	return path, name, nil
}

type nameArg struct {
	VM       string `json:"vm"`
	Switch   string `json:"switch"`
	NAT      string `json:"nat"`
	Group    string `json:"group"`
	TaskPath string `json:"taskPath"`
	TaskName string `json:"taskName"`
}

func namesArg(n Names) (nameArg, error) {
	if err := n.Validate(); err != nil {
		return nameArg{}, err
	}
	tp, tn, err := taskParts(n.GuardTask)
	if err != nil {
		return nameArg{}, err
	}
	return nameArg{VM: n.VM, Switch: n.Switch, NAT: n.NAT, Group: n.FirewallGroup, TaskPath: tp, TaskName: tn}, nil
}

type okWire struct {
	OK bool `json:"ok"`
}

func (h *psHost) ok(ctx context.Context, script string, arg any) error {
	var w okWire
	if err := h.call(ctx, script, arg, &w); err != nil {
		return err
	}
	if !w.OK {
		return errors.New("the script did not report success")
	}
	return nil
}

type ownedWire struct {
	ID    string   `json:"id"`
	Owner string   `json:"owner"`
	Ports []string `json:"ports"`
}

type inventoryWire struct {
	VMs          []ownedWire `json:"vms"`
	GuardianVMs  []string    `json:"guardianVms"`
	Switches     []ownedWire `json:"switches"`
	NATs         []natWire   `json:"nats"`
	Firewall     []ownedWire `json:"firewall"`
	Task         *ownedWire  `json:"task"`
	Guardian     bool        `json:"guardian"`
	GuardianID   string      `json:"guardianId"`
	HyperVAdmin  bool        `json:"hypervAdmin"`
	Enhanced     bool        `json:"enhancedSession"`
	GCS          []string    `json:"gcs"`
	HostBuild    string      `json:"hostBuild"`
	InstalledRAM uint64      `json:"installedRam"`
	FreeRAM      uint64      `json:"freeRam"`
}

func ownedFrom(w ownedWire, guid bool) (OwnedObject, error) {
	if guid && !guidRe.MatchString(w.ID) {
		return OwnedObject{}, fmt.Errorf("object ID %q is not a GUID", w.ID)
	}
	if w.Owner != "" && !ownerRe.MatchString(w.Owner) {
		return OwnedObject{}, fmt.Errorf("owner %q is not a token", w.Owner)
	}
	o := OwnedObject{ID: w.ID, Owner: w.Owner}
	for _, p := range w.Ports {
		if !guidRe.MatchString(p) {
			return OwnedObject{}, fmt.Errorf("port VM ID %q is not a GUID", p)
		}
		o.Ports = append(o.Ports, p)
	}
	return o, nil
}

// Inventory lists every object with our names, and the VM and switch with the
// known IDs, the owner token each carries, and the host facts. The guard reads
// it to find the VM of an interrupted setup.
func (h *psHost) Inventory(ctx context.Context, n Names, known KnownIDs, sid string) (Inventory, error) {
	na, err := namesArg(n)
	if err != nil {
		return Inventory{}, err
	}
	if sid != "" && !sidRe.MatchString(sid) {
		return Inventory{}, fmt.Errorf("%q is not a SID", sid)
	}
	for what, id := range map[string]string{"VM ID": known.VM, "switch ID": known.Switch} {
		if id != "" && !guidRe.MatchString(id) {
			return Inventory{}, fmt.Errorf("%s %q is not a GUID", what, id)
		}
	}
	if known.Owner != "" {
		if err := checkOwner(known.Owner); err != nil {
			return Inventory{}, err
		}
	}
	arg := struct {
		nameArg
		SID      string `json:"sid"`
		VMID     string `json:"vmId"`
		SwitchID string `json:"switchId"`
		Owner    string `json:"owner"`
	}{na, sid, known.VM, known.Switch, known.Owner}
	var w inventoryWire
	if err := h.call(ctx, scriptInventory, arg, &w); err != nil {
		return Inventory{}, err
	}
	inv := Inventory{Guardian: w.Guardian, GuardianID: w.GuardianID, HyperVAdmin: w.HyperVAdmin, Enhanced: w.Enhanced,
		HostBuild: w.HostBuild, InstalledRAM: w.InstalledRAM, FreeRAM: w.FreeRAM, NATs: []LiveNAT{}}
	if w.GCS != nil {
		inv.GCS = GCSBaseline(w.GCS)
	}
	for _, x := range w.VMs {
		o, err := ownedFrom(x, true)
		if err != nil {
			return Inventory{}, err
		}
		inv.VMs = append(inv.VMs, o)
	}
	for _, x := range w.Switches {
		o, err := ownedFrom(x, true)
		if err != nil {
			return Inventory{}, err
		}
		inv.Switches = append(inv.Switches, o)
	}
	for _, x := range w.Firewall {
		o, err := ownedFrom(x, false)
		if err != nil {
			return Inventory{}, err
		}
		inv.Firewall = append(inv.Firewall, o)
	}
	if w.Task != nil {
		o, err := ownedFrom(*w.Task, false)
		if err != nil {
			return Inventory{}, err
		}
		inv.Task = &o
	}
	for _, id := range w.GuardianVMs {
		if !guidRe.MatchString(id) {
			return Inventory{}, fmt.Errorf("VM ID %q is not a GUID", id)
		}
		inv.GuardianVMs = append(inv.GuardianVMs, id)
	}
	for _, x := range w.NATs {
		// Keep the NAT in inventory even if the provider did not return a usable prefix.
		p, _ := netip.ParsePrefix(x.Prefix)
		inv.NATs = append(inv.NATs, LiveNAT{ID: x.ID, Name: x.Name, Prefix: p})
	}
	return inv, nil
}

// EnsureSwitch creates or checks the switch and its host vNIC.
func (b psBuilder) EnsureSwitch(ctx context.Context, n Names, sub netip.Prefix, owner, vnicMAC, switchID, managementID string) (string, string, string, int, error) {
	if switchID != "" && !guidRe.MatchString(switchID) {
		return "", "", "", 0, fmt.Errorf("switch ID %q is not a GUID", switchID)
	}
	if err := n.Validate(); err != nil {
		return "", "", "", 0, err
	}
	if err := checkSubnet(sub); err != nil {
		return "", "", "", 0, err
	}
	if err := checkOwner(owner); err != nil {
		return "", "", "", 0, err
	}
	var mac string
	if switchID != "" {
		var err error
		mac, err = NormalizeMAC(vnicMAC)
		if err != nil {
			return "", "", "", 0, err
		}
	}
	arg := struct {
		Switch       string `json:"switch"`
		SwitchID     string `json:"switchId"`
		ManagementID string `json:"managementId"`
		Owner        string `json:"owner"`
		MAC          string `json:"mac"`
		Gateway      string `json:"gateway"`
		PrefixLen    int    `json:"prefixLen"`
	}{n.Switch, switchID, managementID, owner, mac, sub.Addr().Next().String(), sub.Bits()}
	var w struct {
		SwitchID     string `json:"switchId"`
		ManagementID string `json:"managementId"`
		MAC          string `json:"mac"`
		IfIndex      int    `json:"ifIndex"`
	}
	if err := b.call(ctx, scriptEnsureSwitch, arg, &w); err != nil {
		return "", "", "", 0, err
	}
	if !guidRe.MatchString(w.SwitchID) || !validNATID(w.ManagementID) || checkIfIndex(w.IfIndex) != nil {
		return "", "", "", 0, fmt.Errorf("the switch came back as %q on interface %d", w.SwitchID, w.IfIndex)
	}
	if switchID != "" && (!strings.EqualFold(switchID, w.SwitchID) || !strings.EqualFold(managementID, w.ManagementID) || w.MAC != mac) {
		return "", "", "", 0, errors.New("the switch or management adapter identity changed")
	}
	return w.SwitchID, w.ManagementID, w.MAC, w.IfIndex, nil
}

func validNATID(id string) bool {
	return id != "" && len(id) <= 260 && strings.IndexFunc(id, unicode.IsControl) < 0
}

// matchesRecordedNAT: Windows keys a NAT's InstanceID by its name and store
// ("name;0"), so no NAT has an identity apart from its name. Ownership is the
// exact name, ID and prefix Setup recorded. The first Setup records only a NAT
// it made, because it refuses any NAT it finds. A rerun adopts the NAT with
// the recorded name and prefix, and so does Remove: Windows cannot tell it
// from a NAT someone else made later with the same name and prefix.
func matchesRecordedNAT(nat, recorded LiveNAT) bool {
	return validNATID(recorded.ID) && nat.ID == recorded.ID &&
		strings.EqualFold(nat.Name, recorded.Name) && nat.Prefix.Masked() == recorded.Prefix
}

// EnsureNAT creates or reads the NAT, returning the provider's identity.
func (b psBuilder) EnsureNAT(ctx context.Context, n Names, sub netip.Prefix, knownID string) (string, error) {
	if err := n.Validate(); err != nil {
		return "", err
	}
	if err := checkSubnet(sub); err != nil {
		return "", err
	}
	var w struct {
		ID string `json:"id"`
	}
	if err := b.call(ctx, scriptEnsureNAT, map[string]string{"nat": n.NAT, "prefix": sub.String(), "id": knownID}, &w); err != nil {
		return "", err
	}
	if !validNATID(w.ID) {
		return "", errors.New("the NAT provider did not return an identity")
	}
	return w.ID, nil
}

// CreateVM creates the VM off, with one disconnected adapter.
func (b psBuilder) CreateVM(ctx context.Context, p Plan, iso, answerISO string) (string, string, OwnedDisk, []OwnedDVD, error) {
	if err := p.Names.Validate(); err != nil {
		return "", "", OwnedDisk{}, nil, err
	}
	if err := checkOwner(p.Owner); err != nil {
		return "", "", OwnedDisk{}, nil, err
	}
	for what, path := range map[string]string{"ISO": iso, "answer ISO": answerISO} {
		if err := checkWinPath(what, path); err != nil {
			return "", "", OwnedDisk{}, nil, err
		}
	}
	if _, err := NormalizeMAC(p.GuestMAC); err != nil {
		return "", "", OwnedDisk{}, nil, err
	}
	arg := map[string]any{
		"vm": p.Names.VM, "owner": p.Owner, "dataDir": p.Names.DataDir, "memory": p.MemoryBytes, "diskBytes": uint64(guestDiskBytes),
		"procCount": p.ProcCount, "procMax": p.ProcMax, "procWeight": p.ProcWeight, "mac": p.GuestMAC,
		"iso": iso, "answer": answerISO,
	}
	var w struct {
		VMID      string     `json:"vmId"`
		AdapterID string     `json:"adapterId"`
		Disk      OwnedDisk  `json:"disk"`
		DVDs      []OwnedDVD `json:"dvds"`
	}
	if err := b.call(ctx, scriptCreateVM, arg, &w); err != nil {
		return "", "", OwnedDisk{}, nil, err
	}
	if err := w.Disk.validate(); err != nil {
		return "", "", OwnedDisk{}, nil, err
	}
	return w.VMID, w.AdapterID, w.Disk, w.DVDs, nil
}

// SetIntegration sets integration services by ID.
func (b psBuilder) SetIntegration(ctx context.Context, vmID, owner string, want map[string]bool) error {
	if err := checkVMID("VM ID", vmID); err != nil {
		return err
	}
	if err := checkOwner(owner); err != nil {
		return err
	}
	ids := map[string]bool{}
	for id, on := range want {
		if !guidRe.MatchString(id) {
			return fmt.Errorf("integration service %q is not a GUID", id)
		}
		ids[strings.ToUpper(id)] = on
	}
	return b.ok(ctx, scriptSetIntegration, map[string]any{"vm": vmID, "owner": owner, "want": ids})
}

// SetBindings unbinds components from the host vNIC. Only the planned
// components may go.
func (b psBuilder) SetBindings(ctx context.Context, p Plan, ifIndex int) (map[string]bool, error) {
	if err := checkVMID("switch ID", p.SwitchID); err != nil {
		return nil, err
	}
	if err := checkOwner(p.Owner); err != nil {
		return nil, err
	}
	if err := checkIfIndex(ifIndex); err != nil {
		return nil, err
	}
	for _, c := range p.BindingsOff {
		if !slices.Contains(BindingsOff(), c) {
			return nil, fmt.Errorf("component %q is not one setup unbinds", c)
		}
	}
	var w struct {
		Bindings map[string]bool `json:"bindings"`
	}
	err := b.call(ctx, scriptSetBindings, map[string]any{
		"ifIndex": ifIndex, "switchId": p.SwitchID, "managementId": p.ManagementAdapterID,
		"owner": p.Owner, "off": p.BindingsOff, "expected": p.ManagementBindings,
	}, &w)
	if err == nil && len(w.Bindings) == 0 {
		err = errors.New("the management adapter did not return its bindings")
	}
	return w.Bindings, err
}

// EnsureFirewall makes or verifies the recorded inbound Block rule.
func (b psBuilder) EnsureFirewall(ctx context.Context, p Plan, ifIndex int) (string, error) {
	if err := p.Names.Validate(); err != nil {
		return "", err
	}
	if err := checkVMID("switch ID", p.SwitchID); err != nil {
		return "", err
	}
	if err := checkOwner(p.Owner); err != nil {
		return "", err
	}
	if err := checkIfIndex(ifIndex); err != nil {
		return "", err
	}
	var w struct {
		Identity string `json:"identity"`
	}
	err := b.call(ctx, scriptEnsureFirewall, map[string]any{
		"group": p.Names.FirewallGroup, "id": blockFirewallID(p.Names, p.Owner), "ifIndex": ifIndex,
		"switchId": p.SwitchID, "managementId": p.ManagementAdapterID, "owner": p.Owner, "identity": p.FirewallIdentity,
	}, &w)
	if err == nil && !identityDigestRE.MatchString(w.Identity) {
		err = errors.New("the firewall did not return a rule identity")
	}
	return w.Identity, err
}

// SetHostNeighbor gives the host its static entry for the guest.
func (b psBuilder) SetHostNeighbor(ctx context.Context, ifIndex int, switchID, owner, managementID string, ip netip.Addr, mac string) error {
	if err := checkVMID("switch ID", switchID); err != nil {
		return err
	}
	if err := checkOwner(owner); err != nil {
		return err
	}
	if err := checkIfIndex(ifIndex); err != nil {
		return err
	}
	if !ip.Is4() || !ip.IsPrivate() {
		return fmt.Errorf("guest address %v is not a private IPv4 address", ip)
	}
	m, err := NormalizeMAC(mac)
	if err != nil {
		return err
	}
	return b.ok(ctx, scriptSetHostNeighbor, map[string]any{"ifIndex": ifIndex, "switchId": switchID, "managementId": managementID, "owner": owner, "ip": ip.String(), "mac": macDash(m)})
}

// EnsureGuardian returns the fingerprint only when this call created it.
func (b psBuilder) EnsureGuardian(ctx context.Context) (string, error) {
	var w struct {
		ID string `json:"id"`
	}
	err := b.call(ctx, scriptEnsureGuardian, map[string]string{}, &w)
	if err == nil && w.ID != "" && !identityDigestRE.MatchString(w.ID) {
		return "", errors.New("the guardian fingerprint is invalid")
	}
	return w.ID, err
}

// AddHyperVAdmin adds the account to Hyper-V Administrators.
func (b psBuilder) AddHyperVAdmin(ctx context.Context, sid string) (bool, error) {
	if !sidRe.MatchString(sid) {
		return false, fmt.Errorf("%q is not a SID", sid)
	}
	var w struct {
		Added bool `json:"added"`
	}
	err := b.call(ctx, scriptAddHyperVAdmin, map[string]string{"sid": sid}, &w)
	return w.Added, err
}

// maxAnswerXML caps the answer file: its base64 must fit the argument cap.
const maxAnswerXML = 40 << 10

// WriteAnswerISO writes the answer file onto a new ISO.
func (b psBuilder) WriteAnswerISO(ctx context.Context, path string, xml []byte) error {
	if err := checkWinPath("answer ISO", path); err != nil {
		return err
	}
	if len(xml) == 0 || len(xml) > maxAnswerXML {
		return fmt.Errorf("the answer file is %d bytes; it must be 1 to %d", len(xml), maxAnswerXML)
	}
	return b.ok(ctx, scriptWriteAnswerISO, map[string]string{"path": path, "xml": base64.StdEncoding.EncodeToString(xml)})
}

// BootKeys starts the VM and presses Enter every half second for 20 seconds.
func (b psBuilder) BootKeys(ctx context.Context, vmID, owner string) error {
	if err := checkVMID("VM ID", vmID); err != nil {
		return err
	}
	if err := checkOwner(owner); err != nil {
		return err
	}
	return b.ok(ctx, scriptBootKeys, map[string]string{"vm": vmID, "owner": owner})
}

// EjectDVDs removes every DVD drive.
func (b psBuilder) EjectDVDs(ctx context.Context, vmID, owner string, dvds []OwnedDVD) error {
	if err := checkVMID("VM ID", vmID); err != nil {
		return err
	}
	if err := checkOwner(owner); err != nil {
		return err
	}
	var w struct {
		Left int `json:"left"`
	}
	if err := b.call(ctx, scriptEjectDVDs, map[string]any{"vm": vmID, "owner": owner, "dvds": dvds}, &w); err != nil {
		return err
	}
	if w.Left != 0 {
		return fmt.Errorf("%d unverified DVD drives were left on the VM", w.Left)
	}
	return nil
}

func taskArg(n Names, owner string) (map[string]any, error) {
	if err := n.Validate(); err != nil {
		return nil, err
	}
	tp, tn, err := taskParts(n.GuardTask)
	if err != nil {
		return nil, err
	}
	a := map[string]any{"taskPath": tp, "taskName": tn}
	if owner != "" {
		if err := checkOwner(owner); err != nil {
			return nil, err
		}
		a["owner"] = owner
	}
	return a, nil
}

// RegisterGuardTask registers the SYSTEM guard task, after stopping a running
// one. Its 5-minute trigger starts ProofLockTTL from now, so a guard the task
// starts never runs its boot routine in the middle of the proof.
func (b psBuilder) RegisterGuardTask(ctx context.Context, n Names, exe, owner string) error {
	if err := checkOwner(owner); err != nil {
		return err
	}
	if err := checkWinPath("guard exe", exe); err != nil {
		return err
	}
	if err := checkWinPath("data folder", n.DataDir); err != nil {
		return err
	}
	a, err := taskArg(n, owner)
	if err != nil {
		return err
	}
	a["exe"], a["dir"], a["delay"] = exe, n.DataDir, int(ProofLockTTL/time.Second)
	return b.ok(ctx, scriptRegisterGuardTask, a)
}

// StartGuardTask starts the guard task.
func (b psBuilder) StartGuardTask(ctx context.Context, n Names, owner string) error {
	if err := checkOwner(owner); err != nil {
		return err
	}
	a, err := taskArg(n, owner)
	if err != nil {
		return err
	}
	return b.ok(ctx, scriptStartGuardTask, a)
}

// jsonString is v as a JSON document.
func jsonString(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	if len(b) > maxHVArg/2 {
		return "", fmt.Errorf("guest argument is %d bytes, over the cap of %d", len(b), maxHVArg/2)
	}
	return string(b), nil
}

// maxGuestOut caps what a guest script may print.
const maxGuestOut = 64 << 10

// Guest runs one fixed guest script over PowerShell Direct.
func (b psBuilder) Guest(ctx context.Context, vmID, owner string, cred Cred, s GuestScript, arg, out any) error {
	if err := checkVMID("VM ID", vmID); err != nil {
		return err
	}
	if err := checkOwner(owner); err != nil {
		return err
	}
	if fixed, ok := guestScriptNamed(s.Name); !ok || s != fixed {
		return fmt.Errorf("guest script %q is not one this package defines", s.Name)
	}
	if cred.User != guestUser {
		return fmt.Errorf("guest account %q is not %s", cred.User, guestUser)
	}
	if err := checkPassword(cred.User, cred.Password); err != nil {
		return err
	}
	a, err := jsonString(arg)
	if err != nil {
		return err
	}
	var w struct {
		Out string `json:"out"`
	}
	if err := b.call(ctx, scriptGuest, map[string]string{"vm": vmID, "owner": owner, "user": cred.User, "password": cred.Password, "arg": a, "guest": s.Name}, &w); err != nil {
		return err
	}
	if len(w.Out) > maxGuestOut {
		return fmt.Errorf("the guest printed %d bytes, over the cap of %d", len(w.Out), maxGuestOut)
	}
	return decodeDoc([]byte(strings.TrimSpace(w.Out)), out)
}

// UnregisterGuardTask stops and deletes the guard task when it is ours.
func (b psBuilder) UnregisterGuardTask(ctx context.Context, n Names, owner string) error {
	if err := checkOwner(owner); err != nil {
		return err
	}
	a, err := taskArg(n, owner)
	if err != nil {
		return err
	}
	return b.ok(ctx, scriptUnregisterGuardTask, a)
}

// RemoveVM removes the VM and the exact disk setup recorded.
func (b psBuilder) RemoveVM(ctx context.Context, vmID string, p Plan) ([]string, error) {
	if vmID != "" {
		if err := checkVMID("VM ID", vmID); err != nil {
			return nil, err
		}
	}
	if err := checkOwner(p.Owner); err != nil {
		return nil, err
	}
	if p.Disk != nil {
		if err := p.Disk.validate(); err != nil {
			return nil, err
		}
	}
	var w struct {
		Left []string `json:"left"`
	}
	err := b.call(ctx, scriptRemoveVM, map[string]any{"vm": vmID, "owner": p.Owner, "disk": p.Disk, "adapter": p.AdapterID, "aclDigest": portACLDigest(p.PortACLs)}, &w)
	return w.Left, err
}

// RemoveSwitch removes the recorded switch only when its port is unused.
func (b psBuilder) RemoveSwitch(ctx context.Context, p Plan) (string, error) {
	if err := checkVMID("switch ID", p.SwitchID); err != nil {
		return "", err
	}
	if err := checkOwner(p.Owner); err != nil {
		return "", err
	}
	var w struct {
		Kept string `json:"kept"`
	}
	err := b.call(ctx, scriptRemoveSwitch, map[string]any{
		"switchId": p.SwitchID, "owner": p.Owner, "managementId": p.ManagementAdapterID,
		"mac": p.HostVNICMAC, "gateway": p.Gateway.String(), "prefixLen": p.Subnet.Bits(),
		"guest": p.GuestIP.String(), "guestMac": macDash(p.GuestMAC), "bindings": p.ManagementBindings,
		"broadcast": subnetBroadcast(p.Subnet).String(),
	}, &w)
	return w.Kept, err
}

// subnetBroadcast is the last address of an IPv4 prefix.
func subnetBroadcast(p netip.Prefix) netip.Addr {
	a := p.Masked().Addr().As4()
	host := uint32(1)<<(32-p.Bits()) - 1
	v := uint32(a[0])<<24 | uint32(a[1])<<16 | uint32(a[2])<<8 | uint32(a[3]) | host
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}

// RemoveNAT deletes only the NAT Setup recorded.
func (b psBuilder) RemoveNAT(ctx context.Context, n Names, sub netip.Prefix, id string) (string, error) {
	if err := n.Validate(); err != nil {
		return "", err
	}
	if err := checkSubnet(sub); err != nil {
		return "", err
	}
	if !validNATID(id) {
		return "", errors.New("the NAT has no recorded provider identity")
	}
	var w struct {
		Kept string `json:"kept"`
	}
	err := b.call(ctx, scriptRemoveNAT, map[string]string{"nat": n.NAT, "prefix": sub.String(), "id": id}, &w)
	return w.Kept, err
}

// RemoveFirewall removes only the exact rule Setup recorded.
func (b psBuilder) RemoveFirewall(ctx context.Context, p Plan) (string, error) {
	if err := p.Names.Validate(); err != nil {
		return "", err
	}
	if err := checkOwner(p.Owner); err != nil {
		return "", err
	}
	var w struct {
		Kept string `json:"kept"`
	}
	err := b.call(ctx, scriptRemoveFirewall, map[string]string{
		"group": p.Names.FirewallGroup, "id": blockFirewallID(p.Names, p.Owner), "owner": p.Owner, "identity": p.FirewallIdentity,
		// Setup's rule names the host vNIC, which Hyper-V calls "vEthernet (<switch>)".
		"alias": "vEthernet (" + p.Names.Switch + ")",
	}, &w)
	return w.Kept, err
}

// RemoveGuardian deletes the guardian.
func (b psBuilder) RemoveGuardian(ctx context.Context, id string) error {
	if !identityDigestRE.MatchString(id) {
		return errors.New("the guardian has no recorded fingerprint")
	}
	return b.ok(ctx, scriptRemoveGuardian, map[string]string{"id": id})
}

// ClearProof reports earlier proof artifacts that have no live identity.
func (b psBuilder) ClearProof(ctx context.Context, n Names, owner string) (ProofCleanup, error) {
	if err := n.Validate(); err != nil {
		return ProofCleanup{}, err
	}
	var w ProofCleanup
	arg := map[string]string{"group": n.FirewallGroup + proofGroupSuffix, "elementPrefix": "CashPilot Secure VM proof " + n.VM + "."}
	err := b.call(ctx, scriptClearProof, arg, &w)
	return w, err
}
