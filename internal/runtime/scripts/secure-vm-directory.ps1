if (-not ('CpdDirectory' -as [type])) {
  Add-Type -TypeDefinition @'
using System;
using System.Collections.Generic;
using System.ComponentModel;
using System.IO;
using System.Runtime.InteropServices;
using System.Security.AccessControl;
using Microsoft.Win32.SafeHandles;

public static class CpdDirectory {
  [StructLayout(LayoutKind.Sequential)]
  private struct SecurityAttributes {
    public int Size;
    public IntPtr Descriptor;
    public int Inherit;
  }

  [StructLayout(LayoutKind.Sequential)]
  private struct FileId { public ulong Volume, Low, High; }

  [DllImport("kernel32.dll", CharSet=CharSet.Unicode, SetLastError=true)]
  private static extern bool CreateDirectory(string path, ref SecurityAttributes security);

  [DllImport("kernel32.dll", CharSet=CharSet.Unicode, SetLastError=true)]
  private static extern SafeFileHandle CreateFile(string path, uint access, uint share,
    IntPtr security, uint creation, uint flags, IntPtr template);

  [DllImport("kernel32.dll", SetLastError=true)]
  private static extern bool GetFileInformationByHandleEx(SafeFileHandle file, int kind, out FileId id, uint size);

  [DllImport("kernel32.dll", SetLastError=true)]
  private static extern bool SetFileInformationByHandle(SafeFileHandle file, int kind, ref byte info, uint size);

  private static string Identify(SafeFileHandle handle) {
    FileId id;
    if (!GetFileInformationByHandleEx(handle, 18, out id, 24)) {
      throw new Win32Exception(Marshal.GetLastWin32Error());
    }
    return id.Volume.ToString("x16") + id.High.ToString("x16") + id.Low.ToString("x16");
  }

  private static SafeFileHandle Open(string path, uint access) {
    // OPEN_REPARSE_POINT, BACKUP_SEMANTICS; deny delete sharing.
    var handle = CreateFile(path, access, 3, IntPtr.Zero, 3, 0x2200000, IntPtr.Zero);
    if (handle.IsInvalid) { throw new Win32Exception(Marshal.GetLastWin32Error()); }
    try {
      if ((File.GetAttributes(path) & FileAttributes.ReparsePoint) != 0) {
        throw new IOException("Refusing the reparse point: " + path);
      }
      return handle;
    } catch {
      handle.Dispose();
      throw;
    }
  }

  public static string FileID(string path) {
    using (var handle = Open(path, 0x80)) { return Identify(handle); }
  }

  public static bool DeleteOwned(string path, string id) {
    using (var handle = Open(path, 0x10080)) {
      if (id != Identify(handle)) { return false; }
      byte flag = 1;
      if (!SetFileInformationByHandle(handle, 4, ref flag, 1)) { throw new Win32Exception(Marshal.GetLastWin32Error()); }
      return true;
    }
  }

  private static SafeFileHandle OpenDirectory(string path) {
    var handle = Open(path, 0x80);
    if ((File.GetAttributes(path) & FileAttributes.Directory) == 0) {
      handle.Dispose();
      throw new IOException("Not a directory: " + path);
    }
    return handle;
  }

  public sealed class DirectoryLease : IDisposable {
    internal readonly List<SafeFileHandle> Handles = new List<SafeFileHandle>();
    public string Id { get { return Identify(Handles[Handles.Count - 1]); } }
    public void Dispose() {
      for (int i = Handles.Count - 1; i >= 0; i--) { Handles[i].Dispose(); }
      Handles.Clear();
    }
  }

  public static DirectoryLease Pin(string path) {
    string full = Path.GetFullPath(path);
    if (Path.GetPathRoot(full).Length != 3) { throw new IOException("Expected a local drive path."); }
    var parents = new Stack<string>();
    for (var dir = new DirectoryInfo(full); dir != null; dir = dir.Parent) { parents.Push(dir.FullName); }
    var lease = new DirectoryLease();
    try {
      while (parents.Count != 0) { lease.Handles.Add(OpenDirectory(parents.Pop())); }
      return lease;
    } catch {
      lease.Dispose();
      throw;
    }
  }

  public static void CreatePrivate(string path) {
    var acl = new DirectorySecurity();
    acl.SetSecurityDescriptorSddlForm("O:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)");
    byte[] descriptor = acl.GetSecurityDescriptorBinaryForm();
    var pin = GCHandle.Alloc(descriptor, GCHandleType.Pinned);
    try {
      var sa = new SecurityAttributes { Size = Marshal.SizeOf(typeof(SecurityAttributes)), Descriptor = pin.AddrOfPinnedObject() };
      if (!CreateDirectory(path, ref sa)) { throw new Win32Exception(Marshal.GetLastWin32Error()); }
    } finally { pin.Free(); }
  }
}
'@
}
function Assert-CPDPrivateDirectory([string]$path) {
  $acl = Get-Acl -LiteralPath $path
  $admins = @('S-1-5-18', 'S-1-5-32-544')
  $owner = $acl.GetOwner([Security.Principal.SecurityIdentifier]).Value
  $rules = @($acl.GetAccessRules($true, $true, [Security.Principal.SecurityIdentifier]))
  $inherit = [Security.AccessControl.InheritanceFlags]'ContainerInherit, ObjectInherit'
  if ($owner -notin $admins -or -not $acl.AreAccessRulesProtected -or $rules.Count -ne 2 -or
      @($rules.IdentityReference.Value | Select-Object -Unique).Count -ne 2 -or
      @($rules | Where-Object {
        $_.IdentityReference.Value -notin $admins -or $_.AccessControlType -ne 'Allow' -or $_.IsInherited -or
        $_.FileSystemRights -ne 'FullControl' -or $_.InheritanceFlags -ne $inherit -or $_.PropagationFlags -ne 'None'
      }).Count) {
    throw ('The folder has untrusted access rules: ' + $path)
  }
}

# The record is on the directory itself and binds the owner token to its NTFS ID.
function Open-CPDDirectory([string]$path, [bool]$create, [string]$owner) {
  $parent = [CpdDirectory]::Pin([IO.Path]::GetDirectoryName($path))
  try {
    $made = $false
    if ($create -and -not (Test-Path -LiteralPath $path)) {
      [CpdDirectory]::CreatePrivate($path)
      $made = $true
    }
    $lease = [CpdDirectory]::Pin($path)
    try {
      Assert-CPDPrivateDirectory $path
      $record = Join-Path $path '.cashpilot-directory'
      $want = $owner + ':' + $lease.Id
      if ($made) {
        $file = [IO.File]::Open($record, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write)
        try {
          $bytes = [Text.Encoding]::ASCII.GetBytes($want)
          $file.Write($bytes, 0, $bytes.Length)
          $file.Flush($true)
        } finally { $file.Dispose() }
      }
      $null = [CpdDirectory]::FileID($record)
      if ([IO.File]::ReadAllText($record) -cne $want) { throw ('The directory identity was not recorded: ' + $path) }
      return $lease
    } catch {
      $lease.Dispose()
      throw
    }
  } finally { $parent.Dispose() }
}
