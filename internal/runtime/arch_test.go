package runtime

import (
	"context"
	"errors"
	goruntime "runtime"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/client"
)

func TestArchFamilyFoldsWhatMachinesReport(t *testing.T) {
	cases := map[string]string{
		"x86_64":      "amd64",
		"amd64":       "amd64",
		"aarch64":     "arm64",
		"arm64":       "arm64",
		"armv8l":      "arm64",
		"armv7l":      "arm",
		"armv6l":      "arm",
		"armhf":       "arm",
		"ARM64":       "arm64",
		" x86_64 ":    "amd64",
		"riscv64":     "",
		"s390x":       "",
		"i386":        "",
		"":            "",
		"armeabi-v7a": "arm",
	}
	for machine, want := range cases {
		if got := ArchFamily(machine); got != want {
			t.Errorf("ArchFamily(%q) = %q, want %q", machine, got, want)
		}
	}
}

// THE RULE: the image a machine pulls is the one that can run on it.
//
// traffmonetizer/cli_v2 publishes separate arm64v8 and arm32v7 tags and labels every
// one of them linux/amd64, so Docker cannot pick the right build from the manifest. A
// Raspberry Pi pulling the default tag gets an x86-64 binary, the container dies with
// "exec format error", and the user sees a service that deployed and will not stay up.
func TestImageForArchPicksThePerArchOverride(t *testing.T) {
	dc := catalog.DockerConfig{
		Image: "traffmonetizer/cli_v2@sha256:abc",
		ImageByArch: map[string]string{
			"arm64": "traffmonetizer/cli_v2:arm64v8",
			"arm":   "traffmonetizer/cli_v2:arm32v7",
		},
	}
	cases := map[string]string{
		"aarch64": "traffmonetizer/cli_v2:arm64v8",
		"arm64":   "traffmonetizer/cli_v2:arm64v8",
		"armv7l":  "traffmonetizer/cli_v2:arm32v7",
		"armv6l":  "traffmonetizer/cli_v2:arm32v7",
		// No override for x86-64: the manifest is right there, so the base image wins.
		"x86_64": "traffmonetizer/cli_v2@sha256:abc",
		// An architecture nothing folds cannot be guessed at.
		"riscv64": "traffmonetizer/cli_v2@sha256:abc",
		"":        "traffmonetizer/cli_v2@sha256:abc",
	}
	for machine, want := range cases {
		if got := ImageForArch(dc, machine); got != want {
			t.Errorf("ImageForArch(%q) = %q, want %q", machine, got, want)
		}
	}
}

// Almost every entry has a real multi-arch manifest and no overrides at all. Those must
// come back untouched on every architecture.
func TestImageForArchWithoutOverridesIsTheDeclaredImage(t *testing.T) {
	dc := catalog.DockerConfig{Image: "example/image:1.0.0"}
	for _, machine := range []string{"x86_64", "aarch64", "armv7l", "riscv64", ""} {
		if got := ImageForArch(dc, machine); got != dc.Image {
			t.Errorf("ImageForArch(%q) = %q, want %q", machine, got, dc.Image)
		}
	}
}

// An override written as an empty string is a half-finished entry, not an instruction
// to deploy "".
func TestImageForArchIgnoresABlankOverride(t *testing.T) {
	dc := catalog.DockerConfig{Image: "example/image:1.0.0", ImageByArch: map[string]string{"arm64": "  "}}
	if got := ImageForArch(dc, "aarch64"); got != dc.Image {
		t.Fatalf("a blank override produced %q", got)
	}
}

// 32-bit ARM compatibility runs one way: a v7 board runs v5 and v6 builds, but a Pi
// Zero (v6) cannot run a v7 build. Folding the variant away would tell a Pi Zero owner
// that an armv7-only image runs on their board.
func TestRunsOnIsDirectionalWithin32BitArm(t *testing.T) {
	v6, _ := MachineTarget("armv6l")
	v7, _ := MachineTarget("armv7l")
	buildV6, _ := PlatformTarget("linux/arm/v6")
	buildV7, _ := PlatformTarget("linux/arm/v7")

	if !RunsOn(buildV6, v7) {
		t.Error("a v6 build should run on a v7 board")
	}
	if RunsOn(buildV7, v6) {
		t.Error("a v7 build must NOT be reported as running on a v6 board")
	}
	arm64Build, _ := PlatformTarget("linux/arm64/v8")
	if RunsOn(arm64Build, v7) {
		t.Error("a 64-bit ARM build must not be reported as running on a 32-bit board")
	}
	arm64Machine, _ := MachineTarget("aarch64")
	if !RunsOn(arm64Build, arm64Machine) {
		t.Error("a 64-bit ARM build should run on a 64-bit ARM machine")
	}
}

func TestPlatformTargetParsesCatalogPlatforms(t *testing.T) {
	cases := []struct {
		platform string
		want     ArchTarget
		ok       bool
	}{
		{"linux/amd64", ArchTarget{Family: "amd64"}, true},
		{"linux/arm64", ArchTarget{Family: "arm64"}, true},
		{"linux/arm64/v8", ArchTarget{Family: "arm64"}, true},
		{"linux/arm/v7", ArchTarget{Family: "arm", Variant: 7}, true},
		{"linux/arm/v6", ArchTarget{Family: "arm", Variant: 6}, true},
		// A bare linux/arm is v7, which is what Docker itself defaults to.
		{"linux/arm", ArchTarget{Family: "arm", Variant: 7}, true},
		{"linux/386", ArchTarget{}, false},
		{"windows/amd64", ArchTarget{}, false},
		{"", ArchTarget{}, false},
	}
	for _, tc := range cases {
		got, ok := PlatformTarget(tc.platform)
		if ok != tc.ok || got != tc.want {
			t.Errorf("PlatformTarget(%q) = %+v,%v want %+v,%v", tc.platform, got, ok, tc.want, tc.ok)
		}
	}
}

// "Does this entry publish a build for this machine" has THREE answers, and the third
// one matters: an entry that declares nothing is unknown, not unsupported. Reading it
// as unsupported would put a warning on every service whose platform list nobody has
// filled in yet.
func TestHasBuildForSeparatesUnknownFromNo(t *testing.T) {
	multi := catalog.DockerConfig{Platforms: []string{"linux/amd64", "linux/arm64", "linux/arm/v7"}}
	if runs, known := HasBuildFor(multi, "aarch64"); !known || !runs {
		t.Errorf("declared arm64 build read as runs=%v known=%v", runs, known)
	}

	amdOnly := catalog.DockerConfig{Platforms: []string{"linux/amd64"}}
	if runs, known := HasBuildFor(amdOnly, "aarch64"); !known || runs {
		t.Errorf("an x86-only image read as runs=%v known=%v on ARM", runs, known)
	}

	silent := catalog.DockerConfig{Image: "example/image:1.0.0"}
	if _, known := HasBuildFor(silent, "aarch64"); known {
		t.Error("an entry that declares no platforms must read as unknown, never as an answer")
	}

	if _, known := HasBuildFor(multi, "riscv64"); known {
		t.Error("an architecture nothing folds must read as unknown")
	}

	// An override is a separate single-arch image whose registry label lies — that is
	// precisely why it exists — so it counts for its whole family and its variant
	// cannot be read.
	override := catalog.DockerConfig{
		Platforms:   []string{"linux/amd64"},
		ImageByArch: map[string]string{"arm64": "traffmonetizer/cli_v2:arm64v8"},
	}
	if runs, known := HasBuildFor(override, "aarch64"); !known || !runs {
		t.Errorf("an image_by_arch override read as runs=%v known=%v", runs, known)
	}

	// A 32-bit board must not be told a v7-only image runs on it.
	if runs, known := HasBuildFor(multi, "armv6l"); !known || runs {
		t.Errorf("a v7-only entry read as runs=%v known=%v on a v6 board", runs, known)
	}
}

// fakeInfoClient answers /info with a canned response or an error.
type fakeInfoClient struct {
	info client.SystemInfoResult
	err  error
}

func (f fakeInfoClient) Info(context.Context, client.InfoOptions) (client.SystemInfoResult, error) {
	return f.info, f.err
}

// THE RULE: which CPU the containers run on is the DAEMON's answer, not this process's.
//
// On Docker Desktop and Podman Desktop the app is a macOS or Windows binary while the
// containers run inside a Linux VM, and it is that VM's architecture an image manifest
// is matched against. Asking runtime.GOARCH would be asking the wrong machine — and on
// an Apple Silicon Mac running an amd64 VM it gives the wrong answer.
func TestDaemonFactsAsksTheDaemon(t *testing.T) {
	facts := daemonFacts(context.Background(), fakeInfoClient{
		info: client.SystemInfoResult{Info: system.Info{Architecture: "aarch64", OSType: "linux"}},
	})
	if facts.Architecture != "aarch64" {
		t.Fatalf("Architecture = %q, want the daemon's own answer", facts.Architecture)
	}
	if facts.OSType != "linux" {
		t.Fatalf("OSType = %q, want linux", facts.OSType)
	}
}

// A daemon with a thin /info must not stop a deploy, and the fallback has to keep the
// hardening on rather than switch it off.
func TestDaemonFactsFallsBackWhenTheDaemonCannotAnswer(t *testing.T) {
	facts := daemonFacts(context.Background(), fakeInfoClient{err: errors.New("no /info")})
	if facts.Architecture != goruntime.GOARCH {
		t.Fatalf("Architecture = %q, want this process's GOARCH as the fallback", facts.Architecture)
	}
	if facts.OSType != "linux" {
		t.Fatalf("OSType = %q: the fallback must keep the Linux hardening on", facts.OSType)
	}

	// An answer with blank fields is the same situation.
	blank := daemonFacts(context.Background(), fakeInfoClient{info: client.SystemInfoResult{Info: system.Info{}}})
	if blank.Architecture != goruntime.GOARCH || blank.OSType != "linux" {
		t.Fatalf("a blank /info produced %+v", blank)
	}
}

// A Windows-container daemon has to be recognised as one, because that is what turns
// the Linux-only hardening off.
func TestDaemonFactsReportsWindowsContainers(t *testing.T) {
	facts := daemonFacts(context.Background(), fakeInfoClient{
		info: client.SystemInfoResult{Info: system.Info{Architecture: "x86_64", OSType: "Windows"}},
	})
	if facts.OSType != "windows" {
		t.Fatalf("OSType = %q, want windows", facts.OSType)
	}
}
