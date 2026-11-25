package winvm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"
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
const hgsGuardian = "UntrustedGuardian"

// hvAdmins is the Hyper-V Administrators group, by SID: its name is
// translated.
const hvAdmins = "S-1-5-32-578"

// buildPrelude adds the helpers the build scripts share to hvPrelude.
const buildPrelude = hvPrelude + `function Get-Owner([string]$s) {
  if ($s -match 'cashpilot-owner=([0-9a-f]{32})') { return $Matches[1] }
  return ''
}
function Get-AdapterByMac([string]$mac) {
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
`

// scriptInventory lists every object with one of our names, the owner token
// each carries, and the host facts setup and Remove VM check.
const scriptInventory = buildPrelude + `$o = [ordered]@{}
$o.vms = @(Get-VM | Where-Object { $_.Name -eq $in.vm } | ForEach-Object { [ordered]@{ id = $_.Id.ToString(); owner = (Get-Owner ([string]$_.Notes)) } })
$o.vtpmVms = @(Get-VM | Where-Object { try { (Get-VMSecurity -VM $_).TpmEnabled } catch { $false } } | ForEach-Object { $_.Id.ToString() })
$o.switches = @(Get-VMSwitch | Where-Object { $_.Name -eq $in.switch } | ForEach-Object {
  $sw = $_
  $ports = @(Get-VMNetworkAdapter -All | Where-Object { (-not $_.IsManagementOs) -and ($_.SwitchId -eq $sw.Id) } | ForEach-Object { $_.VMId.ToString() } | Select-Object -Unique)
  [ordered]@{ id = $sw.Id.ToString(); owner = (Get-Owner ([string]$sw.Notes)); ports = @($ports) }
})
$o.nats = @(Get-NetNat -ErrorAction SilentlyContinue | ForEach-Object { [ordered]@{ name = [string]$_.Name; prefix = [string]$_.InternalIPInterfaceAddressPrefix } })
$o.firewall = @(Get-NetFirewallRule -Group $in.group -ErrorAction SilentlyContinue | ForEach-Object { [ordered]@{ id = [string]$_.Name; owner = (Get-Owner ([string]$_.Description)) } })
$t = @(Get-ScheduledTask -TaskPath $in.taskPath -TaskName $in.taskName -ErrorAction SilentlyContinue)
$o.task = $null
if ($t.Count -eq 1) { $o.task = [ordered]@{ id = [string]$t[0].TaskPath + [string]$t[0].TaskName; owner = (Get-Owner ([string]$t[0].Description)) } }
$o.guardian = [bool](Get-HgsGuardian -Name 'UntrustedGuardian' -ErrorAction SilentlyContinue)
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

// scriptEnsureSwitch creates the Internal switch with the owner tag, or takes
// the one there when it carries the tag. It gives the host vNIC its static MAC
// and the gateway address, and returns the switch ID and the vNIC's ifIndex.
const scriptEnsureSwitch = buildPrelude + `$sw = @(Get-VMSwitch | Where-Object { $_.Name -eq $in.switch })
if ($sw.Count -gt 1) { throw 'More than one switch has this name.' }
if ($sw.Count -eq 1) {
  if ((Get-Owner ([string]$sw[0].Notes)) -ne $in.owner) { throw 'A switch with this name exists and was not made by this install.' }
  $sw = $sw[0]
} else {
  $sw = New-VMSwitch -Name $in.switch -SwitchType Internal
  Set-VMSwitch -VMSwitch $sw -Notes ('CashPilot Secure VM. cashpilot-owner=' + $in.owner)
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

// scriptEnsureNAT creates the NetNat on our /29, or checks the one with our
// name is on it.
const scriptEnsureNAT = buildPrelude + `$n = @(Get-NetNat -ErrorAction SilentlyContinue | Where-Object { $_.Name -eq $in.nat })
if ($n.Count -gt 0) {
  if ($n.Count -ne 1 -or [string]$n[0].InternalIPInterfaceAddressPrefix -ne $in.prefix) { throw 'A NAT with this name exists on another range.' }
} else {
  New-NetNat -Name $in.nat -InternalIPInterfaceAddressPrefix $in.prefix | Out-Null
}
Write-Doc ([ordered]@{ ok = $true })
`

// scriptCreateVM creates the VM off, with one disconnected adapter: only the
// proof and the guard ever connect it. Gen 2, Secure Boot, a vTPM with a local
// key protector, static memory, the plan's processor shape, no checkpoints,
// no automatic start, the owner tag in its Notes, and the two ISOs.
const scriptCreateVM = buildPrelude + `if (@(Get-VM | Where-Object { $_.Name -eq $in.vm }).Count -gt 0) { throw 'A VM with this name exists.' }
$disk = Join-Path $in.vhdxDir ($in.vm + '.vhdx')
if (Test-Path -LiteralPath $disk) { throw 'The VM disk already exists.' }
New-Item -ItemType Directory -Force -Path $in.vhdxDir | Out-Null
$vm = New-VM -Name $in.vm -Generation 2 -MemoryStartupBytes ([uint64]$in.memory) -NewVHDPath $disk -NewVHDSizeBytes ([uint64]$in.diskBytes) -Path $in.vhdxDir
Set-VM -VM $vm -Notes ('CashPilot Secure VM. cashpilot-owner=' + $in.owner) -ProcessorCount ([int]$in.procCount) -StaticMemory -CheckpointType Disabled -AutomaticStartAction Nothing -AutomaticStopAction ShutDown
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
Write-Doc ([ordered]@{ vmId = $vm.Id.ToString(); adapterId = ($a[0].Id -split '\\')[-1] })
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

// scriptEnsureGuardian creates the guardian a local key protector uses, when
// there is none.
const scriptEnsureGuardian = buildPrelude + `$c = $false
if (-not (Get-HgsGuardian -Name 'UntrustedGuardian' -ErrorAction SilentlyContinue)) { New-HgsGuardian -Name 'UntrustedGuardian' -GenerateCertificates | Out-Null; $c = $true }
Write-Doc ([ordered]@{ created = $c })
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
if (Test-Path -LiteralPath $stage) { Remove-Item -LiteralPath $stage -Recurse -Force }
New-Item -ItemType Directory -Path $stage | Out-Null
try {
  [IO.File]::WriteAllBytes((Join-Path $stage 'autounattend.xml'), [Convert]::FromBase64String($in.xml))
  $fs = New-Object -ComObject IMAPI2FS.MsftFileSystemImage
  $fs.FileSystemsToCreate = 3
  $fs.VolumeName = 'CPDANSWER'
  $fs.Root.AddTree($stage, $false)
  $tmp = $in.path + '.tmp'
  [CpdIso]::Write($fs.CreateResultImage().ImageStream, $tmp)
  Move-Item -LiteralPath $tmp -Destination $in.path -Force
} finally { Remove-Item -LiteralPath $stage -Recurse -Force -ErrorAction SilentlyContinue }
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
if ($t.Count -eq 1) {
  if ((Get-Owner ([string]$t[0].Description)) -ne $in.owner) { throw 'A scheduled task with this name was not made by this install.' }
  if ($t[0].State.ToString() -eq 'Running') {
    Stop-ScheduledTask -TaskPath $in.taskPath -TaskName $in.taskName
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
const scriptStartGuardTask = buildPrelude + `Start-ScheduledTask -TaskPath $in.taskPath -TaskName $in.taskName
Write-Doc ([ordered]@{ ok = $true })
`

// scriptUnregisterGuardTask stops and deletes the guard task when it carries
// the owner tag, then deletes its folder when nothing else is in it.
const scriptUnregisterGuardTask = buildPrelude + `$t = @(Get-ScheduledTask -TaskPath $in.taskPath -TaskName $in.taskName -ErrorAction SilentlyContinue)
if ($t.Count -eq 1) {
  if ((Get-Owner ([string]$t[0].Description)) -ne $in.owner) { throw 'The scheduled task was not made by this install.' }
  Stop-ScheduledTask -TaskPath $in.taskPath -TaskName $in.taskName -ErrorAction SilentlyContinue
  Unregister-ScheduledTask -TaskPath $in.taskPath -TaskName $in.taskName -Confirm:$false
}
try {
  $svc = New-Object -ComObject Schedule.Service
  $svc.Connect()
  $f = $svc.GetFolder($in.taskPath.TrimEnd('\'))
  if ($f.GetTasks(1).Count -eq 0 -and $f.GetFolders(0).Count -eq 0) { $svc.GetFolder('\').DeleteFolder($in.taskPath.Trim('\'), 0) }
} catch { }
Write-Doc ([ordered]@{ ok = $true })
`

// scriptRemoveVM turns the VM off, removes it, deletes its disks that lie in
// vhdxDir and the empty folders New-VM left there, and lists the disks it
// left.
const scriptRemoveVM = buildPrelude + `$vm = @(Find-OurVM $in.vm)
$left = @()
if ($vm.Count -eq 1) {
  $vm = $vm[0]
  $root = ([IO.Path]::GetFullPath($in.vhdxDir)).TrimEnd('\') + '\'
  $disks = @(Get-VMHardDiskDrive -VM $vm | Where-Object { $_.Path } | ForEach-Object { [string]$_.Path })
  $folder = Join-Path $in.vhdxDir $vm.Name
  if ($vm.State.ToString() -ne 'Off') { Stop-VM -VM $vm -TurnOff -Force }
  Remove-VM -VM $vm -Force
  foreach ($d in $disks) {
    $full = [IO.Path]::GetFullPath($d)
    if ($full.StartsWith($root, [StringComparison]::OrdinalIgnoreCase)) { Remove-Item -LiteralPath $full -Force -ErrorAction SilentlyContinue }
    if (Test-Path -LiteralPath $full) { $left += $full }
  }
  if (Test-Path -LiteralPath $folder) {
    Get-ChildItem -LiteralPath $folder -Recurse -Directory | Sort-Object { $_.FullName.Length } -Descending | Where-Object { @(Get-ChildItem -LiteralPath $_.FullName -Force).Count -eq 0 } | Remove-Item -Force -ErrorAction SilentlyContinue
    if (@(Get-ChildItem -LiteralPath $folder -Force).Count -eq 0) { Remove-Item -LiteralPath $folder -Force -ErrorAction SilentlyContinue }
  }
}
Write-Doc ([ordered]@{ left = @($left) })
`

// scriptRemoveSwitch removes the switch by ID, with its host vNIC. It refuses
// while a VM has an adapter on it.
const scriptRemoveSwitch = buildPrelude + `$sw = @(Get-VMSwitch | Where-Object { $_.Id.ToString() -eq $in.switchId })
if ($sw.Count -eq 1) {
  if (@(Get-VMNetworkAdapter -All | Where-Object { (-not $_.IsManagementOs) -and ($_.SwitchId -eq $sw[0].Id) }).Count -gt 0) { throw 'A VM still has an adapter on the switch.' }
  Remove-VMSwitch -VMSwitch $sw[0] -Force
}
Write-Doc ([ordered]@{ ok = $true })
`

// scriptRemoveNAT removes the NAT with our name when it is on our /29.
const scriptRemoveNAT = buildPrelude + `$n = @(Get-NetNat -ErrorAction SilentlyContinue | Where-Object { ($_.Name -eq $in.nat) -and ([string]$_.InternalIPInterfaceAddressPrefix -eq $in.prefix) })
$n | Remove-NetNat -Confirm:$false
Write-Doc ([ordered]@{ ok = $true })
`

// scriptRemoveFirewall removes the rules of the group that carry the owner
// tag.
const scriptRemoveFirewall = buildPrelude + `@(Get-NetFirewallRule -Group $in.group -ErrorAction SilentlyContinue | Where-Object { (Get-Owner ([string]$_.Description)) -eq $in.owner }) | Remove-NetFirewallRule
Write-Doc ([ordered]@{ ok = $true })
`

// scriptRemoveGuardian deletes the guardian.
const scriptRemoveGuardian = buildPrelude + `Get-HgsGuardian -Name 'UntrustedGuardian' -ErrorAction SilentlyContinue | Remove-HgsGuardian
Write-Doc ([ordered]@{ ok = $true })
`

// scriptRemoveHyperVAdmin takes the account out of Hyper-V Administrators.
const scriptRemoveHyperVAdmin = buildPrelude + `if ((Get-GroupSids 'S-1-5-32-578') -contains $in.sid) { Remove-LocalGroupMember -SID 'S-1-5-32-578' -Member $in.sid }
Write-Doc ([ordered]@{ ok = $true })
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
	VTPMVMs      []string    `json:"vtpmVms"`
	Switches     []ownedWire `json:"switches"`
	NATs         []natWire   `json:"nats"`
	Firewall     []ownedWire `json:"firewall"`
	Task         *ownedWire  `json:"task"`
	Guardian     bool        `json:"guardian"`
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

// Inventory lists every object with our names, the owner token each carries,
// and the host facts. The guard reads it to find the VM of an interrupted
// setup.
func (h *psHost) Inventory(ctx context.Context, n Names, sid string) (Inventory, error) {
	na, err := namesArg(n)
	if err != nil {
		return Inventory{}, err
	}
	if sid != "" && !sidRe.MatchString(sid) {
		return Inventory{}, fmt.Errorf("%q is not a SID", sid)
	}
	arg := struct {
		nameArg
		SID string `json:"sid"`
	}{na, sid}
	var w inventoryWire
	if err := h.call(ctx, scriptInventory, arg, &w); err != nil {
		return Inventory{}, err
	}
	inv := Inventory{Guardian: w.Guardian, HyperVAdmin: w.HyperVAdmin, Enhanced: w.Enhanced,
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
	for _, id := range w.VTPMVMs {
		if !guidRe.MatchString(id) {
			return Inventory{}, fmt.Errorf("VM ID %q is not a GUID", id)
		}
		inv.VTPMVMs = append(inv.VTPMVMs, id)
	}
	for _, x := range w.NATs {
		p, err := netip.ParsePrefix(x.Prefix)
		if err != nil {
			continue // a NAT on IPv6 or a range Hyper-V cannot read is not ours
		}
		inv.NATs = append(inv.NATs, LiveNAT{Name: x.Name, Prefix: p})
	}
	return inv, nil
}

// EnsureSwitch creates or checks the switch and its host vNIC.
func (b psBuilder) EnsureSwitch(ctx context.Context, n Names, sub netip.Prefix, owner, vnicMAC string) (string, int, error) {
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
		Owner     string `json:"owner"`
		MAC       string `json:"mac"`
		Gateway   string `json:"gateway"`
		PrefixLen int    `json:"prefixLen"`
	}{n.Switch, owner, mac, sub.Addr().Next().String(), sub.Bits()}
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

// EnsureNAT creates or checks our NAT.
func (b psBuilder) EnsureNAT(ctx context.Context, n Names, sub netip.Prefix) error {
	if err := n.Validate(); err != nil {
		return err
	}
	if err := checkSubnet(sub); err != nil {
		return err
	}
	return b.ok(ctx, scriptEnsureNAT, map[string]string{"nat": n.NAT, "prefix": sub.String()})
}

// CreateVM creates the VM off, with one disconnected adapter.
func (b psBuilder) CreateVM(ctx context.Context, p Plan, iso, answerISO string) (string, string, error) {
	if err := p.Names.Validate(); err != nil {
		return "", "", err
	}
	if err := checkOwner(p.Owner); err != nil {
		return "", "", err
	}
	for what, path := range map[string]string{"VHDX folder": p.VHDXDir, "ISO": iso, "answer ISO": answerISO} {
		if err := checkWinPath(what, path); err != nil {
			return "", "", err
		}
	}
	if _, err := NormalizeMAC(p.GuestMAC); err != nil {
		return "", "", err
	}
	arg := map[string]any{
		"vm": p.Names.VM, "owner": p.Owner, "vhdxDir": p.VHDXDir, "memory": p.MemoryBytes, "diskBytes": uint64(guestDiskBytes),
		"procCount": p.ProcCount, "procMax": p.ProcMax, "procWeight": p.ProcWeight, "mac": p.GuestMAC,
		"iso": iso, "answer": answerISO,
	}
	var w struct {
		VMID      string `json:"vmId"`
		AdapterID string `json:"adapterId"`
	}
	if err := b.call(ctx, scriptCreateVM, arg, &w); err != nil {
		return "", "", err
	}
	return w.VMID, w.AdapterID, nil
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

// EnsureGuardian creates the guardian when there is none.
func (b psBuilder) EnsureGuardian(ctx context.Context) (bool, error) {
	var w struct {
		Created bool `json:"created"`
	}
	err := b.call(ctx, scriptEnsureGuardian, map[string]string{}, &w)
	return w.Created, err
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
func (b psBuilder) StartGuardTask(ctx context.Context, n Names) error {
	a, err := taskArg(n, "")
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

// RemoveVM removes the VM and its disks in vhdxDir.
func (b psBuilder) RemoveVM(ctx context.Context, vmID, vhdxDir string) ([]string, error) {
	if err := checkVMID("VM ID", vmID); err != nil {
		return nil, err
	}
	if err := checkWinPath("VHDX folder", vhdxDir); err != nil {
		return nil, err
	}
	var w struct {
		Left []string `json:"left"`
	}
	err := b.call(ctx, scriptRemoveVM, map[string]string{"vm": vmID, "vhdxDir": vhdxDir}, &w)
	return w.Left, err
}

// RemoveSwitch removes the switch by ID.
func (b psBuilder) RemoveSwitch(ctx context.Context, switchID string) error {
	if err := checkVMID("switch ID", switchID); err != nil {
		return err
	}
	return b.ok(ctx, scriptRemoveSwitch, map[string]string{"switchId": switchID})
}

// RemoveNAT removes our NAT when it is on our /29.
func (b psBuilder) RemoveNAT(ctx context.Context, n Names, sub netip.Prefix) error {
	if err := n.Validate(); err != nil {
		return err
	}
	if err := checkSubnet(sub); err != nil {
		return err
	}
	return b.ok(ctx, scriptRemoveNAT, map[string]string{"nat": n.NAT, "prefix": sub.String()})
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
func (b psBuilder) RemoveGuardian(ctx context.Context) error {
	return b.ok(ctx, scriptRemoveGuardian, map[string]string{})
}

// RemoveHyperVAdmin takes the account out of Hyper-V Administrators.
func (b psBuilder) RemoveHyperVAdmin(ctx context.Context, sid string) error {
	if !sidRe.MatchString(sid) {
		return fmt.Errorf("%q is not a SID", sid)
	}
	return b.ok(ctx, scriptRemoveHyperVAdmin, map[string]string{"sid": sid})
}
