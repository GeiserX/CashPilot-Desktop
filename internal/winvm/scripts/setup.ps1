# CashPilot Secure VM setup bootstrap. The admin stub copied this file into
# the admin-only guard folder and checked its SHA-256 before it ran it.
# It copies the CashPilot exe and the params file next to itself, checks
# each copy's SHA-256, and runs the exe copy as --vm-setup on the params
# copy. The exe copy is also what the guard task runs.
param(
  [Parameter(Mandatory = $true)][string]$Exe,
  [Parameter(Mandatory = $true)][string]$ExeHash,
  [Parameter(Mandatory = $true)][string]$DataDir,
  [Parameter(Mandatory = $true)][string]$TaskPath,
  [Parameter(Mandatory = $true)][string]$TaskName,
  [Parameter(Mandatory = $true)][string]$Params,
  [Parameter(Mandatory = $true)][string]$ParamsHash
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
# A new file takes the admin-only rules of the guard folder; overwriting one
# in place would keep whatever rules it had.
if (Test-Path -LiteralPath $copy) { Remove-Item -LiteralPath $copy -Force }
Copy-Item -LiteralPath $Exe -Destination $copy
if ((Get-FileHash -LiteralPath $copy -Algorithm SHA256).Hash -ne $ExeHash.ToUpper()) {
  Remove-Item -LiteralPath $copy -Force -ErrorAction SilentlyContinue
  exit 13
}
# The params file lies in a folder the user can write. The exe reads only a
# copy in the admin-only guard folder whose SHA-256 the app gave, so a file
# swapped after the app wrote it is never read, and the elevated exe deletes
# only that copy. The app deletes its own file.
$pcopy = Join-Path $guard 'setup-params.json'
if (Test-Path -LiteralPath $pcopy) { Remove-Item -LiteralPath $pcopy -Force }
Copy-Item -LiteralPath $Params -Destination $pcopy
if ((Get-FileHash -LiteralPath $pcopy -Algorithm SHA256).Hash -ne $ParamsHash.ToUpper()) {
  Remove-Item -LiteralPath $pcopy -Force -ErrorAction SilentlyContinue
  exit 13
}
try {
  # -Wait matters: the exe is a GUI-subsystem binary, so a bare call returns at once.
  $p = Start-Process -FilePath $copy -ArgumentList @('--vm-setup', ('"' + $pcopy + '"')) -Wait -PassThru -WindowStyle Hidden
  $code = $p.ExitCode
} finally {
  if (Test-Path -LiteralPath $pcopy) { Remove-Item -LiteralPath $pcopy -Force -ErrorAction SilentlyContinue }
}
exit $code
