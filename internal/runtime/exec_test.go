package runtime

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// frame is one multiplexed Docker stream frame: 1 = stdout, 2 = stderr.
func frame(stream byte, text string) []byte {
	header := make([]byte, 8)
	header[0] = stream
	binary.BigEndian.PutUint32(header[4:], uint32(len(text)))
	return append(header, text...)
}

type fakeExec struct {
	items  []container.Summary
	output map[string][]byte // container id -> multiplexed output
	exit   map[string]int
	ran    []string // container ids an exec was created in
	cmds   [][]string
	execTo map[string]string // exec id -> container id
}

func (f *fakeExec) ContainerList(context.Context, client.ContainerListOptions) (client.ContainerListResult, error) {
	return client.ContainerListResult{Items: f.items}, nil
}

func (f *fakeExec) ExecCreate(_ context.Context, id string, o client.ExecCreateOptions) (client.ExecCreateResult, error) {
	f.ran = append(f.ran, id)
	f.cmds = append(f.cmds, o.Cmd)
	if f.execTo == nil {
		f.execTo = map[string]string{}
	}
	f.execTo["exec-"+id] = id
	return client.ExecCreateResult{ID: "exec-" + id}, nil
}

func (f *fakeExec) ExecAttach(_ context.Context, execID string, _ client.ExecAttachOptions) (client.ExecAttachResult, error) {
	conn, other := net.Pipe()
	other.Close()
	data := f.output[f.execTo[execID]]
	return client.ExecAttachResult{HijackedResponse: client.HijackedResponse{Conn: conn, Reader: bufio.NewReader(bytes.NewReader(data))}}, nil
}

func (f *fakeExec) ExecInspect(_ context.Context, execID string, _ client.ExecInspectOptions) (client.ExecInspectResult, error) {
	return client.ExecInspectResult{ExitCode: f.exit[f.execTo[execID]]}, nil
}

func summary(id, slug, state string, labels map[string]string) container.Summary {
	l := map[string]string{LabelManaged: "true", LabelService: slug}
	for k, v := range labels {
		l[k] = v
	}
	return container.Summary{ID: id, Labels: l, State: container.ContainerState(state)}
}

// TestExecRunningRunsOnlyInTheServicesRunningContainers. The command is read-only by
// construction, but it still must never run in a stopped copy, another service's
// container or one CashPilot does not manage, whatever the daemon's filter did.
func TestExecRunningRunsOnlyInTheServicesRunningContainers(t *testing.T) {
	f := &fakeExec{
		items: []container.Summary{
			summary("c2", "mysterium", "running", map[string]string{LabelInstance: "2"}),
			summary("c1", "mysterium", "running", nil),
			summary("stopped", "mysterium", "exited", nil),
			summary("other", "honeygain", "running", nil),
			{ID: "unmanaged", Labels: map[string]string{LabelService: "mysterium"}, State: "running"},
		},
		output: map[string][]byte{
			"c1": append(frame(1, `{"id":"0xa"}`), frame(2, " warn")...),
			"c2": frame(1, "partial"),
		},
		exit: map[string]int{"c2": 1},
	}
	cmd := []string{"sh", "-c", "echo"}
	got, err := execRunning(context.Background(), f, "mysterium", cmd, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.ran, []string{"c2", "c1"}) {
		t.Fatalf("exec ran in %v, want only the two running mysterium containers", f.ran)
	}
	if !slices.Equal(f.cmds[0], cmd) {
		t.Errorf("ran %v, want the given command", f.cmds[0])
	}
	if len(got) != 2 || got[0].Instance != 1 || got[1].Instance != 2 {
		t.Fatalf("got %+v, want copies 1 and 2 in order", got)
	}
	if got[0].Text != `{"id":"0xa"} warn` || got[0].ExitCode != 0 || got[0].Err != nil {
		t.Errorf("copy 1 = %+v, want stdout and stderr together, exit 0", got[0])
	}
	if got[1].ExitCode != 1 {
		t.Errorf("copy 2 exit = %d, want 1", got[1].ExitCode)
	}
}

// An answer past the cap is an error, never a cut answer that may still match.
func TestExecRunningRefusesAnOverlongAnswer(t *testing.T) {
	f := &fakeExec{
		items:  []container.Summary{summary("c1", "mysterium", "running", nil)},
		output: map[string][]byte{"c1": append(frame(1, strings.Repeat("x", maxExecOutput-10)), frame(1, strings.Repeat("y", 20))...)},
	}
	got, err := execRunning(context.Background(), f, "mysterium", []string{"true"}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Err == nil || got[0].Text != "" {
		t.Fatalf("got %+v, want an error and no text for an answer over %d bytes", got, maxExecOutput)
	}
	f.output["c1"] = frame(1, strings.Repeat("x", maxExecOutput))
	if got, _ := execRunning(context.Background(), f, "mysterium", []string{"true"}, time.Second); got[0].Err != nil || len(got[0].Text) != maxExecOutput {
		t.Errorf("control: an answer exactly at the cap must be read whole, got err %v len %d", got[0].Err, len(got[0].Text))
	}
}
