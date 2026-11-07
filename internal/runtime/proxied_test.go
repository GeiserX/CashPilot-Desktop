package runtime

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

const (
	testPassword = "pa ss@w:rd-SECRET"
	testUser     = "user-SECRET"
)

var proxiedService = catalog.Service{
	Name: "Earner",
	Slug: "earner",
	Docker: catalog.DockerConfig{
		Image: "example/earner:1.0",
		Env: []catalog.EnvVar{
			{Key: "TOKEN", Required: true},
			{Key: "DEVICE_NAME", Default: "cashpilot-{hostname}"},
		},
		Command: "run --device ${DEVICE_NAME}",
		CapAdd:  []string{"SETUID"},
	},
}

func testSpec() ProxySpec {
	return ProxySpec{Kind: "socks5", Host: "gate.example.net", Port: 7000, Username: testUser, Password: testPassword}
}

// fakeContainer is one container of fakeTunnelDocker.
type fakeContainer struct {
	id, name  string
	opts      client.ContainerCreateOptions
	state     string
	startedAt string
	files     map[string]string
}

// fakeTunnelDocker is a daemon for the proxied-copy logic: it records every call in
// order and keeps the secret files a sidecar was given.
type fakeTunnelDocker struct {
	mu         sync.Mutex
	containers map[string]*fakeContainer
	calls      []string
	next       int
	clock      int
	// exit is what a running sidecar's ready file holds; "" means the tunnel never
	// gets an exit.
	exit string
	// archives are every tar copied in, for the secret checks.
	archives [][]byte
	// network is what sidecarNetwork answers: "" for the default bridge.
	network string
	// networkErr is what sidecarNetwork fails with: a fence that cannot be applied.
	networkErr error
	// created is the Created time list reports for every container (unix seconds).
	created int64
	// renameErr makes every rename fail.
	renameErr error
	// stopErr makes a stop of that container name fail.
	stopErr map[string]error
}

func newFakeTunnelDocker() *fakeTunnelDocker {
	return &fakeTunnelDocker{containers: map[string]*fakeContainer{}, exit: `{"ip":"203.0.113.7","country":"NL"}`}
}

func (f *fakeTunnelDocker) find(ref string) *fakeContainer {
	if c, ok := f.containers[ref]; ok {
		return c
	}
	for _, c := range f.containers {
		if c.id == ref {
			return c
		}
	}
	return nil
}

func (f *fakeTunnelDocker) record(call string) { f.calls = append(f.calls, call) }

func (f *fakeTunnelDocker) facts(context.Context) daemonInfo {
	return daemonInfo{Architecture: "x86_64", OSType: "linux"}
}
func (f *fakeTunnelDocker) pull(_ context.Context, image string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("pull " + image)
	return nil
}
func (f *fakeTunnelDocker) imageRuns(context.Context, string) ([]string, []string, error) {
	return []string{"entrypoint"}, nil, nil
}
func (f *fakeTunnelDocker) create(_ context.Context, opts client.ContainerCreateOptions) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.containers[opts.Name] != nil {
		return "", fmt.Errorf("name %s in use", opts.Name)
	}
	f.next++
	c := &fakeContainer{id: fmt.Sprintf("id%03d", f.next), name: opts.Name, opts: opts, state: "created", files: map[string]string{}}
	f.containers[opts.Name] = c
	f.record("create " + opts.Name)
	return c.id, nil
}
func (f *fakeTunnelDocker) copyTo(_ context.Context, id, dir string, archive []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.find(id)
	if c == nil {
		return errors.New("no such container")
	}
	f.archives = append(f.archives, archive)
	tr := tar.NewReader(bytes.NewReader(archive))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		data, _ := io.ReadAll(tr)
		c.files[h.Name] = string(data)
	}
	f.record("copy " + c.name + " " + dir)
	return nil
}
func (f *fakeTunnelDocker) start(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.find(id)
	if c == nil {
		return errors.New("no such container")
	}
	f.clock++
	c.state, c.startedAt = "running", fmt.Sprintf("2026-11-04T00:00:%02dZ", f.clock)
	f.record("start " + c.name)
	return nil
}
func (f *fakeTunnelDocker) stop(_ context.Context, id string, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.find(id)
	if c == nil {
		return errors.New("no such container")
	}
	if err := f.stopErr[c.name]; err != nil {
		return err
	}
	c.state = "exited"
	f.record("stop " + c.name)
	return nil
}
func (f *fakeTunnelDocker) restart(_ context.Context, id string, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.find(id)
	if c == nil {
		return errors.New("no such container")
	}
	f.clock++
	c.state, c.startedAt = "running", fmt.Sprintf("2026-11-04T00:00:%02dZ", f.clock)
	f.record("restart " + c.name)
	return nil
}
func (f *fakeTunnelDocker) remove(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.find(id)
	if c == nil {
		return errors.New("no such container")
	}
	delete(f.containers, c.name)
	f.record("remove " + c.name)
	return nil
}
func (f *fakeTunnelDocker) rename(_ context.Context, id, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.find(id)
	if c == nil {
		return errors.New("no such container")
	}
	if f.renameErr != nil {
		return f.renameErr
	}
	if f.containers[name] != nil {
		return fmt.Errorf("name %s in use", name)
	}
	delete(f.containers, c.name)
	f.record("rename " + c.name + " " + name)
	c.name = name
	f.containers[name] = c
	return nil
}
func (f *fakeTunnelDocker) inspect(_ context.Context, ref string) (container.InspectResponse, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.find(ref)
	if c == nil {
		return container.InspectResponse{}, false, nil
	}
	return container.InspectResponse{
		ID:      c.id,
		Name:    "/" + c.name,
		State:   &container.State{Status: container.ContainerState(c.state), StartedAt: c.startedAt},
		Config:  c.opts.Config,
		Created: time.Unix(f.created, 0).UTC().Format(time.RFC3339Nano),
	}, true, nil
}
func (f *fakeTunnelDocker) list(_ context.Context, _ ...string) ([]container.Summary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []container.Summary
	for _, c := range f.containers {
		out = append(out, container.Summary{ID: c.id, Names: []string{"/" + c.name}, Labels: c.opts.Config.Labels, State: container.ContainerState(c.state), Created: f.created})
	}
	return out, nil
}
func (f *fakeTunnelDocker) readFile(_ context.Context, id, path string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c := f.find(id)
	if c != nil && c.state == "running" && strings.HasPrefix(path, tunnelSecretDir+"/") {
		if v, ok := c.files[strings.TrimPrefix(path, tunnelSecretDir+"/")]; ok {
			return v, nil
		}
		return "", errors.New("no such file")
	}
	if c == nil || c.state != "running" || path != tunnelReadyFile || f.exit == "" || c.opts.Config.Labels[LabelRole] != RoleTunnel {
		return "", errors.New("no such file")
	}
	return f.exit + "\n", nil
}
func (f *fakeTunnelDocker) sidecarNetwork(context.Context) (string, error) {
	return f.network, f.networkErr
}
func (f *fakeTunnelDocker) logsTail(context.Context, string, int) string {
	return "cp-tunnel: no working exit through the tunnel after 40 s (socks5://" + testUser + ":" + testPassword + "@gate)"
}
func (f *fakeTunnelDocker) netCounters(context.Context, string) (*uint64, *uint64) {
	rx, tx := uint64(1000), uint64(500)
	return &rx, &tx
}

func (f *fakeTunnelDocker) callList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func withTestTunnel(t *testing.T) {
	t.Helper()
	t.Setenv(tunnelImageEnv, "example/cashpilot-tunnel:1.66.0")
	oldLookup, oldPoll, oldTimeout := lookupIPv4, readyPoll, TunnelReadyTimeout
	lookupIPv4 = func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("198.51.100.4")}, nil }
	readyPoll, TunnelReadyTimeout = time.Millisecond, 50*time.Millisecond
	t.Cleanup(func() { lookupIPv4, readyPoll, TunnelReadyTimeout = oldLookup, oldPoll, oldTimeout })
}

func deployRequest() ProxiedDeploy {
	return ProxiedDeploy{Slug: "earner", ProxyID: 3, Service: proxiedService, Env: map[string]string{"TOKEN": "tok"}, Proxy: testSpec(), OwnIP: "192.0.2.1"}
}

// A fenced tunnel gets the fence's resolvers, as every fenced earner does: the fence
// drops the daemon's LAN resolver, so without them the tunnel resolves nothing.
func TestFencedSidecarGetsTheFencesDNS(t *testing.T) {
	id := int64(3)
	fenced := sidecarCreate(tunnelName("earner", id), "earner", &id, "example/cashpilot-tunnel:1.66.0", FenceNetwork).HostConfig
	if fenced.NetworkMode != FenceNetwork {
		t.Fatalf("network mode %q", fenced.NetworkMode)
	}
	if !slices.Equal(fenced.DNS, fenceDNS) {
		t.Fatalf("DNS %v, want the fence's %v", fenced.DNS, fenceDNS)
	}
	plain := sidecarCreate(tunnelName("earner", id), "earner", &id, "example/cashpilot-tunnel:1.66.0", "").HostConfig
	if plain.NetworkMode != "bridge" || plain.DNS != nil {
		t.Fatalf("the default bridge tunnel was changed: net=%q dns=%v", plain.NetworkMode, plain.DNS)
	}
}

// The sidecar is the only container with any network power, so its create options
// are the security boundary: NET_ADMIN and /dev/net/tun and nothing else, never
// privileged, read-only, no Docker socket, no published ports.
func TestSidecarIsLockedDown(t *testing.T) {
	id := int64(3)
	opts := sidecarCreate(tunnelName("earner", id), "earner", &id, "example/cashpilot-tunnel:1.66.0", "")
	h := opts.HostConfig
	if h.Privileged || !slices.Equal(h.CapDrop, []string{"ALL"}) || !slices.Equal(h.CapAdd, []string{"NET_ADMIN"}) {
		t.Fatalf("capabilities: privileged=%v drop=%v add=%v", h.Privileged, h.CapDrop, h.CapAdd)
	}
	if !h.ReadonlyRootfs || !slices.Contains(h.SecurityOpt, noNewPrivileges) || h.PidsLimit == nil || *h.PidsLimit != tunnelPidsLimit {
		t.Fatalf("hardening: ro=%v secopt=%v pids=%v", h.ReadonlyRootfs, h.SecurityOpt, h.PidsLimit)
	}
	if len(h.Resources.Devices) != 1 || h.Resources.Devices[0].PathOnHost != "/dev/net/tun" {
		t.Fatalf("devices: %+v", h.Resources.Devices)
	}
	if len(h.Mounts) != 0 || len(h.Binds) != 0 || len(h.PortBindings) != 0 || h.NetworkMode != "bridge" {
		t.Fatalf("mounts=%v binds=%v ports=%v net=%v", h.Mounts, h.Binds, h.PortBindings, h.NetworkMode)
	}
	if opts.Config.Labels[LabelRole] != RoleTunnel || opts.Config.Labels[LabelProxy] != "3" {
		t.Fatalf("labels: %v", opts.Config.Labels)
	}
	if _, ok := opts.Config.Volumes[tunnelSecretDir]; !ok {
		t.Fatalf("the secrets go in an anonymous volume: %v", opts.Config.Volumes)
	}
}

func TestEarnerJoinsTheSidecarAndCannotEscapeIt(t *testing.T) {
	opts, err := earnerCreate(deployRequest(), "sidecar-id", "example/earner:1.0", "linux")
	if err != nil {
		t.Fatal(err)
	}
	h := opts.HostConfig
	if h.NetworkMode != "container:sidecar-id" || h.RestartPolicy.Name != "no" {
		t.Fatalf("network=%v restart=%v", h.NetworkMode, h.RestartPolicy)
	}
	if opts.Config.Hostname != "" || len(h.PortBindings) != 0 || len(opts.Config.ExposedPorts) != 0 || len(h.Mounts) != 0 {
		t.Fatalf("a joined container sets no network of its own: %+v %+v", opts.Config, h)
	}
	if h.Privileged || !slices.Equal(h.CapDrop, []string{"ALL"}) || len(h.Resources.Devices) != 0 {
		t.Fatalf("privileged=%v drop=%v devices=%v", h.Privileged, h.CapDrop, h.Resources.Devices)
	}
	if opts.Name != "cashpilot-earner-p3" || opts.Config.Labels[LabelProxy] != "3" || opts.Config.Labels[LabelInstance] != "" {
		t.Fatalf("name=%s labels=%v", opts.Name, opts.Config.Labels)
	}
	want := "DEVICE_NAME=cashpilot-" + DeviceHostname() + "-p3"
	if !slices.Contains(opts.Config.Env, want) || argAfter(opts.Config.Cmd, "--device") != "cashpilot-"+DeviceHostname()+"-p3" {
		t.Fatalf("a proxied copy is its own device: env=%v cmd=%v", opts.Config.Env, opts.Config.Cmd)
	}

	for name, mutate := range map[string]func(*catalog.Service){
		"NET_RAW": func(s *catalog.Service) { s.Docker.CapAdd = []string{"NET_RAW"} },
		"port":    func(s *catalog.Service) { s.Docker.Ports = []string{"8080:80"} },
		"device":  func(s *catalog.Service) { s.Docker.Devices = []string{"/dev/net/tun"} },
		"host":    func(s *catalog.Service) { s.Docker.NetworkMode = "host" },
	} {
		req := deployRequest()
		svc := req.Service
		svc.Docker.CapAdd = slices.Clone(svc.Docker.CapAdd)
		mutate(&svc)
		req.Service = svc
		if _, err := earnerCreate(req, "sidecar-id", "example/earner:1.0", "linux"); err == nil {
			t.Errorf("%s: a proxied earner that could leave the tunnel must be refused", name)
		}
	}
}

func TestTunnelImageIsAlwaysExact(t *testing.T) {
	for ref, ok := range map[string]bool{
		"drumsergio/cashpilot-tunnel:1.66.0":                              true,
		"registry.local:5000/cp/tunnel@sha256:" + strings.Repeat("a", 64): true,
		"drumsergio/cashpilot-tunnel":                                     false,
		"drumsergio/cashpilot-tunnel:latest":                              false,
		"drumsergio/cashpilot-tunnel:main":                                false,
		"not an image":                                                    false,
	} {
		t.Setenv(tunnelImageEnv, ref)
		_, err := TunnelImage()
		if (err == nil) != ok {
			t.Errorf("TunnelImage with %s=%q: err=%v, want ok=%v", tunnelImageEnv, ref, err, ok)
		}
	}
	t.Setenv(tunnelImageEnv, "")
	old := pinnedTunnelImage
	t.Cleanup(func() { pinnedTunnelImage = old })
	pinnedTunnelImage = ""
	if _, err := TunnelImage(); err == nil {
		t.Fatal("with no pin and no override there is no image to run")
	}
	pinnedTunnelImage = "drumsergio/cashpilot-tunnel:1.66.0@sha256:" + strings.Repeat("b", 64)
	if img, err := TunnelImage(); err != nil || img != pinnedTunnelImage {
		t.Fatalf("the pin is used: %q %v", img, err)
	}
}

// The pin, once set, must be exact too: this is the test that fails a release that
// pins a floating tag.
func TestPinnedTunnelImageIsExactWhenSet(t *testing.T) {
	if pinnedTunnelImage == "" {
		t.Skip("no tunnel image pinned yet")
	}
	if err := exactImage(pinnedTunnelImage); err != nil || !strings.HasPrefix(pinnedTunnelImage, TunnelRepository+":") {
		t.Fatalf("pinnedTunnelImage %q: %v", pinnedTunnelImage, err)
	}
}

func TestSecretFilesAreRootOnlyAndPercentEncoded(t *testing.T) {
	withTestTunnel(t)
	files, err := tunnelSecretFiles(context.Background(), testSpec(), "192.0.2.1", "earner-p3")
	if err != nil {
		t.Fatal(err)
	}
	if files["proxy_ip"] != "198.51.100.4" || files["proxy_port"] != "7000" || files["proxy_scheme"] != "socks5" || files["own_ip"] != "192.0.2.1" {
		t.Fatalf("files: %v", files)
	}
	if files["proxy_auth"] != "user-SECRET:pa%20ss%40w%3Ard-SECRET@" { // ggignore: a made-up test value
		t.Fatalf("proxy_auth must be percent-encoded so the URL parses: %q", files["proxy_auth"])
	}
	if strings.Count(files["proxy_auth"], "@") != 1 {
		t.Fatalf("an @ in the password would end the userinfo early: %q", files["proxy_auth"])
	}
	archive, err := secretTar(files)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(bytes.NewReader(archive))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Mode != 0o400 || h.Uid != 0 || h.Gid != 0 {
			t.Errorf("%s: mode %o uid %d gid %d, want 0400 root", h.Name, h.Mode, h.Uid, h.Gid)
		}
	}
	ts, err := tunnelSecretFiles(context.Background(), ProxySpec{Kind: "tailscale", AuthKey: "tskey-SECRET", ExitNode: "exit-node-a"}, "", "Earner_P3")
	if err != nil {
		t.Fatal(err)
	}
	if ts["auth_key"] != "tskey-SECRET" || ts["exit_node"] != "exit-node-a" || ts["hostname"] != "cashpilot-earner-p3" {
		t.Fatalf("tailscale files: %v", ts)
	}
}

func TestDeployStartsTheEarnerOnlyAfterTheTunnelIsReady(t *testing.T) {
	withTestTunnel(t)
	f := newFakeTunnelDocker()
	exit, err := deployProxied(context.Background(), f, deployRequest())
	if err != nil {
		t.Fatal(err)
	}
	if exit.IP != "203.0.113.7" || exit.Country != "NL" {
		t.Fatalf("exit: %+v", exit)
	}
	calls := f.callList()
	order := []string{"create cashpilot-tunnel-earner-p3-next", "copy cashpilot-tunnel-earner-p3-next " + tunnelSecretDir, "start cashpilot-tunnel-earner-p3-next", "rename cashpilot-tunnel-earner-p3-next cashpilot-tunnel-earner-p3", "create cashpilot-earner-p3", "start cashpilot-earner-p3"}
	last := -1
	for _, step := range order {
		i := slices.Index(calls, step)
		if i <= last {
			t.Fatalf("step %q out of order in %v", step, calls)
		}
		last = i
	}
	// The secrets went only through the archive, never through a create.
	for name, c := range f.containers {
		raw, _ := json.Marshal(c.opts)
		if bytes.Contains(raw, []byte("SECRET")) {
			t.Fatalf("%s carries a secret in its create options: %s", name, raw)
		}
	}
	tunnel := f.containers["cashpilot-tunnel-earner-p3"]
	if !strings.Contains(tunnel.files["proxy_auth"], "user-SECRET") {
		t.Fatalf("the tunnel never got its credentials: %v", tunnel.files)
	}
	// A second deploy replaces the pair, earner first.
	f.calls = nil
	if _, err := deployProxied(context.Background(), f, deployRequest()); err != nil {
		t.Fatal(err)
	}
	calls = f.callList()
	if slices.Index(calls, "remove cashpilot-earner-p3") > slices.Index(calls, "remove cashpilot-tunnel-earner-p3") {
		t.Fatalf("the earner goes before its tunnel: %v", calls)
	}
}

func TestDeployFailsClosedWhenTheTunnelNeverGetsAnExit(t *testing.T) {
	withTestTunnel(t)
	f := newFakeTunnelDocker()
	f.exit = ""
	_, err := deployProxied(context.Background(), f, deployRequest())
	if err == nil {
		t.Fatal("a tunnel with no exit must refuse the copy")
	}
	if strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "pa%20ss") {
		t.Fatalf("the error carries a secret: %v", err)
	}
	if len(f.containers) != 0 {
		t.Fatalf("nothing may be left behind: %v", f.callList())
	}
	if slices.Contains(f.callList(), "create cashpilot-earner-p3") {
		t.Fatal("the earner must never be created without a ready tunnel")
	}
}

func TestEarnerAction(t *testing.T) {
	cases := []struct {
		earner, earnerAt, tunnel, tunnelAt string
		ready                              bool
		want                               string
	}{
		{"exited", "1", "running", "2", true, "start"},
		{"created", "", "running", "2", true, "start"},
		{"exited", "1", "running", "2", false, ""},
		{"exited", "1", "exited", "2", true, ""},
		{"running", "1", "running", "2", true, "restart"},
		{"running", "3", "running", "2", true, ""},
		{"running", "1", "restarting", "2", true, ""},
	}
	for _, c := range cases {
		if got := earnerAction(c.earner, c.earnerAt, c.tunnel, c.tunnelAt, c.ready); got != c.want {
			t.Errorf("earnerAction(%+v) = %q, want %q", c, got, c.want)
		}
	}
}

func TestStatusStartsAnEarnerOnlyBehindAReadyTunnel(t *testing.T) {
	withTestTunnel(t)
	f := newFakeTunnelDocker()
	if _, err := deployProxied(context.Background(), f, deployRequest()); err != nil {
		t.Fatal(err)
	}
	// A reboot: both stopped, Docker brings the tunnel back but never the earner.
	f.containers["cashpilot-earner-p3"].state = "exited"
	f.containers["cashpilot-tunnel-earner-p3"].state = "exited"
	status, err := proxiedStatus(context.Background(), f)
	if err != nil || len(status) != 1 {
		t.Fatalf("status %v %v", status, err)
	}
	if f.containers["cashpilot-earner-p3"].state != "exited" {
		t.Fatal("the earner must stay down while its tunnel is down")
	}
	f.containers["cashpilot-tunnel-earner-p3"].state = "running"
	status, _ = proxiedStatus(context.Background(), f)
	if f.containers["cashpilot-earner-p3"].state != "running" || status[0].Status != "running" {
		t.Fatalf("a ready tunnel gets its earner back: %+v", status)
	}
	if status[0].ExitIP != "203.0.113.7" || status[0].NetRxBytes == nil || *status[0].NetRxBytes != 1000 || status[0].ProxyID != 3 {
		t.Fatalf("status entry: %+v", status[0])
	}
}

func TestStopStartRemoveTouchOnlyTheirOwnPair(t *testing.T) {
	withTestTunnel(t)
	f := newFakeTunnelDocker()
	ctx := context.Background()
	if _, err := deployProxied(ctx, f, deployRequest()); err != nil {
		t.Fatal(err)
	}
	if _, err := stopProxied(ctx, f, "earner", 4, 0); err == nil {
		t.Fatal("proxy 4 has no copy here")
	}
	if wasRunning, err := stopProxied(ctx, f, "earner", 3, 0); err != nil || !wasRunning {
		t.Fatal(wasRunning, err)
	}
	if wasRunning, err := stopProxied(ctx, f, "earner", 3, 0); err != nil || wasRunning {
		t.Fatalf("a second stop finds it stopped: %v %v", wasRunning, err)
	}
	if f.containers["cashpilot-earner-p3"].state != "exited" || f.containers["cashpilot-tunnel-earner-p3"].state != "exited" {
		t.Fatal("stop stops both")
	}
	if _, err := startProxied(ctx, f, "earner", 3, 0, testSpec(), ""); err != nil {
		t.Fatal(err)
	}
	calls := f.callList()
	if slices.Index(calls, "start cashpilot-tunnel-earner-p3") > lastIndex(calls, "start cashpilot-earner-p3") {
		t.Fatalf("the tunnel starts first: %v", calls)
	}
	removed, err := removeAllProxied(ctx, f, "earner")
	if err != nil || len(removed) != 2 || removed[0] != "cashpilot-earner-p3" {
		t.Fatalf("remove all: %v %v", removed, err)
	}
}

func TestTestProxyRemovesItsTunnelAndNeedsAPublicExit(t *testing.T) {
	withTestTunnel(t)
	f := newFakeTunnelDocker()
	exit, err := testProxy(context.Background(), f, testSpec(), "")
	if err != nil || exit.IP != "203.0.113.7" {
		t.Fatalf("test: %+v %v", exit, err)
	}
	if len(f.containers) != 0 {
		t.Fatalf("the throwaway tunnel must go: %v", f.callList())
	}
	f.exit = `{"ip":"10.1.2.3","country":""}`
	if _, err := testProxy(context.Background(), f, testSpec(), ""); err == nil {
		t.Fatal("a private exit is not a pass")
	}
}

func TestRedactStripsEveryShapeOfASecret(t *testing.T) {
	msg := "dial socks5://" + testUser + ":pa%20ss%40w%3Ard-SECRET@1.2.3.4:7000 failed; raw " + testPassword
	out := redact(msg, []string{testPassword, testUser})
	if strings.Contains(out, "SECRET") {
		t.Fatalf("redact left a secret: %q", out)
	}
}

// A proxied copy has no instance label, so without the guard it would be listed as
// copy 1 of its service and overwrite the service's row.
func TestListAndCopiesSkipProxiedContainers(t *testing.T) {
	docker := startHarnessDocker(t)
	ctx := context.Background()
	p := NewDockerProvider()
	if _, err := p.Deploy(ctx, DeploySpec{Slug: "cmd-example", Service: commandService, Env: map[string]string{"TOKEN": "t"}}, nil); err != nil {
		t.Fatal(err)
	}
	cli, err := dockerClient()
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	for name, labels := range map[string]map[string]string{
		"cashpilot-cmd-example-p3":        {LabelManaged: "true", LabelService: "cmd-example", LabelProxy: "3"},
		"cashpilot-tunnel-cmd-example-p3": {LabelManaged: "true", LabelService: "cmd-example", LabelProxy: "3", LabelRole: RoleTunnel},
	} {
		if _, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{Name: name, Config: &container.Config{Image: "x", Labels: labels}}); err != nil {
			t.Fatal(err)
		}
	}
	if len(docker.Names()) != 3 {
		t.Fatalf("fixture: %v", docker.Names())
	}
	listed, err := p.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Name != "cashpilot-cmd-example" {
		t.Fatalf("List must hold only the service itself: %+v", listed)
	}
	copies, err := p.Copies(ctx, "cmd-example")
	if err != nil || len(copies) != 0 {
		t.Fatalf("Copies: %v %v", copies, err)
	}
}

func lastIndex(list []string, v string) int {
	for i := len(list) - 1; i >= 0; i-- {
		if list[i] == v {
			return i
		}
	}
	return -1
}

// An edit of the proxy reached nothing before: a start only resolved the host again
// and wrote proxy_ip. Every setting is written now, the unused ones empty.
func TestStartWritesTheProxyAsStoredNow(t *testing.T) {
	withTestTunnel(t)
	f := newFakeTunnelDocker()
	ctx := context.Background()
	if _, err := deployProxied(ctx, f, deployRequest()); err != nil {
		t.Fatal(err)
	}
	if _, err := stopProxied(ctx, f, "earner", 3, 0); err != nil {
		t.Fatal(err)
	}
	edited := testSpec()
	edited.Password, edited.Port = "n3w", 7100
	if _, err := startProxied(ctx, f, "earner", 3, 0, edited, "192.0.2.9"); err != nil {
		t.Fatal(err)
	}
	files := f.containers["cashpilot-tunnel-earner-p3"].files
	for _, k := range tunnelSecretKeys {
		if _, ok := files[k]; !ok {
			t.Errorf("%s was not written", k)
		}
	}
	if files["proxy_auth"] != "user-SECRET:n3w@" || files["proxy_port"] != "7100" || files["own_ip"] != "192.0.2.9" || files["login_server"] != "" {
		t.Fatalf("files after the edit: %v", files)
	}
	if f.containers["cashpilot-earner-p3"].state != "running" {
		t.Fatal("the earner comes back")
	}
}

func TestStartRestartsATunnelRunningOnOldSettings(t *testing.T) {
	withTestTunnel(t)
	f := newFakeTunnelDocker()
	ctx := context.Background()
	if _, err := deployProxied(ctx, f, deployRequest()); err != nil {
		t.Fatal(err)
	}
	if _, err := startProxied(ctx, f, "earner", 3, 0, testSpec(), "192.0.2.1"); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(f.callList(), "stop cashpilot-tunnel-earner-p3") {
		t.Fatal("the same settings leave the tunnel running")
	}
	edited := testSpec()
	edited.Password = "n3w"
	if _, err := startProxied(ctx, f, "earner", 3, 0, edited, "192.0.2.1"); err != nil {
		t.Fatal(err)
	}
	calls := f.callList()
	stop, copyIn := lastIndex(calls, "stop cashpilot-tunnel-earner-p3"), lastIndex(calls, "copy cashpilot-tunnel-earner-p3 "+tunnelSecretDir)
	if stop < 0 || copyIn < stop || lastIndex(calls, "stop cashpilot-earner-p3") > stop {
		t.Fatalf("earner and tunnel stop, then the new settings go in: %v", calls)
	}
	if f.containers["cashpilot-tunnel-earner-p3"].files["proxy_auth"] != "user-SECRET:n3w@" {
		t.Fatal("new password not written")
	}
}

// Keeping the old address would send the proxy login to whoever holds it now.
func TestStartStopsWhenTheHostNoLongerResolves(t *testing.T) {
	withTestTunnel(t)
	f := newFakeTunnelDocker()
	ctx := context.Background()
	if _, err := deployProxied(ctx, f, deployRequest()); err != nil {
		t.Fatal(err)
	}
	if _, err := stopProxied(ctx, f, "earner", 3, 0); err != nil {
		t.Fatal(err)
	}
	lookupIPv4 = func(context.Context, string) ([]net.IP, error) { return nil, errors.New("no such host") }
	if _, err := startProxied(ctx, f, "earner", 3, 0, testSpec(), ""); err == nil {
		t.Fatal("a start with an unresolvable host must fail")
	}
	if f.containers["cashpilot-tunnel-earner-p3"].state != "exited" {
		t.Fatal("the tunnel must stay down")
	}
}

func TestADeployThatCannotWorkKeepsTheRunningCopy(t *testing.T) {
	withTestTunnel(t)
	f := newFakeTunnelDocker()
	ctx := context.Background()
	if _, err := deployProxied(ctx, f, deployRequest()); err != nil {
		t.Fatal(err)
	}
	lookupIPv4 = func(context.Context, string) ([]net.IP, error) { return nil, errors.New("no such host") }
	if _, err := deployProxied(ctx, f, deployRequest()); err == nil {
		t.Fatal("expected a refusal")
	}
	if f.containers["cashpilot-earner-p3"] == nil || f.containers["cashpilot-tunnel-earner-p3"] == nil {
		t.Fatal("the running copy was removed for settings that could never work")
	}
}

// With LAN isolation on, every bridge earner joins the fenced network; the tunnel
// joined Docker's default bridge instead, outside the fence.
func TestTheTunnelJoinsTheFencedNetwork(t *testing.T) {
	withTestTunnel(t)
	f := newFakeTunnelDocker()
	f.network = FenceNetwork
	if _, err := deployProxied(context.Background(), f, deployRequest()); err != nil {
		t.Fatal(err)
	}
	if got := f.containers["cashpilot-tunnel-earner-p3"].opts.HostConfig.NetworkMode; got != container.NetworkMode(FenceNetwork) {
		t.Fatalf("tunnel network: %q", got)
	}
}

// A fence that cannot be applied refuses the redeploy before the running pair goes.
func TestAFenceFailureKeepsTheRunningCopy(t *testing.T) {
	withTestTunnel(t)
	f := newFakeTunnelDocker()
	ctx := context.Background()
	if _, err := deployProxied(ctx, f, deployRequest()); err != nil {
		t.Fatal(err)
	}
	f.networkErr = errors.New("the fence could not be applied")
	if _, err := deployProxied(ctx, f, deployRequest()); err == nil {
		t.Fatal("expected a refusal")
	}
	if f.containers["cashpilot-earner-p3"] == nil || f.containers["cashpilot-tunnel-earner-p3"] == nil {
		t.Fatal("the running copy was removed before the fence was known to work")
	}
}

func TestATestTunnelNeverRestartsAndAStaleOneIsSwept(t *testing.T) {
	withTestTunnel(t)
	if got := sidecarCreate("cashpilot-tunnel-test-x", testTunnelSlug, nil, "example/cashpilot-tunnel:1.66.0", "").HostConfig.RestartPolicy.Name; got != "no" {
		t.Fatalf("test tunnel restart policy: %q", got)
	}
	f := newFakeTunnelDocker()
	ctx := context.Background()
	if _, err := deployProxied(ctx, f, deployRequest()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.create(ctx, sidecarCreate("cashpilot-tunnel-test-x", testTunnelSlug, nil, "example/cashpilot-tunnel:1.66.0", "")); err != nil {
		t.Fatal(err)
	}
	f.created = time.Now().Add(-time.Minute).Unix()
	sweepTestTunnels(ctx, f, time.Now())
	if f.containers["cashpilot-tunnel-test-x"] == nil {
		t.Fatal("a test that may still run must be left alone")
	}
	f.created = time.Now().Add(-time.Hour).Unix()
	if _, err := proxiedStatus(ctx, f); err != nil {
		t.Fatal(err)
	}
	if f.containers["cashpilot-tunnel-test-x"] != nil {
		t.Fatal("the stale test tunnel was not swept")
	}
	if f.containers["cashpilot-tunnel-earner-p3"] == nil {
		t.Fatal("a copy's tunnel must never be swept")
	}
}

func TestAProbeAnswerThatIsNotAnIPIsNotReady(t *testing.T) {
	if _, ok := parseReady(`{"ip":"e","country":""}`); ok {
		t.Fatal(`"e" is not an exit IP`)
	}
	if e, ok := parseReady(`{"ip":"203.0.113.7","country":"NL"}`); !ok || e.IP != "203.0.113.7" {
		t.Fatal("a real IP is ready")
	}
}

// A redeploy whose new tunnel cannot start keeps the copy running now: the new tunnel
// proves its exit under a staged name before the old pair goes.
func TestARedeployWhoseTunnelCannotStartKeepsTheRunningCopy(t *testing.T) {
	withTestTunnel(t)
	f := newFakeTunnelDocker()
	ctx := context.Background()
	if _, err := deployProxied(ctx, f, deployRequest()); err != nil {
		t.Fatal(err)
	}
	oldEarner, oldTunnel := f.containers["cashpilot-earner-p3"].id, f.containers["cashpilot-tunnel-earner-p3"].id
	f.exit = "" // the new tunnel never sees an exit
	if _, err := deployProxied(ctx, f, deployRequest()); err == nil {
		t.Fatal("a tunnel with no exit must fail the deploy")
	}
	if e, tn := f.containers["cashpilot-earner-p3"], f.containers["cashpilot-tunnel-earner-p3"]; e == nil || tn == nil || e.id != oldEarner || tn.id != oldTunnel {
		t.Fatalf("the running copy was replaced or removed: %v", f.callList())
	}
	if f.containers["cashpilot-tunnel-earner-p3-next"] != nil {
		t.Fatal("the staged tunnel was left behind")
	}
}

// The staged tunnel takes the final name only after the old pair is gone, and keeps
// the final hostname from the start.
func TestARedeployPromotesTheStagedTunnelAfterTheOldPairGoes(t *testing.T) {
	withTestTunnel(t)
	f := newFakeTunnelDocker()
	ctx := context.Background()
	if _, err := deployProxied(ctx, f, deployRequest()); err != nil {
		t.Fatal(err)
	}
	before := len(f.callList())
	if _, err := deployProxied(ctx, f, deployRequest()); err != nil {
		t.Fatal(err)
	}
	calls := f.callList()[before:]
	ready, gone, renamed := slices.Index(calls, "start cashpilot-tunnel-earner-p3-next"), slices.Index(calls, "remove cashpilot-tunnel-earner-p3"), slices.Index(calls, "rename cashpilot-tunnel-earner-p3-next cashpilot-tunnel-earner-p3")
	if ready < 0 || gone < ready || renamed < gone || slices.Index(calls, "remove cashpilot-earner-p3") > gone {
		t.Fatalf("order: %v", calls)
	}
	if h := f.containers["cashpilot-tunnel-earner-p3"].opts.Config.Hostname; h != "tunnel-earner-p3" {
		t.Fatalf("hostname %q", h)
	}
}

// A staged tunnel a dead deploy left is replaced, never a stranger under that name;
// the status read never reports one, and sweeps it once it is stale.
func TestAStagedTunnelIsNeverReportedAndAStrangerIsNeverRemoved(t *testing.T) {
	withTestTunnel(t)
	f := newFakeTunnelDocker()
	ctx := context.Background()
	if _, err := deployProxied(ctx, f, deployRequest()); err != nil {
		t.Fatal(err)
	}
	pid := int64(3)
	if _, err := f.create(ctx, sidecarCreate("cashpilot-tunnel-earner-p3-next", "earner", &pid, "example/cashpilot-tunnel:1.66.0", "")); err != nil {
		t.Fatal(err)
	}
	f.created = time.Now().Unix()
	status, err := proxiedStatus(ctx, f)
	if err != nil || len(status) != 1 || status[0].TunnelContainerID != f.containers["cashpilot-tunnel-earner-p3"].id {
		t.Fatalf("status reported the staged tunnel: %+v %v", status, err)
	}
	f.created = time.Now().Add(-2 * staleTestTunnel).Unix()
	if _, err := proxiedStatus(ctx, f); err != nil {
		t.Fatal(err)
	}
	if f.containers["cashpilot-tunnel-earner-p3-next"] != nil || f.containers["cashpilot-tunnel-earner-p3"] == nil {
		t.Fatal("a stale staged tunnel is swept, and only it")
	}
	stranger := sidecarCreate("cashpilot-tunnel-earner-p3-next", "other", &pid, "example/cashpilot-tunnel:1.66.0", "")
	if _, err := f.create(ctx, stranger); err != nil {
		t.Fatal(err)
	}
	if _, err := deployProxied(ctx, f, deployRequest()); err == nil || !strings.Contains(err.Error(), "did not create") {
		t.Fatalf("a stranger under the staged name must stop the deploy, got %v", err)
	}
	if f.containers["cashpilot-tunnel-earner-p3-next"] == nil {
		t.Fatal("the stranger was removed")
	}
}

// A young staged tunnel belongs to a deploy still running, maybe in another app on
// the same Docker host: a second deploy refuses instead of removing it.
func TestADeployLeavesAnotherDeploysStagedTunnelAlone(t *testing.T) {
	withTestTunnel(t)
	f := newFakeTunnelDocker()
	ctx := context.Background()
	if _, err := deployProxied(ctx, f, deployRequest()); err != nil {
		t.Fatal(err)
	}
	pid := int64(3)
	if _, err := f.create(ctx, sidecarCreate("cashpilot-tunnel-earner-p3-next", "earner", &pid, "example/cashpilot-tunnel:1.66.0", "")); err != nil {
		t.Fatal(err)
	}
	staged := f.containers["cashpilot-tunnel-earner-p3-next"].id
	oldTunnel := f.containers["cashpilot-tunnel-earner-p3"].id
	f.created = time.Now().Unix()
	if _, err := deployProxied(ctx, f, deployRequest()); err == nil || !strings.Contains(err.Error(), "under way") {
		t.Fatalf("a young staged tunnel must stop the deploy, got %v", err)
	}
	if c := f.containers["cashpilot-tunnel-earner-p3-next"]; c == nil || c.id != staged {
		t.Fatal("the other deploy's staged tunnel was removed")
	}
	if c := f.containers["cashpilot-tunnel-earner-p3"]; c == nil || c.id != oldTunnel {
		t.Fatal("the running pair was touched")
	}
	f.created = time.Now().Add(-2 * staleTestTunnel).Unix()
	if _, err := deployProxied(ctx, f, deployRequest()); err != nil {
		t.Fatalf("an abandoned staged tunnel is replaced: %v", err)
	}
}

// The sweep leaves a stale-looking staged tunnel alone while a deploy of its copy in
// this app still holds the copy's lock.
func TestTheSweepSkipsAStagedTunnelWhoseDeployIsRunning(t *testing.T) {
	withTestTunnel(t)
	f := newFakeTunnelDocker()
	ctx := context.Background()
	if _, err := deployProxied(ctx, f, deployRequest()); err != nil {
		t.Fatal(err)
	}
	pid := int64(3)
	if _, err := f.create(ctx, sidecarCreate("cashpilot-tunnel-earner-p3-next", "earner", &pid, "example/cashpilot-tunnel:1.66.0", "")); err != nil {
		t.Fatal(err)
	}
	f.created = time.Now().Add(-2 * staleTestTunnel).Unix()
	mu := deployLock("earner", 3)
	mu.Lock()
	sweepTestTunnels(ctx, f, time.Now())
	if f.containers["cashpilot-tunnel-earner-p3-next"] == nil {
		mu.Unlock()
		t.Fatal("the sweep removed a staged tunnel its deploy still holds")
	}
	mu.Unlock()
	sweepTestTunnels(ctx, f, time.Now())
	if f.containers["cashpilot-tunnel-earner-p3-next"] != nil {
		t.Fatal("once the deploy is done the stale staged tunnel is swept")
	}
}

// A swap that fails after the old pair went removes the staged tunnel, so nothing is
// left running under a name the status read never reports.
func TestAFailedPromotionRemovesTheStagedTunnel(t *testing.T) {
	withTestTunnel(t)
	f := newFakeTunnelDocker()
	ctx := context.Background()
	f.renameErr = errors.New("409 Conflict")
	if _, err := deployProxied(ctx, f, deployRequest()); err == nil || !strings.Contains(err.Error(), "409") {
		t.Fatalf("the failed rename must fail the deploy, got %v", err)
	}
	if f.containers["cashpilot-tunnel-earner-p3-next"] != nil || f.containers["cashpilot-earner-p3"] != nil {
		t.Fatalf("left behind: %v", f.callList())
	}
}

// Two deploys of one copy take turns, so the second never removes the first one's
// staged tunnel as a leftover.
func TestTwoDeploysOfOneCopyTakeTurns(t *testing.T) {
	withTestTunnel(t)
	f := newFakeTunnelDocker()
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = deployProxied(ctx, f, deployRequest())
		}(i)
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("both deploys must succeed one after the other: %v", errs)
	}
	if f.containers["cashpilot-tunnel-earner-p3"] == nil || f.containers["cashpilot-earner-p3"] == nil || f.containers["cashpilot-tunnel-earner-p3-next"] != nil {
		t.Fatalf("containers after two deploys: %v", f.callList())
	}
}

// The live test removes fixed names before it runs; a container of the operator's
// under one of those names must stop it instead of being removed.
func TestTheLiveTestNeverRemovesAContainerItDidNotCreate(t *testing.T) {
	ours := map[string]string{LabelManaged: "true", LabelService: "mp-live", LabelProxy: "1"}
	containers := map[string]map[string]string{"cashpilot-mp-live-p1": ours, "cp-mp-live-gost": {"com.example": "mine"}}
	inspect := func(name string) (map[string]string, bool, error) {
		labels, ok := containers[name]
		return labels, ok, nil
	}
	owned := map[string]func(map[string]string) bool{
		"cashpilot-mp-live-p1":        func(l map[string]string) bool { return isOurs(l, "mp-live", 1) },
		"cashpilot-tunnel-mp-live-p1": func(l map[string]string) bool { return isOurs(l, "mp-live", 1) },
		"cp-mp-live-gost":             func(l map[string]string) bool { return l[liveTestLabel] == "mp-live" },
	}
	err := refuseForeignLiveContainers(inspect, owned)
	if err == nil || !strings.Contains(err.Error(), "cp-mp-live-gost") || strings.Contains(err.Error(), "cashpilot-mp-live-p1") {
		t.Fatalf("only the stranger must be refused, got %v", err)
	}
	containers["cp-mp-live-gost"] = map[string]string{liveTestLabel: "mp-live"}
	if err := refuseForeignLiveContainers(inspect, owned); err != nil {
		t.Fatalf("containers this test created, or none, must pass: %v", err)
	}
}

// A tunnel removed between the read and the stop must not leave its earner running.
func TestAStopStillStopsTheEarnerWhenTheTunnelIsGone(t *testing.T) {
	withTestTunnel(t)
	f := newFakeTunnelDocker()
	ctx := context.Background()
	if _, err := deployProxied(ctx, f, deployRequest()); err != nil {
		t.Fatal(err)
	}
	f.stopErr = map[string]error{"cashpilot-tunnel-earner-p3": fmt.Errorf("No such container: %w", cerrdefs.ErrNotFound)}
	if _, err := stopProxied(ctx, f, "earner", 3, 0); err != nil {
		t.Fatalf("a tunnel that is gone must not fail the stop: %v", err)
	}
	if st := f.containers["cashpilot-earner-p3"].state; st == "running" {
		t.Fatal("the earner was left running after its tunnel disappeared")
	}
}

// A stop that fails halfway must not leave a ready tunnel beside a stopped earner,
// which the next status read would start again, undoing the owner's stop.
func TestAStopThatFailsHalfwayIsNotUndoneByTheStatusRead(t *testing.T) {
	for _, failing := range []string{"cashpilot-tunnel-earner-p3", "cashpilot-earner-p3"} {
		t.Run(failing, func(t *testing.T) {
			withTestTunnel(t)
			f := newFakeTunnelDocker()
			ctx := context.Background()
			if _, err := deployProxied(ctx, f, deployRequest()); err != nil {
				t.Fatal(err)
			}
			f.stopErr = map[string]error{failing: errors.New("daemon timeout")}
			if _, err := stopProxied(ctx, f, "earner", 3, 0); err == nil {
				t.Fatal("the failed stop must be reported")
			}
			earner, tunnel := f.containers["cashpilot-earner-p3"], f.containers["cashpilot-tunnel-earner-p3"]
			if tunnel.state == "running" && earner.state != "running" {
				t.Fatalf("a ready tunnel was left beside a stopped earner")
			}
			before := len(f.callList())
			if _, err := proxiedStatus(ctx, f); err != nil {
				t.Fatal(err)
			}
			for _, call := range f.callList()[before:] {
				if call == "start cashpilot-earner-p3" || call == "restart cashpilot-earner-p3" {
					t.Fatalf("the status read started the earner again: %v", f.callList()[before:])
				}
			}
		})
	}
}
