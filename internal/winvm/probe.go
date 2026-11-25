package winvm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"runtime"
	"time"
	"unicode/utf16"
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
try { $o.hypervisorPresent = (Get-CimInstance -ClassName Win32_ComputerSystem).HypervisorPresent } catch { }
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
	HypervisorPresent *bool `json:"hypervisorPresent"`
	Architecture      *int  `json:"architecture"`
	Firmware          *bool `json:"firmware"`
	SLAT              *bool `json:"slat"`
	WSLFeature        *int  `json:"wslFeature"`
	VMPFeature        *int  `json:"vmpFeature"`
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
	return func(f *HostFacts) {
		if c.HypervisorPresent != nil {
			f.HypervisorPresent = triPtr(c.HypervisorPresent)
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
	}, nil
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

// psEncode is the -EncodedCommand form of a script: base64 of its UTF-16LE bytes.
func psEncode(script string) string {
	units := utf16.Encode([]rune(script))
	b := make([]byte, 2*len(units))
	for i, u := range units {
		binary.LittleEndian.PutUint16(b[2*i:], u)
	}
	return base64.StdEncoding.EncodeToString(b)
}
