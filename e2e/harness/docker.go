// Package harness holds the fakes the end-to-end tests drive the real app
// against: a stateful Docker Engine API, canned earning providers and a canned
// exchange-rate feed.
//
// Everything here listens on 127.0.0.1 over TCP, never a unix socket and never a
// named pipe, so the same tests run unchanged on linux, macOS and windows with no
// container runtime installed, no provider accounts and no internet.
package harness

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	goruntime "runtime"
	"sort"
	"strings"
	"sync"
)

// Mount is one named volume attached to a container: the volume's name on the
// host and the path it appears at inside the container. Both halves are kept
// because both matter — the name is what an explicit volume delete addresses,
// and the path is how you tell an earner's identity data apart from a scratch
// directory, which is the difference between a safe delete and a device that has
// to be registered again.
type Mount struct {
	Source string
	Target string
}

// Container is one container the fake daemon is holding, as the tests want to
// read it: the state a user would see plus the pieces a deploy is supposed to
// carry across (image, labels, environment, command, named volumes).
type Container struct {
	ID     string
	Name   string
	Image  string
	State  string
	Labels map[string]string
	Env    []string
	Cmd    []string
	// Entrypoint and StopTimeout are what the deploy asked Docker for; nil means
	// it asked for neither (the image's entrypoint, the daemon's 10 seconds).
	Entrypoint  []string
	StopTimeout *int
	Mounts      []Mount
	// Runtime, NetworkMode and PortBindings are the HostConfig fields of the same
	// names, as the deploy sent them.
	Runtime      string
	NetworkMode  string
	PortBindings map[string][]PortBinding
	// ExtraNetworks are networks a test attached it to beside its network mode.
	ExtraNetworks []string
}

// PortBinding is one host binding of a published port.
type PortBinding struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}

// VolumeNames lists the names of the container's named volumes, in the order the
// deploy asked for them.
func (c Container) VolumeNames() []string {
	out := make([]string, 0, len(c.Mounts))
	for _, m := range c.Mounts {
		out = append(out, m.Source)
	}
	return out
}

// Docker is a fake Docker Engine API with state. It answers the endpoints the app
// actually calls, keeps containers, images and volumes between calls, and records
// every request so a test can assert on the ORDER of what happened — which is the
// part a return value cannot show (a deploy that fails and a deploy that fails
// after destroying the running container both just return an error).
type Docker struct {
	srv *httptest.Server

	mu         sync.Mutex
	containers map[string]*Container
	volumes    map[string]bool
	images     []string
	calls      []string
	callsFull  []string
	logs       string
	pullError  string
	down       bool
	downAfter  string
	nextID     int
	cpuTicks   uint64
	// networks are the user-defined networks, by name. The app creates one, its
	// LAN fence network, and runs a one-shot helper container to write the fence.
	networks map[string]map[string]any
	// failInspect and failStop name containers whose inspect or stop fails.
	failInspect, failStop map[string]bool
}

// NewDocker starts the fake daemon on a loopback TCP port.
func NewDocker() *Docker {
	d := &Docker{
		containers: map[string]*Container{},
		volumes:    map[string]bool{},
		networks:   map[string]map[string]any{},
		logs:       "cashpilot fake container log line one\ncashpilot fake container log line two\n",
	}
	d.srv = httptest.NewUnstartedServer(http.HandlerFunc(d.handle))
	// A dead daemon is simulated by dropping the connection, which the HTTP server
	// would otherwise report on stderr for every such request.
	d.srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	d.srv.Start()
	return d
}

// Host is the value to put in DOCKER_HOST so the app's own client finds this fake
// with no seam added to production code.
func (d *Docker) Host() string {
	return "tcp://" + strings.TrimPrefix(d.srv.URL, "http://")
}

// Close shuts the fake down.
func (d *Docker) Close() { d.srv.Close() }

// SetDown makes the daemon go away (every request has its connection dropped, the
// way a stopped daemon refuses one) or come back.
func (d *Docker) SetDown(down bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.down = down
}

// SetDownAfter makes the daemon go away as soon as it has answered this call
// ("METHOD /path"), so a test can cut it off in the middle of an operation at a
// chosen point rather than hoping to hit the right moment.
func (d *Docker) SetDownAfter(call string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.downAfter = call
}

// SetPullError makes the next image pull fail mid-stream with this message, the
// way a registry outage or a mistyped image does.
func (d *Docker) SetPullError(message string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.pullError = message
}

// SetLogs replaces the log text the fake streams back.
func (d *Docker) SetLogs(text string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.logs = text
}

// Calls returns the requests seen so far as "METHOD /path", in order.
func (d *Docker) Calls() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.calls...)
}

// CallsWithQuery is Calls with the query string kept, as "METHOD /path?query".
// The query is where the Engine API carries the options that change what a call
// destroys — removing a container with v=1 takes its anonymous volumes with it,
// v=0 leaves them — so a test about what survived needs to see it.
func (d *Docker) CallsWithQuery() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.callsFull...)
}

// ResetCalls clears the recorded calls so a test can assert on one phase alone.
func (d *Docker) ResetCalls() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls = nil
	d.callsFull = nil
}

// Container returns the container with this name (or id).
func (d *Docker) Container(nameOrID string) (Container, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	c := d.find(nameOrID)
	if c == nil {
		return Container{}, false
	}
	return *c, true
}

// Names returns every container the fake holds, sorted.
func (d *Docker) Names() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, 0, len(d.containers))
	for _, c := range d.containers {
		out = append(out, c.Name)
	}
	sort.Strings(out)
	return out
}

// Volumes returns every named volume the fake holds, sorted.
func (d *Docker) Volumes() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]string, 0, len(d.volumes))
	for name := range d.volumes {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Images returns every image reference that was pulled, in order.
func (d *Docker) Images() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.images...)
}

// find locates a container by name or id. The caller holds the lock.
func (d *Docker) find(nameOrID string) *Container {
	name := strings.TrimPrefix(nameOrID, "/")
	if c, ok := d.containers[name]; ok {
		return c
	}
	for _, c := range d.containers {
		if c.ID == name {
			return c
		}
	}
	return nil
}

func (d *Docker) record(call, query string) {
	d.calls = append(d.calls, call)
	full := call
	if query != "" {
		full += "?" + query
	}
	d.callsFull = append(d.callsFull, full)
	if d.downAfter != "" && call == d.downAfter {
		// This request is already being served; the next one finds no daemon.
		d.downAfter = ""
		d.down = true
	}
}

// handle is the whole Engine API surface the app uses. The client prefixes every
// path with the negotiated /v1.xx, which is stripped here so the recorded calls
// and the switch below read like the documented API.
func (d *Docker) handle(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	down := d.down
	d.mu.Unlock()
	if down {
		// Drop the connection rather than answering with an error status: a daemon
		// that is not running refuses the connection, it does not reply politely,
		// and the app's error handling has to cope with the real thing.
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				_ = conn.Close()
				return
			}
		}
		http.Error(w, `{"message":"Cannot connect to the Docker daemon."}`, http.StatusInternalServerError)
		return
	}

	path := r.URL.Path
	if strings.HasPrefix(path, "/v1.") {
		if i := strings.Index(path[1:], "/"); i >= 0 {
			path = path[i+1:]
		}
	}

	w.Header().Set("Api-Version", "1.51")
	w.Header().Set("Ostype", "linux")

	d.mu.Lock()
	call := r.Method + " " + path
	if d.fenceCall(r, path) {
		// The LAN fence helper is recorded apart, so assertions about the earner's
		// own container (pull, replace, create) never match it.
		call = "FENCE " + call
	}
	d.record(call, r.URL.RawQuery)
	d.mu.Unlock()

	switch {
	case path == "/_ping":
		w.WriteHeader(http.StatusOK)
	case path == "/version":
		writeJSON(w, http.StatusOK, map[string]any{
			"Version": "28.0.0-fake", "ApiVersion": "1.51", "Os": "linux", "Arch": "amd64",
		})
	case path == "/info":
		// What a real daemon reports and the deploy asks for: its CPU (this
		// machine's, as the fallback assumed before /info was answered), its OS
		// and its OCI runtimes.
		writeJSON(w, http.StatusOK, map[string]any{
			"Architecture": goruntime.GOARCH, "OSType": "linux",
			"Runtimes": map[string]any{"runc": map[string]string{"path": "runc"}},
		})
	case path == "/images/create":
		d.pull(w, r)
	case strings.HasPrefix(path, "/images/") && strings.HasSuffix(path, "/json"):
		d.image(w, strings.TrimSuffix(strings.TrimPrefix(path, "/images/"), "/json"))
	case path == "/containers/create":
		d.create(w, r)
	case path == "/containers/json":
		d.list(w)
	case path == "/containers/prune":
		writeJSON(w, http.StatusOK, map[string]any{})
	case strings.HasPrefix(path, "/volumes/") && r.Method == http.MethodDelete:
		d.removeVolume(w, strings.TrimPrefix(path, "/volumes/"))
	case strings.HasPrefix(path, "/containers/"):
		d.container(w, r, strings.TrimPrefix(path, "/containers/"))
	case path == "/networks" || strings.HasPrefix(path, "/networks/"):
		d.network(w, r, strings.TrimPrefix(strings.TrimPrefix(path, "/networks"), "/"))
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "fake daemon: no route for " + path})
	}
}

func (d *Docker) pull(w http.ResponseWriter, r *http.Request) {
	image := r.URL.Query().Get("fromImage")
	if tag := r.URL.Query().Get("tag"); tag != "" {
		if strings.HasPrefix(tag, "sha256:") {
			image += "@" + tag
		} else {
			image += ":" + tag
		}
	}

	d.mu.Lock()
	failure := d.pullError
	d.pullError = ""
	if failure == "" {
		d.images = append(d.images, image)
	}
	d.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if failure != "" {
		// A registry failure arrives inside the progress stream, not as a status
		// code, so that is how the fake reports it.
		_ = json.NewEncoder(w).Encode(map[string]string{"error": failure})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "Pulling from " + image})
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "Download complete", "id": "layer1"})
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "Status: Downloaded newer image for " + image})
}

// image answers an image inspect with a plain entrypoint as the main process:
// Deploy reads it to refuse images that boot an init system, and the fake's
// images never do. Any ref is answered, so a scenario that never pulled (a
// daemon set down after the pull, a digest ref) still reaches the same steps a
// real daemon would.
func (d *Docker) image(w http.ResponseWriter, ref string) {
	writeJSON(w, http.StatusOK, map[string]any{
		"Id":     "sha256:" + ref,
		"Config": map[string]any{"Entrypoint": []string{"entrypoint"}},
	})
}

func (d *Docker) create(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Image       string            `json:"Image"`
		Env         []string          `json:"Env"`
		Cmd         []string          `json:"Cmd"`
		Entrypoint  []string          `json:"Entrypoint"`
		StopTimeout *int              `json:"StopTimeout"`
		Labels      map[string]string `json:"Labels"`
		HostConfig  struct {
			Runtime      string                   `json:"Runtime"`
			NetworkMode  string                   `json:"NetworkMode"`
			PortBindings map[string][]PortBinding `json:"PortBindings"`
			Mounts       []struct {
				Type   string `json:"Type"`
				Source string `json:"Source"`
				Target string `json:"Target"`
			} `json:"Mounts"`
		} `json:"HostConfig"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "fake daemon: unreadable create body"})
		return
	}
	name := strings.TrimPrefix(r.URL.Query().Get("name"), "/")

	d.mu.Lock()
	defer d.mu.Unlock()
	if name != "" && d.containers[name] != nil {
		writeJSON(w, http.StatusConflict, map[string]string{
			"message": "Conflict. The container name \"/" + name + "\" is already in use",
		})
		return
	}
	d.nextID++
	c := &Container{
		ID:          fmt.Sprintf("ctr%09d", d.nextID),
		Name:        name,
		Image:       body.Image,
		State:       "created",
		Labels:      body.Labels,
		Env:         body.Env,
		Cmd:         body.Cmd,
		Entrypoint:  body.Entrypoint,
		StopTimeout: body.StopTimeout,

		Runtime:      body.HostConfig.Runtime,
		NetworkMode:  body.HostConfig.NetworkMode,
		PortBindings: body.HostConfig.PortBindings,
	}
	for _, m := range body.HostConfig.Mounts {
		if m.Type == "volume" && m.Source != "" {
			c.Mounts = append(c.Mounts, Mount{Source: m.Source, Target: m.Target})
			// The daemon creates a named volume on first use.
			d.volumes[m.Source] = true
		}
	}
	if name == "" {
		name = c.ID
		c.Name = c.ID
	}
	d.containers[name] = c
	writeJSON(w, http.StatusCreated, map[string]any{"Id": c.ID, "Warnings": []string{}})
}

func (d *Docker) list(w http.ResponseWriter) {
	d.mu.Lock()
	defer d.mu.Unlock()
	names := make([]string, 0, len(d.containers))
	for name := range d.containers {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]map[string]any, 0, len(names))
	for _, name := range names {
		c := d.containers[name]
		if c.Labels["cashpilot.managed"] != "true" {
			continue
		}
		out = append(out, map[string]any{
			"Id":     c.ID,
			"Names":  []string{"/" + c.Name},
			"Image":  c.Image,
			"State":  c.State,
			"Status": c.State,
			"Labels": c.Labels,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// container handles everything under /containers/<id or name>/...
func (d *Docker) container(w http.ResponseWriter, r *http.Request, rest string) {
	id, action, _ := strings.Cut(rest, "/")

	d.mu.Lock()
	failed := (action == "json" && d.failing(d.failInspect, id)) || (action == "stop" && d.failing(d.failStop, id))
	d.mu.Unlock()
	if failed {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "fake daemon: " + action + " failed for " + id})
		return
	}

	switch {
	case action == "json":
		d.inspect(w, id)
	case action == "stats":
		d.stats(w, id)
	case action == "wait":
		writeJSON(w, http.StatusOK, map[string]any{"StatusCode": 0})
	case action == "logs":
		d.mu.Lock()
		text := d.logs
		c := d.find(id)
		found := c != nil
		if found && c.Labels["cashpilot.probe"] == "fence" {
			// The LAN fence helper prints one JSON result: the table is in place.
			text = `{"backend":"nft","ok":true,"changed":true,"error":"","present":true,"matches":true,"counters":{}}` + "\n"
		}
		d.mu.Unlock()
		if !found {
			writeJSON(w, http.StatusNotFound, map[string]string{"message": "No such container: " + id})
			return
		}
		w.Header().Set("Content-Type", "application/vnd.docker.multiplexed-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(logFrame(text))
	case action == "start":
		d.setState(w, id, "running")
	case action == "stop":
		d.setState(w, id, "exited")
	case action == "restart":
		d.setState(w, id, "running")
	case action == "kill":
		d.setState(w, id, "exited")
	case action == "" && r.Method == http.MethodDelete:
		d.remove(w, id)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "fake daemon: no container route " + rest})
	}
}

func (d *Docker) inspect(w http.ResponseWriter, id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	c := d.find(id)
	if c == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "No such container: " + id})
		return
	}
	mounts := make([]map[string]any, 0, len(c.Mounts))
	for _, m := range c.Mounts {
		mounts = append(mounts, map[string]any{"Type": "volume", "Name": m.Source, "Destination": m.Target, "RW": true})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"Id":     c.ID,
		"Name":   "/" + c.Name,
		"Image":  c.Image,
		"State":  map[string]any{"Status": c.State, "Running": c.State == "running"},
		"Config": map[string]any{"Image": c.Image, "Labels": c.Labels, "Env": c.Env, "Cmd": c.Cmd},
		"Mounts": mounts,
		"HostConfig": map[string]any{
			"NetworkMode": c.NetworkMode, "Runtime": c.Runtime, "PortBindings": c.PortBindings,
		},
		"NetworkSettings": map[string]any{"Networks": c.networks()},
	})
}

// networks is what inspect reports the container attached to: its network mode's
// network, plus any a test attached it to.
func (c *Container) networks() map[string]any {
	out := map[string]any{}
	if c.NetworkMode != "" {
		out[c.NetworkMode] = map[string]any{}
	}
	for _, n := range c.ExtraNetworks {
		out[n] = map[string]any{}
	}
	return out
}

// SetState sets a container's state as Docker would report it ("restarting").
func (d *Docker) SetState(name, state string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if c := d.find(name); c != nil {
		c.State = state
	}
}

// SetLabel sets (or, with value "", removes) one label of a container, the way a
// container made by an older version lacks a label a newer one sets.
func (d *Docker) SetLabel(name, key, value string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if c := d.find(name); c != nil {
		if value == "" {
			delete(c.Labels, key)
		} else {
			c.Labels[key] = value
		}
	}
}

// AttachNetwork reports the container attached to one more network.
func (d *Docker) AttachNetwork(name, network string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if c := d.find(name); c != nil {
		c.ExtraNetworks = append(c.ExtraNetworks, network)
	}
}

// RemoveNetwork deletes a network behind the app's back, the way a user's
// `docker network rm` or a daemon reset would.
func (d *Docker) RemoveNetwork(name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.networks, name)
}

// FailInspect makes inspecting this container fail with a server error.
func (d *Docker) FailInspect(name string) { d.setFail(&d.failInspect, name) }

// FailStop makes stopping this container fail with a server error.
func (d *Docker) FailStop(name string) { d.setFail(&d.failStop, name) }

func (d *Docker) setFail(set *map[string]bool, name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if *set == nil {
		*set = map[string]bool{}
	}
	(*set)[name] = true
}

// failing reports a container whose name or id is in set. Called with d.mu held.
func (d *Docker) failing(set map[string]bool, id string) bool {
	c := d.find(id)
	return set[id] || (c != nil && (set[c.Name] || set[c.ID]))
}

func (d *Docker) setState(w http.ResponseWriter, id, state string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	c := d.find(id)
	if c == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "No such container: " + id})
		return
	}
	c.State = state
	w.WriteHeader(http.StatusNoContent)
}

func (d *Docker) remove(w http.ResponseWriter, id string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	c := d.find(id)
	if c == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "No such container: " + id})
		return
	}
	// A named volume survives its container, exactly as the real daemon leaves it:
	// only an explicit volume delete removes it.
	delete(d.containers, c.Name)
	w.WriteHeader(http.StatusNoContent)
}

func (d *Docker) removeVolume(w http.ResponseWriter, name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.volumes[name] {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "no such volume: " + name})
		return
	}
	delete(d.volumes, name)
	w.WriteHeader(http.StatusNoContent)
}

// stats answers the one-shot stats read. The CPU counter climbs on every call so
// two samples taken a moment apart produce a real, non-zero CPU percentage.
func (d *Docker) stats(w http.ResponseWriter, id string) {
	d.mu.Lock()
	c := d.find(id)
	if c == nil {
		d.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "No such container: " + id})
		return
	}
	d.cpuTicks += 10_000_000
	cpu := d.cpuTicks
	d.mu.Unlock()

	writeJSON(w, http.StatusOK, map[string]any{
		"read": "2026-01-01T00:00:00Z",
		"cpu_stats": map[string]any{
			"cpu_usage":        map[string]any{"total_usage": cpu, "percpu_usage": []uint64{cpu / 2, cpu / 2}},
			"system_cpu_usage": cpu * 100,
			"online_cpus":      2,
		},
		"memory_stats": map[string]any{
			"usage": 134217728, // 128 MiB
			"limit": 268435456,
			"stats": map[string]any{"inactive_file": 8388608}, // 8 MiB -> 120 MiB reported
		},
	})
}

// logFrame wraps text in Docker's 8-byte multiplexed stream header, which is what
// the daemon sends and what the app has to strip back off.
func logFrame(text string) []byte {
	payload := []byte(text)
	size := len(payload)
	header := []byte{1, 0, 0, 0, byte(size >> 24), byte(size >> 16), byte(size >> 8), byte(size)}
	return append(header, payload...)
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// network answers the network endpoints: list (with a label filter), create, inspect
// and remove. A bridge created with options keeps them, as the real daemon does.
func (d *Docker) network(w http.ResponseWriter, r *http.Request, ref string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	find := func(ref string) map[string]any {
		for name, n := range d.networks {
			if name == ref || n["Id"] == ref {
				return n
			}
		}
		return nil
	}
	switch {
	case ref == "" && r.Method == http.MethodGet:
		out := make([]map[string]any, 0, len(d.networks))
		for _, n := range d.networks {
			out = append(out, n)
		}
		writeJSON(w, http.StatusOK, out)
	case ref == "create" && r.Method == http.MethodPost:
		var body struct {
			Name    string
			Driver  string
			Options map[string]string
			Labels  map[string]string
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "fake daemon: unreadable network body"})
			return
		}
		d.nextID++
		id := fmt.Sprintf("net%09d", d.nextID)
		d.networks[body.Name] = map[string]any{
			"Name": body.Name, "Id": id, "Driver": body.Driver, "Options": body.Options, "Labels": body.Labels,
			"IPAM":       map[string]any{"Config": []map[string]any{{"Subnet": "172.30.0.0/16", "Gateway": "172.30.0.1"}}},
			"Containers": map[string]any{},
		}
		writeJSON(w, http.StatusCreated, map[string]any{"Id": id})
	case r.Method == http.MethodGet:
		if n := find(ref); n != nil {
			writeJSON(w, http.StatusOK, n)
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "network " + ref + " not found"})
	case r.Method == http.MethodDelete:
		if n := find(ref); n != nil {
			delete(d.networks, n["Name"].(string))
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "network " + ref + " not found"})
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "fake daemon: no network route " + ref})
	}
}

// fenceCall reports a request about the LAN fence helper container. Called with d.mu held.
func (d *Docker) fenceCall(r *http.Request, path string) bool {
	const helper = "cashpilot-fence"
	if path == "/containers/create" {
		return strings.TrimPrefix(r.URL.Query().Get("name"), "/") == helper
	}
	rest, ok := strings.CutPrefix(path, "/containers/")
	if !ok {
		return false
	}
	id, _, _ := strings.Cut(rest, "/")
	if id == helper {
		return true
	}
	c := d.find(id)
	return c != nil && c.Labels["cashpilot.probe"] == "fence"
}
