package catalog

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type Catalog struct {
	services []Service
	bySlug   map[string]Service
}

type Service struct {
	Name             string            `json:"name" yaml:"name"`
	Slug             string            `json:"slug" yaml:"slug"`
	Category         string            `json:"category" yaml:"category"`
	Status           string            `json:"status" yaml:"status"`
	Website          string            `json:"website" yaml:"website"`
	Description      string            `json:"description" yaml:"description"`
	ShortDescription string            `json:"shortDescription" yaml:"short_description"`
	Referral         Referral          `json:"referral" yaml:"referral"`
	Docker           DockerConfig      `json:"docker" yaml:"docker"`
	Native           NativeConfig      `json:"native" yaml:"native"`
	Requirements     Requirements      `json:"requirements" yaml:"requirements"`
	Payment          Payment           `json:"payment" yaml:"payment"`
	Earnings         EarningsEstimate  `json:"earnings" yaml:"earnings"`
	Cashout          Cashout           `json:"cashout" yaml:"cashout"`
	Platforms        []string          `json:"platforms" yaml:"platforms"`
	Collector        CollectorMetadata `json:"collector" yaml:"collector"`
	Payout           Payout            `json:"payout" yaml:"payout"`
	Disclosure       Disclosure        `json:"disclosure" yaml:"disclosure"`
	// ResurrectionChecked is the date ("YYYY-MM-DD") on which a human confirmed a
	// dead entry's programme really is gone even though its site still answers. It is
	// carried so the reason a dead service stays dead travels with the entry instead
	// of living only in the web repository's history.
	ResurrectionChecked string `json:"resurrectionChecked" yaml:"resurrection_checked"`
	SourcePath          string `json:"sourcePath" yaml:"-"`
	ManualOnly          bool   `json:"manualOnly" yaml:"-"`
}

type Referral struct {
	SignupURL string `json:"signupUrl" yaml:"signup_url"`
	// Code is the referral code itself, kept alongside the URL so a domain migration
	// that drops the code from signup_url is detectable rather than silent. Referral
	// attribution is revenue: losing it loses money, and it fails invisibly.
	Code string `json:"code" yaml:"code"`
	// Program is three-valued on purpose and absent is NOT false: true means the
	// provider was verified to run a referral programme (true with no Code is an
	// actionable gap), false means it was verified not to, nil means nobody has
	// checked. Collapsing nil into false would turn "unknown" into "nothing to do".
	Program *bool         `json:"program" yaml:"program"`
	Bonus   ReferralBonus `json:"bonus" yaml:"bonus"`
}

// ReferralBonus is what each side gets for a referral, shown on the service detail
// page.
type ReferralBonus struct {
	Referrer string `json:"referrer" yaml:"referrer"`
	Referee  string `json:"referee" yaml:"referee"`
}

// Payout is where the money actually lives, which is a different question from
// Cashout's "how do I withdraw it". Model is the load-bearing field: "internal"
// means there is no address to show and no chain to watch, which is not the same
// as an address nobody has filled in yet.
type Payout struct {
	// Model is one of external, internal, minted, unknown. "unknown" is a deliberate
	// value, not a placeholder.
	Model string `json:"model" yaml:"model"`
	// Chain is the settlement chain, or "none" when no on-chain surface exists at
	// all. Absent means the chain is chosen per withdrawal and there is no
	// persistent on-chain identity.
	Chain string `json:"chain" yaml:"chain"`
	// AddressEnv is the container env var carrying the user's payout address, for
	// external models where the address can be read back from the deployed spec.
	AddressEnv string `json:"addressEnv" yaml:"address_env"`
	// AddressSource is env, manual (set after deploy, e.g. Mysterium's beneficiary
	// via TequilAPI) or app (linked inside the provider's own site).
	AddressSource string `json:"addressSource" yaml:"address_source"`
	Notes         string `json:"notes" yaml:"notes"`
}

// Disclosure is what a service does with the user's machine, in plain words. An
// absent block means nobody has documented it, which the UI must be able to say
// rather than render as a reassuring blank.
type Disclosure struct {
	Sells string `json:"sells" yaml:"sells"`
	// ThirdPartyTraffic opens with a standalone yes/no/unknown followed by
	// punctuation so it can be read programmatically as well as by a person.
	ThirdPartyTraffic string `json:"thirdPartyTraffic" yaml:"third_party_traffic"`
	DataCollected     string `json:"dataCollected" yaml:"data_collected"`
	ISPRisk           string `json:"ispRisk" yaml:"isp_risk"`
	AccountRules      string `json:"accountRules" yaml:"account_rules"`
}

type DockerConfig struct {
	Image string `json:"image" yaml:"image"`
	// Tag is a legacy field a single catalog entry still carries alongside Image. It
	// is parsed so the field round-trips, and deliberately not used: Image already
	// carries the reference Desktop deploys, digest and all.
	Tag string `json:"tag" yaml:"tag"`
	// ImageByArch overrides Image per architecture family (keys: amd64, arm64, arm).
	// It exists for images whose ARM builds Docker cannot select itself because every
	// tag is labelled linux/amd64 in the registry, so the right build is chosen by
	// tag rather than by manifest. Falls back to Image when the host's family is
	// absent.
	ImageByArch map[string]string `json:"imageByArch" yaml:"image_by_arch"`
	// Platforms is what the registry PUBLISHES for this image, nothing more. An ARM
	// host deploying an entry that lacks its family gets a container that dies with
	// "exec format error", so this list is a real check and not a badge.
	Platforms []string `json:"platforms" yaml:"platforms"`
	Env       []EnvVar `json:"env" yaml:"env"`
	Ports     []string `json:"ports" yaml:"ports"`
	Volumes   []string `json:"volumes" yaml:"volumes"`
	// CriticalVolumes marks the mounts whose loss is unrecoverable — node
	// identities, keystores, generated wallets — so deleting one has to be an
	// explicit act rather than a side effect of tidying up.
	CriticalVolumes []CriticalVolume `json:"criticalVolumes" yaml:"critical_volumes"`
	Command         string           `json:"command" yaml:"command"`
	NetworkMode     string           `json:"networkMode" yaml:"network_mode"`
	// CapAdd are the Linux capabilities the container needs on top of a cap_drop ALL
	// baseline. Getting this wrong does not crash the container: Mysterium without
	// SETUID and SETGID registers, looks healthy, and fails every session at setup.
	CapAdd []string `json:"capAdd" yaml:"cap_add"`
	// Devices are host devices to map in ("/dev/net/tun"). A device is a direct line
	// to the kernel, so only devices the runtime allow-lists may be requested, and
	// only by the service whose own entry declares them.
	Devices []string `json:"devices" yaml:"devices"`
	// HealthSignals are log patterns that say whether the service is EARNING, which
	// container health cannot see. For services with no collector it is the only
	// signal there is.
	HealthSignals []HealthSignal `json:"healthSignals" yaml:"health_signals"`
	// AdvertisedAddressEnv names the ONE env var holding the address the network
	// dials this service back at, so it can be compared against the machine's
	// current egress address. Only that variable is ever copied out; the rest may
	// hold credentials.
	AdvertisedAddressEnv string         `json:"advertisedAddressEnv" yaml:"advertised_address_env"`
	Privileged           bool           `json:"privileged" yaml:"privileged"`
	StopTimeout          int            `json:"stopTimeout" yaml:"stop_timeout"`
	Resources            ResourceLimits `json:"resources" yaml:"resources"`
	Setup                string         `json:"setup" yaml:"setup"`
	Notes                string         `json:"notes" yaml:"notes"`
}

// CriticalVolume is one mount holding state that cannot be recovered if it is
// destroyed. Target must match the container-side path in Volumes; Holds is what is
// lost, written to be shown to the operator being asked to confirm.
type CriticalVolume struct {
	Target string `json:"target" yaml:"target"`
	Holds  string `json:"holds" yaml:"holds"`
}

// HealthSignal is one log pattern that reveals whether a service is earning. Pattern
// is a regex matched case-insensitively against container logs, Means is the
// explanation shown to the user, and State is "failing" (the default) or "idle".
type HealthSignal struct {
	Pattern string `json:"pattern" yaml:"pattern"`
	Means   string `json:"means" yaml:"means"`
	State   string `json:"state" yaml:"state"`
}

// ResourceLimits is the optional docker.resources block from a service YAML. Its
// fields map to Docker HostConfig knobs applied at container creation (see
// internal/runtime.applyResourceLimits) so a service's memory ceiling and OOM
// priority survive restarts instead of being set out-of-band. MemLimit and
// MemReservation are Docker-style size strings ("768m", "2g"); an empty string
// leaves that limit unset. OomScoreAdj is a pointer so an absent value is
// distinguishable from an explicit 0 and is applied only when present.
type ResourceLimits struct {
	MemLimit       string `json:"memLimit" yaml:"mem_limit"`
	MemReservation string `json:"memReservation" yaml:"mem_reservation"`
	OomScoreAdj    *int   `json:"oomScoreAdj" yaml:"oom_score_adj"`
	// CPUShares is a relative CPU weight (Docker's API default is 1024) that only
	// arbitrates between containers while the host is contended — it never caps
	// an idle container. A plain int64 is right here, unlike OomScoreAdj's
	// pointer: Docker already reads 0 as "use the default", so the zero value IS
	// absent and needs no separate representation.
	CPUShares int64 `json:"cpuShares" yaml:"cpu_shares"`
}

// NativeConfig is the optional native: block from a service YAML. It declares how a
// service can be run as a supervised native child process (no container): a per
// OS/arch pinned binary download plus a launch-argument template. It is additive and
// parallel to DockerConfig — a service may declare docker:, native:, or both. The
// NativeProcessProvider (internal/runtime) downloads+verifies+extracts the matching
// Binary and launches it with argv built from Command via the same shell-safe
// tokenizeCommand/substitute path Docker uses, reusing the existing EnvVar type for
// Env so the schema stays consistent.
type NativeConfig struct {
	Binaries []NativeBinary `json:"binaries" yaml:"binaries"`
	Command  string         `json:"command" yaml:"command"`
	Env      []EnvVar       `json:"env" yaml:"env"`
}

// NativeBinary is one downloadable, SHA-256-pinned native executable for a specific
// OS/arch. OS matches Go's runtime.GOOS (darwin|linux|windows) and Arch matches
// runtime.GOARCH (amd64|arm64). URL must be HTTPS and SHA256 is the hex digest of the
// downloaded artifact (the archive or the raw binary) — the NativeProcessProvider
// verifies it and refuses to execute anything that fails or lacks verification.
// Archive is how the artifact is packaged: "tar.gz", "zip", or "none" (a raw binary).
// Bin is the path to the executable inside the extracted archive (e.g. "myst" or
// "myst.exe"); for archive "none" it is the on-disk name to give the raw binary.
type NativeBinary struct {
	OS      string `json:"os" yaml:"os"`
	Arch    string `json:"arch" yaml:"arch"`
	URL     string `json:"url" yaml:"url"`
	SHA256  string `json:"sha256" yaml:"sha256"`
	Archive string `json:"archive" yaml:"archive"`
	Bin     string `json:"bin" yaml:"bin"`
}

type EnvVar struct {
	Key         string `json:"key" yaml:"key"`
	Label       string `json:"label" yaml:"label"`
	Required    bool   `json:"required" yaml:"required"`
	Secret      bool   `json:"secret" yaml:"secret"`
	Description string `json:"description" yaml:"description"`
	Default     string `json:"default" yaml:"default"`
}

type Requirements struct {
	ResidentialIP     bool `json:"residentialIp" yaml:"residential_ip"`
	VPSIP             bool `json:"vpsIp" yaml:"vps_ip"`
	DevicesPerAccount int  `json:"devicesPerAccount" yaml:"devices_per_account"`
	// DevicesPerIP is a pointer because omitted and 0 mean different things and the
	// difference is what the user is told. Omitted means nobody has documented a
	// per-IP limit, so a second instance behind one address needs checking against
	// the provider's terms. 0 is a verified statement that there is no limit, which
	// downgrades that warning to "the pair shares one connection, so it earns about
	// what one does". Reading an omitted value as 0 turns a cautious message into a
	// wrong one.
	DevicesPerIP *int   `json:"devicesPerIp" yaml:"devices_per_ip"`
	MinBandwidth string `json:"minBandwidth" yaml:"min_bandwidth"`
	GPU          bool   `json:"gpu" yaml:"gpu"`
	MinStorage   string `json:"minStorage" yaml:"min_storage"`
	// ContainerProhibited is set only when the provider explicitly forbids running
	// its software in containers, VMs or on servers. It is the strongest verdict in
	// the catalog: the stated penalty is a terminated account with the pending
	// balance cancelled, and deploying it as a container IS the violation.
	ContainerProhibited bool   `json:"containerProhibited" yaml:"container_prohibited"`
	Note                string `json:"note" yaml:"note"`
	// NoteColumn says which column Note is a footnote for (vps_ip, devices_per_ip).
	NoteColumn string `json:"noteColumn" yaml:"note_column"`
}

type Payment struct {
	Methods []string `json:"methods" yaml:"methods"`
	// CryptoToken is the specific token when it differs from Currency ("SOL",
	// "USDT").
	CryptoToken   string `json:"cryptoToken" yaml:"crypto_token"`
	MinimumPayout string `json:"minimumPayout" yaml:"minimum_payout"`
	Currency      string `json:"currency" yaml:"currency"`
	Frequency     string `json:"frequency" yaml:"frequency"`
}

type EarningsEstimate struct {
	MonthlyLow  float64 `json:"monthlyLow" yaml:"monthly_low"`
	MonthlyHigh float64 `json:"monthlyHigh" yaml:"monthly_high"`
	Currency    string  `json:"currency" yaml:"currency"`
	Per         string  `json:"per" yaml:"per"`
	Notes       string  `json:"notes" yaml:"notes"`
}

type Cashout struct {
	Method       string  `json:"method" yaml:"method"`
	DashboardURL string  `json:"dashboardUrl" yaml:"dashboard_url"`
	MinAmount    float64 `json:"minAmount" yaml:"min_amount"`
	Currency     string  `json:"currency" yaml:"currency"`
	Notes        string  `json:"notes" yaml:"notes"`
}

type CollectorMetadata struct {
	Type string `json:"type" yaml:"type"`
	// Notes are implementation hints for whoever writes the collector.
	Notes string `json:"notes" yaml:"notes"`
	// CredentialHint is written for the USER, not the developer: the sentence
	// someone reads while looking at a provider dashboard they have never seen
	// before. It allows limited HTML (<a>, <b>) because the UI inserts it as markup
	// deliberately.
	CredentialHint string `json:"credentialHint" yaml:"credential_hint"`
	// PerNodeEarnings says the collector can break earnings down per node rather
	// than reporting only an account total.
	PerNodeEarnings bool `json:"perNodeEarnings" yaml:"per_node_earnings"`
}

// HasNative reports whether the service declares at least one native binary, i.e. it
// can be run as a supervised native child process (by the NativeProcessProvider)
// instead of, or in addition to, a Docker container. It is the native counterpart of
// the "has a Docker image" check that drives ManualOnly and runtime routing.
func (s Service) HasNative() bool {
	return len(s.Native.Binaries) > 0
}

// NativeBinaryFor returns the native binary declared for the given GOOS/GOARCH (as in
// Go's runtime.GOOS/GOARCH), and whether one exists. It is used both by the runtime
// provider (to pick the artifact to download) and by routing (to prefer native only
// when this host actually has a native binary available).
func (s Service) NativeBinaryFor(goos, goarch string) (NativeBinary, bool) {
	for _, b := range s.Native.Binaries {
		if b.OS == goos && b.Arch == goarch {
			return b, true
		}
	}
	return NativeBinary{}, false
}

func Load() (*Catalog, error) {
	root, err := locateServicesDir()
	if err != nil {
		return nil, err
	}
	var services []Service
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yml") || strings.HasPrefix(entry.Name(), "_") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var svc Service
		if err := yaml.Unmarshal(raw, &svc); err != nil {
			return err
		}
		if svc.Slug == "" || svc.Name == "" {
			return nil
		}
		svc.SourcePath = path
		// A service is tracked manually (no local lifecycle) only when it has neither a
		// Docker image nor a native binary. Having a native: block makes it deployable
		// via the NativeProcessProvider even with no image, so native-only services are
		// not manual-only. Docker-backed services (image set) are unaffected.
		svc.ManualOnly = svc.Docker.Image == "" && !svc.HasNative()
		services = append(services, svc)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(services, func(i, j int) bool {
		if services[i].Category == services[j].Category {
			return services[i].Name < services[j].Name
		}
		return services[i].Category < services[j].Category
	})
	bySlug := make(map[string]Service, len(services))
	for _, svc := range services {
		bySlug[svc.Slug] = svc
	}
	return &Catalog{services: services, bySlug: bySlug}, nil
}

func LoadEmbedded(fsys fs.FS) (*Catalog, error) {
	services, err := loadFromFS(fsys, "services")
	if err != nil || len(services) == 0 {
		return Load()
	}
	return newCatalog(services), nil
}

func (c *Catalog) List() []Service {
	out := make([]Service, len(c.services))
	copy(out, c.services)
	return out
}

// retiredStatuses are the lifecycle states that hide a service from the UI. "dropped"
// means evaluated and then removed — a deliberate decision, not a failure — and it is
// hidden for the same reason dead and broken are: offering it invites someone to sign
// up for a programme that will not pay them.
var retiredStatuses = map[string]bool{"dead": true, "broken": true, "dropped": true}

// IsRetired reports whether a status hides the service from the UI.
func IsRetired(status string) bool {
	return retiredStatuses[strings.ToLower(strings.TrimSpace(status))]
}

func (c *Catalog) ListVisible() []Service {
	out := make([]Service, 0, len(c.services))
	for _, svc := range c.services {
		if IsRetired(svc.Status) {
			continue
		}
		out = append(out, svc)
	}
	return out
}

func (c *Catalog) Get(slug string) (Service, bool) {
	svc, ok := c.bySlug[slug]
	return svc, ok
}

// splitImage splits a Docker image reference into repository, tag, and digest.
// A ':' is a tag only when it comes after the last '/'; before it, it is a
// registry port (e.g. localhost:5000/img).
func splitImage(ref string) (repo, tag, digest string) {
	if i := strings.Index(ref, "@"); i >= 0 {
		ref, digest = ref[:i], ref[i+1:]
	}
	repo = ref
	if lastColon := strings.LastIndex(ref, ":"); lastColon > strings.LastIndex(ref, "/") {
		repo, tag = ref[:lastColon], ref[lastColon+1:]
	}
	return repo, tag, digest
}

// HasDigestPin reports whether an image reference pins an immutable digest.
//
// It is deliberately stricter than "contains @sha256:". A reference like
// "repo@sha256:aaaa" contains that substring, is not a digest anything can resolve,
// and would satisfy a substring check while pinning nothing at all — the pin rule
// would report a service as pinned that is still floating. A sha256 digest is
// exactly 64 lowercase hex characters, and the registry API rejects anything else,
// so a reference that fails this check would fail at pull time too.
//
// Lowercase is required rather than folded: the OCI digest grammar defines the hex
// as lowercase, and an uppercase digest is a typed-by-hand pin that will not match
// the registry's own.
//
// One rule, one place: the catalog's pin gate and the catalog-overlay pin checks
// both call this, so tightening it cannot tighten only one of them.
func HasDigestPin(image string) bool {
	_, _, digest := splitImage(strings.TrimSpace(image))
	const prefix = "sha256:"
	hex, ok := strings.CutPrefix(digest, prefix)
	if !ok || len(hex) != 64 {
		return false
	}
	for _, r := range hex {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

// ImageOutdated reports whether a running container's image no longer matches the
// catalog entry it was deployed from. It is true when the provider changed the
// image path (the ProxyBase migration) or the catalog re-pinned to a new digest,
// so the UI can prompt a re-deploy instead of showing a healthy-looking container
// that is silently running a retired image and earning nothing. Deliberately
// conservative: unknown/empty images and a pure tag-vs-digest difference of the
// same repository are NOT flagged.
func ImageOutdated(deployed, catalogImage string) bool {
	if deployed == "" || catalogImage == "" {
		return false
	}
	dRepo, _, dDigest := splitImage(deployed)
	cRepo, _, cDigest := splitImage(catalogImage)
	if dRepo != cRepo {
		return true
	}
	return cDigest != "" && dDigest != "" && cDigest != dDigest
}

func locateServicesDir() (string, error) {
	candidates := []string{
		filepath.Join("services"),
		filepath.Join("..", "CashPilot", "services"),
		filepath.Join("..", "..", "CashPilot", "services"),
	}
	for _, candidate := range candidates {
		if st, err := os.Stat(candidate); err == nil && st.IsDir() {
			return candidate, nil
		}
	}
	return "", errors.New("CashPilot services catalog not found")
}

func loadFromFS(fsys fs.FS, root string) ([]Service, error) {
	var services []Service
	err := fs.WalkDir(fsys, root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yml") || strings.HasPrefix(entry.Name(), "_") {
			return nil
		}
		raw, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		var svc Service
		if err := yaml.Unmarshal(raw, &svc); err != nil {
			return err
		}
		if svc.Slug == "" || svc.Name == "" {
			return nil
		}
		svc.SourcePath = path
		// A service is tracked manually (no local lifecycle) only when it has neither a
		// Docker image nor a native binary. Having a native: block makes it deployable
		// via the NativeProcessProvider even with no image, so native-only services are
		// not manual-only. Docker-backed services (image set) are unaffected.
		svc.ManualOnly = svc.Docker.Image == "" && !svc.HasNative()
		services = append(services, svc)
		return nil
	})
	return services, err
}

func newCatalog(services []Service) *Catalog {
	sort.Slice(services, func(i, j int) bool {
		if services[i].Category == services[j].Category {
			return services[i].Name < services[j].Name
		}
		return services[i].Category < services[j].Category
	})
	bySlug := make(map[string]Service, len(services))
	for _, svc := range services {
		bySlug[svc.Slug] = svc
	}
	return &Catalog{services: services, bySlug: bySlug}
}
