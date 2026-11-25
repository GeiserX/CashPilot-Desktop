<#
.SYNOPSIS
  Lab only (T2): read the fence proof of one Secure VM lab instance and check
  every step's outcome, the guard's status and the guard task.

.DESCRIPTION
  Runs on the lab host after t2-build.ps1 (each run of which, Repair
  included, runs the proof again). It prints the proof table from proof.json
  and exits 1 unless: every red step went red (tunnel-red and tailnet-red may
  be n/a), every green step stayed green, the checks held, desired.json has a
  proof date, status.json says the fence is held, and the guard task runs.
  Changes nothing.

.EXAMPLE
  .\t2-proof.ps1 -Instance 1
#>
param([Parameter(Mandatory = $true)][ValidateRange(1, 9)][int]$Instance)
$ErrorActionPreference = 'Stop'
$name = 'cpd-svm-' + $Instance
$data = Join-Path $env:ProgramData ('CashPilot\lab\' + $name)
$proof = Get-Content -LiteralPath (Join-Path $data 'proof.json') -Raw | ConvertFrom-Json
$bad = @()
$want = @{ red = 'went-red'; green = 'stayed-green'; check = 'held'; function = 'held' }
foreach ($s in @($proof.steps)) {
  '{0,-16} {1,-9} {2,-13} {3}' -f $s.id, $s.kind, $s.outcome, $s.detail
  $optional = @('tunnel-red', 'tailnet-red') -contains $s.id
  if ($s.outcome -ne $want[$s.kind] -and -not ($optional -and $s.outcome -eq 'n/a')) { $bad += $s.id }
}
$desired = Get-Content -LiteralPath (Join-Path $data 'desired.json') -Raw | ConvertFrom-Json
if (-not $desired.provedAt -or ([string]$desired.provedAt).StartsWith('0001')) { $bad += 'desired.json has no proof date' }
$status = Get-Content -LiteralPath (Join-Path $data 'status.json') -Raw | ConvertFrom-Json
'status: network={0} fence={1} reason={2}' -f $status.network, $status.fence, $status.reason
if ($status.fence -ne 'held') { $bad += 'status fence is ' + $status.fence }
$t = Get-ScheduledTask -TaskPath '\CashPilot\' -TaskName ('VMGuard-' + $name) -ErrorAction SilentlyContinue
if (-not $t -or $t.State.ToString() -ne 'Running') { $bad += 'the guard task does not run' }
if ($bad.Count -gt 0) { 'FAIL: ' + ($bad -join ', '); exit 1 }
'PASS'
exit 0
