package runtime

import (
	"context"
	goruntime "runtime"
	"strconv"
	"strings"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/moby/moby/client"
)

// CPU architecture, folded to the three families a catalog entry can promise. This is
// the Go half of the web repository's app/arch.py and speaks the same vocabulary, so
// "does this image run on this machine" is answered the same way on both sides:
//
//	amd64  x86-64. Every catalog image has this build.
//	arm64  64-bit ARM: Apple Silicon, Raspberry Pi 4/5 on a 64-bit OS, Graviton, Ampere.
//	arm    32-bit ARM: Raspberry Pi 2/3 and any Pi on a 32-bit OS.
//
// Anything else (i386, riscv64, s390x) is unknown here: no catalog image publishes it,
// and the honest answer is "cannot say" rather than a guess.
//
// 32-bit ARM keeps its variant because compatibility only runs one way: a v7 board runs
// v5 and v6 builds, but a Pi Zero (v6) cannot run a v7 build. arm64 has one variant in
// practice, so it folds.

// archFamily maps what a machine reports — Docker's Info().Architecture, Go's GOARCH,
// uname -m — onto a family. Keys are lowercase.
var archFamily = map[string]string{
	"x86_64":      "amd64",
	"x86-64":      "amd64",
	"amd64":       "amd64",
	"aarch64":     "arm64",
	"arm64":       "arm64",
	"arm64-v8a":   "arm64",
	"armv8l":      "arm64", // 64-bit CPU running a 32-bit userland reports this on some kernels
	"armv7l":      "arm",
	"armv6l":      "arm",
	"armv5l":      "arm",
	"armhf":       "arm",
	"armeabi-v7a": "arm",
	"arm":         "arm",
}

// archLabel is how a family reads to a person.
var archLabel = map[string]string{
	"amd64": "x86-64",
	"arm64": "64-bit ARM",
	"arm":   "32-bit ARM",
}

// armDefaultVariant is Docker's default variant for a bare "linux/arm", and what
// "armhf" means.
const armDefaultVariant = 7

// ArchFamily folds a reported architecture onto amd64, arm64 or arm. It returns ""
// for anything it does not recognise, which callers must read as "cannot say" and
// never as a family.
func ArchFamily(machine string) string {
	return archFamily[strings.ToLower(strings.TrimSpace(machine))]
}

// ArchLabel is how a family reads to a person ("64-bit ARM"). An unknown family is
// returned unchanged so a message never renders an empty gap.
func ArchLabel(family string) string {
	if label, ok := archLabel[family]; ok {
		return label
	}
	if family == "" {
		return "unknown"
	}
	return family
}

// ArchTarget is one architecture, with the ARM variant kept because 32-bit ARM
// compatibility is directional. Variant is 0 (and meaningless) for amd64 and arm64.
type ArchTarget struct {
	Family  string
	Variant int
}

// MachineTarget is what a machine can run, in the same shape PlatformTarget gives
// builds: "armv6l" -> {arm 6}, "armhf" -> {arm 7}, "aarch64" -> {arm64}. ok is false
// when the architecture is not one of the three families.
func MachineTarget(machine string) (ArchTarget, bool) {
	family := ArchFamily(machine)
	if family == "" {
		return ArchTarget{}, false
	}
	if family != "arm" {
		return ArchTarget{Family: family}, true
	}
	return ArchTarget{Family: "arm", Variant: armVariant(machine)}, true
}

// armVariant reads the digit out of "armv7l"/"armv6l", defaulting to 7 for the
// variant-less spellings ("arm", "armhf", "armeabi-v7a" — all v7 in practice).
func armVariant(machine string) int {
	lower := strings.ToLower(machine)
	idx := strings.Index(lower, "armv")
	if idx >= 0 && idx+4 < len(lower) {
		if n, err := strconv.Atoi(string(lower[idx+4])); err == nil && n > 0 {
			return n
		}
	}
	return armDefaultVariant
}

// PlatformTarget parses a catalog platform string: "linux/amd64" -> {amd64},
// "linux/arm64/v8" -> {arm64}, "linux/arm/v6" -> {arm 6}, bare "linux/arm" -> {arm 7}.
// ok is false for a non-linux platform or a family nothing folds ("linux/386").
func PlatformTarget(platform string) (ArchTarget, bool) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(platform)), "/")
	if len(parts) < 2 || parts[0] != "linux" {
		return ArchTarget{}, false
	}
	switch parts[1] {
	case "amd64", "arm64":
		return ArchTarget{Family: parts[1]}, true
	case "arm":
		variant := armDefaultVariant
		if len(parts) > 2 {
			if n, err := strconv.Atoi(strings.TrimPrefix(parts[2], "v")); err == nil && n > 0 {
				variant = n
			}
		}
		return ArchTarget{Family: "arm", Variant: variant}, true
	}
	return ArchTarget{}, false
}

// RunsOn reports whether a build of this platform runs on that machine. Families must
// match exactly; within 32-bit ARM a lower-or-equal variant runs (a v7 board runs a v6
// build, a v6 board does not run a v7 build).
func RunsOn(build, target ArchTarget) bool {
	if build.Family != target.Family {
		return false
	}
	if build.Family != "arm" {
		return true
	}
	return build.Variant <= target.Variant
}

// ImageResolver is a provider that can say, before a deploy runs, which image it would
// actually pull on this machine. It is optional: a provider that does not implement it
// simply has the catalog's default image recorded, which is what every provider did
// before this existed.
//
// It exists because the answer depends on the DAEMON's architecture, which only the
// provider can ask for, while the caller that records the deploy history holds the
// catalog. Returning "" means "cannot say", and the caller keeps its own answer.
type ImageResolver interface {
	ResolveImage(ctx context.Context, svc catalog.Service) string
}

// ResolveImage reports the image a deploy of svc would pull right now, resolved against
// the daemon's architecture exactly as Deploy resolves it.
//
// An entry with no per-architecture override needs no daemon at all, which is nearly
// every entry, so this costs a round trip only where the answer can actually differ.
// A daemon that cannot be reached yields the catalog's default rather than an error:
// this only labels a history entry, and it must never be the reason a deploy fails.
func (p *DockerProvider) ResolveImage(ctx context.Context, svc catalog.Service) string {
	if len(svc.Docker.ImageByArch) == 0 {
		return svc.Docker.Image
	}
	cli, err := dockerClient()
	if err != nil {
		return svc.Docker.Image
	}
	defer cli.Close()
	return ImageForArch(svc.Docker, daemonFacts(ctx, cli).Architecture)
}

// ImageForArch is the image a machine of this architecture should pull.
//
// Docker picks the right build out of a multi-arch manifest by itself, so almost every
// entry needs only docker.image. image_by_arch exists for the images whose ARM builds
// Docker CANNOT select: traffmonetizer/cli_v2 publishes separate arm64v8 and arm32v7
// tags and labels every one of them linux/amd64, so a machine on a Raspberry Pi pulling
// the default tag gets an x86-64 binary that cannot start. An unknown architecture, or
// a family the entry does not name, falls back to docker.image.
func ImageForArch(dc catalog.DockerConfig, machine string) string {
	family := ArchFamily(machine)
	if family == "" || len(dc.ImageByArch) == 0 {
		return dc.Image
	}
	if override := strings.TrimSpace(dc.ImageByArch[family]); override != "" {
		return override
	}
	return dc.Image
}

// HasBuildFor reports whether a catalog entry declares a build that runs on this
// machine. known is false when the question cannot be answered — an architecture
// nothing folds, or an entry that declares no platforms and no per-arch override —
// and callers must read that as "not checked", never as a pass.
//
// An image_by_arch override counts for its whole family: the tag is a separate
// single-arch image whose registry label lies (that is exactly why it exists), so its
// variant cannot be read.
func HasBuildFor(dc catalog.DockerConfig, machine string) (runs bool, known bool) {
	target, ok := MachineTarget(machine)
	if !ok {
		return false, false
	}
	declared := make([]ArchTarget, 0, len(dc.Platforms))
	for _, platform := range dc.Platforms {
		if build, ok := PlatformTarget(platform); ok {
			declared = append(declared, build)
		}
	}
	overrides := make(map[string]bool, len(dc.ImageByArch))
	for family := range dc.ImageByArch {
		if _, ok := archLabel[family]; ok {
			overrides[family] = true
		}
	}
	if len(declared) == 0 && len(overrides) == 0 {
		return false, false
	}
	if overrides[target.Family] {
		return true, true
	}
	for _, build := range declared {
		if RunsOn(build, target) {
			return true, true
		}
	}
	return false, true
}

// daemonInfo is the handful of facts about the container runtime that the deploy path
// has to ask the DAEMON for rather than assume about this process.
type daemonInfo struct {
	// Architecture is the CPU the containers actually run on. On Docker Desktop and
	// Podman Desktop that is the Linux VM's CPU, which is what an image manifest is
	// matched against — not Go's GOARCH, which is the CPU of the desktop app.
	Architecture string
	// OSType is "linux" or "windows". Docker Desktop on Windows can be switched to
	// Windows containers, and that daemon REFUSES cap_add/cap_drop outright, so the
	// Linux-only hardening has to be skipped there rather than fail every deploy.
	OSType string
}

// infoClient is the one call daemonFacts needs, narrowed so the fallback behaviour is
// unit-testable without a daemon.
type infoClient interface {
	Info(ctx context.Context, options client.InfoOptions) (client.SystemInfoResult, error)
}

// daemonFacts asks the daemon what it is. A daemon that cannot answer falls back to
// this process's own GOARCH and "linux": the fallback keeps a deploy going on a
// runtime with a thin /info (some Podman builds), and "linux" keeps the hardening ON,
// because failing open on security is the wrong direction.
func daemonFacts(ctx context.Context, cli infoClient) daemonInfo {
	facts := daemonInfo{Architecture: goruntime.GOARCH, OSType: "linux"}
	result, err := cli.Info(ctx, client.InfoOptions{})
	if err != nil {
		return facts
	}
	if arch := strings.TrimSpace(result.Info.Architecture); arch != "" {
		facts.Architecture = arch
	}
	if osType := strings.TrimSpace(strings.ToLower(result.Info.OSType)); osType != "" {
		facts.OSType = osType
	}
	return facts
}
