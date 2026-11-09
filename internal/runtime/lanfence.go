package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/proxies"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// Earners fenced off the LAN by default, the same mechanism as the CashPilot worker
// (web app/lan_fence.py, GeiserX/CashPilot#574).
//
// Docker's default bridge lets every container reach the LAN, this machine, a tailnet
// and cloud metadata. These images are closed-source and run whatever their vendor
// ships, so a new install now puts them on a bridge of CashPilot's own instead:
//
//   - the network cashpilot-isolated (interface cp-isolated), with Docker's own
//     masquerade and inter-container traffic off, labelled cashpilot.isolation=managed;
//   - one nftables table, "ip cashpilot", written into the daemon's network namespace
//     by a one-shot helper container (host networking, NET_ADMIN). The table holds the
//     masquerade that gives the earners internet plus drops for 10/8, 172.16/12,
//     192.168/16, 100.64/10, 169.254/16, directly connected subnets and the host
//     itself; on a host with IPv6 an "ip6 cashpilot" table drops all IPv6 from the
//     bridge. Kernels without nf_tables get the same rules through iptables-legacy.
//
// NAT and drops share one table, so if something deletes it the earners lose the
// internet instead of gaining the LAN. The table is written again at startup, before
// every deploy and every five minutes.
//
// The helper runs the worker image's own rule writer (app/lan_fence.py HELPER_SCRIPT),
// so the rules are the web's, byte for byte, at the pinned image version. On Docker
// Desktop and colima the daemon's namespace is the Linux VM's, which is where the
// containers' traffic leaves from, so the fence sits in the right place there too.
//
// Podman is not fenced. Its Docker-compatible API keeps the interface name out of the
// network's options and drops the masquerade and inter-container options, and netavark
// adds its own masquerade, so none of what the fence relies on can be set or checked
// there. A Podman daemon resolves to the unsupported mode instead of refusing every
// deploy: earners run on the default network and the dashboard says they are not fenced.
//
// Modes (the state lives in Docker and in the app's config, so losing either one
// alone changes nothing):
//
//   - off: Settings say off. Docker's default bridge, as before. If our network still
//     exists, the table keeps only its masquerade, so earners still on it keep their
//     internet until they are redeployed.
//   - on: Settings say on, or the config remembers isolation was on (written the first
//     time the mode resolves to on, cleared by off), or our labelled network exists,
//     or a managed earner is still bound to it after it was removed, or no managed
//     earner sits on the default bridge (a fresh install).
//   - legacy: an upgraded install whose earners are on the default bridge. Nothing
//     changes until the user turns isolation on from the dashboard.
//   - unsupported: a Podman daemon with Settings not off. Earners deploy on the default
//     network; with Settings on, deploys are refused, since on was asked for and cannot
//     be given.
//
// A proxied copy's tunnel sidecar (cashpilot.role=tunnel) never counts as outside and
// never makes an install legacy: its own firewall drops every packet but those to the
// proxy and through the tunnel, private ranges included (the tunnel image's cp-tunnel).

const (
	FenceNetwork = "cashpilot-isolated"
	// Linux caps interface names at 15 characters, so the network's own name cannot
	// double as the interface name.
	fenceBridge     = "cp-isolated"
	fenceLabel      = "cashpilot.isolation"
	fenceLabelValue = "managed"

	FenceModeOff    = "off"
	FenceModeOn     = "on"
	FenceModeLegacy = "legacy"
	// FenceModeUnsupported is a runtime the fence cannot work on (Podman).
	FenceModeUnsupported = "unsupported"

	// FenceRecheck is how often the app puts the table back if it went.
	FenceRecheck = 5 * time.Minute

	fenceHelperName    = "cashpilot-fence"
	fenceProbeLabel    = "cashpilot.probe"
	fenceHelperTimeout = 60 * time.Second

	optBridgeName = "com.docker.network.bridge.name"
	optMasquerade = "com.docker.network.bridge.enable_ip_masquerade"
	optICC        = "com.docker.network.bridge.enable_icc"
)

// fenceHelperImage is the CashPilot worker image the helper runs. It carries nft,
// iptables-legacy and app/lan_fence.py; the worker starts the same helper from its own
// image. Pinned by digest, like the tunnel image: the helper runs with host networking
// and NET_ADMIN, so a re-pushed tag must not change what it runs. A var so a live test
// can point it at a local build.
var fenceHelperImage = "drumsergio/cashpilot-worker:1.70.0@sha256:1f0fccd176b1ad6128665bdde36c20f5c070a02049ef405d0d45ebb769af5b64"

// fenceDNS are the resolvers an earner on the fenced bridge gets. Docker's embedded
// resolver forwards a container's lookups from inside its network, so a daemon whose
// list starts with a LAN or tailnet resolver would be refused by the very fence that
// isolates the bridge. The web worker's default (CASHPILOT_CONTAINER_DNS).
var fenceDNS = []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("9.9.9.9")}

// fenceHelperScript runs inside the helper. It imports the image's own module and
// runs its helper, which reads sys.argv[1] ("apply" or "read") and prints one JSON
// object on its last line.
const fenceHelperScript = "from app import lan_fence as f; exec(f.HELPER_SCRIPT)"

// FenceStatus is what the dashboard shows. A copy of the cached state; reading it
// costs no Docker call.
type FenceStatus struct {
	// Mode is off, on, legacy or unsupported, or "" while Docker cannot be reached.
	Mode string `json:"mode"`
	// Setting is what Settings ask for: auto, on or off.
	Setting string `json:"setting"`
	// State is applied, reduced (masquerade only), failed, unsupported or n/a.
	State   string `json:"state"`
	Backend string `json:"backend"`
	Error   string `json:"error"`
	// Outside names the running earners that can reach the LAN. A redeploy moves them.
	Outside []string `json:"outside"`
	// OutsideStopped names stopped earners outside the fence. They are left alone and
	// move the next time they are deployed.
	OutsideStopped []string `json:"outsideStopped"`
}

// fenceAPI is the part of the Docker client the fence uses, so the decisions can be
// tested against a fake daemon.
type fenceAPI interface {
	NetworkList(ctx context.Context, options client.NetworkListOptions) (client.NetworkListResult, error)
	NetworkInspect(ctx context.Context, networkID string, options client.NetworkInspectOptions) (client.NetworkInspectResult, error)
	NetworkCreate(ctx context.Context, name string, options client.NetworkCreateOptions) (client.NetworkCreateResult, error)
	NetworkRemove(ctx context.Context, networkID string, options client.NetworkRemoveOptions) (client.NetworkRemoveResult, error)
	NetworkConnect(ctx context.Context, networkID string, options client.NetworkConnectOptions) (client.NetworkConnectResult, error)
	NetworkDisconnect(ctx context.Context, networkID string, options client.NetworkDisconnectOptions) (client.NetworkDisconnectResult, error)
	ContainerList(ctx context.Context, options client.ContainerListOptions) (client.ContainerListResult, error)
	ContainerInspect(ctx context.Context, containerID string, options client.ContainerInspectOptions) (client.ContainerInspectResult, error)
	ContainerCreate(ctx context.Context, options client.ContainerCreateOptions) (client.ContainerCreateResult, error)
	ContainerStart(ctx context.Context, containerID string, options client.ContainerStartOptions) (client.ContainerStartResult, error)
	ContainerStop(ctx context.Context, containerID string, options client.ContainerStopOptions) (client.ContainerStopResult, error)
	ContainerWait(ctx context.Context, containerID string, options client.ContainerWaitOptions) client.ContainerWaitResult
	ContainerLogs(ctx context.Context, containerID string, options client.ContainerLogsOptions) (client.ContainerLogsResult, error)
	ContainerRemove(ctx context.Context, containerID string, options client.ContainerRemoveOptions) (client.ContainerRemoveResult, error)
	ImageInspect(ctx context.Context, imageID string, inspectOpts ...client.ImageInspectOption) (client.ImageInspectResult, error)
	ImagePull(ctx context.Context, refStr string, options client.ImagePullOptions) (client.ImagePullResponse, error)
	ServerVersion(ctx context.Context, options client.ServerVersionOptions) (client.ServerVersionResult, error)
}

// isPodman reports a Podman daemon behind the Docker-compatible API. Podman names
// itself in /version's components ("Podman Engine"). A daemon that does not answer
// is taken for Docker: the fence's own checks still refuse a network it cannot hold.
func isPodman(ctx context.Context, cli fenceAPI) bool {
	v, err := cli.ServerVersion(ctx, client.ServerVersionOptions{})
	if err != nil {
		return false
	}
	if strings.Contains(strings.ToLower(v.Platform.Name), "podman") {
		return true
	}
	for _, c := range v.Components {
		if strings.Contains(strings.ToLower(c.Name), "podman") {
			return true
		}
	}
	return false
}

const podmanUnsupported = "CashPilot cannot fence earners off the LAN on Podman yet: its Docker-compatible API does not keep the network settings the fence relies on"

// fence holds the cached status and serialises the helper: two helpers writing the
// table at once would race on the same container name.
type fence struct {
	setting func() string
	// remembered and remember keep "isolation on" in the app's config. Docker's own
	// evidence (our network, an earner bound to it) is gone once every fenced earner
	// was removed and a prune or a runtime reset took the network; without the memory
	// a stopped earner left on the default bridge would read as an upgraded install.
	remembered func() bool
	remember   func(on bool)
	// lookupEgress returns this machine's public IP, "" when it cannot be read. Most
	// home routers loop a connection to their own public address back into the LAN
	// (NAT loopback), so the table drops that address too. egress and egressAt cache
	// it; mu guards them.
	lookupEgress func(ctx context.Context) string
	// gatedSlug reports a service the catalog marks fence_gated (SetFenceGated).
	gatedSlug   func(slug string) bool
	egress      string
	egressAt    time.Time
	mu          sync.Mutex // the helper and the apply sequence
	reconcileMu sync.Mutex // whole reconciles, so one cannot overwrite a later one's status
	stateMu     sync.Mutex // status
	status      FenceStatus
}

// FenceSetting normalises a Settings value: auto, on or off. Anything else is auto.
func FenceSetting(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "on", "true", "1", "yes":
		return "on"
	case "off", "false", "0", "no":
		return "off"
	}
	return "auto"
}

func (f *fence) wasOn() bool {
	return f != nil && f.remembered != nil && f.remembered()
}

// record keeps whether isolation is on, writing only on a change.
func (f *fence) record(on bool) {
	if f == nil || f.remember == nil || on == f.wasOn() {
		return
	}
	f.remember(on)
}

func (f *fence) chosen() string {
	if f == nil || f.setting == nil {
		return "auto"
	}
	return FenceSetting(f.setting())
}

// fenceEarner is what the fence needs to know about one managed container.
type fenceEarner struct {
	ID      string
	Name    string
	Slug    string
	Running bool
	// Tunnel is a proxied copy's sidecar, which carries its own firewall.
	Tunnel      bool
	NetworkMode string
	// Networks maps each endpoint's network name to the network ID it was bound to.
	Networks map[string]string
}

func earnerFromSummary(c container.Summary) fenceEarner {
	e := fenceEarner{
		ID:          c.ID,
		Slug:        c.Labels[LabelService],
		Running:     strings.EqualFold(string(c.State), "running"),
		Tunnel:      c.Labels[LabelRole] == RoleTunnel,
		NetworkMode: c.HostConfig.NetworkMode,
		Networks:    map[string]string{},
	}
	if len(c.Names) > 0 {
		e.Name = strings.TrimPrefix(c.Names[0], "/")
	}
	if c.NetworkSettings != nil {
		for name, ep := range c.NetworkSettings.Networks {
			id := ""
			if ep != nil {
				id = ep.NetworkID
			}
			e.Networks[name] = id
		}
	}
	return e
}

// onDefaultBridge reports a container on Docker's default bridge: the exposed ones.
func onDefaultBridge(e fenceEarner) bool {
	if e.NetworkMode == "bridge" || e.NetworkMode == "default" {
		return true
	}
	_, ok := e.Networks["bridge"]
	return ok
}

// resolveFenceMode decides the mode from the setting, what Docker holds and whether
// isolation was on before.
func resolveFenceMode(setting string, managedNetwork, boundToRemoved, rememberedOn bool, earners []fenceEarner) string {
	switch setting {
	case "off":
		return FenceModeOff
	case "on":
		return FenceModeOn
	}
	if managedNetwork || boundToRemoved || rememberedOn {
		return FenceModeOn
	}
	for _, e := range earners {
		if !e.Tunnel && onDefaultBridge(e) {
			return FenceModeLegacy
		}
	}
	return FenceModeOn
}

// boundToRemoved reports a managed container that still names our network after the
// network was removed. Each earner keeps its endpoint by name with the old ID, so
// this is the proof isolation was on when the network itself is gone.
func boundToRemoved(networkExists bool, earners []fenceEarner) bool {
	if networkExists {
		return false
	}
	for _, e := range earners {
		if _, ok := e.Networks[FenceNetwork]; ok {
			return true
		}
	}
	return false
}

// outsideFence splits the earners that are not on the network into running and
// stopped. Host, none and container: networking are the service's own choice and
// never count, and neither does a tunnel sidecar, whose own firewall closes the LAN.
func outsideFence(earners []fenceEarner, onNetwork string, legacy bool) (running, stopped []string) {
	runSet, stopSet := map[string]bool{}, map[string]bool{}
	for _, e := range earners {
		if e.Tunnel {
			continue
		}
		mode := e.NetworkMode
		if mode == "" {
			mode = "bridge"
		}
		if mode == "host" || mode == "none" || strings.HasPrefix(mode, "container:") {
			continue
		}
		if legacy {
			if !onDefaultBridge(e) {
				continue
			}
		} else if onlyOn(e, onNetwork) {
			continue
		}
		label := e.Slug
		if label == "" {
			label = e.Name
		}
		if e.Running {
			runSet[label] = true
		} else {
			stopSet[label] = true
		}
	}
	for label := range runSet {
		running = append(running, label)
	}
	for label := range stopSet {
		if !runSet[label] {
			stopped = append(stopped, label)
		}
	}
	sort.Strings(running)
	sort.Strings(stopped)
	return running, stopped
}

// onlyOn reports an earner whose only network is the given one. One also attached
// to another network can reach whatever that network reaches, so it is outside.
func onlyOn(e fenceEarner, network string) bool {
	if _, ok := e.Networks[network]; !ok {
		return false
	}
	return len(e.Networks) == 1
}

// staleEndpoint reports a container bound to an earlier network of ours, by its ID.
func staleEndpoint(e fenceEarner, currentID string) bool {
	old := e.Networks[FenceNetwork]
	return old != "" && old != currentID
}

func listEarners(ctx context.Context, cli fenceAPI) ([]fenceEarner, error) {
	res, err := cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: make(client.Filters).Add("label", LabelManaged+"=true")})
	if err != nil {
		return nil, err
	}
	out := make([]fenceEarner, 0, len(res.Items))
	for _, c := range res.Items {
		out = append(out, earnerFromSummary(c))
	}
	return out, nil
}

// managedNetwork returns our labelled network, or nil.
func managedNetwork(ctx context.Context, cli fenceAPI) (*network.Inspect, error) {
	res, err := cli.NetworkList(ctx, client.NetworkListOptions{Filters: make(client.Filters).Add("label", fenceLabel+"="+fenceLabelValue)})
	if err != nil {
		return nil, err
	}
	for _, n := range res.Items {
		if n.Name != FenceNetwork {
			continue
		}
		got, err := cli.NetworkInspect(ctx, n.ID, client.NetworkInspectOptions{})
		if err != nil {
			return nil, err
		}
		return &got.Network, nil
	}
	return nil, nil
}

// networkByName returns any network called FenceNetwork, ours or not, or nil.
func networkByName(ctx context.Context, cli fenceAPI) (*network.Inspect, error) {
	got, err := cli.NetworkInspect(ctx, FenceNetwork, client.NetworkInspectOptions{})
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	if got.Network.Name != FenceNetwork {
		// Docker matches an ID prefix too; a network whose ID starts with our name is
		// not ours by name.
		return nil, nil
	}
	return &got.Network, nil
}

// fenceNetworkOptions is how our network is created. The fence's own table does the
// NAT, so no table means no internet.
func fenceNetworkOptions() client.NetworkCreateOptions {
	off := false
	return client.NetworkCreateOptions{
		Driver: "bridge",
		Options: map[string]string{
			optBridgeName: fenceBridge,
			optMasquerade: "false",
			optICC:        "false",
		},
		Labels:     map[string]string{fenceLabel: fenceLabelValue},
		EnableIPv6: &off,
	}
}

// checkFenceNetwork refuses a network whose interface is not cp-isolated. Every rule
// matches that interface name, so a runtime that ignored the option would leave the
// earners on a bridge no rule applies to.
func checkFenceNetwork(n *network.Inspect) error {
	if n.Driver != "bridge" {
		return fmt.Errorf("the %s network uses the %q driver, not bridge", FenceNetwork, n.Driver)
	}
	if n.Options[optBridgeName] != fenceBridge {
		return fmt.Errorf("this container runtime did not name the %s network's interface %s, so the fence's rules would not match it", FenceNetwork, fenceBridge)
	}
	// Docker's own masquerade would give the earners internet with no table, and
	// traffic between containers would pass no rule of ours. Both default to on, so
	// a missing option is as bad as "true".
	for _, opt := range []string{optMasquerade, optICC} {
		if !strings.EqualFold(n.Options[opt], "false") {
			return fmt.Errorf("%w: the %s network does not have %s=false, so the fence cannot hold on it. Remove the network so CashPilot can create it again", errFenceConflict, FenceNetwork, opt)
		}
	}
	return nil
}

var errFenceConflict = errors.New("fence conflict")

// ensureNetwork returns our network, created when missing.
func ensureNetwork(ctx context.Context, cli fenceAPI) (*network.Inspect, error) {
	net, err := managedNetwork(ctx, cli)
	if err != nil {
		return nil, err
	}
	if net != nil {
		return net, checkFenceNetwork(net)
	}
	other, err := networkByName(ctx, cli)
	if err != nil {
		return nil, err
	}
	if other != nil {
		if len(other.Containers) > 0 {
			return nil, fmt.Errorf("%w: a network named %s already exists, CashPilot did not create it, and containers use it. Remove it so CashPilot can create its own", errFenceConflict, FenceNetwork)
		}
		if _, err := cli.NetworkRemove(ctx, other.ID, client.NetworkRemoveOptions{}); err != nil {
			return nil, err
		}
	}
	created, err := cli.NetworkCreate(ctx, FenceNetwork, fenceNetworkOptions())
	if err != nil {
		return nil, err
	}
	got, err := cli.NetworkInspect(ctx, created.ID, client.NetworkInspectOptions{})
	if err != nil {
		return nil, err
	}
	return &got.Network, checkFenceNetwork(&got.Network)
}

// reattach binds earners left on a removed copy of our network to the current one.
// Docker keeps a container's network by ID, so after the network is removed (Unraid
// does it on every boot, a Docker Desktop reset does it too) each earner fails to
// start with "network <id> not found". One that the missing network stopped is
// started again; one the user stopped is moved and left stopped. Never fails.
func reattach(ctx context.Context, cli fenceAPI, netID string, earners []fenceEarner) []string {
	var moved []string
	for _, e := range earners {
		if e.Running || !staleEndpoint(e, netID) {
			continue
		}
		state := ""
		if got, err := cli.ContainerInspect(ctx, e.ID, client.ContainerInspectOptions{}); err == nil && got.Container.State != nil {
			state = got.Container.State.Error
		}
		_, _ = cli.NetworkDisconnect(ctx, netID, client.NetworkDisconnectOptions{Container: e.ID, Force: true})
		if _, err := cli.NetworkConnect(ctx, netID, client.NetworkConnectOptions{Container: e.ID}); err != nil {
			continue
		}
		if strings.Contains(state, "network") && strings.Contains(state, "not found") {
			_, _ = cli.ContainerStart(ctx, e.ID, client.ContainerStartOptions{})
		}
		moved = append(moved, e.Name)
	}
	return moved
}

func networkSubnet(n *network.Inspect) (string, error) {
	for _, c := range n.IPAM.Config {
		if c.Subnet.IsValid() && c.Subnet.Addr().Is4() {
			return c.Subnet.String(), nil
		}
	}
	return "", fmt.Errorf("the %s network has no IPv4 subnet", FenceNetwork)
}

// fenceResult is what the helper prints.
type fenceResult struct {
	Backend  string         `json:"backend"`
	OK       bool           `json:"ok"`
	Changed  bool           `json:"changed"`
	Error    string         `json:"error"`
	Present  bool           `json:"present"`
	Matches  bool           `json:"matches"`
	Counters map[string]int `json:"counters"`
}

// parseFenceResult reads the helper's last stdout line.
func parseFenceResult(stdout string) (fenceResult, error) {
	var r fenceResult
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if last == "" {
		return r, errors.New("the fence helper printed nothing")
	}
	if err := json.Unmarshal([]byte(last), &r); err != nil {
		return r, fmt.Errorf("the fence helper printed something that is not its result: %w", err)
	}
	return r, nil
}

// fenceHelperSpec is the helper container. It needs NET_ADMIN in the daemon's network
// namespace to write the table, and nothing else: every other capability is dropped,
// its root filesystem is read-only, and it runs our own script, never an earner.
// egress is the public IPv4 to drop as well, "" for none.
func fenceHelperSpec(action, subnet, egress string, filtered bool) (*container.Config, *container.HostConfig) {
	filter := "1"
	if !filtered {
		filter = "0"
	}
	pids := int64(64)
	cfg := &container.Config{
		Image:      fenceHelperImage,
		Entrypoint: []string{"python", "-c", fenceHelperScript},
		Cmd:        []string{action},
		Env: []string{
			"FENCE_IFACE=" + fenceBridge,
			"FENCE_SUBNET=" + subnet,
			"FENCE_FILTER=" + filter,
			"FENCE_BACKEND=auto",
			"FENCE_EGRESS=" + egress,
		},
		User:        "0:0",
		Labels:      map[string]string{fenceProbeLabel: "fence"},
		Healthcheck: &container.HealthConfig{Test: []string{"NONE"}},
	}
	host := &container.HostConfig{
		NetworkMode:    "host",
		ReadonlyRootfs: true,
		// iptables-legacy takes its lock in /run.
		Tmpfs:   map[string]string{"/run": "size=1m"},
		CapDrop: []string{"ALL"},
		// NET_RAW only matters to iptables-legacy, which talks to the kernel over a
		// raw socket.
		CapAdd:      []string{"NET_ADMIN", "NET_RAW"},
		SecurityOpt: []string{noNewPrivileges},
		Privileged:  false,
	}
	host.PidsLimit = &pids
	host.Memory = 64 << 20
	return cfg, host
}

func ensureHelperImage(ctx context.Context, cli fenceAPI) error {
	if _, err := cli.ImageInspect(ctx, fenceHelperImage); err == nil {
		return nil
	}
	reader, err := cli.ImagePull(ctx, fenceHelperImage, client.ImagePullOptions{})
	if err != nil {
		return fmt.Errorf("could not pull %s: %w", fenceHelperImage, err)
	}
	defer reader.Close()
	if err := streamPullProgress(reader, nil); err != nil {
		return fmt.Errorf("could not pull %s: %w", fenceHelperImage, err)
	}
	return nil
}

// helperGrace is how long a helper that was just created or has just exited still
// belongs to the process that started it: that process starts it, then reads its
// logs and removes it, all within moments. helperPoll is how often a busy helper is
// looked at again. Vars so a test can shorten them.
var (
	helperGrace = 10 * time.Second
	helperPoll  = 250 * time.Millisecond
)

// helperBusy reports a helper another process is still using: running, or created or
// exited within helperGrace.
func helperBusy(st *container.State, created string, now time.Time) bool {
	if st == nil {
		return false
	}
	if st.Running || st.Restarting {
		return true
	}
	recent := func(stamp string) bool {
		t, err := time.Parse(time.RFC3339Nano, stamp)
		return err == nil && now.Sub(t) < helperGrace
	}
	switch st.Status {
	case container.StateCreated:
		return recent(created)
	case container.StateExited, container.StateDead:
		return recent(st.FinishedAt)
	}
	return false
}

// clearHelper makes room for this process's helper. Only a container that carries
// the helper's label is touched: a container someone else named cashpilot-fence is
// left alone, and the create that follows fails on the name instead.
//
// The window and the background daemon both run the fence, and the web worker's
// helper uses the same name and label, so a helper of ours can belong to another
// process that is still waiting for its result. That one is waited for, never killed:
// killing it would make the other side record a failed fence and refuse its deploy.
// Only a leftover, one idle past helperGrace or still there after the helper timeout,
// is removed.
func clearHelper(ctx context.Context, cli fenceAPI) {
	deadline := time.Now().Add(fenceHelperTimeout + helperGrace)
	for {
		got, err := cli.ContainerInspect(ctx, fenceHelperName, client.ContainerInspectOptions{})
		if err != nil || got.Container.Config == nil || got.Container.Config.Labels[fenceProbeLabel] != "fence" {
			return
		}
		now := time.Now()
		if !helperBusy(got.Container.State, got.Container.Created, now) || now.After(deadline) {
			_, _ = cli.ContainerRemove(context.Background(), got.Container.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(helperPoll):
		}
	}
}

func helperLogs(ctx context.Context, cli fenceAPI, id string, stdout bool) string {
	reader, err := cli.ContainerLogs(ctx, id, client.ContainerLogsOptions{ShowStdout: stdout, ShowStderr: !stdout})
	if err != nil {
		return ""
	}
	defer reader.Close()
	raw, _ := io.ReadAll(io.LimitReader(reader, 1<<20))
	return stripDockerLogHeaders(raw)
}

// runHelper runs the helper to completion and returns what it printed. The helper is
// removed on every path. The caller holds f.mu.
func runHelper(ctx context.Context, cli fenceAPI, action, subnet, egress string, filtered bool) (fenceResult, error) {
	if err := ensureHelperImage(ctx, cli); err != nil {
		return fenceResult{}, err
	}
	cfg, host := fenceHelperSpec(action, subnet, egress, filtered)
	var created client.ContainerCreateResult
	var err error
	// Twice: another process can create its helper between our clear and our create.
	for attempt := 0; attempt < 2; attempt++ {
		clearHelper(ctx, cli)
		created, err = cli.ContainerCreate(ctx, client.ContainerCreateOptions{Config: cfg, HostConfig: host, Name: fenceHelperName})
		if err == nil || !cerrdefs.IsConflict(err) {
			break
		}
	}
	if err != nil {
		return fenceResult{}, err
	}
	// Ours by ID, so the cleanup can never hit another container.
	defer func() {
		_, _ = cli.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
	}()
	waitCtx, cancel := context.WithTimeout(ctx, fenceHelperTimeout)
	defer cancel()
	wait := cli.ContainerWait(waitCtx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNextExit})
	if _, err := cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return fenceResult{}, err
	}
	var code int64
	select {
	case res := <-wait.Result:
		code = res.StatusCode
	case err := <-wait.Error:
		return fenceResult{}, fmt.Errorf("the fence helper did not finish: %w", err)
	}
	if code != 0 {
		errText := helperLogs(ctx, cli, created.ID, false)
		if len(errText) > 300 {
			errText = errText[len(errText)-300:]
		}
		return fenceResult{}, fmt.Errorf("the fence helper exited with status %d: %s", code, strings.TrimSpace(errText))
	}
	return parseFenceResult(helperLogs(ctx, cli, created.ID, true))
}

const fenceOffHint = "Set LAN isolation to off in Settings to deploy on Docker's default bridge instead, without the fence."

func (f *fence) setStatus(update func(*FenceStatus)) {
	f.stateMu.Lock()
	defer f.stateMu.Unlock()
	update(&f.status)
}

// Status is a copy of the cached state.
func (f *fence) Status() FenceStatus {
	f.stateMu.Lock()
	defer f.stateMu.Unlock()
	s := f.status
	s.Setting = f.chosen()
	s.Outside = append([]string(nil), s.Outside...)
	s.OutsideStopped = append([]string(nil), s.OutsideStopped...)
	return s
}

func (f *fence) recordResult(state string, res fenceResult, err error) {
	f.setStatus(func(s *FenceStatus) {
		s.State, s.Backend, s.Error = state, res.Backend, res.Error
		if err != nil {
			s.Error = err.Error()
		}
	})
}

// apply makes sure our network and its full table exist.
func (f *fence) apply(ctx context.Context, cli fenceAPI) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	res, err := f.applyLocked(ctx, cli)
	if err != nil {
		f.recordResult("failed", res, err)
		return err
	}
	f.recordResult("applied", res, nil)
	return nil
}

func (f *fence) applyLocked(ctx context.Context, cli fenceAPI) (fenceResult, error) {
	net, err := ensureNetwork(ctx, cli)
	if err != nil {
		if errors.Is(err, errFenceConflict) {
			return fenceResult{}, err
		}
		return fenceResult{}, fmt.Errorf("The LAN fence could not be applied on this machine: %w.", err)
	}
	subnet, err := networkSubnet(net)
	if err != nil {
		return fenceResult{}, fmt.Errorf("The LAN fence could not be applied on this machine: %w.", err)
	}
	res, err := runHelper(ctx, cli, "apply", subnet, f.egressIP(ctx), true)
	if err != nil {
		return res, fmt.Errorf("The LAN fence could not be applied on this machine: %w.", err)
	}
	if !res.OK {
		return res, fmt.Errorf("The LAN fence could not be applied on this machine (%s): %s.", res.Backend, res.Error)
	}
	// Only now: reattaching starts an earner the removed network had stopped, and it
	// must not run before the drops are in place.
	if earners, err := listEarners(ctx, cli); err == nil {
		reattach(ctx, cli, net.ID, earners)
	}
	return res, nil
}

// Egress lookups: one an hour once the address is known, every five minutes while
// it is not. The table is rewritten every five minutes, so without this cache each
// rewrite would ask a third party.
const (
	egressTTL   = time.Hour
	egressRetry = 5 * time.Minute
)

// fenceNow is the clock the egress cache reads; a seam for tests.
var fenceNow = time.Now

// egressIP is the public IPv4 the table drops, or "" when none is known; the helper
// then keeps the drop the table already has. A failed lookup keeps the last address.
// The caller holds f.mu.
func (f *fence) egressIP(ctx context.Context) string {
	if f.lookupEgress == nil {
		return ""
	}
	now := fenceNow()
	ttl := egressTTL
	if f.egress == "" {
		ttl = egressRetry
	}
	if !f.egressAt.IsZero() && now.Sub(f.egressAt) < ttl {
		return f.egress
	}
	f.egressAt = now
	// The fence only drops IPv4: the earners' network has no IPv6 route out.
	if ip, err := netip.ParseAddr(proxies.PublicIP(f.lookupEgress(ctx))); err == nil && ip.Is4() {
		f.egress = ip.String()
	}
	return f.egress
}

// publicIPURL answers with the caller's public IP as plain text; a seam for tests.
var publicIPURL = "https://api.ipify.org"

// LookupPublicIP asks a public echo service for this machine's public IP, "" when it
// cannot be read. The fence's egress lookup in production.
func LookupPublicIP(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, publicIPURL, nil)
	if err != nil {
		return ""
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	return string(body)
}

// reduce keeps only the masquerade, so earners left on our network keep internet.
func (f *fence) reduce(ctx context.Context, cli fenceAPI, net *network.Inspect) {
	f.mu.Lock()
	defer f.mu.Unlock()
	subnet, err := networkSubnet(net)
	if err != nil {
		f.recordResult("failed", fenceResult{}, err)
		return
	}
	res, err := runHelper(ctx, cli, "apply", subnet, f.egressIP(ctx), false)
	switch {
	case err != nil:
		f.recordResult("failed", res, err)
	case !res.OK:
		f.recordResult("failed", res, nil)
	default:
		f.recordResult("reduced", res, nil)
	}
}

// mode resolves the mode against what Docker holds.
func (f *fence) mode(ctx context.Context, cli fenceAPI) (string, *network.Inspect, []fenceEarner, error) {
	earners, err := listEarners(ctx, cli)
	if err != nil {
		return "", nil, nil, err
	}
	net, err := managedNetwork(ctx, cli)
	if err != nil {
		return "", nil, nil, err
	}
	exists := net != nil
	if !exists {
		other, err := networkByName(ctx, cli)
		if err != nil {
			return "", nil, nil, err
		}
		exists = other != nil
	}
	chosen := f.chosen()
	if chosen != "off" && isPodman(ctx, cli) {
		return FenceModeUnsupported, net, earners, nil
	}
	mode := resolveFenceMode(chosen, net != nil, boundToRemoved(exists, earners), f.wasOn(), earners)
	switch mode {
	case FenceModeOn:
		f.record(true)
	case FenceModeOff:
		f.record(false)
	}
	return mode, net, earners, nil
}

func (f *fence) setOutside(mode string, earners []fenceEarner) {
	var running, stopped []string
	switch mode {
	case FenceModeLegacy:
		running, stopped = outsideFence(earners, "", true)
	case FenceModeOn:
		running, stopped = outsideFence(earners, FenceNetwork, false)
	}
	f.setStatus(func(s *FenceStatus) { s.Mode, s.Outside, s.OutsideStopped = mode, running, stopped })
}

// networkForDeploy is the network a bridge-networked earner joins: "" for Docker's
// default bridge (off and legacy). In on mode the fence is applied first; if it
// cannot be, the deploy is refused here, before the running container is touched.
func (f *fence) networkForDeploy(ctx context.Context, cli fenceAPI) (string, error) {
	mode, _, _, err := f.mode(ctx, cli)
	if err != nil {
		return "", fmt.Errorf("could not check the LAN fence, so the running container was not touched: %w", err)
	}
	f.setStatus(func(s *FenceStatus) { s.Mode = mode })
	if mode == FenceModeUnsupported {
		if f.chosen() == "on" {
			return "", fmt.Errorf("LAN isolation is set to on, but %s. The running container was not touched. Set LAN isolation to auto or off in Settings to deploy on Podman's default network, without the fence", podmanUnsupported)
		}
		return "", nil
	}
	if mode != FenceModeOn {
		return "", nil
	}
	if err := f.apply(ctx, cli); err != nil {
		return "", fmt.Errorf("%v The running container was not touched. %s", err, fenceOffHint)
	}
	return FenceNetwork, nil
}

// reconcile puts the fence back if it went and refreshes the status. Never fails: a
// failure is recorded in the status, which the dashboard shows.
//
// Whole reconciles are serialised: a setting change schedules one (app.go SaveSettings),
// and serialising it behind any in-flight reconcile means the later-started one runs
// last and writes the current setting's status, rather than an earlier "on" apply
// overwriting a just-finished "off" with a stale result.
func (f *fence) reconcile(ctx context.Context, cli fenceAPI) {
	f.reconcileMu.Lock()
	defer f.reconcileMu.Unlock()
	chosen := f.chosen()
	mode, net, earners, err := f.mode(ctx, cli)
	if err != nil {
		f.setStatus(func(s *FenceStatus) { s.State, s.Error = "failed", err.Error() })
		return
	}
	var gatedErr error
	if mode != FenceModeOn {
		// Before the drops go: a fence-gated earner must not outlive the fence.
		gatedErr = f.stopGated(ctx, cli, nil)
	}
	switch {
	case mode == FenceModeOn:
		// A failed apply may mean the table is gone (a ruleset flush, a runtime
		// restart that brought the node back), so every fence-gated earner stops. One
		// that applied leaves the earners on the fenced network alone, and stops one
		// outside it: a host-networked node made before the fence keeps the host's
		// and the LAN's addresses.
		var keep func(fenceEarner) bool
		if f.apply(ctx, cli) == nil {
			keep = fencedAlone
		}
		if gatedErr = f.stopGated(ctx, cli, keep); gatedErr != nil {
			f.setStatus(func(s *FenceStatus) { s.Error = withGatedErr(s.Error, gatedErr) })
		}
		if fresh, err := listEarners(ctx, cli); err == nil {
			earners = fresh
		}
	case mode == FenceModeOff && net != nil && gatedErr != nil:
		// The drops stay until every fence-gated earner is stopped; the next
		// reconcile tries again.
		f.setStatus(func(s *FenceStatus) {
			s.State, s.Error = "failed", gatedErr.Error()+". The LAN fence was kept"
		})
	case mode == FenceModeOff && net != nil:
		f.reduce(ctx, cli, net)
	case mode == FenceModeUnsupported:
		f.setStatus(func(s *FenceStatus) {
			s.State, s.Backend, s.Error = "unsupported", "", withGatedErr(podmanUnsupported+".", gatedErr)
		})
	default:
		f.setStatus(func(s *FenceStatus) { s.State, s.Backend, s.Error = "n/a", "", withGatedErr("", gatedErr) })
	}
	// Settings changed while this ran: its mode is stale, and the reconcile the
	// change triggers publishes the right one.
	if f.chosen() != chosen {
		return
	}
	f.setOutside(mode, earners)
}

// withGatedErr adds a failed stop of a fence-gated earner to a status message, so a
// node still running without the fence shows on the dashboard instead of an empty
// error. The next reconcile tries the stop again.
func withGatedErr(msg string, gatedErr error) string {
	if gatedErr == nil {
		return msg
	}
	if msg == "" {
		return gatedErr.Error()
	}
	return msg + " " + gatedErr.Error()
}

// isGated reports a fence-gated container: labelled so at deploy, or a service the
// catalog marks fence_gated, which covers containers made before the label.
func (f *fence) isGated(labels map[string]string) bool {
	if labels[LabelFenceGated] == "true" {
		return true
	}
	return f.gatedSlug != nil && labels[LabelService] != "" && f.gatedSlug(labels[LabelService])
}

// fencedAlone reports an earner on the fenced network and nothing else.
func fencedAlone(e fenceEarner) bool {
	return e.NetworkMode == FenceNetwork && onlyOn(e, FenceNetwork)
}

// stopGated stops every fence-gated container that is running or that Docker is
// about to restart, except those keep accepts (nil keeps none). An explicit stop, so a restart policy of unless-stopped does not
// bring it back; Start refuses it until isolation is on again. An error means one
// may still be running, and the caller must keep the fence.
func (f *fence) stopGated(ctx context.Context, cli fenceAPI, keep func(fenceEarner) bool) error {
	res, err := cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: make(client.Filters).Add("label", LabelManaged+"=true")})
	if err != nil {
		return fmt.Errorf("could not list the fence-gated earners: %w", err)
	}
	var failed []string
	for _, c := range res.Items {
		if !f.isGated(c.Labels) {
			continue
		}
		// A restarting container is stopped too, or Docker would bring it back
		// after the drops are gone, past the app's Start guard.
		if c.State != container.StateRunning && c.State != container.StateRestarting {
			continue
		}
		if keep != nil && keep(earnerFromSummary(c)) {
			continue
		}
		if _, err := cli.ContainerStop(ctx, c.ID, client.ContainerStopOptions{}); err != nil {
			failed = append(failed, strings.TrimPrefix(firstName(c.Names), "/"))
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("could not stop %s, which runs only behind the LAN fence", strings.Join(failed, ", "))
	}
	return nil
}

func firstName(names []string) string {
	if len(names) == 0 {
		return "a fence-gated earner"
	}
	return names[0]
}

// refreshOutside recounts the earners outside the fence after a deploy or removal,
// so the dashboard does not keep naming a moved earner until the next check.
func (f *fence) refreshOutside(ctx context.Context, cli fenceAPI) {
	mode := f.Status().Mode
	if mode == "" {
		return
	}
	if earners, err := listEarners(ctx, cli); err == nil {
		f.setOutside(mode, earners)
	}
}

// enable turns isolation on for an upgraded install. The caller redeploys the running
// earners it returns in Outside.
func (f *fence) enable(ctx context.Context, cli fenceAPI) (FenceStatus, error) {
	if f.chosen() == "off" {
		return f.Status(), errors.New("LAN isolation is off in Settings. Set it to auto or on first")
	}
	if isPodman(ctx, cli) {
		return f.Status(), errors.New(podmanUnsupported + ".")
	}
	if err := f.apply(ctx, cli); err != nil {
		return f.Status(), err
	}
	f.record(true)
	earners, err := listEarners(ctx, cli)
	if err != nil {
		return f.Status(), err
	}
	f.setOutside(FenceModeOn, earners)
	return f.Status(), nil
}

// isolate points a bridge-networked host config at the fenced network.
func isolate(host *container.HostConfig, networkName string) {
	if networkName == "" || host.NetworkMode != "bridge" {
		return
	}
	host.NetworkMode = container.NetworkMode(networkName)
	host.DNS = append([]netip.Addr(nil), fenceDNS...)
}
