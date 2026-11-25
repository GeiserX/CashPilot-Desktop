<#
.SYNOPSIS
  Lab only: read the live state of one Secure VM lab instance. Changes nothing
  on Hyper-V.

.DESCRIPTION
  Copies the CashPilot exe into the lab folder, runs it with --vm-inspect
  against the lab's desired.json, and saves the JSON report there. The only
  write is into the lab folder. No host name, address or VM name is written
  here: everything comes in as a parameter.

.EXAMPLE
  .\read.ps1 -Exe .\CashPilot-Desktop.exe -LabDir C:\ProgramData\CashPilot\lab\cpd-svm-1 -Desired C:\ProgramData\CashPilot\lab\cpd-svm-1\desired.json
#>
param(
  [Parameter(Mandatory = $true)][string]$Exe,
  [Parameter(Mandatory = $true)][string]$LabDir,
  [Parameter(Mandatory = $true)][string]$Desired
)
$ErrorActionPreference = 'Stop'

if (-not (Test-Path -LiteralPath $Exe -PathType Leaf)) { throw "No exe at $Exe" }
if (-not (Test-Path -LiteralPath $Desired -PathType Leaf)) { throw "No desired.json at $Desired" }
if (-not (Test-Path -LiteralPath $LabDir -PathType Container)) { New-Item -ItemType Directory -Path $LabDir | Out-Null }

$copy = Join-Path $LabDir 'cashpilot-inspect.exe'
Copy-Item -LiteralPath $Exe -Destination $copy -Force
$report = Join-Path $LabDir 'inspect.json'
$errors = Join-Path $LabDir 'inspect.err.txt'

# The exe is a GUI-subsystem binary, so a bare call would return at once.
# Start-Process -Wait waits for it, and the redirects catch its output.
$p = Start-Process -FilePath $copy -ArgumentList @('--vm-inspect', ('"' + $Desired + '"')) -Wait -PassThru -NoNewWindow -RedirectStandardOutput $report -RedirectStandardError $errors
Get-Content -LiteralPath $report -Raw
if ((Get-Item -LiteralPath $errors).Length -gt 0) { Get-Content -LiteralPath $errors -Raw | Write-Warning }
'exit code: ' + $p.ExitCode
exit $p.ExitCode
