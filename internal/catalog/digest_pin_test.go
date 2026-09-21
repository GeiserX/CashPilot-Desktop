package catalog

import (
	"strings"
	"testing"
)

const validDigest = "sha256:62af1d6e0245667765f217bcce90d31c82fe687203abc66af7cf117e12581265"

// THE RULE: an image counts as pinned only when its digest could actually resolve.
//
// The pin rule used to be a substring test for "@sha256:". "repo@sha256:aaaa" passes
// that, resolves at no registry, and is therefore a service reported as pinned that
// is still floating — the supply-chain guarantee the rule exists for, reported as
// held when it is not. A truncated or hand-typed digest is now an offender.
func TestHasDigestPinRequiresAResolvableDigest(t *testing.T) {
	pinned := []string{
		"example/thing@" + validDigest,
		"example/thing:v1.2.3@" + validDigest,
		"ghcr.io/org/thing:latest@" + validDigest,
		"localhost:5000/thing@" + validDigest, // a registry port is not a tag
		"  example/thing@" + validDigest + "  ",
	}
	for _, image := range pinned {
		if !HasDigestPin(image) {
			t.Errorf("HasDigestPin(%q) = false; a well-formed pin was rejected", image)
		}
	}

	floating := map[string]string{
		"":                          "no image at all",
		"example/thing":             "a bare repository",
		"example/thing:latest":      "a floating tag",
		"example/thing@sha256:aaaa": "a digest far too short to be one",
		"example/thing@sha256:":     "the prefix with nothing after it",
		"example/thing@" + validDigest[:len(validDigest)-1]:           "63 hex characters",
		"example/thing@" + validDigest + "0":                          "65 hex characters",
		"example/thing@" + strings.ToUpper(validDigest):               "uppercase hex, which no registry returns",
		"example/thing@sha512:" + strings.Repeat("a", 64):             "a digest algorithm this rule does not accept",
		"example/thing@sha256:" + strings.Repeat("g", 64):             "64 characters that are not hex",
		"example/thing:" + strings.TrimPrefix(validDigest, "sha256:"): "the digest used as a tag, with no @",
	}
	for image, why := range floating {
		if HasDigestPin(image) {
			t.Errorf("HasDigestPin(%q) = true, but it is %s", image, why)
		}
	}
}

// The gate that consumes the rule has to move with it: a live entry carrying a
// truncated digest is an unpinned image, and must be reported as one.
func TestUnpinnedImagesFlagsAMalformedDigest(t *testing.T) {
	services := []Service{
		{Slug: "good", Status: "active"},
		{Slug: "truncated", Status: "active"},
		{Slug: "retired", Status: "dead"},
	}
	services[0].Docker.Image = "example/good@" + validDigest
	services[1].Docker.Image = "example/truncated@sha256:aaaa"
	services[2].Docker.Image = "example/retired@sha256:aaaa"

	offenders := unpinnedImages(services)
	if len(offenders) != 1 {
		t.Fatalf("unpinnedImages = %v; want only the truncated live entry", offenders)
	}
	if !strings.Contains(offenders[0], "truncated") {
		t.Errorf("unpinnedImages named %q, want the truncated entry", offenders[0])
	}
}
