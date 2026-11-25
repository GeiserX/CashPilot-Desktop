package runtime

import (
	"context"
	"time"

	"github.com/moby/moby/client"
)

// DockerDesktopEngineReason probes Docker Desktop's own pipes with no Docker
// environment overrides. The configured engine can still serve deployments.
func DockerDesktopEngineReason(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	denied := false
	for _, pipe := range []string{"dockerDesktopLinuxEngine", "dockerDesktopWindowsEngine"} {
		host := "npipe:////./pipe/" + pipe
		cli, err := client.NewClientWithOpts(client.WithHost(host))
		if err != nil {
			continue
		}
		_, err = cli.Ping(ctx, client.PingOptions{})
		_ = cli.Close()
		if err == nil {
			if pipe == "dockerDesktopWindowsEngine" {
				return ReasonWindowsContainers
			}
			return ReasonRunning
		}
		denied = probeEndpoint(ctx, host) == endpointDenied || denied
	}
	if denied {
		return ReasonAccessDenied
	}
	return ""
}
