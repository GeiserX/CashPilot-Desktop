<#
.SYNOPSIS
  Lab only (T2): remove one Secure VM lab instance through the real removal
  chain, then check that nothing with its names is left.

.DESCRIPTION
  Runs elevated on the lab host. Like the app's admin stub, it copies
  remove.ps1 into the instance's admin-only guard folder and runs it with a
  lab build of CashPilot; remove.ps1 runs --vm-remove, then deletes the exe
  copy and the folders when they are empty. Afterwards it lists every object
  that still carries the instance's names, and exits 1 if there is one.
  With -ForgetPasswords it also deletes the instance's saved guest passwords.

.EXAMPLE
  .\t2-remove.ps1 -Instance 1 -Exe .\CashPilot-Desktop-lab.exe -Repo C:\src\CashPilot-Desktop
#>
param(
  [Parameter(Mandatory = $true)][ValidateRange(1, 9)][int]$Instance,
  [Parameter(Mandatory = $true)][string]$Exe,
  [Parameter(Mandatory = $true)][string]$Repo,
  [switch]$ForgetPasswords
)
$ErrorActionPreference = 'Stop'
$name = 'cpd-svm-' + $Instance
$data = Join-Path $env:ProgramData ('CashPilot\lab\' + $name)
$guard = Join-Path $data 'guard'
$taskPath = '\CashPilot\'
$taskName = 'VMGuard-' + $name
if (-not (Test-Path -LiteralPath $guard)) { throw ('no guard folder at ' + $guard + '; nothing was built here') }
$src = Join-Path $Repo 'internal\winvm\scripts\remove.ps1'
$dst = Join-Path $guard 'remove.ps1'
Copy-Item -LiteralPath $src -Destination $dst -Force
$exeHash = (Get-FileHash -LiteralPath $Exe -Algorithm SHA256).Hash
$ps = Join-Path ([Environment]::SystemDirectory) 'WindowsPowerShell\v1.0\powershell.exe'
# Run Windows PowerShell with only its own modules, even from PowerShell 7.
$env:PSModulePath = $env:ProgramFiles + '\WindowsPowerShell\Modules;' + [Environment]::SystemDirectory + '\WindowsPowerShell\v1.0\Modules'
& $ps -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $dst -Exe $Exe -ExeHash $exeHash -DataDir $data -TaskPath $taskPath -TaskName $taskName
$code = $LASTEXITCODE
'remove exit code: ' + $code

$left = @()
$left += @(Get-VM | Where-Object { $_.Name -eq $name } | ForEach-Object { 'vm ' + $_.Id })
$left += @(Get-VMSwitch | Where-Object { $_.Name -eq $name } | ForEach-Object { 'switch ' + $_.Id })
$left += @(Get-NetNat -ErrorAction SilentlyContinue | Where-Object { $_.Name -eq $name } | ForEach-Object { 'nat ' + $_.InternalIPInterfaceAddressPrefix })
$left += @(Get-NetFirewallRule -Group $name -ErrorAction SilentlyContinue | ForEach-Object { 'rule ' + $_.Name })
$left += @(Get-NetFirewallRule -Group ($name + '-proof') -ErrorAction SilentlyContinue | ForEach-Object { 'proof rule ' + $_.Name })
$left += @(Get-ScheduledTask -TaskPath $taskPath -TaskName $taskName -ErrorAction SilentlyContinue | ForEach-Object { 'task ' + $_.TaskName })
if (Test-Path -LiteralPath $data) { $left += @('folder ' + $data) }
$left | ForEach-Object { 'left: ' + $_ }
if ($ForgetPasswords) { Remove-Item -LiteralPath (Join-Path $env:LOCALAPPDATA ('cpd-lab\' + $name + '.cred')) -Force -ErrorAction SilentlyContinue }
if ($code -ne 0 -or $left.Count -gt 0) { exit 1 }
exit 0
