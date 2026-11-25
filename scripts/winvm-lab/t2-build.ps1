<#
.SYNOPSIS
  Lab only (T2): build, or repair, one Secure VM lab instance through the real
  setup chain, and prove its fence.

.DESCRIPTION
  Runs elevated on the lab host. It does what the app's admin stub does: makes
  the instance's data folder and its guard folder admin-only, copies setup.ps1
  there and checks its SHA-256, then runs it with a lab build of CashPilot
  (go build -tags winvmlab). setup.ps1 copies the exe, checks it, and runs
  --vm-setup, which builds the VM, runs the fence proof and starts the guard.

  The guest passwords are made here once per instance and kept DPAPI-protected
  for this account under %LOCALAPPDATA%\cpd-lab, so a second run (Repair) uses
  the same ones. No host name, address or VM name is written here: everything
  comes in as a parameter, and the instance's names are cpd-svm-<n>.

.EXAMPLE
  .\t2-build.ps1 -Instance 1 -Exe .\CashPilot-Desktop-lab.exe -Repo C:\src\CashPilot-Desktop -Iso D:\iso\windows.iso -ImageIndex 6 -VhdxDir D:\cpd-lab -GuestMemoryGB 4 -MinHostRamGB 8 -KeepEnhancedSession
#>
param(
  [Parameter(Mandatory = $true)][ValidateRange(1, 9)][int]$Instance,
  [Parameter(Mandatory = $true)][string]$Exe,
  [Parameter(Mandatory = $true)][string]$Repo,
  [string]$Iso = '',
  [int]$ImageIndex = 1,
  [string]$ProductKey = '',
  [Parameter(Mandatory = $true)][string]$VhdxDir,
  [ValidateRange(2, 64)][int]$GuestMemoryGB = 4,
  [ValidateRange(0, 1024)][int]$MinHostRamGB = 0,
  [switch]$KeepEnhancedSession,
  [switch]$ApproveNetwork
)
$ErrorActionPreference = 'Stop'
$name = 'cpd-svm-' + $Instance
$data = Join-Path $env:ProgramData ('CashPilot\lab\' + $name)
$guard = Join-Path $data 'guard'
$taskPath = '\CashPilot\'
$taskName = 'VMGuard-' + $name

function Lock-Dir([string]$p) {
  if (Test-Path -LiteralPath $p) {
    $i = Get-Item -LiteralPath $p -Force
    if (-not $i.PSIsContainer -or ($i.Attributes -band [IO.FileAttributes]::ReparsePoint)) { throw ('not a plain folder: ' + $p) }
  } else { New-Item -ItemType Directory -Path $p -Force | Out-Null }
  $acl = New-Object Security.AccessControl.DirectorySecurity
  $acl.SetSecurityDescriptorSddlForm('O:BAD:PAI(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)')
  Set-Acl -LiteralPath $p -AclObject $acl
}

function New-LabPassword {
  $set = 'ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789'
  $b = New-Object byte[] 24
  [Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($b)
  'Cp9!' + (-join ($b | ForEach-Object { $set[$_ % $set.Length] }))
}

# The passwords: made once per instance, kept for this account only.
$credDir = Join-Path $env:LOCALAPPDATA 'cpd-lab'
New-Item -ItemType Directory -Force -Path $credDir | Out-Null
$credFile = Join-Path $credDir ($name + '.cred')
if (-not (Test-Path -LiteralPath $credFile)) {
  $pair = [ordered]@{ admin = (New-LabPassword); earner = (New-LabPassword) }
  ConvertTo-SecureString ($pair | ConvertTo-Json -Compress) -AsPlainText -Force | ConvertFrom-SecureString | Set-Content -LiteralPath $credFile
}
$sec = Get-Content -LiteralPath $credFile | ConvertTo-SecureString
$pair = [Runtime.InteropServices.Marshal]::PtrToStringBSTR([Runtime.InteropServices.Marshal]::SecureStringToBSTR($sec)) | ConvertFrom-Json

$sid = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
$params = [ordered]@{
  version = 1; vhdxDir = $VhdxDir; iso = $Iso; imageIndex = $ImageIndex; productKey = $ProductKey; timeZone = ''
  adminPassword = $pair.admin; earnerPassword = $pair.earner; userSid = $sid; extraExits = @(); approveNetwork = [bool]$ApproveNetwork
  lab = [ordered]@{ minHostRam = [uint64]$MinHostRamGB * 1GB; guestMemory = [uint64]$GuestMemoryGB * 1GB; keepEnhancedSession = [bool]$KeepEnhancedSession; instance = $Instance }
}
$paramsFile = Join-Path $credDir ($name + '.params.json')
[IO.File]::WriteAllText($paramsFile, ($params | ConvertTo-Json -Compress -Depth 4), (New-Object Text.UTF8Encoding $false))

Lock-Dir (Join-Path $env:ProgramData 'CashPilot')
New-Item -ItemType Directory -Force -Path (Join-Path $env:ProgramData 'CashPilot\lab') | Out-Null
Lock-Dir $data
Lock-Dir $guard
$src = Join-Path $Repo 'internal\winvm\scripts\setup.ps1'
$dst = Join-Path $guard 'setup.ps1'
Copy-Item -LiteralPath $src -Destination $dst -Force
if ((Get-FileHash -LiteralPath $dst).Hash -ne (Get-FileHash -LiteralPath $src).Hash) { throw 'setup.ps1 changed while it was copied' }
$exeHash = (Get-FileHash -LiteralPath $Exe -Algorithm SHA256).Hash
$ps = Join-Path ([Environment]::SystemDirectory) 'WindowsPowerShell\v1.0\powershell.exe'
$started = Get-Date
& $ps -NoProfile -NonInteractive -ExecutionPolicy Bypass -File $dst -Exe $Exe -ExeHash $exeHash -DataDir $data -TaskPath $taskPath -TaskName $taskName -Params $paramsFile
$code = $LASTEXITCODE
Remove-Item -LiteralPath $paramsFile -Force -ErrorAction SilentlyContinue
'setup exit code: ' + $code + ' after ' + [int]((Get-Date) - $started).TotalMinutes + ' min'
$log = Join-Path $data 'setup.log'
if (Test-Path -LiteralPath $log) { Get-Content -LiteralPath $log -Tail 60 }
exit $code
