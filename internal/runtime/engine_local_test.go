package runtime

import (
	"context"
	"io/fs"
	"testing"
	"time"
)

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
		{"tcp://203.0.113.10:2375", "", false},
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

func TestProbeDockerDesktopDefaultPipe(t *testing.T) {
	for _, c := range []struct{ osType, want string }{
		{"linux", ReasonRunning},
		{"windows", ReasonWindowsContainers},
	} {
		t.Run(c.osType, func(t *testing.T) {
			got := probeDockerDesktopPipes(t.Context(), func(_ context.Context, host string) (string, error) {
				if host == "npipe:////./pipe/docker_engine" {
					return c.osType, nil
				}
				return "", fs.ErrNotExist
			})
			if got != c.want {
				t.Errorf("only docker_engine answers: got %q, want %q", got, c.want)
			}
		})
	}
}

func TestProbeDockerDesktopDefaultPipeAfterTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	got := probeDockerDesktopPipes(ctx, func(ctx context.Context, host string) (string, error) {
		if host == "npipe:////./pipe/dockerDesktopLinuxEngine" {
			<-ctx.Done()
			return "", ctx.Err()
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if host == "npipe:////./pipe/docker_engine" {
			return "linux", nil
		}
		return "", fs.ErrNotExist
	})
	if got != ReasonRunning {
		t.Errorf("docker_engine answers after the Linux pipe timed out: got %q, want %q", got, ReasonRunning)
	}
}
