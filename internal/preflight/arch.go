package preflight

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// CPU architecture, folded to the three families a catalog entry can promise.
//
// A container runtime reports its own architecture ("aarch64", "arm64", "x86_64"),
// an image manifest names "linux/arm64/v8", a catalog entry declares "linux/arm/v7"
// and a Traffmonetizer override is keyed "arm". They all mean one of three things,
// and everything that reasons about "will this image run on that machine" reasons in
// those three terms:
//
//   - amd64: x86-64. Every catalog image has this build.
//   - arm64: 64-bit ARM. Raspberry Pi 4/5 on a 64-bit OS, Apple Silicon, AWS
//     Graviton, Oracle Ampere.
//   - arm: 32-bit ARM. Raspberry Pi 2/3 and any Pi on the 32-bit OS. Builds within
//     the family run each other's binaries (an arm/v5 build runs on a v7 board), so
//     the variant is tracked only for 32-bit ARM, where compatibility runs one way.
//
// Anything else (i386, riscv64, s390x) is unknown here: no catalog image publishes
// it, and the honest answer is "cannot say" rather than a guess. This is the Go port
// of the web repository's app/arch.py and must keep answering the same way.

// families are the three architecture families a catalog entry may name in
// docker.image_by_arch.
var families = map[string]bool{"amd64": true, "arm64": true, "arm": true}

// machineFamily folds what a runtime reports about its own CPU into a family.
var machineFamily = map[string]string{
	"x86_64":      "amd64",
	"amd64":       "amd64",
	"aarch64":     "arm64",
	"arm64":       "arm64",
	"arm64-v8a":   "arm64", // Android
	"armv8l":      "arm64", // 64-bit CPU running a 32-bit userland reports this on some kernels
	"armv7l":      "arm",
	"armv6l":      "arm",
	"armhf":       "arm",
	"armeabi-v7a": "arm", // Android
	"arm":         "arm",
}

var archLabels = map[string]string{"amd64": "x86-64", "arm64": "64-bit ARM", "arm": "32-bit ARM"}

// armDefaultVariant is Docker's default variant for a bare "linux/arm", and what
// "armhf" means.
const armDefaultVariant = 7

var armVariantPattern = regexp.MustCompile(`armv(\d)`)

// Target is one CPU a build can be made for: a family, plus the variant that only
// 32-bit ARM tracks (0 elsewhere). Known is false when nothing could be determined,
// which callers must read as "not checked" and never as a pass.
type Target struct {
	Family  string
	Variant int
	Known   bool
}

// Family folds a reported machine name to one of the three families: "aarch64" ->
// "arm64", a family name to itself, anything unknown to "".
func Family(machine string) string {
	name := strings.ToLower(strings.TrimSpace(machine))
	if name == "" {
		return ""
	}
	return machineFamily[name]
}

// normalisePlatform reads one catalog platform string: "linux/amd64" -> amd64,
// "linux/arm64/v8" -> arm64, "linux/arm/v6" -> arm variant 6.
//
// 32-bit ARM keeps its variant because compatibility runs one way: a v7 board runs
// v5 and v6 builds, but a Pi Zero (v6) cannot run a v7 build. arm64 has one variant
// in practice, so it folds.
func normalisePlatform(platform string) Target {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(platform)), "/")
	if len(parts) < 2 || parts[0] != "linux" {
		return Target{}
	}
	kind := parts[1]
	variant := ""
	if len(parts) > 2 {
		variant = parts[2]
	}
	switch kind {
	case "amd64", "arm64":
		return Target{Family: kind, Known: true}
	case "arm":
		digits := strings.TrimPrefix(variant, "v")
		if n, err := strconv.Atoi(digits); err == nil {
			return Target{Family: "arm", Variant: n, Known: true}
		}
		return Target{Family: "arm", Variant: armDefaultVariant, Known: true}
	}
	return Target{}
}

// machineTarget says what a machine can run, in the same shape normalisePlatform
// gives builds: "armv6l" -> arm v6, "armhf" and Android's "armeabi-v7a" -> arm v7,
// "aarch64" -> arm64.
func machineTarget(machine string) Target {
	fam := Family(machine)
	if fam == "" {
		return Target{}
	}
	if fam != "arm" {
		return Target{Family: fam, Known: true}
	}
	if m := armVariantPattern.FindStringSubmatch(strings.ToLower(machine)); m != nil {
		n, err := strconv.Atoi(m[1])
		if err == nil {
			return Target{Family: "arm", Variant: n, Known: true}
		}
	}
	return Target{Family: "arm", Variant: armDefaultVariant, Known: true}
}

// runsOn answers whether a build for one platform runs on a given machine.
func runsOn(build, target Target) bool {
	if !build.Known || !target.Known || build.Family != target.Family {
		return false
	}
	if build.Family == "arm" {
		return build.Variant <= target.Variant
	}
	return true
}

// Label is how a family reads to a person.
func Label(family string) string {
	if label, ok := archLabels[family]; ok {
		return label
	}
	if family == "" {
		return "unknown"
	}
	return family
}

// describeBuilds lists the builds an entry has, variants included: "x86-64, 32-bit
// ARM v7". Folding to the family here would let a Pi Zero read "no build for 32-bit
// ARM, only 32-bit ARM": the variant is the point.
func describeBuilds(docker catalog.DockerConfig) string {
	seen := map[Target]bool{}
	for _, platform := range docker.Platforms {
		if build := normalisePlatform(platform); build.Known {
			seen[build] = true
		}
	}
	for key := range docker.ImageByArch {
		if families[key] {
			seen[Target{Family: key, Known: true}] = true
		}
	}
	names := make([]string, 0, len(seen))
	for build := range seen {
		if build.Family == "arm" && build.Variant > 0 {
			names = append(names, Label(build.Family)+" v"+strconv.Itoa(build.Variant))
			continue
		}
		names = append(names, Label(build.Family))
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// Supports is supports for callers outside this package. The compose export asks
// it before writing a file for a chosen architecture, so the same rule that
// warns in the wizard refuses an export that could not run: one answer, not two.
func Supports(docker catalog.DockerConfig, machine string) (supported bool, known bool) {
	return supports(docker, machine)
}

// Builds is describeBuilds for callers outside this package.
func Builds(docker catalog.DockerConfig) string {
	return describeBuilds(docker)
}

// supports answers whether a catalog entry has a build that runs on this machine.
//
// known is false when it cannot be told: no reported architecture, an architecture
// nothing folds, or an entry that declares no platforms at all. Callers must read
// that as "not checked", never as a pass. An image_by_arch override counts for its
// whole family: the tag is a separate single-architecture image whose label lies
// (that is exactly why it exists), so its variant cannot be read.
func supports(docker catalog.DockerConfig, machine string) (supported bool, known bool) {
	target := machineTarget(machine)
	declared := make([]Target, 0, len(docker.Platforms))
	for _, platform := range docker.Platforms {
		if build := normalisePlatform(platform); build.Known {
			declared = append(declared, build)
		}
	}
	overrides := map[string]bool{}
	for key := range docker.ImageByArch {
		if families[key] {
			overrides[key] = true
		}
	}
	if !target.Known || (len(declared) == 0 && len(overrides) == 0) {
		return false, false
	}
	if overrides[target.Family] {
		return true, true
	}
	for _, build := range declared {
		if runsOn(build, target) {
			return true, true
		}
	}
	return false, true
}

// overrideImage is the separate image a catalog entry names for this machine's CPU
// family, empty when it names none.
//
// An entry carries image_by_arch only when the container runtime cannot pick the
// right build from the default image by itself — traffmonetizer/cli_v2 labels every
// tag x86-64, including its two real ARM ones. So a non-empty answer here means the
// default image is NOT the build that runs on this machine, and whoever deploys the
// default image on that CPU gets a container that cannot start.
func overrideImage(docker catalog.DockerConfig, machine string) string {
	family := Family(machine)
	if family == "" {
		return ""
	}
	return strings.TrimSpace(docker.ImageByArch[family])
}
