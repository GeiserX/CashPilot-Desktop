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
Import-Module Microsoft.PowerShell.Utility
$journal = Open-CPDFileJournal $DataDir
$nonce = [guid]::NewGuid().ToString('N')
$exeRel = 'guard\CashPilot-guard.exe'
$stagedExeRel = 'guard\.setup-exe.' + $nonce + '.tmp'
$paramsRel = 'guard\.setup-params.' + $nonce + '.json'
$backupRel = 'guard\.guard-backup.' + $nonce + '.tmp'
$copy = Join-Path $DataDir $exeRel
$pcopy = Join-Path $DataDir $paramsRel
$stagedExe = Join-Path $DataDir $stagedExeRel
$exeBackup = Join-Path $DataDir $backupRel
$description = ''
$stopped = $false
$published = $false
$launched = $false
$exeId = $null
$paramsId = $null
$oldId = $null

function Get-OwnedTask {
  $t = @(Get-ScheduledTask -TaskPath $TaskPath -TaskName $TaskName -ErrorAction SilentlyContinue)
  if ($t.Count -eq 0) { return }
  if ($t.Count -ne 1 -or -not $description -or $t[0].Description -cne $description) {
    throw 'The scheduled task was not made by this install.'
  }
  $t[0]
}
try {
  $exeId = Copy-CPDFile $journal $stagedExeRel $Exe
  $paramsId = Copy-CPDFile $journal $paramsRel $Params
  if ((Get-FileHash -LiteralPath $stagedExe -Algorithm SHA256).Hash -ne $ExeHash.ToUpper() -or
      (Get-FileHash -LiteralPath $pcopy -Algorithm SHA256).Hash -ne $ParamsHash.ToUpper()) { exit 13 }

  $planPath = Join-Path $DataDir 'desired.json'
  if (Test-Path -LiteralPath $planPath -PathType Leaf) {
    $plan = Get-Content -LiteralPath $planPath -Raw | ConvertFrom-Json
    if ($plan.guardTaskId -ceq ($TaskPath + $TaskName) -and $plan.owner -cmatch '^[0-9a-f]{32}$' -and $plan.names.dataDir -eq $DataDir -and
        $plan.names.guardTask -eq ($TaskPath + $TaskName)) {
      $description = 'CashPilot Secure VM guard. cashpilot-owner=' + $plan.owner
    }
  }
  $oldId = Get-CPDFileID $journal $exeRel
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

  Write-CPDFileRecord $journal $exeRel $exeId
  if ($oldId) {
    Write-CPDFileRecord $journal $backupRel $oldId
    if ((Get-CPDFileID $journal $exeRel) -cne $oldId) { throw 'The guard executable changed.' }
    [IO.File]::Replace($stagedExe, $copy, $exeBackup)
  } else { [IO.File]::Move($stagedExe, $copy) }
  $published = $true
  # -Wait matters for a GUI-subsystem exe.
  $p = Start-Process -FilePath $copy -ArgumentList @('--vm-setup', ('"' + $pcopy + '"')) -Wait -PassThru -WindowStyle Hidden
  $code = $p.ExitCode
  # Exit 2 rejects the input before the setup role changes the journal.
  $launched = $code -ne 2
} finally {
  if (-not $launched -and $published) {
    Remove-CPDFile $journal $exeRel $exeId
    if ($oldId -and (Get-CPDFileID $journal $backupRel) -ceq $oldId) {
      [IO.File]::Move($exeBackup, $copy)
    }
  }
  Remove-CPDFile $journal $paramsRel $paramsId
  if ($stopped -and (-not $launched -or $code -ne 0)) {
    $task = Get-OwnedTask
    if ($task -and $task.State.ToString() -ne 'Running') { Start-ScheduledTask -InputObject $task | Out-Null }
  }
  Remove-CPDFile $journal $stagedExeRel $exeId
  if (-not $launched -or $code -eq 0 -or $code -eq 3) {
    Remove-CPDFile $journal $backupRel $oldId
  } elseif ($oldId) {
    Write-Output ('kept: ' + $exeBackup + ' (previous guard copy)')
  }
}
exit $code
