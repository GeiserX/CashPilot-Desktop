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
$copy = Join-Path $guard 'CashPilot-guard.exe'
$t = @(Get-ScheduledTask -TaskPath $TaskPath -TaskName $TaskName -ErrorAction SilentlyContinue)
if ($t.Count -eq 1 -and $t[0].State.ToString() -eq 'Running') {
  Stop-ScheduledTask -TaskPath $TaskPath -TaskName $TaskName
  for ($i = 0; $i -lt 30; $i++) {
    if ((Get-ScheduledTask -TaskPath $TaskPath -TaskName $TaskName).State.ToString() -ne 'Running') { break }
    Start-Sleep -Seconds 1
  }
}
Copy-Item -LiteralPath $Exe -Destination $copy -Force
if ((Get-FileHash -LiteralPath $copy -Algorithm SHA256).Hash -ne $ExeHash.ToUpper()) {
  Remove-Item -LiteralPath $copy -Force -ErrorAction SilentlyContinue
  exit 13
}
$p = Start-Process -FilePath $copy -ArgumentList @('--vm-remove', ('"' + $DataDir + '"')) -Wait -PassThru -WindowStyle Hidden
$code = $p.ExitCode
if ($code -eq 0) {
  foreach ($f in @($copy, (Join-Path $guard 'remove.ps1'), (Join-Path $guard 'setup.ps1'))) {
    Remove-Item -LiteralPath $f -Force -ErrorAction SilentlyContinue
  }
  foreach ($d in @($guard, $DataDir)) {
    if ((Test-Path -LiteralPath $d) -and @(Get-ChildItem -LiteralPath $d -Force).Count -eq 0) { Remove-Item -LiteralPath $d -Force }
  }
}
exit $code
