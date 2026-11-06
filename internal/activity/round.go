package activity

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"sync"
	"time"
)

func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

// Runner runs rounds. One round at a time; a round asked for while another
// runs is skipped.
type Runner struct {
	Engine Engine
	Worker string
	Deltas *Deltas
	// Now and Monotonic are swappable for tests.
	Now func() time.Time

	busy sync.Mutex
}

// RoundTimeout bounds a whole round, Docker calls and the helper image pull
// included, so a stalled daemon cannot hold the sampler loop (and its hourly
// purge) past the next window. A variable so tests can shorten it.
var RoundTimeout = 270 * time.Second

// ErrBusy is a round asked for while another is still running.
var ErrBusy = errors.New("a round is already running")

// sweepTimeout bounds one sweep. A variable so a test need not wait for it.
var sweepTimeout = ProbeTimeout

// Sweep removes every helper this app left behind, and stopped helpers of
// apps that labelled no worker. It returns the names Docker would not remove.
func (r *Runner) Sweep(ctx context.Context) []string {
	// Bounded on its own: the startup sweep runs outside any round's timeout,
	// and a stalled daemon must not hold the sampler loop.
	ctx, cancel := context.WithTimeout(ctx, sweepTimeout)
	defer cancel()
	var left []string
	probes, err := r.Engine.Probes(ctx)
	if err != nil {
		return left
	}
	for _, p := range probes {
		if !ShouldSweep(p, r.Worker) {
			continue
		}
		if err := r.Engine.Remove(ctx, p.ID); err != nil {
			left = append(left, p.Name)
		}
	}
	return left
}

// Round runs one sampling round: every earner's namespace, at most
// MaxParallel helpers at once, inside RoundBudget.
func (r *Runner) Round(ctx context.Context) (Window, error) {
	if !r.busy.TryLock() {
		return Window{}, ErrBusy
	}
	defer r.busy.Unlock()
	ctx, cancel := context.WithTimeout(ctx, RoundTimeout)
	defer cancel()
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	w := Window{WindowEnd: WindowEnd(now()), WorkerStatus: "ok", Samples: []Sample{}}
	w.LeftoverProbes = r.Sweep(ctx)
	if w.LeftoverProbes == nil {
		w.LeftoverProbes = []string{}
	}
	earners, err := r.Engine.Earners(ctx)
	if err != nil {
		w.WorkerStatus = "no_runtime"
		return w, nil
	}
	if len(earners) == 0 {
		return w, nil
	}
	if err := r.Engine.EnsureImage(ctx, HelperImage); err != nil {
		w.WorkerStatus = "no_image"
		return w, nil
	}
	names := map[string]bool{}
	for _, c := range earners {
		names[c.Name] = true
	}
	lookup := func(id string) (Container, bool) { return r.Engine.Inspect(ctx, id) }
	groups, fixed := Plan(earners, lookup, JoinableRuntimes(r.Engine.Runtimes(ctx)))
	w.Samples = append(w.Samples, fixed...)

	deadline := time.Now().Add(RoundBudget)
	var mu sync.Mutex
	networks := map[string]NetworkInfo{}
	inspect := func(id string) NetworkInfo { return r.Engine.Network(ctx, id, names) }
	ids := make([]string, 0, len(groups))
	for id := range groups {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	results := make([]Sample, len(ids))
	sem := make(chan struct{}, MaxParallel)
	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int, id string) {
			defer wg.Done()
			defer func() { <-sem }()
			g := groups[id]
			name := g.Owner.Name
			if name == "" {
				name = id[:min(12, len(id))]
			}
			if time.Now().After(deadline) {
				results[i] = baseSample(name, g.Members, g.Mode, "skipped_budget", "")
				return
			}
			mu.Lock()
			nctx := BuildContext(g.Owner, g.Kind, networks, inspect)
			mu.Unlock()
			out, leftover, err := r.Engine.RunProbe(ctx, ProbeSpec{Name: ProbeName(r.Worker, id), OwnerID: id, Kind: g.Kind, Worker: r.Worker, Context: nctx})
			if leftover {
				mu.Lock()
				w.LeftoverProbes = append(w.LeftoverProbes, ProbeName(r.Worker, id))
				mu.Unlock()
			}
			if err != nil {
				r.Deltas.Forget(id)
				reason := "run_error"
				var pe *ProbeError
				if errors.As(err, &pe) {
					reason = pe.Reason
				}
				results[i] = baseSample(name, g.Members, g.Mode, "probe_failed", reason)
				return
			}
			res, err := ParseProbe(out)
			if err != nil {
				r.Deltas.Forget(id)
				results[i] = baseSample(name, g.Members, g.Mode, "probe_failed", "bad_output")
				return
			}
			results[i] = BuildSample(name, g, res, r.Deltas, float64(time.Now().UnixNano())/1e9)
		}(i, id)
	}
	wg.Wait()
	w.Samples = append(w.Samples, results...)
	live := map[string]bool{}
	for _, id := range ids {
		live[id] = true
	}
	r.Deltas.Keep(live)
	sort.SliceStable(w.Samples, func(a, b int) bool { return w.Samples[a].Container < w.Samples[b].Container })
	Fit(&w)
	return w, nil
}
