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
)

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
var guardianIDRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// hvAdmins is the Hyper-V Administrators group, by SID: its name is
// translated.
const hvAdmins = "S-1-5-32-578"

// buildPrelude adds the helpers the build scripts share to hvPrelude.
const buildPrelude = hvPrelude + `function Get-AdapterByMac([string]$mac) {
  @(Get-NetAdapter -IncludeHidden | Where-Object { (([string]$_.MacAddress) -replace '-', '').ToUpper() -eq $mac })
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
function Get-GuardianIdentity($g) {
  if (-not $g.SigningCertificate -or -not $g.EncryptionCertificate) { return '' }
  $pair = [Convert]::ToBase64String([byte[]]$g.SigningCertificate) + ':' + [Convert]::ToBase64String([byte[]]$g.EncryptionCertificate)
  $sha = [Security.Cryptography.SHA256]::Create()
  try { return [BitConverter]::ToString($sha.ComputeHash([Text.Encoding]::ASCII.GetBytes($pair))).Replace('-', '').ToLowerInvariant() }
  finally { $sha.Dispose() }
}
`

// scriptInventory lists every object with one of our names, and the VM and the
// switch with the recorded IDs whatever their names are now, the owner token
// each carries, and the host facts setup and Remove VM check.
const scriptInventory = buildPrelude + `$o = [ordered]@{}
$o.vms = @(Get-VM | Where-Object { ($_.Name -eq $in.vm) -or ($in.vmId -and ($_.Id.ToString() -eq $in.vmId)) } | ForEach-Object { [ordered]@{ id = $_.Id.ToString(); owner = (Get-Owner ([string]$_.Notes)) } })
$o.guardianVms = @(Get-VM | Where-Object { Test-GuardianUser $_ } | ForEach-Object { $_.Id.ToString() })
$o.switches = @(Get-VMSwitch | Where-Object { ($_.Name -eq $in.switch) -or ($in.switchId -and ($_.Id.ToString() -eq $in.switchId)) } | ForEach-Object {
  $sw = $_
  $ports = @(Get-VMNetworkAdapter -All | Where-Object { (-not $_.IsManagementOs) -and ($_.SwitchId -eq $sw.Id) } | ForEach-Object { $_.VMId.ToString() } | Select-Object -Unique)
  [ordered]@{ id = $sw.Id.ToString(); owner = (Get-Owner ([string]$sw.Notes)); ports = @($ports) }
})
$o.nats = @(Get-NetNat -ErrorAction SilentlyContinue | ForEach-Object { [ordered]@{ id = [string]$_.InstanceID; name = [string]$_.Name; prefix = [string]$_.InternalIPInterfaceAddressPrefix } })
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

// scriptEnsureSwitch takes the switch with the recorded ID (renamed or not),
// else the one with our name when it carries the tag, else creates the
// Internal switch with the tag in the same call, so no switch of ours is ever
// left without it. It gives the host vNIC its static MAC and the gateway
// address, and returns the switch ID and the vNIC's ifIndex.
const scriptEnsureSwitch = buildPrelude + `$sw = @()
if ($in.switchId) { $sw = @(Get-VMSwitch | Where-Object { $_.Id.ToString() -eq $in.switchId }) }
if (-not $in.switchId) {
  $sw = @(Get-VMSwitch | Where-Object { $_.Name -eq $in.switch })
  if ($sw.Count -gt 1) { throw 'More than one switch has this name.' }
  if ($sw.Count -eq 1 -and (Get-Owner ([string]$sw[0].Notes)) -ne $in.owner) { throw 'A switch with this name exists and was not made by this install.' }
}
if ($sw.Count -eq 1) {
  $sw = $sw[0]
  if ((Get-Owner ([string]$sw.Notes)) -cne $in.owner) { throw 'The switch was not made by this install.' }
  if ($sw.Name -ne $in.switch) {
    if (@(Get-VMSwitch | Where-Object { $_.Name -eq $in.switch }).Count -gt 0) { throw 'Another switch has this name.' }
    Rename-VMSwitch -VMSwitch $sw -NewName $in.switch
  }
} else {
  if ($in.switchId) { throw 'The recorded switch was not found.' }
  $sw = New-VMSwitch -Name $in.switch -SwitchType Internal -Notes ('CashPilot Secure VM. cashpilot-owner=' + $in.owner)
}
$mo = @(Get-VMNetworkAdapter -ManagementOS | Where-Object { $_.SwitchId -eq $sw.Id })
if ($mo.Count -ne 1) { throw 'The switch does not have exactly one host vNIC.' }
if ((([string]$mo[0].MacAddress) -replace '-', '').ToUpper() -ne $in.mac) {
  Set-VMNetworkAdapter -VMNetworkAdapter $mo[0] -StaticMacAddress $in.mac
}
$na = @()
for ($i = 0; $i -lt 30 -and $na.Count -ne 1; $i++) {
  $na = @(Get-AdapterByMac $in.mac)
  if ($na.Count -ne 1) { Start-Sleep -Seconds 1 }
}
if ($na.Count -ne 1) { throw 'The host vNIC did not appear with its MAC.' }
$idx = [int]$na[0].ifIndex
$have = @(Get-NetIPAddress -InterfaceIndex $idx -AddressFamily IPv4 -ErrorAction SilentlyContinue)
if (-not ($have | Where-Object { ([string]$_.IPAddress -eq $in.gateway) -and ([int]$_.PrefixLength -eq [int]$in.prefixLen) })) {
  $have | Remove-NetIPAddress -Confirm:$false -ErrorAction SilentlyContinue
  New-NetIPAddress -InterfaceIndex $idx -IPAddress $in.gateway -PrefixLength ([int]$in.prefixLen) | Out-Null
}
Write-Doc ([ordered]@{ switchId = $sw.Id.ToString(); ifIndex = $idx })
`

// scriptEnsureNAT returns the provider identity of the new or recorded NAT.
const scriptEnsureNAT = buildPrelude + `$n = @(Get-NetNat -ErrorAction SilentlyContinue | Where-Object { $_.Name -eq $in.nat })
if ($n.Count -gt 0) {
  if ($n.Count -ne 1 -or -not $in.id -or [string]$n[0].InstanceID -cne $in.id -or [string]$n[0].InternalIPInterfaceAddressPrefix -ne $in.prefix) {
    throw 'A NAT with this name exists and its identity does not match the journal.'
  }
  $nat = $n[0]
} else {
  $nat = New-NetNat -Name $in.nat -InternalIPInterfaceAddressPrefix $in.prefix
}
Write-Doc ([ordered]@{ id = [string]$nat.InstanceID })
`

// scriptCreateVM creates the VM off, with one disconnected adapter: only the
// proof and the guard ever connect it. Gen 2, Secure Boot, a vTPM with a local
// key protector, static memory, the plan's processor shape, no checkpoints,
// no automatic start, the owner tag in its Notes, and the two ISOs.
const scriptCreateVM = buildPrelude + diskIdentityPrelude + `if (@(Get-VM | Where-Object { $_.Name -eq $in.vm }).Count -gt 0) { throw 'A VM with this name exists.' }
$disk = [IO.Path]::GetFullPath((Join-Path $in.vhdxDir ($in.vm + '.vhdx')))
for ($dir = [IO.DirectoryInfo]::new($in.vhdxDir); $dir; $dir = $dir.Parent) {
  if ($dir.Exists -and ($dir.Attributes -band [IO.FileAttributes]::ReparsePoint)) { throw ('A junction in the VM disk path: ' + $dir.FullName) }
}
if (Test-Path -LiteralPath $disk) { throw 'The VM disk already exists.' }
New-Item -ItemType Directory -Force -Path $in.vhdxDir | Out-Null
$vm = $null
$createdDisk = New-VHD -Path $disk -Dynamic -SizeBytes ([uint64]$in.diskBytes)
if (-not $createdDisk -or [IO.Path]::GetFullPath($createdDisk.Path) -ne $disk) { throw 'The disk creation did not return the requested file.' }
$diskID = [CpdDisk]::Identify($disk)
if (-not $diskID) { throw 'Could not verify the created disk.' }
try {
  $vm = New-VM -Name $in.vm -Generation 2 -MemoryStartupBytes ([uint64]$in.memory) -VHDPath $disk -Path $in.vhdxDir
  # The owner tag first, in its own call: it is what makes a rerun adopt the VM.
  Set-VM -VM $vm -Notes ('CashPilot Secure VM. cashpilot-owner=' + $in.owner)
  Set-VM -VM $vm -ProcessorCount ([int]$in.procCount) -StaticMemory -CheckpointType Disabled -AutomaticStartAction Nothing -AutomaticStopAction ShutDown
  Set-VMProcessor -VM $vm -Maximum ([int]$in.procMax) -RelativeWeight ([int]$in.procWeight) -ExposeVirtualizationExtensions $false
  $a = @(Get-VMNetworkAdapter -VM $vm)
  if ($a.Count -ne 1) { throw 'The new VM does not have exactly one network adapter.' }
  if ($a[0].SwitchName) { Disconnect-VMNetworkAdapter -VMNetworkAdapter $a[0] }
  Set-VMNetworkAdapter -VMNetworkAdapter $a[0] -StaticMacAddress $in.mac -DhcpGuard On -RouterGuard On -MacAddressSpoofing Off -DeviceNaming Off
  Set-VMFirmware -VM $vm -EnableSecureBoot On -SecureBootTemplate MicrosoftWindows
  Set-VMKeyProtector -VM $vm -NewLocalKeyProtector
  Enable-VMTPM -VM $vm
  Add-VMDvdDrive -VM $vm -Path $in.iso
  Add-VMDvdDrive -VM $vm -Path $in.answer
  $dvd = @(Get-VMDvdDrive -VM $vm | Where-Object { $_.Path -eq $in.iso })
  Set-VMFirmware -VM $vm -FirstBootDevice $dvd[0]
} catch {
  if ($vm) {
    Stop-VM -VM $vm -TurnOff -Force -ErrorAction SilentlyContinue
    Remove-VM -VM $vm -Force -ErrorAction SilentlyContinue
    if (-not (Test-DiskInUse $disk)) { [CpdDisk]::DeleteOwned($disk, $diskID) | Out-Null }
  }
  throw
}
Write-Doc ([ordered]@{ vmId = $vm.Id.ToString(); adapterId = ($a[0].Id -split '\\')[-1]; disk = @{ path = $disk; id = $diskID } })
`

// scriptSetIntegration sets integration services by ID.
const scriptSetIntegration = buildPrelude + `$vm = @(Find-OurVM $in.vm)
if ($vm.Count -ne 1) { throw 'The VM was not found.' }
foreach ($p in $in.want.PSObject.Properties) {
  $s = @(Get-VMIntegrationService -VM $vm[0] | Where-Object { (($_.Id -split '\\')[-1]).ToUpper() -eq $p.Name })
  foreach ($x in $s) { if ($p.Value) { Enable-VMIntegrationService -VMIntegrationService $x } else { Disable-VMIntegrationService -VMIntegrationService $x } }
}
Write-Doc ([ordered]@{ ok = $true })
`

// scriptSetBindings unbinds components from the host vNIC by component ID.
const scriptSetBindings = buildPrelude + `$na = @(Get-NetAdapter -IncludeHidden | Where-Object { $_.ifIndex -eq [int]$in.ifIndex })
if ($na.Count -ne 1) { throw 'The host vNIC was not found.' }
foreach ($c in @($in.off)) {
  $b = @(Get-NetAdapterBinding -Name $na[0].Name -IncludeHidden -AllBindings | Where-Object { ([string]$_.ComponentID).ToLower() -eq $c })
  foreach ($x in $b) { if ($x.Enabled) { Disable-NetAdapterBinding -Name $na[0].Name -ComponentID $x.ComponentID -IncludeHidden -AllBindings } }
}
Write-Doc ([ordered]@{ ok = $true })
`

// scriptEnsureFirewall makes the group's one rule: inbound, block, on the host
// vNIC, with the owner tag. It refuses a group with a rule that does not carry
// the tag.
const scriptEnsureFirewall = buildPrelude + `$na = @(Get-NetAdapter -IncludeHidden | Where-Object { $_.ifIndex -eq [int]$in.ifIndex })
if ($na.Count -ne 1) { throw 'The host vNIC was not found.' }
$rs = @(Get-NetFirewallRule -Group $in.group -ErrorAction SilentlyContinue)
foreach ($r in $rs) { if ((Get-Owner ([string]$r.Description)) -ne $in.owner) { throw 'A firewall rule in this group was not made by this install.' } }
$rs | Remove-NetFirewallRule -ErrorAction SilentlyContinue
New-NetFirewallRule -Name ($in.group + '-block-in') -DisplayName ($in.group + ' block inbound') -Group $in.group -Direction Inbound -Action Block -InterfaceAlias $na[0].Name -Enabled True -Description ('CashPilot Secure VM. cashpilot-owner=' + $in.owner) | Out-Null
Write-Doc ([ordered]@{ ok = $true })
`

// scriptSetHostNeighbor gives the host one static entry for the guest on the
// vNIC, in place of any other static entry there.
const scriptSetHostNeighbor = buildPrelude + `Get-NetNeighbor -InterfaceIndex ([int]$in.ifIndex) -AddressFamily IPv4 -ErrorAction SilentlyContinue | Where-Object { $_.State.ToString() -eq 'Permanent' } | Remove-NetNeighbor -Confirm:$false
New-NetNeighbor -InterfaceIndex ([int]$in.ifIndex) -IPAddress $in.ip -LinkLayerAddress $in.mac -State Permanent | Out-Null
Write-Doc ([ordered]@{ ok = $true })
`

// scriptSetEnhancedSession sets the host-wide setting and returns the old
// value.
const scriptSetEnhancedSession = buildPrelude + `$was = [bool](Get-VMHost).EnableEnhancedSessionMode
if ($was -ne [bool]$in.on) { Set-VMHost -EnableEnhancedSessionMode ([bool]$in.on) }
Write-Doc ([ordered]@{ was = $was })
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
const scriptWriteAnswerISO = buildPrelude + `if (-not ('CpdIso' -as [type])) { Add-Type -TypeDefinition @'
using System; using System.IO; using System.Runtime.InteropServices; using System.Runtime.InteropServices.ComTypes;
public static class CpdIso { public static void Write(object stream, string path) {
  IStream s = (IStream)stream; using (var f = File.Create(path)) { byte[] buf = new byte[65536]; IntPtr p = Marshal.AllocHGlobal(4);
  try { while (true) { s.Read(buf, buf.Length, p); int n = Marshal.ReadInt32(p); if (n <= 0) break; f.Write(buf, 0, n); } } finally { Marshal.FreeHGlobal(p); } } } }
'@ }
$stage = $in.path + '.stage'
$answer = Join-Path $stage 'autounattend.xml'
if (Test-Path -LiteralPath $stage) {
  $d = Get-Item -LiteralPath $stage -Force
  if (-not $d.PSIsContainer -or ($d.Attributes -band [IO.FileAttributes]::ReparsePoint)) { throw 'The answer folder is not a plain directory.' }
  foreach ($f in @(Get-ChildItem -LiteralPath $stage -Force)) {
    if ($f.Name -cne 'autounattend.xml' -or $f.PSIsContainer -or ($f.Attributes -band [IO.FileAttributes]::ReparsePoint)) {
      throw ('Unknown file in the answer folder: ' + $f.FullName)
    }
  }
} else { New-Item -ItemType Directory -Path $stage | Out-Null }
try {
  [IO.File]::WriteAllBytes($answer, [Convert]::FromBase64String($in.xml))
  $fs = New-Object -ComObject IMAPI2FS.MsftFileSystemImage
  $fs.FileSystemsToCreate = 3
  $fs.VolumeName = 'CPDANSWER'
  $fs.Root.AddTree($stage, $false)
  $tmp = $in.path + '.tmp'
  if (Test-Path -LiteralPath $tmp) {
    $f = Get-Item -LiteralPath $tmp -Force
    if ($f.PSIsContainer -or ($f.Attributes -band [IO.FileAttributes]::ReparsePoint)) { throw 'The temporary answer ISO is not a plain file.' }
  }
  [CpdIso]::Write($fs.CreateResultImage().ImageStream, $tmp)
  Move-Item -LiteralPath $tmp -Destination $in.path -Force
} finally {
  Remove-Item -LiteralPath $answer -Force -ErrorAction SilentlyContinue
  if (@(Get-ChildItem -LiteralPath $stage -Force).Count -eq 0) { [IO.Directory]::Delete($stage) }
}
Write-Doc ([ordered]@{ ok = $true })
`

// scriptBootKeys presses Enter once a second for 10 seconds through the VM's
// synthetic keyboard, for the "press any key to boot from CD" prompt.
const scriptBootKeys = buildPrelude + `$cs = Get-CimInstance -Namespace root\virtualization\v2 -ClassName Msvm_ComputerSystem -Filter ("Name='" + $in.vm + "'")
if (-not $cs) { throw 'The VM was not found.' }
$kb = Get-CimAssociatedInstance -InputObject $cs -ResultClassName Msvm_Keyboard
for ($i = 0; $i -lt 10; $i++) { try { Invoke-CimMethod -InputObject $kb -MethodName TypeKey -Arguments @{ keyCode = [uint32]13 } | Out-Null } catch { }; Start-Sleep -Seconds 1 }
Write-Doc ([ordered]@{ ok = $true })
`

// scriptEjectDVDs removes every DVD drive of the VM.
const scriptEjectDVDs = buildPrelude + `$vm = @(Find-OurVM $in.vm)
if ($vm.Count -ne 1) { throw 'The VM was not found.' }
Get-VMDvdDrive -VM $vm[0] | Remove-VMDvdDrive
Write-Doc ([ordered]@{ left = @(Get-VMDvdDrive -VM $vm[0]).Count })
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
Register-ScheduledTask -TaskPath $in.taskPath -TaskName $in.taskName -Action $action -Trigger @($boot, $rep, $resume) -Settings $set -Principal $pr -Description ('CashPilot Secure VM guard. cashpilot-owner=' + $in.owner) -Force | Out-Null
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

// scriptUnregisterGuardTask stops and deletes the guard task when it carries
// the owner tag, then deletes its folder when nothing else is in it.
const scriptUnregisterGuardTask = buildPrelude + `$t = @(Get-ScheduledTask -TaskPath $in.taskPath -TaskName $in.taskName -ErrorAction SilentlyContinue)
if ($t.Count -gt 1) { throw 'More than one scheduled task matched.' }
if ($t.Count -eq 1) {
  if ($t[0].Description -cne ('CashPilot Secure VM guard. cashpilot-owner=' + $in.owner)) { throw 'The scheduled task was not made by this install.' }
  Stop-ScheduledTask -InputObject $t[0] -ErrorAction SilentlyContinue | Out-Null
  Unregister-ScheduledTask -InputObject $t[0] -Confirm:$false
}
Write-Doc ([ordered]@{ ok = $true })
`

// scriptRemoveVM removes the owned VM and the recorded disk file. Any
// other attached VHDX is reported and kept.
const scriptRemoveVM = buildPrelude + diskIdentityPrelude + `$vm = @()
if ($in.vm) { $vm = @(Find-OurVM $in.vm) }
if ($vm.Count -gt 1) { throw 'More than one VM has the recorded ID.' }
$disks = @()
if ($vm.Count -eq 1) {
  $vm = $vm[0]
  if ((Get-Owner ([string]$vm.Notes)) -cne $in.owner) { throw 'The VM was not made by this install.' }
  $disks = @(Get-VMHardDiskDrive -VM $vm | Where-Object { $_.Path } | ForEach-Object { [string]$_.Path })
  if ($vm.State.ToString() -ne 'Off') { Stop-VM -VM $vm -TurnOff -Force }
  Remove-VM -VM $vm -Force
}
$left = @()
if ($in.disk) {
  $path = $in.disk.path
  if (-not (Test-DiskInUse $path) -and [CpdDisk]::DeleteOwned($path, $in.disk.id)) {
    $disks = @($disks | Where-Object { $_ -ne $path })
  } elseif (Test-Path -LiteralPath $path) { $disks += $path }
}
$left = @($disks | Select-Object -Unique)
Write-Doc ([ordered]@{ left = @($left) })
`

// scriptRemoveSwitch removes the switch by ID, with its host vNIC. It refuses
// while a VM has an adapter on it.
const scriptRemoveSwitch = buildPrelude + `$sw = @(Get-VMSwitch | Where-Object { $_.Id.ToString() -eq $in.switchId })
if ($sw.Count -eq 1) {
  if ((Get-Owner ([string]$sw[0].Notes)) -cne $in.owner) { throw 'The switch was not made by this install.' }
  if (@(Get-VMNetworkAdapter -All | Where-Object { (-not $_.IsManagementOs) -and ($_.SwitchId -eq $sw[0].Id) }).Count -gt 0) { throw 'A VM still has an adapter on the switch.' }
  Remove-VMSwitch -VMSwitch $sw[0] -Force
}
Write-Doc ([ordered]@{ ok = $true })
`

// scriptRemoveNAT removes the exact recorded provider object.
const scriptRemoveNAT = buildPrelude + `$n = @(Get-NetNat -ErrorAction SilentlyContinue | Where-Object { $_.Name -eq $in.nat })
if ($n.Count -gt 0) {
  if ($n.Count -ne 1 -or [string]$n[0].InstanceID -cne $in.id -or [string]$n[0].InternalIPInterfaceAddressPrefix -ne $in.prefix) {
    throw 'The NAT changed since inventory.'
  }
  Remove-NetNat -InputObject $n[0] -Confirm:$false
}
Write-Doc ([ordered]@{ ok = $true })
`

// scriptRemoveFirewall removes the rules of the group that carry the owner
// tag.
const scriptRemoveFirewall = buildPrelude + `@(Get-NetFirewallRule -Group $in.group -ErrorAction SilentlyContinue | Where-Object { (Get-Owner ([string]$_.Description)) -eq $in.owner }) | Remove-NetFirewallRule
Write-Doc ([ordered]@{ ok = $true })
`

// scriptRemoveGuardian rechecks the fingerprint and live VM dependencies.
const scriptRemoveGuardian = buildPrelude + `$g = @(Get-HgsGuardian -Name 'UntrustedGuardian' -ErrorAction SilentlyContinue)
if ($g.Count -gt 0) {
  if ($g.Count -ne 1 -or (Get-GuardianIdentity $g[0]) -cne $in.id) { throw 'The guardian changed since inventory.' }
  if (@(Get-VM | Where-Object { Test-GuardianUser $_ }).Count -gt 0) { throw 'A VM still needs the guardian.' }
  Remove-HgsGuardian -InputObject $g[0] -Confirm:$false
}
Write-Doc ([ordered]@{ ok = $true })
`

// scriptRemoveHyperVAdmin takes the account out of Hyper-V Administrators.
const scriptRemoveHyperVAdmin = buildPrelude + `if ((Get-GroupSids 'S-1-5-32-578') -contains $in.sid) { Remove-LocalGroupMember -SID 'S-1-5-32-578' -Member $in.sid }
Write-Doc ([ordered]@{ ok = $true })
`

// scriptClearProof matches the owner tag on rules and registry keys and
// the full instance-specific path of a pktmon session.
const scriptClearProof = buildPrelude + `$done = @()
$left = @()
foreach ($r in @(Get-NetFirewallRule -Group $in.group -ErrorAction SilentlyContinue)) {
  if ($in.desc -and [string]$r.Description -ceq $in.desc) {
    Remove-NetFirewallRule -InputObject $r
    $done += 'the proof firewall rule ' + [string]$r.Name
  } else { $left += 'the unverified proof firewall rule ' + [string]$r.Name }
}
$k = 'HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\Virtualization\GuestCommunicationServices'
if (Test-Path -LiteralPath $k) {
  foreach ($c in @(Get-ChildItem -LiteralPath $k)) {
    $element = [string]$c.GetValue('ElementName')
    if ($in.element -and $element -ceq $in.element -and $c.SubKeyCount -eq 0) {
      Remove-Item -LiteralPath $c.PSPath -Force
      $done += 'the proof hv_sock key ' + [string]$c.PSChildName
    } elseif ($element.StartsWith($in.elementPrefix, [StringComparison]::Ordinal)) {
      $left += 'the unverified proof hv_sock key ' + [string]$c.PSChildName
    }
  }
}
$s = @(Get-EtwTraceSession -Name 'PktMon' -ErrorAction SilentlyContinue)
if ($s.Count -gt 0) {
  $path = [string]$s[0].LocalFilePath
  $pattern = '^' + [regex]::Escape($in.dir + 'pktmon-' + $in.owner + '-') + '[0-9]+\\capture\.etl$'
  if ($s.Count -eq 1 -and $in.owner -and $path -match $pattern) {
    $pm = Join-Path $env:SystemRoot 'System32\PktMon.exe'
    & $pm stop | Out-Null
    if ($LASTEXITCODE -ne 0) { throw ('pktmon stop failed: ' + $LASTEXITCODE) }
    $done += 'the proof pktmon session'
  } else { $left += 'the running pktmon session (ownership could not be verified)' }
}
Write-Doc ([ordered]@{ cleared = @($done); left = @($left) })
`

// scriptGuest runs one guest script over PowerShell Direct as the given
// account. The script text and its JSON argument travel as data: the guest
// side parses the argument into $a and dot-sources the fixed text, so $a is
// in its scope. What the guest printed comes back as one string.
const scriptGuest = buildPrelude + `$pw = ConvertTo-SecureString ([string]$in.password) -AsPlainText -Force
$cred = New-Object Management.Automation.PSCredential ([string]$in.user, $pw)
$out = Invoke-Command -VMId $in.vm -Credential $cred -ErrorAction Stop -ArgumentList ([string]$in.arg), ([string]$in.text) -ScriptBlock {
  param($j, $t)
  $a = $j | ConvertFrom-Json
  . ([scriptblock]::Create($t))
}
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
	"SetEnhancedSession":  scriptSetEnhancedSession,
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
	"RemoveHyperVAdmin":   scriptRemoveHyperVAdmin,
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
	arg := struct {
		nameArg
		SID      string `json:"sid"`
		VMID     string `json:"vmId"`
		SwitchID string `json:"switchId"`
	}{na, sid, known.VM, known.Switch}
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
		p, err := netip.ParsePrefix(x.Prefix)
		if err != nil {
			continue // a NAT on IPv6 or a range Hyper-V cannot read is not ours
		}
		inv.NATs = append(inv.NATs, LiveNAT{ID: x.ID, Name: x.Name, Prefix: p})
	}
	return inv, nil
}

// EnsureSwitch creates or checks the switch and its host vNIC.
func (b psBuilder) EnsureSwitch(ctx context.Context, n Names, sub netip.Prefix, owner, vnicMAC, switchID string) (string, int, error) {
	if switchID != "" && !guidRe.MatchString(switchID) {
		return "", 0, fmt.Errorf("switch ID %q is not a GUID", switchID)
	}
	if err := n.Validate(); err != nil {
		return "", 0, err
	}
	if err := checkSubnet(sub); err != nil {
		return "", 0, err
	}
	if err := checkOwner(owner); err != nil {
		return "", 0, err
	}
	mac, err := NormalizeMAC(vnicMAC)
	if err != nil {
		return "", 0, err
	}
	arg := struct {
		Switch    string `json:"switch"`
		SwitchID  string `json:"switchId"`
		Owner     string `json:"owner"`
		MAC       string `json:"mac"`
		Gateway   string `json:"gateway"`
		PrefixLen int    `json:"prefixLen"`
	}{n.Switch, switchID, owner, mac, sub.Addr().Next().String(), sub.Bits()}
	var w struct {
		SwitchID string `json:"switchId"`
		IfIndex  int    `json:"ifIndex"`
	}
	if err := b.call(ctx, scriptEnsureSwitch, arg, &w); err != nil {
		return "", 0, err
	}
	if !guidRe.MatchString(w.SwitchID) || checkIfIndex(w.IfIndex) != nil {
		return "", 0, fmt.Errorf("the switch came back as %q on interface %d", w.SwitchID, w.IfIndex)
	}
	return w.SwitchID, w.IfIndex, nil
}

func validNATID(id string) bool {
	return id != "" && len(id) <= 260 && strings.IndexFunc(id, unicode.IsControl) < 0
}

// A name or a name/store key can be reused by a replacement NAT.
func independentNATID(name, id string) bool {
	if !validNATID(id) {
		return false
	}
	key, _, _ := strings.Cut(id, ";")
	return !strings.EqualFold(key, name)
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
func (b psBuilder) CreateVM(ctx context.Context, p Plan, iso, answerISO string) (string, string, OwnedDisk, error) {
	if err := p.Names.Validate(); err != nil {
		return "", "", OwnedDisk{}, err
	}
	if err := checkOwner(p.Owner); err != nil {
		return "", "", OwnedDisk{}, err
	}
	for what, path := range map[string]string{"VHDX folder": p.VHDXDir, "ISO": iso, "answer ISO": answerISO} {
		if err := checkWinPath(what, path); err != nil {
			return "", "", OwnedDisk{}, err
		}
	}
	if _, err := NormalizeMAC(p.GuestMAC); err != nil {
		return "", "", OwnedDisk{}, err
	}
	arg := map[string]any{
		"vm": p.Names.VM, "owner": p.Owner, "vhdxDir": p.VHDXDir, "memory": p.MemoryBytes, "diskBytes": uint64(guestDiskBytes),
		"procCount": p.ProcCount, "procMax": p.ProcMax, "procWeight": p.ProcWeight, "mac": p.GuestMAC,
		"iso": iso, "answer": answerISO,
	}
	var w struct {
		VMID      string    `json:"vmId"`
		AdapterID string    `json:"adapterId"`
		Disk      OwnedDisk `json:"disk"`
	}
	if err := b.call(ctx, scriptCreateVM, arg, &w); err != nil {
		return "", "", OwnedDisk{}, err
	}
	if err := w.Disk.validate(); err != nil {
		return "", "", OwnedDisk{}, err
	}
	return w.VMID, w.AdapterID, w.Disk, nil
}

// SetIntegration sets integration services by ID.
func (b psBuilder) SetIntegration(ctx context.Context, vmID string, want map[string]bool) error {
	if err := checkVMID("VM ID", vmID); err != nil {
		return err
	}
	ids := map[string]bool{}
	for id, on := range want {
		if !guidRe.MatchString(id) {
			return fmt.Errorf("integration service %q is not a GUID", id)
		}
		ids[strings.ToUpper(id)] = on
	}
	return b.ok(ctx, scriptSetIntegration, map[string]any{"vm": vmID, "want": ids})
}

// SetBindings unbinds components from the host vNIC. Only the planned
// components may go.
func (b psBuilder) SetBindings(ctx context.Context, ifIndex int, off []string) error {
	if err := checkIfIndex(ifIndex); err != nil {
		return err
	}
	for _, c := range off {
		if !slices.Contains(BindingsOff(), c) {
			return fmt.Errorf("component %q is not one setup unbinds", c)
		}
	}
	return b.ok(ctx, scriptSetBindings, map[string]any{"ifIndex": ifIndex, "off": off})
}

// EnsureFirewall makes the group's one inbound Block rule on the vNIC.
func (b psBuilder) EnsureFirewall(ctx context.Context, n Names, ifIndex int, owner string) error {
	if err := n.Validate(); err != nil {
		return err
	}
	if err := checkIfIndex(ifIndex); err != nil {
		return err
	}
	if err := checkOwner(owner); err != nil {
		return err
	}
	return b.ok(ctx, scriptEnsureFirewall, map[string]any{"group": n.FirewallGroup, "ifIndex": ifIndex, "owner": owner})
}

// SetHostNeighbor gives the host its static entry for the guest.
func (b psBuilder) SetHostNeighbor(ctx context.Context, ifIndex int, ip netip.Addr, mac string) error {
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
	return b.ok(ctx, scriptSetHostNeighbor, map[string]any{"ifIndex": ifIndex, "ip": ip.String(), "mac": macDash(m)})
}

// SetEnhancedSession sets the host-wide setting.
func (b psBuilder) SetEnhancedSession(ctx context.Context, on bool) (bool, error) {
	var w struct {
		Was bool `json:"was"`
	}
	err := b.call(ctx, scriptSetEnhancedSession, map[string]bool{"on": on}, &w)
	return w.Was, err
}

// EnsureGuardian returns the fingerprint only when this call created it.
func (b psBuilder) EnsureGuardian(ctx context.Context) (string, error) {
	var w struct {
		ID string `json:"id"`
	}
	err := b.call(ctx, scriptEnsureGuardian, map[string]string{}, &w)
	if err == nil && w.ID != "" && !guardianIDRE.MatchString(w.ID) {
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

// BootKeys presses Enter once a second for 10 seconds.
func (b psBuilder) BootKeys(ctx context.Context, vmID string) error {
	if err := checkVMID("VM ID", vmID); err != nil {
		return err
	}
	return b.ok(ctx, scriptBootKeys, map[string]string{"vm": vmID})
}

// EjectDVDs removes every DVD drive.
func (b psBuilder) EjectDVDs(ctx context.Context, vmID string) error {
	if err := checkVMID("VM ID", vmID); err != nil {
		return err
	}
	var w struct {
		Left int `json:"left"`
	}
	if err := b.call(ctx, scriptEjectDVDs, map[string]string{"vm": vmID}, &w); err != nil {
		return err
	}
	if w.Left != 0 {
		return fmt.Errorf("%d DVD drives are still on the VM", w.Left)
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
func (b psBuilder) Guest(ctx context.Context, vmID string, cred Cred, s GuestScript, arg, out any) error {
	if err := checkVMID("VM ID", vmID); err != nil {
		return err
	}
	if !slices.ContainsFunc(guestScripts, func(g GuestScript) bool { return g == s }) {
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
	if err := b.call(ctx, scriptGuest, map[string]string{"vm": vmID, "user": cred.User, "password": cred.Password, "arg": a, "text": s.Text}, &w); err != nil {
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
	err := b.call(ctx, scriptRemoveVM, map[string]any{"vm": vmID, "owner": p.Owner, "disk": p.Disk}, &w)
	return w.Left, err
}

// RemoveSwitch removes the switch by ID.
func (b psBuilder) RemoveSwitch(ctx context.Context, switchID, owner string) error {
	if err := checkVMID("switch ID", switchID); err != nil {
		return err
	}
	if err := checkOwner(owner); err != nil {
		return err
	}
	return b.ok(ctx, scriptRemoveSwitch, map[string]string{"switchId": switchID, "owner": owner})
}

// RemoveNAT deletes only an independently identified NAT.
func (b psBuilder) RemoveNAT(ctx context.Context, n Names, sub netip.Prefix, id string) error {
	if err := n.Validate(); err != nil {
		return err
	}
	if err := checkSubnet(sub); err != nil {
		return err
	}
	if !independentNATID(n.NAT, id) {
		return errors.New("the NAT has no independent provider identity")
	}
	return b.ok(ctx, scriptRemoveNAT, map[string]string{"nat": n.NAT, "prefix": sub.String(), "id": id})
}

// RemoveFirewall removes the group's rules that carry the owner tag.
func (b psBuilder) RemoveFirewall(ctx context.Context, n Names, owner string) error {
	if err := n.Validate(); err != nil {
		return err
	}
	if err := checkOwner(owner); err != nil {
		return err
	}
	return b.ok(ctx, scriptRemoveFirewall, map[string]string{"group": n.FirewallGroup, "owner": owner})
}

// RemoveGuardian deletes the guardian.
func (b psBuilder) RemoveGuardian(ctx context.Context, id string) error {
	if !guardianIDRE.MatchString(id) {
		return errors.New("the guardian has no recorded fingerprint")
	}
	return b.ok(ctx, scriptRemoveGuardian, map[string]string{"id": id})
}

// RemoveHyperVAdmin takes the account out of Hyper-V Administrators.
func (b psBuilder) RemoveHyperVAdmin(ctx context.Context, sid string) error {
	if !sidRe.MatchString(sid) {
		return fmt.Errorf("%q is not a SID", sid)
	}
	return b.ok(ctx, scriptRemoveHyperVAdmin, map[string]string{"sid": sid})
}

// ClearProof clears earlier owned proof artifacts and lists unverified ones.
func (b psBuilder) ClearProof(ctx context.Context, n Names, owner string) (ProofCleanup, error) {
	if err := n.Validate(); err != nil {
		return ProofCleanup{}, err
	}
	desc, element := "", ""
	if owner != "" {
		if err := checkOwner(owner); err != nil {
			return ProofCleanup{}, err
		}
		desc, element = proofCanaryDesc(owner), ProofElementName(n, owner)
	}
	if err := checkWinPath("data folder", n.DataDir); err != nil {
		return ProofCleanup{}, err
	}
	var w ProofCleanup
	arg := map[string]string{"group": n.FirewallGroup + proofGroupSuffix, "desc": desc,
		"element": element, "elementPrefix": "CashPilot Secure VM proof " + n.VM,
		"owner": owner, "dir": strings.TrimRight(n.DataDir, `\`) + `\proof\`}
	err := b.call(ctx, scriptClearProof, arg, &w)
	return w, err
}
