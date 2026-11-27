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
	// A suspended revision that a 1.x save superseded is not the pairing shown:
	// the address waiting in config.json has its own status.
	// A suspended pairing still shows when its last heartbeat got through, so the
	// refusal does not read as if none ever did.
	suspended := false
	if p, err := a.cfg.Pairing(); err == nil && p.Suspended && !a.cfg.PairingSuperseded(p) {
		suspended = true
		out.ErrorCode, out.LastError = "unauthorized", refusedPairingMessage
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
	if !suspended {
		out.LastError, out.ErrorCode = a.upstream.lastFailure.Message, a.upstream.lastFailure.Code
	}
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
	// ServerCommandsOn is the server commands switch after the join: on for a
	// first join, off when the join carried a 1.x pairing over.
	ServerCommandsOn bool `json:"serverCommandsOn"`
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
	if a.pairingServer(current, errors.Is(err, config.ErrPairingSuperseded)) != "" && !current.Suspended && (current.Revision == "" || key.Worker != "") {
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
	// No revision yet and a server address in config.json: this enrolment carries
	// a 1.x pairing over. Server commands did not exist in 1.x, and an upgrade is
	// not a choice to allow them, so they start off unless the user already used
	// the switch. Written before the pairing is published, so neither the window
	// nor the helper listens in between. A fresh join has no saved address and
	// keeps the default (on). A 1.x pairing made after a downgrade has a revision:
	// enrolSavedPairing turns the switch off itself before it calls this.
	if current.Revision == "" && a.pairingServer(current, false) != "" {
		if err := a.cfg.Update(func(c *config.AppConfig) {
			if !c.ServerCommandsChosen {
				c.ServerCommandsOff = true
			}
		}); err != nil {
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
	out.ServerCommandsOn = !a.cfg.Config().ServerCommandsOff
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

// pairingServer is the address an explicit change starts from. A live revision
// gives its own address, even if config.json could not reload. A revision that
// a 1.x save superseded (config.ErrPairingSuperseded) gives the address in
// config.json instead: that is what the window shows, and what Leave and a key
// typed alone in Settings act on, also when the revision itself has no server.
func (a *App) pairingServer(p config.Pairing, superseded bool) string {
	if p.Revision != "" && !superseded {
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
	before := a.pairingServer(current, errors.Is(keyErr, config.ErrPairingSuperseded))
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
	// A superseded revision's own server is left too: a 1.x Leave carried over
	// at the start (enrolSavedPairing) has no address in config.json.
	left := address == "" && (before != "" || current.ServerURL != "")
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
	p, _, err := a.cfg.ReadPairingCredential(false)
	if err != nil && p.Revision == "" {
		return LeaveResult{}, err
	}
	out := LeaveResult{ServerURL: a.pairingServer(p, errors.Is(err, config.ErrPairingSuperseded)), Name: a.upstreamPayload().Name}
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

// enrolSavedPairing runs in the window at every start. It carries a 1.x
// pairing over on the first upgrade, when no revision exists yet. It does it
// again after a downgrade, when 1.x saved config.json over a revision
// (config.ErrPairingSuperseded): the 1.x choice wins. A 1.x join, to the same
// server or another, is carried over as on the first upgrade, and that
// publication retires the old revision's credential and the 1.x credentials. A
// 1.x Leave is published. A 1.x save that kept the server keeps the revision.
// When a server stays, server commands start off, because 1.x dropped the
// switch. A 1.x address with no key waits, unused, for the user; Leave and a
// key typed in Settings act on that address (pairingServer). A key that
// cannot be read (a locked keychain) waits for the next start. The helper can
// report with legacy credentials before the first upgrade, but it cannot
// publish any key it receives, and it does not report on a superseded revision.
// A 2.0 development build from before the marker (never released) also left
// no marker, and its state matches a 1.x save: such an install may lose its
// pairing at this start and must join again.
func (a *App) enrolSavedPairing(ctx context.Context) {
	a.pairingMu.Lock()
	defer a.pairingMu.Unlock()
	p, _, err := a.cfg.ReadPairingCredential(true)
	superseded := errors.Is(err, config.ErrPairingSuperseded)
	if !superseded && (err != nil || p.Revision != "") {
		return
	}
	if !a.joined() {
		if !superseded {
			return
		}
		if p.ServerURL == "" {
			a.keepPairing(p)
		} else if _, err := a.saveSettingsPairing("", true, "", true); err != nil {
			a.emitError("pairing", err)
		} else if a.ctx != nil {
			a.runScheduler(a.ctx, a.collectInterval())
		}
		return
	}
	url := a.cfg.Config().UpstreamURL
	enrol, err := config.LegacyEnrolmentKey(a.cfg.AppDir())
	if err != nil {
		return
	}
	worker, err := config.LegacyWorkerKey(a.cfg.AppDir(), url)
	if err != nil {
		return
	}
	carry := enrol != "" || worker != ""
	if superseded {
		if !carry && url != p.ServerURL {
			// 1.x points at another server with no key, as 1.x showed it. Nothing
			// reports to either server until the user joins or leaves here.
			a.recordPairing(url, upstream.Failure{Code: "no-key", Message: "No key is available for your CashPilot. Enter the enrolment key in Settings or join again."})
			return
		}
		if err := a.cfg.Update(func(c *config.AppConfig) {
			if !c.ServerCommandsChosen {
				c.ServerCommandsOff = true
			}
		}); err != nil {
			a.emitError("pairing", err)
			return
		}
		if !carry {
			a.keepPairing(p)
			return
		}
	} else if !carry {
		return
	}
	if out, err := a.enrolPairing(ctx, JoinRequest{URL: url, Key: enrol}, p, worker); err != nil {
		a.emitError("pairing", err)
	} else if !out.OK {
		a.recordPairing(url, upstream.Failure{Code: out.Code, Message: out.Message})
	}
}

// keepPairing ends the wait after a 1.x save that left the pairing as it was.
func (a *App) keepPairing(p config.Pairing) {
	if err := a.cfg.KeepPairing(p.Revision); err != nil {
		a.emitError("pairing", err)
		return
	}
	a.syncWorkerAPI()
}
