package runtime

import (
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
)

func volumeMount(name, destination string) container.MountPoint {
	return container.MountPoint{Type: mount.TypeVolume, Name: name, Destination: destination, RW: true}
}

func bindMount(source, destination string) container.MountPoint {
	return container.MountPoint{Type: mount.TypeBind, Source: source, Destination: destination, RW: true}
}

// THE RULE: a volume the catalog marks unrecoverable is never deleted as a side effect
// of removing a service.
//
// Mysterium's volume is the node's identity keystore; Storj's is an identity whose ID
// is proof-of-work bound, takes hours to regenerate, and whose loss forfeits the held
// payout balance. Neither exists anywhere else. Removing the service used to delete
// both, behind a confirm that said "and its Docker volumes".
func TestRemoveRefusesCriticalVolumesWithoutAnExplicitYes(t *testing.T) {
	plan := planFromMounts("mysterium", "cashpilot-mysterium",
		[]container.MountPoint{volumeMount("mysterium-data", "/var/lib/mysterium-node")},
		map[string]string{"/var/lib/mysterium-node": "Node identity keystore."})

	blocked := blockedCriticalVolumes(plan, RemoveOptions{DeleteData: true})
	if len(blocked) != 1 || blocked[0].Source != "mysterium-data" {
		t.Fatalf("a critical volume was not blocked: %+v", blocked)
	}

	err := criticalRefusal("mysterium", blocked)
	// The refusal has to be readable on its own: a bare "refused" sends the user
	// looking for the reason somewhere it is not written down.
	for _, want := range []string{"mysterium-data", "/var/lib/mysterium-node", "Node identity keystore."} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the refusal does not mention %q: %v", want, err)
		}
	}
}

// The explicit yes is what unblocks it — and it is a different field from "delete the
// data", so the dangerous act cannot ride along with the ordinary one.
func TestAllowCriticalIsWhatUnblocksTheDelete(t *testing.T) {
	plan := planFromMounts("storj", "cashpilot-storj",
		[]container.MountPoint{volumeMount("storj-identity", "/app/identity")},
		map[string]string{"/app/identity": "Node identity."})

	if blocked := blockedCriticalVolumes(plan, RemoveOptions{DeleteData: true, AllowCritical: true}); len(blocked) != 0 {
		t.Fatalf("an explicit yes still blocked: %+v", blocked)
	}
	// And a remove that is not deleting data has nothing to block in the first place.
	if blocked := blockedCriticalVolumes(plan, RemoveOptions{}); len(blocked) != 0 {
		t.Fatalf("keeping the data still produced a block: %+v", blocked)
	}
}

// A volume the catalog says nothing about is ordinary: it goes when the data goes. A
// guard that cried wolf on a cache would be a guard nobody reads on a keystore.
func TestOrdinaryVolumesAreNotBlocked(t *testing.T) {
	plan := planFromMounts("svc", "cashpilot-svc",
		[]container.MountPoint{volumeMount("svc-cache", "/cache")},
		map[string]string{"/app/identity": "something else entirely"})

	if plan.HasCritical() {
		t.Fatal("a volume the catalog does not mark was reported as critical")
	}
	if blocked := blockedCriticalVolumes(plan, RemoveOptions{DeleteData: true}); len(blocked) != 0 {
		t.Fatalf("an ordinary volume was blocked: %+v", blocked)
	}
}

// A service that has left the catalog cannot be checked, and an unknown must never
// quietly unlock an irreversible delete.
func TestUnknownServiceTreatsEveryVolumeAsCritical(t *testing.T) {
	plan := planFromMounts("gone", "cashpilot-gone",
		[]container.MountPoint{volumeMount("gone-data", "/data")}, nil)

	if plan.CatalogKnown {
		t.Fatal("a plan built with no catalog claims the catalog was read")
	}
	if !plan.HasCritical() {
		t.Fatal("a volume nobody could check was treated as safe to delete")
	}
	if blocked := blockedCriticalVolumes(plan, RemoveOptions{DeleteData: true}); len(blocked) != 1 {
		t.Fatalf("an unchecked volume was not blocked: %+v", blocked)
	}
}

// Bind mounts are folders the user owns. They are reported so the confirmation can say
// they survive, and they are never in the deletable list — a user who pointed Storj at
// a 12 TB disk must not have it in scope.
func TestBindMountsAreReportedButNeverDeletable(t *testing.T) {
	plan := planFromMounts("storj", "cashpilot-storj", []container.MountPoint{
		bindMount("/Volumes/Storj", "/app/config"),
		volumeMount("storj-identity", "/app/identity"),
	}, map[string]string{"/app/identity": "Node identity.", "/app/config": "Stored customer data."})

	if len(plan.Volumes) != 1 || plan.Volumes[0].Source != "storj-identity" {
		t.Fatalf("Volumes = %+v, want only the named volume", plan.Volumes)
	}
	if len(plan.Binds) != 1 || plan.Binds[0].Source != "/Volumes/Storj" {
		t.Fatalf("Binds = %+v, want the host folder", plan.Binds)
	}
	// A bind marked critical is still never deleted, so it must not turn up in the
	// block list either — there is nothing to block.
	if blocked := blockedCriticalVolumes(plan, RemoveOptions{DeleteData: true, AllowCritical: true}); len(blocked) != 0 {
		t.Fatalf("blocked = %+v", blocked)
	}
}

// A catalog entry written "/app/identity/" has to match Docker's "/app/identity", or
// the guard fails open on the only irreversible path there is.
func TestCriticalTargetsNormaliseTrailingSlashes(t *testing.T) {
	svc := catalog.Service{Docker: catalog.DockerConfig{CriticalVolumes: []catalog.CriticalVolume{
		{Target: "/app/identity/", Holds: "Node identity."},
		{Target: "  /app/config  ", Holds: "Stored data."},
		{Target: "   ", Holds: "nothing"},
	}}}

	targets := CriticalTargets(svc)
	if _, ok := targets["/app/identity"]; !ok {
		t.Fatalf("a trailing slash broke the match: %+v", targets)
	}
	if _, ok := targets["/app/config"]; !ok {
		t.Fatalf("surrounding space broke the match: %+v", targets)
	}
	if len(targets) != 2 {
		t.Fatalf("a blank target became an entry: %+v", targets)
	}

	// The map must be non-nil even when the entry declares none, so "the catalog says
	// none" can be told apart from "nobody could say".
	empty := CriticalTargets(catalog.Service{})
	if empty == nil {
		t.Fatal("CriticalTargets returned nil for an entry with no critical volumes")
	}

	// And it has to actually match a live mount reported without the slash.
	plan := planFromMounts("storj", "cashpilot-storj",
		[]container.MountPoint{volumeMount("storj-identity", "/app/identity")}, targets)
	if !plan.HasCritical() {
		t.Fatal("a normalised catalog target did not match the live mount")
	}
}

// An anonymous volume has no name to delete and no name to keep. It must not appear as
// something the user can choose about.
func TestAnonymousVolumesAreNotOffered(t *testing.T) {
	anonymous := container.MountPoint{Type: mount.TypeVolume, Name: "", Destination: "/scratch", RW: true}
	plan := planFromMounts("svc", "cashpilot-svc", []container.MountPoint{anonymous}, map[string]string{})
	if len(plan.Volumes) != 0 {
		t.Fatalf("an unnamed volume was offered for deletion: %+v", plan.Volumes)
	}
}
