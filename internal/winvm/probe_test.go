package winvm

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"
	"unicode/utf16"
)

// fakeProbes answers each probe from a function; a nil one blocks until the
// deadline, the way a hung PowerShell would.
type fakeProbes struct {
	unsupported                                         bool
	osV, cimV, featV, wslV, dockerV, accountV, restartV func(*HostFacts)
	cimErr                                              error
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

func TestSnapshotAppliesEveryProbe(t *testing.T) {
	p := fakeProbes{
		osV:      func(f *HostFacts) { f.Build, f.EditionID = 22631, "Core" },
		cimV:     func(f *HostFacts) { f.Arch, f.HypervisorPresent, f.Firmware = "x64", No, No },
		featV:    func(f *HostFacts) { f.VMPlatformFeature = Yes },
		wslV:     func(f *HostFacts) { f.WSLInstalled, f.WSLVersion = Yes, "2.0.9.0" },
		dockerV:  func(f *HostFacts) { f.DockerDesktopInstalled = No },
		accountV: func(f *HostFacts) { f.UserSID = "S-1-5-21-1-2-3-1001" },
		restartV: func(f *HostFacts) { f.RestartPending, f.RestartReasons = Yes, []string{"cbs"} },
	}
	f := snapshot(context.Background(), p, "windows", time.Second)
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

func TestPSEncodeIsUTF16LEBase64(t *testing.T) {
	raw, err := base64.StdEncoding.DecodeString(psEncode("exit 3"))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(utf16le("exit 3")) {
		t.Errorf("decoded %q", raw)
	}
}
