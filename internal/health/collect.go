package health

import (
	"context"
	"sync"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// LogReader reads a deployed service's recent log tail. It is the one-method slice of
// the services manager this package needs, so the collection can be tested without a
// container runtime.
type LogReader interface {
	Logs(ctx context.Context, slug string, lines int) (string, error)
}

// RunLogReader reads the log of a service's last finished run only, or "" when it has
// none worth judging (see runtime.DockerProvider.LastRunLogs). A refusal is judged
// only through it: a plain tail can hold an earlier run's refusal after the account
// came back, and calling that run disabled hides Start until the service is removed.
type RunLogReader interface {
	LastRunLogs(ctx context.Context, slug string, lines int) (string, error)
}

// ServiceLookup finds a catalog entry by slug: *catalog.Catalog, narrowed.
type ServiceLookup interface {
	Get(slug string) (catalog.Service, bool)
}

// NativeRuntime is the runtime kind for a service supervised as a process on the
// user's own machine (internal/runtime.NativeRuntimeKind). It is repeated here rather
// than imported so this package stays a leaf with no runtime dependency.
const NativeRuntime = "native"

// Deployed is one deployed service as the caller sees it, before any judgement.
type Deployed struct {
	Slug string
	// ContainerState is the runtime's word for it; empty means it could not be asked.
	ContainerState string
	// Runtime is which backend is running it: "docker", "podman" or NativeRuntime.
	Runtime string
}

// logLines is how much log tail to match against. Enough to catch a signal that
// repeats (the ones in the catalog were all seen repeating for hours or days), small
// enough that reading it is not noticeable.
const logLines = 200

// logTimeout bounds one log read. The verdict is a nice-to-have on a dashboard
// refresh, so a wedged container runtime must cost a missing badge, never a frozen
// window.
const logTimeout = 3 * time.Second

// Collect returns the producer verdict for every deployed service, keyed by slug.
//
// Logs are read ONLY for a service whose catalog entry declares signals to match them
// against, and only while its container is up, except the last run's log of a
// container whose entry declares an account_disabled signal, read through a
// RunLogReader — reading a log tail nobody has a
// pattern for would be a container-runtime round trip per service per refresh, spent
// to learn nothing. A natively supervised process is skipped for a second reason: the
// catalog's signals sit under its docker: stanza and explain themselves in terms of a
// container, so matching them there produces advice the user cannot act on.
//
// The log reads run at the same time, each under its own logTimeout, so the whole
// collection costs one timeout at most rather than one per service: ten deployed
// services on a wedged runtime used to hold a dashboard refresh for thirty seconds.
func Collect(ctx context.Context, logs LogReader, services ServiceLookup, deployed []Deployed) map[string]Report {
	if len(deployed) == 0 {
		return nil
	}
	inputs := make([]Input, 0, len(deployed))
	for _, dep := range deployed {
		if dep.Slug == "" {
			continue
		}
		in := Input{
			Slug:           dep.Slug,
			ContainerState: dep.ContainerState,
			Native:         dep.Runtime == NativeRuntime,
		}
		if services != nil && !in.Native {
			if svc, ok := services.Get(dep.Slug); ok {
				in.Signals = svc.Docker.HealthSignals
			}
		}
		inputs = append(inputs, in)
	}

	reader, canWindow := logs.(WindowReader)
	runs, canRun := logs.(RunLogReader)
	var wg sync.WaitGroup
	for i := range inputs {
		in := &inputs[i]
		windowed, _ := windowedOnly(in.Signals)
		if len(windowed) > 0 {
			if !isUp(in.ContainerState) {
				windows.forget(in.Slug)
			} else if canWindow {
				in.WindowHits = windows.hits(reader, in.Slug, windowed)
			}
		}
		if canRun && !in.Native && declaresRefusal(in.Signals) && mayBeRefused(in.ContainerState) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if out, ok := readLogs(ctx, runs.LastRunLogs, in.Slug, refusalLines); ok {
					in.Refusal = MatchRefusal(out, in.Signals)
				}
			}()
		}
		if len(in.Signals) == 0 || logs == nil || !isUp(in.ContainerState) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			in.Logs, in.LogsRead = readLogs(ctx, logs.Logs, in.Slug, logLines)
		}()
	}
	wg.Wait()

	out := make(map[string]Report, len(inputs))
	for _, in := range inputs {
		out[in.Slug] = Assess(in)
	}
	return out
}

// refusalLines is how much of the last run's log is read to find a refusal: its last
// words, which is where a client says the provider refused it.
const refusalLines = 20

// mayBeRefused reports whether the container is in a state the last-run read judges:
// stopped, restarting, or running (the reader itself answers nothing for a copy up
// long enough to count as accepted).
func mayBeRefused(state string) bool {
	return state == containerRunning || state == containerRestarting || state == "exited" || state == "dead"
}

// isUp reports whether the container is in a state whose logs are worth reading.
func isUp(state string) bool {
	return state == containerRunning || state == containerRestarting
}

// readLogs returns the log tail and whether the read succeeded. A failure is reported
// as "not read" rather than as empty logs, so the verdict can say we could not look
// instead of implying we looked and found nothing.
func readLogs(ctx context.Context, read func(context.Context, string, int) (string, error), slug string, lines int) (string, bool) {
	if ctx == nil {
		// The app's context is nil until Startup has run, and a dashboard refresh
		// that raced it used to panic here rather than simply skip the log read.
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, logTimeout)
	defer cancel()
	out, err := read(ctx, slug, lines)
	if err != nil {
		return "", false
	}
	return out, true
}
