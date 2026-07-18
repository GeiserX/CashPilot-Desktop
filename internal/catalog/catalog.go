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
	SourcePath       string            `json:"sourcePath" yaml:"-"`
	ManualOnly       bool              `json:"manualOnly" yaml:"-"`
}

type Referral struct {
	SignupURL string `json:"signupUrl" yaml:"signup_url"`
}

type DockerConfig struct {
	Image       string         `json:"image" yaml:"image"`
	Platforms   []string       `json:"platforms" yaml:"platforms"`
	Env         []EnvVar       `json:"env" yaml:"env"`
	Ports       []string       `json:"ports" yaml:"ports"`
	Volumes     []string       `json:"volumes" yaml:"volumes"`
	Command     string         `json:"command" yaml:"command"`
	NetworkMode string         `json:"networkMode" yaml:"network_mode"`
	CapAdd      []string       `json:"capAdd" yaml:"cap_add"`
	Privileged  bool           `json:"privileged" yaml:"privileged"`
	StopTimeout int            `json:"stopTimeout" yaml:"stop_timeout"`
	Resources   ResourceLimits `json:"resources" yaml:"resources"`
	Setup       string         `json:"setup" yaml:"setup"`
	Notes       string         `json:"notes" yaml:"notes"`
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
	ResidentialIP     bool   `json:"residentialIp" yaml:"residential_ip"`
	VPSIP             bool   `json:"vpsIp" yaml:"vps_ip"`
	DevicesPerAccount int    `json:"devicesPerAccount" yaml:"devices_per_account"`
	DevicesPerIP      int    `json:"devicesPerIp" yaml:"devices_per_ip"`
	MinBandwidth      string `json:"minBandwidth" yaml:"min_bandwidth"`
	GPU               bool   `json:"gpu" yaml:"gpu"`
	MinStorage        string `json:"minStorage" yaml:"min_storage"`
	Note              string `json:"note" yaml:"note"`
}

type Payment struct {
	Methods       []string `json:"methods" yaml:"methods"`
	MinimumPayout string   `json:"minimumPayout" yaml:"minimum_payout"`
	Currency      string   `json:"currency" yaml:"currency"`
	Frequency     string   `json:"frequency" yaml:"frequency"`
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
	Type  string `json:"type" yaml:"type"`
	Notes string `json:"notes" yaml:"notes"`
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

func (c *Catalog) ListVisible() []Service {
	out := make([]Service, 0, len(c.services))
	for _, svc := range c.services {
		if svc.Status == "dead" || svc.Status == "broken" {
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
