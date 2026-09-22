package e2e

import (
	"slices"
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

	t.Run("a named volume is created and mounted where the earner writes", func(t *testing.T) {
		earnapp, ok := e.Catalog.Get("earnapp")
		if !ok || len(earnapp.Docker.Volumes) == 0 {
			t.Fatal("earnapp declares no volume, so this test would assert nothing")
		}
		// The catalog says "<volume name>:<path inside the container>". Both halves
		// have to reach the runtime: the name is the volume that has to survive a
		// redeploy, and the path is where the earner keeps the node identity. A
		// deploy that creates the volume but mounts it somewhere else looks fine
		// and loses the identity on every restart.
		wantName, wantPath, found := strings.Cut(earnapp.Docker.Volumes[0], ":")
		if !found {
			t.Fatalf("the catalog entry %q has no container path", earnapp.Docker.Volumes[0])
		}

		if _, err := e.Manager.Deploy(ctx, "earnapp", earnappCreds); err != nil {
			t.Fatalf("Deploy earnapp: %v", err)
		}
		if !contains(e.Docker.Volumes(), wantName) {
			t.Errorf("the declared named volume was not created: %v", e.Docker.Volumes())
		}
		container, ok := e.Docker.Container("cashpilot-earnapp")
		if !ok {
			t.Fatal("the daemon has no cashpilot-earnapp container")
		}
		if len(container.Mounts) != 1 || container.Mounts[0].Source != wantName || container.Mounts[0].Target != wantPath {
			t.Errorf("the container mounts %+v, want %s at %s", container.Mounts, wantName, wantPath)
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
		// identity, and losing it means re-registering the device. Checking the
		// volume is still there is not enough on its own — a daemon only takes
		// unnamed volumes when a container is deleted "with volumes", so a
		// redeploy could start asking for that and this one would still look
		// fine. So the request itself is checked: replacing the container must
		// not ask for its volumes to go with it.
		if !contains(e.Docker.Volumes(), "earnapp-data") {
			t.Errorf("the redeploy destroyed the data volume: %v", e.Docker.Volumes())
		}
		deleted := ""
		for _, call := range e.Docker.CallsWithQuery() {
			if strings.HasPrefix(call, "DELETE /containers/cashpilot-earnapp") {
				deleted = call
			}
		}
		if deleted == "" {
			t.Fatalf("the redeploy never deleted the old container: %v", e.Docker.CallsWithQuery())
		}
		if strings.Contains(deleted, "v=1") {
			t.Errorf("the redeploy asked the daemon to delete the container's volumes too: %q", deleted)
		}
		if row, _ := e.deployment("earnapp"); row.ContainerID != after.ID {
			t.Errorf("the row still points at the old container: %q vs %q", row.ContainerID, after.ID)
		}
	})

	t.Run("remove takes the container and the row, and leaves the other earner alone", func(t *testing.T) {
		e.Docker.ResetCalls()
		if err := e.Manager.Remove(ctx, "earnapp", false, false); err != nil {
			t.Fatalf("Remove: %v", err)
		}
		if _, ok := e.Docker.Container("cashpilot-earnapp"); ok {
			t.Error("the container is still there after Remove")
		}
		if _, ok := e.deployment("earnapp"); ok {
			t.Error("the deployment row outlived Remove")
		}
		// The other earner is untouched: removing one service must not take the
		// rest of the dashboard with it.
		if _, ok := e.Docker.Container("cashpilot-honeygain"); !ok {
			t.Error("removing earnapp also removed honeygain")
		}
		if _, ok := e.deployment("honeygain"); !ok {
			t.Error("removing earnapp also deleted honeygain's row")
		}
		// The data stays. A plain "remove this service" is the ordinary act, and
		// the volume it leaves behind is what a later redeploy picks back up, so
		// the daemon must not be asked to delete it. Both halves are checked: that
		// the volume survived, and that no delete was attempted — a volume also
		// survives a delete that FAILED, which looks identical from here and
		// behaves nothing like this against a real daemon.
		if deletedVolumes := volumeDeletes(e.Docker.CallsWithQuery()); len(deletedVolumes) != 0 {
			t.Errorf("the default Remove deleted volumes %v; calls: %v", deletedVolumes, e.Docker.CallsWithQuery())
		}
		if !contains(e.Docker.Volumes(), "earnapp-data") {
			t.Errorf("the default Remove destroyed the data volume: %v", e.Docker.Volumes())
		}
		// And the container delete must not carry the daemon's own "take the
		// volumes with it" flag, which destroys anonymous volumes with no volume
		// delete of its own to show for it.
		deleted := ""
		for _, call := range e.Docker.CallsWithQuery() {
			if strings.HasPrefix(call, "DELETE /containers/cashpilot-earnapp") {
				deleted = call
			}
		}
		if deleted == "" {
			t.Fatalf("Remove never deleted the container: %v", e.Docker.CallsWithQuery())
		}
		if strings.Contains(deleted, "v=1") {
			t.Errorf("the default Remove asked the daemon to take the volumes too: %q", deleted)
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

// TestDeletingTheDataIsASeparateChoiceFromRemovingTheService is the other half of
// the rule the default remove pins: when the user DOES ask for the data, the
// volume really goes — and only the volumes of the service that was asked about.
// A guard that kept data by refusing to delete anything at all would pass the
// default-remove checks and lose the user nothing but the feature.
func TestDeletingTheDataIsASeparateChoiceFromRemovingTheService(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx()

	// Two earners that both keep a named volume, so "exactly its volumes" is a
	// claim with something to be wrong about.
	if _, err := e.Manager.Deploy(ctx, "earnapp", earnappCreds); err != nil {
		t.Fatalf("deploy earnapp: %v", err)
	}
	if _, err := e.Manager.Deploy(ctx, "mysterium", nil); err != nil {
		t.Fatalf("deploy mysterium: %v", err)
	}
	if !contains(e.Docker.Volumes(), "earnapp-data") || !contains(e.Docker.Volumes(), "mysterium-data") {
		t.Fatalf("the deploys created no data volumes to delete: %v", e.Docker.Volumes())
	}

	e.Docker.ResetCalls()
	if err := e.Manager.Remove(ctx, "earnapp", true, false); err != nil {
		t.Fatalf("Remove(deleteData): %v", err)
	}

	if got, want := volumeDeletes(e.Docker.CallsWithQuery()), []string{"earnapp-data"}; !slices.Equal(got, want) {
		t.Errorf("volume deletes = %v, want %v; calls: %v", got, want, e.Docker.CallsWithQuery())
	}
	if contains(e.Docker.Volumes(), "earnapp-data") {
		t.Errorf("the volume survived the delete the user asked for: %v", e.Docker.Volumes())
	}
	if !contains(e.Docker.Volumes(), "mysterium-data") {
		t.Errorf("deleting earnapp's data took the other earner's node identity: %v", e.Docker.Volumes())
	}
	if _, ok := e.Docker.Container("cashpilot-earnapp"); ok {
		t.Error("the container is still there after Remove")
	}
	if _, ok := e.deployment("earnapp"); ok {
		t.Error("the deployment row outlived Remove")
	}
	if _, ok := e.Docker.Container("cashpilot-mysterium"); !ok {
		t.Error("removing earnapp also removed the other earner")
	}
}

// TestRemoveRefusesToDestroyANodeIdentityWithoutASecondYes is the guard that
// exists because the loss is permanent: a Mysterium keystore has no server-side
// copy, so "delete the data too" on its own must not be enough to destroy it. The
// refusal has to happen BEFORE anything is removed — a refusal that arrives after
// the container is gone has already taken the thing the data belonged to.
func TestRemoveRefusesToDestroyANodeIdentityWithoutASecondYes(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx()

	if _, err := e.Manager.Deploy(ctx, "mysterium", nil); err != nil {
		t.Fatalf("deploy mysterium: %v", err)
	}
	plan, err := e.Manager.PlanRemoval(ctx, "mysterium")
	if err != nil {
		t.Fatalf("PlanRemoval: %v", err)
	}
	if len(plan.Volumes) != 1 || !plan.Volumes[0].Critical {
		t.Fatalf("the catalog's critical volume is not in the plan: %+v", plan.Volumes)
	}

	e.Docker.ResetCalls()
	err = e.Manager.Remove(ctx, "mysterium", true, false)
	if err == nil {
		t.Fatal("deleting a node identity went through on one yes")
	}
	// The message has to be enough to decide with on its own: the volume and what
	// is lost, not a bare refusal that sends the user looking elsewhere.
	if !strings.Contains(err.Error(), "mysterium-data") || !strings.Contains(err.Error(), plan.Volumes[0].Holds) {
		t.Errorf("the refusal does not say what was protected: %v", err)
	}
	if deleted := volumeDeletes(e.Docker.CallsWithQuery()); len(deleted) != 0 {
		t.Errorf("the refused Remove still deleted volumes %v", deleted)
	}
	if !contains(e.Docker.Volumes(), "mysterium-data") {
		t.Errorf("the refused Remove destroyed the node identity: %v", e.Docker.Volumes())
	}
	// Nothing at all happened: the container is still there, still running, and the
	// row still describes it, so the user can retry or change their mind.
	container, ok := e.Docker.Container("cashpilot-mysterium")
	if !ok {
		t.Fatal("the refused Remove took the container anyway")
	}
	if container.State != "running" {
		t.Errorf("the refused Remove left the container %q, want running", container.State)
	}
	if _, ok := e.deployment("mysterium"); !ok {
		t.Error("the refused Remove deleted the deployment row")
	}

	// Positive control: the same call with the second, explicit yes goes through,
	// so the refusal above cannot be a Remove that stopped working.
	e.Docker.ResetCalls()
	if err := e.Manager.Remove(ctx, "mysterium", true, true); err != nil {
		t.Fatalf("Remove(deleteData, allowCritical): %v", err)
	}
	if got, want := volumeDeletes(e.Docker.CallsWithQuery()), []string{"mysterium-data"}; !slices.Equal(got, want) {
		t.Errorf("volume deletes = %v, want %v", got, want)
	}
	if contains(e.Docker.Volumes(), "mysterium-data") {
		t.Errorf("the explicit delete left the volume behind: %v", e.Docker.Volumes())
	}
	if _, ok := e.Docker.Container("cashpilot-mysterium"); ok {
		t.Error("the container survived the explicit remove")
	}
	if _, ok := e.deployment("mysterium"); ok {
		t.Error("the deployment row survived the explicit remove")
	}
}

// TestPlanRemovalNamesTheDataAndMarksWhatCannotBeGotBack covers the question the
// user is actually asked. The confirmation is built from this plan, so if it did
// not name the volume or did not mark the irreplaceable one, the user would be
// agreeing to "and its Docker volumes" — the wording that lost people their nodes.
func TestPlanRemovalNamesTheDataAndMarksWhatCannotBeGotBack(t *testing.T) {
	e := newEnv(t)
	ctx := e.ctx()

	if _, err := e.Manager.Deploy(ctx, "earnapp", earnappCreds); err != nil {
		t.Fatalf("deploy earnapp: %v", err)
	}
	if _, err := e.Manager.Deploy(ctx, "mysterium", nil); err != nil {
		t.Fatalf("deploy mysterium: %v", err)
	}

	ordinary, err := e.Manager.PlanRemoval(ctx, "earnapp")
	if err != nil {
		t.Fatalf("PlanRemoval(earnapp): %v", err)
	}
	if ordinary.Name != "cashpilot-earnapp" || !ordinary.CatalogKnown {
		t.Errorf("plan = %+v, want the live container and a catalog that was read", ordinary)
	}
	if len(ordinary.Volumes) != 1 || ordinary.Volumes[0].Source != "earnapp-data" || !ordinary.Volumes[0].Volume {
		t.Fatalf("the plan does not name the live named volume: %+v", ordinary.Volumes)
	}
	if ordinary.Volumes[0].Target != "/etc/earnapp" {
		t.Errorf("the plan reports the volume at %q, want the container path it is mounted at", ordinary.Volumes[0].Target)
	}
	if ordinary.HasCritical() {
		t.Errorf("an ordinary earner's data was marked irreplaceable: %+v", ordinary.Volumes)
	}

	identity, err := e.Manager.PlanRemoval(ctx, "mysterium")
	if err != nil {
		t.Fatalf("PlanRemoval(mysterium): %v", err)
	}
	if len(identity.Volumes) != 1 || identity.Volumes[0].Source != "mysterium-data" {
		t.Fatalf("the plan does not name the node's volume: %+v", identity.Volumes)
	}
	if !identity.Volumes[0].Critical || !identity.HasCritical() {
		t.Errorf("the node identity volume was not marked irreplaceable: %+v", identity.Volumes)
	}
	if identity.Volumes[0].Holds == "" {
		t.Error("the plan marks the volume irreplaceable without saying what is lost")
	}
}

// volumeDeletes lists the volumes the daemon was asked to delete, in order, from
// the recorded calls. What survived is only half the story: a volume also survives
// a delete that failed, so the tests check the request that was made as well.
func volumeDeletes(calls []string) []string {
	var deleted []string
	for _, call := range calls {
		rest, ok := strings.CutPrefix(call, "DELETE /volumes/")
		if !ok {
			continue
		}
		name, _, _ := strings.Cut(rest, "?")
		deleted = append(deleted, name)
	}
	return deleted
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
