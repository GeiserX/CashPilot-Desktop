<#
.SYNOPSIS
Installs CashPilot Desktop from its installer, uninstalls it twice, and checks
that every piece the install and the app put on the PC is gone, or kept when the
user asked to keep it.

.DESCRIPTION
Round 1 is the default uninstall. Before it, the script seeds what the app itself
creates: the background helper's Scheduled Task (started, so its process runs
from the install folder), the RunOnce value of a guided Docker setup, the data
folder, and three containers. After it, the app, the shortcuts, the Apps &
Features entry, the task, its process and the RunOnce value must be gone, and
the data and every container must still be there.

Round 2 installs again, seeds a native earner with its Windows Firewall rule, and
uninstalls with /REMOVEEARNERS /DELETEDATA. Then the data folder, the firewall
rule and the container CashPilot Desktop made must be gone, and the container a
CashPilot worker made, and an unrelated one, must still be there.

It changes this user's profile, the firewall and Docker, so it is for a
throwaway CI runner, and it needs admin rights for the firewall rule. It exits 1
when any check fails.

.EXAMPLE
pwsh -File scripts/windows-installer-check.ps1 -Installer build/bin/CashPilot-Desktop-setup.exe
#>
param(
    [Parameter(Mandatory = $true)] [string] $Installer
)

$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$script:failures = @()

function Check([bool] $ok, [string] $what) {
    if ($ok) { Write-Host "ok    $what" } else { Write-Host "FAIL  $what"; $script:failures += $what }
}

$Installer = (Resolve-Path $Installer).Path
$installDir = Join-Path $env:LOCALAPPDATA 'Programs\CashPilot Desktop'
$exe = Join-Path $installDir 'CashPilot Desktop.exe'
$uninstaller = Join-Path $installDir 'uninstall.exe'
$startMenuLink = Join-Path ([Environment]::GetFolderPath('Programs')) 'CashPilot Desktop.lnk'
$desktopLink = Join-Path ([Environment]::GetFolderPath('Desktop')) 'CashPilot Desktop.lnk'
$uninstKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\CashPilotDesktop'
$machineKey = 'HKLM:\Software\Microsoft\Windows\CurrentVersion\Uninstall\CashPilotDesktop'
$dataDir = Join-Path $env:APPDATA 'CashPilot Desktop'
$runOnceKey = 'HKCU:\Software\Microsoft\Windows\CurrentVersion\RunOnce'
$runOnceValue = '!CashPilotResumeSetup'
$taskPath = '\CashPilot\'
$taskName = 'Helper'

function Test-Task { $null -ne (Get-ScheduledTask -TaskPath $taskPath -TaskName $taskName -ErrorAction SilentlyContinue) }
function Test-RunOnce { $null -ne (Get-ItemProperty -Path $runOnceKey -Name $runOnceValue -ErrorAction SilentlyContinue) }
function Get-AppProcess { @(Get-CimInstance Win32_Process | Where-Object { $_.ExecutablePath -and $_.ExecutablePath -ieq $exe }) }
function Test-Container([string] $name) { (docker ps -a --filter "name=^$name$" --format '{{.Names}}') -eq $name }

function Install-App {
    $p = Start-Process -FilePath $Installer -ArgumentList '/S' -Wait -PassThru
    Check ($p.ExitCode -eq 0) "the installer exits 0 (got $($p.ExitCode))"
}

# The uninstaller copies itself to %TEMP% and runs from there, so the process
# started here ends at once. Wait for the copy's work: the entry goes last.
function Uninstall-App([string[]] $flags) {
    Start-Process -FilePath $uninstaller -ArgumentList (@('/S') + $flags) -Wait | Out-Null
    $deadline = (Get-Date).AddMinutes(4)
    while (((Test-Path $uninstKey) -or (Test-Path $installDir)) -and (Get-Date) -lt $deadline) { Start-Sleep -Seconds 1 }
}

function Check-Installed {
    Check (Test-Path $exe) "the app is in $installDir"
    Check (Test-Path $uninstaller) 'uninstall.exe is next to it'
    Check (Test-Path $startMenuLink) 'the Start menu shortcut exists'
    Check (Test-Path $desktopLink) 'the desktop shortcut exists'
    if (Test-Path $startMenuLink) {
        $target = (New-Object -ComObject WScript.Shell).CreateShortcut($startMenuLink).TargetPath
        Check ($target -ieq $exe) "the Start menu shortcut opens the installed app (got $target)"
    }
    Check (Test-Path $uninstKey) 'the Apps & Features entry exists, for this user'
    if (Test-Path $uninstKey) {
        $entry = Get-ItemProperty $uninstKey
        Check ($entry.DisplayName -eq 'CashPilot Desktop') "the entry is named CashPilot Desktop (got $($entry.DisplayName))"
        Check ($entry.UninstallString -eq "`"$uninstaller`"") "the entry runs uninstall.exe (got $($entry.UninstallString))"
        Check ($entry.QuietUninstallString -eq "`"$uninstaller`" /S") 'the entry has a silent uninstall command'
        Check ($entry.DisplayVersion -match '^\d+\.\d+\.\d+$') "the entry has a version (got $($entry.DisplayVersion))"
    }
    Check (-not (Test-Path $machineKey)) 'nothing is written for all users (HKLM)'
}

function Check-AppGone {
    Check (-not (Test-Path $installDir)) 'the install folder is gone'
    Check (-not (Test-Path $startMenuLink)) 'the Start menu shortcut is gone'
    Check (-not (Test-Path $desktopLink)) 'the desktop shortcut is gone'
    Check (-not (Test-Path $uninstKey)) 'the Apps & Features entry is gone'
    Check (-not (Test-Task)) 'the background helper task is gone'
    Check ((Get-AppProcess).Count -eq 0) 'no process runs from the install folder'
    Check (-not (Test-RunOnce)) "the RunOnce value $runOnceValue is gone"
}

# The installer asks for no admin rights: its manifest says asInvoker.
$manifest = [Text.Encoding]::ASCII.GetString([IO.File]::ReadAllBytes($Installer))
Check ($manifest -match 'requestedExecutionLevel\s+level="asInvoker"') 'the installer asks for no admin rights (asInvoker)'

$isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
Check $isAdmin 'this runner is elevated, so the firewall rule of round 2 can be made'

# Containers: one CashPilot Desktop made, one a CashPilot worker made (managed,
# no cashpilot.runtime), and one nothing of ours made. Windows containers, since
# the runner's Docker runs those; they are only created, never started.
# Some runners boot with the Docker service not started yet, so start it and wait
# for its pipe before the first docker command.
$dockerService = Get-Service -Name docker -ErrorAction SilentlyContinue
if ($dockerService -and $dockerService.Status -ne 'Running') { Start-Service -Name docker }
$deadline = (Get-Date).AddMinutes(3)
while (-not (Test-Path '\\.\pipe\docker_engine') -and (Get-Date) -lt $deadline) { Start-Sleep -Seconds 2 }
Check (Test-Path '\\.\pipe\docker_engine') 'the Docker engine answers on its pipe'
$build = [Environment]::OSVersion.Version.Build
$image = if ($build -ge 26100) { 'mcr.microsoft.com/windows/nanoserver:ltsc2025' } else { 'mcr.microsoft.com/windows/nanoserver:ltsc2022' }
docker pull $image | Out-Null
Check ($LASTEXITCODE -eq 0) "Docker pulled $image"
docker create --name cp-desktop-test --label cashpilot.managed=true --label cashpilot.runtime=existing-docker --label cashpilot.service=cp-test $image cmd | Out-Null
docker create --name cp-worker-test --label cashpilot.managed=true --label cashpilot.service=cp-test $image cmd | Out-Null
docker create --name cp-unrelated-test $image cmd | Out-Null
Check ((Test-Container 'cp-desktop-test') -and (Test-Container 'cp-worker-test') -and (Test-Container 'cp-unrelated-test')) 'the three test containers exist'

try {
    Write-Host '--- round 1: install, then the default uninstall (keep earners and data)'
    Check (-not (Test-Path $uninstKey)) 'nothing is installed before the test'
    Install-App
    Check-Installed

    New-Item -ItemType Directory -Force -Path (Join-Path $dataDir 'data') | Out-Null
    Set-Content -Path (Join-Path $dataDir 'config.json') -Value '{"firstRunComplete":true}'
    Set-Content -Path (Join-Path $dataDir 'data\keep-me.txt') -Value 'user data'
    # A fresh profile may have no RunOnce key yet; the app creates it the same way.
    if (-not (Test-Path $runOnceKey)) { New-Item -Path $runOnceKey -Force | Out-Null }
    Set-ItemProperty -Path $runOnceKey -Name $runOnceValue -Value "`"$exe`" --resume-setup"
    $action = New-ScheduledTaskAction -Execute $exe -Argument '--daemon'
    $trigger = New-ScheduledTaskTrigger -AtLogOn -User "$env:USERDOMAIN\$env:USERNAME"
    Register-ScheduledTask -TaskPath $taskPath -TaskName $taskName -Action $action -Trigger $trigger -Force | Out-Null
    Start-ScheduledTask -TaskPath $taskPath -TaskName $taskName
    $deadline = (Get-Date).AddSeconds(20)
    while ((Get-AppProcess).Count -eq 0 -and (Get-Date) -lt $deadline) { Start-Sleep -Milliseconds 500 }
    if ((Get-AppProcess).Count -eq 0) {
        # A logon task may not start in a CI session with no desktop. What the
        # uninstaller must deal with is the helper process itself, so start it
        # the way the task would.
        Write-Host 'note  the task did not start here; starting the helper directly'
        Start-Process -FilePath $exe -ArgumentList '--daemon' | Out-Null
        $deadline = (Get-Date).AddSeconds(20)
        while ((Get-AppProcess).Count -eq 0 -and (Get-Date) -lt $deadline) { Start-Sleep -Milliseconds 500 }
    }
    Check (Test-Task) 'seeded: the background helper task'
    Check ((Get-AppProcess).Count -gt 0) 'seeded: the helper runs from the install folder'
    Check (Test-RunOnce) "seeded: the RunOnce value $runOnceValue"

    Uninstall-App @()
    Check-AppGone
    Check (Test-Path (Join-Path $dataDir 'config.json')) 'the settings stay (the default keeps the data)'
    Check (Test-Path (Join-Path $dataDir 'data\keep-me.txt')) 'the data folder stays'
    Check (Test-Container 'cp-desktop-test') 'the Desktop container stays (the default keeps the earners)'
    Check (Test-Container 'cp-worker-test') 'the worker container stays'
    Check (Test-Container 'cp-unrelated-test') 'the unrelated container stays'

    Write-Host '--- round 2: install again, then uninstall with /REMOVEEARNERS /DELETEDATA'
    Install-App
    Check-Installed

    # A native earner as the app records it, and the firewall rule the app makes
    # for it: the rule's name carries a hash of the binary's path (firewall.go).
    $slug = 'cp-test'
    $binDir = Join-Path $dataDir "native\$slug"
    $bin = Join-Path $binDir 'cp-test.exe'
    New-Item -ItemType Directory -Force -Path $binDir | Out-Null
    Set-Content -Path $bin -Value 'not a real binary'
    $registry = @{ entries = @{ $slug = @{ slug = $slug; pid = 0; binPath = $bin; url = ''; sha256 = ''; archive = ''; firewallBlock = @('10.0.0.0/8'); startedAt = ''; desired = 'stopped' } } }
    $registry | ConvertTo-Json -Depth 5 | Set-Content -Path (Join-Path $dataDir 'native\state.json')
    $sha = [Security.Cryptography.SHA256]::Create().ComputeHash([Text.Encoding]::UTF8.GetBytes($bin.ToLowerInvariant()))
    $hash = -join ($sha[0..3] | ForEach-Object { $_.ToString('x2') })
    $ruleName = "CashPilot: block $slug from private networks ($hash)"
    New-NetFirewallRule -DisplayName $ruleName -Direction Outbound -Action Block -Program $bin -RemoteAddress 10.0.0.0/8 -Profile Any | Out-Null
    Check ($null -ne (Get-NetFirewallRule -DisplayName $ruleName -ErrorAction SilentlyContinue)) 'seeded: the native earner and its firewall rule'

    Uninstall-App @('/REMOVEEARNERS', '/DELETEDATA')
    Check-AppGone
    Check (-not (Test-Path $dataDir)) 'the data folder is gone (asked to delete the data)'
    Check ($null -eq (Get-NetFirewallRule -DisplayName $ruleName -ErrorAction SilentlyContinue)) "the native earner's firewall rule is gone"
    Check (-not (Test-Container 'cp-desktop-test')) 'the Desktop container is gone (asked to remove the earners)'
    Check (Test-Container 'cp-worker-test') 'the worker container stays: only Desktop containers go'
    Check (Test-Container 'cp-unrelated-test') 'the unrelated container stays'
}
finally {
    docker rm -f cp-desktop-test cp-worker-test cp-unrelated-test 2>$null | Out-Null
    Get-NetFirewallRule -DisplayName 'CashPilot: block cp-test *' -ErrorAction SilentlyContinue | Remove-NetFirewallRule
    Unregister-ScheduledTask -TaskPath $taskPath -TaskName $taskName -Confirm:$false -ErrorAction SilentlyContinue
}

if ($script:failures.Count -gt 0) {
    Write-Host ''
    Write-Host "$($script:failures.Count) check(s) failed:"
    $script:failures | ForEach-Object { Write-Host "  - $_" }
    exit 1
}
Write-Host ''
Write-Host 'Every installer check passed.'
