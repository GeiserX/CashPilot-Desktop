package preflight

import (
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

func TestSupports(t *testing.T) {
	cases := []struct {
		name      string
		platforms []string
		byArch    map[string]string
		machine   string
		supported bool
		known     bool
	}{
		{
			name:      "an amd64-only image does not run on a 64-bit ARM board",
			platforms: []string{"linux/amd64"},
			machine:   "aarch64",
			known:     true,
		},
		{
			name:      "the same image runs on the CPU it was built for",
			platforms: []string{"linux/amd64"},
			machine:   "x86_64",
			supported: true,
			known:     true,
		},
		{
			name:      "a v7 build runs on a v7 board",
			platforms: []string{"linux/amd64", "linux/arm/v7"},
			machine:   "armv7l",
			supported: true,
			known:     true,
		},
		{
			// The variant is the whole point: a Pi Zero cannot run a v7 build, and
			// folding both to "32-bit ARM" would tell its owner the image is fine.
			name:      "a v7 build does NOT run on a v6 board",
			platforms: []string{"linux/amd64", "linux/arm/v7"},
			machine:   "armv6l",
			known:     true,
		},
		{
			name:      "and a v6 build runs on a v7 board, because compatibility runs one way",
			platforms: []string{"linux/arm/v6"},
			machine:   "armv7l",
			supported: true,
			known:     true,
		},
		{
			// traffmonetizer/cli_v2 publishes separate arm64v8 and arm32v7 tags and
			// labels every one of them linux/amd64. The override is what makes the
			// build reachable, so it has to count as one.
			name:      "a per-architecture image override counts as a build",
			platforms: []string{"linux/amd64"},
			byArch:    map[string]string{"arm64": "traffmonetizer/cli_v2:arm64v8"},
			machine:   "aarch64",
			supported: true,
			known:     true,
		},
		{
			name:      "an entry that declares no platforms is unknown, never a refusal",
			platforms: nil,
			machine:   "aarch64",
		},
		{
			name:      "a machine that reports nothing is unknown, never a pass",
			platforms: []string{"linux/amd64"},
			machine:   "",
		},
		{
			name:      "a CPU family nothing publishes for is unknown, not a refusal",
			platforms: []string{"linux/amd64", "linux/arm64"},
			machine:   "riscv64",
		},
		{
			name:      "a platform string nobody can parse is ignored, leaving nothing known",
			platforms: []string{"windows/amd64", "linux/386"},
			machine:   "x86_64",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			docker := catalog.DockerConfig{Image: "example/image", Platforms: tc.platforms, ImageByArch: tc.byArch}
			supported, known := supports(docker, tc.machine)
			if known != tc.known || supported != tc.supported {
				t.Fatalf("supports(%v, %q) = (%t, %t), want (%t, %t)",
					tc.platforms, tc.machine, supported, known, tc.supported, tc.known)
			}
		})
	}
}

func TestDescribeBuildsKeepsTheARMVariant(t *testing.T) {
	// Folding to the family here would let a Pi Zero read "no build for 32-bit ARM,
	// only 32-bit ARM".
	docker := catalog.DockerConfig{Platforms: []string{"linux/amd64", "linux/arm/v7"}}
	if got := describeBuilds(docker); got != "32-bit ARM v7, x86-64" {
		t.Fatalf("describeBuilds = %q", got)
	}
	override := catalog.DockerConfig{
		Platforms:   []string{"linux/amd64"},
		ImageByArch: map[string]string{"arm64": "traffmonetizer/cli_v2:arm64v8"},
	}
	if got := describeBuilds(override); got != "64-bit ARM, x86-64" {
		t.Fatalf("describeBuilds with an override = %q", got)
	}
}

func TestFamilyFoldsWhatRuntimesReport(t *testing.T) {
	cases := map[string]string{
		"x86_64":      "amd64",
		"amd64":       "amd64",
		"aarch64":     "arm64",
		"arm64":       "arm64",
		"ARM64":       "arm64",
		" arm64 ":     "arm64",
		"armv7l":      "arm",
		"armv6l":      "arm",
		"armhf":       "arm",
		"armeabi-v7a": "arm",
		"riscv64":     "",
		"s390x":       "",
		"":            "",
	}
	for machine, want := range cases {
		if got := Family(machine); got != want {
			t.Fatalf("Family(%q) = %q, want %q", machine, got, want)
		}
	}
}

func TestLabelReadsAsWords(t *testing.T) {
	cases := map[string]string{
		"amd64": "x86-64",
		"arm64": "64-bit ARM",
		"arm":   "32-bit ARM",
		"":      "unknown",
	}
	for family, want := range cases {
		if got := Label(family); got != want {
			t.Fatalf("Label(%q) = %q, want %q", family, got, want)
		}
	}
}
