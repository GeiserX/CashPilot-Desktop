package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// desktopRuntime is the cashpilot.runtime value CashPilot Desktop gives every
// container it creates: the earners, their copies and the proxied earners with
// their tunnels (containerLabels, proxied.go). The web app's Docker workers also
// set cashpilot.managed=true but never set cashpilot.runtime, so this label is
// what tells a Desktop container apart from one a CashPilot worker made on the
// same Docker.
const desktopRuntime = "existing-docker"

// IsDesktopContainer reports whether labels mark a container CashPilot Desktop
// created: cashpilot.managed=true and cashpilot.runtime=existing-docker.
func IsDesktopContainer(labels map[string]string) bool {
	return labels[LabelManaged] == "true" && labels[LabelRuntime] == desktopRuntime
}

// workerContainer reports a container another CashPilot made on this Docker: it
// carries cashpilot.managed=true but not Desktop's cashpilot.runtime. A CashPilot
// worker names its containers cashpilot-<slug> too, so a name alone never proves
// the container is Desktop's.
func workerContainer(c container.InspectResponse) bool {
	return c.Config != nil && c.Config.Labels[LabelManaged] == "true" && !IsDesktopContainer(c.Config.Labels)
}

// desktopOwned refuses a start or a stop of a container CashPilot Desktop did not
// create, so a server command or a click by slug never reaches a worker's
// container with the same name.
func desktopOwned(name string, c container.InspectResponse) error {
	if c.Config != nil && IsDesktopContainer(c.Config.Labels) {
		return nil
	}
	if workerContainer(c) {
		return fmt.Errorf("%s was made by a CashPilot worker on this Docker, not by CashPilot Desktop, so it was left as it is", name)
	}
	return fmt.Errorf("%s was not created by CashPilot Desktop, so it was left as it is", name)
}

// RemoveDesktopContainers stops and removes every container CashPilot Desktop
// created on this computer's Docker, and nothing else. It is what the Windows
// uninstaller runs when the user asks it to remove the earning services.
//
// Named volumes stay: an earner's node identity lives there, and the ordinary
// "remove this service" keeps it too. The LAN fence's network stays as well,
// because a CashPilot worker on the same Docker uses the same network.
//
// It returns the names it removed. It tries every container and returns the
// first error with the count that failed, so one stuck container does not leave
// the others running.
func RemoveDesktopContainers(ctx context.Context) ([]string, error) {
	cli, err := dockerClient()
	if err != nil {
		return nil, err
	}
	defer cli.Close()
	list, err := cli.ContainerList(ctx, client.ContainerListOptions{
		All:     true,
		Filters: make(client.Filters).Add("label", LabelManaged+"=true", LabelRuntime+"="+desktopRuntime),
	})
	if err != nil {
		return nil, err
	}
	var removed []string
	var errs []error
	for _, c := range list.Items {
		// The daemon filtered by label already. Checked again, so a runtime that
		// ignores a filter cannot hand over a container another tool made.
		if !IsDesktopContainer(c.Labels) {
			continue
		}
		name := c.ID
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		timeout := 10
		// A stop that fails is not the end: the forced remove below kills it.
		_, _ = cli.ContainerStop(ctx, c.ID, client.ContainerStopOptions{Timeout: &timeout})
		if _, err := cli.ContainerRemove(ctx, c.ID, client.ContainerRemoveOptions{Force: true}); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		removed = append(removed, name)
	}
	if len(errs) > 0 {
		return removed, fmt.Errorf("%d container(s) could not be removed: %w", len(errs), errors.Join(errs...))
	}
	return removed, nil
}
