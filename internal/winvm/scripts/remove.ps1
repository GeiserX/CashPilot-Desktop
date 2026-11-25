# CashPilot Secure VM removal bootstrap. The admin stub copied this file into
# the admin-only guard folder and checked its SHA-256 before it ran it.
# It copies the CashPilot exe next to itself, checks the copy's SHA-256, runs
# the copy as --vm-remove, then deletes the copies and the folders when they
# are empty.
param(
  [Parameter(Mandatory = $true)][string]$Exe,
  [Parameter(Mandatory = $true)][string]$ExeHash,
  [Parameter(Mandatory = $true)][string]$DataDir,
  [Parameter(Mandatory = $true)][string]$TaskPath,
  [Parameter(Mandatory = $true)][string]$TaskName
)
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
trap { [Console]::Error.WriteLine($_.Exception.Message); exit 1 }
$guard = Join-Path $DataDir 'guard'
$copy = Join-Path $guard ('.remove-exe.' + [guid]::NewGuid().ToString('N') + '.exe')
$journal = Join-Path $DataDir 'desired.json'
$ownsGuardFiles = $false
if (Test-Path -LiteralPath $journal -PathType Leaf) {
  $plan = Get-Content -LiteralPath $journal -Raw | ConvertFrom-Json
  $ownsGuardFiles = $plan.owner -cmatch '^[0-9a-f]{32}$' -and $plan.names.dataDir -eq $DataDir -and
    $plan.names.guardTask -eq ($TaskPath + $TaskName)
}
try {
  Copy-Item -LiteralPath $Exe -Destination $copy
  if ((Get-FileHash -LiteralPath $copy -Algorithm SHA256).Hash -ne $ExeHash.ToUpper()) { exit 13 }
  # Remove owns the task check and unregisters only a task with its owner tag.
  $p = Start-Process -FilePath $copy -ArgumentList @('--vm-remove', ('"' + $DataDir + '"')) -Wait -PassThru -WindowStyle Hidden
  $code = $p.ExitCode
} finally {
  Remove-Item -LiteralPath $copy -Force -ErrorAction SilentlyContinue
}
if ($code -eq 0 -and $ownsGuardFiles -and -not (Test-Path -LiteralPath $journal)) {
  $tasks = @(Get-ScheduledTask -TaskPath $TaskPath -TaskName $TaskName -ErrorAction SilentlyContinue)
  if ($tasks.Count -eq 0) {
    foreach ($f in @('CashPilot-guard.exe', 'remove.ps1', 'setup.ps1')) {
      Remove-Item -LiteralPath (Join-Path $guard $f) -Force -ErrorAction SilentlyContinue
    }
  } else {
    Write-Output ('left: ' + (Join-Path $guard 'CashPilot-guard.exe') + ' (a scheduled task still exists)')
  }
  foreach ($d in @($guard, $DataDir)) {
    if ((Test-Path -LiteralPath $d) -and @(Get-ChildItem -LiteralPath $d -Force).Count -eq 0) { Remove-Item -LiteralPath $d -Force }
  }
}
exit $code
