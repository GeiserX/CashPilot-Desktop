package runtime

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/moby/moby/client"
)

// refusedRunProvenAfter is how long a running copy must be up before it counts as
// accepted by the provider. A refused pawns-cli lives 0.4 to 1.8 s, so a crash loop
// is "running" for about a second per restart, and that second is not a recovery.
// The same rule as the web worker's _FATAL_PROVEN_AFTER_S.
const refusedRunProvenAfter = 2 * time.Minute

type runLogsClient interface {
	inspectClient
	ContainerLogs(ctx context.Context, containerID string, options client.ContainerLogsOptions) (client.ContainerLogsResult, error)
}

// LastRunLogs returns the log of the service's last finished run only, for judging
// whether the provider refused it (internal/health). It ports the web worker's
// _fatal_entry:
//
//   - restarting, exited or dead: the log since the last start, so an earlier run's
//     refusal is not judged as this one's;
//   - running for under refusedRunProvenAfter: the log before this start, that is
//     the run before;
//   - running longer, or never finished a run: "" (nothing to judge).
func (p *DockerProvider) LastRunLogs(ctx context.Context, slug string, lines int) (string, error) {
	cli, err := dockerClient()
	if err != nil {
		return "", err
	}
	defer cli.Close()
	return lastRunLogs(ctx, cli, containerName(slug), lines, time.Now())
}

func lastRunLogs(ctx context.Context, cli runLogsClient, name string, lines int, now time.Time) (string, error) {
	inspect, err := cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if err != nil {
		return "", err
	}
	state := inspect.Container.State
	if state == nil {
		return "", nil
	}
	started, okStarted := dockerTime(state.StartedAt)
	if _, finished := dockerTime(state.FinishedAt); !finished || !okStarted {
		return "", nil
	}
	opts := client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Tail: fmt.Sprintf("%d", lines)}
	switch string(state.Status) {
	case "restarting", "exited", "dead":
		opts.Since = state.StartedAt
	case "running":
		if now.Sub(started) >= refusedRunProvenAfter {
			return "", nil
		}
		opts.Until = state.StartedAt
	default:
		return "", nil
	}
	reader, err := cli.ContainerLogs(ctx, name, opts)
	if err != nil {
		return "", err
	}
	defer reader.Close()
	raw, err := io.ReadAll(io.LimitReader(reader, maxLogBytes))
	if err != nil {
		return "", err
	}
	return stripDockerLogHeaders(raw), nil
}

// dockerTime parses Docker's StartedAt/FinishedAt; false for its zero time
// ("0001-01-01T00:00:00Z"), which means it never happened.
func dockerTime(value string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || t.Year() <= 1 {
		return time.Time{}, false
	}
	return t, true
}
