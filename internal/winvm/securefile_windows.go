//go:build windows

package winvm

import (
	"errors"
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The access rules as protected DACLs: no rule is inherited from the folder.
// SY is SYSTEM, BA is Administrators, BU is Users; FA is full access, FR is
// read.
const (
	sddlAdminOnly  = "D:P(A;;FA;;;SY)(A;;FA;;;BA)"
	sddlUsersRead  = "D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FR;;;BU)"
	sddlUsersWrite = "D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FRFW;;;BU)"
)

// protect replaces the file's DACL with the access rule.
func protect(path string, a fileAccess) error {
	sddl := sddlAdminOnly
	switch a {
	case usersRead:
		sddl = sddlUsersRead
	case usersWrite:
		sddl = sddlUsersWrite
	}
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

// errNotAdminOwned is a file a standard user could have made.
var errNotAdminOwned = errors.New("the file is not owned by SYSTEM, Administrators or this account")

// ownedByAdmin checks the owner SID of an open file: SYSTEM, Administrators,
// or the account this process runs as. A standard user cannot make a file
// owned by any of them for the guard, which runs as SYSTEM.
func ownedByAdmin(f *os.File) error {
	sd, err := windows.GetSecurityInfo(windows.Handle(f.Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	if owner.IsWellKnown(windows.WinLocalSystemSid) || owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
		return nil
	}
	u, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err == nil && owner.Equals(u.User.Sid) {
		return nil
	}
	return errNotAdminOwned
}

// CheckSetupAccess checks the protected setup paths before the elevated role
// opens the password file. Only SYSTEM and Administrators may have access.
func CheckSetupAccess(paths ...string) error {
	isAdmin := func(sid *windows.SID) bool {
		return sid.IsWellKnown(windows.WinLocalSystemSid) || sid.IsWellKnown(windows.WinBuiltinAdministratorsSid)
	}
	for _, path := range paths {
		sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
			windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			return err
		}
		owner, _, err := sd.Owner()
		if err != nil {
			return err
		}
		dacl, _, err := sd.DACL()
		if err != nil {
			return err
		}
		if !isAdmin(owner) || dacl == nil || dacl.AceCount == 0 {
			return fmt.Errorf("setup path has untrusted permissions: %s", path)
		}
		for i := uint32(0); i < uint32(dacl.AceCount); i++ {
			var ace *windows.ACCESS_ALLOWED_ACE
			if err := windows.GetAce(dacl, i, &ace); err != nil {
				return err
			}
			if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE ||
				!isAdmin((*windows.SID)(unsafe.Pointer(&ace.SidStart))) {
				return fmt.Errorf("setup path has untrusted permissions: %s", path)
			}
		}
	}
	return nil
}
