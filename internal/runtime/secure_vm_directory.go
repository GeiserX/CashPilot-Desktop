package runtime

import _ "embed"

// SecureVMDirectoryPS creates private NTFS directories and holds their
// ancestors open while elevated code uses a path.
//
//go:embed scripts/secure-vm-directory.ps1
var SecureVMDirectoryPS string

// SecureVMFilesPS reads and writes the same file journal as the Secure VM roles.
//
//go:embed scripts/secure-vm-files.ps1
var SecureVMFilesPS string
