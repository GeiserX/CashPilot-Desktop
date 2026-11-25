package runtime

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"time"
)

const dockerDesktopWindowsPipe = "dockerDesktopWindowsEngine"

// Prefer the pipes named by Docker Desktop's own contexts, then try the default
// pipe, which Docker Desktop can use in either container mode.
var dockerDesktopPipes = [...]string{
	"dockerDesktopLinuxEngine",
	dockerDesktopWindowsPipe,
	"docker_engine",
}

// Docker Desktop's own context names. The Docker CLI calls the default endpoint
// "default", and Docker Desktop adds desktop-linux and desktop-windows.
var dockerDesktopContexts = map[string]bool{
	"":                true, // no docker CLI on PATH: the app dials the default pipe
	"default":         true,
	"desktop-linux":   true,
	"desktop-windows": true,
}

// LocalDockerDesktop says whether the engine the app reached is this PC's own
// Docker Desktop. dockerHost is DOCKER_HOST, and dockerContext the Docker CLI's
// current context (Status.Context). Anything else, a tcp or ssh host, a pipe on
// another machine or a context the user made, may be an engine elsewhere, so it
// proves nothing about this PC.
func LocalDockerDesktop(dockerHost, dockerContext string) bool {
	if !dockerDesktopContexts[strings.ToLower(strings.TrimSpace(dockerContext))] {
		return false
	}
	host := strings.TrimSpace(dockerHost)
	if host == "" {
		return true
	}
	lower := strings.ToLower(host)
	if !strings.HasPrefix(lower, "npipe://") {
		return false
	}
	// npipe:////./pipe/<name>: "." is this machine.
	name, ok := strings.CutPrefix(strings.TrimLeft(strings.TrimPrefix(lower, "npipe:"), "/"), "./pipe/")
	if !ok {
		return false
	}
	for _, pipe := range dockerDesktopPipes {
		if strings.EqualFold(name, pipe) {
			return true
		}
	}
	return false
}

func probeDockerDesktopPipes(ctx context.Context, ping func(context.Context, string) (string, error)) string {
	denied := false
	for _, pipe := range dockerDesktopPipes {
		pipeCtx, cancel := context.WithTimeout(ctx, time.Second)
		osType, err := ping(pipeCtx, "npipe:////./pipe/"+pipe)
		cancel()
		if err == nil {
			if pipe == dockerDesktopWindowsPipe || strings.EqualFold(osType, "windows") {
				return ReasonWindowsContainers
			}
			return ReasonRunning
		}
		denied = errors.Is(err, fs.ErrPermission) || denied
	}
	if denied {
		return ReasonAccessDenied
	}
	return ""
}
