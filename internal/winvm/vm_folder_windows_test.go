//go:build windows

package winvm

import (
	"slices"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"golang.org/x/sys/windows"
)

// TestOpenVMFolderAcceptsHyperVRules: after New-VM -Path the VM folder holds
// Hyper-V's rules beside the two private ones (live run 5). Remove must open
// it to delete the disk; any other principal is still refused.
func TestOpenVMFolderAcceptsHyperVRules(t *testing.T) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("creating a folder owned by Administrators requires elevation")
	}
	script := runtime.SecureVMDirectoryPS + vmFolderPS + `
$ErrorActionPreference = 'Stop'
$dir = Join-Path ([IO.Path]::GetTempPath()) ('cpd-vmfolder-' + [guid]::NewGuid().ToString('N'))
[CpdDirectory]::CreatePrivate($dir)
try {
  $owner = 'aaaabbbbccccddddeeeeffff00001111'
  $l = [CpdDirectory]::Pin($dir); $id = $l.Id; $l.Dispose()
  [IO.File]::WriteAllText((Join-Path $dir '.cashpilot-directory'), $owner + ':' + $id)
  function Add-Rule([string]$sid, [string]$rights, [string]$inherit, [string]$prop) {
    $acl = Get-Acl -LiteralPath $dir
    $acl.AddAccessRule([Security.AccessControl.FileSystemAccessRule]::new([Security.Principal.SecurityIdentifier]::new($sid), $rights, $inherit, $prop, 'Allow'))
    Set-Acl -LiteralPath $dir -AclObject $acl
  }
  Add-Rule 'S-1-3-0' 'FullControl' 'ContainerInherit, ObjectInherit' 'InheritOnly'
  Add-Rule 'S-1-5-83-0' 'CreateFiles, AppendData, Read, Synchronize' 'None' 'None'
  Add-Rule 'S-1-15-3-1024-2268835264-3721307629-241982045-173645152-1490879176-104643441-2915960892-1612460704' 'FullControl' 'None' 'None'
  # The root cause: the private-folder check refuses Hyper-V's rules.
  try { (Open-CPDDirectory $dir $false $owner).Dispose(); 'private-accepted' } catch { 'private-refused' }
  $lease = Open-CPDVMFolder $dir $owner
  'hyperv-ok ' + ($lease.Id -ceq $id)
  $lease.Dispose()
  try { $null = Open-CPDVMFolder $dir ('0' * 32); 'other-owner-accepted' } catch { 'other-owner-refused' }
  Add-Rule 'S-1-5-32-545' 'Read' 'None' 'None'
  try { $null = Open-CPDVMFolder $dir $owner; 'users-accepted' } catch { 'users-refused' }
} finally { Remove-Item -LiteralPath $dir -Recurse -Force }
`
	code, out := runFakePowerShell(t, script)
	got := string(out)
	for _, want := range []string{"private-refused", "hyperv-ok True", "other-owner-refused", "users-refused"} {
		if !strings.Contains(got, want) {
			t.Fatalf("exit %d, want %q in:\n%s", code, want, got)
		}
	}
}

// TestNetTransComponentsIncludeIPv4: the guard reads the installed protocols
// to require IPv4 as the only one on our vNIC. On live run 5 it read them
// from Control\Class, which refuses Administrators and holds no components
// for SYSTEM, so every check said "protocols: unread" and Setup failed.
func TestNetTransComponentsIncludeIPv4(t *testing.T) {
	code, out := runFakePowerShell(t, netTransPS+"\n$ErrorActionPreference = 'Stop'\n(Get-NetTransComponents) -join ','\n")
	got := strings.ToLower(string(out))
	for _, want := range []string{"ms_tcpip", "ms_tcpip6"} {
		if code != 0 || !slices.Contains(strings.Split(strings.TrimSpace(got), ","), want) {
			t.Fatalf("exit %d, want %s among the protocols: %s", code, want, out)
		}
	}
}
