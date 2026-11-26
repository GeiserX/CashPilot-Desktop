//go:build windows

package winvm

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var scriptFileMu sync.Mutex

func filePowerShellCommand(ctx context.Context, dir, script string) (*exec.Cmd, func(), error) {
	path, release, err := preparePowerShellFile(dir, script)
	if err != nil {
		return nil, nil, err
	}
	cmd, err := hostPowerShellCommand(ctx, "-ExecutionPolicy", "Bypass", "-File", path)
	if err != nil {
		release()
		return nil, nil, err
	}
	return cmd, release, nil
}

// preparePowerShellFile pins the script against changes and deletion until the
// caller releases it. Installed instances journal one copy of each fixed script
// so that the guard can reuse it and Remove can clean it up.
func preparePowerShellFile(dir, script string) (string, func(), error) {
	// Windows PowerShell 5.1 treats UTF-8 without a BOM as the ANSI code page.
	body := []byte("\xef\xbb\xbf" + script)
	sum := sha256.Sum256(body)
	if dir == "" {
		return privateHostPowerShellFile(body, sum)
	}

	root, err := openArtifact(dir, false)
	if err != nil {
		return "", nil, err
	}
	path := filepath.Join(dir, fmt.Sprintf("cashpilot-%x.ps1", sum))
	scriptFileMu.Lock()
	f, err := openPowerShellFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		err = writeAtomic(path, body, adminOnly)
		if err == nil {
			f, err = openPowerShellFile(path)
		}
	}
	scriptFileMu.Unlock()
	if err == nil {
		err = verifyPowerShellFile(f, len(body), sum)
	}
	if err != nil {
		if f != nil {
			f.Close()
		}
		windows.CloseHandle(root)
		return "", nil, fmt.Errorf("prepare PowerShell script %s: %w", path, err)
	}
	return path, func() {
		f.Close()
		windows.CloseHandle(root)
	}, nil
}

// privateHostPowerShellFile stages and removes one script for a host read
// outside an instance. Each call has its own file so separate processes can
// read the same host without replacing each other's scripts.
func privateHostPowerShellFile(body []byte, sum [sha256.Size]byte) (string, func(), error) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return "", nil, fmt.Errorf("launch from the private PowerShell script directory: %w", windows.ERROR_ELEVATION_REQUIRED)
	}
	programData, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0)
	if err != nil {
		return "", nil, err
	}
	dir, err := hostScriptDirectory(programData)
	if err != nil {
		return "", nil, err
	}
	release, err := pinHostScriptDirectory(dir)
	if err != nil {
		return "", nil, err
	}
	path, removeScript, err := stageHostPowerShellFile(dir, body, sum)
	if err != nil {
		release()
		return "", nil, err
	}
	return path, func() {
		removeScript()
		release()
	}, nil
}

func stageHostPowerShellFile(dir string, body []byte, sum [sha256.Size]byte) (string, func(), error) {
	f, err := createAdminPowerShellFile(dir)
	if err != nil {
		return "", nil, err
	}
	path := f.Name()
	id, err := artifactHandleID(windows.Handle(f.Fd()))
	if err != nil {
		f.Close()
		return "", nil, err
	}
	remove := func() { _ = deleteArtifact(path, []string{id}) }
	abort := func(err error) (string, func(), error) {
		f.Close()
		remove()
		return "", nil, err
	}
	if _, err := f.Write(body); err != nil {
		return abort(err)
	}
	if err := f.Close(); err != nil {
		return abort(err)
	}
	f, err = openPowerShellFile(path)
	if err != nil {
		remove()
		return "", nil, err
	}
	if err := verifyPowerShellFile(f, len(body), sum); err != nil {
		return abort(err)
	}
	return path, func() { f.Close(); remove() }, nil
}

func createAdminPowerShellFile(dir string) (*os.File, error) {
	path := filepath.Join(dir, randomArtifactName("cashpilot-", ".ps1"))
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	// Set the owner and the protected DACL when the file is created.
	sd, err := windows.SecurityDescriptorFromString("O:BA" + sddlAdminOnly)
	if err != nil {
		return nil, err
	}
	sa := windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(sa))
	h, err := windows.CreateFile(p, windows.GENERIC_WRITE, 0, &sa, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "create", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}

func openPowerShellFile(path string) (*os.File, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		windows.CloseHandle(h)
		return nil, err
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		windows.CloseHandle(h)
		return nil, errNotRegular
	}
	return os.NewFile(uintptr(h), path), nil
}

func verifyPowerShellFile(f *os.File, size int, sum [sha256.Size]byte) error {
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(f, int64(size)+1))
	if err != nil {
		return err
	}
	if n != int64(size) || [sha256.Size]byte(hash.Sum(nil)) != sum {
		return errors.New("PowerShell script SHA-256 mismatch")
	}
	return nil
}
