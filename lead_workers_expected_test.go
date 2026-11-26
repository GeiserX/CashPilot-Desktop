package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// idWorker is a CashPilot worker that names the container a deploy would replace
// (CashPilot #831): its lookup answers the full Docker ID, and its checked deploy
// replaces only the container the request expects, under one lock.
type idWorker struct {
	srv *httptest.Server

	mu      sync.Mutex
	current string // "none" or the full ID of cashpilot-plain
	serial  int
	posts   []map[string]any
	// beforeDeploy runs under the deploy lock, before the ID check: a test moves
	// the container there to change it between the lookup and the deploy.
	beforeDeploy func()
	// dropReply makes the next deploy finish on the worker and close the connection
	// before it answers.
	dropReply bool
	// older makes it answer as a worker before CashPilot #831 (a rollback): its
	// lookup names no container, and a deploy with replace_existing replaces
	// whatever container runs.
	older bool
}

// dockerID is a full Docker container ID, distinct in its first 12 characters too.
func dockerID(n int) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprint("container-", n))))
}

func newIDWorker(t *testing.T) *idWorker {
	t.Helper()
	w := &idWorker{current: "none"}
	w.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/containers/plain/deploy-checked" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			rw.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Method == http.MethodGet {
			w.mu.Lock()
			current, older := w.current, w.older
			w.mu.Unlock()
			if older {
				_ = json.NewEncoder(rw).Encode(map[string]any{"exists": current != "none"})
				return
			}
			_ = json.NewEncoder(rw).Encode(map[string]any{"exists": current != "none", "expected_container": current})
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		w.mu.Lock()
		w.posts = append(w.posts, body)
		if hook := w.beforeDeploy; hook != nil {
			w.beforeDeploy = nil
			hook()
		}
		expected, _ := body["expected_container"].(string)
		if expected == "" {
			expected = "none"
		}
		if w.older {
			// It reads only the flag: with it, the deploy replaces whatever runs.
			if body["replace_existing"] == true {
				expected = w.current
			} else if w.current != "none" {
				w.mu.Unlock()
				rw.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(rw).Encode(map[string]any{"detail": map[string]any{
					"error":   "replace_existing",
					"message": "A container already exists. Confirm the replacement to redeploy.",
				}})
				return
			}
		}
		if expected != w.current && !w.older && body["replace_existing"] == true && expected == "none" {
			// CashPilot #831 refuses the bare flag of an older Desktop and says so.
			current := w.current
			w.mu.Unlock()
			rw.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(rw).Encode(map[string]any{"detail": map[string]any{
				"error":             "replace_existing",
				"message":           "Update CashPilot-Desktop to confirm the exact container before replacing it.",
				"current_container": current,
			}})
			return
		}
		if expected != w.current {
			current := w.current
			w.mu.Unlock()
			rw.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(rw).Encode(map[string]any{"detail": map[string]any{
				"error":             "deploy_target_changed",
				"message":           "The container on this worker no longer matches the deploy request. Review the current container and use Redeploy to confirm a replacement.",
				"current_container": current,
			}})
			return
		}
		w.serial++
		w.current = dockerID(w.serial)
		created, drop := w.current, w.dropReply
		w.dropReply = false
		w.mu.Unlock()
		if drop {
			conn, _, err := rw.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("drop reply: %v", err)
				return
			}
			_ = conn.Close()
			return
		}
		_ = json.NewEncoder(rw).Encode(map[string]any{"status": "deployed", "container_id": created})
	}))
	t.Cleanup(w.srv.Close)
	return w
}

// set puts a container on the worker (or "none"), as a deploy from somewhere else would.
func (w *idWorker) set(id string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.current = id
}

func (w *idWorker) state() (string, []map[string]any) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.current, append([]map[string]any(nil), w.posts...)
}

func newIDWorkerLead(t *testing.T) (*App, *idWorker, WorkerDeployRequest) {
	t.Helper()
	app := newLeadApp(t)
	worker := newIDWorker(t)
	enrolWorker(t, app, "garage", worker.srv.URL, nil)
	return app, worker, WorkerDeployRequest{WorkerID: workerID(t, app, "garage"), Values: map[string]string{"TOKEN": "typed"}}
}

// approve is the deploy form's yes to the question in asked.
func approve(req WorkerDeployRequest, asked WorkerDeployResult) WorkerDeployRequest {
	req.ReplaceExisting = true
	req.ExpectedContainer = asked.ExpectedContainer
	req.WorkerIdentity = asked.WorkerIdentity
	return req
}

func TestAFirstWorkerDeployExpectsNoContainer(t *testing.T) {
	app, worker, req := newIDWorkerLead(t)
	result, err := app.DeployToWorker("plain", req)
	current, posts := worker.state()
	if err != nil || result.Ask != "" || result.ContainerID != current || len(posts) != 1 {
		t.Fatalf("first deploy = %+v, %v, posts %v; want one deploy and no question", result, err, posts)
	}
	if posts[0]["expected_container"] != "none" || posts[0]["replace_existing"] != nil {
		t.Fatalf("first deploy sent %v; want expected_container none and no replace flag", posts[0])
	}
}

func TestARedeployReplacesOnlyTheContainerTheOwnerSaw(t *testing.T) {
	app, worker, req := newIDWorkerLead(t)
	first := dockerID(100)
	worker.set(first)
	asked, err := app.DeployToWorker("plain", req)
	if _, posts := worker.state(); err != nil || asked.Ask != askReplace || asked.ExpectedContainer != first || len(posts) != 0 {
		t.Fatalf("redeploy = %+v, %v, posts %v; want a question about %s and no deploy", asked, err, posts, first)
	}
	if !strings.Contains(asked.Message, first[:12]) || strings.Contains(asked.Message, "Update CashPilot") {
		t.Fatalf("question %q must name container %s", asked.Message, first[:12])
	}
	result, err := app.DeployToWorker("plain", approve(req, asked))
	current, posts := worker.state()
	if err != nil || result.Ask != "" || result.ContainerID != current || current == first || len(posts) != 1 {
		t.Fatalf("approved redeploy = %+v, %v, worker runs %s; posts %v", result, err, current, posts)
	}
	if posts[0]["expected_container"] != first || posts[0]["replace_existing"] != nil {
		t.Fatalf("approved redeploy sent %v; want expected_container %s", posts[0], first)
	}
}

func TestAYesWithoutTheContainerIsNoYes(t *testing.T) {
	app, worker, req := newIDWorkerLead(t)
	worker.set(dockerID(1))
	req.ReplaceExisting = true // A yes that names no container cannot replace one.
	req.WorkerIdentity = identityOf(t, app, req.WorkerID)
	asked, err := app.DeployToWorker("plain", req)
	if _, posts := worker.state(); err != nil || asked.Ask != askReplace || asked.ExpectedContainer != dockerID(1) || len(posts) != 0 {
		t.Fatalf("deploy = %+v, %v, posts %v; want the question again", asked, err, posts)
	}
}

func TestAContainerThatChangesWhileTheDialogIsOpenIsAskedAbout(t *testing.T) {
	app, worker, req := newIDWorkerLead(t)
	seen, swapped := dockerID(1), dockerID(2)
	worker.set(seen)
	asked, err := app.DeployToWorker("plain", req)
	if err != nil || asked.ExpectedContainer != seen {
		t.Fatalf("first question = %+v, %v", asked, err)
	}
	// Another controller replaces the container while the owner reads the question.
	worker.set(swapped)
	again, err := app.DeployToWorker("plain", approve(req, asked))
	current, posts := worker.state()
	if err != nil || again.Ask != askReplace || again.ExpectedContainer != swapped || current != swapped || len(posts) != 0 {
		t.Fatalf("approved stale question = %+v, %v, worker runs %s, posts %v; want a new question and no deploy", again, err, current, posts)
	}
	if !strings.Contains(again.Message, swapped[:12]) || !strings.Contains(again.Message, "changed") {
		t.Fatalf("new question %q must say the container changed and name %s", again.Message, swapped[:12])
	}
	result, err := app.DeployToWorker("plain", approve(req, again))
	current, posts = worker.state()
	if err != nil || result.ContainerID != current || current == swapped || len(posts) != 1 || posts[0]["expected_container"] != swapped {
		t.Fatalf("approved new question = %+v, %v, posts %v", result, err, posts)
	}
}

func TestAContainerThatChangesDuringTheDeployIsAskedAboutWithTheWorkersWords(t *testing.T) {
	app, worker, req := newIDWorkerLead(t)
	seen, swapped := dockerID(1), dockerID(2)
	worker.set(seen)
	asked, err := app.DeployToWorker("plain", req)
	if err != nil || asked.ExpectedContainer != seen {
		t.Fatalf("first question = %+v, %v", asked, err)
	}
	// The container changes after Desktop's lookup, before the worker's check.
	worker.mu.Lock()
	worker.beforeDeploy = func() { worker.current = swapped }
	worker.mu.Unlock()
	again, err := app.DeployToWorker("plain", approve(req, asked))
	current, posts := worker.state()
	if err != nil || again.Ask != askReplace || again.ExpectedContainer != swapped || current != swapped || len(posts) != 1 {
		t.Fatalf("refused deploy = %+v, %v, worker runs %s, posts %v; want a new question about %s", again, err, current, posts, swapped)
	}
	for _, want := range []string{"deploy_target_changed", "no longer matches the deploy request", swapped[:12]} {
		if !strings.Contains(again.Message, want) {
			t.Fatalf("new question %q must show %q", again.Message, want)
		}
	}
	if strings.Contains(again.Message, "HTTP 409") {
		t.Fatalf("new question %q shows a bare status instead of the worker's words", again.Message)
	}
	result, err := app.DeployToWorker("plain", approve(req, again))
	current, posts = worker.state()
	if err != nil || result.ContainerID != current || current == swapped || len(posts) != 2 || posts[1]["expected_container"] != swapped {
		t.Fatalf("approved new question = %+v, %v, posts %v", result, err, posts)
	}
}

func TestARepeatedApprovedRequestReplacesNothingMore(t *testing.T) {
	app, worker, req := newIDWorkerLead(t)
	worker.set(dockerID(50))
	asked, err := app.DeployToWorker("plain", req)
	if err != nil || asked.Ask != askReplace {
		t.Fatalf("question = %+v, %v", asked, err)
	}
	approved := approve(req, asked)
	first, err := app.DeployToWorker("plain", approved)
	if err != nil || first.ContainerID == "" {
		t.Fatalf("approved redeploy = %+v, %v", first, err)
	}
	// The same yes sent again (a double click, a retry) must not replace the new container.
	again, err := app.DeployToWorker("plain", approved)
	current, posts := worker.state()
	if err != nil || again.Ask != askReplace || again.ExpectedContainer != first.ContainerID || current != first.ContainerID || len(posts) != 1 {
		t.Fatalf("repeated yes = %+v, %v, worker runs %s, posts %v; want a question about %s", again, err, current, posts, first.ContainerID)
	}
	// Even when it reaches the worker (its lookup raced the first deploy), the worker refuses it.
	worker.mu.Lock()
	worker.current = dockerID(50)
	worker.beforeDeploy = func() { worker.current = first.ContainerID }
	worker.mu.Unlock()
	raced, err := app.DeployToWorker("plain", approved)
	current, posts = worker.state()
	if err != nil || raced.Ask != askReplace || raced.ExpectedContainer != first.ContainerID || current != first.ContainerID || len(posts) != 2 {
		t.Fatalf("raced repeated yes = %+v, %v, worker runs %s, posts %v", raced, err, current, posts)
	}
}

func TestADroppedSuccessfulReplyIsNotDeployedAgainUnasked(t *testing.T) {
	app, worker, req := newIDWorkerLead(t)
	worker.mu.Lock()
	worker.dropReply = true
	worker.mu.Unlock()
	if _, err := app.DeployToWorker("plain", req); err == nil || !strings.Contains(err.Error(), "dropped") {
		t.Fatalf("first deploy = %v; want an unanswered request", err)
	}
	made, posts := worker.state()
	if made == "none" || len(posts) != 1 {
		t.Fatalf("the first deploy did not finish on the worker: %s %v", made, posts)
	}
	retry, err := app.DeployToWorker("plain", req)
	current, posts := worker.state()
	if err != nil || retry.Ask != askReplace || retry.ExpectedContainer != made || current != made || len(posts) != 1 {
		t.Fatalf("retry = %+v, %v, worker runs %s, posts %v; want a question about %s", retry, err, current, posts, made)
	}
	// The retry's lookup ran before the first deploy made its container: the
	// worker's refusal comes back as the question, with its words.
	worker.mu.Lock()
	worker.current = "none"
	worker.beforeDeploy = func() { worker.current = made }
	worker.mu.Unlock()
	raced, err := app.DeployToWorker("plain", req)
	current, posts = worker.state()
	if err != nil || raced.Ask != askReplace || raced.ExpectedContainer != made || current != made || len(posts) != 2 ||
		!strings.Contains(raced.Message, "deploy_target_changed") {
		t.Fatalf("raced retry = %+v, %v, worker runs %s, posts %v", raced, err, current, posts)
	}
}

func TestAnOlderWorkerKeepsTheReplaceFlagAndSaysToUpdate(t *testing.T) {
	// A worker whose lookup does not name the container (before CashPilot #831).
	app := newLeadApp(t)
	worker := newFakeDockerWorker(t)
	enrolWorker(t, app, "garage", worker.srv.URL, nil)
	req := WorkerDeployRequest{WorkerID: workerID(t, app, "garage"), Values: map[string]string{"TOKEN": "typed"}}
	if result, err := app.DeployToWorker("plain", req); err != nil || result.Ask != "" || worker.count() != 1 {
		t.Fatalf("first deploy = %+v, %v", result, err)
	}
	if _, sent := worker.last(t).Body["expected_container"]; sent {
		t.Fatalf("an older worker got a field it does not know: %v", worker.last(t).Body)
	}
	asked, err := app.DeployToWorker("plain", req)
	if err != nil || asked.Ask != askReplace || asked.ExpectedContainer != "" || worker.count() != 1 {
		t.Fatalf("redeploy = %+v, %v", asked, err)
	}
	if !strings.Contains(asked.Message, "The existing container will be replaced") || !strings.Contains(asked.Message, "Update CashPilot on garage") {
		t.Fatalf("older worker question %q must keep its words and say to update the worker", asked.Message)
	}
	if result, err := app.DeployToWorker("plain", approve(req, asked)); err != nil || result.Ask != "" || worker.count() != 2 {
		t.Fatalf("approved redeploy = %+v, %v", result, err)
	}
	body := worker.last(t).Body
	if body["replace_existing"] != true || body["expected_container"] != nil {
		t.Fatalf("older worker redeploy sent %v; want replace_existing only", body)
	}
}

func TestAWorkerWithoutTheCheckedRouteSaysToUpdate(t *testing.T) {
	app := newLeadApp(t)
	var deploys []map[string]any
	var mu sync.Mutex
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/containers/plain/deploy-checked":
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"detail":"Not Found"}`)
		case "/api/containers":
			_, _ = io.WriteString(w, `[{"name":"cashpilot-plain"}]`)
		case "/api/containers/plain/deploy":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			mu.Lock()
			deploys = append(deploys, body)
			mu.Unlock()
			_, _ = io.WriteString(w, `{"status":"deployed","container_id":"legacy"}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer worker.Close()
	enrolWorker(t, app, "garage", worker.URL, nil)
	req := WorkerDeployRequest{WorkerID: workerID(t, app, "garage"), Values: map[string]string{"TOKEN": "typed"}}
	asked, err := app.DeployToWorker("plain", req)
	if err != nil || asked.Ask != askReplace || asked.ExpectedContainer != "" || !strings.Contains(asked.Message, "Update CashPilot on garage") {
		t.Fatalf("redeploy = %+v, %v", asked, err)
	}
	if result, err := app.DeployToWorker("plain", approve(req, asked)); err != nil || result.ContainerID != "legacy" {
		t.Fatalf("approved redeploy = %+v, %v", result, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(deploys) != 1 || deploys[0]["replace_existing"] != true || deploys[0]["expected_container"] != nil {
		t.Fatalf("legacy deploy sent %v", deploys)
	}
}

// setOlder rolls the worker back to a release before CashPilot #831, or forward again.
func (w *idWorker) setOlder(older bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.older = older
}

func TestAYesForOneContainerIsAskedAgainWhenTheWorkerStopsNamingIt(t *testing.T) {
	app, worker, req := newIDWorkerLead(t)
	seen, swapped := dockerID(1), dockerID(2)
	worker.set(seen)
	asked, err := app.DeployToWorker("plain", req)
	if err != nil || asked.Ask != askReplace || asked.ExpectedContainer != seen {
		t.Fatalf("first question = %+v, %v", asked, err)
	}
	// While the owner reads the question about seen, the worker is rolled back to
	// a release that names no container, and another container takes seen's place.
	worker.setOlder(true)
	worker.set(swapped)
	again, err := app.DeployToWorker("plain", approve(req, asked))
	current, posts := worker.state()
	if err != nil || again.Ask != askReplace || again.ExpectedContainer != "" || current != swapped || len(posts) != 0 {
		t.Fatalf("yes for %s on a rolled-back worker = %+v, %v, worker runs %s, posts %v; want the older-worker question and no deploy",
			seen[:12], again, err, current, posts)
	}
	for _, want := range []string{"does not count", "The existing container will be replaced", "Update CashPilot on garage"} {
		if !strings.Contains(again.Message, want) {
			t.Fatalf("new question %q must say %q", again.Message, want)
		}
	}
	// Only a yes to that question sends the flag.
	result, err := app.DeployToWorker("plain", approve(req, again))
	current, posts = worker.state()
	if err != nil || result.Ask != "" || result.ContainerID != current || current == swapped || len(posts) != 1 {
		t.Fatalf("approved older-worker question = %+v, %v, worker runs %s, posts %v", result, err, current, posts)
	}
	if posts[0]["replace_existing"] != true || posts[0]["expected_container"] != nil {
		t.Fatalf("older-worker redeploy sent %v; want replace_existing only", posts[0])
	}
}

func TestAYesForOneContainerSendsNoFlagWhenTheRolledBackWorkerHasNone(t *testing.T) {
	app, worker, req := newIDWorkerLead(t)
	seen := dockerID(1)
	worker.set(seen)
	asked, err := app.DeployToWorker("plain", req)
	if err != nil || asked.ExpectedContainer != seen {
		t.Fatalf("first question = %+v, %v", asked, err)
	}
	// Rolled back with no container, and one appears after the lookup: the yes for
	// seen must not reach the worker as the flag.
	worker.setOlder(true)
	worker.set("none")
	worker.mu.Lock()
	worker.beforeDeploy = func() { worker.current = dockerID(2) }
	worker.mu.Unlock()
	again, err := app.DeployToWorker("plain", approve(req, asked))
	current, posts := worker.state()
	if err != nil || again.Ask != askReplace || again.ExpectedContainer != "" || current != dockerID(2) || len(posts) != 1 {
		t.Fatalf("deploy = %+v, %v, worker runs %s, posts %v; want the older-worker question", again, err, current, posts)
	}
	if posts[0]["replace_existing"] != nil && posts[0]["replace_existing"] != false {
		t.Fatalf("a yes for %s was sent as %v", seen[:12], posts[0])
	}
}

func TestAnOlderWorkerYesIsAskedAgainWhenTheWorkerStartsNamingTheContainer(t *testing.T) {
	app, worker, req := newIDWorkerLead(t)
	seen := dockerID(1)
	worker.set(seen)
	worker.setOlder(true)
	asked, err := app.DeployToWorker("plain", req)
	if err != nil || asked.Ask != askReplace || asked.ExpectedContainer != "" {
		t.Fatalf("older-worker question = %+v, %v", asked, err)
	}
	// The worker is updated while the dialog is open: the yes named no container.
	worker.setOlder(false)
	again, err := app.DeployToWorker("plain", approve(req, asked))
	current, posts := worker.state()
	if err != nil || again.Ask != askReplace || again.ExpectedContainer != seen || current != seen || len(posts) != 0 {
		t.Fatalf("older-worker yes on an updated worker = %+v, %v, worker runs %s, posts %v; want a question about %s",
			again, err, current, posts, seen[:12])
	}
	result, err := app.DeployToWorker("plain", approve(req, again))
	current, posts = worker.state()
	if err != nil || result.ContainerID != current || len(posts) != 1 || posts[0]["expected_container"] != seen || posts[0]["replace_existing"] != nil {
		t.Fatalf("approved question = %+v, %v, posts %v", result, err, posts)
	}
}
