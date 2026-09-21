// Package compose writes a docker-compose.yml for CashPilot services.
//
// It exists so a machine that earns does not have to keep CashPilot running to keep
// earning: the user saves a compose file, runs it with `docker compose up -d` (or
// Podman, or Portainer, or a box's own GitOps), and CashPilot can still see those
// containers afterwards because the file carries the same cashpilot.* labels the
// app's own deploy writes.
//
// Two rules shape everything here, and both are about what the exported file must
// NOT be.
//
// It must not be weaker than a CashPilot deploy. A file that drops the memory
// ceiling, the capability set or the TUN device produces a container that looks
// identical and is either less protected or silently unable to earn, which is the
// worst kind of difference: invisible. So the export writes the same hardening the
// app applies at deploy time — every capability dropped, only the ones the entry
// declares added back, no new privileges, a PID ceiling, the declared resource
// limits, and the declared stop grace period.
//
// And it must not contain a single credential. Everything the user has to supply is
// written as a ${VAR} placeholder, never a value, so the file is safe to keep in a
// repository, paste into a ticket or send to the other half of a fleet. Compose
// fills the placeholders from a .env file beside it at run time.
//
// The structure mirrors the web CashPilot's app/compose_generator.py, which is the
// behaviour this has to match: same container names, same labels, same logging
// rotation, same per-architecture image choice.
package compose

import (
	"fmt"
	"sort"
	"strings"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"gopkg.in/yaml.v3"
)

const (
	containerPrefix = "cashpilot-"

	labelManaged    = "cashpilot.managed"
	labelService    = "cashpilot.service"
	labelVersion    = "cashpilot.version"
	labelCategory   = "cashpilot.category"
	labelDeployedBy = "cashpilot.deployed-by"

	// pidsLimit is the PID ceiling written for every exported service, matching the
	// worker's default (app/orchestrator.py). These images are third-party and
	// closed-source; a runaway one must not be able to exhaust the host's PID
	// namespace. Generous for a real earner — they run a single process tree.
	pidsLimit = 512

	// defaultCategory is what an entry with no category declared is labelled, the
	// same fallback the web exporter uses.
	defaultCategory = "bandwidth"
)

// allowedDevices is the ceiling on host devices an exported file may map in, the
// same one internal/runtime enforces when the app deploys a container itself. A
// device is a direct line to the kernel, so widening it is a deliberate change here,
// never something a catalog entry can do on its own.
//
// /dev/net/tun is on it because Mysterium cannot carry wireguard traffic without it:
// exported without the device the node starts, registers, appears in discovery and
// earns nothing.
var allowedDevices = map[string]bool{"/dev/net/tun": true}

// Source is the slice of the catalog the exporter needs. Taking an interface keeps
// the generator testable against hand-built entries with no services/ directory on
// disk; *catalog.Catalog satisfies it.
type Source interface {
	Get(slug string) (catalog.Service, bool)
}

// Options are the choices the caller has to make for the export, because the file is
// written for a machine that is not necessarily this one.
type Options struct {
	// Arch is the architecture family the file is for: amd64, arm64 or arm. Empty
	// means "whatever the machine running the file resolves from the image manifest",
	// which is right for every entry except the ones whose ARM builds Docker cannot
	// pick itself. Anything else is an error: a typo must not quietly produce an
	// amd64 file for a Raspberry Pi.
	Arch string
	// Hostname fills the {hostname} placeholder catalog defaults use for device
	// names. Empty leaves the placeholder's own default text alone.
	Hostname string
}

// Generate returns the docker-compose.yml text for the given services.
//
// It refuses rather than degrades. An unknown slug, a retired service, a service
// with no image and an unknown architecture are all errors, because each of them
// would otherwise produce a runnable-looking file that cannot earn: a compose file
// silently missing one of the three services the user selected is indistinguishable
// from one that deployed them all.
func Generate(src Source, slugs []string, opts Options) (string, error) {
	family, err := archFamily(opts.Arch)
	if err != nil {
		return "", err
	}
	if len(slugs) == 0 {
		return "", fmt.Errorf("choose at least one service to export")
	}

	out := composeFile{Services: map[string]*composeService{}}
	var placeholders []string
	seen := map[string]bool{}
	for _, slug := range slugs {
		slug = strings.TrimSpace(slug)
		if slug == "" || seen[slug] {
			continue
		}
		seen[slug] = true
		svc, ok := src.Get(slug)
		if !ok {
			return "", fmt.Errorf("unknown service: %s", slug)
		}
		if catalog.IsRetired(svc.Status) {
			return "", fmt.Errorf("%s is no longer available (%s), so there is nothing to export", svc.Name, svc.Status)
		}
		block, keys, err := serviceBlock(svc, family, opts.Hostname)
		if err != nil {
			return "", err
		}
		out.Services[containerPrefix+slug] = block
		placeholders = append(placeholders, keys...)
	}
	if len(out.Services) == 0 {
		return "", fmt.Errorf("choose at least one service to export")
	}

	if volumes := namedVolumes(out.Services); len(volumes) > 0 {
		out.Volumes = volumes
	}

	body, err := marshal(out)
	if err != nil {
		return "", err
	}
	return header(placeholders) + body, nil
}

// composeFile and composeService are the file's shape. They are structs rather than
// maps so the key order is the one a person reads top to bottom (what it runs, what
// it is called, what it is allowed to do), and so an absent catalog value stays
// absent instead of being written as a zero.
type composeFile struct {
	Services map[string]*composeService `yaml:"services"`
	Volumes  map[string]emptyBlock      `yaml:"volumes,omitempty"`
}

// emptyBlock is a top-level named volume with no options: `volumes: {name: {}}`.
type emptyBlock struct{}

type composeService struct {
	Image         string            `yaml:"image"`
	ContainerName string            `yaml:"container_name"`
	Hostname      string            `yaml:"hostname"`
	Restart       string            `yaml:"restart"`
	Labels        map[string]string `yaml:"labels"`
	Logging       *loggingBlock     `yaml:"logging"`
	Environment   map[string]string `yaml:"environment,omitempty"`
	Ports         []quoted          `yaml:"ports,omitempty"`
	Volumes       []string          `yaml:"volumes,omitempty"`
	Devices       []string          `yaml:"devices,omitempty"`
	NetworkMode   string            `yaml:"network_mode,omitempty"`
	CapDrop       []string          `yaml:"cap_drop,omitempty"`
	CapAdd        []string          `yaml:"cap_add,omitempty"`
	SecurityOpt   []string          `yaml:"security_opt,omitempty"`
	PidsLimit     int               `yaml:"pids_limit,omitempty"`
	Command       string            `yaml:"command,omitempty"`
	StopGrace     string            `yaml:"stop_grace_period,omitempty"`
	MemLimit      string            `yaml:"mem_limit,omitempty"`
	MemReserve    string            `yaml:"mem_reservation,omitempty"`
	CPUShares     int64             `yaml:"cpu_shares,omitempty"`
	OomScoreAdj   *int              `yaml:"oom_score_adj,omitempty"`
}

// quoted is a scalar that is always written in quotes. Port mappings need it: an
// older YAML parser (podman-compose reads the file with PyYAML) resolves an
// unquoted 22:22 as a sexagesimal number and publishes port 1342 instead, and the
// container that comes up is reachable on nothing.
type quoted string

func (q quoted) MarshalYAML() (any, error) {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: string(q), Style: yaml.DoubleQuotedStyle}, nil
}

type loggingBlock struct {
	Driver  string            `yaml:"driver"`
	Options map[string]string `yaml:"options"`
}

// serviceBlock builds one service's compose block and returns the placeholder keys
// the user has to fill in for it.
func serviceBlock(svc catalog.Service, family, hostname string) (*composeService, []string, error) {
	image := imageFor(svc.Docker, family)
	if image == "" {
		return nil, nil, fmt.Errorf("%s has no container image to export; it is set up outside CashPilot", svc.Name)
	}

	category := svc.Category
	if category == "" {
		category = defaultCategory
	}

	env, placeholders := environment(svc.Docker.Env, hostname)
	devices, err := devicesFor(svc)
	if err != nil {
		return nil, nil, err
	}

	block := &composeService{
		Image:         image,
		ContainerName: containerPrefix + svc.Slug,
		Hostname:      containerPrefix + svc.Slug,
		Restart:       "unless-stopped",
		Labels: map[string]string{
			labelManaged:    "true",
			labelService:    svc.Slug,
			labelVersion:    "1",
			labelCategory:   category,
			labelDeployedBy: "compose",
		},
		Logging: &loggingBlock{
			Driver:  "json-file",
			Options: map[string]string{"max-size": "10m", "max-file": "3"},
		},
		Environment: env,
		Ports:       ports(svc.Docker.Ports),
		Devices:     devices,
		NetworkMode: svc.Docker.NetworkMode,
		// Every capability dropped, then only the ones this entry declares added
		// back, and no path to new privileges. Same set the app's own deploy uses.
		CapDrop:     []string{"ALL"},
		CapAdd:      append([]string(nil), svc.Docker.CapAdd...),
		SecurityOpt: []string{"no-new-privileges:true"},
		PidsLimit:   pidsLimit,
		MemLimit:    svc.Docker.Resources.MemLimit,
		MemReserve:  svc.Docker.Resources.MemReservation,
		CPUShares:   svc.Docker.Resources.CPUShares,
		OomScoreAdj: svc.Docker.Resources.OomScoreAdj,
	}

	for _, volume := range svc.Docker.Volumes {
		block.Volumes = append(block.Volumes, interpolate(volume, env))
	}
	if svc.Docker.Command != "" {
		block.Command = interpolate(svc.Docker.Command, env)
	}
	// A container SIGKILLed before it has finished writing loses work — Storj drops
	// in-flight pieces and audit score — so the entry's own stop timeout travels
	// with the file instead of leaving it on Docker's 10-second default.
	if svc.Docker.StopTimeout > 0 {
		block.StopGrace = fmt.Sprintf("%ds", svc.Docker.StopTimeout)
	}
	return block, placeholders, nil
}

// environment builds the environment block, and returns the keys the user still has
// to supply.
//
// A value the user owns — a password, a token, an account email — is NEVER written.
// It becomes ${KEY}, which Compose fills from a .env file at run time, so the
// exported file carries no secret and can be shared as-is. A catalog default (a
// device name, an opt-in flag) is written literally, because it is not the user's to
// keep private and a file that asks for it is a file nobody can run unedited.
func environment(vars []catalog.EnvVar, hostname string) (map[string]string, []string) {
	env := map[string]string{}
	var placeholders []string
	for _, item := range vars {
		key := strings.TrimSpace(item.Key)
		if key == "" {
			continue
		}
		def := strings.TrimSpace(item.Default)
		switch {
		case def != "" && !item.Secret:
			env[key] = escapeValue(substituteHostname(def, hostname))
		case item.Required || item.Secret:
			env[key] = "${" + key + "}"
			placeholders = append(placeholders, key)
		}
	}
	if len(env) == 0 {
		return nil, nil
	}
	return env, placeholders
}

// interpolate prepares a catalog string (a command line, a volume's host path) for
// the file, where the catalog writes ${KEY} to mean "this service's KEY variable".
//
// Compose does not read a service's own environment block when it expands a command:
// it expands from the .env file beside the compose file. So each ${KEY} takes the
// form that makes the container receive the value it would have received from
// CashPilot:
//
//   - a credential, which the environment block leaves as ${KEY}, stays ${KEY}: the
//     user's .env feeds the command and the environment alike.
//   - a catalog default (a device name), which the environment block carries
//     literally, is written out literally, because a live ${KEY} here would expand to
//     an empty string and the container would start with no device name at all.
//   - a ${KEY} this entry never declares (proxyrack's ${UUID}) is escaped to $${KEY},
//     so Compose leaves the text visible instead of silently blanking the argument.
func interpolate(value string, env map[string]string) string {
	var out strings.Builder
	for i := 0; i < len(value); {
		if value[i] == '$' && i+1 < len(value) && value[i+1] == '{' {
			end := strings.IndexByte(value[i:], '}')
			if end > 0 {
				token := value[i : i+end+1]
				key := token[2 : len(token)-1]
				switch declared, ok := env[key]; {
				case !ok:
					out.WriteString("$" + token)
				case declared == token:
					out.WriteString(token)
				default:
					out.WriteString(declared)
				}
				i += end + 1
				continue
			}
		}
		out.WriteByte(value[i])
		i++
	}
	return out.String()
}

// escapeValue doubles every $ in a literal value. Compose interpolates inside
// unquoted and double-quoted values, so a default containing a dollar sign would
// otherwise change (or vanish) when the exported file runs.
func escapeValue(value string) string {
	return strings.ReplaceAll(value, "$", "$$")
}

func ports(declared []string) []quoted {
	out := make([]quoted, 0, len(declared))
	for _, port := range declared {
		if trimmed := strings.TrimSpace(port); trimmed != "" {
			out = append(out, quoted(trimmed))
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func substituteHostname(value, hostname string) string {
	if hostname == "" {
		return value
	}
	return strings.ReplaceAll(value, "{hostname}", hostname)
}

// devicesFor returns the host devices to map, refusing anything outside the ceiling.
//
// Refusing rather than dropping is deliberate and matches the deploy path: a file
// quietly missing a device its entry asked for produces a container that looks
// healthy and cannot do the job it was exported for.
func devicesFor(svc catalog.Service) ([]string, error) {
	var devices []string
	var blocked []string
	for _, raw := range svc.Docker.Devices {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}
		host := strings.TrimRight(strings.TrimSpace(strings.SplitN(entry, ":", 2)[0]), "/")
		if host == "" {
			return nil, fmt.Errorf("%s declares the device %q with no host path", svc.Name, entry)
		}
		if !allowedDevices[host] {
			blocked = append(blocked, host)
			continue
		}
		devices = append(devices, entry)
	}
	if len(blocked) > 0 {
		return nil, fmt.Errorf("%s asks for devices CashPilot does not export: %s", svc.Name, strings.Join(blocked, ", "))
	}
	return devices, nil
}

// namedVolumes collects the named volumes the services mount, which Compose requires
// declared at the top level of the file or it refuses to start. A host path (/data,
// ./data, ~/data) is not one, and neither is a path still carrying a placeholder.
func namedVolumes(services map[string]*composeService) map[string]emptyBlock {
	out := map[string]emptyBlock{}
	for _, svc := range services {
		for _, volume := range svc.Volumes {
			source := strings.SplitN(volume, ":", 2)[0]
			if source == "" || strings.ContainsAny(source, "/.~$") {
				continue
			}
			out[source] = emptyBlock{}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func marshal(file composeFile) (string, error) {
	var buf strings.Builder
	encoder := yaml.NewEncoder(&buf)
	encoder.SetIndent(2)
	if err := encoder.Encode(file); err != nil {
		return "", err
	}
	if err := encoder.Close(); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// header is what the user reads before the YAML: what this file is, what they have
// to fill in, and how to run it. The placeholder list is the load-bearing part —
// without it a Compose run substitutes empty strings and the container starts,
// authenticates against nothing and earns nothing.
func header(placeholders []string) string {
	var out strings.Builder
	out.WriteString("# Generated by CashPilot Desktop\n")
	out.WriteString("# https://github.com/GeiserX/CashPilot-Desktop\n")
	out.WriteString("#\n")
	if keys := uniqueSorted(placeholders); len(keys) > 0 {
		out.WriteString("# Set these before you start it, in a .env file next to this one:\n")
		for _, key := range keys {
			out.WriteString("#   " + key + "=\n")
		}
		out.WriteString("#\n")
	}
	out.WriteString("# Start it with: docker compose up -d\n")
	out.WriteString("#\n")
	out.WriteString("# The cashpilot labels are kept so CashPilot can still find and watch\n")
	out.WriteString("# these containers, even though it did not start them.\n\n")
	return out.String()
}

func uniqueSorted(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}
