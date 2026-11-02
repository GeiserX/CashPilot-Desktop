package runtime

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/noderead"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
)

// maxExecOutput caps what one exec's output may hold in memory. A node's identity
// and settlement history are a few kilobytes; 1 MiB is far beyond any real answer.
const maxExecOutput = 1 << 20

// errExecOutputTooLong refuses an answer longer than maxExecOutput rather than
// cutting it: a cut answer could still match the regexes and read as a figure.
var errExecOutputTooLong = fmt.Errorf("the command printed more than %d bytes", maxExecOutput)

// cappedBuffer holds up to limit bytes and fails the write that would pass it.
type cappedBuffer struct {
	buf   bytes.Buffer
	limit int
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	if c.buf.Len()+len(p) > c.limit {
		return 0, errExecOutputTooLong
	}
	return c.buf.Write(p)
}

// execClient is the slice of the Docker client ExecRunning needs, so it can be tested
// without a daemon.
type execClient interface {
	ContainerList(ctx context.Context, options client.ContainerListOptions) (client.ContainerListResult, error)
	ExecCreate(ctx context.Context, containerID string, options client.ExecCreateOptions) (client.ExecCreateResult, error)
	ExecAttach(ctx context.Context, execID string, options client.ExecAttachOptions) (client.ExecAttachResult, error)
	ExecInspect(ctx context.Context, execID string, options client.ExecInspectOptions) (client.ExecInspectResult, error)
}

// ExecRunning runs cmd in every running container CashPilot manages for slug, each
// under its own timeout, and returns what each printed (stdout and stderr together,
// as the web worker's exec_run does). It is how the collector reads a node's own
// earnings (internal/noderead); the command always comes from the catalog.
func (p *DockerProvider) ExecRunning(ctx context.Context, slug string, cmd []string, timeout time.Duration) ([]noderead.Output, error) {
	cli, err := dockerClient()
	if err != nil {
		return nil, err
	}
	defer cli.Close()
	return execRunning(ctx, cli, slug, cmd, timeout)
}

func execRunning(ctx context.Context, cli execClient, slug string, cmd []string, timeout time.Duration) ([]noderead.Output, error) {
	list, err := cli.ContainerList(ctx, client.ContainerListOptions{
		Filters: make(client.Filters).Add("label", LabelManaged+"=true", LabelService+"="+slug),
	})
	if err != nil {
		return nil, err
	}
	var outputs []noderead.Output
	for _, c := range list.Items {
		// Filtered by the daemon already; checked again so a runtime that ignores a
		// filter cannot have the command run in another service's container.
		if c.Labels[LabelService] != slug || c.Labels[LabelManaged] != "true" || string(c.State) != "running" {
			continue
		}
		out := noderead.Output{Instance: instanceOf(c.Labels)}
		out.Text, out.ExitCode, out.Err = execOne(ctx, cli, c.ID, cmd, timeout)
		outputs = append(outputs, out)
	}
	sort.Slice(outputs, func(i, j int) bool { return outputs[i].Instance < outputs[j].Instance })
	return outputs, nil
}

func execOne(ctx context.Context, cli execClient, containerID string, cmd []string, timeout time.Duration) (string, int, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	created, err := cli.ExecCreate(ctx, containerID, client.ExecCreateOptions{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          cmd,
	})
	if err != nil {
		return "", 0, err
	}
	attached, err := cli.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		return "", 0, err
	}
	// The hijacked connection ignores ctx once open, so the deadline closes it. That
	// stops the read, not the process: the Docker API has no call that kills an exec.
	// The catalog's commands bound themselves (Mysterium's wget -T 5), so a stuck
	// read ends inside the container on its own.
	stop := context.AfterFunc(ctx, attached.Close)
	defer stop()
	defer attached.Close()
	out := &cappedBuffer{limit: maxExecOutput}
	if _, err := stdcopy.StdCopy(out, out, attached.Reader); err != nil {
		if ctx.Err() != nil {
			return "", 0, ctx.Err()
		}
		return "", 0, err
	}
	buf := &out.buf
	// The stream ends when the process does; the daemon may take a moment to record
	// the exit code.
	for {
		inspect, err := cli.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
		if err != nil {
			return "", 0, err
		}
		if !inspect.Running {
			return buf.String(), inspect.ExitCode, nil
		}
		select {
		case <-ctx.Done():
			return "", 0, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}
