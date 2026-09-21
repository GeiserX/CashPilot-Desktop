package e2e

import (
	"strings"
	"testing"
)

// TestEarnerLifecycle walks one earner from the catalog to removal, the way a
// user does, and checks BOTH sides of every step: what the runtime was told and
// what the app wrote down. A step that only checks one side misses the failures
// that actually hurt — a deploy the daemon accepted but the app recorded under
// the wrong id, a stop the app believes in that never reached the container.
func TestEarnerLifecycle(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx()

	// The catalog is real, so the entry under test has to be in it and deployable.
	svc, ok := e.Catalog.Get("honeygain")
	if !ok {
		t.Fatal("honeygain is missing from the catalog")
	}
	if svc.ManualOnly || svc.Docker.Image == "" {
		t.Fatalf("honeygain is not deployable: manualOnly=%v image=%q", svc.ManualOnly, svc.Docker.Image)
	}

	t.Run("missing credentials stop the deploy before the runtime is touched", func(t *testing.T) {
		e.Docker.ResetCalls()
		if err := e.Manager.ValidateCredentials("honeygain", map[string]string{}); err == nil {
			t.Fatal("a deploy with no credentials was accepted")
		}
		_, err := e.Manager.Deploy(ctx, "honeygain", map[string]string{"HONEYGAIN_EMAIL": "someone@example.test"})
		if err == nil {
			t.Fatal("a deploy with no password was accepted")
		}
		if !strings.Contains(err.Error(), "Password") {
			t.Errorf("the error does not name the missing field: %v", err)
		}
		if calls := e.Docker.Calls(); len(calls) > 0 {
			t.Errorf("a deploy that was never valid still talked to the runtime: %v", calls)
		}
		if _, ok := e.deployment("honeygain"); ok {
			t.Error("a rejected deploy still wrote a deployment row")
		}
	})

	t.Run("deploy pulls, replaces and starts, in that order", func(t *testing.T) {
		e.Docker.ResetCalls()
		dep, err := e.Manager.Deploy(ctx, "honeygain", honeygainCreds)
		if err != nil {
			t.Fatalf("Deploy: %v", err)
		}

		calls := e.Docker.Calls()
		pull := callIndex(calls, "POST /images/create")
		replace := callIndex(calls, "DELETE /containers/cashpilot-honeygain")
		create := callIndex(calls, "POST /containers/create")
		if pull < 0 || replace < 0 || create < 0 {
			t.Fatalf("the deploy did not pull, replace and create; calls: %v", calls)
		}
		if pull > replace || replace > create {
			t.Fatalf("expected pull then replace then create, got %v", calls)
		}
		if !hasCallMatching(calls, "POST /containers/", "/start") {
			t.Fatalf("the container was never started; calls: %v", calls)
		}

		// The runtime holds a running container carrying the labels the app needs
		// to recognise it later, and the credentials the earner needs to log in.
		container, ok := e.Docker.Container("cashpilot-honeygain")
		if !ok {
			t.Fatal("the daemon has no cashpilot-honeygain container")
		}
		if container.State != "running" {
			t.Errorf("container state is %q, want running", container.State)
		}
		if container.Labels["cashpilot.managed"] != "true" || container.Labels["cashpilot.service"] != "honeygain" {
			t.Errorf("the container is not labelled as a managed honeygain earner: %v", container.Labels)
		}
		if container.Image != svc.Docker.Image {
			t.Errorf("container image %q, want the catalog's pinned %q", container.Image, svc.Docker.Image)
		}
		if !hasEnv(container.Env, "HONEYGAIN_EMAIL=someone@example.test") {
			t.Errorf("the credentials never reached the container: %v", container.Env)
		}
		// The catalog's command template is filled in from those same credentials.
		if !strings.Contains(strings.Join(container.Cmd, " "), "someone@example.test") {
			t.Errorf("the command was not expanded from the credentials: %v", container.Cmd)
		}

		// And the app wrote down what the runtime actually gave it.
		row, ok := e.deployment("honeygain")
		if !ok {
			t.Fatal("no deployment row was written")
		}
		if row.ContainerID != container.ID {
			t.Errorf("stored container id %q, runtime id %q", row.ContainerID, container.ID)
		}
		if row.Name != "cashpilot-honeygain" || row.Status != "running" {
			t.Errorf("stored row is %+v", row)
		}
		if row.Runtime == "" {
			t.Error("the row does not record which runtime served the deploy")
		}
		if dep.ContainerID != row.ContainerID {
			t.Errorf("Deploy returned %q but stored %q", dep.ContainerID, row.ContainerID)
		}
	})

	t.Run("a named volume is created with the container", func(t *testing.T) {
		if _, err := e.Manager.Deploy(ctx, "earnapp", earnappCreds); err != nil {
			t.Fatalf("Deploy earnapp: %v", err)
		}
		if !contains(e.Docker.Volumes(), "earnapp-data") {
			t.Errorf("the declared named volume was not created: %v", e.Docker.Volumes())
		}
	})

	t.Run("refresh reconciles the dashboard with the runtime", func(t *testing.T) {
		deployments, err := e.Manager.Refresh(ctx)
		if err != nil {
			t.Fatalf("Refresh: %v", err)
		}
		slugs := map[string]bool{}
		for _, dep := range deployments {
			slugs[dep.Slug] = true
		}
		if !slugs["honeygain"] || !slugs["earnapp"] {
			t.Fatalf("refresh lost a deployed earner: %v", slugs)
		}
	})

	t.Run("stop and start reach the container and the row follows", func(t *testing.T) {
		if err := e.Manager.Stop(ctx, "honeygain"); err != nil {
			t.Fatalf("Stop: %v", err)
		}
		if container, _ := e.Docker.Container("cashpilot-honeygain"); container.State != "exited" {
			t.Errorf("the container is %q after Stop, want exited", container.State)
		}
		if row, _ := e.deployment("honeygain"); row.Status != "stopped" {
			t.Errorf("the row says %q after Stop, want stopped", row.Status)
		}

		if err := e.Manager.Start(ctx, "honeygain"); err != nil {
			t.Fatalf("Start: %v", err)
		}
		if container, _ := e.Docker.Container("cashpilot-honeygain"); container.State != "running" {
			t.Errorf("the container is %q after Start, want running", container.State)
		}
		if row, _ := e.deployment("honeygain"); row.Status != "running" {
			t.Errorf("the row says %q after Start, want running", row.Status)
		}
	})

	t.Run("logs come back readable", func(t *testing.T) {
		e.Docker.SetLogs("honeygain: connected\nhoneygain: earning\n")
		logs, err := e.Manager.Logs(ctx, "honeygain", 50)
		if err != nil {
			t.Fatalf("Logs: %v", err)
		}
		if !strings.Contains(logs, "honeygain: earning") {
			t.Errorf("the log stream did not survive the round trip: %q", logs)
		}
	})

	t.Run("a redeploy replaces the container and keeps the volume", func(t *testing.T) {
		before, _ := e.Docker.Container("cashpilot-earnapp")
		e.Docker.ResetCalls()

		if _, err := e.Manager.Deploy(ctx, "earnapp", earnappCreds); err != nil {
			t.Fatalf("redeploy: %v", err)
		}
		after, ok := e.Docker.Container("cashpilot-earnapp")
		if !ok {
			t.Fatal("the redeploy left no container")
		}
		if after.ID == before.ID {
			t.Errorf("the redeploy reused container %s instead of replacing it", after.ID)
		}
		if after.State != "running" {
			t.Errorf("the replacement container is %q, want running", after.State)
		}
		// The earner's data volume must survive a redeploy: it holds the node
		// identity, and losing it means re-registering the device.
		if !contains(e.Docker.Volumes(), "earnapp-data") {
			t.Errorf("the redeploy destroyed the data volume: %v", e.Docker.Volumes())
		}
		if row, _ := e.deployment("earnapp"); row.ContainerID != after.ID {
			t.Errorf("the row still points at the old container: %q vs %q", row.ContainerID, after.ID)
		}
	})

	t.Run("remove takes the container, the volume and the row", func(t *testing.T) {
		if err := e.Manager.Remove(ctx, "earnapp"); err != nil {
			t.Fatalf("Remove: %v", err)
		}
		if _, ok := e.Docker.Container("cashpilot-earnapp"); ok {
			t.Error("the container is still there after Remove")
		}
		if contains(e.Docker.Volumes(), "earnapp-data") {
			t.Errorf("the data volume outlived an explicit Remove: %v", e.Docker.Volumes())
		}
		if _, ok := e.deployment("earnapp"); ok {
			t.Error("the deployment row outlived Remove")
		}
		// The other earner is untouched.
		if _, ok := e.Docker.Container("cashpilot-honeygain"); !ok {
			t.Error("removing earnapp also removed honeygain")
		}
	})
}

// TestARedeployThatCannotSucceedKeepsTheEarnerRunning is the end-to-end half of
// the rule the unit test pins: when the pull fails, the user must still have the
// container that was earning a second earlier, and the dashboard row that
// describes it.
func TestARedeployThatCannotSucceedKeepsTheEarnerRunning(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx()

	if _, err := e.Manager.Deploy(ctx, "honeygain", honeygainCreds); err != nil {
		t.Fatalf("first deploy: %v", err)
	}
	before, _ := e.Docker.Container("cashpilot-honeygain")
	row, _ := e.deployment("honeygain")

	e.Docker.SetPullError("pull refused by the fake registry")
	if _, err := e.Manager.Deploy(ctx, "honeygain", honeygainCreds); err == nil {
		t.Fatal("the redeploy succeeded; the pull was supposed to fail")
	}

	after, ok := e.Docker.Container("cashpilot-honeygain")
	if !ok {
		t.Fatal("the failed redeploy removed the running container")
	}
	if after.ID != before.ID || after.State != "running" {
		t.Errorf("the running container changed: %+v -> %+v", before, after)
	}
	if now, _ := e.deployment("honeygain"); now.ContainerID != row.ContainerID {
		t.Errorf("the row changed after a failed redeploy: %q -> %q", row.ContainerID, now.ContainerID)
	}

	// Positive control: with the registry working again the same call goes
	// through, so "nothing was removed" above cannot be a deploy that silently
	// stopped doing anything.
	if _, err := e.Manager.Deploy(ctx, "honeygain", honeygainCreds); err != nil {
		t.Fatalf("the deploy still fails once the registry works: %v", err)
	}
	if replaced, _ := e.Docker.Container("cashpilot-honeygain"); replaced.ID == before.ID {
		t.Error("the recovered deploy did not actually replace the container")
	}
}

// hasCallMatching reports whether any recorded call starts with prefix and ends
// with suffix — used for the calls that carry a generated container id.
func hasCallMatching(calls []string, prefix, suffix string) bool {
	for _, call := range calls {
		if strings.HasPrefix(call, prefix) && strings.HasSuffix(call, suffix) {
			return true
		}
	}
	return false
}

func hasEnv(env []string, want string) bool { return contains(env, want) }

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
