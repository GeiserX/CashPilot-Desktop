// Package workerapi holds both ends of the CashPilot worker API: the routes a
// CashPilot Docker worker serves (app/worker_api.py in the web repository), which
// the web UI calls to deploy, stop, start, restart and remove services on it.
//
// This file is the calling side, used when this Desktop is a lead: it sends a
// worker that reports here exactly the requests the web UI's server sends
// (app/main.py _proxy_to_worker, _proxy_worker_deploy, _proxy_worker_command).
// The worker stores no credentials: every deploy carries its env.
package workerapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Mount is one host path or named volume and where it goes in the container,
// as the worker's DeploySpec.volumes reads it: {"bind": target, "mode": "rw"}.
type Mount struct {
	Bind string `json:"bind"`
	Mode string `json:"mode"`
}

// Resources are the catalog's docker.resources, under the worker's field names.
// Only the limits the catalog sets go out, as the web app forwards the block.
type Resources struct {
	MemLimit       string `json:"mem_limit,omitempty"`
	MemReservation string `json:"mem_reservation,omitempty"`
	OomScoreAdj    *int   `json:"oom_score_adj,omitempty"`
	CPUShares      int64  `json:"cpu_shares,omitempty"`
}

// DeploySpec is the body of POST /api/containers/{slug}/deploy, field for field
// what the web app's _deploy sends, with optional replacement approval.
// A port binding is a host port (int), or
// [address, port] for a bind to one interface, or a list of those pairs.
type DeploySpec struct {
	Image       string            `json:"image"`
	Env         map[string]string `json:"env"`
	Hostname    *string           `json:"hostname"`
	Ports       map[string]any    `json:"ports"`
	Volumes     map[string]Mount  `json:"volumes"`
	NetworkMode *string           `json:"network_mode"`
	CapAdd      []string          `json:"cap_add"`
	Devices     []string          `json:"devices"`
	Privileged  bool              `json:"privileged"`
	Resources   *Resources        `json:"resources,omitempty"`
	Command     *string           `json:"command,omitempty"`
	MovedMounts []string          `json:"moved_mounts"`
	// Approval is the signed note that the owner confirmed this exact spec on a
	// service that keeps a wallet or node identity (see Sign and DeployTarget).
	Approval string `json:"approval,omitempty"`
	// ReplaceExisting is sent only after the owner has approved a replacement, and
	// only to a worker that cannot name the container it would replace.
	ReplaceExisting bool `json:"replace_existing,omitempty"`
	// ExpectedContainer is the container the deploy may replace, for a worker that
	// names it (DeployTargetState.Container): "none" for a first deploy, or the full
	// Docker ID the owner approved. The worker refuses with deploy_target_changed
	// when another container is there, so a replacement never hits one the owner
	// did not see.
	ExpectedContainer string `json:"expected_container,omitempty"`
}

// KeptMount is one mount the worker kept where the running container has it.
type KeptMount struct {
	Target    string `json:"target"`
	Kept      string `json:"kept"`
	Requested string `json:"requested"`
}

// DeployResult is the worker's answer to a deploy it carried out.
type DeployResult struct {
	Status      string      `json:"status"`
	ContainerID string      `json:"container_id"`
	KeptMounts  []KeptMount `json:"kept_mounts"`
}

// Commands a worker takes for one copy of a service.
const (
	CommandStop    = "stop"
	CommandStart   = "start"
	CommandRestart = "restart"
	CommandRemove  = "remove"
)

// Error is a request the worker refused or never answered, in words that are safe
// to show. Code is the worker's own error code for a refusal it structures
// ("step_up_required", "route_locked", ...), "" otherwise. Unknown is true when the
// request may have reached the worker but no answer came back, so it may still
// carry it out.
type Error struct {
	Status  int
	Code    string
	Message string
	Unknown bool
	// CurrentContainer is the container the worker found instead of the expected
	// one, on a deploy_target_changed refusal: "none" or a full Docker ID.
	CurrentContainer string

	missingEndpoint bool
}

func (e *Error) Error() string { return e.Message }

// Client calls one worker. BaseURL is the URL the worker reports in its heartbeat
// (CASHPILOT_WORKER_URL); Key is the key it takes: its own per-worker key once it
// confirmed it, else the shared fleet key it enrolled with.
type Client struct {
	BaseURL string
	Key     string
	// HTTP is the client to use; nil means a fresh one. It never follows a
	// redirect, so the key goes only to the address the worker reported.
	HTTP *http.Client
}

// ValidBaseURL checks a worker URL before any key is sent to it: http or https,
// a host, and nothing after the path a reverse proxy may add.
func ValidBaseURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "", errors.New("this worker reported no address, so it only reports to this Desktop and cannot take commands")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("this worker reported an address that is not an http or https URL (%q)", raw)
	}
	return raw, nil
}

// Deploy sends one deploy. timeout must cover the worker stopping the container it
// replaces and waiting out a slow start (see DeployTimeout).
func (c *Client) Deploy(ctx context.Context, slug string, spec DeploySpec, timeout time.Duration) (DeployResult, error) {
	var out DeployResult
	err := c.do(ctx, http.MethodPost, "/api/containers/"+url.PathEscape(slug)+"/deploy", nil, spec, spec.Env, timeout, &out)
	return out, err
}

const checkedDeploySuffix = "/deploy-checked"

// DeployTargetState is what a worker says about the container a deploy would replace.
type DeployTargetState struct {
	// Checked is true when the worker serves the checked deploy route.
	Checked bool
	// Exists is true when a container already has the deploy's Docker name.
	Exists bool
	// Container names that container: "none", or its full Docker ID. It is "" when
	// the worker does not name it (a worker older than CashPilot #831), so a
	// replacement there can hit whichever container runs when the deploy starts.
	Container string
}

// DeployState reports whether the worker supports checked deploys, and if so
// whether the actual Docker name to be deployed already exists.
func (c *Client) DeployState(ctx context.Context, slug string) (DeployTargetState, error) {
	var out map[string]json.RawMessage
	err := c.do(ctx, http.MethodGet, "/api/containers/"+url.PathEscape(slug)+checkedDeploySuffix, nil, nil, nil, 0, &out)
	if err != nil {
		var refusal *Error
		if errors.As(err, &refusal) && refusal.missingEndpoint {
			return DeployTargetState{}, nil
		}
		return DeployTargetState{}, err
	}
	var exists bool
	if raw, ok := out["exists"]; !ok || isNull(raw) || json.Unmarshal(raw, &exists) != nil {
		return DeployTargetState{}, errors.New("the worker did not answer the container lookup")
	}
	state := DeployTargetState{Checked: true, Exists: exists}
	// Left out, the worker is older than CashPilot #831 and names no container. A
	// worker that sends the field must name one this app can send back (a null
	// names none), and agree with itself about whether it exists.
	if raw, ok := out["expected_container"]; ok {
		var id string
		if isNull(raw) || json.Unmarshal(raw, &id) != nil || !ContainerRef(id) || (id != NoContainer) != exists {
			return DeployTargetState{}, errors.New("the worker answered the container lookup with an ID this app cannot use; update the worker")
		}
		state.Container = id
	}
	return state, nil
}

func isNull(raw json.RawMessage) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

// NoContainer is what a worker says, and a deploy expects, when no container has
// the deploy's Docker name.
const NoContainer = "none"

// ContainerRef reports whether s names a deploy target as the worker does:
// NoContainer, or a full Docker container ID (64 lowercase hex characters).
func ContainerRef(s string) bool {
	if s == NoContainer {
		return true
	}
	if len(s) != 64 {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// LegacyDeployTargetExists checks the worker's inventory. It can show an
// existing container, but a cached inventory cannot prove one is absent.
func (c *Client) LegacyDeployTargetExists(ctx context.Context, slug string) (bool, error) {
	var out []struct {
		Name string `json:"name"`
	}
	if err := c.do(ctx, http.MethodGet, "/api/containers", nil, nil, nil, 0, &out); err != nil {
		return false, err
	}
	for _, container := range out {
		if container.Name == "cashpilot-"+slug {
			return true, nil
		}
	}
	return false, nil
}

// DeployChecked makes the worker enforce replacement approval under its deploy lock.
func (c *Client) DeployChecked(ctx context.Context, slug string, spec DeploySpec, timeout time.Duration) (DeployResult, error) {
	var out DeployResult
	err := c.do(ctx, http.MethodPost, "/api/containers/"+url.PathEscape(slug)+checkedDeploySuffix, nil, spec, spec.Env, timeout, &out)
	return out, err
}

// Command sends stop, start or restart for copy instance of slug, or removes it.
// Copy 1 is addressed without ?instance, as the web app sends it, so an older worker
// sees the request it always did. Removing copy 1 removes the service on that worker:
// every copy, and its proxied copies; the worker keeps the data volumes.
func (c *Client) Command(ctx context.Context, slug, command string, instance int, timeout time.Duration) error {
	escaped := url.PathEscape(slug)
	var query url.Values
	if instance > 1 {
		query = url.Values{"instance": {strconv.Itoa(instance)}}
	}
	switch command {
	case CommandStop, CommandStart, CommandRestart:
		return c.do(ctx, http.MethodPost, "/api/containers/"+escaped+"/"+command, query, nil, nil, timeout, nil)
	case CommandRemove:
		if instance > 1 {
			return c.do(ctx, http.MethodDelete, "/api/containers/"+escaped+"/instances/"+strconv.Itoa(instance), nil, nil, nil, timeout, nil)
		}
		return c.do(ctx, http.MethodDelete, "/api/containers/"+escaped, nil, nil, nil, timeout, nil)
	}
	return fmt.Errorf("unknown command %q: use stop, start, restart or remove", command)
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body any, sentEnv map[string]string, timeout time.Duration, out any) error {
	base, err := ValidBaseURL(c.BaseURL)
	if err != nil {
		return err
	}
	target := base + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(raw)
	}
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.Key != "" {
		req.Header.Set("Authorization", "Bearer "+c.Key)
	}
	deploy, isDeploy := out.(*DeployResult)
	resp, err := c.httpClient().Do(req)
	if err != nil {
		failure := transportError(err, timeout)
		if isDeploy && failure.Unknown {
			failure.Message = "The worker deploy outcome is unknown. " + failure.Message
		}
		return failure
	}
	defer resp.Body.Close()
	const maxReplyBytes = 1 << 20
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxReplyBytes+1))
	if isDeploy && (readErr != nil || len(raw) > maxReplyBytes) {
		return unknownDeployReply(resp.StatusCode)
	}
	if resp.StatusCode >= 400 {
		failure := refusal(resp.StatusCode, raw, sentEnv)
		if isDeploy && failure.Unknown {
			return unknownDeployReply(resp.StatusCode)
		}
		return failure
	}
	if isDeploy {
		if resp.StatusCode < 200 || resp.StatusCode >= 300 ||
			json.Unmarshal(raw, deploy) != nil || deploy.Status != "deployed" ||
			strings.TrimSpace(deploy.ContainerID) == "" {
			*deploy = DeployResult{}
			return unknownDeployReply(resp.StatusCode)
		}
		return nil
	}
	if out == nil || len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return &Error{Status: resp.StatusCode, Message: "The worker answered, but not in a form this app reads; update the worker"}
	}
	return nil
}

func unknownDeployReply(status int) *Error {
	return &Error{
		Status: status, Unknown: true,
		Message: "The worker deploy outcome is unknown. CashPilot did not get a complete success reply. Check the worker before trying again.",
	}
}

func (c *Client) httpClient() *http.Client {
	base := c.HTTP
	if base == nil {
		base = &http.Client{}
	}
	cp := *base
	cp.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &cp
}

// transportError is a request that failed on the way. Never reached the worker:
// it did nothing. Timed out or dropped after it went out: it may still finish.
func transportError(err error, timeout time.Duration) *Error {
	var netErr net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &netErr) && netErr.Timeout()) {
		var opErr *net.OpError
		if errors.As(err, &opErr) && opErr.Op == "dial" {
			return &Error{Status: 503, Message: "Could not reach the worker: it did not take the connection. Check that it runs and that this computer can reach its address."}
		}
		return &Error{Status: 504, Unknown: true, Message: fmt.Sprintf("The worker did not answer within %g s. It may still be working on it; check the worker.", timeout.Seconds())}
	}
	var opErr *net.OpError
	var dnsErr *net.DNSError
	if errors.As(err, &opErr) && opErr.Op == "dial" || errors.As(err, &dnsErr) {
		return &Error{Status: 503, Message: "Could not reach the worker: it did not take the connection. Check that it runs and that this computer can reach its address."}
	}
	return &Error{Status: 504, Unknown: true, Message: "The connection to the worker dropped before it answered. It may still be working on it; check the worker."}
}

// forwardableCodes are the refusals the worker structures for the operator, with a
// message it wrote itself (web main._safe_worker_detail).
var forwardableCodes = map[string]bool{
	"self_update_unavailable":     true,
	"isolation_check_busy":        true,
	"isolation_check_unavailable": true,
	"critical_volume_mount":       true,
	"step_up_required":            true,
	"replace_existing":            true,
	"deploy_target_changed":       true,
	"step_up_rejected":            true,
	"restore_unfinished":          true,
	"restore_cleared":             true,
	"route_locked":                true,
	"proxied_only":                true,
	"route_not_tunnel":            true,
	"route_unknown":               true,
	"routed_copy_may_exist":       true,
	"critical_volume":             true,
	"copy_not_found":              true,
}

// forwardableTextStatuses are the statuses where the worker writes a plain-string
// refusal itself (web main._FORWARDABLE_WORKER_TEXT_STATUSES). A 500 is a failure
// nobody vetted, and a 422 echoes the request body, env values included.
var forwardableTextStatuses = map[int]bool{400: true, 401: true, 403: true, 404: true, 409: true, 503: true}

const (
	workerTextMax = 400
	// maskMinLen: shorter env values are not masked, or a PUID of 1000 would garble
	// the worker's own numbers; no provider credential is that short.
	maskMinLen = 8
)

// refusal turns a worker's error answer into words that are safe to show: its own
// structured message, its own plain-text reason with any env value masked, or a
// generic line that says where the reason is.
func refusal(status int, raw []byte, sentEnv map[string]string) *Error {
	var envelope struct {
		Detail json.RawMessage `json:"detail"`
	}
	unknown := status == 502 || status == 504 || status == 520 || status == 524
	if json.Unmarshal(raw, &envelope) == nil && len(envelope.Detail) > 0 {
		var structured struct {
			Error   any `json:"error"`
			Message any `json:"message"`
			Current any `json:"current_container"`
		}
		if json.Unmarshal(envelope.Detail, &structured) == nil {
			if code, ok := structured.Error.(string); ok && forwardableCodes[code] {
				message, _ := structured.Message.(string)
				message = clean(message, sentEnv)
				if message == "" {
					message = failureText(status)
				}
				failure := &Error{Status: status, Code: code, Message: message, Unknown: unknown}
				if current, ok := structured.Current.(string); ok && ContainerRef(current) {
					failure.CurrentContainer = current
				}
				return failure
			}
		}
		var text string
		if forwardableTextStatuses[status] && json.Unmarshal(envelope.Detail, &text) == nil {
			if text == "Not Found" {
				return &Error{Status: status, Message: failureText(status), missingEndpoint: status == http.StatusNotFound}
			}
			if cleaned := clean(text, sentEnv); cleaned != "" {
				return &Error{Status: status, Message: cleaned}
			}
		}
	}
	return &Error{Status: status, Message: failureText(status), Unknown: unknown}
}

// clean is the worker's text on one line, printable, capped, with every env value
// this request carried masked: the worker never echoes one, but a deploy spec holds
// live credentials, so one that slipped in is not shown.
func clean(text string, env map[string]string) string {
	values := make([]string, 0, len(env))
	for _, v := range env {
		if len(v) >= maskMinLen {
			values = append(values, v)
		}
	}
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, v := range values {
		text = strings.ReplaceAll(text, v, "***")
	}
	text = strings.Join(strings.Fields(text), " ")
	text = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return -1
	}, text)
	if runes := []rune(text); len(runes) > workerTextMax {
		text = strings.TrimRightFunc(string(runes[:workerTextMax-1]), unicode.IsSpace) + "…"
	}
	return text
}

// failureText is what to say when the worker's answer has no reason that can be shown.
func failureText(status int) string {
	if status == 404 {
		return "The worker does not know this request (HTTP 404); it may be older than this app, so update it"
	}
	return fmt.Sprintf("Worker request failed (HTTP %d); the worker's log has the reason", status)
}

// StopTimeout is how long the worker waits for a service to stop cleanly, read as
// the worker reads the catalog's stop_timeout: unset, zero or negative mean 30.
func StopTimeout(catalogStopTimeout int) time.Duration {
	if catalogStopTimeout <= 0 {
		catalogStopTimeout = 30
	}
	return time.Duration(catalogStopTimeout) * time.Second
}

// startSettle is the worker's START_SETTLE_SECONDS: how long it waits out a start
// Docker is slow to answer.
const startSettle = 120 * time.Second

// DeployTimeout is how long a deploy may take on the worker: it stops the old
// container first and waits out a slow start (web main._deploy_timeout).
func DeployTimeout(catalogStopTimeout int) time.Duration {
	return 60*time.Second + StopTimeout(catalogStopTimeout) + startSettle
}

// CommandTimeout is how long a command may take: a stop, a restart or a removal waits
// for the container to stop cleanly, and a stop of a fence-gated node also waits for
// a redeploy of it to finish (web main._proxy_worker_command).
func CommandTimeout(command string, catalogStopTimeout int, fenceGated bool) time.Duration {
	timeout := 30 * time.Second
	if command == CommandStop || command == CommandRestart || command == CommandRemove {
		timeout += StopTimeout(catalogStopTimeout)
	}
	if command == CommandStop && fenceGated {
		timeout += DeployTimeout(catalogStopTimeout)
	}
	return timeout
}
