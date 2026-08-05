// Package upstream lets this Desktop optionally report to a CashPilot server as
// a worker.
//
// # WHY THIS EXISTS
//
// Desktop was only ever a hub. fleet_server.go RECEIVES heartbeats at
// /api/workers/heartbeat, runs its own collectors, keeps its own store and
// exports its own fleet metrics — and there was no outbound direction at all.
// So anyone running both Desktop and a CashPilot server owned two hubs that
// each believed they were the source of truth, with two separate earnings
// histories and no way to reconcile them. Adding the machine to the fleet meant
// turning Desktop into a duplicate, or not adding it.
//
// The shape chosen is the one the user asked for: "like another worker but it
// can also host native apps, just like cashpilot-android but even better".
// Standalone stays the default and loses nothing — an empty URL means not
// paired, and nothing here runs.
//
// # WHAT THIS DELIBERATELY DOES NOT DO
//
// It does not touch earnings. The server collects them centrally from provider
// accounts and returns them in the heartbeat RESPONSE; a worker only reports
// what it is running. So pairing does not migrate, delete or duplicate
// Desktop's local earnings history — that question is genuinely open (see the
// pairing-history bead) and this package is careful not to pre-empt it.
package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/fleetnet"
)

// Payload is the body POST /api/workers/heartbeat expects.
//
// Field names and shapes are the SERVER's, not Desktop's own inbound
// workerHeartbeat — those differ and conflating them would fail at the wire.
// In particular the server declares `apps` and `containers` as lists of
// OBJECTS, while Desktop's inbound struct types Apps as []string; sending that
// shape earns a 422. The server discovers what a worker runs via
// containers[*].slug, which is why Container carries one.
type Payload struct {
	Name       string     `json:"name"`
	URL        string     `json:"url"`
	ClientID   string     `json:"client_id"`
	Containers []Item     `json:"containers"`
	Apps       []Item     `json:"apps"`
	SystemInfo SystemInfo `json:"system_info"`
}

// Item is one thing this machine is running, in the server's shape.
type Item struct {
	Slug   string `json:"slug"`
	Name   string `json:"name,omitempty"`
	Status string `json:"status,omitempty"`
}

// SystemInfo describes the host.
//
// Version is read by the server to decide whether this worker is on a different
// release series from the UI. It is omitted when empty ON PURPOSE: an absent
// version must read as unknown, never as a match — the server's own rule is
// that both sides must be known releases before it calls anything a mismatch.
type SystemInfo struct {
	OS         string `json:"os,omitempty"`
	Arch       string `json:"arch,omitempty"`
	Hostname   string `json:"hostname,omitempty"`
	DeviceType string `json:"device_type,omitempty"`
	Version    string `json:"version,omitempty"`
}

// Response is what the server sends back.
//
// WorkerKey is present only while enrolling: on first contact the server mints
// this worker's own key and returns it once, then re-delivers it on every
// heartbeat until the worker proves receipt by authenticating with it. Once
// confirmed the field is absent, and the shared enrolment key is refused.
type Response struct {
	Status    string          `json:"status"`
	WorkerID  int64           `json:"worker_id"`
	WorkerKey string          `json:"worker_key"`
	Earnings  json.RawMessage `json:"earnings,omitempty"`
}

// ErrNotPaired is returned when no upstream server is configured. It is a
// normal state, not a failure: standalone is the default.
var ErrNotPaired = errors.New("upstream: not paired with a CashPilot server")

// AuthToken picks the credential for the next heartbeat.
//
// The per-worker key wins whenever we hold one; the shared enrolment key is
// only ever used to bootstrap. That ordering is the whole point of per-worker
// keys — once enrolled, the shared key is refused for this worker, so
// presenting it after enrolment would lock the machine out of its own fleet.
func AuthToken(workerKey, enrolmentKey string) string {
	if k := strings.TrimSpace(workerKey); k != "" {
		return k
	}
	return strings.TrimSpace(enrolmentKey)
}

// KeyToPersist reports the per-worker key to newly store, or "" for no change.
//
// Pure, so the enrolment rule is unit-tested without a server. Three cases the
// caller must not get wrong:
//
//   - the server sent nothing: keep what we have. A heartbeat that omits the key
//     means we are already enrolled and confirmed.
//   - the server sent the key we already hold: no write. Rewriting it every
//     heartbeat would churn the keychain for no reason.
//   - the server sent a different key: store it. This covers the re-issue path,
//     where a dropped enrolment response would otherwise lock the worker out.
func KeyToPersist(current, received string) string {
	received = strings.TrimSpace(received)
	if received == "" || received == strings.TrimSpace(current) {
		return ""
	}
	return received
}

// Client posts heartbeats to a CashPilot server.
type Client struct {
	// HTTP is the transport. Injectable so tests drive a stub server.
	HTTP *http.Client
	// Policy is the SSRF policy applied to the upstream URL before dialing.
	// The URL is user-supplied and the request carries a bearer token, so it
	// gets the same validation as any other outbound worker call rather than a
	// weaker one just because the user typed it into a different box.
	Policy fleetnet.Policy
}

// New returns a Client with a sane timeout.
//
// The timeout matters more than it looks: heartbeats are on a ticker, so a
// server that accepts connections and never answers would otherwise pile up
// goroutines for the lifetime of the process.
func New(policy fleetnet.Policy) *Client {
	return &Client{HTTP: &http.Client{Timeout: 30 * time.Second}, Policy: policy}
}

// Send posts one heartbeat and returns the server's response.
//
// serverURL is the CashPilot UI base URL; token is whatever [AuthToken] chose.
func (c *Client) Send(ctx context.Context, serverURL, token string, p Payload) (*Response, error) {
	serverURL = strings.TrimRight(strings.TrimSpace(serverURL), "/")
	if serverURL == "" {
		return nil, ErrNotPaired
	}
	if strings.TrimSpace(token) == "" {
		// Refuse rather than send an unauthenticated heartbeat: the server would
		// reject it, and a blank token here means the credential was lost, which
		// is worth surfacing as an error the UI can show.
		return nil, errors.New("upstream: no credential to authenticate with")
	}
	if err := fleetnet.ValidateWorkerURL(serverURL, c.Policy); err != nil {
		return nil, fmt.Errorf("upstream: refusing to contact %s: %w", serverURL, err)
	}

	body, err := json.Marshal(p)
	if err != nil {
		return nil, fmt.Errorf("upstream: encoding heartbeat: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serverURL+"/api/workers/heartbeat", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("upstream: building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upstream: heartbeat failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Bounded read: the response is small, and an unbounded io.ReadAll against a
	// server that is not the one we think it is would happily consume memory.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("upstream: reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		// The status is carried in the message because 401 and 422 mean very
		// different things to a user: the first is a wrong or revoked key, the
		// second is a payload this server's version does not accept.
		return nil, fmt.Errorf("upstream: server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out Response
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("upstream: decoding response: %w", err)
	}
	return &out, nil
}
