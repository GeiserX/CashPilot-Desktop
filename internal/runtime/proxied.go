package runtime

// Proxied copies of a service: one earner that can only leave through a proxy. This
// ports the CashPilot worker's app/proxy_tunnel.py (GeiserX/CashPilot docs/proxies.md).
//
// A proxied copy is two containers:
//
//   - a tunnel sidecar from CashPilot's own pinned image (drumsergio/cashpilot-tunnel).
//     Its entrypoint drops every packet that is not tunnel traffic before it starts
//     the tunnel, so the proxy is the only way out of its network namespace;
//   - the earner, created with network_mode container:<sidecar>. It has no interface
//     of its own. If the proxy, the tunnel process or the sidecar dies, it has nowhere
//     to send a packet.
//
// Secrets: the proxy URL parts or the Tailscale auth key go into the sidecar as files
// in an anonymous volume, copied in between create and start. They are never in a
// label, the environment or the command Docker records, so docker inspect shows only
// the mount. Nothing here logs a proxy spec, and errors pass through redact before
// they leave this file.

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/proxies"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

const (
	// LabelProxy is the proxy id a proxied copy and its tunnel belong to. A container
	// with this label is never "copy N" of its service: it lives on another exit IP
	// and is managed on its own. The web worker uses the same label.
	LabelProxy = "cashpilot.proxy"
	// LabelRole says what a managed container is when it is not an earner.
	LabelRole = "cashpilot.role"
	// RoleTunnel is the sidecar whose network namespace a proxied copy runs in.
	RoleTunnel = "tunnel"

	// TunnelRepository is the tunnel image, published by every CashPilot release.
	TunnelRepository = "drumsergio/cashpilot-tunnel"
	tunnelImageEnv   = "CASHPILOT_TUNNEL_IMAGE"
	tunnelSecretDir  = "/run/cashpilot-secret"
	tunnelReadyFile  = "/run/ready"
)

// TunnelReadyTimeout is how long the sidecar gets to prove an exit through the
// tunnel before the copy is refused.
var TunnelReadyTimeout = 45 * time.Second

// pinnedTunnelImage is the tunnel image this build runs, by exact version and digest
// (the multi-arch index CashPilot v1.69.0 published). An empty pin makes proxied copies
// ask for CASHPILOT_TUNNEL_IMAGE instead of pulling anything floating.
var pinnedTunnelImage = "drumsergio/cashpilot-tunnel:1.69.0@sha256:322d3e784c4c5f1929328650b5b68f5d015c7354573be158beba1fe96dfd351b"

var floatingTags = map[string]bool{"": true, "latest": true, "stable": true, "edge": true, "main": true, "master": true}

// ProxySpec is the proxy a copy leaves through, decrypted. Never log it.
type ProxySpec struct {
	Kind        string
	Host        string
	Port        int
	Username    string
	Password    string
	AuthKey     string
	ExitNode    string
	LoginServer string
}

func (s ProxySpec) secrets() []string {
	return []string{s.Password, s.AuthKey, s.Username}
}

// ProxiedDeploy is everything one proxied copy is built from.
type ProxiedDeploy struct {
	Slug    string
	ProxyID int64
	// Service is the catalog entry, with Docker.Image already set to the image the
	// deployment really runs (a custom image replaces the catalog's).
	Service catalog.Service
	// Env is the credentials the service is deployed with; the device names derived
	// from {hostname} get "-p<id>".
	Env   map[string]string
	Proxy ProxySpec
	// OwnIP is this machine's public IP, or "" when unknown. A tunnel whose exit is
	// this IP is not ready, because traffic would not be leaving through the proxy.
	OwnIP string
	// Routed is the service itself, routed through the proxy from its first deploy,
	// not a copy beside it: it keeps the deploy's own device names and ids, since no
	// other copy of it runs on this machine to tell apart.
	Routed bool
}

// Exit is where a tunnel's traffic comes out.
type Exit struct {
	IP      string `json:"ip"`
	Country string `json:"country"`
	MS      int64  `json:"ms,omitempty"`
}

// ProxiedCopy is one proxied copy on this machine, as the status read sees it.
type ProxiedCopy struct {
	Slug              string
	ProxyID           int64
	Name              string
	Status            string
	ContainerID       string
	TunnelStatus      string
	TunnelContainerID string
	TunnelStartedAt   string
	NetRxBytes        *uint64
	NetTxBytes        *uint64
	ExitIP            string
	ExitCountry       string
}

// ProxyError is a proxied copy that could not be set up; its message is safe to show.
type ProxyError struct{ msg string }

func (e *ProxyError) Error() string { return e.msg }

func proxyErr(format string, args ...any) error { return &ProxyError{fmt.Sprintf(format, args...)} }

// ProxyProvider is a runtime that can run proxied copies (Docker only).
type ProxyProvider interface {
	DeployProxied(ctx context.Context, req ProxiedDeploy) (Exit, error)
	// StopProxied reports whether the copy was running: Docker answers a stop of a
	// stopped container with success, and the cap must tell the two apart.
	StopProxied(ctx context.Context, slug string, proxyID int64, stopTimeoutSeconds int) (bool, error)
	// StartProxied writes spec, the proxy as stored now, into the tunnel before it
	// starts, so an edit (a new password, key, port or exit node) reaches the copy.
	StartProxied(ctx context.Context, slug string, proxyID int64, stopTimeoutSeconds int, spec ProxySpec, ownIP string) (Exit, error)
	RemoveProxied(ctx context.Context, slug string, proxyID int64, stopTimeoutSeconds int) ([]string, error)
	RemoveAllProxied(ctx context.Context, slug string) ([]string, error)
	ProxiedStatus(ctx context.Context) ([]ProxiedCopy, error)
	TestProxy(ctx context.Context, spec ProxySpec, ownIP string) (Exit, error)
}

// IsProxied reports whether a container is a proxied copy or a tunnel. Neither is
// copy N of its service: a proxied copy carries no instance label, so without this
// it reads as copy 1 and would be taken for the service itself.
func IsProxied(labels map[string]string) bool {
	return labels[LabelProxy] != "" || labels[LabelRole] != ""
}

// TunnelImage is the pinned tunnel image this build uses. CASHPILOT_TUNNEL_IMAGE
// points at a mirror or a test build, but only with an exact tag or a digest: a
// floating tag would let the image change under a running copy.
func TunnelImage() (string, error) {
	if override := strings.TrimSpace(os.Getenv(tunnelImageEnv)); override != "" {
		if err := exactImage(override); err != nil {
			return "", proxyErr("%s is %q; %v", tunnelImageEnv, override, err)
		}
		return override, nil
	}
	if pinnedTunnelImage == "" {
		return "", proxyErr("This build has no tunnel image pinned yet, so it cannot run a copy through a proxy. Set %s to an exact tag or digest of %s.", tunnelImageEnv, TunnelRepository)
	}
	return pinnedTunnelImage, nil
}

// exactImage refuses an image reference that can move: no tag, or a floating one.
func exactImage(ref string) error {
	if ValidateImageRef(ref) != nil {
		return errors.New("give it an image reference")
	}
	if strings.Contains(ref, "@sha256:") {
		return nil
	}
	last := ref[strings.LastIndex(ref, "/")+1:]
	tag := ""
	if i := strings.LastIndex(last, ":"); i >= 0 {
		tag = last[i+1:]
	}
	if floatingTags[strings.ToLower(tag)] {
		return errors.New("give it an exact version tag or a digest, never a floating tag")
	}
	return nil
}

var imageRefRE = regexp.MustCompile(`^` +
	`(?:[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?(?::[0-9]{1,5})?/)?` +
	`[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*` +
	`(?:/[a-z0-9]+(?:(?:[._]|__|-+)[a-z0-9]+)*)*` +
	`(?::[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?` +
	`(?:@sha256:[a-f0-9]{64})?` +
	`$`)

// ValidateImageRef refuses anything that is not a plain image reference.
func ValidateImageRef(ref string) error {
	if len(ref) > 255 || !imageRefRE.MatchString(ref) {
		return errors.New("not an image reference")
	}
	return nil
}

func tunnelName(slug string, proxyID int64) string {
	return fmt.Sprintf("%stunnel-%s-p%d", containerName(""), slug, proxyID)
}

// stagedSuffix: a redeploy starts the new tunnel under its name plus this suffix,
// next to the copy that runs now, and gives it the final name only once the old pair
// is gone. The status read never reports a staged tunnel.
const stagedSuffix = "-next"

// proxiedSwap makes deployProxied's swap of the old pair for the staged tunnel, and
// the status read, take turns: a status read must not start an earner that is being
// removed, or read a half-swapped pair.
var proxiedSwap sync.Mutex

// proxiedDeploys runs one deploy per copy at a time: a second one would take the
// first one's staged tunnel for a leftover and remove it.
var proxiedDeploys sync.Map // "slug/id" -> *sync.Mutex

func deployLock(slug string, proxyID int64) *sync.Mutex {
	mu, _ := proxiedDeploys.LoadOrStore(fmt.Sprintf("%s/%d", slug, proxyID), &sync.Mutex{})
	return mu.(*sync.Mutex)
}

func proxiedName(slug string, proxyID int64) string {
	return fmt.Sprintf("%s-p%d", containerName(slug), proxyID)
}

var urlUserinfo = regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://)[^/\s'"]*@`)

// redact strips URL credentials and every known secret value from a message.
func redact(text string, secrets []string) string {
	out := urlUserinfo.ReplaceAllString(text, "$1")
	for _, s := range secrets {
		if s == "" {
			continue
		}
		out = strings.ReplaceAll(out, s, "***")
		out = strings.ReplaceAll(out, escapeAll(s), "***")
		out = strings.ReplaceAll(out, url.QueryEscape(s), "***")
		out = strings.ReplaceAll(out, url.PathEscape(s), "***")
	}
	return out
}

// lookupIPv4 resolves the proxy host here, so the sidecar does no DNS before its
// tunnel is up. A seam for tests.
var lookupIPv4 = func(ctx context.Context, host string) ([]net.IP, error) {
	return net.DefaultResolver.LookupIP(ctx, "ip4", host)
}

func resolveProxyIP(ctx context.Context, host string) (string, error) {
	if ip := net.ParseIP(host); ip != nil && ip.To4() != nil {
		return ip.To4().String(), nil
	}
	ips, err := lookupIPv4(ctx, host)
	if err != nil {
		return "", proxyErr("Cannot resolve the proxy host %q from this machine: %v", host, err)
	}
	for _, ip := range ips {
		if v4 := ip.To4(); v4 != nil {
			return v4.String(), nil
		}
	}
	return "", proxyErr("The proxy host %q has no IPv4 address", host)
}

var hostnameUnsafe = regexp.MustCompile(`[^a-z0-9-]`)

// proxyAuth is the user:password@ part of the proxy URL, percent-encoded, or "".
func proxyAuth(username, password string) string {
	if username == "" && password == "" {
		return ""
	}
	return escapeAll(username) + ":" + escapeAll(password) + "@"
}

// escapeAll percent-encodes everything but the unreserved characters, as Python's
// quote(safe="") does: an "@" or ":" left in a password would end the userinfo
// early and send the rest of it to the proxy as a host name.
func escapeAll(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// tunnelSecretKeys is every file cp-tunnel reads. A rewrite writes them all, the ones
// the new settings do not use as empty files, so nothing of the old settings (a
// login server, a host) is left for cp-tunnel to pick up.
var tunnelSecretKeys = []string{
	"kind", "own_ip", "auth_key", "exit_node", "hostname", "login_server",
	"proxy_host", "proxy_ip", "proxy_port", "proxy_scheme", "proxy_auth", "settings",
}

// tunnelSecretFiles are the files written into the sidecar's secret volume, one
// value per file. The names and contents match what cp-tunnel reads. "settings" is a
// digest of everything but own_ip and proxy_ip, so a start can tell whether the proxy
// was edited since the tunnel last read its files.
func tunnelSecretFiles(ctx context.Context, spec ProxySpec, ownIP, label string) (map[string]string, error) {
	files, err := tunnelSettingFiles(ctx, spec, ownIP, label)
	if err != nil {
		return nil, err
	}
	digestOf := map[string]string{}
	for k, v := range files {
		if k != "own_ip" && k != "proxy_ip" {
			digestOf[k] = v
		}
	}
	raw, _ := json.Marshal(digestOf) // map keys marshal sorted
	sum := sha256.Sum256(raw)
	files["settings"] = hex.EncodeToString(sum[:])
	return files, nil
}

func tunnelSettingFiles(ctx context.Context, spec ProxySpec, ownIP, label string) (map[string]string, error) {
	files := map[string]string{"kind": spec.Kind, "own_ip": ownIP}
	switch spec.Kind {
	case "tailscale":
		if spec.AuthKey == "" || spec.ExitNode == "" {
			return nil, proxyErr("A Tailscale proxy needs an auth key and an exit node")
		}
		files["auth_key"] = spec.AuthKey
		files["exit_node"] = spec.ExitNode
		host := hostnameUnsafe.ReplaceAllString(strings.ToLower("cashpilot-"+label), "-")
		if len(host) > 63 {
			host = host[:63]
		}
		files["hostname"] = host
		if spec.LoginServer != "" {
			files["login_server"] = spec.LoginServer
		}
		return files, nil
	case "socks5", "http":
	default:
		return nil, proxyErr("Unknown proxy type %q", spec.Kind)
	}
	host := strings.TrimSpace(spec.Host)
	if host == "" || spec.Port < 1 || spec.Port > 65535 {
		return nil, proxyErr("A proxy needs a host and a port between 1 and 65535")
	}
	ip, err := resolveProxyIP(ctx, host)
	if err != nil {
		return nil, err
	}
	files["proxy_host"] = host
	files["proxy_ip"] = ip
	files["proxy_port"] = strconv.Itoa(spec.Port)
	files["proxy_scheme"] = spec.Kind
	files["proxy_auth"] = proxyAuth(spec.Username, spec.Password)
	return files, nil
}

// secretTar is a tar of the files, owned by root and readable by root only. Root in
// the sidecar has no CAP_DAC_OVERRIDE, so the files must be owned by uid 0 to be
// readable at all.
func secretTar(files map[string]string) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		data := []byte(files[name])
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o400, Size: int64(len(data)), Uid: 0, Gid: 0, ModTime: time.Now()}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// tunnelPidsLimit bounds the sidecar: cp-tunnel, tun2proxy or tailscaled, and probes.
const tunnelPidsLimit int64 = 64

// sidecarCreate is everything the sidecar is created with. No secret appears here.
//
// network is the fenced network when LAN isolation is on, "" for Docker's default
// bridge, the same choice every bridge earner gets. A Test tunnel (no proxy id) gets
// no restart policy: one left behind by an app that quit during the test must not
// come back with every daemon restart, holding the proxy login.
func sidecarCreate(name, slug string, proxyID *int64, image, network string) client.ContainerCreateOptions {
	labels := map[string]string{
		LabelManaged: "true",
		LabelRole:    RoleTunnel,
		LabelService: slug,
		LabelVersion: "1",
		LabelRuntime: "existing-docker",
	}
	if proxyID != nil {
		labels[LabelProxy] = strconv.FormatInt(*proxyID, 10)
	}
	init := true
	pids := tunnelPidsLimit
	restart := container.RestartPolicy{Name: "unless-stopped"}
	if proxyID == nil {
		restart = container.RestartPolicy{Name: "no"}
	}
	// A staged tunnel already gets the hostname of the name it is promoted to.
	hostname := strings.TrimSuffix(strings.TrimPrefix(name, containerName("")), stagedSuffix)
	if len(hostname) > 63 {
		hostname = hostname[:63]
	}
	host := &container.HostConfig{
		NetworkMode:    "bridge",
		CapDrop:        []string{"ALL"},
		CapAdd:         []string{"NET_ADMIN"},
		Privileged:     false,
		SecurityOpt:    []string{noNewPrivileges},
		ReadonlyRootfs: true,
		Tmpfs:          map[string]string{"/run": "size=8m,mode=0755"},
		Init:           &init,
		Sysctls: map[string]string{
			"net.ipv6.conf.all.disable_ipv6":     "1",
			"net.ipv6.conf.default.disable_ipv6": "1",
			// DNS sent to Docker's resolver is redirected into the tunnel and keeps a
			// loopback source until it is rewritten; see cp-tunnel.
			"net.ipv4.conf.all.route_localnet": "1",
		},
		RestartPolicy: restart,
		// tun2proxy runs at warn, so this log holds errors, never the sites the
		// earner reaches; the bound keeps even a noisy failure off the disk.
		LogConfig: container.LogConfig{Type: "json-file", Config: map[string]string{"max-size": "1m", "max-file": "2"}},
		Resources: container.Resources{
			PidsLimit: &pids,
			Devices:   []container.DeviceMapping{{PathOnHost: "/dev/net/tun", PathInContainer: "/dev/net/tun", CgroupPermissions: "rwm"}},
		},
	}
	// The fenced network and its resolvers, as every bridge earner gets them: the
	// fence drops the daemon's LAN resolver, so the tunnel needs the public ones.
	isolate(host, network)
	return client.ContainerCreateOptions{
		Name: name,
		Config: &container.Config{
			Image:    image,
			Labels:   labels,
			Hostname: hostname,
			Volumes:  map[string]struct{}{tunnelSecretDir: {}},
		},
		HostConfig: host,
	}
}

// proxiedEnv gives a proxied copy its own device names and ids: the env values the
// catalog derives from {hostname} get "-p<id>", as copy N gets "-N", and every
// per_device id gets its own value (renameDeviceIDs), or the copy is refused.
func proxiedEnv(svc catalog.Service, env map[string]string, proxyID int64) (map[string]string, error) {
	for _, item := range svc.Docker.Env {
		if strings.Contains(item.Default, "{hostname}") && env[item.Key] != "" {
			env[item.Key] = fmt.Sprintf("%s-p%d", env[item.Key], proxyID)
		}
	}
	if err := renameDeviceIDs(svc, env, fmt.Sprintf("p%d", proxyID)); err != nil {
		return nil, err
	}
	return env, nil
}

// earnerCreate is the proxied earner: the service's own container spec, inside the
// sidecar's namespace, with no network settings of its own.
func earnerCreate(req ProxiedDeploy, sidecarID, image, osType string) (client.ContainerCreateOptions, error) {
	svc := req.Service
	if why := proxies.Refusal(svc); why != "" {
		return client.ContainerCreateOptions{}, proxyErr("%s", why)
	}
	env, err := buildEnv(svc, req.Env), error(nil)
	if !req.Routed {
		env, err = proxiedEnv(svc, env, req.ProxyID)
	}
	if err != nil {
		name := svc.Name
		if name == "" {
			name = req.Slug
		}
		return client.ContainerCreateOptions{}, proxyErr("%s: %s", name, err)
	}
	stopTimeout := StopTimeoutSeconds(svc)
	labels := containerLabels(req.Slug, 1)
	labels[LabelProxy] = strconv.FormatInt(req.ProxyID, 10)
	cfg := &container.Config{
		Image:       image,
		Env:         envSlice(env),
		StopTimeout: &stopTimeout,
		Labels:      labels,
		Entrypoint:  entrypointFor(svc),
	}
	if svc.Docker.Command != "" {
		cfg.Cmd = buildCommandArgs(svc.Docker.Command, env)
	}
	host := &container.HostConfig{
		NetworkMode: container.NetworkMode("container:" + sidecarID),
		// Never started by Docker on its own: after a reboot or a daemon restart it
		// would join the tunnel's namespace before the tunnel's firewall is loaded.
		// ProxiedStatus starts it once the tunnel says it is ready.
		RestartPolicy: container.RestartPolicy{Name: "no"},
	}
	applyHardening(host, svc, osType)
	if err := applyResourceLimits(host, svc.Docker.Resources); err != nil {
		return client.ContainerCreateOptions{}, err
	}
	// The backstop for Refusal: whatever the entry says, a proxied earner gets no
	// device, no privilege and nothing that could rewrite or bypass the firewall.
	host.Privileged = false
	host.Resources.Devices = nil
	if risky := proxies.RiskyCaps(host.CapAdd); len(risky) > 0 {
		return client.ContainerCreateOptions{}, proxyErr("A proxied copy cannot hold %s: it could change or bypass its tunnel's firewall", strings.Join(risky, ", "))
	}
	return client.ContainerCreateOptions{Name: proxiedName(req.Slug, req.ProxyID), Config: cfg, HostConfig: host}, nil
}

// tunnelDocker is the slice of Docker a proxied copy needs, so its order of
// operations and its refusals can be tested without a daemon.
type tunnelDocker interface {
	facts(ctx context.Context) daemonInfo
	pull(ctx context.Context, image string) error
	imageRuns(ctx context.Context, image string) (entrypoint, cmd []string, err error)
	create(ctx context.Context, opts client.ContainerCreateOptions) (string, error)
	copyTo(ctx context.Context, id, dir string, archive []byte) error
	start(ctx context.Context, id string) error
	stop(ctx context.Context, id string, timeout int) error
	restart(ctx context.Context, id string, timeout int) error
	remove(ctx context.Context, id string) error
	rename(ctx context.Context, id, name string) error
	inspect(ctx context.Context, nameOrID string) (container.InspectResponse, bool, error)
	list(ctx context.Context, labels ...string) ([]container.Summary, error)
	readFile(ctx context.Context, id, path string) (string, error)
	logsTail(ctx context.Context, id string, lines int) string
	netCounters(ctx context.Context, id string) (*uint64, *uint64)
	// sidecarNetwork is the network a tunnel joins: the fenced one when LAN isolation
	// is on, "" for the default bridge. An error means the fence could not be applied.
	sidecarNetwork(ctx context.Context) (string, error)
}

type liveTunnelDocker struct {
	cli   *client.Client
	fence *fence
}

func (d liveTunnelDocker) sidecarNetwork(ctx context.Context) (string, error) {
	if d.fence == nil {
		return "", nil
	}
	return d.fence.networkForDeploy(ctx, d.cli, false)
}

func (d liveTunnelDocker) facts(ctx context.Context) daemonInfo { return daemonFacts(ctx, d.cli) }
func (d liveTunnelDocker) pull(ctx context.Context, image string) error {
	return pullImage(ctx, d.cli, image, nil)
}
func (d liveTunnelDocker) imageRuns(ctx context.Context, image string) ([]string, []string, error) {
	inspected, err := d.cli.ImageInspect(ctx, image)
	if err != nil {
		return nil, nil, err
	}
	if inspected.Config == nil {
		return nil, nil, errors.New("the daemon returned no image config")
	}
	return inspected.Config.Entrypoint, inspected.Config.Cmd, nil
}
func (d liveTunnelDocker) create(ctx context.Context, opts client.ContainerCreateOptions) (string, error) {
	created, err := d.cli.ContainerCreate(ctx, opts)
	return created.ID, err
}
func (d liveTunnelDocker) copyTo(ctx context.Context, id, dir string, archive []byte) error {
	_, err := d.cli.CopyToContainer(ctx, id, client.CopyToContainerOptions{DestinationPath: dir, Content: bytes.NewReader(archive)})
	return err
}
func (d liveTunnelDocker) start(ctx context.Context, id string) error {
	_, err := d.cli.ContainerStart(ctx, id, client.ContainerStartOptions{})
	return err
}
func (d liveTunnelDocker) stop(ctx context.Context, id string, timeout int) error {
	_, err := d.cli.ContainerStop(ctx, id, client.ContainerStopOptions{Timeout: &timeout})
	return err
}
func (d liveTunnelDocker) restart(ctx context.Context, id string, timeout int) error {
	_, err := d.cli.ContainerRestart(ctx, id, client.ContainerRestartOptions{Timeout: &timeout})
	return err
}
func (d liveTunnelDocker) remove(ctx context.Context, id string) error {
	_, err := d.cli.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
	return err
}
func (d liveTunnelDocker) rename(ctx context.Context, id, name string) error {
	_, err := d.cli.ContainerRename(ctx, id, client.ContainerRenameOptions{NewName: name})
	return err
}
func (d liveTunnelDocker) inspect(ctx context.Context, nameOrID string) (container.InspectResponse, bool, error) {
	res, err := d.cli.ContainerInspect(ctx, nameOrID, client.ContainerInspectOptions{})
	if err != nil {
		if cerrdefs.IsNotFound(err) {
			return container.InspectResponse{}, false, nil
		}
		return container.InspectResponse{}, false, err
	}
	return res.Container, true, nil
}
func (d liveTunnelDocker) list(ctx context.Context, labels ...string) ([]container.Summary, error) {
	res, err := d.cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: make(client.Filters).Add("label", labels...)})
	return res.Items, err
}
func (d liveTunnelDocker) readFile(ctx context.Context, id, path string) (string, error) {
	out, code, err := execOne(ctx, d.cli, id, []string{"cat", path}, 5*time.Second)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fmt.Errorf("exit %d", code)
	}
	return out, nil
}
func (d liveTunnelDocker) logsTail(ctx context.Context, id string, lines int) string {
	reader, err := d.cli.ContainerLogs(ctx, id, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Tail: strconv.Itoa(lines)})
	if err != nil {
		return ""
	}
	defer reader.Close()
	raw, _ := io.ReadAll(io.LimitReader(reader, 64<<10))
	return stripDockerLogHeaders(raw)
}
func (d liveTunnelDocker) netCounters(ctx context.Context, id string) (*uint64, *uint64) {
	sample, ok := sampleStats(ctx, d.cli, id)
	if !ok {
		return nil, nil
	}
	return netCounters(sample)
}

// withTunnelDocker runs fn against the live daemon, with the provider's LAN fence.
func withTunnelDocker[T any](f *fence, fn func(tunnelDocker) (T, error)) (T, error) {
	var zero T
	cli, err := dockerClient()
	if err != nil {
		return zero, err
	}
	defer cli.Close()
	return fn(liveTunnelDocker{cli: cli, fence: f})
}

// randReader names throwaway test tunnels; a seam for nothing but clarity.
var randReader io.Reader = rand.Reader

// readyPoll is how often the sidecar's ready file is read while it comes up.
var readyPoll = time.Second

// parseReady reads cp-probe's JSON line, {"ip":"203.0.113.7","country":"ES"}.
func parseReady(out string) (Exit, bool) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var e Exit
	// cp-probe keeps only IP characters of what an echo site answered, so an error
	// page can still come through as "e"; only a real address counts.
	if err := json.Unmarshal([]byte(strings.TrimSpace(lines[len(lines)-1])), &e); err != nil || net.ParseIP(e.IP) == nil {
		return Exit{}, false
	}
	return e, true
}

func waitReady(ctx context.Context, d tunnelDocker, id string, timeout time.Duration) (Exit, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ins, found, err := d.inspect(ctx, id)
		if err != nil || !found || ins.State == nil {
			break
		}
		status := string(ins.State.Status)
		if status != "running" && status != "created" && status != "restarting" {
			break
		}
		if status == "running" {
			if out, err := d.readFile(ctx, id, tunnelReadyFile); err == nil {
				if e, ok := parseReady(out); ok {
					return e, nil
				}
			}
		}
		select {
		case <-ctx.Done():
			return Exit{}, ctx.Err()
		case <-time.After(readyPoll):
		}
	}
	tail := strings.Join(strings.Fields(d.logsTail(context.Background(), id, 5)), " ")
	if len(tail) > 300 {
		tail = tail[len(tail)-300:]
	}
	msg := fmt.Sprintf("The tunnel did not reach the internet through the proxy within %d s, so nothing was started behind it.", int(timeout.Seconds()))
	if tail != "" {
		msg += " Tunnel log: " + tail
	}
	return Exit{}, &ProxyError{msg}
}

func removeQuietly(d tunnelDocker, id string, timeout int) {
	ctx := context.Background()
	_ = d.stop(ctx, id, timeout)
	_ = d.remove(ctx, id)
}

// startSidecar creates, provisions and starts a sidecar, then waits for its exit. It
// is removed on any failure, and the error never carries a secret.
// network is what sidecarNetwork answered; the caller asks before it removes anything,
// so a fence that cannot be applied never costs the running copy.
func startSidecar(ctx context.Context, d tunnelDocker, name, slug, network string, proxyID *int64, spec ProxySpec, ownIP string) (string, Exit, error) {
	label := slug
	if proxyID != nil {
		label = fmt.Sprintf("%s-p%d", slug, *proxyID)
	}
	files, err := tunnelSecretFiles(ctx, spec, ownIP, label)
	if err != nil {
		return "", Exit{}, &ProxyError{redact(err.Error(), spec.secrets())}
	}
	image, err := TunnelImage()
	if err != nil {
		return "", Exit{}, err
	}
	archive, err := secretTar(files)
	if err != nil {
		return "", Exit{}, err
	}
	// A failed pull falls back to a local copy of the exact image, which is what a
	// test build or an offline mirror is.
	_ = d.pull(ctx, image)
	id, err := d.create(ctx, sidecarCreate(name, slug, proxyID, image, network))
	if err != nil {
		return "", Exit{}, &ProxyError{redact("The tunnel could not be created: "+err.Error(), spec.secrets())}
	}
	fail := func(err error) (string, Exit, error) {
		removeQuietly(d, id, 2)
		var pe *ProxyError
		if errors.As(err, &pe) {
			return "", Exit{}, &ProxyError{redact(pe.msg, spec.secrets())}
		}
		return "", Exit{}, &ProxyError{redact("The tunnel could not start: "+err.Error(), spec.secrets())}
	}
	if err := d.copyTo(ctx, id, tunnelSecretDir, archive); err != nil {
		return fail(fmt.Errorf("could not hand the proxy settings to the tunnel container: %w", err))
	}
	if err := d.start(ctx, id); err != nil {
		return fail(err)
	}
	exit, err := waitReady(ctx, d, id, TunnelReadyTimeout)
	if err != nil {
		return fail(err)
	}
	return id, exit, nil
}

func isOurs(labels map[string]string, slug string, proxyID int64) bool {
	return labels[LabelManaged] == "true" && labels[LabelService] == slug && labels[LabelProxy] == strconv.FormatInt(proxyID, 10)
}

type proxiedPair struct {
	earner, tunnel       *container.InspectResponse
	earnerOK, tunnelOK   bool
	earnerName, tunnelNm string
}

func pairOf(ctx context.Context, d tunnelDocker, slug string, proxyID int64) (proxiedPair, error) {
	p := proxiedPair{earnerName: proxiedName(slug, proxyID), tunnelNm: tunnelName(slug, proxyID)}
	if e, found, err := d.inspect(ctx, p.earnerName); err != nil {
		return p, err
	} else if found {
		p.earner = &e
		p.earnerOK = e.Config != nil && isOurs(e.Config.Labels, slug, proxyID)
	}
	if t, found, err := d.inspect(ctx, p.tunnelNm); err != nil {
		return p, err
	} else if found {
		p.tunnel = &t
		p.tunnelOK = t.Config != nil && isOurs(t.Config.Labels, slug, proxyID)
	}
	return p, nil
}

func deployProxied(ctx context.Context, d tunnelDocker, req ProxiedDeploy) (Exit, error) {
	mu := deployLock(req.Slug, req.ProxyID)
	mu.Lock()
	defer mu.Unlock()
	facts := d.facts(ctx)
	if facts.OSType != "" && facts.OSType != "linux" {
		return Exit{}, proxyErr("Proxied copies need a Linux container runtime; this one runs %s containers.", facts.OSType)
	}
	image := ImageForArch(req.Service.Docker, facts.Architecture)
	if image == "" {
		return Exit{}, proxyErr("%s has no Docker image", req.Service.Name)
	}
	// Built and checked before anything is touched: an entry-level refusal must not
	// cost the copy that is running now.
	if _, err := earnerCreate(req, "check", image, facts.OSType); err != nil {
		return Exit{}, err
	}
	if err := d.pull(ctx, image); err != nil {
		return Exit{}, err
	}
	entrypoint, cmd, err := d.imageRuns(ctx, image)
	if err != nil {
		return Exit{}, fmt.Errorf("could not read what %s runs: %w", image, err)
	}
	probe, _ := earnerCreate(req, "check", image, facts.OSType)
	if bootsInitSystem(entrypoint, cmd, probe.Config.Entrypoint, probe.Config.Cmd) {
		return Exit{}, proxyErr("%s runs an init system as its main process, which CashPilot never runs", image)
	}
	// Settings that cannot work (a host that does not resolve, no tunnel image) must
	// not cost the copy running now either.
	if _, err := tunnelSecretFiles(ctx, req.Proxy, req.OwnIP, fmt.Sprintf("%s-p%d", req.Slug, req.ProxyID)); err != nil {
		return Exit{}, &ProxyError{redact(err.Error(), req.Proxy.secrets())}
	}
	if _, err := TunnelImage(); err != nil {
		return Exit{}, err
	}
	network, err := d.sidecarNetwork(ctx)
	if err != nil {
		return Exit{}, err
	}
	pid := req.ProxyID
	final := tunnelName(req.Slug, pid)
	staged := final + stagedSuffix
	// Left by a deploy that died midway; replaced only if it is ours.
	if ins, found, err := d.inspect(ctx, staged); err != nil {
		return Exit{}, err
	} else if found {
		if ins.Config == nil || !isOurs(ins.Config.Labels, req.Slug, pid) {
			return Exit{}, proxyErr("A container named %s that CashPilot did not create is in the way", staged)
		}
		// One younger than the sweep age belongs to a deploy still running, in this
		// app or another one on the same Docker host; removing it would cut that
		// deploy's tunnel after it may already have removed the old pair.
		if created, perr := time.Parse(time.RFC3339Nano, ins.Created); perr == nil && time.Since(created) < staleTestTunnel {
			return Exit{}, proxyErr("Another deploy of %s through proxy %d is under way; try again in a few minutes", req.Slug, pid)
		}
		removeQuietly(d, ins.ID, 2)
	}
	// The new tunnel proves an exit while the old pair keeps running, so a tunnel that
	// cannot start costs nothing. Only then does the old pair go.
	sidecarID, exit, err := startSidecar(ctx, d, staged, req.Slug, network, &pid, req.Proxy, req.OwnIP)
	if err != nil {
		return Exit{}, err
	}
	proxiedSwap.Lock()
	_, err = removeProxied(ctx, d, req.Slug, req.ProxyID, StopTimeoutSeconds(req.Service))
	if err == nil {
		err = d.rename(ctx, sidecarID, final)
	}
	proxiedSwap.Unlock()
	if err != nil {
		removeQuietly(d, sidecarID, 2)
		return Exit{}, err
	}
	opts, err := earnerCreate(req, sidecarID, image, facts.OSType)
	if err != nil {
		removeQuietly(d, sidecarID, 2)
		return Exit{}, err
	}
	earnerID, err := d.create(ctx, opts)
	if err != nil {
		removeQuietly(d, sidecarID, 2)
		return Exit{}, &ProxyError{redact("Docker refused the proxied copy: "+err.Error(), req.Proxy.secrets())}
	}
	if err := d.start(ctx, earnerID); err != nil {
		removeQuietly(d, earnerID, 2)
		removeQuietly(d, sidecarID, 2)
		return Exit{}, &ProxyError{redact("The proxied copy could not start: "+err.Error(), req.Proxy.secrets())}
	}
	return exit, nil
}

func live(ins *container.InspectResponse) bool {
	return ins != nil && ins.State != nil && (ins.State.Status == "running" || ins.State.Status == "restarting")
}

// stopProxied stops the tunnel, then its earner, and reports whether either was
// running.
//
// The tunnel goes first so a stop that fails halfway never leaves a ready tunnel
// beside a stopped earner: the status read starts an earner in that state, which
// would undo the owner's stop. An earner left running after its tunnel stopped sits
// in a namespace with no way out, so it does not leak, and earnerAction leaves it
// alone because the tunnel is not running.
func stopProxied(ctx context.Context, d tunnelDocker, slug string, proxyID int64, timeout int) (bool, error) {
	p, err := pairOf(ctx, d, slug, proxyID)
	if err != nil {
		return false, err
	}
	if !p.earnerOK && !p.tunnelOK {
		return false, proxyErr("No proxied copy of %s through proxy %d on this machine", slug, proxyID)
	}
	wasRunning := (p.earnerOK && live(p.earner)) || (p.tunnelOK && live(p.tunnel))
	if p.tunnelOK {
		// A tunnel removed since pairOf read it is as good as stopped: with no
		// tunnel the status read never starts the earner, so stop it too.
		if err := d.stop(ctx, p.tunnelNm, 10); err != nil && !cerrdefs.IsNotFound(err) {
			return wasRunning, err
		}
	}
	if p.earnerOK {
		return wasRunning, d.stop(ctx, p.earnerName, stopTimeoutOr(p.earner, timeout))
	}
	return wasRunning, nil
}

func stopTimeoutOr(ins *container.InspectResponse, fromCatalog int) int {
	if fromCatalog > 0 {
		return fromCatalog
	}
	if ins != nil && ins.Config != nil && ins.Config.StopTimeout != nil {
		return *ins.Config.StopTimeout
	}
	return 10
}

// startProxied starts the tunnel, waits for its exit, then the earner.
//
// spec is the proxy as stored now. Its settings are written over the ones the tunnel
// was deployed with (the host is resolved again too, so a provider that moved its
// gateway needs only a Stop and Start). A tunnel still running on other settings is
// stopped first, with its earner, and comes back on the new ones. A host that no
// longer resolves stops the start: the old address may belong to someone else now,
// and cp-tunnel would hand it the proxy login.
func startProxied(ctx context.Context, d tunnelDocker, slug string, proxyID int64, timeout int, spec ProxySpec, ownIP string) (Exit, error) {
	p, err := pairOf(ctx, d, slug, proxyID)
	if err != nil {
		return Exit{}, err
	}
	if !p.earnerOK || !p.tunnelOK {
		return Exit{}, proxyErr("No proxied copy of %s through proxy %d on this machine", slug, proxyID)
	}
	files, err := tunnelSecretFiles(ctx, spec, ownIP, fmt.Sprintf("%s-p%d", slug, proxyID))
	if err != nil {
		return Exit{}, &ProxyError{redact(err.Error(), spec.secrets())}
	}
	running := p.tunnel.State != nil && p.tunnel.State.Status == "running"
	if running {
		if current, err := d.readFile(ctx, p.tunnelNm, tunnelSecretDir+"/settings"); err != nil || strings.TrimSpace(current) != files["settings"] {
			if err := d.stop(ctx, p.earnerName, stopTimeoutOr(p.earner, timeout)); err != nil {
				return Exit{}, err
			}
			if err := d.stop(ctx, p.tunnelNm, 10); err != nil {
				return Exit{}, err
			}
			running = false
		}
	}
	if !running {
		full := map[string]string{}
		for _, k := range tunnelSecretKeys {
			full[k] = files[k]
		}
		archive, err := secretTar(full)
		if err != nil {
			return Exit{}, err
		}
		if err := d.copyTo(ctx, p.tunnelNm, tunnelSecretDir, archive); err != nil {
			return Exit{}, proxyErr("Could not hand the proxy settings to the tunnel container: %v", err)
		}
	}
	if err := d.start(ctx, p.tunnelNm); err != nil {
		return Exit{}, err
	}
	exit, err := waitReady(ctx, d, p.tunnelNm, TunnelReadyTimeout)
	if err != nil {
		return Exit{}, err
	}
	earner, found, err := d.inspect(ctx, p.earnerName)
	if err != nil || !found {
		return Exit{}, proxyErr("The proxied copy of %s went away while its tunnel started", slug)
	}
	if earner.State != nil && earner.State.Status == "running" {
		return exit, d.restart(ctx, p.earnerName, stopTimeoutOr(&earner, timeout))
	}
	return exit, d.start(ctx, p.earnerName)
}

func removeProxied(ctx context.Context, d tunnelDocker, slug string, proxyID int64, timeout int) ([]string, error) {
	p, err := pairOf(ctx, d, slug, proxyID)
	if err != nil {
		return nil, err
	}
	var removed []string
	// The earner first, so no tunnel is removed under a running earner.
	if p.earnerOK {
		_ = d.stop(ctx, p.earnerName, stopTimeoutOr(p.earner, timeout))
		if err := d.remove(ctx, p.earnerName); err != nil {
			return removed, err
		}
		removed = append(removed, p.earnerName)
	}
	if p.tunnelOK {
		_ = d.stop(ctx, p.tunnelNm, 10)
		if err := d.remove(ctx, p.tunnelNm); err != nil {
			return removed, err
		}
		removed = append(removed, p.tunnelNm)
	}
	return removed, nil
}

func removeAllProxied(ctx context.Context, d tunnelDocker, slug string) ([]string, error) {
	found, err := d.list(ctx, LabelManaged+"=true", LabelService+"="+slug, LabelProxy)
	if err != nil {
		return nil, err
	}
	var mine []container.Summary
	for _, c := range found {
		if c.Labels[LabelService] == slug && c.Labels[LabelManaged] == "true" && c.Labels[LabelProxy] != "" {
			mine = append(mine, c)
		}
	}
	// Earners first, so no tunnel is removed under a running earner.
	sort.SliceStable(mine, func(i, j int) bool {
		return mine[i].Labels[LabelRole] != RoleTunnel && mine[j].Labels[LabelRole] == RoleTunnel
	})
	var removed []string
	for _, c := range mine {
		_ = d.stop(ctx, c.ID, 10)
		if err := d.remove(ctx, c.ID); err != nil {
			return removed, err
		}
		removed = append(removed, summaryName(c))
	}
	return removed, nil
}

func summaryName(c container.Summary) string {
	if len(c.Names) > 0 {
		return strings.TrimPrefix(c.Names[0], "/")
	}
	return c.ID
}

// earnerAction is what the status read does to an earner: "restart", "start" or "".
//
// Nothing happens until the tunnel is running and has written its ready file, which
// it does only after its firewall is loaded and an exit through the tunnel was seen.
// So an earner never enters a namespace that could still leak, after a reboot or a
// daemon restart included.
//
// A running earner whose tunnel started after it sits in the old namespace, which has
// no way out: it does not leak, it just stops earning until it is restarted into the
// new one. An earner that is not running while its tunnel is up was stopped by a
// reboot or its own crash, never by the owner or the cap, which stop the tunnel too;
// it is started.
func earnerAction(earnerStatus, earnerStartedAt, tunnelStatus, tunnelStartedAt string, ready bool) string {
	if !ready || tunnelStatus != "running" {
		return ""
	}
	switch earnerStatus {
	case "running":
		if tunnelStartedAt > earnerStartedAt {
			return "restart"
		}
	case "exited", "created":
		return "start"
	}
	return ""
}

func startedAt(ins container.InspectResponse) string {
	if ins.State == nil {
		return ""
	}
	return ins.State.StartedAt
}

// testTunnelSlug is the service label of a throwaway Test tunnel.
const testTunnelSlug = "proxy-test"

// staleTestTunnel is how old a Test tunnel must be before the status read removes
// it. A test lasts at most TunnelReadyTimeout, so an older one was left behind by an
// app that quit during the test.
const staleTestTunnel = 5 * time.Minute

// sweepTestTunnels removes Test tunnels older than staleTestTunnel, and staged
// tunnels as old: a deploy is past its ready timeout by then, so the deploy that
// staged it died, and it holds the proxy login while no status read reports it.
func sweepTestTunnels(ctx context.Context, d tunnelDocker, now time.Time) {
	if staged, err := d.list(ctx, LabelManaged+"=true", LabelRole+"="+RoleTunnel, LabelProxy); err == nil {
		for _, c := range staged {
			if c.Labels[LabelProxy] != "" && strings.HasSuffix(summaryName(c), stagedSuffix) && now.Sub(time.Unix(c.Created, 0)) >= staleTestTunnel {
				id, perr := strconv.ParseInt(c.Labels[LabelProxy], 10, 64)
				if perr != nil {
					continue
				}
				// A deploy of this copy in this app may still be waiting to swap it in.
				mu := deployLock(c.Labels[LabelService], id)
				if !mu.TryLock() {
					continue
				}
				removeQuietly(d, c.ID, 2)
				mu.Unlock()
			}
		}
	}
	found, err := d.list(ctx, LabelManaged+"=true", LabelRole+"="+RoleTunnel, LabelService+"="+testTunnelSlug)
	if err != nil {
		return
	}
	for _, c := range found {
		if c.Labels[LabelService] != testTunnelSlug || c.Labels[LabelRole] != RoleTunnel || c.Labels[LabelProxy] != "" {
			continue
		}
		if now.Sub(time.Unix(c.Created, 0)) < staleTestTunnel {
			continue
		}
		removeQuietly(d, c.ID, 2)
	}
}

func proxiedStatus(ctx context.Context, d tunnelDocker) ([]ProxiedCopy, error) {
	sweepTestTunnels(ctx, d, time.Now())
	proxiedSwap.Lock()
	defer proxiedSwap.Unlock()
	found, err := d.list(ctx, LabelManaged+"=true", LabelProxy)
	if err != nil {
		return nil, err
	}
	type key struct {
		slug string
		id   int64
	}
	pairs := map[key]map[string]container.Summary{}
	for _, c := range found {
		if c.Labels[LabelManaged] != "true" || strings.HasSuffix(summaryName(c), stagedSuffix) {
			continue // a staged tunnel is not part of a copy until it is promoted
		}
		id, err := strconv.ParseInt(c.Labels[LabelProxy], 10, 64)
		if err != nil {
			continue
		}
		k := key{c.Labels[LabelService], id}
		role := "earner"
		if c.Labels[LabelRole] == RoleTunnel {
			role = "tunnel"
		}
		if pairs[k] == nil {
			pairs[k] = map[string]container.Summary{}
		}
		pairs[k][role] = c
	}
	keys := make([]key, 0, len(pairs))
	for k := range pairs {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].slug != keys[j].slug {
			return keys[i].slug < keys[j].slug
		}
		return keys[i].id < keys[j].id
	})
	out := make([]ProxiedCopy, 0, len(keys))
	for _, k := range keys {
		pair := pairs[k]
		cp := ProxiedCopy{Slug: k.slug, ProxyID: k.id, Name: proxiedName(k.slug, k.id), Status: "missing", TunnelStatus: "missing"}
		earner, hasEarner := pair["earner"]
		tunnel, hasTunnel := pair["tunnel"]
		var tunnelIns, earnerIns container.InspectResponse
		if hasTunnel {
			cp.TunnelStatus = string(tunnel.State)
			cp.TunnelContainerID = tunnel.ID
			if ins, ok, err := d.inspect(ctx, tunnel.ID); err == nil && ok {
				tunnelIns = ins
				cp.TunnelStartedAt = startedAt(ins)
			}
			if cp.TunnelStatus == "running" {
				if out, err := d.readFile(ctx, tunnel.ID, tunnelReadyFile); err == nil {
					if e, ok := parseReady(out); ok {
						cp.ExitIP, cp.ExitCountry = e.IP, e.Country
					}
				}
				// Traffic is the sidecar's: the earner has no interface of its own, so
				// every byte it moves crosses the sidecar's eth0.
				cp.NetRxBytes, cp.NetTxBytes = d.netCounters(ctx, tunnel.ID)
			}
		}
		if hasEarner {
			cp.Name = summaryName(earner)
			cp.Status = string(earner.State)
			cp.ContainerID = earner.ID
			if ins, ok, err := d.inspect(ctx, earner.ID); err == nil && ok {
				earnerIns = ins
			}
			action := earnerAction(cp.Status, startedAt(earnerIns), cp.TunnelStatus, startedAt(tunnelIns), cp.ExitIP != "")
			var actErr error
			switch action {
			case "restart":
				actErr = d.restart(ctx, earner.ID, stopTimeoutOr(&earnerIns, 0))
			case "start":
				actErr = d.start(ctx, earner.ID)
			}
			if action != "" && actErr == nil {
				cp.Status = "running"
			}
		}
		out = append(out, cp)
	}
	return out, nil
}

func testProxy(ctx context.Context, d tunnelDocker, spec ProxySpec, ownIP string) (Exit, error) {
	began := time.Now()
	var suffix [5]byte
	_, _ = io.ReadFull(randReader, suffix[:])
	name := fmt.Sprintf("%stunnel-test-%x", containerName(""), suffix)
	network, err := d.sidecarNetwork(ctx)
	if err != nil {
		return Exit{}, err
	}
	id, exit, err := startSidecar(ctx, d, name, testTunnelSlug, network, nil, spec, ownIP)
	if err != nil {
		return Exit{}, err
	}
	removeQuietly(d, id, 2)
	exit.MS = time.Since(began).Milliseconds()
	if proxies.PublicIP(exit.IP) == "" {
		return Exit{}, proxyErr("The tunnel came up, but its exit address is not a public IP")
	}
	return exit, nil
}

// DeployProxied deploys (or replaces) the proxied copy of req.Slug through req.ProxyID.
// The old pair goes first, the sidecar starts and proves an exit, and only then is the
// earner created inside its namespace.
func (p *DockerProvider) DeployProxied(ctx context.Context, req ProxiedDeploy) (Exit, error) {
	return withTunnelDocker(&p.fence, func(d tunnelDocker) (Exit, error) { return deployProxied(ctx, d, req) })
}

// StopProxied stops the earner, then its tunnel, and reports whether either was running.
func (p *DockerProvider) StopProxied(ctx context.Context, slug string, proxyID int64, stopTimeoutSeconds int) (bool, error) {
	return withTunnelDocker(&p.fence, func(d tunnelDocker) (bool, error) {
		return stopProxied(ctx, d, slug, proxyID, stopTimeoutSeconds)
	})
}

// StartProxied writes spec into the tunnel, starts it, waits for its exit, then
// starts the earner (see startProxied).
func (p *DockerProvider) StartProxied(ctx context.Context, slug string, proxyID int64, stopTimeoutSeconds int, spec ProxySpec, ownIP string) (Exit, error) {
	return withTunnelDocker(&p.fence, func(d tunnelDocker) (Exit, error) {
		return startProxied(ctx, d, slug, proxyID, stopTimeoutSeconds, spec, ownIP)
	})
}

// RemoveProxied removes the earner, then its tunnel and the tunnel's secret volume.
func (p *DockerProvider) RemoveProxied(ctx context.Context, slug string, proxyID int64, stopTimeoutSeconds int) ([]string, error) {
	return withTunnelDocker(&p.fence, func(d tunnelDocker) ([]string, error) {
		return removeProxied(ctx, d, slug, proxyID, stopTimeoutSeconds)
	})
}

// RemoveAllProxied removes every proxied copy of slug and its tunnel, when the
// service itself is removed.
func (p *DockerProvider) RemoveAllProxied(ctx context.Context, slug string) ([]string, error) {
	return withTunnelDocker(&p.fence, func(d tunnelDocker) ([]string, error) { return removeAllProxied(ctx, d, slug) })
}

// ProxiedStatus is one entry per proxied copy on this machine, and it starts an
// earner whose tunnel is ready but which is not running in it (see earnerAction).
func (p *DockerProvider) ProxiedStatus(ctx context.Context) ([]ProxiedCopy, error) {
	return withTunnelDocker(&p.fence, func(d tunnelDocker) ([]ProxiedCopy, error) { return proxiedStatus(ctx, d) })
}

// TestProxy starts a throwaway tunnel, reads the exit IP it gets, and removes it on
// every path. It passes only when the exit IP is public and is not this machine's.
func (p *DockerProvider) TestProxy(ctx context.Context, spec ProxySpec, ownIP string) (Exit, error) {
	return withTunnelDocker(&p.fence, func(d tunnelDocker) (Exit, error) { return testProxy(ctx, d, spec, ownIP) })
}
