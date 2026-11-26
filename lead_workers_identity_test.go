package main

import (
	"net/http"
	"strings"
	"testing"
)

// A yes is about one worker. The Where box keeps the fleet row's number, and while
// a worker enrols, another worker that reports the same name takes over that row
// with its own client ID and address. Every question names the worker identity it
// was asked about (WorkerDeployResult.WorkerIdentity), the form sends it back with
// the yes, and a yes for another identity is dropped before anything is sent: the
// owner is asked about the worker that reports now.

// garageHeartbeat sends one bootstrap heartbeat for "garage" on the shared key.
func garageHeartbeat(t *testing.T, app *App, clientID, workerURL string) {
	t.Helper()
	reply := postHeartbeat(t, app, map[string]any{
		"name": "garage", "client_id": clientID, "url": workerURL,
		"system_info": map[string]any{"os": "Linux", "arch": "x86_64"},
	})
	if reply.Code != http.StatusOK {
		t.Fatalf("heartbeat %s: HTTP %d %s", clientID, reply.Code, reply.Body.String())
	}
}

func TestAYesAboutOneWorkerNeverReachesAnotherWithTheSameName(t *testing.T) {
	for _, tc := range []struct {
		name             string
		olderA, olderB   bool
		currentB         string
		wantDeployAfter  bool // B's own state needs no question after the owner's new yes
		wantAskedAboutID string
	}{
		{name: "named, then named with no container", currentB: "none", wantDeployAfter: true},
		{name: "named, then named with a container", currentB: dockerID(2), wantAskedAboutID: dockerID(2)},
		{name: "named, then older with a container", olderB: true, currentB: dockerID(2)},
		{name: "older, then older with a container", olderA: true, olderB: true, currentB: dockerID(2)},
		{name: "older, then named with no container", olderA: true, currentB: "none", wantDeployAfter: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := newLeadApp(t)
			a, b := newIDWorker(t), newIDWorker(t)
			a.set(dockerID(1))
			a.setOlder(tc.olderA)
			b.set(tc.currentB)
			b.setOlder(tc.olderB)
			garageHeartbeat(t, app, "client-a", a.srv.URL)
			id := workerID(t, app, "garage")
			req := WorkerDeployRequest{WorkerID: id, Values: map[string]string{"TOKEN": "typed"}}
			asked, err := app.DeployToWorker("plain", req)
			if err != nil || asked.Ask != askReplace || asked.WorkerIdentity == "" {
				t.Fatalf("question about A = %+v, %v; want a replacement question naming the worker", asked, err)
			}

			// B reports under the same name and takes over the row while the dialog is open.
			garageHeartbeat(t, app, "client-b", b.srv.URL)
			if next := workerID(t, app, "garage"); next != id {
				t.Fatalf("the row changed from %d to %d", id, next)
			}
			result, err := app.DeployToWorker("plain", approve(req, asked))
			aNow, aPosts := a.state()
			bNow, bPosts := b.state()
			if err != nil || result.Ask != askWorkerChanged || len(aPosts) != 0 || len(bPosts) != 0 || aNow != dockerID(1) || bNow != tc.currentB {
				t.Fatalf("yes about A after B took the row = %+v, %v; A %s with %d POSTs, B %s with %d POSTs; want a new question and no POST",
					result, err, aNow, len(aPosts), bNow, len(bPosts))
			}
			if result.WorkerIdentity == "" || result.WorkerIdentity == asked.WorkerIdentity || result.ExpectedContainer != "" {
				t.Fatalf("worker question = %+v; want B's identity and no container", result)
			}
			for _, want := range []string{"garage", "another CashPilot worker", "do not count"} {
				if !strings.Contains(result.Message, want) {
					t.Fatalf("worker question %q must say %q", result.Message, want)
				}
			}

			// A yes to that question carries no other answer: B's own state decides
			// what is asked next, as for a deploy that starts now.
			again, err := app.DeployToWorker("plain", WorkerDeployRequest{WorkerID: id, Values: req.Values, WorkerIdentity: result.WorkerIdentity})
			bNow, bPosts = b.state()
			if tc.wantDeployAfter {
				if err != nil || again.Ask != "" || len(bPosts) != 1 || bPosts[0]["expected_container"] != "none" {
					t.Fatalf("deploy to B after the yes = %+v, %v, B POSTs %v; want one create-only deploy", again, err, bPosts)
				}
				return
			}
			if err != nil || again.Ask != askReplace || len(bPosts) != 0 || again.WorkerIdentity != result.WorkerIdentity || again.ExpectedContainer != tc.wantAskedAboutID {
				t.Fatalf("deploy to B after the yes = %+v, %v, B POSTs %v; want B's own replacement question", again, err, bPosts)
			}
		})
	}
}

func TestEveryKindOfYesIsBoundToTheWorkerThatAsked(t *testing.T) {
	app := newLeadApp(t)
	a, b := newIDWorker(t), newIDWorker(t)
	a.set(dockerID(1))
	garageHeartbeat(t, app, "client-a", a.srv.URL)
	id := workerID(t, app, "garage")
	asked, err := app.DeployToWorker("plain", WorkerDeployRequest{WorkerID: id, Values: map[string]string{"TOKEN": "typed"}})
	if err != nil || asked.WorkerIdentity == "" {
		t.Fatalf("question = %+v, %v", asked, err)
	}
	garageHeartbeat(t, app, "client-b", b.srv.URL)
	a1 := asked.WorkerIdentity
	for _, req := range []WorkerDeployRequest{
		{AllowSharedIdentity: true, WorkerIdentity: a1},
		{Confirmed: true, WorkerIdentity: a1},
		{ReplaceExisting: true, ExpectedContainer: "none", WorkerIdentity: a1},
		// A yes the form sent without the worker it was about is bound to none.
		{ReplaceExisting: true, ExpectedContainer: "none"},
		{AllowSharedIdentity: true},
	} {
		req.WorkerID, req.Values = id, map[string]string{"TOKEN": "typed"}
		result, err := app.DeployToWorker("plain", req)
		if _, posts := b.state(); err != nil || result.Ask != askWorkerChanged || len(posts) != 0 {
			t.Fatalf("yes %+v sent after B took the row = %+v, %v, B POSTs %d; want the worker question", req, result, err, len(posts))
		}
	}
	// The same worker keeps its yes.
	if result, err := app.DeployToWorker("plain", WorkerDeployRequest{WorkerID: id, Values: map[string]string{"TOKEN": "typed"}, ReplaceExisting: true, ExpectedContainer: "none", WorkerIdentity: identityOf(t, app, id)}); err != nil || result.Ask != "" {
		t.Fatalf("yes bound to B = %+v, %v; want a deploy", result, err)
	}
}

func TestALateOlderWorkerRefusalSaysToUpdateTheWorker(t *testing.T) {
	app, worker, req := newIDWorkerLead(t)
	worker.setOlder(true)
	// No container at the lookup; one appears before the worker's own check.
	worker.mu.Lock()
	worker.beforeDeploy = func() { worker.current = dockerID(2) }
	worker.mu.Unlock()
	asked, err := app.DeployToWorker("plain", req)
	current, posts := worker.state()
	if err != nil || asked.Ask != askReplace || asked.ExpectedContainer != "" || current != dockerID(2) || len(posts) != 1 {
		t.Fatalf("late refusal = %+v, %v, worker runs %s, posts %v; want the older-worker question", asked, err, current, posts)
	}
	for _, want := range []string{"The existing container will be replaced", "Update CashPilot on garage"} {
		if !strings.Contains(asked.Message, want) {
			t.Fatalf("late question %q must say %q", asked.Message, want)
		}
	}
	if asked.WorkerIdentity == "" {
		t.Fatalf("late question %+v names no worker", asked)
	}
}

func TestAnUpgradeBetweenLookupAndDeployAsksAboutTheExactContainer(t *testing.T) {
	app, worker, req := newIDWorkerLead(t)
	seen := dockerID(100) // the worker numbers its own deploys from 1
	worker.set(seen)
	worker.setOlder(true)
	asked, err := app.DeployToWorker("plain", req)
	if err != nil || asked.Ask != askReplace || asked.ExpectedContainer != "" {
		t.Fatalf("older-worker question = %+v, %v", asked, err)
	}
	// The worker is updated after Desktop's lookup and before the deploy reaches it:
	// it refuses the bare flag, and its answer says to update Desktop.
	worker.mu.Lock()
	worker.beforeDeploy = func() { worker.older = false }
	worker.mu.Unlock()
	again, err := app.DeployToWorker("plain", approve(req, asked))
	current, posts := worker.state()
	if err != nil || again.Ask != askReplace || again.ExpectedContainer != seen || current != seen || len(posts) != 1 {
		t.Fatalf("yes on a worker updated mid-deploy = %+v, %v, worker runs %s, posts %v; want a question about %s", again, err, current, posts, seen[:12])
	}
	if !strings.Contains(again.Message, seen[:12]) || strings.Contains(again.Message, "Update CashPilot-Desktop") {
		t.Fatalf("question %q must name %s and not ask to update this Desktop", again.Message, seen[:12])
	}
	result, err := app.DeployToWorker("plain", approve(req, again))
	current, posts = worker.state()
	if err != nil || result.Ask != "" || result.ContainerID != current || current == seen || len(posts) != 2 || posts[1]["expected_container"] != seen {
		t.Fatalf("approved question = %+v, %v, posts %v", result, err, posts)
	}
}

// identityOf is the worker identity a question about worker id names now. The form
// sends it back with each yes.
func identityOf(t *testing.T, app *App, id int64) string {
	t.Helper()
	target, err := app.workerFor(id)
	if err != nil {
		t.Fatalf("worker %d: %v", id, err)
	}
	return target.identity()
}
