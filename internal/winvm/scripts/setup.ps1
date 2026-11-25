# CashPilot Secure VM setup bootstrap. The admin stub copied this file into
# the admin-only guard folder and checked its SHA-256 before it ran it.
# It copies the CashPilot exe next to itself, checks the copy's SHA-256, and
# runs the copy as --vm-setup. The copy is also what the guard task runs.
param(
  [Parameter(Mandatory = $true)][string]$Exe,
  [Parameter(Mandatory = $true)][string]$ExeHash,
  [Parameter(Mandatory = $true)][string]$DataDir,
  [Parameter(Mandatory = $true)][string]$TaskPath,
  [Parameter(Mandatory = $true)][string]$TaskName,
  [Parameter(Mandatory = $true)][string]$Params
)
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
trap { [Console]::Error.WriteLine($_.Exception.Message); exit 1 }
$guard = Join-Path $DataDir 'guard'
$copy = Join-Path $guard 'CashPilot-guard.exe'
# A guard that runs holds the copy open: stop it first (Repair).
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
# -Wait matters: the exe is a GUI-subsystem binary, so a bare call returns at once.
$p = Start-Process -FilePath $copy -ArgumentList @('--vm-setup', ('"' + $Params + '"')) -Wait -PassThru -WindowStyle Hidden
exit $p.ExitCode
