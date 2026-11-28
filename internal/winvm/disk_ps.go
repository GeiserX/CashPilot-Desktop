package winvm

// The file ID includes its volume serial number. Pin the full resolved path
// and, on removal, compare the identity and set deletion on the same handle.
const diskIdentityPrelude = `Add-Type -TypeDefinition @'
using System;
using System.ComponentModel;
using System.IO;
using System.Runtime.InteropServices;
using System.Text;
using Microsoft.Win32.SafeHandles;

public static class CpdDisk {
  [StructLayout(LayoutKind.Sequential)]
  private struct FileId { public ulong Volume, Low, High; }

  [DllImport("kernel32.dll", CharSet=CharSet.Unicode, SetLastError=true)]
  private static extern SafeFileHandle CreateFile(string path, uint access, uint share,
    IntPtr security, uint creation, uint flags, IntPtr template);
  [DllImport("kernel32.dll", CharSet=CharSet.Unicode, SetLastError=true)]
  private static extern uint GetFinalPathNameByHandle(SafeFileHandle file, StringBuilder path, uint size, uint flags);
  [DllImport("kernel32.dll", SetLastError=true)]
  private static extern bool GetFileInformationByHandleEx(SafeFileHandle file, int kind, out FileId id, uint size);
  [DllImport("kernel32.dll", SetLastError=true)]
  private static extern bool SetFileInformationByHandle(SafeFileHandle file, int kind, ref byte info, uint size);

  public static string Identify(string path) { return Inspect(path, null, true); }
  public static string IdentifyTarget(string path) { return Inspect(path, null, false); }
  public static bool DeleteOwned(string path, string id) { return Inspect(path, id, true) != null; }
  private static string Inspect(string path, string want, bool exactPath) {
    string full = Path.GetFullPath(path);
    bool deleting = want != null;
    const uint readAttributes = 0x80, delete = 0x10000, openExisting = 3;
    using (var file = CreateFile(full, readAttributes | (deleting ? delete : 0),
      deleting ? 1u : 7u, IntPtr.Zero, openExisting, 0, IntPtr.Zero)) {
      if (file.IsInvalid) {
        int error = Marshal.GetLastWin32Error();
        if (error == 2 || error == 3) { return null; }
        throw new Win32Exception(error);
      }
      var resolved = new StringBuilder(32768);
      uint size = GetFinalPathNameByHandle(file, resolved, (uint)resolved.Capacity, 0);
      if (size == 0 || size >= resolved.Capacity) { throw new Win32Exception(Marshal.GetLastWin32Error()); }
      if (exactPath && !String.Equals(resolved.ToString(), @"\\?\" + full, StringComparison.OrdinalIgnoreCase)) { return null; }
      FileId id;
      if (!GetFileInformationByHandleEx(file, 18, out id, 24)) { throw new Win32Exception(Marshal.GetLastWin32Error()); }
      string found = id.Volume.ToString("x16") + id.High.ToString("x16") + id.Low.ToString("x16");
      if (deleting) {
        if (!String.Equals(found, want, StringComparison.Ordinal)) { return null; }
        byte flag = 1;
        if (!SetFileInformationByHandle(file, 4, ref flag, 1)) { throw new Win32Exception(Marshal.GetLastWin32Error()); }
      }
      return found;
    }
  }
}
'@
function Get-DiskIdentity([string]$path) { [CpdDisk]::IdentifyTarget($path) }
function Test-DiskInUse([string]$id) {
  try {
    foreach ($v in @(Get-VM -ErrorAction Stop)) {
      $drives = @(Get-VMHardDiskDrive -VM $v -ErrorAction Stop)
      foreach ($checkpoint in @(Get-VMSnapshot -VM $v -ErrorAction Stop)) {
        $drives += @(Get-VMHardDiskDrive -VMSnapshot $checkpoint -ErrorAction Stop)
      }
      foreach ($drive in $drives) {
        $current = [string]$drive.Path
        if (-not $current) { return $true }
        $visited = @{}
        while ($current) {
          $found = Get-DiskIdentity $current
          if (-not $found -or $found -ceq $id -or $visited.ContainsKey($found)) { return $true }
          $visited[$found] = $true
          $vhd = Get-VHD -Path $current -ErrorAction Stop
          if (-not $vhd) { return $true }
          if ([string]$vhd.VhdType -eq 'Differencing') {
            $current = [string]$vhd.ParentPath
            if (-not $current) { return $true }
          } elseif ([string]$vhd.VhdType -in @('Dynamic', 'Fixed')) {
            $current = ''
          } else { return $true }
        }
      }
    }
    return $false
  } catch { return $true }
}
`
