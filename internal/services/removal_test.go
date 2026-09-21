package services

import (
	"context"
	"testing"
	"testing/fstest"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
)

// identityServiceYAML is an entry that keeps something irreplaceable: the shape
// Mysterium and Storj have, and the reason the removal guard exists.
const identityServiceYAML = `name: Identity Keeper
slug: keeper
category: bandwidth
status: active
docker:
  image: keeper/image:1.0.0
  volumes:
    - "keeper-data:/var/lib/keeper"
  critical_volumes:
    - target: "/var/lib/keeper"
      holds: "Node identity keystore. A new one is a different node."
`

func newRemovalCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	cat, err := catalog.LoadEmbedded(fstest.MapFS{
		"services/bandwidth/example.yml": {Data: []byte(exampleServiceYAML)},
		"services/bandwidth/keeper.yml":  {Data: []byte(identityServiceYAML)},
	})
	if err != nil {
		t.Fatalf("LoadEmbedded error: %v", err)
	}
	return cat
}

// THE RULE: the manager hands the runtime what the CATALOG says is irreplaceable, and
// the user's answer, separately.
//
// The guard that refuses to destroy a keystore lives in the runtime, and it can only
// work on a list. If the manager passed an empty one, the refusal would never fire and
// every test of it would still pass — which is exactly the shape of bug the guard is
// there to stop.
func TestRemovePassesTheCatalogsCriticalListAndTheUsersChoice(t *testing.T) {
	st := newTestStore(t)
	fake := &fakeProvider{}
	m := NewManager(fake, newRemovalCatalog(t), st)

	if err := m.Remove(context.Background(), "keeper", false, false); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(fake.removeOpts) != 1 {
		t.Fatalf("Remove reached the runtime %d times", len(fake.removeOpts))
	}
	opts := fake.removeOpts[0]
	if opts.DeleteData || opts.AllowCritical {
		t.Fatalf("the default remove asked to delete data: %+v", opts)
	}
	holds, ok := opts.Critical["/var/lib/keeper"]
	if !ok {
		t.Fatalf("the catalog's critical volume never reached the runtime: %+v", opts.Critical)
	}
	if holds == "" {
		t.Fatal("the critical volume arrived with no explanation of what is lost")
	}

	// And the user's two answers travel separately, so the dangerous one cannot ride
	// along with the ordinary one.
	if err := m.Remove(context.Background(), "keeper", true, true); err != nil {
		t.Fatalf("Remove (delete data): %v", err)
	}
	if opts := fake.removeOpts[1]; !opts.DeleteData || !opts.AllowCritical {
		t.Fatalf("the explicit choice was not passed through: %+v", opts)
	}
}

// A service with no critical volumes must send an EMPTY list, not a missing one: the
// runtime reads a missing list as "nobody could say" and refuses on it, which would
// block an ordinary cleanup.
func TestRemoveSendsAnEmptyListForAServiceWithNothingIrreplaceable(t *testing.T) {
	st := newTestStore(t)
	fake := &fakeProvider{}
	m := NewManager(fake, newRemovalCatalog(t), st)

	if err := m.Remove(context.Background(), "example", true, false); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if fake.removeOpts[0].Critical == nil {
		t.Fatal("a service the catalog knows sent a nil critical list, which reads as unknown")
	}
	if len(fake.removeOpts[0].Critical) != 0 {
		t.Fatalf("a service with no critical volumes sent %+v", fake.removeOpts[0].Critical)
	}
}

// A service that has left the catalog cannot be checked. It must send nothing at all,
// so the runtime refuses rather than guesses.
func TestRemoveSendsNoListForAServiceTheCatalogDoesNotKnow(t *testing.T) {
	st := newTestStore(t)
	fake := &fakeProvider{}
	m := NewManager(fake, newRemovalCatalog(t), st)

	if err := m.Remove(context.Background(), "vanished", false, false); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if fake.removeOpts[0].Critical != nil {
		t.Fatalf("an unknown service claimed to know its critical volumes: %+v", fake.removeOpts[0].Critical)
	}
}

// PlanRemoval feeds the confirmation the user reads, so it needs the same list.
func TestPlanRemovalCarriesTheCatalogsCriticalList(t *testing.T) {
	st := newTestStore(t)
	fake := &fakeProvider{}
	m := NewManager(fake, newRemovalCatalog(t), st)

	plan, err := m.PlanRemoval(context.Background(), "keeper")
	if err != nil {
		t.Fatalf("PlanRemoval: %v", err)
	}
	if plan.Slug != "keeper" {
		t.Fatalf("PlanRemoval returned %+v", plan)
	}
	if _, ok := fake.planCritical[0]["/var/lib/keeper"]; !ok {
		t.Fatalf("the plan was built without the catalog's critical volume: %+v", fake.planCritical[0])
	}

	if _, err := m.PlanRemoval(context.Background(), "vanished"); err != nil {
		t.Fatalf("PlanRemoval (unknown): %v", err)
	}
	if fake.planCritical[1] != nil {
		t.Fatalf("an unknown service produced a critical list: %+v", fake.planCritical[1])
	}
}

// CriticalTargets is the one definition of "irreplaceable", shared by the question the
// user answers and the guard that refuses. Two definitions would drift.
func TestCriticalTargetsMatchesTheCatalogEntry(t *testing.T) {
	svc, ok := newRemovalCatalog(t).Get("keeper")
	if !ok {
		t.Fatal("the test catalog did not load the keeper entry")
	}
	targets := runtime.CriticalTargets(svc)
	if len(targets) != 1 {
		t.Fatalf("CriticalTargets = %+v", targets)
	}
}
