//go:build windows

package winvm

import (
	"fmt"
	"os"
	"slices"
	"unsafe"

	"golang.org/x/sys/windows"
)

func openArtifact(path string, deleteAccess bool) (windows.Handle, error) {
	access := uint32(windows.FILE_READ_ATTRIBUTES)
	if deleteAccess {
		access |= windows.DELETE
	}
	return openArtifactWithAccess(path, access)
}

func openArtifactWithAccess(path string, access uint32) (windows.Handle, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, err
	}
	h, err := windows.CreateFile(p, access, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return windows.InvalidHandle, &os.PathError{Op: "open", Path: path, Err: err}
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		windows.CloseHandle(h)
		return windows.InvalidHandle, err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		windows.CloseHandle(h)
		return windows.InvalidHandle, untrackedFile(path)
	}
	return h, nil
}

func artifactHandleID(h windows.Handle) (string, error) {
	var id struct{ Volume, Low, High uint64 }
	if err := windows.GetFileInformationByHandleEx(h, windows.FileIdInfo, (*byte)(unsafe.Pointer(&id)), uint32(unsafe.Sizeof(id))); err != nil {
		return "", err
	}
	return fmt.Sprintf("%016x%016x%016x", id.Volume, id.High, id.Low), nil
}

func artifactID(path string) (string, error) {
	h, err := openArtifact(path, false)
	if err != nil {
		return "", err
	}
	defer windows.CloseHandle(h)
	return artifactHandleID(h)
}

func deleteArtifact(path string, ids []string) error {
	h, err := openArtifact(path, true)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	id, err := artifactHandleID(h)
	if err != nil {
		return err
	}
	if !slices.Contains(ids, id) {
		return untrackedFile(path)
	}
	deleteOnClose := byte(1)
	return windows.SetFileInformationByHandle(h, windows.FileDispositionInfo, &deleteOnClose, 1)
}
