package runtime

import (
	"fmt"
	"sort"
	"strings"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
)

// Removing a service must not be able to destroy something that cannot be got back.
//
// Some of these services keep their whole identity on disk: a Mysterium keystore, a
// Storj node identity whose ID is proof-of-work bound and takes hours to regenerate and
// whose loss forfeits the held payout balance. There is no server-side copy and no
// backup. Until now Remove deleted every named volume the container had, with one
// confirm that said "and its Docker volumes" — so a user tidying up a service they
// meant to redeploy next week destroyed the node instead.
//
// So: removing a service removes the CONTAINER. The data stays unless the user asks for
// it separately, and asking for the volumes the catalog marks unrecoverable takes one
// more explicit yes than asking for the rest.

// RemoveOptions is how far a Remove is allowed to go.
type RemoveOptions struct {
	// DeleteData also deletes the named volumes the container mounts. Default false:
	// the container goes, the data stays, and deploying the service again picks it
	// back up. Host folders (bind mounts) are never touched either way.
	DeleteData bool

	// Critical maps container paths whose contents cannot be recovered to a plain
	// sentence saying what is lost. A nil map means nobody could say — no catalog
	// entry for this service — and is treated as "assume every volume is critical",
	// because an unknown must never quietly unlock an irreversible delete. An empty
	// non-nil map means the catalog was read and declared none.
	Critical map[string]string

	// AllowCritical is the separate, explicit yes to destroying the volumes named in
	// Critical. Without it a delete that would touch one is refused and names it, so
	// the dangerous act cannot happen as a side effect of the ordinary one.
	AllowCritical bool

	// StopTimeout is the grace period, in seconds, the service's catalog entry asks
	// for. Remove stops the container before deleting it, and data that is being kept
	// has to be left consistent, so a storage node that asks for 300 seconds gets
	// them here too. 0 means the caller had no entry to read, and the runtime falls
	// back to what the container itself says.
	StopTimeout int
}

// DataMount is one place a deployed service keeps data on this machine.
type DataMount struct {
	// Target is the path inside the container.
	Target string `json:"target"`
	// Source is the named volume, or the host folder for a bind mount.
	Source string `json:"source"`
	// Volume is true for a named Docker volume — the only kind Remove can delete.
	// A bind mount is a folder the user owns and is always left alone.
	Volume bool `json:"volume"`
	// Critical marks data that cannot be recovered if it is destroyed.
	Critical bool `json:"critical"`
	// Holds is what is lost, in plain words, for the confirmation the user reads.
	Holds string `json:"holds"`
}

// RemovalPlan is what removing a service would do, so the question put to the user can
// name the actual data rather than "and its Docker volumes".
type RemovalPlan struct {
	Slug string `json:"slug"`
	// Name is the container that will be removed.
	Name string `json:"name"`
	// Volumes are the named volumes that "delete the data too" would destroy.
	Volumes []DataMount `json:"volumes"`
	// Binds are host folders the service uses. They are never deleted; they are
	// reported so the user can see their data survives.
	Binds []DataMount `json:"binds"`
	// CatalogKnown is false when this service is no longer in the catalog, so nothing
	// could be checked for criticality and every volume is treated as irreplaceable.
	CatalogKnown bool `json:"catalogKnown"`
}

// HasCritical reports whether deleting the data would destroy something unrecoverable.
func (p RemovalPlan) HasCritical() bool {
	for _, v := range p.Volumes {
		if v.Critical {
			return true
		}
	}
	return false
}

// unknownCriticalHolds is what the user is told when the service has left the catalog,
// so nothing can say whether its data matters.
const unknownCriticalHolds = "This service is no longer in the catalog, so CashPilot cannot tell what this volume holds. It may be irreplaceable."

// CriticalTargets maps the container paths a catalog entry marks unrecoverable to what
// is lost if they are destroyed. The result is always non-nil, so a caller can tell
// "the catalog says none" (empty) from "nobody could say" (nil, which the caller
// supplies itself when there is no entry at all).
//
// Paths are normalised, because a catalog entry written "/app/identity/" has to match
// Docker's "/app/identity" or the guard fails open on the only irreversible path there
// is.
func CriticalTargets(svc catalog.Service) map[string]string {
	targets := make(map[string]string, len(svc.Docker.CriticalVolumes))
	for _, cv := range svc.Docker.CriticalVolumes {
		target := normaliseMountTarget(cv.Target)
		if target == "" {
			continue
		}
		targets[target] = strings.TrimSpace(cv.Holds)
	}
	return targets
}

// normaliseMountTarget strips a trailing slash so a catalog path and Docker's reported
// destination compare equal. Root is left alone.
func normaliseMountTarget(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "/" {
		return "/"
	}
	return strings.TrimRight(trimmed, "/")
}

// planFromMounts turns a live container's mounts plus the catalog's critical list into
// the plan the user is shown. critical is nil when nothing could be consulted, which
// marks every named volume critical.
func planFromMounts(slug, name string, mounts []container.MountPoint, critical map[string]string) RemovalPlan {
	plan := RemovalPlan{Slug: slug, Name: name, CatalogKnown: critical != nil}
	for _, m := range mounts {
		target := normaliseMountTarget(m.Destination)
		entry := DataMount{Target: target}
		if m.Type == mount.TypeVolume && m.Name != "" {
			entry.Source = m.Name
			entry.Volume = true
			if critical == nil {
				entry.Critical = true
				entry.Holds = unknownCriticalHolds
			} else if holds, ok := critical[target]; ok {
				entry.Critical = true
				entry.Holds = holds
			}
			plan.Volumes = append(plan.Volumes, entry)
			continue
		}
		if m.Type == mount.TypeBind && m.Source != "" {
			entry.Source = m.Source
			if critical != nil {
				if holds, ok := critical[target]; ok {
					entry.Critical = true
					entry.Holds = holds
				}
			}
			plan.Binds = append(plan.Binds, entry)
		}
	}
	sort.Slice(plan.Volumes, func(i, j int) bool { return plan.Volumes[i].Target < plan.Volumes[j].Target })
	sort.Slice(plan.Binds, func(i, j int) bool { return plan.Binds[i].Target < plan.Binds[j].Target })
	return plan
}

// blockedCriticalVolumes returns the named volumes a delete would destroy that the
// caller has not explicitly agreed to lose.
func blockedCriticalVolumes(plan RemovalPlan, opts RemoveOptions) []DataMount {
	if !opts.DeleteData || opts.AllowCritical {
		return nil
	}
	var blocked []DataMount
	for _, v := range plan.Volumes {
		if v.Critical {
			blocked = append(blocked, v)
		}
	}
	return blocked
}

// criticalRefusal is the error a delete is refused with. It names every volume and what
// it holds, so the message is enough on its own to decide with — a bare refusal would
// send the user looking for the reason somewhere it is not written down.
func criticalRefusal(slug string, blocked []DataMount) error {
	parts := make([]string, 0, len(blocked))
	for _, v := range blocked {
		parts = append(parts, fmt.Sprintf("%s (%s): %s", v.Source, v.Target, v.Holds))
	}
	return fmt.Errorf("%s keeps data that cannot be recovered, so it was not deleted: %s", slug, strings.Join(parts, "; "))
}
