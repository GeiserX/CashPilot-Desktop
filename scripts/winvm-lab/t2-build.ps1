<#
.SYNOPSIS
  Lab only (T2): build, or repair, one Secure VM lab instance through the real
  setup chain, and prove its fence.

.DESCRIPTION
  Runs elevated on the lab host. It creates private directories and holds
  them open while setup runs. Guest passwords are kept under the lab
  instance's data directory and protected with DPAPI for this account.
  Setup stores the VM under the same private data directory.

.EXAMPLE
  .\t2-build.ps1 -Instance 1 -Exe .\CashPilot-Desktop-lab.exe -Repo C:\src\CashPilot-Desktop -Iso D:\iso\windows.iso -ImageIndex 6 -GuestMemoryGB 4 -MinHostRamGB 8 -KeepEnhancedSession
#>
param(
  [Parameter(Mandatory = $true)][ValidateRange(1, 9)][int]$Instance,
  [Parameter(Mandatory = $true)][string]$Exe,
  [Parameter(Mandatory = $true)][string]$Repo,
  [string]$Iso = '',
  [int]$ImageIndex = 1,
  [string]$ProductKey = '',
  [ValidateRange(2, 64)][int]$GuestMemoryGB = 4,
  [ValidateRange(0, 1024)][int]$MinHostRamGB = 0,
  [switch]$KeepEnhancedSession,
  [switch]$ApproveNetwork
)
$ErrorActionPreference = 'Stop'
$name = 'cpd-svm-' + $Instance
$common = [Environment]::GetFolderPath('CommonApplicationData')
if ($common -ne 'C:\ProgramData') { throw ('Secure VM Setup uses C:\ProgramData\CashPilot. This host reports ProgramData at ' + $common) }
$data = Join-Path $common ('CashPilot\lab\' + $name)
$guard = Join-Path $data 'guard'
Write-Host ('VM storage volume: ' + [IO.Path]::GetPathRoot($data) + ' (private directory under ' + $data + ', 128 GiB dynamic disk)')
$taskPath = '\CashPilot\'
$taskName = 'VMGuard-' + $name

. (Join-Path $Repo 'internal\runtime\scripts\secure-vm-directory.ps1')
. (Join-Path $Repo 'internal\runtime\scripts\secure-vm-files.ps1')
$base = Open-CPDDirectory (Join-Path $common 'CashPilot') $true 'cashpilot-data'
$lab = Open-CPDDirectory (Join-Path $common 'CashPilot\lab') $true 'cashpilot-lab'
$instanceLease = Open-CPDDirectory $data $true 'cashpilot-data'
$guardLease = Open-CPDDirectory $guard $true 'cashpilot-guard'
$journal = Open-CPDFileJournal $data

function New-LabPassword {
  $set = 'ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789'
  $b = New-Object byte[] 24
  [Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($b)
  'Cp9!' + (-join ($b | ForEach-Object { $set[$_ % $set.Length] }))
}

$credRel = 'lab-guest.cred'
$credFile = Join-Path $data $credRel
if (-not (Get-CPDFileID $journal $credRel)) {
  $pair = [ordered]@{ admin = (New-LabPassword); earner = (New-LabPassword) }
  $protected = ConvertTo-SecureString ($pair | ConvertTo-Json -Compress) -AsPlainText -Force | ConvertFrom-SecureString
  $null = Write-CPDNewFile $journal $credRel ([Text.Encoding]::ASCII.GetBytes($protected))
}
$sec = [IO.File]::ReadAllText($credFile) | ConvertTo-SecureString
$bstr = [Runtime.InteropServices.Marshal]::SecureStringToBSTR($sec)
try { $pair = [Runtime.InteropServices.Marshal]::PtrToStringBSTR($bstr) | ConvertFrom-Json }
finally { [Runtime.InteropServices.Marshal]::ZeroFreeBSTR($bstr) }

$sid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
$params = [ordered]@{
  version = 1; iso = $Iso; imageIndex = $ImageIndex; productKey = $ProductKey; timeZone = ''
  adminPassword = $pair.admin; earnerPassword = $pair.earner; userSid = $sid; extraExits = @(); approveNetwork = [bool]$ApproveNetwork
  lab = [ordered]@{ minHostRam = [uint64]$MinHostRamGB * 1GB; guestMemory = [uint64]$GuestMemoryGB * 1GB; keepEnhancedSession = [bool]$KeepEnhancedSession; instance = $Instance }
}
$src = Join-Path $Repo 'internal\winvm\scripts\setup.ps1'
$exeHash = (Get-FileHash -LiteralPath $Exe -Algorithm SHA256).Hash
$started = Get-Date
$paramsRel = 'guard\.lab-params.' + [guid]::NewGuid().ToString('N') + '.tmp'
$paramsFile = Join-Path $data $paramsRel
$paramsId = $null
try {
  $paramsId = Write-CPDNewFile $journal $paramsRel ([Text.Encoding]::UTF8.GetBytes(($params | ConvertTo-Json -Compress -Depth 4)))
  $paramsHash = (Get-FileHash -LiteralPath $paramsFile -Algorithm SHA256).Hash
  & $src -Exe $Exe -ExeHash $exeHash -DataDir $data -TaskPath $taskPath -TaskName $taskName -Params $paramsFile -ParamsHash $paramsHash
  $code = $LASTEXITCODE
} finally {
  Remove-CPDFile $journal $paramsRel $paramsId
  $guardLease.Dispose()
  $instanceLease.Dispose()
  $lab.Dispose()
  $base.Dispose()
}
'setup exit code: ' + $code + ' after ' + [int]((Get-Date) - $started).TotalMinutes + ' min'
$log = Join-Path $data 'setup.log'
if (Test-Path -LiteralPath $log) { Get-Content -LiteralPath $log -Tail 60 }

# How long each proof step took, from this run's lines in setup.log (local
# time, whole seconds). The proof lock lasts 900 s and is taken again before
# each step, so a step of 900 s or more lets the guard act in the middle.
if (Test-Path -LiteralPath $log) {
  $marks = @()
  foreach ($l in Get-Content -LiteralPath $log) {
    if ($l -match '^(\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}) (proof: \S+|setup done|setup: built|setup failed|connected|disconnected|cut)') {
      $at = [datetime]::ParseExact($Matches[1], 'yyyy/MM/dd HH:mm:ss', [Globalization.CultureInfo]::InvariantCulture)
      if ($at -ge $started.AddSeconds(-1)) { $marks += [pscustomobject]@{ At = $at; What = $Matches[2] } }
    }
  }
  $first = -1
  $longest = 0
  for ($i = 0; $i -lt $marks.Count - 1; $i++) {
    if ($marks[$i].What -notlike 'proof: *') { continue }
    if ($first -lt 0) { $first = $i }
    $s = [int]($marks[$i + 1].At - $marks[$i].At).TotalSeconds
    if ($s -gt $longest) { $longest = $s }
    '{0,-24} {1,5} s' -f $marks[$i].What, $s
  }
  if ($first -ge 0) {
    'proof took {0} s; longest step {1} s (lock 900 s per step)' -f [int]($marks[$marks.Count - 1].At - $marks[$first].At).TotalSeconds, $longest
    if ($longest -ge 900) { 'WARNING: a proof step outlasted the proof lock' }
  }
}
exit $code
