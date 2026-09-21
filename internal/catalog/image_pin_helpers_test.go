package catalog

import (
	"fmt"
	"strings"
)

// isPinExempt reports whether a service's status exempts it from the digest-pin rule.
// A retired service (dead/dropped/broken) is exempt: it is never deployed and its
// upstream image may no longer resolve. The rule is IsRetired rather than a second
// copy of the same three statuses, so a new lifecycle state cannot be hidden from the
// UI while still being required to pin an image nobody will ever pull.
func isPinExempt(status string) bool {
	return IsRetired(status)
}

// unpinnedImages returns a human-readable entry ("slug (status): \"image\"") for every
// service that is REQUIRED to pin its image to an immutable digest but does not. A
// service is required to pin when it is not pin-exempt (dead/dropped/broken) and it
// declares a non-empty docker.image. An image counts as pinned only when it carries
// a well-formed sha256 digest (HasDigestPin), so a truncated or hand-typed one is an
// offender rather than a pin that resolves to nothing. The returned slice is empty
// when every live image is pinned.
func unpinnedImages(services []Service) []string {
	var offenders []string
	for _, svc := range services {
		if isPinExempt(svc.Status) {
			continue
		}
		image := strings.TrimSpace(svc.Docker.Image)
		if image == "" {
			continue
		}
		if !HasDigestPin(image) {
			offenders = append(offenders, fmt.Sprintf("%s (%s): %q", svc.Slug, svc.Status, svc.Docker.Image))
		}
	}
	return offenders
}
