package compose

import (
	"os"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"gopkg.in/yaml.v3"
)

// Every assertion here reads the generated file back through a YAML parser and looks
// at the structure, the way Docker and Podman do. Matching text would pass on a file
// that happens to contain the right words in the wrong place, and fail on a
// reformatted file that runs identically.

type fakeCatalog map[string]catalog.Service

func (f fakeCatalog) Get(slug string) (catalog.Service, bool) {
	svc, ok := f[slug]
	return svc, ok
}

// parse reads a generated compose file back into plain maps.
func parse(t *testing.T, text string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := yaml.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("generated file is not valid YAML: %v\n%s", err, text)
	}
	return out
}

// serviceBlockOf returns one service block from a parsed file.
func serviceBlockOf(t *testing.T, file map[string]any, name string) map[string]any {
	t.Helper()
	services, ok := file["services"].(map[string]any)
	if !ok {
		t.Fatalf("file has no services mapping: %#v", file)
	}
	block, ok := services[name].(map[string]any)
	if !ok {
		t.Fatalf("file has no service %q; it has %v", name, keysOf(services))
	}
	return block
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for key := range m {
		out = append(out, key)
	}
	return out
}

func mapOf(t *testing.T, block map[string]any, key string) map[string]any {
	t.Helper()
	out, ok := block[key].(map[string]any)
	if !ok {
		t.Fatalf("%q is not a mapping: %#v", key, block[key])
	}
	return out
}

func listOf(t *testing.T, block map[string]any, key string) []string {
	t.Helper()
	raw, ok := block[key].([]any)
	if !ok {
		t.Fatalf("%q is not a list: %#v", key, block[key])
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		out = append(out, item.(string))
	}
	return out
}

// honeygain is the shape of an ordinary bandwidth entry: two credentials the user
// owns, one catalog default, and a command that references all three.
func honeygain() catalog.Service {
	return catalog.Service{
		Name:     "Honeygain",
		Slug:     "honeygain",
		Category: "bandwidth",
		Status:   "active",
		Docker: catalog.DockerConfig{
			Image: "honeygain/honeygain@sha256:159d120caac9f482074d3e8d82d4cc247cd07e65af37bcc8e6b89c60674d5ed4",
			Env: []catalog.EnvVar{
				{Key: "HONEYGAIN_EMAIL", Label: "Email", Required: true},
				{Key: "HONEYGAIN_PASSWORD", Label: "Password", Required: true, Secret: true},
				{Key: "HONEYGAIN_DEVICE_NAME", Label: "Device name", Default: "cashpilot-{hostname}"},
			},
			Command:   "-tou-accept -email ${HONEYGAIN_EMAIL} -pass ${HONEYGAIN_PASSWORD} -device ${HONEYGAIN_DEVICE_NAME}",
			Resources: catalog.ResourceLimits{MemLimit: "256m", OomScoreAdj: intPtr(200)},
		},
	}
}

func intPtr(value int) *int { return &value }

func generate(t *testing.T, services fakeCatalog, slugs []string, opts Options) string {
	t.Helper()
	text, err := Generate(services, slugs, opts)
	if err != nil {
		t.Fatalf("Generate(%v): %v", slugs, err)
	}
	return text
}

// THE RULE: the exported file starts the same container CashPilot would.
func TestExportedServiceRunsWhatTheCatalogDeclares(t *testing.T) {
	file := parse(t, generate(t, fakeCatalog{"honeygain": honeygain()}, []string{"honeygain"}, Options{Hostname: "mac-mini"}))
	block := serviceBlockOf(t, file, "cashpilot-honeygain")

	if block["image"] != honeygain().Docker.Image {
		t.Errorf("image = %v, want the catalog's pinned image", block["image"])
	}
	if block["container_name"] != "cashpilot-honeygain" {
		t.Errorf("container_name = %v", block["container_name"])
	}
	if block["hostname"] != "cashpilot-honeygain" {
		t.Errorf("hostname = %v", block["hostname"])
	}
	if block["restart"] != "unless-stopped" {
		t.Errorf("restart = %v, want unless-stopped so the earner comes back after a reboot", block["restart"])
	}

	labels := mapOf(t, block, "labels")
	for key, want := range map[string]string{
		"cashpilot.managed":     "true",
		"cashpilot.service":     "honeygain",
		"cashpilot.version":     "1",
		"cashpilot.category":    "bandwidth",
		"cashpilot.deployed-by": "compose",
	} {
		if labels[key] != want {
			t.Errorf("label %s = %v, want %q (CashPilot finds these containers by label)", key, labels[key], want)
		}
	}

	logging := mapOf(t, block, "logging")
	if logging["driver"] != "json-file" {
		t.Errorf("logging driver = %v", logging["driver"])
	}
	options := mapOf(t, logging, "options")
	if options["max-size"] != "10m" || options["max-file"] != "3" {
		t.Errorf("log rotation = %v, want 10m x 3 so a chatty image cannot fill the disk", options)
	}
}

// THE RULE: no credential is ever written into the file.
//
// The export is a file people keep in a repository, paste into a ticket, or copy to
// the other half of a fleet. A password written into it leaks the moment the file
// moves, and nothing about the file looks different.
func TestCredentialsAreWrittenAsPlaceholdersNeverValues(t *testing.T) {
	text := generate(t, fakeCatalog{"honeygain": honeygain()}, []string{"honeygain"}, Options{Hostname: "mac-mini"})
	block := serviceBlockOf(t, parse(t, text), "cashpilot-honeygain")
	env := mapOf(t, block, "environment")

	for _, key := range []string{"HONEYGAIN_EMAIL", "HONEYGAIN_PASSWORD"} {
		if env[key] != "${"+key+"}" {
			t.Errorf("environment[%s] = %v, want the ${%s} placeholder", key, env[key], key)
		}
	}
	if !strings.Contains(text, "HONEYGAIN_PASSWORD=") {
		t.Error("the header does not tell the user which values to set, so a run would start with empty credentials")
	}
}

// A catalog default is not the user's secret, and a file that asks for it is a file
// nobody can run unedited. It is written out — including into the command, where a
// live ${VAR} would expand to an empty string because Compose reads .env there, not
// the service's own environment block.
func TestCatalogDefaultsAreWrittenOutAndReachTheCommand(t *testing.T) {
	block := serviceBlockOf(t,
		parse(t, generate(t, fakeCatalog{"honeygain": honeygain()}, []string{"honeygain"}, Options{Hostname: "mac-mini"})),
		"cashpilot-honeygain")

	env := mapOf(t, block, "environment")
	if env["HONEYGAIN_DEVICE_NAME"] != "cashpilot-mac-mini" {
		t.Errorf("device name = %v, want the machine name filled in", env["HONEYGAIN_DEVICE_NAME"])
	}

	command, _ := block["command"].(string)
	if !strings.Contains(command, "-device cashpilot-mac-mini") {
		t.Errorf("command = %q, want the device name spelled out", command)
	}
	if !strings.Contains(command, "-email ${HONEYGAIN_EMAIL}") || !strings.Contains(command, "-pass ${HONEYGAIN_PASSWORD}") {
		t.Errorf("command = %q, want the credentials left as placeholders the .env fills", command)
	}
}

// A ${VAR} the entry never declares must stay visible. Left live, Compose replaces
// it with an empty string and the container starts with one argument silently gone.
func TestAnUndeclaredPlaceholderIsLeftVisible(t *testing.T) {
	svc := catalog.Service{
		Name: "ProxyRack", Slug: "proxyrack", Status: "active",
		Docker: catalog.DockerConfig{Image: "proxyrack/pop:latest", Command: "--uuid ${UUID}"},
	}
	block := serviceBlockOf(t,
		parse(t, generate(t, fakeCatalog{"proxyrack": svc}, []string{"proxyrack"}, Options{})),
		"cashpilot-proxyrack")
	// $${UUID} on disk is how Compose is told to leave the text alone: the container
	// receives the literal ${UUID}, which the user can see and replace.
	if command, _ := block["command"].(string); command != "--uuid $${UUID}" {
		t.Errorf("command = %q, want the placeholder escaped so Compose does not blank it", command)
	}
}

// A value containing a dollar sign must survive Compose's own interpolation pass.
func TestADollarSignInADefaultSurvives(t *testing.T) {
	svc := catalog.Service{
		Name: "Dollars", Slug: "dollars", Status: "active",
		Docker: catalog.DockerConfig{
			Image: "example/dollars:1.0",
			Env:   []catalog.EnvVar{{Key: "TAG", Default: "a$b"}},
		},
	}
	block := serviceBlockOf(t,
		parse(t, generate(t, fakeCatalog{"dollars": svc}, []string{"dollars"}, Options{})),
		"cashpilot-dollars")
	if got := mapOf(t, block, "environment")["TAG"]; got != "a$$b" {
		t.Errorf("TAG = %v, want a$$b so Compose hands the container a$b", got)
	}
}

// THE RULE: the exported file is no weaker than a CashPilot deploy.
func TestExportCarriesTheSameHardeningTheAppDeploysWith(t *testing.T) {
	mysterium := catalog.Service{
		Name: "MystNodes", Slug: "mysterium", Category: "bandwidth", Status: "active",
		Docker: catalog.DockerConfig{
			Image:       "mysteriumnetwork/myst@sha256:1b68",
			NetworkMode: "host",
			CapAdd:      []string{"NET_ADMIN", "SETUID", "SETGID"},
			Devices:     []string{"/dev/net/tun"},
			Volumes:     []string{"mysterium-data:/var/lib/mysterium-node"},
			Resources:   catalog.ResourceLimits{MemLimit: "768m", MemReservation: "256m", CPUShares: 512, OomScoreAdj: intPtr(-100)},
			StopTimeout: 300,
		},
	}
	file := parse(t, generate(t, fakeCatalog{"mysterium": mysterium}, []string{"mysterium"}, Options{}))
	block := serviceBlockOf(t, file, "cashpilot-mysterium")

	if got := listOf(t, block, "cap_drop"); len(got) != 1 || got[0] != "ALL" {
		t.Errorf("cap_drop = %v, want [ALL]", got)
	}
	if got := strings.Join(listOf(t, block, "cap_add"), ","); got != "NET_ADMIN,SETUID,SETGID" {
		t.Errorf("cap_add = %v, want exactly the capabilities the entry declares", got)
	}
	if got := listOf(t, block, "security_opt"); len(got) != 1 || got[0] != "no-new-privileges:true" {
		t.Errorf("security_opt = %v, want no-new-privileges", got)
	}
	if block["pids_limit"] != 512 {
		t.Errorf("pids_limit = %v, want a PID ceiling", block["pids_limit"])
	}
	if got := listOf(t, block, "devices"); len(got) != 1 || got[0] != "/dev/net/tun" {
		t.Errorf("devices = %v; without the TUN device the node registers and carries no traffic", got)
	}
	if block["network_mode"] != "host" {
		t.Errorf("network_mode = %v, want host", block["network_mode"])
	}
	if block["mem_limit"] != "768m" || block["mem_reservation"] != "256m" || block["cpu_shares"] != 512 || block["oom_score_adj"] != -100 {
		t.Errorf("resource limits lost: %v %v %v %v", block["mem_limit"], block["mem_reservation"], block["cpu_shares"], block["oom_score_adj"])
	}
	if block["stop_grace_period"] != "300s" {
		t.Errorf("stop_grace_period = %v; a node killed early loses in-flight work", block["stop_grace_period"])
	}

	volumes := mapOf(t, file, "volumes")
	if _, ok := volumes["mysterium-data"]; !ok {
		t.Errorf("top-level volumes = %v, want mysterium-data declared or Compose refuses to start", keysOf(volumes))
	}
}

// A port mapping is written in quotes. podman-compose reads the file with an older
// YAML parser that resolves an unquoted 22:22 as a number (1342), so the container
// comes up published on a port nobody asked for.
func TestPortsAreQuoted(t *testing.T) {
	svc := catalog.Service{
		Name: "Sshish", Slug: "sshish", Status: "active",
		Docker: catalog.DockerConfig{Image: "example/sshish:1.0", Ports: []string{"22:22"}},
	}
	text := generate(t, fakeCatalog{"sshish": svc}, []string{"sshish"}, Options{})
	if !strings.Contains(text, `"22:22"`) {
		t.Errorf("port mapping was written unquoted:\n%s", text)
	}
	if got := listOf(t, serviceBlockOf(t, parse(t, text), "cashpilot-sshish"), "ports"); len(got) != 1 || got[0] != "22:22" {
		t.Errorf("ports = %v, want the mapping the catalog declares", got)
	}
}

// An entry with no limits gets no limit keys: absent stays absent rather than being
// written as a zero that Docker would read as a real setting.
func TestAnEntryWithoutLimitsGetsNoLimitKeys(t *testing.T) {
	block := serviceBlockOf(t,
		parse(t, generate(t, fakeCatalog{"honeygain": honeygain()}, []string{"honeygain"}, Options{})),
		"cashpilot-honeygain")
	for _, key := range []string{"mem_reservation", "cpu_shares", "stop_grace_period", "devices", "cap_add", "ports", "volumes"} {
		if _, ok := block[key]; ok {
			t.Errorf("%s was written for an entry that declares none: %v", key, block[key])
		}
	}
	if _, ok := block["oom_score_adj"]; !ok {
		t.Error("oom_score_adj was dropped, but this entry declares one")
	}
}

// A host path is not a named volume, so it must not appear in the top-level block.
func TestAHostPathIsNotDeclaredAsANamedVolume(t *testing.T) {
	svc := catalog.Service{
		Name: "Storj", Slug: "storj", Status: "active",
		Docker: catalog.DockerConfig{Image: "storjlabs/storagenode:latest", Volumes: []string{"/mnt/data:/app/config"}},
	}
	file := parse(t, generate(t, fakeCatalog{"storj": svc}, []string{"storj"}, Options{}))
	if _, ok := file["volumes"]; ok {
		t.Errorf("top-level volumes = %v, want none for a host-path mount", file["volumes"])
	}
}

// The per-architecture image, for the images whose ARM builds Docker cannot pick.
func TestArchitectureChoosesTheBuildThatCanRun(t *testing.T) {
	traffmonetizer := catalog.Service{
		Name: "Traffmonetizer", Slug: "traffmonetizer", Status: "active",
		Docker: catalog.DockerConfig{
			Image:       "traffmonetizer/cli_v2@sha256:6dbf",
			ImageByArch: map[string]string{"arm64": "traffmonetizer/cli_v2:arm64v8", "arm": "traffmonetizer/cli_v2:arm32v7"},
		},
	}
	services := fakeCatalog{"traffmonetizer": traffmonetizer}

	for _, tc := range []struct{ arch, want string }{
		{"", "traffmonetizer/cli_v2@sha256:6dbf"},
		{"amd64", "traffmonetizer/cli_v2@sha256:6dbf"},
		{"arm64", "traffmonetizer/cli_v2:arm64v8"},
		{"arm", "traffmonetizer/cli_v2:arm32v7"},
		{" ARM64 ", "traffmonetizer/cli_v2:arm64v8"},
	} {
		block := serviceBlockOf(t,
			parse(t, generate(t, services, []string{"traffmonetizer"}, Options{Arch: tc.arch})),
			"cashpilot-traffmonetizer")
		if block["image"] != tc.want {
			t.Errorf("arch %q: image = %v, want %q", tc.arch, block["image"], tc.want)
		}
	}
}

// An entry with no override keeps its image whatever the architecture is: Docker
// resolves the build from the manifest itself.
func TestAnEntryWithoutOverridesKeepsItsImageOnEveryArchitecture(t *testing.T) {
	services := fakeCatalog{"honeygain": honeygain()}
	for _, arch := range []string{"amd64", "arm64", "arm"} {
		block := serviceBlockOf(t,
			parse(t, generate(t, services, []string{"honeygain"}, Options{Arch: arch})),
			"cashpilot-honeygain")
		if block["image"] != honeygain().Docker.Image {
			t.Errorf("arch %s: image = %v", arch, block["image"])
		}
	}
}

// Everything the export cannot honestly produce is refused, because each of these
// would otherwise be a file that looks runnable and earns nothing.
func TestTheExportRefusesRatherThanProducingAFileThatCannotEarn(t *testing.T) {
	services := fakeCatalog{
		"honeygain": honeygain(),
		"presearch": {Name: "Presearch", Slug: "presearch", Status: "dead", Docker: catalog.DockerConfig{Image: "presearch/node:latest"}},
		"salad":     {Name: "Salad", Slug: "salad", Status: "active"},
		"kmem": {Name: "Kmem", Slug: "kmem", Status: "active", Docker: catalog.DockerConfig{
			Image: "example/kmem:1.0", Devices: []string{"/dev/mem"},
		}},
	}

	for _, tc := range []struct {
		name  string
		slugs []string
		opts  Options
		want  string
	}{
		{"an unknown slug", []string{"nosuchservice"}, Options{}, "unknown service"},
		{"a retired service", []string{"presearch"}, Options{}, "no longer available"},
		{"a service with no image", []string{"salad"}, Options{}, "no container image"},
		{"a device outside the ceiling", []string{"kmem"}, Options{}, "does not export"},
		{"an unknown architecture", []string{"honeygain"}, Options{Arch: "riscv64"}, "unknown architecture"},
		{"nothing selected", nil, Options{}, "at least one service"},
	} {
		text, err := Generate(services, tc.slugs, tc.opts)
		if err == nil {
			t.Errorf("%s produced a file instead of an error:\n%s", tc.name, text)
			continue
		}
		if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: error %q does not explain the problem (want %q)", tc.name, err, tc.want)
		}
	}
}

// A selection exports every service in it, once each.
func TestASelectionExportsEveryServiceOnce(t *testing.T) {
	second := honeygain()
	second.Name, second.Slug = "Earn.fm", "earnfm"
	second.Docker.Image = "earnfm/earnfm-client:latest"
	services := fakeCatalog{"honeygain": honeygain(), "earnfm": second}

	file := parse(t, generate(t, services, []string{"honeygain", "earnfm", "honeygain"}, Options{}))
	blocks, ok := file["services"].(map[string]any)
	if !ok || len(blocks) != 2 {
		t.Fatalf("services = %v, want exactly two blocks", keysOf(blocks))
	}
	for _, name := range []string{"cashpilot-honeygain", "cashpilot-earnfm"} {
		block := serviceBlockOf(t, file, name)
		if mapOf(t, block, "labels")["cashpilot.managed"] != "true" {
			t.Errorf("%s is not labelled as managed", name)
		}
	}
}

// And the file has to hold for the catalog that actually ships, not only fixtures:
// every visible container service exports to a file that parses and names an image.
func TestEveryShippingServiceExports(t *testing.T) {
	cat, err := catalog.LoadEmbedded(os.DirFS("../.."))
	if err != nil {
		t.Fatalf("load catalog: %v", err)
	}
	exported := 0
	for _, svc := range cat.ListVisible() {
		if svc.Docker.Image == "" {
			continue
		}
		exported++
		text, err := Generate(cat, []string{svc.Slug}, Options{Hostname: "mac-mini"})
		if err != nil {
			t.Errorf("%s: %v", svc.Slug, err)
			continue
		}
		block := serviceBlockOf(t, parse(t, text), "cashpilot-"+svc.Slug)
		if block["image"] == "" || block["image"] == nil {
			t.Errorf("%s exported with no image", svc.Slug)
		}
		for _, value := range mapOf(t, block, "labels") {
			if value == nil {
				t.Errorf("%s exported an empty label", svc.Slug)
			}
		}
	}
	// Sixteen entries ship a container image today; the rest are tracked manually or
	// run natively. A number far below that means the catalog did not load and every
	// assertion above ran on nothing.
	if exported < 12 {
		t.Fatalf("only %d services exported; the catalog did not load, so this check proves nothing", exported)
	}
}
