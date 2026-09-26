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
// worst kind of difference: invisible. So the export writes the hardening the web
// CashPilot worker deploys with (app/orchestrator.py) — every capability dropped,
// only the ones the entry declares added back, no new privileges, a PID ceiling, the
// declared resource limits, and the declared stop grace period. That is a real,
// running configuration for these images rather than an invention here, and it is
// the same set Desktop's own deploy path applies (internal/runtime/hardening.go),
// so a container started from the file is exactly as protected as one started
// from the dashboard.
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
	"github.com/GeiserX/CashPilot-Desktop/internal/preflight"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
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
	var settings []setting
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
		block, keys, changeable, err := serviceBlock(svc, family, opts.Hostname)
		if err != nil {
			return "", err
		}
		out.Services[containerPrefix+slug] = block
		placeholders = append(placeholders, keys...)
		settings = append(settings, changeable...)
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
	return header(placeholders, settings) + body, nil
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
	Entrypoint    []string          `yaml:"entrypoint,omitempty"`
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

// serviceBlock builds one service's compose block and returns the keys the user has
// to fill in for it, and the ones it already carries a value for that they may want
// to change.
func serviceBlock(svc catalog.Service, family, hostname string) (*composeService, []string, []setting, error) {
	image := imageFor(svc.Docker, family)
	if image == "" {
		return nil, nil, nil, fmt.Errorf("%s has no container image to export; it is set up outside CashPilot", svc.Name)
	}
	// A file for a CPU the image has no build for looks runnable and dies with
	// "exec format error" on the machine it was made for. The wizard's preflight
	// already knows which entries publish which builds, so the export asks it and
	// refuses, rather than falling back to the default image and hoping. An entry
	// that declares no platforms at all is not refused: nothing is known, and the
	// preflight reports that as "not checked" for the same reason.
	if family != "" {
		if supported, known := preflight.Supports(svc.Docker, family); known && !supported {
			return nil, nil, nil, fmt.Errorf("%s has no build for %s; it publishes %s", svc.Name, preflight.Label(family), preflight.Builds(svc.Docker))
		}
	}

	category := svc.Category
	if category == "" {
		category = defaultCategory
	}

	env, placeholders, settings := environment(svc.Docker.Env, hostname)
	devices, err := devicesFor(svc)
	if err != nil {
		return nil, nil, nil, err
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
		// back, and no path to new privileges. Same set the web CashPilot worker
		// runs these images under.
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
	// Every $ in an entrypoint is escaped, not only ${KEY}: a shell wrapper uses bare
	// $F and $@, and Compose would fill those from the host's environment (empty),
	// silently breaking the script. Nothing in an entrypoint is meant for Compose.
	for _, part := range svc.Docker.Entrypoint {
		block.Entrypoint = append(block.Entrypoint, escapeValue(part))
	}
	if svc.Docker.Command != "" {
		block.Command = interpolate(svc.Docker.Command, env)
	}
	// A container SIGKILLed before it has finished writing loses work — Storj drops
	// in-flight pieces and audit score — so the entry's own stop timeout travels
	// with the file instead of leaving it on Docker's 10-second default.
	// An entry that declares none gets what the runtime gives it (30 s), not
	// Docker's 10: an exported container must not stop faster than a deployed one.
	block.StopGrace = fmt.Sprintf("%ds", runtime.StopTimeoutSeconds(svc))
	return block, placeholders, settings, nil
}

// setting is a value the exported file already carries and the user may want to
// change: a device name, a disk allocation, an opt-in flag. It is listed in the
// header with the value it was given, so changing it is one line in the .env file
// rather than an edit to the YAML.
type setting struct {
	Key   string
	Value string
	// FromMachine records that the value contains the name of the machine the file
	// was exported FROM. Two machines running one provider under one device name
	// register as a single device, and a provider that keys on the device id then
	// pays for one of them, so the header says so where it happens.
	FromMachine bool
}

// environment builds the environment block, and returns the keys the user still has
// to supply plus the ones that already carry a value they can change.
//
// A value the user owns — a password, a token, an account email — is NEVER written.
// It becomes ${KEY}, which Compose fills from a .env file at run time, so the
// exported file carries no secret and can be shared as-is.
//
// A catalog default (a device name, a disk allocation, an opt-in flag) is not the
// user's to keep private, and a file that asks for it is a file nobody can run
// unedited — so it is written as ${KEY:-default}: the default is what the container
// gets, and the same .env file that holds the credentials overrides it. That matters
// most for the device name, which carries the exporting machine's name and has to be
// changed when the file is run somewhere else.
//
// A default that itself contains a dollar sign is written literally instead, escaped
// as it always was: nesting it inside ${KEY:-...} would hand Compose an expression
// whose meaning depends on how it parses the inner text.
func environment(vars []catalog.EnvVar, hostname string) (map[string]string, []string, []setting) {
	env := map[string]string{}
	var placeholders []string
	var settings []setting
	for _, item := range vars {
		key := strings.TrimSpace(item.Key)
		if key == "" {
			continue
		}
		def := strings.TrimSpace(item.Default)
		switch {
		case def != "" && !item.Secret:
			value := substituteHostname(def, hostname)
			if strings.Contains(value, "$") {
				env[key] = escapeValue(value)
				continue
			}
			env[key] = "${" + key + ":-" + value + "}"
			settings = append(settings, setting{
				Key:         key,
				Value:       value,
				FromMachine: hostname != "" && strings.Contains(def, "{hostname}"),
			})
		case item.Required || item.Secret:
			env[key] = "${" + key + "}"
			placeholders = append(placeholders, key)
		}
	}
	if len(env) == 0 {
		return nil, nil, nil
	}
	return env, placeholders, settings
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

// devicesFor returns the host devices to map, refusing anything outside the ceiling
// internal/runtime enforces on a deploy: one list, so the file and the dashboard
// can never disagree about what a service may touch.
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
		if !runtime.AllowedDevice(host) {
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
// to fill in, what they can change, and how to run it. The placeholder list is the
// load-bearing part — without it a Compose run substitutes empty strings and the
// container starts, authenticates against nothing and earns nothing.
func header(placeholders []string, settings []setting) string {
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
	if changeable := uniqueSettings(settings); len(changeable) > 0 {
		out.WriteString("# These already have a value. Put any of them in the same .env file to\n")
		out.WriteString("# change it:\n")
		named := false
		for _, item := range changeable {
			out.WriteString("#   " + item.Key + "=" + item.Value + "\n")
			named = named || item.FromMachine
		}
		if named {
			out.WriteString("#\n")
			out.WriteString("# The device names above are this computer's name. Running this file on\n")
			out.WriteString("# another machine without changing them puts both machines under one\n")
			out.WriteString("# device, and some providers then pay for only one of them.\n")
		}
		out.WriteString("#\n")
	}
	out.WriteString("# Start it with: docker compose up -d\n")
	out.WriteString("#\n")
	out.WriteString("# The cashpilot labels are kept so CashPilot can still find and watch\n")
	out.WriteString("# these containers, even though it did not start them.\n\n")
	return out.String()
}

// uniqueSettings drops the duplicates a multi-service export produces and orders the
// list by key, so the same selection always writes the same header.
func uniqueSettings(values []setting) []setting {
	seen := map[string]bool{}
	out := make([]setting, 0, len(values))
	for _, item := range values {
		if item.Key == "" || seen[item.Key] {
			continue
		}
		seen[item.Key] = true
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
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
