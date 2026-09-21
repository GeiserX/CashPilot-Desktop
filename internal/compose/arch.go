package compose

import (
	"fmt"
	"strings"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// The three architecture families a catalog entry can promise a build for, in the
// words the catalog itself uses (image_by_arch keys):
//
//	amd64  x86-64: every catalog image has this build.
//	arm64  64-bit ARM: Raspberry Pi 4/5 on a 64-bit OS, Apple Silicon, Ampere.
//	arm    32-bit ARM: Raspberry Pi 2/3, and any Pi on the 32-bit OS.
//
// This mirrors app/arch.py in the web CashPilot. Only the family matters for an
// export: within 32-bit ARM the builds run each other's binaries, so the variant
// changes nothing about which image line to write.
var families = map[string]bool{"amd64": true, "arm64": true, "arm": true}

// archFamily validates the caller's architecture choice.
//
// Empty is allowed and means "let the machine running this file pick from the image
// manifest", which is right for every entry except the ones whose ARM builds Docker
// cannot pick itself. Anything else that is not one of the three families is an
// error rather than a fallback: silently exporting an amd64 file for a Raspberry Pi
// produces a container that dies with "exec format error", and a typo is far more
// likely than a deliberate "any".
func archFamily(arch string) (string, error) {
	family := strings.ToLower(strings.TrimSpace(arch))
	if family == "" {
		return "", nil
	}
	if !families[family] {
		return "", fmt.Errorf("unknown architecture %q: choose amd64, arm64 or arm", arch)
	}
	return family, nil
}

// imageFor is the image an exported file should run on a machine of this family.
//
// Docker picks the right build out of a multi-arch manifest by itself, so almost
// every entry needs only docker.image. image_by_arch exists for the images whose ARM
// builds Docker cannot select: traffmonetizer/cli_v2 publishes separate arm64v8 and
// arm32v7 tags and labels every one of them linux/amd64, so a Pi pulling the default
// tag gets an x86-64 binary that cannot start. No family, or a family this entry
// does not name, falls back to docker.image.
func imageFor(docker catalog.DockerConfig, family string) string {
	if family == "" {
		return strings.TrimSpace(docker.Image)
	}
	if override := strings.TrimSpace(docker.ImageByArch[family]); override != "" {
		return override
	}
	return strings.TrimSpace(docker.Image)
}
