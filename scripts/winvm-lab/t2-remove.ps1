<#
.SYNOPSIS
  Lab only (T2): remove one Secure VM lab instance through the real removal
  chain, then report inventory. Use -AssertClean to require an empty inventory.

.DESCRIPTION
  Runs elevated on the lab host. It opens the recorded private directories
  and runs the removal bootstrap from the checkout.

.EXAMPLE
  .\t2-remove.ps1 -Instance 1 -Exe .\CashPilot-Desktop-lab.exe -Repo C:\src\CashPilot-Desktop
#>
param(
  [Parameter(Mandatory = $true)][ValidateRange(1, 9)][int]$Instance,
  [Parameter(Mandatory = $true)][string]$Exe,
  [Parameter(Mandatory = $true)][string]$Repo,
  [switch]$AssertClean
)
$ErrorActionPreference = 'Stop'
$name = 'cpd-svm-' + $Instance
$common = [Environment]::GetFolderPath('CommonApplicationData')
$data = Join-Path $common ('CashPilot\lab\' + $name)
$guard = Join-Path $data 'guard'
$taskPath = '\CashPilot\'
$taskName = 'VMGuard-' + $name
. (Join-Path $Repo 'internal\runtime\scripts\secure-vm-directory.ps1')
. (Join-Path $Repo 'internal\runtime\scripts\secure-vm-files.ps1')
$instanceLease = Open-CPDDirectory $data $false 'cashpilot-data'
try {
  $guardLease = Open-CPDDirectory $guard $false 'cashpilot-guard'
  try {
    $src = Join-Path $Repo 'internal\winvm\scripts\remove.ps1'
    $exeHash = (Get-FileHash -LiteralPath $Exe -Algorithm SHA256).Hash
    & $src -Exe $Exe -ExeHash $exeHash -DataDir $data -TaskPath $taskPath -TaskName $taskName
    $code = $LASTEXITCODE
  } finally { $guardLease.Dispose() }
} finally { $instanceLease.Dispose() }
'remove exit code: ' + $code

$left = @()
$left += @(Get-VM | Where-Object { $_.Name -eq $name } | ForEach-Object { 'vm ' + $_.Id })
$left += @(Get-VMSwitch | Where-Object { $_.Name -eq $name } | ForEach-Object { 'switch ' + $_.Id })
$left += @(Get-NetNat -ErrorAction SilentlyContinue | Where-Object { $_.Name -eq $name } | ForEach-Object { 'nat ' + $_.InternalIPInterfaceAddressPrefix })
$left += @(Get-NetFirewallRule -Group $name -ErrorAction SilentlyContinue | ForEach-Object { 'rule ' + $_.Name })
$left += @(Get-NetFirewallRule -Group ($name + '-proof') -ErrorAction SilentlyContinue | ForEach-Object { 'proof rule ' + $_.Name })
$left += @(Get-ScheduledTask -TaskPath $taskPath -TaskName $taskName -ErrorAction SilentlyContinue | ForEach-Object { 'task ' + $_.TaskName })
$left | ForEach-Object { 'left: ' + $_ }
'clean host: ' + ($left.Count -eq 0)
if ($code -ne 0) { exit $code }
if ($AssertClean -and $left.Count -gt 0) { exit 1 }
exit 0
