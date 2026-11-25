package health

import (
	"context"
	"fmt"
	"regexp"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// Copy is a running copy of a service other than its first: copy Instance (2 and
// up) on this machine's own IP, or the copy through proxy ProxyID when ProxyID > 0
// (a routed service's copy included). Collect judges the first copy; CheckCopies
// judges these.
type Copy struct {
	Slug     string
	Instance int
	ProxyID  int64
	// State is the runtime's word for the copy. A proxied copy counts as up only
	// while its tunnel runs and has proved its exit, so the caller gives "running"
	// only then.
	State string
}

// key is the copy's entry in the window cache. Neither form can be a slug, so a
// copy never shares a verdict or a wait with its service's first copy.
func (c Copy) key() string {
	if c.ProxyID > 0 {
		return fmt.Sprintf("%s@proxy%d", c.Slug, c.ProxyID)
	}
	return fmt.Sprintf("%s#%d", c.Slug, c.Instance)
}

// Label names the copy in a log line: "proxybase copy 2", "proxybase through proxy 3".
func (c Copy) Label() string {
	if c.ProxyID > 0 {
		return fmt.Sprintf("%s through proxy %d", c.Slug, c.ProxyID)
	}
	return fmt.Sprintf("%s copy %d", c.Slug, c.Instance)
}

// CopyWindowReader reads hours of one copy's logs, filtered. *services.Manager, narrowed.
type CopyWindowReader interface {
	CopyLogsMatching(ctx context.Context, slug string, instance int, proxyID int64, since time.Duration, match *regexp.Regexp, lines int) (string, error)
	SignalVars(slug string) map[string]string
}

// CopyRestarter restarts one copy that a signal with restart: true found stuck,
// unless the owner stopped it after listedAt. *services.Manager, narrowed.
type CopyRestarter interface {
	RestartStuckCopy(ctx context.Context, slug string, instance int, proxyID int64, listedAt time.Time, reason string) error
}

// CheckCopies runs the windowed health check on every copy of a service besides its
// first, with the same window, the same 30-minute cadence and the same 6-hour wait
// between two restarts, held per copy (web CashPilot #809). Only the signals with
// restart: true are read: no page shows a verdict for one of these copies, so a
// signal that only warns would cost hours of logs to learn nothing. Reads and
// restarts run in the background; it returns at once.
func CheckCopies(reader CopyWindowReader, services ServiceLookup, copies []Copy) {
	if reader == nil || services == nil || len(copies) == 0 {
		return
	}
	listedAt := time.Now()
	restarter, canRestart := reader.(CopyRestarter)
	for _, cp := range copies {
		if cp.Slug == "" || (cp.ProxyID <= 0 && cp.Instance < 2) {
			continue
		}
		if !isUp(cp.State) {
			windows.forget(cp.key())
			continue
		}
		svc, ok := services.Get(cp.Slug)
		if !ok {
			continue
		}
		signals := restartSignals(svc.Docker.HealthSignals)
		if len(signals) == 0 {
			continue
		}
		windows.hitsOf(copyTarget(reader, restarter, canRestart, cp), signals, listedAt)
	}
}

// restartSignals are the windowed signals that restart the copy they hold for.
func restartSignals(signals []catalog.HealthSignal) []catalog.HealthSignal {
	var out []catalog.HealthSignal
	for _, s := range signals {
		if IsWindowed(s) && s.Restart {
			out = append(out, s)
		}
	}
	return out
}

func copyTarget(reader CopyWindowReader, restarter CopyRestarter, canRestart bool, cp Copy) target {
	t := target{
		key:   cp.key(),
		label: cp.Label(),
		read: func(ctx context.Context, since time.Duration, match *regexp.Regexp, lines int) (string, error) {
			return reader.CopyLogsMatching(ctx, cp.Slug, cp.Instance, cp.ProxyID, since, match, lines)
		},
		vars: func() map[string]string { return reader.SignalVars(cp.Slug) },
	}
	if canRestart {
		t.restart = func(ctx context.Context, listedAt time.Time, reason string) error {
			return restarter.RestartStuckCopy(ctx, cp.Slug, cp.Instance, cp.ProxyID, listedAt, reason)
		}
	}
	return t
}
