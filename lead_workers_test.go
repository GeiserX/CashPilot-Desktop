package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/services"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
	"github.com/GeiserX/CashPilot-Desktop/internal/workerapi"
)

// workerCall is one request a fake worker received.
type workerCall struct {
	Method, Path, Query, Auth string
	Body                      map[string]any
}

// fakeDockerWorker is a CashPilot Docker worker's API: it records each call and
// answers with reply (status 200 and {"status":"ok"} by default).
type fakeDockerWorker struct {
	srv   *httptest.Server
	mu    sync.Mutex
	calls []workerCall
	reply func(call workerCall) (int, string)
}

func newFakeDockerWorker(t *testing.T) *fakeDockerWorker {
	t.Helper()
	f := &fakeDockerWorker{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		call := workerCall{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Auth: r.Header.Get("Authorization")}
		_ = json.Unmarshal(raw, &call.Body)
		f.mu.Lock()
		f.calls = append(f.calls, call)
		reply := f.reply
		f.mu.Unlock()
		status, body := 200, `{"status":"ok","container_id":"c0ffee"}`
		if reply != nil {
			status, body = reply(call)
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeDockerWorker) last(t *testing.T) workerCall {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		t.Fatal("the worker received no request")
	}
	return f.calls[len(f.calls)-1]
}

func (f *fakeDockerWorker) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

const leadCatalogYAML = `name: Plain
slug: plain
category: bandwidth
status: active
docker:
  image: repo/plain:1.0.0
  env:
    - key: TOKEN
      label: Token
      required: true
      secret: true
    - key: DEVICE_NAME
      label: Device name
      default: "cashpilot-{hostname}"
  volumes:
    - "plain-data:/data"
`

const leadCriticalYAML = `name: Vault
slug: vault
category: storage
status: active
docker:
  image: repo/vault:2.0.0
  volumes:
    - "vault-id:/id"
  critical_volumes:
    - target: /id
      holds: the node identity
`

const leadDeviceIDYAML = `name: Ider
slug: ider
category: bandwidth
status: active
docker:
  image: repo/ider:3.0.0
  env:
    - key: DEVICE_ID
      label: Device ID
      per_device: true
`

// newLeadApp is a lead Desktop that accepts workers, with a small catalog.
func newLeadApp(t *testing.T) *App {
	t.Helper()
	app := newFleetTestApp(t, "fleet-key")
	cat, err := catalog.LoadEmbedded(fstest.MapFS{
		"services/bandwidth/plain.yml": {Data: []byte(leadCatalogYAML)},
		"services/storage/vault.yml":   {Data: []byte(leadCriticalYAML)},
		"services/bandwidth/ider.yml":  {Data: []byte(leadDeviceIDYAML)},
	})
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	app.catalog = cat
	provider := runtime.NewDockerProvider()
	app.runtime = provider
	app.services = services.NewManager(provider, cat, app.store)
	return app
}

// enrolWorker sends the heartbeats a CashPilot worker sends: one on the shared key,
// which gets its own key back, and one on its own key, which confirms it. It answers
// the worker's own key.
func enrolWorker(t *testing.T, app *App, name, url string, containers []map[string]any) string {
	t.Helper()
	body := map[string]any{
		"name": name, "client_id": "client-" + name, "url": url, "containers": containers,
		"system_info": map[string]any{"os": "Linux 6.1", "arch": "x86_64", "features": []any{"instances", "fence_gated", 7}},
	}
	w := postHeartbeat(t, app, body)
	if w.Code != http.StatusOK {
		t.Fatalf("enrol %s: %d %s", name, w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	key, _ := resp["worker_key"].(string)
	if key == "" {
		t.Fatalf("enrol %s: no worker_key in %v", name, resp)
	}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/workers/heartbeat", strings.NewReader(string(raw)))
	req.Header.Set("Authorization", "Bearer "+key)
	rec := httptest.NewRecorder()
	app.handleWorkerHeartbeat(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("confirm %s: %d", name, rec.Code)
	}
	return key
}

func workerID(t *testing.T, app *App, name string) int64 {
	t.Helper()
	for _, d := range app.store.ListFleetDevices() {
		if d.Name == name {
			return d.ID
		}
	}
	t.Fatalf("no device %s", name)
	return 0
}

func TestALeadDeploysToItsWorkerWithTheWorkersOwnKey(t *testing.T) {
	app := newLeadApp(t)
	worker := newFakeDockerWorker(t)
	key := enrolWorker(t, app, "garage", worker.srv.URL, nil)
	if err := app.store.SaveCredentials("plain", map[string]string{"TOKEN": "stored-token"}); err != nil {
		t.Fatal(err)
	}

	res, err := app.DeployToWorker("plain", WorkerDeployRequest{WorkerID: workerID(t, app, "garage"), Values: map[string]string{"TOKEN": "", "DEVICE_NAME": ""}})
	if err != nil {
		t.Fatalf("DeployToWorker: %v", err)
	}
	if res.Ask != "" || res.Worker != "garage" || res.ContainerID != "c0ffee" {
		t.Fatalf("result = %+v", res)
	}
	call := worker.last(t)
	if call.Method != http.MethodPost || call.Path != "/api/containers/plain/deploy" {
		t.Fatalf("sent %s %s", call.Method, call.Path)
	}
	if call.Auth != "Bearer "+key {
		t.Fatalf("Authorization = %q: a confirmed worker takes only its own key", call.Auth)
	}
	env, _ := call.Body["env"].(map[string]any)
	if env["TOKEN"] != "stored-token" {
		t.Fatalf("env = %v: the worker keeps no credentials, so the deploy carries them", env)
	}
	if env["DEVICE_NAME"] != "cashpilot-"+workerapi.DeviceName("cashpilot", "client-garage", 0) {
		t.Fatalf("DEVICE_NAME = %v: each worker gets its own device name", env["DEVICE_NAME"])
	}
	// The Fleet page shows the new copy before the next heartbeat.
	state, err := app.GetFleetState()
	if err != nil {
		t.Fatal(err)
	}
	view := state.WorkerCommands[jsonID(workerID(t, app, "garage"))]
	if view.Reason != "" || len(view.Copies) != 1 || view.Copies[0].Slug != "plain" || view.Copies[0].Status != "running" {
		t.Fatalf("fleet view = %+v", view)
	}
}

func jsonID(id int64) string {
	raw, _ := json.Marshal(id)
	return string(raw)
}

func TestTheWorkerChoiceFollowsTheWebRules(t *testing.T) {
	app := newLeadApp(t)
	if err := app.store.SaveCredentials("plain", map[string]string{"TOKEN": "t"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.DeployToWorker("plain", WorkerDeployRequest{}); !errors.Is(err, workerapi.ErrNoWorker) {
		t.Fatalf("no worker: %v", err)
	}
	first := newFakeDockerWorker(t)
	enrolWorker(t, app, "first", first.srv.URL, nil)
	if _, err := app.DeployToWorker("plain", WorkerDeployRequest{}); err != nil || first.count() != 1 {
		t.Fatalf("one online worker is picked: %v (calls %d)", err, first.count())
	}
	second := newFakeDockerWorker(t)
	enrolWorker(t, app, "second", second.srv.URL, nil)
	if _, err := app.DeployToWorker("plain", WorkerDeployRequest{}); !errors.Is(err, workerapi.ErrChooseWorker) {
		t.Fatalf("several online: %v", err)
	}
	if _, err := app.DeployToWorker("plain", WorkerDeployRequest{WorkerID: workerID(t, app, "second")}); err != nil || second.count() != 1 {
		t.Fatalf("the chosen worker: %v (calls %d)", err, second.count())
	}
	targets := app.workerTargets()
	if len(targets) != 2 || !targets[0].Online || targets[0].Reason != "" {
		t.Fatalf("targets = %+v", targets)
	}
}

func TestAnOfflineWorkerIsNamedNotCalled(t *testing.T) {
	app := newLeadApp(t)
	worker := newFakeDockerWorker(t)
	enrolWorker(t, app, "garage", worker.srv.URL, nil)
	if err := app.store.SaveCredentials("plain", map[string]string{"TOKEN": "t"}); err != nil {
		t.Fatal(err)
	}
	// Its heartbeats stopped long ago.
	if _, err := app.store.UpsertFleetHeartbeat(store.FleetDevice{Name: "garage", Kind: "worker", Endpoint: worker.srv.URL, LastSeen: "2020-01-01T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	_, err := app.DeployToWorker("plain", WorkerDeployRequest{WorkerID: workerID(t, app, "garage")})
	if err == nil || !strings.Contains(err.Error(), "garage is offline") || worker.count() != 0 {
		t.Fatalf("err = %v, calls %d", err, worker.count())
	}
}

func TestAWorkerThatStoppedAnsweringSaysSo(t *testing.T) {
	app := newLeadApp(t)
	worker := newFakeDockerWorker(t)
	enrolWorker(t, app, "garage", worker.srv.URL, nil)
	worker.srv.Close()
	_, err := app.CommandWorkerCopy(workerID(t, app, "garage"), "plain", "stop", 1)
	if err == nil || !strings.Contains(err.Error(), "garage: Could not reach the worker") {
		t.Fatalf("err = %v", err)
	}
}

func TestARefusedSpecShowsTheWorkersMessage(t *testing.T) {
	app := newLeadApp(t)
	worker := newFakeDockerWorker(t)
	worker.reply = func(workerCall) (int, string) {
		return 403, `{"detail":"Capability NET_ADMIN is not allowed on this worker"}`
	}
	enrolWorker(t, app, "garage", worker.srv.URL, nil)
	_, err := app.DeployToWorker("plain", WorkerDeployRequest{WorkerID: workerID(t, app, "garage"), Values: map[string]string{"TOKEN": "t"}})
	if err == nil || err.Error() != "garage: Capability NET_ADMIN is not allowed on this worker" {
		t.Fatalf("err = %v", err)
	}
}

func TestACustomImageOnAnIdentityServiceNeedsAYesAndIsSigned(t *testing.T) {
	app := newLeadApp(t)
	worker := newFakeDockerWorker(t)
	key := enrolWorker(t, app, "garage", worker.srv.URL, nil)
	id := workerID(t, app, "garage")

	res, err := app.DeployToWorker("vault", WorkerDeployRequest{WorkerID: id, Image: "mine/vault:9"})
	if err != nil || res.Ask != "step_up_required" || !strings.Contains(res.Message, "keeps a wallet or node identity") {
		t.Fatalf("res %+v err %v", res, err)
	}
	if worker.count() != 0 {
		t.Fatal("nothing may be sent before the owner's yes")
	}
	res, err = app.DeployToWorker("vault", WorkerDeployRequest{WorkerID: id, Image: "mine/vault:9", Confirmed: true})
	if err != nil || res.Ask != "" {
		t.Fatalf("confirmed: %+v %v", res, err)
	}
	call := worker.last(t)
	approval, _ := call.Body["approval"].(string)
	if call.Body["image"] != "mine/vault:9" || approval == "" {
		t.Fatalf("body = %v", call.Body)
	}
	// The approval verifies with the worker's key over the exact spec it received.
	volumes := map[string]workerapi.Mount{}
	for source, m := range call.Body["volumes"].(map[string]any) {
		mm := m.(map[string]any)
		volumes[source] = workerapi.Mount{Bind: mm["bind"].(string), Mode: mm["mode"].(string)}
	}
	target := workerapi.DeployTarget("vault", "mine/vault:9", nil, map[string]string{}, volumes)
	if !verifyApproval(key, approval, target) {
		t.Fatalf("approval %q does not verify for %q", approval, target)
	}
}

func TestAWorkersOwnStepUpQuestionIsAsked(t *testing.T) {
	app := newLeadApp(t)
	worker := newFakeDockerWorker(t)
	worker.reply = func(call workerCall) (int, string) {
		if call.Body["approval"] != nil {
			return 200, `{"status":"deployed","container_id":"ok"}`
		}
		return 403, `{"detail":{"error":"step_up_required","message":"vault keeps a wallet or node identity, and this deploy differs from the catalog: the image repo/vault:2.0.0 is not the repo/vault:2.1.0 this worker's catalog pins."}}`
	}
	enrolWorker(t, app, "garage", worker.srv.URL, nil)
	id := workerID(t, app, "garage")
	res, err := app.DeployToWorker("vault", WorkerDeployRequest{WorkerID: id})
	if err != nil || res.Ask != "step_up_required" || !strings.Contains(res.Message, "this worker's catalog pins") {
		t.Fatalf("res %+v err %v", res, err)
	}
	if res, err = app.DeployToWorker("vault", WorkerDeployRequest{WorkerID: id, Confirmed: true}); err != nil || res.ContainerID != "ok" {
		t.Fatalf("after the yes: %+v %v", res, err)
	}
}

func TestADeviceIDThatRunsElsewhereNeedsAYes(t *testing.T) {
	app := newLeadApp(t)
	first := newFakeDockerWorker(t)
	second := newFakeDockerWorker(t)
	enrolWorker(t, app, "first", first.srv.URL, []map[string]any{{"slug": "ider", "status": "running", "instance": 1}})
	enrolWorker(t, app, "second", second.srv.URL, nil)
	id := workerID(t, app, "second")
	res, err := app.DeployToWorker("ider", WorkerDeployRequest{WorkerID: id, Values: map[string]string{"DEVICE_ID": "dev-123"}})
	if err != nil || res.Ask != "identity_in_use" || !strings.Contains(res.Message, "The Device ID for Ider already runs on first") || strings.Contains(res.Message, "dev-123") {
		t.Fatalf("res %+v err %v", res, err)
	}
	if second.count() != 0 {
		t.Fatal("nothing may be sent before the owner's yes")
	}
	if res, err = app.DeployToWorker("ider", WorkerDeployRequest{WorkerID: id, Values: map[string]string{"DEVICE_ID": "dev-123"}, AllowSharedIdentity: true}); err != nil || res.Ask != "" || second.count() != 1 {
		t.Fatalf("after the yes: %+v %v", res, err)
	}
}

func TestACopyCommandReachesTheWorkerAndTheFleetPage(t *testing.T) {
	app := newLeadApp(t)
	worker := newFakeDockerWorker(t)
	key := enrolWorker(t, app, "garage", worker.srv.URL, []map[string]any{
		{"slug": "plain", "status": "running", "instance": 1},
		{"slug": "plain", "status": "running", "instance": 2},
		{"slug": "found", "status": "running", "deployed_by": "external"},
	})
	id := workerID(t, app, "garage")
	msg, err := app.CommandWorkerCopy(id, "plain", "stop", 2)
	if err != nil || msg != "Copy 2 of plain on garage stopped." {
		t.Fatalf("msg %q err %v", msg, err)
	}
	call := worker.last(t)
	if call.Method != http.MethodPost || call.Path != "/api/containers/plain/stop" || call.Query != "instance=2" || call.Auth != "Bearer "+key {
		t.Fatalf("call = %+v", call)
	}
	state, _ := app.GetFleetState()
	copies := state.WorkerCommands[jsonID(id)].Copies
	if len(copies) != 3 || copies[1].Status != "exited" || copies[2].Managed {
		t.Fatalf("copies = %+v", copies)
	}
	if _, err := app.CommandWorkerCopy(id, "plain", "remove", 1); err != nil {
		t.Fatal(err)
	}
	if call := worker.last(t); call.Method != http.MethodDelete || call.Path != "/api/containers/plain" {
		t.Fatalf("remove sent %+v", call)
	}
	if _, err := app.CommandWorkerCopy(id, "plain", "wipe", 1); err == nil {
		t.Fatal("an unknown command must be refused")
	}
}

func TestAWorkerEnrolledBeforeKeysWereKeptSaysHowToFixIt(t *testing.T) {
	app := newLeadApp(t)
	worker := newFakeDockerWorker(t)
	enrolWorker(t, app, "old", worker.srv.URL, nil)
	// What an older Desktop stored: the hash only, confirmed.
	if _, err := app.store.UpsertFleetHeartbeat(store.FleetDevice{Name: "legacy", Kind: "worker", Endpoint: worker.srv.URL}); err != nil {
		t.Fatal(err)
	}
	if err := app.store.SetFleetDeviceKey("worker", "legacy", store.HashFleetKey("lost")); err != nil {
		t.Fatal(err)
	}
	if err := app.store.ConfirmFleetDeviceKey("worker", "legacy"); err != nil {
		t.Fatal(err)
	}
	_, err := app.CommandWorkerCopy(workerID(t, app, "legacy"), "plain", "stop", 1)
	if err == nil || !strings.Contains(err.Error(), "Remove it on the Fleet page") || worker.count() != 0 {
		t.Fatalf("err = %v, calls %d", err, worker.count())
	}
}

func TestOnlyALeadThatAcceptsWorkersCommandsThem(t *testing.T) {
	app := newLeadApp(t)
	worker := newFakeDockerWorker(t)
	enrolWorker(t, app, "garage", worker.srv.URL, nil)
	id := workerID(t, app, "garage")
	c := app.cfg.Config()
	c.Mode = config.ModeFollower
	if err := app.cfg.Save(c); err != nil {
		t.Fatal(err)
	}
	if _, err := app.CommandWorkerCopy(id, "plain", "stop", 1); err == nil || !strings.Contains(err.Error(), "joined") {
		t.Fatalf("joined: %v", err)
	}
	if app.workerTargets() != nil {
		t.Fatal("a joined computer offers no workers in the Where box")
	}
	c.Mode = config.ModeLead
	c.FleetServerEnabled = false
	if err := app.cfg.Save(c); err != nil {
		t.Fatal(err)
	}
	if _, err := app.CommandWorkerCopy(id, "plain", "stop", 1); err == nil || !strings.Contains(err.Error(), "Accept workers") {
		t.Fatalf("hub off: %v", err)
	}
	if worker.count() != 0 {
		t.Fatal("no command may reach the worker")
	}
}

// verifyApproval is the worker's check (web app/step_up.py check): the MAC over the
// body with the key derived from the worker's own key, and the action and target in
// the payload.
func verifyApproval(workerKey, token, target string) bool {
	body, mac, ok := strings.Cut(token, ".")
	if !ok {
		return false
	}
	derive := hmac.New(sha256.New, []byte(workerKey))
	derive.Write([]byte("cashpilot-step-up-v1"))
	m := hmac.New(sha256.New, derive.Sum(nil))
	m.Write([]byte(body))
	if !hmac.Equal([]byte(hex.EncodeToString(m.Sum(nil))), []byte(mac)) {
		return false
	}
	raw, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return false
	}
	var payload struct {
		A, T string
		Exp  int64
	}
	if json.Unmarshal(raw, &payload) != nil {
		return false
	}
	return payload.A == workerapi.ActionCustomImage && payload.T == target && payload.Exp > time.Now().Unix()
}

func TestAWorkerThatHasNotUsedItsKeyYetGetsTheSharedKey(t *testing.T) {
	app := newLeadApp(t)
	worker := newFakeDockerWorker(t)
	// One heartbeat: the key is issued, but the worker has not sent it back yet, so it
	// may not hold it and still takes the shared key, as the web UI assumes.
	w := postHeartbeat(t, app, map[string]any{"name": "fresh", "url": worker.srv.URL, "system_info": map[string]any{"os": "Linux"}})
	if w.Code != http.StatusOK {
		t.Fatalf("enrol: %d", w.Code)
	}
	id := workerID(t, app, "fresh")
	if _, err := app.CommandWorkerCopy(id, "plain", "start", 1); err != nil {
		t.Fatal(err)
	}
	if got := worker.last(t).Auth; got != "Bearer fleet-key" {
		t.Fatalf("Authorization = %q, want the shared key", got)
	}
	// It cannot check an approval yet, so a deploy that needs one says to wait.
	_, err := app.DeployToWorker("vault", WorkerDeployRequest{WorkerID: id, Image: "mine/vault:9", Confirmed: true})
	if err == nil || !strings.Contains(err.Error(), "has not finished enrolling") || worker.count() != 1 {
		t.Fatalf("err = %v, calls %d", err, worker.count())
	}
}

func TestWorkerDeploySavesTypedCredentialsAndKeepsOmittedSettings(t *testing.T) {
	app := newLeadApp(t)
	worker := newFakeDockerWorker(t)
	enrolWorker(t, app, "garage", worker.srv.URL, nil)
	if err := app.store.SaveCredentials("plain", map[string]string{
		"TOKEN": "previous-token", "DEVICE_NAME": "my-worker",
	}); err != nil {
		t.Fatal(err)
	}
	result, err := app.DeployToWorker("plain", WorkerDeployRequest{
		WorkerID: workerID(t, app, "garage"), Values: map[string]string{"TOKEN": "replacement-token"},
	})
	if err != nil || result.ContainerID == "" || result.CredentialWarning != "" {
		t.Fatalf("worker deploy failed: %v", err)
	}
	saved, err := app.store.GetCredentials("plain")
	if err != nil || saved["TOKEN"] != "replacement-token" || saved["DEVICE_NAME"] != "my-worker" {
		t.Fatalf("the worker deploy did not save the new token and keep the device name: %v", err)
	}
}
