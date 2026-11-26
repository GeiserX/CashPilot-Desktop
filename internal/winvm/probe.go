package winvm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"runtime"
	"strings"
	"time"
)

// ProbeTimeout bounds one whole reading. Each probe runs on its own goroutine, and
// a probe still running at the deadline is abandoned with its facts left Unknown,
// so a hung PowerShell or wsl.exe never holds up the caller.
const ProbeTimeout = 15 * time.Second

// probes reads the machine. Each method returns a function that writes what it
// read into the facts, so a probe that is abandoned at the deadline writes nothing.
type probes interface {
	// supported is false where nothing can be read.
	supported() bool
	osVersion(ctx context.Context) (func(*HostFacts), error)
	// processor reads the CPU through native calls, at once: the native machine
	// from IsWow64Process2 and the firmware and SLAT flags from
	// IsProcessorFeaturePresent. cpu reads the same through CIM, plus
	// HypervisorPresent, which has no documented native call; WMI can take many
	// seconds on a busy PC, so the native answer wins where both have one.
	processor(ctx context.Context) (func(*HostFacts), error)
	cpu(ctx context.Context) (func(*HostFacts), error)
	features(ctx context.Context) (func(*HostFacts), error)
	wsl(ctx context.Context) (func(*HostFacts), error)
	docker(ctx context.Context) (func(*HostFacts), error)
	account(ctx context.Context) (func(*HostFacts), error)
	restart(ctx context.Context) (func(*HostFacts), error)
	// services reads the Server service's start type and whether another
	// hypervisor's driver service is installed.
	services(ctx context.Context) (func(*HostFacts), error)
}

// Snapshot reads this PC. It returns within ProbeTimeout (or ctx's deadline, if
// sooner) whatever happens to the probes.
func Snapshot(ctx context.Context) HostFacts {
	return snapshot(ctx, system, runtime.GOOS, ProbeTimeout)
}

func snapshot(ctx context.Context, p probes, goos string, timeout time.Duration) HostFacts {
	f := unknownFacts(goos)
	if !p.supported() {
		derive(&f)
		return f
	}
	f.Supported = true
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	type result struct {
		name  string
		apply func(*HostFacts)
		err   error
	}
	all := map[string]func(context.Context) (func(*HostFacts), error){
		"os":        p.osVersion,
		"processor": p.processor,
		"cpu":       p.cpu,
		"features":  p.features,
		"wsl":       p.wsl,
		"docker":    p.docker,
		"account":   p.account,
		"restart":   p.restart,
		"services":  p.services,
	}
	// Buffered for every probe, so one that answers after the deadline does not
	// leak its goroutine on the send.
	results := make(chan result, len(all))
	pending := map[string]bool{}
	for name, probe := range all {
		pending[name] = true
		go func() {
			apply, err := probe(ctx)
			results <- result{name, apply, err}
		}()
	}
	for len(pending) > 0 {
		select {
		case r := <-results:
			delete(pending, r.name)
			if r.err != nil {
				// A probe that returns ctx.Err() at the deadline can land here
				// rather than on ctx.Done(): it timed out all the same.
				if errors.Is(r.err, context.DeadlineExceeded) {
					f.Problems[r.name] = "timed out"
				} else {
					f.Problems[r.name] = r.err.Error()
				}
				continue
			}
			if r.apply != nil {
				r.apply(&f)
			}
		case <-ctx.Done():
			for name := range pending {
				f.Problems[name] = "timed out"
			}
			pending = nil
		}
	}
	derive(&f)
	return f
}

// settingsFileFound turns the Stat results for Docker Desktop's settings files
// into "was it ever opened": Yes when one exists, No only when every one is
// proven absent, and Unknown when a Stat failed any other way (access denied),
// so an unreadable folder never reads as "never opened".
func settingsFileFound(statErrs ...error) Tri {
	if len(statErrs) == 0 {
		return Unknown
	}
	absent := true
	for _, err := range statErrs {
		if err == nil {
			return Yes
		}
		if !errors.Is(err, fs.ErrNotExist) {
			absent = false
		}
	}
	if absent {
		return No
	}
	return Unknown
}

// cpuScript and featureScript read the CIM facts as JSON, in two processes that
// run side by side: Win32_OptionalFeature walks every feature and can take many
// seconds on a busy PC (it outlasted the deadline on a loaded CI runner), and it
// must not take the CPU facts down with it. Each block has its own try, so a class
// that cannot be read leaves only its own fields out. Values are written as CIM
// returns them, so a null stays null and reads as Unknown.
const cpuScript = `$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$o = [ordered]@{}
try {
  $cs = Get-CimInstance -ClassName Win32_ComputerSystem
  $o.hypervisorPresent = $cs.HypervisorPresent
  $o.manufacturer = $cs.Manufacturer
  $o.model = $cs.Model
} catch { }
try { $o.lastBoot = (Get-CimInstance -ClassName Win32_OperatingSystem).LastBootUpTime.ToUniversalTime().ToString('o') } catch { }
try {
  $p = @(Get-CimInstance -ClassName Win32_Processor)[0]
  $o.architecture = $p.Architecture
  $o.firmware = $p.VirtualizationFirmwareEnabled
  $o.slat = $p.SecondLevelAddressTranslationExtensions
} catch { }
$o | ConvertTo-Json -Compress`

const featureScript = `$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$o = [ordered]@{}
try { $f = Get-CimInstance -ClassName Win32_OptionalFeature -Filter "Name='Microsoft-Windows-Subsystem-Linux'"; if ($f) { $o.wslFeature = $f.InstallState } else { $o.wslFeature = 3 } } catch { }
try { $f = Get-CimInstance -ClassName Win32_OptionalFeature -Filter "Name='VirtualMachinePlatform'"; if ($f) { $o.vmpFeature = $f.InstallState } else { $o.vmpFeature = 3 } } catch { }
$o | ConvertTo-Json -Compress`

type cimFacts struct {
	HypervisorPresent *bool   `json:"hypervisorPresent"`
	Manufacturer      *string `json:"manufacturer"`
	Model             *string `json:"model"`
	LastBoot          *string `json:"lastBoot"`
	Architecture      *int    `json:"architecture"`
	Firmware          *bool   `json:"firmware"`
	SLAT              *bool   `json:"slat"`
	WSLFeature        *int    `json:"wslFeature"`
	VMPFeature        *int    `json:"vmpFeature"`
}

// processorFacts applies what the native processor probe read. It always wins
// over CIM's answer for the same facts.
func processorFacts(machine uint16, firmware, slat bool) func(*HostFacts) {
	return func(f *HostFacts) {
		if a := machineArch(machine); a != "unknown" {
			f.Arch = a
		}
		f.Firmware = triOf(firmware)
		f.SLAT = triOf(slat)
	}
}

// machineArch maps an IMAGE_FILE_MACHINE value, as IsWow64Process2 reports the
// native machine, to an arch name. An x64 exe emulated on an Arm PC is told
// ARM64 here, where GOARCH says amd64.
func machineArch(machine uint16) string {
	switch machine {
	case 0x8664:
		return "x64"
	case 0xAA64:
		return "arm64"
	case 0x014C:
		return "x86"
	case 0x01C4:
		return "arm"
	}
	return "unknown"
}

// parseCIM decodes cpuScript's or featureScript's output into a function that
// applies it. It writes only the fields the answer has, so the two answers never
// undo each other, and a null value stays Unknown.
func parseCIM(out []byte) (func(*HostFacts), error) {
	var c cimFacts
	if err := json.Unmarshal(bytes.TrimSpace(out), &c); err != nil {
		return nil, fmt.Errorf("could not read the CIM answer: %v", err)
	}
	return c.apply, nil
}

func (c cimFacts) apply(f *HostFacts) {
	if c.HypervisorPresent != nil {
		f.HypervisorPresent = triPtr(c.HypervisorPresent)
	}
	if c.Manufacturer != nil {
		f.Manufacturer = strings.TrimSpace(*c.Manufacturer)
	}
	if c.Model != nil {
		f.Model = strings.TrimSpace(*c.Model)
	}
	if c.LastBoot != nil {
		f.LastBoot = bootTime(*c.LastBoot)
	}
	// The native processor probe wins these three; CIM fills them only when it
	// had no answer.
	if c.Firmware != nil && f.Firmware == Unknown {
		f.Firmware = triPtr(c.Firmware)
	}
	if c.SLAT != nil && f.SLAT == Unknown {
		f.SLAT = triPtr(c.SLAT)
	}
	if c.Architecture != nil && f.Arch == "unknown" {
		f.Arch = archName(*c.Architecture)
	}
	if c.WSLFeature != nil {
		f.WSLFeature = featureState(c.WSLFeature)
	}
	if c.VMPFeature != nil {
		f.VMPlatformFeature = featureState(c.VMPFeature)
	}
}

// bootTime normalises LastBootUpTime as PowerShell's round-trip format writes it
// ("2026-11-25T07:59:58.5000000Z") to RFC 3339 in UTC. Anything else is "".
func bootTime(text string) string {
	t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(text))
	if err != nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

// cimError says why a CIM PowerShell failed. A PowerShell killed at the deadline
// exits with an error of its own, which says nothing about time, so the context's
// error is returned instead: Snapshot then reports the probe as timed out. Both
// are wrapped, so errors.Is still finds them.
func cimError(ctxErr, err error) error {
	if ctxErr != nil {
		return fmt.Errorf("PowerShell: %w", ctxErr)
	}
	return fmt.Errorf("PowerShell: %w", err)
}

// restartPending decides RestartPending from the markers found. Only Component
// Based Servicing and Windows Update mean a change waits for a restart; file
// renames alone are routine and never say "restart first". certain is false when
// a marker could not be read, which leaves No unproven.
func restartPending(reasons []string, certain bool) Tri {
	for _, r := range reasons {
		if r == "cbs" || r == "windows-update" {
			return Yes
		}
	}
	if certain {
		return No
	}
	return Unknown
}

// startTypeName maps a service's start type (QUERY_SERVICE_CONFIG.dwStartType) to
// a word: 2 automatic (delayed start included), 3 manual, 4 disabled. 0 and 1 are
// for drivers, which start even earlier, so they count as automatic.
func startTypeName(startType uint32) string {
	switch startType {
	case 0, 1, 2:
		return "auto"
	case 3:
		return "manual"
	case 4:
		return "disabled"
	}
	return "unknown"
}

// otherHypervisors combines whether VirtualBox's and VMware's driver services are
// installed: Yes with the names found, No only when both are proven absent.
func otherHypervisors(virtualBox, vmware Tri) (Tri, []string) {
	names := []string{}
	if virtualBox == Yes {
		names = append(names, "VirtualBox")
	}
	if vmware == Yes {
		names = append(names, "VMware")
	}
	switch {
	case len(names) > 0:
		return Yes, names
	case virtualBox == No && vmware == No:
		return No, names
	}
	return Unknown, names
}

// dockerSettings is the one field read from Docker Desktop's settings file. Go
// matches JSON keys without regard to case, so this reads both the older
// settings.json ("wslEngineEnabled") and settings-store.json ("WslEngineEnabled").
type dockerSettings struct {
	WSLEngineEnabled *bool `json:"wslEngineEnabled"`
}

// dockerBackend reads which engine a Docker Desktop settings file chooses. A file
// that does not parse, or does not say, is unknown.
func dockerBackend(data []byte) string {
	var s dockerSettings
	if err := json.Unmarshal(data, &s); err != nil || s.WSLEngineEnabled == nil {
		return BackendUnknown
	}
	if *s.WSLEngineEnabled {
		return BackendWSL
	}
	return BackendHyperV
}

// featureState maps Win32_OptionalFeature.InstallState: 1 enabled, 2 disabled, 3
// absent. 4 (unknown) and a missing value stay Unknown.
func featureState(state *int) Tri {
	if state == nil {
		return Unknown
	}
	switch *state {
	case 1:
		return Yes
	case 2, 3:
		return No
	}
	return Unknown
}

var versionPattern = regexp.MustCompile(`\d+\.\d+\.\d+(?:\.\d+)?`)

// parseWSLVersion takes the first dotted version in "wsl --version" output. The
// first line is the WSL package version in every language, and only its digits are
// read, never the translated label. wsl.exe writes UTF-16LE unless WSL_UTF8 is set,
// so NUL bytes are dropped first.
func parseWSLVersion(out []byte) string {
	clean := bytes.ReplaceAll(out, []byte{0}, nil)
	return string(versionPattern.Find(clean))
}
