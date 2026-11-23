package services

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
)

// failedStartProvider is a provider that reports earners Docker could not start and
// records every start it is asked for.
type failedStartProvider struct {
	*fakeProvider
	failed  []runtime.FailedStart
	listErr error
	starts  []string
	// duringStart, when set, runs inside every Start, so a test can act while a
	// start is in flight (one that waits on a deploy's lock).
	duringStart func()
}

func (f *failedStartProvider) FailedStarts(context.Context) ([]runtime.FailedStart, error) {
	return f.failed, f.listErr
}

func (f *failedStartProvider) Start(_ context.Context, slug string) error {
	f.starts = append(f.starts, slug)
	if f.duringStart != nil {
		f.duringStart()
	}
	return f.startErr
}

func (f *failedStartProvider) Copies(context.Context, string) ([]int, error) { return nil, nil }
func (f *failedStartProvider) StopCopy(context.Context, string, int, int) error {
	return nil
}
func (f *failedStartProvider) StartCopy(_ context.Context, slug string, n int) error {
	f.starts = append(f.starts, fmt.Sprintf("%s-%d", slug, n))
	return f.startErr
}
func (f *failedStartProvider) LogsCopy(context.Context, string, int, int) (string, error) {
	return "", nil
}
func (f *failedStartProvider) RemoveCopy(context.Context, string, int, int) error { return nil }

const goferError = "failed to create task for container: OCI runtime create failed: creating gofer filestore files: file exists"

func newFailedStartRig(t *testing.T, failed ...runtime.FailedStart) (*Manager, *failedStartProvider) {
	t.Helper()
	p := &failedStartProvider{fakeProvider: &fakeProvider{}, failed: failed}
	return NewManager(p, newTestCatalog(t), newTestStore(t)), p
}

func withStartRetryClock(t *testing.T) *time.Time {
	t.Helper()
	now := time.Date(2026, 11, 23, 1, 0, 0, 0, time.UTC)
	old := startRetryNow
	startRetryNow = func() time.Time { return now }
	t.Cleanup(func() { startRetryNow = old })
	return &now
}

// THE RULE: an earner Docker could not start at its own start is started again by
// the app, because Docker never tries it a second time. The provider decides which
// containers qualify (internal/runtime/failed_starts_test.go); this is the retry.
func TestStartFailedStartsStartsEachEarnerAsItself(t *testing.T) {
	withStartRetryClock(t)
	m, p := newFailedStartRig(t,
		runtime.FailedStart{ID: "a", Name: "cashpilot-example", Slug: "example", Instance: 1, Error: goferError},
		runtime.FailedStart{ID: "b", Name: "cashpilot-example-2", Slug: "example", Instance: 2, Error: goferError},
	)
	got := m.StartFailedStarts(context.Background())
	if !slices.Equal(got, []string{"cashpilot-example", "cashpilot-example-2"}) {
		t.Fatalf("started %v", got)
	}
	if !slices.Equal(p.starts, []string{"example", "example-2"}) {
		t.Fatalf("starts %v", p.starts)
	}
}

// Nothing listed means nothing started: the positive control for the test above.
func TestStartFailedStartsStartsNothingWhenNothingFailed(t *testing.T) {
	withStartRetryClock(t)
	m, p := newFailedStartRig(t)
	if got := m.StartFailedStarts(context.Background()); len(got) != 0 || len(p.starts) != 0 {
		t.Fatalf("started %v, starts %v", got, p.starts)
	}
}

func TestStartFailedStartsTriesEachContainerOncePerRetryInterval(t *testing.T) {
	now := withStartRetryClock(t)
	m, p := newFailedStartRig(t, runtime.FailedStart{ID: "a", Name: "cashpilot-example", Slug: "example", Instance: 1, Error: goferError})
	p.startErr = errors.New("creating gofer filestore files: file exists")
	ctx := context.Background()
	if got := m.StartFailedStarts(ctx); len(got) != 0 {
		t.Fatalf("a start that failed was reported as started: %v", got)
	}
	*now = now.Add(StartRetry - time.Second)
	m.StartFailedStarts(ctx)
	if len(p.starts) != 1 {
		t.Fatalf("tried again before StartRetry: %v", p.starts)
	}
	*now = now.Add(time.Second)
	m.StartFailedStarts(ctx)
	if len(p.starts) != 2 {
		t.Fatalf("not tried again after StartRetry: %v", p.starts)
	}
}

// A container that is no longer listed (it runs, or it was removed) is forgotten, so
// a later failure of the same container is tried at once.
func TestStartFailedStartsForgetsAContainerThatIsNoLongerListed(t *testing.T) {
	withStartRetryClock(t)
	failed := runtime.FailedStart{ID: "a", Name: "cashpilot-example", Slug: "example", Instance: 1, Error: goferError}
	m, p := newFailedStartRig(t, failed)
	ctx := context.Background()
	m.StartFailedStarts(ctx)
	p.failed = nil
	m.StartFailedStarts(ctx)
	p.failed = []runtime.FailedStart{failed}
	m.StartFailedStarts(ctx)
	if len(p.starts) != 2 {
		t.Fatalf("starts %v, want a second try once it was forgotten", p.starts)
	}
}

// A daemon that cannot be listed starts nothing and keeps what was tried, so a
// daemon that comes and goes does not make every pass try at once.
func TestStartFailedStartsKeepsItsRecordWhenTheListFails(t *testing.T) {
	withStartRetryClock(t)
	m, p := newFailedStartRig(t, runtime.FailedStart{ID: "a", Name: "cashpilot-example", Slug: "example", Instance: 1, Error: goferError})
	ctx := context.Background()
	m.StartFailedStarts(ctx)
	p.listErr = errors.New("daemon gone")
	if got := m.StartFailedStarts(ctx); got != nil {
		t.Fatalf("started %v with no list", got)
	}
	p.listErr = nil
	m.StartFailedStarts(ctx)
	if len(p.starts) != 1 {
		t.Fatalf("starts %v: the failed list made the next pass try again at once", p.starts)
	}
}

// Each try goes through Start, so what holds a Start from the dashboard holds here.
func TestStartFailedStartsHonoursTheHoldsOfStart(t *testing.T) {
	t.Run("proxied only", func(t *testing.T) {
		withStartRetryClock(t)
		t.Setenv(ProxiedOnlyEnv, "true")
		m, p := newFailedStartRig(t, runtime.FailedStart{ID: "a", Name: "cashpilot-example", Slug: "example", Instance: 1, Error: goferError})
		if got := m.StartFailedStarts(context.Background()); len(got) != 0 || len(p.starts) != 0 {
			t.Fatalf("a direct earner started on a proxied-only machine: %v %v", got, p.starts)
		}
	})
	t.Run("restore not finished", func(t *testing.T) {
		withStartRetryClock(t)
		m, p := newFailedStartRig(t, runtime.FailedStart{ID: "a", Name: "cashpilot-example", Slug: "example", Instance: 1, Error: goferError})
		if err := m.store.HoldRestore("example", map[string]string{"example-data": "/data"}, time.Now()); err != nil {
			t.Fatal(err)
		}
		if got := m.StartFailedStarts(context.Background()); len(got) != 0 || len(p.starts) != 0 {
			t.Fatalf("an earner held by a restore started: %v %v", got, p.starts)
		}
	})
}

// A provider without FailedStarts (the native runtime) is skipped, never asked.
func TestStartFailedStartsSkipsAProviderThatCannotList(t *testing.T) {
	m := NewManager(&fakeProvider{}, newTestCatalog(t), newTestStore(t))
	if got := m.StartFailedStarts(context.Background()); got != nil {
		t.Fatalf("started %v", got)
	}
}

// The interval counts from each try, not from the start of the pass: a Start that
// waited on a deploy must not shorten the next one's interval.
func TestStartFailedStartsCountsTheIntervalFromEachTry(t *testing.T) {
	now := withStartRetryClock(t)
	m, p := newFailedStartRig(t,
		runtime.FailedStart{ID: "a", Name: "cashpilot-example", Slug: "example", Instance: 1, Error: goferError},
		runtime.FailedStart{ID: "b", Name: "cashpilot-manual-svc", Slug: "manual-svc", Instance: 1, Error: goferError},
	)
	p.startErr = errors.New("still broken")
	p.duringStart = func() {
		if len(p.starts) == 1 {
			*now = now.Add(4 * time.Minute) // the first start waited on a deploy
		}
	}
	ctx := context.Background()
	m.StartFailedStarts(ctx)
	p.duringStart = nil
	*now = now.Add(StartRetry - 4*time.Minute) // StartRetry after the first try
	m.StartFailedStarts(ctx)
	if !slices.Equal(p.starts, []string{"example", "manual-svc", "example"}) {
		t.Fatalf("starts %v: the second container was tried again one minute after its try", p.starts)
	}
}

// One pass at a time: a pass that finds another still running (its Start waiting
// on a deploy) returns at once and starts nothing.
func TestStartFailedStartsRunsOnePassAtATime(t *testing.T) {
	withStartRetryClock(t)
	m, p := newFailedStartRig(t, runtime.FailedStart{ID: "a", Name: "cashpilot-example", Slug: "example", Instance: 1, Error: goferError})
	var second []string
	p.duringStart = func() {
		p.duringStart = nil
		// A container that is due, so only the one-pass rule can keep this
		// second pass from starting it.
		p.failed = append(p.failed, runtime.FailedStart{ID: "c", Name: "cashpilot-manual-svc", Slug: "manual-svc", Instance: 1, Error: goferError})
		second = m.StartFailedStarts(context.Background())
	}
	got := m.StartFailedStarts(context.Background())
	if len(second) != 0 || len(p.starts) != 1 {
		t.Fatalf("a pass ran inside another: second %v, starts %v", second, p.starts)
	}
	if !slices.Equal(got, []string{"cashpilot-example"}) {
		t.Fatalf("started %v", got)
	}
	// Positive control: once the first pass is over, the lock is free again and
	// the container the second pass skipped starts.
	if got := m.StartFailedStarts(context.Background()); !slices.Equal(got, []string{"cashpilot-manual-svc"}) {
		t.Fatalf("the pass after it started %v", got)
	}
}
