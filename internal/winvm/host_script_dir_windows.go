//go:build windows

package winvm

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Match the instance ACL in internal/runtime/scripts/secure-vm-directory.ps1.
const privateScriptDirectorySDDL = "O:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"

// pinHostScriptDirectory holds the entire path open without delete sharing.
// A pre-existing folder is used only if it has the same ACL as an instance
// directory. Never apply the ACL to an unknown or pre-existing directory.
func pinHostScriptDirectory(dir string) (func(), error) {
	if len(filepath.VolumeName(dir)) != 2 || !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("expected a local drive path for the PowerShell script directory: %s", dir)
	}
	var paths []string
	for path := filepath.Dir(dir); ; path = filepath.Dir(path) {
		paths = append(paths, path)
		if path == filepath.Dir(path) {
			break
		}
	}
	var handles []windows.Handle
	release := func() {
		for _, h := range slices.Backward(handles) {
			windows.CloseHandle(h)
		}
	}
	fail := func(err error) (func(), error) {
		release()
		return nil, fmt.Errorf("open private PowerShell script directory %s: %w", dir, err)
	}
	// Directory data access makes Windows enforce the share modes for the lease.
	const directoryAccess = windows.FILE_LIST_DIRECTORY | windows.FILE_READ_ATTRIBUTES
	for _, path := range slices.Backward(paths) {
		h, err := openArtifactWithAccess(path, directoryAccess)
		if err != nil {
			return fail(err)
		}
		handles = append(handles, h)
	}
	const access = directoryAccess | windows.READ_CONTROL
	h, err := openArtifactWithAccess(dir, access)
	if errors.Is(err, os.ErrNotExist) {
		if err := createPrivateScriptDirectory(dir); err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			return fail(err)
		}
		h, err = openArtifactWithAccess(dir, access)
	}
	if err != nil {
		return fail(err)
	}
	handles = append(handles, h)
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return fail(err)
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 {
		return fail(errors.New("path is not a directory"))
	}
	if err := verifyPrivateScriptDirectory(h); err != nil {
		return fail(err)
	}
	return release, nil
}

func createPrivateScriptDirectory(path string) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString(privateScriptDirectorySDDL)
	if err != nil {
		return err
	}
	sa := windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(sa))
	if err := windows.CreateDirectory(p, &sa); err != nil {
		return &os.PathError{Op: "mkdir", Path: path, Err: err}
	}
	return nil
}

func verifyPrivateScriptDirectory(h windows.Handle) error {
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	control, _, err := sd.Control()
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}

	untrusted := errors.New("directory has untrusted access rules")
	if !(owner.IsWellKnown(windows.WinLocalSystemSid) || owner.IsWellKnown(windows.WinBuiltinAdministratorsSid)) ||
		control&windows.SE_DACL_PROTECTED == 0 || acl == nil || acl.AceCount != 2 {
		return untrusted
	}
	const fileAllAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | 0x1ff
	var system, admins bool
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			return err
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE ||
			ace.Header.AceFlags != windows.CONTAINER_INHERIT_ACE|windows.OBJECT_INHERIT_ACE ||
			ace.Mask != fileAllAccess {
			return untrusted
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		system = system || sid.IsWellKnown(windows.WinLocalSystemSid)
		admins = admins || sid.IsWellKnown(windows.WinBuiltinAdministratorsSid)
	}
	if !system || !admins {
		return untrusted
	}
	return nil
}
