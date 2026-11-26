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
// # WHAT A HEARTBEAT DOES NOT CARRY
//
// Ongoing earnings. The server collects those centrally from provider accounts
// and returns them in the heartbeat RESPONSE; a worker only reports what it is
// running. The history a Desktop collected BEFORE it was paired is a separate,
// one-time hand-over — see import.go, which copies it and leaves the local rows
// untouched.
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

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// Payload is the body POST /api/workers/heartbeat expects.
//
// Field names and shapes are the SERVER's, not Desktop's own inbound
// workerHeartbeat — those differ and conflating them would fail at the wire.
// In particular the server declares `apps` and `containers` as lists of
// OBJECTS; a list of strings earns a 422. The server discovers what a worker runs via
// containers[*].slug, which is why Container carries one.
type Payload struct {
	Name       string      `json:"name"`
	URL        string      `json:"url"`
	ClientID   string      `json:"client_id"`
	Containers []Container `json:"containers"`
	Apps       []Item      `json:"apps"`
	SystemInfo SystemInfo  `json:"system_info"`
}

// Item is one thing this machine is running, in the server's shape.
type Item struct {
	Slug   string `json:"slug"`
	Name   string `json:"name,omitempty"`
	Status string `json:"status,omitempty"`
}

// Container is one copy of a service, in the web worker's shape
// (orchestrator.get_status). The server folds the counters into its usage rows
// (usage.samples_from_heartbeat), and its stall check reads those rows, so the
// names and units are the web worker's: net_rx_bytes and net_tx_bytes are the
// cumulative bytes since the container started, keyed by container_id, the
// 12-character short id. A counter that was not read goes out as null, never
// 0: a stopped copy and a host-network one have none, as on the web.
type Container struct {
	Slug        string `json:"slug"`
	Name        string `json:"name,omitempty"`
	Status      string `json:"status,omitempty"`
	ContainerID string `json:"container_id,omitempty"`
	// Instance is which copy of its service this is, 1 for the first. Without it
	// the server reads every copy as copy 1.
	Instance   int      `json:"instance,omitempty"`
	CPUPercent *float64 `json:"cpu_percent"`
	MemoryMB   *float64 `json:"memory_mb"`
	NetRxBytes *uint64  `json:"net_rx_bytes"`
	NetTxBytes *uint64  `json:"net_tx_bytes"`
}

// Proxied is one copy that leaves through a proxy, in the web worker's shape
// (proxy_tunnel.status). It is kept out of Containers, as on the web: it leaves
// from another IP and is not a copy of the service. Its counters are its
// tunnel's, which carries every byte the earner moves.
//
// It has no proxy_id. This computer numbers its proxies itself, and the server
// reads proxy_id as one of its own proxies: it would count this copy's traffic
// against that proxy, and stop the copy when that proxy hits its cap, and
// read its logs, all through calls this computer does not serve. Without the id
// the server skips the copy everywhere it needs one, and still groups it by exit.
type Proxied struct {
	Slug              string  `json:"slug"`
	Name              string  `json:"name"`
	Status            string  `json:"status"`
	ContainerID       string  `json:"container_id"`
	TunnelStatus      string  `json:"tunnel_status"`
	TunnelContainerID string  `json:"tunnel_container_id"`
	TunnelStartedAt   string  `json:"tunnel_started_at"`
	NetRxBytes        *uint64 `json:"net_rx_bytes"`
	NetTxBytes        *uint64 `json:"net_tx_bytes"`
	ExitIP            string  `json:"exit_ip,omitempty"`
	ExitCountry       string  `json:"exit_country,omitempty"`
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
	// EgressIP is this machine's public IPv4. The server hands it to the fleet's other
	// boxes as fleet_exits, and each of their LAN fences drops it, so an earner there
	// cannot reach this line's port forwards. Omitted when unknown: the server then
	// keeps the last address it had.
	EgressIP string `json:"egress_ip,omitempty"`
	// Proxied is this machine's proxied copies: an empty list when it has none,
	// null when they could not be read, as the web worker sends it. A null makes
	// the server treat this machine's list as partial, so it clears no alert of
	// a copy the list may leave out.
	Proxied []Proxied `json:"proxied"`
	// ContainersListed is false when Containers is not this machine's whole list:
	// the live read failed or the other process held its turn, so only the stored
	// first copies went out. The server then retires no device id of a copy the
	// list leaves out, as it does for a web worker that sends the same flag.
	ContainersListed *bool `json:"containers_listed,omitempty"`
	// GPUGrants is the GPU each catalog entry gets here (catalog.GPUGrants), as the
	// web worker reports it. The server deploys a GPU entry to this computer only
	// when its own catalog's grant matches this one.
	GPUGrants map[string]catalog.GPUSpec `json:"gpu_grants"`
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
	// FleetExits is the public IPv4 of every box of the fleet, read by ParseFleetExits.
	FleetExits json.RawMessage `json:"fleet_exits,omitempty"`
	// Features names what the server can do, read by TakesCommands. A server from
	// before the list sends no key.
	Features json.RawMessage `json:"features,omitempty"`
}

// CommandsFeature is what a CashPilot server lists in Features when it may send
// this computer commands: web v3.0.0 and newer, which redeploys a running copy only
// after the owner approves the replacement and keeps the seed-phrase guard. web
// v2.10.x sends no list, yet takes any reported command URL as a command target and
// deploys without either check.
const CommandsFeature = "desktop_commands"

// TakesCommands reports whether a heartbeat reply's features list names
// CommandsFeature. No key, null, or anything but a list is false: the heartbeat then
// reports no command URL and this computer stays report-only on that server.
func TakesCommands(raw json.RawMessage) bool {
	var features []any
	if json.Unmarshal(raw, &features) != nil {
		return false
	}
	for _, f := range features {
		if s, ok := f.(string); ok && s == CommandsFeature {
			return true
		}
	}
	return false
}

// ParseFleetExits reads the fleet_exits a heartbeat response carried: the public IPv4
// exits of every box of the fleet (CashPilot v2.10.0, web main._fleet_exits), which
// this machine's LAN fence drops as it drops its own.
//
// It tells three answers apart, as the web worker does (worker_api._apply_fleet_exits_reply):
//
//   - a list: the fleet's exits now. ok is true. Entries that are not strings are left out.
//   - null, or anything but a list: the server could not read its fleet this time.
//     ok is false and sent is true: keep the list held, unknown after a start.
//   - no key: a server from before the list. sent is false: no other exits while no
//     list is held yet, and keep a held list (a server rolled back to before the list).
func ParseFleetExits(raw json.RawMessage) (ips []string, ok, sent bool) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, false, false
	}
	var items []any
	if trimmed[0] != '[' || json.Unmarshal(trimmed, &items) != nil {
		return nil, false, true
	}
	ips = []string{}
	for _, item := range items {
		if s, isString := item.(string); isString {
			ips = append(ips, s)
		}
	}
	return ips, true, true
}

// FleetEarnings is what the server reports back about the platforms THIS
// machine is running, across the whole account.
//
// THE HONESTY CONSTRAINT, which is the server's and is inherited here:
// earnings are collected per PLATFORM, from the provider's account. They are
// not, and cannot be, attributed to a device — if two machines both run Grass,
// the provider reports one balance and nothing can split it. So this never
// means "this device earned X". It means "the platforms this device runs earned
// X on your account", and Shared marks each platform where that distinction
// actually bites.
//
// Every money field is a POINTER because absent means UNKNOWN. A platform with
// no reading has never been collected for — most often no collector exists, or
// its credentials were never entered — and rendering that as 0.00 would report
// a loss that did not happen.
type FleetEarnings struct {
	WindowDays int             `json:"window_days"`
	Currency   string          `json:"currency"`
	Platforms  []FleetPlatform `json:"platforms"`
	// TotalUSD sums only what is KNOWN. The server omits it entirely when no
	// platform has a reading, because a total that treats unknown as zero is the
	// same lie in aggregate.
	TotalUSD                 *float64 `json:"total_usd"`
	PlatformsWithoutReadings []string `json:"platforms_without_readings"`
}

// FleetPlatform is one platform's account-level figure.
type FleetPlatform struct {
	Slug string   `json:"slug"`
	USD  *float64 `json:"usd"`
	// Shared is true when more than one worker on the fleet runs this platform,
	// which is exactly when "this machine earned it" stops being true.
	Shared bool `json:"shared_with_other_workers"`
}

// ParseEarnings decodes the earnings block a heartbeat response may carry.
//
// Returns nil, nil when the server sent nothing. That is the normal case for an
// older server, and for a worker the server can produce no figures for; it is
// UNKNOWN, and a caller must render it as such rather than as zero. A malformed
// block is an error, not silence — a server sending something unreadable is
// worth surfacing, whereas silence is not.
func ParseEarnings(raw json.RawMessage) (*FleetEarnings, error) {
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return nil, nil
	}
	var out FleetEarnings
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("upstream: decoding the earnings the server reported: %w", err)
	}
	return &out, nil
}

// ErrUnauthorized is wrapped by Send when the server answers 401: the token
// it was given is not (or no longer) valid for this worker. The pairing loop
// uses it to fall back to the enrolment key, the way the Android app does,
// after the owner removed this machine from the fleet or the server lost its
// key store.
var ErrUnauthorized = errors.New("upstream: the server refused this machine's key")

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
}

// New returns a Client with a sane timeout.
//
// The timeout matters more than it looks: heartbeats are on a ticker, so a
// server that accepts connections and never answers would otherwise pile up
// goroutines for the lifetime of the process.
func New() *Client {
	return &Client{HTTP: defaultHTTPClient()}
}

// defaultHTTPClient is the transport used when none was injected. Shared by
// every call so a zero-valued Client cannot end up with one timeout on the
// heartbeat and none on the import.
func defaultHTTPClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second, CheckRedirect: checkRedirect}
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
	if err := ValidateServerURL(serverURL); err != nil {
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
		client = defaultHTTPClient()
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upstream: heartbeat failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Bounded read: the response is small, and an unbounded io.ReadAll against a
	// server that is not the one we think it is would happily consume memory.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return nil, &StatusError{Code: resp.StatusCode, Body: strings.TrimSpace(string(raw)), JSON: json.Valid(raw)}
	}
	if err != nil {
		return nil, fmt.Errorf("upstream: reading response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		// The status is carried in the message because 401 and 422 mean very
		// different things to a user: the first is a wrong or revoked key, the
		// second is a payload this server's version does not accept.
		return nil, &StatusError{Code: resp.StatusCode, Body: strings.TrimSpace(string(raw)), JSON: json.Valid(raw)}
	}
	var out Response
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("%w: decoding response: %v", ErrNotCashPilot, err)
	}
	return &out, nil
}

// StatusError is a heartbeat the server answered with a status other than 200.
// The join screen reads Code and Body to tell the user what went wrong in plain
// words (Explain); a 401 also matches ErrUnauthorized, which the pairing loop
// uses to suspend the pairing.
type StatusError struct {
	Code int
	Body string
	// JSON is true when the body was JSON. A CashPilot server answers in JSON even
	// for a route it does not have; a web page that is not CashPilot answers HTML.
	JSON bool
}

func (e *StatusError) Error() string {
	if e.Code == http.StatusUnauthorized {
		return fmt.Sprintf("%v: server returned %d: %s", ErrUnauthorized, e.Code, e.Body)
	}
	return fmt.Sprintf("upstream: server returned %d: %s", e.Code, e.Body)
}

func (e *StatusError) Unwrap() error {
	if e.Code == http.StatusUnauthorized {
		return ErrUnauthorized
	}
	return nil
}

// ErrNotCashPilot is wrapped by Send when the server answered 200 with a body
// that is not a heartbeat reply: the address is some other web service.
var ErrNotCashPilot = errors.New("upstream: the reply is not from a CashPilot server")
