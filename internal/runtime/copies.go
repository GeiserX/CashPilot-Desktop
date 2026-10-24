package runtime

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/moby/moby/client"
)

// LabelInstance says which copy of a service a container is, when the service runs
// more than once on this machine. The first copy has no such label and keeps the name
// it always had (cashpilot-<slug>); copy N is cashpilot-<slug>-N and carries "N". The
// web worker uses the same label, so either side can read the other's containers.
const LabelInstance = "cashpilot.instance"

// CopyProvider is a runtime that can run copies 2..N of a service next to the first.
// Only Docker has them: a copy is another container from the same image, and the
// native runtime runs one process per service. The first copy is still driven
// through Provider, exactly as before, so a single-copy service never touches this.
type CopyProvider interface {
	// Copies lists the copy numbers above 1 that exist for slug, in order.
	Copies(ctx context.Context, slug string) ([]int, error)
	StopCopy(ctx context.Context, slug string, n, stopTimeoutSeconds int) error
	StartCopy(ctx context.Context, slug string, n int) error
	LogsCopy(ctx context.Context, slug string, n, lines int) (string, error)
	// RemoveCopy removes copy n and nothing else. Copies only exist for services
	// with no volumes, so there is no data to keep or delete.
	RemoveCopy(ctx context.Context, slug string, n, stopTimeoutSeconds int) error
}

// copyName is the container name of copy n of slug: the first copy's name for n <= 1.
func copyName(slug string, n int) string {
	if n <= 1 {
		return containerName(slug)
	}
	return fmt.Sprintf("%s-%d", containerName(slug), n)
}

// instanceOf reads which copy a container is from its labels; 1 when unlabelled.
func instanceOf(labels map[string]string) int {
	n, err := strconv.Atoi(labels[LabelInstance])
	if err != nil || n < 1 {
		return 1
	}
	return n
}

// copyEnv gives copy n its own device names. The env values the catalog derives from
// {hostname} are the names a provider counts devices by, so two copies under one name
// would be one device; copy n gets "-n" on each of them (cashpilot-box-2). Nothing
// else differs between copies.
func copyEnv(svc catalog.Service, env map[string]string, n int) map[string]string {
	if n <= 1 {
		return env
	}
	for _, item := range svc.Docker.Env {
		if strings.Contains(item.Default, "{hostname}") && env[item.Key] != "" {
			env[item.Key] = fmt.Sprintf("%s-%d", env[item.Key], n)
		}
	}
	return env
}

// managedCopy returns the name of copy n of slug after checking the container really
// is that copy: managed by CashPilot, of this service, with this copy number. A
// container that only happens to have the name is refused like a missing one.
func managedCopy(ctx context.Context, cli inspectClient, slug string, n int) (string, error) {
	if n <= 1 {
		return "", fmt.Errorf("copy 1 of %s is the service itself", slug)
	}
	name := copyName(slug, n)
	inspect, err := cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if err != nil {
		return "", err
	}
	cfg := inspect.Container.Config
	if cfg == nil || cfg.Labels[LabelManaged] != "true" || cfg.Labels[LabelService] != slug || instanceOf(cfg.Labels) != n {
		return "", fmt.Errorf("%s is not copy %d of %s managed by CashPilot", name, n, slug)
	}
	return name, nil
}

func (p *DockerProvider) Copies(ctx context.Context, slug string) ([]int, error) {
	cli, err := dockerClient()
	if err != nil {
		return nil, err
	}
	defer cli.Close()
	list, err := cli.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: make(client.Filters).Add("label", LabelManaged+"=true", LabelService+"="+slug),
	})
	if err != nil {
		return nil, err
	}
	var copies []int
	for _, c := range list.Items {
		if n := instanceOf(c.Labels); n > 1 {
			copies = append(copies, n)
		}
	}
	sort.Ints(copies)
	return copies, nil
}

func (p *DockerProvider) StopCopy(ctx context.Context, slug string, n, stopTimeoutSeconds int) error {
	cli, err := dockerClient()
	if err != nil {
		return err
	}
	defer cli.Close()
	name, err := managedCopy(ctx, cli, slug, n)
	if err != nil {
		return err
	}
	timeout := stopTimeoutForContainer(ctx, cli, name, stopTimeoutSeconds)
	_, err = cli.ContainerStop(ctx, name, client.ContainerStopOptions{Timeout: &timeout})
	return err
}

func (p *DockerProvider) StartCopy(ctx context.Context, slug string, n int) error {
	cli, err := dockerClient()
	if err != nil {
		return err
	}
	defer cli.Close()
	name, err := managedCopy(ctx, cli, slug, n)
	if err != nil {
		return err
	}
	_, err = cli.ContainerStart(ctx, name, client.ContainerStartOptions{})
	return err
}

func (p *DockerProvider) LogsCopy(ctx context.Context, slug string, n, lines int) (string, error) {
	cli, err := dockerClient()
	if err != nil {
		return "", err
	}
	defer cli.Close()
	name, err := managedCopy(ctx, cli, slug, n)
	if err != nil {
		return "", err
	}
	if lines <= 0 {
		lines = 200
	}
	reader, err := cli.ContainerLogs(ctx, name, client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Tail:       fmt.Sprintf("%d", lines),
		Timestamps: true,
	})
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

func (p *DockerProvider) RemoveCopy(ctx context.Context, slug string, n, stopTimeoutSeconds int) error {
	cli, err := dockerClient()
	if err != nil {
		return err
	}
	defer cli.Close()
	name, err := managedCopy(ctx, cli, slug, n)
	if err != nil {
		return err
	}
	// Stopped first with its grace period, as every other removal here is.
	timeout := stopTimeoutForContainer(ctx, cli, name, stopTimeoutSeconds)
	_, _ = cli.ContainerStop(ctx, name, client.ContainerStopOptions{Timeout: &timeout})
	_, err = cli.ContainerRemove(ctx, name, client.ContainerRemoveOptions{Force: true})
	return err
}
