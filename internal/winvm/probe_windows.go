//go:build windows

package winvm

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// systemProbes reads this Windows PC, without admin rights.
type systemProbes struct{}

var system probes = systemProbes{}

// createNoWindow keeps a child process from flashing a console over the app.
const createNoWindow = 0x08000000

func hidden(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	return cmd
}

// cimCommand runs script in System32's Windows PowerShell, by its full path,
// never from PATH.
func cimCommand(ctx context.Context, script string) (*exec.Cmd, func(), error) {
	return filePowerShellCommand(ctx, "", script)
}

func (systemProbes) supported() bool { return true }

// osVersion reads the build and the edition from the registry. The 64-bit view is
// asked for by name, so a 32-bit build of the app would read the same values.
func (systemProbes) osVersion(context.Context) (func(*HostFacts), error) {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Windows NT\CurrentVersion`, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return nil, err
	}
	defer k.Close()
	buildText, _, err := k.GetStringValue("CurrentBuild")
	if err != nil {
		return nil, err
	}
	build, err := strconv.Atoi(strings.TrimSpace(buildText))
	if err != nil {
		return nil, fmt.Errorf("CurrentBuild %q is not a number", buildText)
	}
	ubr, _, _ := k.GetIntegerValue("UBR")
	edition, _, _ := k.GetStringValue("EditionID")
	return func(f *HostFacts) {
		f.Build = build
		f.UBR = int(ubr)
		f.EditionID = strings.TrimSpace(edition)
	}, nil
}

// processor asks Windows directly. IsWow64Process2 needs Windows 10 1709; on an
// older build the call fails and CIM's Win32_Processor.Architecture stands.
func (systemProbes) processor(context.Context) (func(*HostFacts), error) {
	var process, native uint16
	if err := windows.IsWow64Process2(windows.CurrentProcess(), &process, &native); err != nil {
		native = 0
	}
	return processorFacts(native,
		windows.IsProcessorFeaturePresent(windows.PF_VIRT_FIRMWARE_ENABLED),
		windows.IsProcessorFeaturePresent(windows.PF_SECOND_LEVEL_ADDRESS_TRANSLATION)), nil
}

// An unelevated app reads the same facts directly through WMI. PowerShell
// scripts require access to the Administrators-only directory.
func (systemProbes) cpu(ctx context.Context) (func(*HostFacts), error) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return unelevatedWMI.cpu(ctx)
	}
	return runCIM(ctx, cpuScript)
}

func (systemProbes) features(ctx context.Context) (func(*HostFacts), error) {
	if !windows.GetCurrentProcessToken().IsElevated() {
		return unelevatedWMI.features(ctx)
	}
	return runCIM(ctx, featureScript)
}

func runCIM(ctx context.Context, script string) (func(*HostFacts), error) {
	cmd, release, err := cimCommand(ctx, script)
	if err != nil {
		return nil, err
	}
	defer release()
	out, err := cmd.Output()
	if err != nil {
		return nil, cimError(ctx.Err(), err)
	}
	return parseCIM(out)
}

// wsl looks for the WSL package and asks it its version. Only the package's own
// wsl.exe under Program Files is run, never the inbox System32 one: on a PC without
// WSL that one can offer to install it, and this check changes nothing.
func (systemProbes) wsl(ctx context.Context) (func(*HostFacts), error) {
	pf := os.Getenv("ProgramFiles")
	if pf == "" {
		return nil, errors.New("ProgramFiles is not set")
	}
	exe := filepath.Join(pf, "WSL", "wsl.exe")
	if _, err := os.Stat(exe); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return func(f *HostFacts) { f.WSLInstalled = No }, nil
		}
		return nil, err
	}
	cmd := hidden(ctx, exe, "--version")
	cmd.Env = append(os.Environ(), "WSL_UTF8=1")
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	version := ""
	if err == nil {
		version = parseWSLVersion(out)
	}
	return func(f *HostFacts) {
		f.WSLInstalled = Yes
		f.WSLVersion = version
	}, nil
}

// docker reads whether Docker Desktop is installed, running and was ever started
// on this account, the state of its service, and the backend its settings choose.
func (systemProbes) docker(ctx context.Context) (func(*HostFacts), error) {
	installed := dockerInstalled()
	running := processRunning("Docker Desktop.exe")
	started := Unknown
	backend := BackendUnknown
	if appData := os.Getenv("APPDATA"); appData != "" {
		var errs []error
		for _, name := range []string{"settings-store.json", "settings.json"} {
			path := filepath.Join(appData, "Docker", name)
			_, err := os.Stat(path)
			errs = append(errs, err)
			if err == nil && backend == BackendUnknown {
				if data, err := os.ReadFile(path); err == nil {
					backend = dockerBackend(data)
				}
			}
		}
		started = settingsFileFound(errs...)
	}
	service := serviceState("com.docker.service")
	var engineReason string
	if running == Yes {
		engineReason = runtime.DockerDesktopEngineReason(ctx)
	}

	return func(f *HostFacts) {
		f.DockerDesktopInstalled = installed
		f.DockerDesktopRunning = running
		f.DockerDesktopStarted = started
		f.DockerService = service
		f.DockerBackend = backend
		f.desktopEngineReason = engineReason
	}, nil
}

// services reads the Server service's start type and looks for VirtualBox's and
// VMware's driver services. Querying a service's configuration is open to every
// user.
func (systemProbes) services(context.Context) (func(*HostFacts), error) {
	lanman := serviceStartType("LanmanServer")
	others, names := otherHypervisors(serviceExists("VBoxSup"), serviceExists("vmx86"))
	return func(f *HostFacts) {
		f.LanmanServer = lanman
		f.OtherHypervisors = others
		f.OtherHypervisorNames = names
	}, nil
}

// openService opens one service with the given rights. absent is true when the
// service does not exist.
func openService(name string, access uint32) (m, s windows.Handle, absent bool, err error) {
	m, err = windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return 0, 0, false, err
	}
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		windows.CloseServiceHandle(m)
		return 0, 0, false, err
	}
	s, err = windows.OpenService(m, n, access)
	if err != nil {
		windows.CloseServiceHandle(m)
		return 0, 0, errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST), err
	}
	return m, s, false, nil
}

// serviceExists is Yes when the service opens, No when the service manager says
// it does not exist, Unknown on any other failure.
func serviceExists(name string) Tri {
	m, s, absent, err := openService(name, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		if absent {
			return No
		}
		return Unknown
	}
	windows.CloseServiceHandle(s)
	windows.CloseServiceHandle(m)
	return Yes
}

// serviceStartType reads a service's start type: auto, manual, disabled, absent
// or unknown.
func serviceStartType(name string) string {
	m, s, absent, err := openService(name, windows.SERVICE_QUERY_CONFIG)
	if err != nil {
		if absent {
			return "absent"
		}
		return "unknown"
	}
	defer windows.CloseServiceHandle(m)
	defer windows.CloseServiceHandle(s)
	var needed uint32
	// The first call asks how big the answer is.
	_ = windows.QueryServiceConfig(s, nil, 0, &needed)
	if needed == 0 {
		return "unknown"
	}
	buf := make([]byte, needed)
	cfg := (*windows.QUERY_SERVICE_CONFIG)(unsafe.Pointer(&buf[0]))
	if err := windows.QueryServiceConfig(s, cfg, needed, &needed); err != nil {
		return "unknown"
	}
	return startTypeName(cfg.StartType)
}

// dockerInstalled is Yes when the uninstall key or the app's exe is found, No when
// every place was read and none had it, Unknown when one could not be read.
func dockerInstalled() Tri {
	const uninstall = `SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\Docker Desktop`
	certain := true
	for _, root := range []registry.Key{registry.LOCAL_MACHINE, registry.CURRENT_USER} {
		k, err := registry.OpenKey(root, uninstall, registry.QUERY_VALUE|registry.WOW64_64KEY)
		if err == nil {
			k.Close()
			return Yes
		}
		if !errors.Is(err, registry.ErrNotExist) {
			certain = false
		}
	}
	var paths []string
	if pf := os.Getenv("ProgramFiles"); pf != "" {
		paths = append(paths, filepath.Join(pf, "Docker", "Docker", "Docker Desktop.exe"))
	}
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		paths = append(paths, filepath.Join(local, "Programs", "DockerDesktop", "Docker Desktop.exe"))
	}
	for _, p := range paths {
		_, err := os.Stat(p)
		if err == nil {
			return Yes
		}
		if !errors.Is(err, fs.ErrNotExist) {
			certain = false
		}
	}
	if certain {
		return No
	}
	return Unknown
}

// processRunning walks the process list for an exe name.
func processRunning(exe string) Tri {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return Unknown
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		if strings.EqualFold(windows.UTF16ToString(e.ExeFile[:]), exe) {
			return Yes
		}
	}
	if errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return No
	}
	return Unknown
}

// serviceState asks the service manager for one service's state. Querying status
// is open to every user.
func serviceState(name string) string {
	m, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return "unknown"
	}
	defer windows.CloseServiceHandle(m)
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return "unknown"
	}
	s, err := windows.OpenService(m, n, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return "absent"
		}
		return "unknown"
	}
	defer windows.CloseServiceHandle(s)
	var st windows.SERVICE_STATUS
	if err := windows.QueryServiceStatus(s, &st); err != nil {
		return "unknown"
	}
	switch st.CurrentState {
	case windows.SERVICE_RUNNING:
		return "running"
	case windows.SERVICE_STOPPED:
		return "stopped"
	case windows.SERVICE_START_PENDING:
		return "starting"
	}
	return "unknown"
}

// account reads the signed-in user's SID and its docker-users membership, both on
// the group's member list and in this session's token. docker-users is the name
// Docker Desktop gives the group on every language of Windows; it is looked up on
// this computer by name once, and everything after that compares SIDs.
func (systemProbes) account(context.Context) (func(*HostFacts), error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	userSID := user.User.Sid
	sidText := userSID.String()
	group, listed, token := Unknown, Unknown, Unknown
	computer, err := windows.ComputerName()
	if err == nil {
		gsid, _, _, err := windows.LookupSID("", computer+`\docker-users`)
		switch {
		case errors.Is(err, windows.ERROR_NONE_MAPPED):
			group, listed, token = No, No, No
		case err == nil:
			group = Yes
			if in, err := windows.Token(0).IsMember(gsid); err == nil {
				token = triOf(in)
			}
			if in, err := localGroupHas("docker-users", userSID); err == nil {
				listed = triOf(in)
			}
		}
	}
	return func(f *HostFacts) {
		f.UserSID = sidText
		f.DockerUsersGroup = group
		f.DockerUsersListed = listed
		f.DockerUsersToken = token
	}, nil
}

var (
	modnetapi32                 = windows.NewLazySystemDLL("netapi32.dll")
	procNetLocalGroupGetMembers = modnetapi32.NewProc("NetLocalGroupGetMembers")
)

// localGroupHas reads a local group's member list at level 0 (one SID per member)
// and reports whether sid is on it. Get-LocalGroupMember is not used: it throws on
// a group holding a member it cannot resolve, such as a deleted or Entra account.
func localGroupHas(group string, sid *windows.SID) (bool, error) {
	g, err := windows.UTF16PtrFromString(group)
	if err != nil {
		return false, err
	}
	const maxPreferredLength = 0xFFFFFFFF
	var buf *byte
	var read, total uint32
	var resume uintptr
	r, _, _ := procNetLocalGroupGetMembers.Call(0, uintptr(unsafe.Pointer(g)), 0,
		uintptr(unsafe.Pointer(&buf)), maxPreferredLength,
		uintptr(unsafe.Pointer(&read)), uintptr(unsafe.Pointer(&total)), uintptr(unsafe.Pointer(&resume)))
	if buf != nil {
		defer windows.NetApiBufferFree(buf)
	}
	if r != 0 {
		return false, syscall.Errno(r)
	}
	if read == 0 {
		return false, nil
	}
	// LOCALGROUP_MEMBERS_INFO_0 is a single PSID.
	for _, member := range unsafe.Slice((**windows.SID)(unsafe.Pointer(buf)), read) {
		if member != nil && member.Equals(sid) {
			return true, nil
		}
	}
	return false, nil
}

// restart reads the three markers Windows leaves while it waits for a restart.
func (systemProbes) restart(context.Context) (func(*HostFacts), error) {
	var reasons []string
	certain := true
	keyExists := func(path string) bool {
		k, err := registry.OpenKey(registry.LOCAL_MACHINE, path, registry.QUERY_VALUE|registry.WOW64_64KEY)
		if err == nil {
			k.Close()
			return true
		}
		if !errors.Is(err, registry.ErrNotExist) {
			certain = false
		}
		return false
	}
	if keyExists(`SOFTWARE\Microsoft\Windows\CurrentVersion\Component Based Servicing\RebootPending`) {
		reasons = append(reasons, "cbs")
	}
	if keyExists(`SOFTWARE\Microsoft\Windows\CurrentVersion\WindowsUpdate\Auto Update\RebootRequired`) {
		reasons = append(reasons, "windows-update")
	}
	if k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Session Manager`, registry.QUERY_VALUE|registry.WOW64_64KEY); err == nil {
		ops, _, err := k.GetStringsValue("PendingFileRenameOperations")
		k.Close()
		switch {
		case err == nil && hasNonEmpty(ops):
			reasons = append(reasons, "file-rename")
		case err != nil && !errors.Is(err, registry.ErrNotExist):
			certain = false
		}
	} else {
		certain = false
	}
	pending := restartPending(reasons, certain)
	return func(f *HostFacts) {
		f.RestartPending = pending
		f.RestartReasons = append([]string{}, reasons...)
	}, nil
}

func hasNonEmpty(values []string) bool {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return true
		}
	}
	return false
}
