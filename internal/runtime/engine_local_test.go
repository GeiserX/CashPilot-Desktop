package runtime

import "testing"

// Only Docker Desktop's own endpoints and contexts count as this PC's engine.
func TestLocalDockerDesktop(t *testing.T) {
	for _, c := range []struct {
		host, context string
		want          bool
	}{
		{"", "", true},
		{"", "default", true},
		{"", "desktop-linux", true},
		{"", "Desktop-Windows", true},
		{"npipe:////./pipe/docker_engine", "default", true},
		{"npipe:////./pipe/dockerDesktopLinuxEngine", "desktop-linux", true},
		{"tcp://192.168.10.100:2375", "", false},
		{"ssh://me@nas", "default", false},
		{"npipe:////nas/pipe/docker_engine", "", false},
		{"npipe:////./pipe/podman-machine-default", "", false},
		{"unix:///var/run/docker.sock", "", false},
		{"", "nas", false},
		{"", "desktop-linux-remote", false},
	} {
		if got := LocalDockerDesktop(c.host, c.context); got != c.want {
			t.Errorf("DOCKER_HOST %q, context %q: %v, want %v", c.host, c.context, got, c.want)
		}
	}
}
