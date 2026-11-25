# The elevated caller has verified this script and holds the directory leases.
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
Import-Module Microsoft.PowerShell.Utility
$journal = Open-CPDFileJournal $DataDir
$rel = 'guard\.remove-exe.' + [guid]::NewGuid().ToString('N') + '.exe'
$copy = Join-Path $DataDir $rel
$id = $null
try {
  $id = Copy-CPDFile $journal $rel $Exe
  if ((Get-FileHash -LiteralPath $copy -Algorithm SHA256).Hash -ne $ExeHash.ToUpper()) { exit 13 }
  $p = Start-Process -FilePath $copy -ArgumentList @('--vm-remove', ('"' + $DataDir + '"')) -Wait -PassThru -WindowStyle Hidden
  $code = $p.ExitCode
} finally {
  Remove-CPDFile $journal $rel $id
}
exit $code
