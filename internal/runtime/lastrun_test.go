package runtime

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"strconv"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/health"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// logFrame is one stdout frame of Docker's multiplexed log stream.
func logFrame(text string) []byte {
	header := []byte{1, 0, 0, 0, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(header[4:], uint32(len(text)))
	return append(header, text...)
}

type logRecord struct {
	at   time.Time
	text string
}

// fakeRunDocker answers inspect with one State and logs from timestamped records,
// honouring since, until and tail the way the daemon does.
type fakeRunDocker struct {
	state   container.State
	records []logRecord
	asked   []client.ContainerLogsOptions
}

func (f *fakeRunDocker) ContainerInspect(context.Context, string, client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	st := f.state
	return client.ContainerInspectResult{Container: container.InspectResponse{State: &st}}, nil
}

func (f *fakeRunDocker) ContainerLogs(_ context.Context, _ string, o client.ContainerLogsOptions) (client.ContainerLogsResult, error) {
	f.asked = append(f.asked, o)
	var kept []string
	for _, r := range f.records {
		if o.Since != "" {
			since, _ := time.Parse(time.RFC3339Nano, o.Since)
			if r.at.Before(since) {
				continue
			}
		}
		if o.Until != "" {
			until, _ := time.Parse(time.RFC3339Nano, o.Until)
			if !r.at.Before(until) {
				continue
			}
		}
		kept = append(kept, r.text)
	}
	if n, err := strconv.Atoi(o.Tail); err == nil && len(kept) > n {
		kept = kept[len(kept)-n:]
	}
	var buf bytes.Buffer
	for _, k := range kept {
		buf.Write(logFrame(k))
	}
	return io.NopCloser(&buf), nil
}

var (
	t0           = time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	refused      = logRecord{t0, "user is disabled"}
	userDisabled = catalog.HealthSignal{Pattern: "user is disabled", Means: "disabled", State: catalog.SignalAccountDisabled}
)

func at(d time.Duration) string { return t0.Add(d).Format(time.RFC3339Nano) }

func judged(t *testing.T, f *fakeRunDocker, now time.Time) string {
	t.Helper()
	out, err := lastRunLogs(context.Background(), f, "cashpilot-iproyal", 20, now)
	if err != nil {
		t.Fatal(err)
	}
	return health.MatchRefusal(out, []catalog.HealthSignal{userDisabled})
}

// The regression: the account came back, a later run connected and shared, and the
// user stopped it. Its tail still holds the refusal; its last run does not.
func TestLastRunLeavesOutAnEarlierRunsRefusal(t *testing.T) {
	f := &fakeRunDocker{
		state: container.State{Status: "exited", StartedAt: at(5 * time.Second), FinishedAt: at(time.Hour)},
		records: []logRecord{refused,
			{t0.Add(5 * time.Second), "connected"},
			{t0.Add(6 * time.Second), "running, sharing bandwidth"}},
	}
	if got := judged(t, f, t0.Add(2*time.Hour)); got != "" {
		t.Fatalf("an earlier run's refusal was judged as the last run's (asked %+v)", f.asked)
	}
	// A container that never started has no run to bound, so nothing is read.
	f.state.StartedAt = "0001-01-01T00:00:00Z"
	if out, _ := lastRunLogs(context.Background(), f, "x", 20, t0); out != "" {
		t.Errorf("a container that never started has no last run to judge, got %q", out)
	}
}

func TestLastRunCatchesTheRefusalOfTheRunThatEnded(t *testing.T) {
	for _, status := range []container.ContainerState{"exited", "restarting", "dead"} {
		f := &fakeRunDocker{
			state:   container.State{Status: status, StartedAt: at(time.Minute), FinishedAt: at(time.Minute + time.Second)},
			records: []logRecord{{t0, "connected"}, {t0.Add(time.Minute), "user is disabled"}},
		}
		if got := judged(t, f, t0.Add(time.Hour)); got == "" {
			t.Errorf("%s: the last run's refusal was missed (asked %+v)", status, f.asked)
		}
	}
}

// A copy running for under two minutes has not proven the provider accepts it: the
// run before it is judged. One up longer is not judged at all.
func TestRunningCopyIsJudgedByTheRunBeforeOnlyWhileYoung(t *testing.T) {
	f := &fakeRunDocker{
		state:   container.State{Status: "running", StartedAt: at(time.Minute), FinishedAt: at(time.Minute - time.Second)},
		records: []logRecord{refused, {t0.Add(time.Minute), "starting"}},
	}
	if got := judged(t, f, t0.Add(time.Minute+time.Second)); got == "" {
		t.Errorf("a young running copy must be judged by the run before (asked %+v)", f.asked)
	}
	f.asked = nil
	if got := judged(t, f, t0.Add(time.Minute+refusedRunProvenAfter)); got != "" || len(f.asked) != 0 {
		t.Errorf("a copy up past %s must not be judged or read, got %q, asked %+v", refusedRunProvenAfter, got, f.asked)
	}
}
