package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"
)

// The checked deploy route (worker_api.go): the CashPilot web worker's
// GET and POST /api/containers/{slug}/deploy-checked, so a server can replace a
// container here only when it is the one the owner saw. The lookup names the
// container by its full ID ("none" when there is none); the deploy carries that
// ID as expected_container, and this computer compares it with the live
// container under its deploy lock, right before it replaces anything.

const checkedPath = "/api/containers/honeygain/deploy-checked"

// checkedSpec is honeygainSpec with the fields of the checked route.
func checkedSpec(t *testing.T, app *appUnderTest, extra map[string]any) map[string]any {
	t.Helper()
	spec := honeygainSpec(t, app)
	for k, v := range extra {
		spec[k] = v
	}
	return spec
}

// lookup answers the checked lookup as a map.
func lookup(t *testing.T, app *appUnderTest) map[string]any {
	t.Helper()
	w := call(t, app, http.MethodGet, checkedPath, testWorkerKey, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("lookup: %d %s", w.Code, w.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("lookup answered %s: %v", w.Body.String(), err)
	}
	return out
}

// earnerID is the full ID of the honeygain container in the fake Docker, "none" without one.
func earnerID(app *appUnderTest) string {
	if c, ok := app.Docker.Container("cashpilot-honeygain"); ok {
		return c.ID
	}
	return "none"
}

// targetChanged decodes a deploy_target_changed refusal.
func refusalDetail(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body struct {
		Detail map[string]any `json:"detail"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Detail == nil {
		t.Fatalf("not a structured refusal: %d %s", w.Code, w.Body.String())
	}
	return body.Detail
}

func TestCheckedLookupNamesTheContainerWithoutReadingLogs(t *testing.T) {
	app := joinedApp(t)
	if got := lookup(t, app); got["exists"] != false || got["expected_container"] != "none" {
		t.Fatalf("lookup with nothing deployed = %v; want exists false, none", got)
	}
	if w := call(t, app, http.MethodPost, "/api/containers/honeygain/deploy", testWorkerKey, honeygainSpec(t, app)); w.Code != http.StatusOK {
		t.Fatalf("deploy: %d %s", w.Code, w.Body.String())
	}
	app.Docker.ResetCalls()
	got := lookup(t, app)
	if got["exists"] != true || got["expected_container"] != earnerID(app) {
		t.Fatalf("lookup = %v; want exists true and the full ID %s", got, earnerID(app))
	}
	for _, c := range app.Docker.Calls() {
		if strings.Contains(c, "/logs") {
			t.Fatalf("the lookup read logs: %v", app.Docker.Calls())
		}
	}
	// A copy other than the first, and a service that is not in this catalog, are not deploy targets here.
	if w := call(t, app, http.MethodGet, checkedPath+"?instance=2", testWorkerKey, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("lookup of copy 2: %d %s; want 400", w.Code, w.Body.String())
	}
	if w := call(t, app, http.MethodGet, "/api/containers/not-in-catalog/deploy-checked", testWorkerKey, nil); w.Code != http.StatusForbidden {
		t.Fatalf("lookup of an unknown service: %d %s; want 403", w.Code, w.Body.String())
	}
	// Only this computer's own key opens it, as every other route.
	if w := call(t, app, http.MethodGet, checkedPath, testSharedKey, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("lookup with the shared key: %d; want 401", w.Code)
	}
	if w := call(t, app, http.MethodPost, checkedPath, testSharedKey, checkedSpec(t, app, map[string]any{"expected_container": "none"})); w.Code != http.StatusUnauthorized {
		t.Fatalf("checked deploy with the shared key: %d; want 401", w.Code)
	}
}

func TestCheckedDeployCreatesAndReplacesOnlyTheApprovedContainer(t *testing.T) {
	app := joinedApp(t)
	// A first deploy expects no container.
	w := call(t, app, http.MethodPost, checkedPath, testWorkerKey, checkedSpec(t, app, map[string]any{"expected_container": "none"}))
	if w.Code != http.StatusOK {
		t.Fatalf("first checked deploy: %d %s", w.Code, w.Body.String())
	}
	var answer map[string]string
	_ = json.Unmarshal(w.Body.Bytes(), &answer)
	first := earnerID(app)
	if answer["status"] != "deployed" || answer["container_id"] != first {
		t.Fatalf("first checked deploy answered %v; want the full ID %s", answer, first)
	}
	// An approved replacement names the container the lookup showed.
	seen := lookup(t, app)["expected_container"]
	w = call(t, app, http.MethodPost, checkedPath, testWorkerKey, checkedSpec(t, app, map[string]any{"expected_container": seen}))
	if w.Code != http.StatusOK {
		t.Fatalf("approved replacement: %d %s", w.Code, w.Body.String())
	}
	second := earnerID(app)
	if second == first || second == "none" {
		t.Fatalf("the approved replacement left %s; want a new container", second)
	}
	if last := app.lastServerCommand(); last == nil || last.Action != "deploy" || !last.OK {
		t.Fatalf("last command %+v", last)
	}
}

func TestCheckedDeployRefusesAContainerTheOwnerDidNotSee(t *testing.T) {
	app := joinedApp(t)
	if w := call(t, app, http.MethodPost, checkedPath, testWorkerKey, checkedSpec(t, app, map[string]any{"expected_container": "none"})); w.Code != http.StatusOK {
		t.Fatalf("first deploy: %d %s", w.Code, w.Body.String())
	}
	seen := earnerID(app)

	// The container changes after the lookup: another deploy replaces it.
	if w := call(t, app, http.MethodPost, "/api/containers/honeygain/deploy", testWorkerKey, honeygainSpec(t, app)); w.Code != http.StatusOK {
		t.Fatalf("legacy redeploy: %d %s", w.Code, w.Body.String())
	}
	current := earnerID(app)
	for _, tc := range []struct {
		name     string
		expected any
	}{
		{"the container the owner saw", seen},
		{"a first deploy, with a container there", "none"},
		{"no expectation at all, read as a first deploy", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			extra := map[string]any{}
			if tc.expected != nil {
				extra["expected_container"] = tc.expected
			}
			w := call(t, app, http.MethodPost, checkedPath, testWorkerKey, checkedSpec(t, app, extra))
			if w.Code != http.StatusConflict {
				t.Fatalf("stale checked deploy: %d %s; want 409", w.Code, w.Body.String())
			}
			d := refusalDetail(t, w)
			if d["error"] != "deploy_target_changed" || d["current_container"] != current {
				t.Fatalf("refusal = %v; want deploy_target_changed with %s", d, current)
			}
			if msg, _ := d["message"].(string); !strings.Contains(msg, "no longer matches the deploy request") {
				t.Fatalf("refusal message %q is not the worker's", msg)
			}
			if earnerID(app) != current {
				t.Fatalf("the refused deploy replaced %s with %s", current, earnerID(app))
			}
			if last := app.lastServerCommand(); last == nil || last.OK {
				t.Fatalf("a refused deploy was recorded as %+v", last)
			}
		})
	}

	// A successful replacement sent again (a lost reply, a retry) replaces nothing more.
	if w := call(t, app, http.MethodPost, checkedPath, testWorkerKey, checkedSpec(t, app, map[string]any{"expected_container": current})); w.Code != http.StatusOK {
		t.Fatalf("approved replacement: %d %s", w.Code, w.Body.String())
	}
	replaced := earnerID(app)
	w := call(t, app, http.MethodPost, checkedPath, testWorkerKey, checkedSpec(t, app, map[string]any{"expected_container": current}))
	if w.Code != http.StatusConflict || refusalDetail(t, w)["current_container"] != replaced || earnerID(app) != replaced {
		t.Fatalf("replayed replacement: %d %s; want 409 naming %s", w.Code, w.Body.String(), replaced)
	}

	// The container goes away after the lookup: the owner's yes was for one that is gone.
	if w := call(t, app, http.MethodDelete, "/api/containers/honeygain", testWorkerKey, nil); w.Code != http.StatusOK {
		t.Fatalf("remove: %d %s", w.Code, w.Body.String())
	}
	w = call(t, app, http.MethodPost, checkedPath, testWorkerKey, checkedSpec(t, app, map[string]any{"expected_container": replaced}))
	if w.Code != http.StatusConflict || refusalDetail(t, w)["current_container"] != "none" || earnerID(app) != "none" {
		t.Fatalf("deploy over a removed container: %d %s; want 409 naming none", w.Code, w.Body.String())
	}
}

func TestCheckedDeployTellsAnOlderDesktopToUpdate(t *testing.T) {
	// CashPilot-Desktop v1.6.0 confirms with replace_existing alone, which names no container.
	app := joinedApp(t)
	if w := call(t, app, http.MethodPost, checkedPath, testWorkerKey, checkedSpec(t, app, map[string]any{"expected_container": "none"})); w.Code != http.StatusOK {
		t.Fatalf("first deploy: %d %s", w.Code, w.Body.String())
	}
	current := earnerID(app)
	w := call(t, app, http.MethodPost, checkedPath, testWorkerKey, checkedSpec(t, app, map[string]any{"replace_existing": true}))
	if w.Code != http.StatusConflict {
		t.Fatalf("replace_existing alone: %d %s; want 409", w.Code, w.Body.String())
	}
	d := refusalDetail(t, w)
	if d["error"] != "replace_existing" || d["current_container"] != current || !strings.Contains(d["message"].(string), "Update CashPilot-Desktop") {
		t.Fatalf("refusal = %v", d)
	}
	if earnerID(app) != current {
		t.Fatal("replace_existing alone replaced the container")
	}
	// The flag never stands in for the ID: with the right ID it deploys, flag or not.
	if w := call(t, app, http.MethodPost, checkedPath, testWorkerKey, checkedSpec(t, app, map[string]any{"replace_existing": true, "expected_container": current})); w.Code != http.StatusOK {
		t.Fatalf("flag with the right ID: %d %s", w.Code, w.Body.String())
	}
}

func TestCheckedDeployRejectsAnExpectationItCannotRead(t *testing.T) {
	app := joinedApp(t)
	for _, tc := range []struct {
		name  string
		extra map[string]any
	}{
		{"an explicit null", map[string]any{"expected_container": nil}},
		{"a number", map[string]any{"expected_container": 7}},
		{"a flag that is not a boolean", map[string]any{"expected_container": "none", "replace_existing": "yes"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := call(t, app, http.MethodPost, checkedPath, testWorkerKey, checkedSpec(t, app, tc.extra))
			if w.Code != http.StatusUnprocessableEntity {
				t.Fatalf("%s: %d %s; want 422", tc.name, w.Code, w.Body.String())
			}
			if earnerID(app) != "none" {
				t.Fatal("an unreadable request deployed")
			}
		})
	}
}

func TestCheckedDeployKeepsTheLockFromTheCheckToTheReplacement(t *testing.T) {
	app := joinedApp(t)
	if w := call(t, app, http.MethodPost, checkedPath, testWorkerKey, checkedSpec(t, app, map[string]any{"expected_container": "none"})); w.Code != http.StatusOK {
		t.Fatalf("first deploy: %d %s", w.Code, w.Body.String())
	}
	seen := earnerID(app)
	spec := checkedSpec(t, app, map[string]any{"expected_container": seen})

	// Two requests approved for the same container arrive together (a double
	// click, two dashboards). While the first stops that container, the second
	// starts; it must wait, and then find the first one's replacement.
	second := make(chan *httptest.ResponseRecorder, 1)
	app.Docker.BeforeCall("POST /containers/cashpilot-honeygain/stop", func() {
		go func() { second <- call(t, app, http.MethodPost, checkedPath, testWorkerKey, spec) }()
		time.Sleep(500 * time.Millisecond)
	})
	first := call(t, app, http.MethodPost, checkedPath, testWorkerKey, spec)
	if first.Code != http.StatusOK {
		t.Fatalf("first approved replacement: %d %s", first.Code, first.Body.String())
	}
	winner := earnerID(app)
	var w *httptest.ResponseRecorder
	select {
	case w = <-second:
	case <-time.After(30 * time.Second):
		t.Fatal("the second request never finished")
	}
	if w.Code != http.StatusConflict {
		t.Fatalf("second approved replacement: %d %s; want 409 (it replaced the first one's container)", w.Code, w.Body.String())
	}
	if d := refusalDetail(t, w); d["error"] != "deploy_target_changed" || d["current_container"] != winner {
		t.Fatalf("second refusal = %v; want deploy_target_changed naming %s", d, winner)
	}
	if earnerID(app) != winner {
		t.Fatalf("the earner is %s; want the first replacement %s", earnerID(app), winner)
	}
	starts := 0
	for _, c := range app.Docker.Calls() {
		if strings.HasPrefix(c, "POST /containers/create") {
			starts++
		}
	}
	if starts != 2 { // the first deploy and the first replacement
		t.Fatalf("Docker created %d containers; want 2: %v", starts, app.Docker.Calls())
	}
}

func TestTheLegacyDeployRouteIsUnchanged(t *testing.T) {
	// An older CashPilot sends this route, with no expectation or with the short ID
	// of its container list; it keeps replacing whatever runs, as before.
	app := joinedApp(t)
	if w := call(t, app, http.MethodPost, "/api/containers/honeygain/deploy", testWorkerKey, honeygainSpec(t, app)); w.Code != http.StatusOK {
		t.Fatalf("first deploy: %d %s", w.Code, w.Body.String())
	}
	first := earnerID(app)
	for _, extra := range []map[string]any{
		{},
		{"expected_container": first[:min(12, len(first))], "replace_existing": true},
		{"expected_container": "not-this-one"},
	} {
		before := earnerID(app)
		w := call(t, app, http.MethodPost, "/api/containers/honeygain/deploy", testWorkerKey, checkedSpec(t, app, extra))
		if w.Code != http.StatusOK {
			t.Fatalf("legacy deploy with %v: %d %s", extra, w.Code, w.Body.String())
		}
		var answer map[string]string
		_ = json.Unmarshal(w.Body.Bytes(), &answer)
		after := earnerID(app)
		if after == before || answer["container_id"] != after[:min(12, len(after))] {
			t.Fatalf("legacy deploy with %v answered %v and left %s (was %s)", extra, answer, after, before)
		}
	}
	if !slices.Contains(app.Docker.Names(), "cashpilot-honeygain") {
		t.Fatal("no earner after the legacy deploys")
	}
}

func TestCheckedDeployRefusesAStaleTargetBeforePullingAnything(t *testing.T) {
	// As the web worker: compared once before any work, so a refusal never waits
	// on a slow registry, and again right before the replacement.
	app := joinedApp(t)
	if w := call(t, app, http.MethodPost, checkedPath, testWorkerKey, checkedSpec(t, app, map[string]any{"expected_container": "none"})); w.Code != http.StatusOK {
		t.Fatalf("first deploy: %d %s", w.Code, w.Body.String())
	}
	current := earnerID(app)
	app.Docker.ResetCalls()
	w := call(t, app, http.MethodPost, checkedPath, testWorkerKey, checkedSpec(t, app, map[string]any{"expected_container": "none"}))
	if w.Code != http.StatusConflict || refusalDetail(t, w)["current_container"] != current {
		t.Fatalf("stale first deploy: %d %s; want 409 naming %s", w.Code, w.Body.String(), current)
	}
	for _, c := range app.Docker.Calls() {
		if strings.HasPrefix(c, "POST /images/create") || strings.Contains(c, "/stop") || strings.HasPrefix(c, "POST /containers/create") {
			t.Fatalf("the refused deploy reached %q: %v", c, app.Docker.Calls())
		}
	}
}

func TestCheckedDeployComparesAgainRightBeforeTheReplacement(t *testing.T) {
	// The container changes outside CashPilot while the image is pulled, after
	// the first comparison: the runtime's own comparison, under its replace lock,
	// still refuses before anything is stopped.
	app := joinedApp(t)
	if w := call(t, app, http.MethodPost, checkedPath, testWorkerKey, checkedSpec(t, app, map[string]any{"expected_container": "none"})); w.Code != http.StatusOK {
		t.Fatalf("first deploy: %d %s", w.Code, w.Body.String())
	}
	seen := earnerID(app)
	app.Docker.BeforeCall("POST /images/create", func() { app.Docker.Rename("cashpilot-honeygain", "someone-elses-copy") })
	w := call(t, app, http.MethodPost, checkedPath, testWorkerKey, checkedSpec(t, app, map[string]any{"expected_container": seen}))
	if w.Code != http.StatusConflict || refusalDetail(t, w)["current_container"] != "none" {
		t.Fatalf("deploy over a container renamed away during the pull: %d %s; want 409 naming none", w.Code, w.Body.String())
	}
	if _, ok := app.Docker.Container("cashpilot-honeygain"); ok {
		t.Fatal("the refused deploy created a container")
	}
	if c, ok := app.Docker.Container("someone-elses-copy"); !ok || c.ID != seen || c.State != "running" {
		t.Fatalf("the renamed container was touched: %+v", c)
	}
}
