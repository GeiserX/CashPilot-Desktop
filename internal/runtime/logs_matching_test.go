package runtime

import (
	"context"
	"regexp"
	"testing"
	"time"
)

// LogsMatching reads a window from the daemon and keeps only the matching lines; a
// container that is not there is an error, so nothing is judged from it.
func TestLogsMatchingFiltersTheDaemonsStream(t *testing.T) {
	docker := startHarnessDocker(t)
	ctx := context.Background()
	p := NewDockerProvider()
	if _, err := p.Deploy(ctx, DeploySpec{Slug: "cmd-example", Service: commandService, Env: map[string]string{"TOKEN": "t"}}, nil); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	docker.SetLogs("a Method:manual Success:false\nnoise\nb Method:manual Success:true\n")
	match := regexp.MustCompile("Method:manual")
	got, err := p.LogsMatching(ctx, "cmd-example", 6*time.Hour, match, 1000)
	if err != nil || got != "a Method:manual Success:false\nb Method:manual Success:true" {
		t.Fatalf("LogsMatching = %q, %v", got, err)
	}
	if _, err := p.LogsMatching(ctx, "not-deployed", time.Hour, match, 10); err == nil {
		t.Fatal("LogsMatching answered for a container that does not exist")
	}
}
