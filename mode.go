package main

import (
	"context"
	"errors"
	"fmt"
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
	HubDevices []string `json:"hubDevices"`
}

// GetModeState returns the mode, the pairing status and what a switch affects.
func (a *App) GetModeState() (ModeState, error) {
	if err := a.ready(); err != nil {
		return ModeState{}, err
	}
	cfg := a.cfg.Config()
	return ModeState{
		Mode:       cfg.Mode,
		Name:       a.upstreamPayload().Name,
		Pairing:    a.pairingStatus(),
		HubOn:      cfg.FleetServerEnabled,
		HubDevices: a.hubDeviceNames(),
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
	if a.joined() {
		return JoinResult{}, errors.New("this computer is already joined to a CashPilot; switch to Run everything here first")
	}
	payload := a.upstreamPayload()
	out := JoinResult{Name: payload.Name, HubDevices: []string{}}
	target, err := upstream.ParseJoin(req.Block, req.URL, req.Key)
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

	ctx := a.ctx
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

	// Accepted. Save the link: the key first, so a config that names a server
	// always has its key.
	a.stopUpstream()
	if err := config.SetUpstreamEnrolmentKey(a.cfg.AppDir(), target.Key); err != nil {
		return JoinResult{}, fmt.Errorf("saving the key: %w", err)
	}
	// The key the server just issued, or none: a key another server issued means
	// nothing to this one.
	workerKey := strings.TrimSpace(resp.WorkerKey)
	if err := config.SetUpstreamWorkerKey(a.cfg.AppDir(), workerKey); err != nil {
		return JoinResult{}, fmt.Errorf("saving this computer's own key: %w", err)
	}
	a.upstream.mu.Lock()
	a.upstream.workerKey = workerKey
	a.upstream.mu.Unlock()
	out.HubWasOn = a.cfg.Config().FleetServerEnabled
	if out.HubWasOn {
		out.HubDevices = a.hubDeviceNames()
	}
	if err := a.cfg.Update(func(c *config.AppConfig) {
		c.UpstreamURL = target.URL
		c.Mode = config.ModeFollower
		c.FleetServerEnabled = false
	}); err != nil {
		return JoinResult{}, err
	}
	a.recordPairing(target.URL, upstream.Failure{})
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
}

// SwitchToLead makes a joined computer run everything itself again. It stops the
// heartbeats, clears the address and both keys, and starts local earnings
// collection at once. The earners keep running. The server keeps this computer's
// row until the user removes it there: no worker route can remove it.
func (a *App) SwitchToLead() (LeaveResult, error) {
	if err := a.ready(); err != nil {
		return LeaveResult{}, err
	}
	if !a.joined() {
		return LeaveResult{}, errors.New("this computer already runs everything here")
	}
	out := LeaveResult{
		ServerURL: strings.TrimRight(strings.TrimSpace(a.cfg.Config().UpstreamURL), "/"),
		Name:      a.upstreamPayload().Name,
	}
	a.stopUpstream()
	if err := config.SetUpstreamWorkerKey(a.cfg.AppDir(), ""); err != nil {
		return LeaveResult{}, fmt.Errorf("clearing this computer's own key: %w", err)
	}
	if err := config.SetUpstreamEnrolmentKey(a.cfg.AppDir(), ""); err != nil {
		return LeaveResult{}, fmt.Errorf("clearing the key: %w", err)
	}
	a.upstream.mu.Lock()
	a.upstream.workerKey, a.upstream.statusURL = "", ""
	a.upstream.mu.Unlock()
	if err := a.cfg.Update(func(c *config.AppConfig) {
		c.UpstreamURL = ""
		c.Mode = config.ModeLead
	}); err != nil {
		return LeaveResult{}, err
	}
	// Collect now rather than at the next tick, which can be an hour away.
	if a.ctx != nil {
		a.runScheduler(a.ctx, a.collectInterval())
	}
	return out, nil
}
