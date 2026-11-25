package workerapi

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
)

// SpecInput is what a deploy to one worker is built from, besides the catalog entry.
type SpecInput struct {
	// Typed are the deploy form's values. A blank box is not a value.
	Typed map[string]string
	// Stored are the credentials this Desktop keeps for the service. They fill
	// the boxes left blank, as the web app's recorded spec does, since the worker
	// keeps no credentials of its own.
	Stored map[string]string
	// LocalHostname is what the form shows {hostname} as: this computer's name. A
	// device name still showing it is the form's prefill, not a choice, and becomes
	// the worker's own name (DeviceName) instead.
	LocalHostname string
	// DeviceName is what {hostname} becomes on this worker.
	DeviceName string
	// WorkerArch is the machine the worker reported (platform.machine()).
	WorkerArch string
	// WorkerFeatures are what the worker said it can do in its last heartbeat.
	WorkerFeatures []string
	// CustomImage replaces the catalog image; "" runs the catalog's.
	CustomImage string
}

// CheckRemoteWalletDeploy refuses seed-file services. The worker API carries no
// wallet choice, so the user must deploy from the machine that holds the wallet.
func CheckRemoteWalletDeploy(svc catalog.Service) error {
	if _, ok := svc.SeedVar(); ok {
		return fmt.Errorf("%s keeps its wallet seed phrase in a file on the machine that runs it", svc.Name)
	}
	return nil
}

// BuildDeploySpec builds the spec the web app's _deploy sends a worker for a new
// deployment of svc: the catalog's env defaults under the values given, {hostname}
// as the worker's own device name, the catalog's ports, mounts, capabilities,
// devices, resources and command, and the image for the worker's architecture.
func BuildDeploySpec(svc catalog.Service, in SpecInput) (DeploySpec, error) {
	if svc.Custom {
		return DeploySpec{}, fmt.Errorf("%s is a custom service, which runs only on this computer for now", svc.Name)
	}
	if err := CheckRemoteWalletDeploy(svc); err != nil {
		return DeploySpec{}, fmt.Errorf("%w, so it runs only on this computer for now. Choose This computer in Where. Nothing was deployed", err)
	}
	if svc.ManualOnly || strings.TrimSpace(svc.Docker.Image) == "" {
		return DeploySpec{}, fmt.Errorf("%s has no Docker image, so a worker cannot run it", svc.Name)
	}
	if catalog.IsRetired(svc.Status) {
		return DeploySpec{}, fmt.Errorf("%s is no longer available for deployment (%s)", svc.Name, svc.Status)
	}
	dc := svc.Docker
	env := deployEnv(dc.Env, in)
	if missing := missingRequired(dc.Env, env); len(missing) > 0 {
		return DeploySpec{}, fmt.Errorf("missing required fields: %s", strings.Join(missing, ", "))
	}
	if invalid := invalidValues(dc.Env, env); len(invalid) > 0 {
		// The value is never echoed: it may be a credential.
		return DeploySpec{}, fmt.Errorf("invalid value for: %s", strings.Join(invalid, ", "))
	}
	custom := strings.TrimSpace(in.CustomImage)
	if custom != "" && dc.FenceGated {
		return DeploySpec{}, fmt.Errorf("%s runs only the image the catalog pins, since it holds NET_ADMIN in its own namespace; clear the Image box", svc.Name)
	}
	image := runtime.ImageForArch(dc, in.WorkerArch)
	if custom != "" {
		image = custom
	}

	ports := map[string]any{}
	for _, mapping := range dc.Ports {
		key, binding, err := parsePort(mapping)
		if err != nil {
			return DeploySpec{}, fmt.Errorf("invalid catalog port for %s: %w", svc.Slug, err)
		}
		ports[key] = binding
	}
	if dc.FenceGated {
		if !hasFeature(in.WorkerFeatures, "fence_gated") {
			return DeploySpec{}, fmt.Errorf("this worker is too old to run %s behind the LAN fence. Update the worker first; a node it runs keeps running as it is until then", svc.Slug)
		}
		if err := fenceGatedPorts(dc, env, ports); err != nil {
			return DeploySpec{}, fmt.Errorf("invalid value for %s: %w. Nothing was deployed", svc.Slug, err)
		}
	}

	volumes := map[string]Mount{}
	keysByTarget := map[string]map[string]bool{}
	for _, mapping := range dc.Volumes {
		parts := strings.Split(mapping, ":")
		if len(parts) < 2 {
			continue
		}
		mode := "rw"
		if len(parts) > 2 {
			mode = parts[2]
		}
		volumes[fill(parts[0], env)] = Mount{Bind: parts[1], Mode: mode}
		for _, m := range varPattern.FindAllStringSubmatch(parts[0], -1) {
			if keysByTarget[parts[1]] == nil {
				keysByTarget[parts[1]] = map[string]bool{}
			}
			keysByTarget[parts[1]][m[1]] = true
		}
	}
	// A path typed on this deploy is a move the owner asked for; every other mount of
	// a container being replaced stays where it is (the worker's moved_mounts).
	moved := []string{}
	typed := typedValues(dc.Env, in.Typed)
	for target, keys := range keysByTarget {
		for key := range keys {
			if typed[key] {
				moved = append(moved, target)
				break
			}
		}
	}
	sort.Strings(moved)

	spec := DeploySpec{
		Image:       image,
		Env:         env,
		Ports:       ports,
		Volumes:     volumes,
		CapAdd:      nonEmpty(dc.CapAdd),
		Devices:     nonEmpty(dc.Devices),
		Privileged:  dc.Privileged,
		Resources:   resources(dc.Resources),
		MovedMounts: moved,
	}
	if dc.NetworkMode != "" {
		mode := dc.NetworkMode
		spec.NetworkMode = &mode
	}
	if dc.Command != "" {
		command := fill(dc.Command, env)
		spec.Command = &command
	}
	return spec, nil
}

// deployEnv is every declared variable: its catalog default, then the stored value,
// then the typed one, each only when not blank, with {hostname} as the worker's name.
// Only declared keys go out: a worker refuses an undeclared one on a service that
// keeps a wallet, and nothing else reads one.
func deployEnv(vars []catalog.EnvVar, in SpecInput) map[string]string {
	env := make(map[string]string, len(vars))
	for _, v := range vars {
		value := v.Default
		if stored := strings.TrimSpace(in.Stored[v.Key]); stored != "" {
			value = stored
		}
		if typed := strings.TrimSpace(in.Typed[v.Key]); typed != "" {
			value = typed
		}
		if strings.Contains(v.Default, "{hostname}") && prefilled(value, v.Default, in.LocalHostname) {
			value = v.Default
		}
		env[v.Key] = value
	}
	for key, value := range env {
		env[key] = strings.ReplaceAll(value, "{hostname}", in.DeviceName)
	}
	return env
}

// prefilled reports whether a device-name value is what the form or this computer
// filled in, rather than a name the owner chose: the template itself, or the template
// with this computer's name (or the "desktop" fallback) in it. Sent as is, every
// worker would register under this computer's name.
func prefilled(value, template, localHostname string) bool {
	if value == template {
		return true
	}
	for _, name := range []string{localHostname, "desktop"} {
		if name != "" && value == strings.ReplaceAll(template, "{hostname}", name) {
			return true
		}
	}
	return false
}

// typedValues are the keys the owner typed on this deploy: not blank, and not the
// catalog default the form shows.
func typedValues(vars []catalog.EnvVar, typed map[string]string) map[string]bool {
	out := map[string]bool{}
	for _, v := range vars {
		value := strings.TrimSpace(typed[v.Key])
		if value != "" && value != strings.TrimSpace(v.Default) {
			out[v.Key] = true
		}
	}
	return out
}

func missingRequired(vars []catalog.EnvVar, env map[string]string) []string {
	var out []string
	for _, v := range vars {
		if v.Required && strings.TrimSpace(env[v.Key]) == "" {
			out = append(out, label(v))
		}
	}
	return out
}

func invalidValues(vars []catalog.EnvVar, env map[string]string) []string {
	var out []string
	for _, v := range vars {
		if !v.AcceptsValue(env[v.Key]) {
			out = append(out, label(v))
		}
	}
	return out
}

func label(v catalog.EnvVar) string {
	if v.Label != "" {
		return v.Label
	}
	return v.Key
}

var varPattern = regexp.MustCompile(`\$\{(\w+)\}`)

// fill replaces each ${VAR} with its env value, leaving an unknown one as written.
func fill(template string, env map[string]string) string {
	return varPattern.ReplaceAllStringFunc(template, func(m string) string {
		if value, ok := env[m[2:len(m)-1]]; ok {
			return value
		}
		return m
	})
}

// portPattern is the web catalog's _PORT_RE: [ADDRESS:]HOST:CONTAINER[/tcp|/udp].
var portPattern = regexp.MustCompile(`^(?:([^:]+):)?(\d+):(\d+)(?:/(tcp|udp))?$`)

// parsePort is web catalog.parse_port: "9981:9981" gives ("9981/tcp", 9981), and
// "127.0.0.1:9980:9980" gives ("9980/tcp", ["127.0.0.1", 9980]).
func parsePort(mapping string) (string, any, error) {
	raw := strings.TrimSpace(mapping)
	m := portPattern.FindStringSubmatch(raw)
	if m == nil {
		return "", nil, fmt.Errorf("port %q must be HOST:CONTAINER or ADDRESS:HOST:CONTAINER, optionally ending in /tcp or /udp", raw)
	}
	host, _ := strconv.Atoi(m[2])
	container, _ := strconv.Atoi(m[3])
	if host < 1 || host > 65535 || container < 1 || container > 65535 {
		return "", nil, fmt.Errorf("port %q is outside 1-65535", raw)
	}
	protocol := m[4]
	if protocol == "" {
		protocol = "tcp"
	}
	key := fmt.Sprintf("%d/%s", container, protocol)
	if m[1] == "" {
		return key, host, nil
	}
	if !ipv4Pattern.MatchString(m[1]) {
		return "", nil, fmt.Errorf("port %q must bind to an IPv4 address, not %q", raw, m[1])
	}
	return key, []any{m[1], host}, nil
}

var ipv4Pattern = regexp.MustCompile(`^(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)(\.(25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)){3}$`)

// fenceGatedPorts adds the WebUI port on each address ui_port names and the UDP range
// udp_port_range names, published 1:1 (web main.fence_gated_ports).
func fenceGatedPorts(dc catalog.DockerConfig, env map[string]string, ports map[string]any) error {
	if ui := dc.UIPort; ui != nil {
		addrs, err := catalog.UIBindings(env[ui.Env])
		if err != nil {
			return err
		}
		bindings := make([]any, 0, len(addrs))
		for _, a := range addrs {
			bindings = append(bindings, []any{a.String(), ui.Port})
		}
		key := fmt.Sprintf("%d/tcp", ui.Port)
		if len(bindings) == 1 {
			ports[key] = bindings[0]
		} else {
			ports[key] = bindings
		}
	}
	if rng := dc.UDPPortRange; rng != nil {
		list, err := catalog.ExpandUDPRange(env[rng.Env], rng.Cap())
		if err != nil {
			return err
		}
		for _, p := range list {
			ports[fmt.Sprintf("%d/udp", p)] = p
		}
	}
	return nil
}

func resources(r catalog.ResourceLimits) *Resources {
	if r.MemLimit == "" && r.MemReservation == "" && r.OomScoreAdj == nil && r.CPUShares == 0 {
		return nil
	}
	return &Resources{MemLimit: r.MemLimit, MemReservation: r.MemReservation, OomScoreAdj: r.OomScoreAdj, CPUShares: r.CPUShares}
}

func nonEmpty(list []string) []string {
	if len(list) == 0 {
		return nil
	}
	return append([]string(nil), list...)
}

func hasFeature(features []string, want string) bool {
	for _, f := range features {
		if f == want {
			return true
		}
	}
	return false
}

// PerDeviceKeys are the variables holding the id the provider knows one device by.
func PerDeviceKeys(svc catalog.Service) []string {
	var out []string
	for _, v := range svc.Docker.Env {
		if v.PerDevice {
			out = append(out, v.Key)
		}
	}
	return out
}

// KeepsIdentity reports whether the service keeps a wallet or node identity, which
// the worker runs only as its catalog says or with a signed approval.
func KeepsIdentity(svc catalog.Service) bool {
	return !svc.Custom && len(svc.Docker.CriticalVolumes) > 0
}

var unsafeName = regexp.MustCompile(`[^a-z0-9-]+`)

// DeviceName is what {hostname} becomes on a worker (web main._device_hostname): the
// hostname prefix and eight hex characters of a hash of the worker's client id, so
// two boxes never register one device name and nothing the worker sends reaches a
// provider's command line. A worker with no client id is named by its row id.
func DeviceName(prefix, clientID string, workerID int64) string {
	prefix = strings.Trim(unsafeName.ReplaceAllString(strings.ToLower(prefix), "-"), "-")
	if len(prefix) > 32 {
		prefix = strings.Trim(prefix[:32], "-")
	}
	if prefix == "" {
		prefix = "cashpilot"
	}
	identity := clientID
	if identity == "" {
		identity = fmt.Sprintf("worker-id:%d", workerID)
	}
	sum := sha256.Sum256([]byte("cashpilot-device-name:" + identity))
	return prefix + "-" + hex.EncodeToString(sum[:])[:8]
}

// ErrNoWorker and ErrChooseWorker are the worker choice's two refusals.
var (
	ErrNoWorker     = errors.New("no worker is online that can take commands")
	ErrChooseWorker = errors.New("several workers are online: choose the one to deploy to")
)

// PickWorker is the web app's worker choice (_resolve_worker_id): the worker asked
// for, or with none asked for (0) the one online worker; none online, or several,
// is a refusal that says so.
func PickWorker(requested int64, online []int64) (int64, error) {
	if requested > 0 {
		return requested, nil
	}
	switch len(online) {
	case 0:
		return 0, ErrNoWorker
	case 1:
		return online[0], nil
	}
	return 0, ErrChooseWorker
}
