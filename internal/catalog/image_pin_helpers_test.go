package catalog

import (
	"fmt"
	"strings"
)

// pinExemptStatuses are the service lifecycle states that release a service from the
// immutable-digest-pin requirement: retired services whose upstream images may no
// longer resolve and which are never deployed.
var pinExemptStatuses = map[string]bool{
	"dead":    true,
	"dropped": true,
	"broken":  true,
}

// isPinExempt reports whether a service's status exempts it from the digest-pin rule.
func isPinExempt(status string) bool {
	return pinExemptStatuses[strings.ToLower(strings.TrimSpace(status))]
}

// unpinnedImages returns a human-readable entry ("slug (status): \"image\"") for every
// service that is REQUIRED to pin its image to an immutable digest but does not. A
// service is required to pin when it is not pin-exempt (dead/dropped/broken) and it
// declares a non-empty docker.image. An image is considered pinned when it contains
// an "@sha256:" digest reference. The returned slice is empty when every live image
// is pinned.
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
		if !strings.Contains(image, "@sha256:") {
			offenders = append(offenders, fmt.Sprintf("%s (%s): %q", svc.Slug, svc.Status, svc.Docker.Image))
		}
	}
	return offenders
}
