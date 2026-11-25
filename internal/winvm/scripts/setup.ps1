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
$pcopy = Join-Path $guard 'setup-params.json'
$nonce = [guid]::NewGuid().ToString('N')
$stagedExe = Join-Path $guard ('.setup-exe.' + $nonce + '.tmp')
$stagedParams = Join-Path $guard ('.setup-params.' + $nonce + '.tmp')
$exeBackup = Join-Path $guard ('.guard-backup.' + $nonce + '.tmp')
$paramsBackup = Join-Path $guard ('.params-backup.' + $nonce + '.tmp')
$discard = Join-Path $guard ('.guard-discard.' + $nonce + '.tmp')
$description = ''
$stopped = $false
$exePublished = $false
$paramsPublished = $false
$launched = $false

function Get-OwnedTask {
  $t = @(Get-ScheduledTask -TaskPath $TaskPath -TaskName $TaskName -ErrorAction SilentlyContinue)
  if ($t.Count -eq 0) { return }
  if ($t.Count -ne 1 -or -not $description -or $t[0].Description -cne $description) {
    throw 'The scheduled task was not made by this install.'
  }
  $t[0]
}
function Publish($from, $to, $backup) {
  if ([IO.File]::Exists($to)) { [IO.File]::Replace($from, $to, $backup) }
  else { [IO.File]::Move($from, $to) }
}

try {
  Copy-Item -LiteralPath $Exe -Destination $stagedExe
  Copy-Item -LiteralPath $Params -Destination $stagedParams
  if ((Get-FileHash -LiteralPath $stagedExe -Algorithm SHA256).Hash -ne $ExeHash.ToUpper() -or
      (Get-FileHash -LiteralPath $stagedParams -Algorithm SHA256).Hash -ne $ParamsHash.ToUpper()) { exit 13 }

  $journal = Join-Path $DataDir 'desired.json'
  if (Test-Path -LiteralPath $journal -PathType Leaf) {
    $plan = Get-Content -LiteralPath $journal -Raw | ConvertFrom-Json
    if ($plan.owner -cmatch '^[0-9a-f]{32}$' -and $plan.names.dataDir -eq $DataDir -and
        $plan.names.guardTask -eq ($TaskPath + $TaskName)) {
      $description = 'CashPilot Secure VM guard. cashpilot-owner=' + $plan.owner
    }
  }
  $task = Get-OwnedTask
  if ($task -and $task.State.ToString() -eq 'Running') {
    Stop-ScheduledTask -InputObject $task | Out-Null
    $stopped = $true
    for ($i = 0; $i -lt 30; $i++) {
      $task = Get-OwnedTask
      if (-not $task -or $task.State.ToString() -ne 'Running') { break }
      Start-Sleep -Seconds 1
    }
    if ($task -and $task.State.ToString() -eq 'Running') { throw 'The guard task did not stop.' }
  }

  Publish $stagedParams $pcopy $paramsBackup
  $paramsPublished = $true
  Publish $stagedExe $copy $exeBackup
  $exePublished = $true
  # -Wait matters for a GUI-subsystem exe.
  $p = Start-Process -FilePath $copy -ArgumentList @('--vm-setup', ('"' + $pcopy + '"')) -Wait -PassThru -WindowStyle Hidden
  $launched = $true
  $code = $p.ExitCode
} finally {
  if (-not $launched -and $exePublished) {
    if ([IO.File]::Exists($exeBackup)) { Publish $exeBackup $copy $discard }
    else { [IO.File]::Delete($copy) }
  }
  if ($paramsPublished) {
    if (-not $launched -and [IO.File]::Exists($paramsBackup)) { Publish $paramsBackup $pcopy $stagedParams }
    else { [IO.File]::Delete($pcopy) }
  }
  if ($stopped -and (-not $launched -or $code -ne 0)) {
    $task = Get-OwnedTask
    if ($task -and $task.State.ToString() -ne 'Running') { Start-ScheduledTask -InputObject $task | Out-Null }
  }
  foreach ($f in @($stagedExe, $stagedParams, $exeBackup, $paramsBackup, $discard)) {
    Remove-Item -LiteralPath $f -Force -ErrorAction SilentlyContinue
  }
}
exit $code
