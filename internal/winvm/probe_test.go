package winvm

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"testing"
	"time"
	"unicode/utf16"
)

// fakeProbes answers each probe from a function; a nil one blocks until the
// deadline, the way a hung PowerShell would.
type fakeProbes struct {
	unsupported                                                bool
	osV, procV, cimV, featV, wslV, dockerV, accountV, restartV func(*HostFacts)
	servicesV                                                  func(*HostFacts)
	cimErr                                                     error
}

func block(ctx context.Context) (func(*HostFacts), error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func answer(ctx context.Context, apply func(*HostFacts), err error) (func(*HostFacts), error) {
	if apply == nil && err == nil {
		return block(ctx)
	}
	return apply, err
}

func (p fakeProbes) supported() bool { return !p.unsupported }
func (p fakeProbes) osVersion(ctx context.Context) (func(*HostFacts), error) {
	return answer(ctx, p.osV, nil)
}
func (p fakeProbes) processor(ctx context.Context) (func(*HostFacts), error) {
	if p.procV == nil {
		return func(*HostFacts) {}, nil
	}
	return p.procV, nil
}
func (p fakeProbes) cpu(ctx context.Context) (func(*HostFacts), error) {
	return answer(ctx, p.cimV, p.cimErr)
}
func (p fakeProbes) features(ctx context.Context) (func(*HostFacts), error) {
	return answer(ctx, p.featV, nil)
}
func (p fakeProbes) wsl(ctx context.Context) (func(*HostFacts), error) {
	return answer(ctx, p.wslV, nil)
}
func (p fakeProbes) docker(ctx context.Context) (func(*HostFacts), error) {
	return answer(ctx, p.dockerV, nil)
}
func (p fakeProbes) account(ctx context.Context) (func(*HostFacts), error) {
	return answer(ctx, p.accountV, nil)
}
func (p fakeProbes) restart(ctx context.Context) (func(*HostFacts), error) {
	return answer(ctx, p.restartV, nil)
}
func (p fakeProbes) services(ctx context.Context) (func(*HostFacts), error) {
	return answer(ctx, p.servicesV, nil)
}

func TestSnapshotAppliesEveryProbe(t *testing.T) {
	p := fakeProbes{
		osV:      func(f *HostFacts) { f.Build, f.EditionID = 22631, "Core" },
		cimV:     func(f *HostFacts) { f.Arch, f.HypervisorPresent, f.Firmware = "x64", No, No },
		featV:    func(f *HostFacts) { f.VMPlatformFeature = Yes },
		wslV:     func(f *HostFacts) { f.WSLInstalled, f.WSLVersion = Yes, "2.0.9.0" },
		dockerV:  func(f *HostFacts) { f.DockerDesktopInstalled = No },
		accountV: func(f *HostFacts) { f.UserSID = "S-1-5-21-1-2-3-1001" },
		restartV: func(f *HostFacts) { f.RestartPending, f.RestartReasons = Yes, []string{"cbs"} },
		servicesV: func(f *HostFacts) {
			f.LanmanServer, f.OtherHypervisors, f.OtherHypervisorNames = "manual", Yes, []string{"VirtualBox"}
		},
	}
	f := snapshot(context.Background(), p, "windows", time.Second)
	if f.LanmanServer != "manual" || f.OtherHypervisors != Yes {
		t.Errorf("the services probe was not applied: %+v", f)
	}
	if !f.Supported || f.Build != 22631 || f.Arch != "x64" || f.UserSID == "" || f.RestartPending != Yes {
		t.Fatalf("a probe's answer was not applied: %+v", f)
	}
	if f.Virtualization != No {
		t.Errorf("no hypervisor and firmware off derived %s, want no", f.Virtualization)
	}
	if f.WSL != WSLOutdated {
		t.Errorf("WSL 2.0.9 derived %s, want outdated", f.WSL)
	}
	if f.SecureVMHost.Reason != HostHomeEdition {
		t.Errorf("Home derived %+v", f.SecureVMHost)
	}
	if len(f.Problems) != 0 {
		t.Errorf("no probe failed, but Problems = %v", f.Problems)
	}
}

// A probe that never answers must not hold the reading past its deadline, and its
// facts must stay unknown rather than read as "no".
func TestSnapshotDegradesAHungProbeToUnknown(t *testing.T) {
	p := fakeProbes{
		osV:     func(f *HostFacts) { f.Build, f.EditionID = 26100, "Professional" },
		cimErr:  errors.New("CIM refused"),
		wslV:    func(f *HostFacts) { f.WSLInstalled, f.WSLVersion = Yes, "2.3.26.0" },
		dockerV: func(f *HostFacts) { f.DockerDesktopInstalled = Yes },
		// account and restart hang.
	}
	start := time.Now()
	f := snapshot(context.Background(), p, "windows", 150*time.Millisecond)
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("a hung probe held the reading for %v", took)
	}
	if f.Problems["account"] != "timed out" || f.Problems["restart"] != "timed out" {
		t.Errorf("the hung probes are not reported as timed out: %v", f.Problems)
	}
	if f.Problems["cpu"] == "" || f.Problems["features"] != "timed out" {
		t.Errorf("the failed CIM probe is not reported: %v", f.Problems)
	}
	if f.DockerUsersToken != Unknown || f.RestartPending != Unknown {
		t.Errorf("a hung probe's facts read as known: token %s, restart %s", f.DockerUsersToken, f.RestartPending)
	}
	if f.Virtualization != Unknown {
		t.Errorf("CIM failed, but virtualization derived %s", f.Virtualization)
	}
	if f.Build != 26100 || f.DockerDesktopInstalled != Yes {
		t.Errorf("the probes that answered were dropped: %+v", f)
	}
}

// The CI runner showed it: Win32_OptionalFeature can outlast the deadline. A slow
// feature read must not cost the CPU facts read beside it.
func TestSnapshotKeepsCPUFactsWhenTheFeatureReadHangs(t *testing.T) {
	p := fakeProbes{
		osV:      func(f *HostFacts) { f.Build, f.EditionID = 26100, "Professional" },
		cimV:     func(f *HostFacts) { f.Arch, f.HypervisorPresent = "x64", Yes },
		wslV:     func(f *HostFacts) { f.WSLInstalled = No },
		dockerV:  func(f *HostFacts) {},
		accountV: func(f *HostFacts) {},
		restartV: func(f *HostFacts) {},
		// features hangs.
	}
	f := snapshot(context.Background(), p, "windows", 150*time.Millisecond)
	if f.Arch != "x64" || f.Virtualization != Yes || f.SecureVMHost.Eligible != Yes {
		t.Errorf("a hung feature read cost the CPU facts: %+v", f)
	}
	if f.VMPlatformFeature != Unknown || f.Problems["features"] != "timed out" {
		t.Errorf("the hung feature read: %s, %v", f.VMPlatformFeature, f.Problems)
	}
}

// The native processor read wins over CIM for the CPU, in whichever order the
// two answers land, and CIM fills in only what the native read lacked.
func TestNativeProcessorReadWinsOverCIM(t *testing.T) {
	native := processorFacts(0xAA64, true, true)
	cim, err := parseCIM([]byte(`{"hypervisorPresent":true,"architecture":9,"firmware":false,"slat":false}`))
	if err != nil {
		t.Fatal(err)
	}
	for _, order := range [][]func(*HostFacts){{native, cim}, {cim, native}} {
		f := unknownFacts("windows")
		for _, apply := range order {
			apply(&f)
		}
		if f.Arch != "arm64" || f.Firmware != Yes || f.SLAT != Yes || f.HypervisorPresent != Yes {
			t.Errorf("CIM overrode the native read: %+v", f)
		}
	}
	// A native call that failed (machine 0) leaves CIM's architecture.
	f := unknownFacts("windows")
	processorFacts(0, false, false)(&f)
	cim(&f)
	if f.Arch != "x64" {
		t.Errorf("with no native machine, CIM's architecture was lost: %s", f.Arch)
	}
}

func TestMachineArch(t *testing.T) {
	for m, want := range map[uint16]string{0x8664: "x64", 0xAA64: "arm64", 0x014C: "x86", 0x01C4: "arm", 0: "unknown"} {
		if got := machineArch(m); got != want {
			t.Errorf("machineArch(%#x) = %s, want %s", m, got, want)
		}
	}
}

// A probe that hands back ctx.Err() at the deadline races ctx.Done() in the
// collector. Whichever side wins, the probe is reported as timed out.
func TestSnapshotCallsADeadlineErrorTimedOut(t *testing.T) {
	p := fakeProbes{
		osV:       func(f *HostFacts) {},
		cimErr:    fmt.Errorf("powershell: %w", context.DeadlineExceeded),
		featV:     func(f *HostFacts) {},
		wslV:      func(f *HostFacts) {},
		dockerV:   func(f *HostFacts) {},
		accountV:  func(f *HostFacts) {},
		restartV:  func(f *HostFacts) {},
		servicesV: func(f *HostFacts) {},
	}
	f := snapshot(context.Background(), p, "windows", time.Second)
	if f.Problems["cpu"] != "timed out" {
		t.Errorf("a deadline error is reported as %q, want \"timed out\"", f.Problems["cpu"])
	}
}

// Docker Desktop's "ever opened" reads No only when every settings file is proven
// absent. An access-denied Stat leaves it Unknown.
func TestSettingsFileFound(t *testing.T) {
	denied := &fs.PathError{Op: "stat", Path: "settings.json", Err: fs.ErrPermission}
	missing := &fs.PathError{Op: "stat", Path: "settings.json", Err: fs.ErrNotExist}
	cases := []struct {
		name string
		errs []error
		want Tri
	}{
		{"both missing", []error{missing, missing}, No},
		{"one found", []error{missing, nil}, Yes},
		{"found beats denied", []error{denied, nil}, Yes},
		{"denied and missing", []error{denied, missing}, Unknown},
		{"both denied", []error{denied, denied}, Unknown},
		{"nothing read", nil, Unknown},
	}
	for _, c := range cases {
		if got := settingsFileFound(c.errs...); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

func TestSnapshotOutsideWindowsKnowsNothing(t *testing.T) {
	f := snapshot(context.Background(), fakeProbes{unsupported: true}, "linux", time.Second)
	if f.Supported || f.Virtualization != Unknown || f.WSL != WSLUnknown || f.SecureVMHost.Reason != HostNotWindows {
		t.Errorf("an unsupported OS reported facts: %+v", f)
	}
}

func TestParseCIM(t *testing.T) {
	apply, err := parseCIM([]byte(`{"hypervisorPresent":true,"architecture":12,"firmware":false,"slat":true,"wslFeature":2,"vmpFeature":1}` + "\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	f := unknownFacts("windows")
	apply(&f)
	if f.HypervisorPresent != Yes || f.Arch != "arm64" || f.Firmware != No || f.SLAT != Yes || f.WSLFeature != No || f.VMPlatformFeature != Yes {
		t.Errorf("parsed %+v", f)
	}

	// CIM can return null, and a class that failed leaves its fields out: both
	// stay unknown, never false.
	apply, err = parseCIM([]byte(`{"hypervisorPresent":null,"wslFeature":4}`))
	if err != nil {
		t.Fatal(err)
	}
	f = unknownFacts("windows")
	apply(&f)
	if f.HypervisorPresent != Unknown || f.Firmware != Unknown || f.Arch != "unknown" || f.WSLFeature != Unknown || f.VMPlatformFeature != Unknown {
		t.Errorf("null or missing CIM values read as known: %+v", f)
	}

	// The two answers are applied one after the other; neither may undo the other.
	cpu, _ := parseCIM([]byte(`{"hypervisorPresent":true,"architecture":9}`))
	feat, _ := parseCIM([]byte(`{"wslFeature":1,"vmpFeature":1}`))
	for _, order := range [][]func(*HostFacts){{cpu, feat}, {feat, cpu}} {
		f = unknownFacts("windows")
		for _, apply := range order {
			apply(&f)
		}
		if f.HypervisorPresent != Yes || f.Arch != "x64" || f.WSLFeature != Yes || f.VMPlatformFeature != Yes {
			t.Errorf("one CIM answer undid the other: %+v", f)
		}
	}

	if _, err := parseCIM([]byte("Get-CimInstance : Access denied")); err == nil {
		t.Error("text that is not JSON was accepted")
	}
}

func utf16le(s string) []byte {
	units := utf16.Encode([]rune(s))
	b := make([]byte, 0, 2*len(units))
	for _, u := range units {
		b = append(b, byte(u), byte(u>>8))
	}
	return b
}

func TestParseWSLVersionReadsDigitsInAnyLanguage(t *testing.T) {
	// The Spanish test PC writes UTF-16LE with translated labels.
	spanish := utf16le("Versión de WSL: 2.3.26.0\r\nVersión de kernel: 5.15.167.4-1\r\nVersión de WSLg: 1.0.65\r\n")
	if got := parseWSLVersion(spanish); got != "2.3.26.0" {
		t.Errorf("Spanish UTF-16LE: got %q", got)
	}
	if got := parseWSLVersion([]byte("WSL version: 2.1.5.0\nKernel version: 5.15.146.1-2\n")); got != "2.1.5.0" {
		t.Errorf("English UTF-8: got %q", got)
	}
	if got := parseWSLVersion([]byte("no version here")); got != "" {
		t.Errorf("no version: got %q", got)
	}
}

// A routine file rename left by an installer never says "restart first": only
// Component Based Servicing and Windows Update do.
func TestRestartPendingIgnoresFileRenamesAlone(t *testing.T) {
	cases := []struct {
		reasons []string
		certain bool
		want    Tri
	}{
		{[]string{"file-rename"}, true, No},
		{[]string{"file-rename"}, false, Unknown},
		{[]string{"cbs"}, true, Yes},
		{[]string{"file-rename", "windows-update"}, true, Yes},
		{nil, true, No},
		{nil, false, Unknown},
	}
	for _, tc := range cases {
		if got := restartPending(tc.reasons, tc.certain); got != tc.want {
			t.Errorf("restartPending(%v, %v) = %s, want %s", tc.reasons, tc.certain, got, tc.want)
		}
	}
}

// A PowerShell killed at the deadline reports as timed out, not as a failed exit.
func TestCIMErrorKeepsTheDeadline(t *testing.T) {
	exit := errors.New("exit status 1")
	err := cimError(context.DeadlineExceeded, exit)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a timed-out CIM read lost the deadline: %v", err)
	}
	err = cimError(nil, exit)
	if !errors.Is(err, exit) || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a failed CIM read: %v", err)
	}
	// And the collector then calls it what it is.
	p := fakeProbes{osV: func(*HostFacts) {}, cimErr: cimError(context.DeadlineExceeded, exit), featV: func(*HostFacts) {},
		wslV: func(*HostFacts) {}, dockerV: func(*HostFacts) {}, accountV: func(*HostFacts) {}, restartV: func(*HostFacts) {}, servicesV: func(*HostFacts) {}}
	if f := snapshot(context.Background(), p, "windows", time.Second); f.Problems["cpu"] != "timed out" {
		t.Fatalf("a killed PowerShell reads %q", f.Problems["cpu"])
	}
}

func TestStartTypeName(t *testing.T) {
	for in, want := range map[uint32]string{0: "auto", 1: "auto", 2: "auto", 3: "manual", 4: "disabled", 9: "unknown"} {
		if got := startTypeName(in); got != want {
			t.Errorf("startTypeName(%d) = %s, want %s", in, got, want)
		}
	}
}

func TestOtherHypervisors(t *testing.T) {
	if got, names := otherHypervisors(Yes, No); got != Yes || len(names) != 1 || names[0] != "VirtualBox" {
		t.Errorf("VirtualBox alone: %s %v", got, names)
	}
	if got, names := otherHypervisors(Yes, Yes); got != Yes || len(names) != 2 {
		t.Errorf("both: %s %v", got, names)
	}
	if got, _ := otherHypervisors(No, No); got != No {
		t.Errorf("neither: %s", got)
	}
	if got, _ := otherHypervisors(No, Unknown); got != Unknown {
		t.Errorf("one unread is not proof of none: %s", got)
	}
}

// Both settings files are read: the older settings.json in camelCase and the
// newer settings-store.json in PascalCase.
func TestDockerBackendReadsEitherSettingsFile(t *testing.T) {
	cases := map[string]string{
		`{"wslEngineEnabled": true}`:  BackendWSL,
		`{"WslEngineEnabled": true}`:  BackendWSL,
		`{"WslEngineEnabled": false}`: BackendHyperV,
		`{"AutoStart": true}`:         BackendUnknown,
		`not json`:                    BackendUnknown,
	}
	for in, want := range cases {
		if got := dockerBackend([]byte(in)); got != want {
			t.Errorf("dockerBackend(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestParseCIMReadsTheMachineAndTheBoot(t *testing.T) {
	apply, err := parseCIM([]byte(`{"manufacturer":"Microsoft Corporation","model":"Virtual Machine","lastBoot":"2026-11-25T07:59:58.5000000Z"}`))
	if err != nil {
		t.Fatal(err)
	}
	f := unknownFacts("windows")
	apply(&f)
	derive(&f)
	if f.IsVM != Yes || f.LastBoot != "2026-11-25T07:59:58.5Z" {
		t.Fatalf("machine and boot: vm %s boot %q", f.IsVM, f.LastBoot)
	}
	if bootTime("25/11/2026 8:00:00") != "" {
		t.Fatal("a localized date was read as a boot time")
	}
}

// TestProofPortsStayBelowTheDynamicRange: Windows reserves blocks of its
// dynamic port range (49152-65535) for Hyper-V and WinNAT, and a canary
// there cannot listen. Every port the proof picks must stay below it.
func TestProofPortsStayBelowTheDynamicRange(t *testing.T) {
	for v := 0; v < 1<<16; v++ {
		r := Runner{Rand: bytes.NewReader([]byte{byte(v >> 8), byte(v)})}
		if p := r.port(); p < 40000 || p >= 49152 {
			t.Fatalf("random %d gave port %d, want 40000..49151", v, p)
		}
	}
}
