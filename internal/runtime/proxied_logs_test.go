package runtime

import (
	"context"
	"strings"
	"testing"
)

// ProxiedLogs reads the earner of the copy through that proxy, never the service's
// own container or the copy through another proxy.
func TestProxiedLogsReadOnlyThatProxysEarner(t *testing.T) {
	docker := startHarnessDocker(t)
	ctx := context.Background()
	p := NewDockerProvider()
	if _, err := p.Deploy(ctx, DeploySpec{Slug: "cmd-example", Service: commandService, Env: map[string]string{"TOKEN": "t"}}, nil); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	// A routed service has no container of its own: only its earner through proxy 3.
	docker.Rename(containerName("cmd-example"), proxiedName("cmd-example", 3))
	docker.SetLogs("routed earner says hello\n")

	logs, err := p.ProxiedLogs(ctx, "cmd-example", 3, 0)
	if err != nil || !strings.Contains(logs, "routed earner says hello") {
		t.Fatalf("ProxiedLogs(3) = %q, %v", logs, err)
	}
	if _, err := p.ProxiedLogs(ctx, "cmd-example", 4, 10); err == nil {
		t.Fatal("ProxiedLogs answered for proxy 4, which has no copy")
	}
	if _, err := p.Logs(ctx, "cmd-example", 10); err == nil {
		t.Fatal("Logs answered for a container that is not there")
	}
}
