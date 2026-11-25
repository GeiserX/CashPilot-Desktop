package runtime

import "strings"

// Docker Desktop's own engine endpoints on Windows: the default pipe, and the two
// pipes its desktop-linux and desktop-windows contexts name.
var dockerDesktopPipes = map[string]bool{
	"docker_engine":              true,
	"dockerdesktoplinuxengine":   true,
	"dockerdesktopwindowsengine": true,
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
	return ok && dockerDesktopPipes[name]
}
