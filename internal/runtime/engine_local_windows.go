package runtime

import (
	"context"
	"io/fs"

	"github.com/moby/moby/client"
)

// DockerDesktopEngineReason probes Docker Desktop's own pipes with no Docker
// environment overrides. The configured engine can still serve deployments.
func DockerDesktopEngineReason(ctx context.Context) string {
	return probeDockerDesktopPipes(ctx, pingDockerDesktopPipe)
}

func pingDockerDesktopPipe(ctx context.Context, host string) (string, error) {
	cli, err := client.NewClientWithOpts(client.WithHost(host))
	if err != nil {
		return "", err
	}
	defer cli.Close()
	ping, err := cli.Ping(ctx, client.PingOptions{})
	if err != nil && probeEndpoint(ctx, host) == endpointDenied {
		return "", fs.ErrPermission
	}
	return ping.OSType, err
}
