package catalog

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestServiceImagesArePinned enforces security fix S1: every live service that ships
// a container image must pin it to an immutable digest (image[:tag]@sha256:...), never
// a floating tag or a bare :latest (a supply-chain risk, and a violation of the repo
// rule "All external container images: pinned semver tags, never :latest").
//
// Retired services (status dead/dropped/broken) are exempt because their upstream
// images may no longer resolve and they are never deployed.
//
// This is the enforcement gate: it PASSES once every live image carries an @sha256:
// digest and FAILS the moment someone adds an unpinned image to a live service.
func TestServiceImagesArePinned(t *testing.T) {
	// Reuse the real catalog loader (catalog.go) against the on-disk ../../services
	// tree. os.DirFS is rooted at the repo root (two levels up from internal/catalog),
	// so LoadEmbedded walks the actual "services" directory shipped in the repo.
	cat, err := LoadEmbedded(os.DirFS(filepath.Join("..", "..")))
	if err != nil {
		t.Fatalf("load real services catalog: %v", err)
	}

	services := cat.List()
	if len(services) < 20 {
		t.Fatalf("loaded only %d services from ../../services; the real catalog did not "+
			"load, so the pin check would be a no-op", len(services))
	}

	if bad := unpinnedImages(services); len(bad) > 0 {
		t.Fatalf("%d live service image(s) are not pinned to an immutable @sha256: digest.\n"+
			"Pin each to \"image[:tag]@sha256:<digest>\", or mark the service "+
			"dead/dropped/broken if it is retired:\n\t%s",
			len(bad), strings.Join(bad, "\n\t"))
	}
}
