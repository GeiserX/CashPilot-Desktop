package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/config"
	"github.com/GeiserX/CashPilot-Desktop/internal/upstream"
)

// How this computer runs: a lead ("Run everything here") collects the earnings,
// shows the dashboard and runs its earners; a follower ("Join my CashPilot") runs
// earners for a CashPilot server, which collects and shows the earnings. The first
// run asks; Settings switches. config.AppConfig.Mode holds the answer.
//
// While joined, this computer does not log in to the vendor accounts: the server
// does, and both doing it would sign in to the same accounts from two places.
// That is the recommended answer to an open owner decision (O3).

// joinTimeout bounds the one live heartbeat a join sends, so a server that takes
// the connection and never answers does not hold the join screen.
const joinTimeout = 20 * time.Second

// errJoinedNoCollect is what a manual Collect answers while joined.
var errJoinedNoCollect = errors.New("this computer is joined to your CashPilot, which reads the earnings; this computer does not log in to the provider accounts while joined")

// refreshConfig reads the config file again if the other process (the window
// or the background helper) saved a change, so a join or a switch made in one
// holds in both. A file that cannot be read leaves the config as it was.
func (a *App) refreshConfig() {
	if a.cfg == nil {
		return
	}
	if _, err := a.cfg.Reload(); err != nil {
		log.Printf("config: could not read the saved config again: %v", err)
	}
}

// joined reports whether this computer runs as a follower of a CashPilot server.
func (a *App) joined() bool {
	return a.cfg != nil && a.cfg.Config().Mode == config.ModeFollower
}

// PairingStatus is the link to the CashPilot server, as the last heartbeat left it.
type PairingStatus struct {
	ServerURL string `json:"serverUrl"`
	// LastHeartbeat is when the server last accepted a heartbeat, RFC3339; "" never.
	LastHeartbeat string `json:"lastHeartbeat"`
	// LastAttempt is when the last heartbeat was tried, RFC3339; "" not yet.
	LastAttempt string `json:"lastAttempt"`
	// LastError is why the last attempt failed, in plain words; "" when it worked.
	LastError string `json:"lastError"`
	ErrorCode string `json:"errorCode"`
}

// pairingStatus is nil unless this computer is joined.
func (a *App) pairingStatus() *PairingStatus {
	if !a.joined() {
		return nil
	}
	serverURL := strings.TrimRight(strings.TrimSpace(a.cfg.Config().UpstreamURL), "/")
	out := &PairingStatus{ServerURL: serverURL}
	if p, err := a.cfg.Pairing(); err == nil && p.Suspended {
		out.ErrorCode, out.LastError = "unauthorized", refusedPairingMessage
		return out
	}
	a.upstream.mu.Lock()
	defer a.upstream.mu.Unlock()
	if a.upstream.statusURL != serverURL {
		return out
	}
	stamp := func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		return t.Format(time.RFC3339)
	}
	out.LastHeartbeat, out.LastAttempt = stamp(a.upstream.lastOK), stamp(a.upstream.lastAttempt)
	out.LastError, out.ErrorCode = a.upstream.lastFailure.Message, a.upstream.lastFailure.Code
	return out
}

// ModeState is what the Settings card "How this computer runs" shows.
type ModeState struct {
	// Mode is "lead", "follower", or "" before the first run chose.
	Mode string `json:"mode"`
	// Name is this computer's name on the CashPilot Fleet page.
	Name    string         `json:"name"`
	Pairing *PairingStatus `json:"pairing"`
	// HubOn is the "Accept workers" switch. Joining turns it off.
	HubOn bool `json:"hubOn"`
	// HubDevices are the workers and phones that report to this computer, which
	// must be pointed at the CashPilot server once this computer joins it.
	HubDevices      []string `json:"hubDevices"`
	FirewallCleanup string   `json:"firewallCleanup"`
}

// GetModeState returns the mode, the pairing status and what a switch affects.
func (a *App) GetModeState() (ModeState, error) {
	if err := a.ready(); err != nil {
		return ModeState{}, err
	}
	a.refreshConfig()
	cfg := a.cfg.Config()
	return ModeState{
		Mode:            cfg.Mode,
		Name:            a.upstreamPayload().Name,
		Pairing:         a.pairingStatus(),
		HubOn:           cfg.FleetServerEnabled,
		HubDevices:      a.hubDeviceNames(),
		FirewallCleanup: a.firewallCleanupStatus(),
	}, nil
}

func (a *App) hubDeviceNames() []string {
	names := []string{}
	for _, d := range a.store.ListFleetDevices() {
		if n := strings.TrimSpace(d.Name); n != "" {
			names = append(names, n)
		}
	}
	return names
}

// StartAsLead is the first run's "Run everything here".
func (a *App) StartAsLead() error {
	if err := a.ready(); err != nil {
		return err
	}
	if a.cfg.Config().FirstRunComplete {
		return errors.New("the first run is over; switch in Settings, under How this computer runs")
	}
	if a.joined() {
		// The user joined, then came back to the choice: leave the server first.
		if _, err := a.SwitchToLead(); err != nil {
			return err
		}
		return nil
	}
	return a.cfg.Update(func(c *config.AppConfig) { c.Mode = config.ModeLead })
}

// JoinRequest is what the join screen sends: the pasted "Add a box" block, or the
// address and key typed in two fields.
type JoinRequest struct {
	Block string `json:"block"`
	URL   string `json:"url"`
	Key   string `json:"key"`
}

// JoinResult is the answer to one join. A failed connection is not an error: OK
// is false and Code and Message say why, so the screen can keep the user on it.
type JoinResult struct {
	OK      bool   `json:"ok"`
	Code    string `json:"code"`
	Message string `json:"message"`
	// ServerURL and ServerHost are the server joined.
	ServerURL  string `json:"serverUrl"`
	ServerHost string `json:"serverHost"`
	// Name is this computer's name on the server's Fleet page.
	Name string `json:"name"`
	// WorkerID is the server's id for this computer.
	WorkerID int64 `json:"workerId"`
	// HubWasOn is true when joining turned off the "Accept workers" switch, and
	// HubDevices lists the workers and phones to point at the server instead.
	HubWasOn   bool     `json:"hubWasOn"`
	HubDevices []string `json:"hubDevices"`
}

// JoinCashPilot makes this computer a follower of a CashPilot server. It reads the
// request, sends ONE live heartbeat, and saves the address and key only when the
// server accepted it. Joining stops local earnings collection and turns the hub
// off; the earners keep running, and the earnings history here is copied to the
// server once, after it confirms this computer (pushHistoryOnce).
func (a *App) JoinCashPilot(req JoinRequest) (JoinResult, error) {
	if err := a.ready(); err != nil {
		return JoinResult{}, err
	}
	a.pairingMu.Lock()
	defer a.pairingMu.Unlock()
	a.refreshConfig()
	current, key, err := a.cfg.ReadPairingCredential(false)
	if err != nil && current.Revision == "" {
		return JoinResult{}, err
	}
	if a.pairingServer(current) != "" && !current.Suspended && (current.Revision == "" || key.Worker != "") {
		return JoinResult{}, errors.New("this computer is already joined to a CashPilot; switch to Run everything here first")
	}
	return a.enrolPairing(a.ctx, req, current, "")
}

// enrolPairing is the window's only enrolment entry point. An upgrade can
// present an existing worker key to the server in place of the shared key.
func (a *App) enrolPairing(ctx context.Context, req JoinRequest, current config.Pairing, worker string) (JoinResult, error) {
	payload := a.upstreamPayload()
	out := JoinResult{Name: payload.Name, HubDevices: []string{}}
	inputKey := req.Key
	if worker != "" {
		inputKey = worker
	}
	target, err := upstream.ParseJoin(req.Block, req.URL, inputKey)
	if worker == "" {
		req.Key = target.Key
	}
	if err != nil {
		out.Code, out.Message = "input", err.Error()
		return out, nil
	}
	out.ServerURL, out.ServerHost = target.URL, upstream.Host(target.URL)
	if err := upstream.ValidateServerURL(target.URL); err != nil {
		f := upstream.ExplainAddress(err)
		out.Code, out.Message = f.Code, f.Message
		return out, nil
	}

	if ctx == nil {
		ctx = context.Background()
	}
	sendCtx, cancel := context.WithTimeout(ctx, joinTimeout)
	defer cancel()
	resp, err := upstream.New().Send(sendCtx, target.URL, target.Key, payload)
	if err == nil && strings.TrimSpace(resp.Status) == "" && resp.WorkerID == 0 {
		// Valid JSON, but not a heartbeat reply.
		err = upstream.ErrNotCashPilot
	}
	if err != nil {
		f := upstream.Explain(err, out.ServerHost, payload.Name)
		out.Code, out.Message = f.Code, f.Message
		return out, nil
	}

	out.HubWasOn = a.cfg.Config().FleetServerEnabled
	if out.HubWasOn && (current.Revision != "" || !a.joined()) {
		out.HubDevices = a.hubDeviceNames()
		if err := a.cfg.Update(func(c *config.AppConfig) { c.FleetServerEnabled = false }); err != nil {
			return JoinResult{}, err
		}
	}
	if key := strings.TrimSpace(resp.WorkerKey); key != "" {
		worker = key
	}
	if worker == req.Key {
		worker = ""
	}
	if err := a.cfg.PublishPairing(current.Revision, target.URL, false, config.PairingCredential{Worker: worker, Enrolment: req.Key}); err != nil {
		return JoinResult{}, fmt.Errorf("saving the pairing: %w", err)
	}
	a.stopUpstream()
	a.recordPairing(target.URL, upstream.Failure{})
	// Before the loop's first heartbeat, so it already reports the API's address.
	a.syncWorkerAPI()
	if a.ctx != nil {
		a.startUpstream(a.ctx)
	}
	out.OK, out.WorkerID = true, resp.WorkerID
	return out, nil
}

// LeaveResult says what to do on the server after this computer stopped following it.
type LeaveResult struct {
	ServerURL string `json:"serverUrl"`
	// Name is the row to remove on the server's Fleet page.
	Name string `json:"name"`
	// FirewallError says why the firewall rule CashPilot made for the server's
	// commands is still there, "" when it was removed or there was none.
	FirewallError string `json:"firewallError"`
}

// Explicit changes use the durable address even if config.json could not reload.
func (a *App) pairingServer(p config.Pairing) string {
	if p.Revision != "" {
		return p.ServerURL
	}
	return a.cfg.Config().UpstreamURL
}

func (a *App) saveSettingsPairing(address string, hasAddress bool, key string, hasKey bool) (bool, error) {
	a.refreshConfig()
	current, stored, keyErr := a.cfg.ReadPairingCredential(false)
	if keyErr != nil && current.Revision == "" {
		return false, keyErr
	}
	before := a.pairingServer(current)
	if !hasAddress {
		address = before
	}
	if address != before && address != "" {
		if err := upstream.ValidateServerURL(address); err != nil {
			return false, fmt.Errorf("pairing address: %w", err)
		}
	}
	if address == before && !hasKey {
		return false, nil
	}
	if keyErr != nil && !hasKey && address != "" && before == "" {
		return false, keyErr
	}
	if current.Revision == "" {
		stored.Enrolment, _ = config.LegacyEnrolmentKey(a.cfg.AppDir())
	}
	if !hasKey && before == "" {
		key = stored.Enrolment
	}
	if address == before && key == stored.Enrolment && !current.Suspended && stored.Worker != "" {
		return false, nil
	}
	if address != "" && key != "" {
		out, err := a.enrolPairing(a.ctx, JoinRequest{URL: address, Key: key}, current, "")
		if err == nil && !out.OK {
			err = errors.New(out.Message)
		}
		return false, err
	}
	left := address == "" && before != ""
	if left {
		key = ""
	}
	if err := a.cfg.PublishPairing(current.Revision, address, left && firewallRuleNeeded, config.PairingCredential{Enrolment: key}); err != nil {
		return false, err
	}
	a.stopUpstream()
	a.recordPairing("", upstream.Failure{})
	a.syncWorkerAPI()
	return left, nil
}

// SwitchToLead stops command authorization and records firewall removal in
// the same write. Only a Windows read can confirm the removal.
func (a *App) SwitchToLead() (LeaveResult, error) {
	if err := a.ready(); err != nil {
		return LeaveResult{}, err
	}
	a.pairingMu.Lock()
	defer a.pairingMu.Unlock()
	a.refreshConfig()
	p, err := a.cfg.Pairing()
	if err != nil {
		return LeaveResult{}, err
	}
	out := LeaveResult{ServerURL: a.pairingServer(p), Name: a.upstreamPayload().Name}
	if out.ServerURL == "" {
		if !firewallRuleNeeded {
			return out, errors.New("this computer already runs everything here")
		}
	} else if _, err := a.saveSettingsPairing("", true, "", true); err != nil {
		return out, err
	}
	if err := a.removeServerCommandsRule(); err != nil {
		out.FirewallError = err.Error()
	}
	if out.ServerURL != "" && a.ctx != nil {
		a.runScheduler(a.ctx, a.collectInterval())
	}
	return out, nil
}

// enrolSavedPairing runs in the window on upgrade. The helper can report with
// legacy credentials but cannot publish any key it receives.
func (a *App) enrolSavedPairing(ctx context.Context) {
	a.pairingMu.Lock()
	defer a.pairingMu.Unlock()
	p, _, err := a.cfg.ReadPairingCredential(true)
	if err != nil || p.Revision != "" || !a.joined() {
		return
	}
	url := a.cfg.Config().UpstreamURL
	enrol, err := config.LegacyEnrolmentKey(a.cfg.AppDir())
	if err != nil {
		return
	}
	worker, err := config.LegacyWorkerKey(a.cfg.AppDir(), url)
	if err != nil || (enrol == "" && worker == "") {
		return
	}
	if out, err := a.enrolPairing(ctx, JoinRequest{URL: url, Key: enrol}, p, worker); err != nil {
		a.emitError("pairing", err)
	} else if !out.OK {
		a.recordPairing(url, upstream.Failure{Code: out.Code, Message: out.Message})
	}
}
