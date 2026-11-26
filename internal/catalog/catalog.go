package catalog

import (
	"errors"
	"fmt"
	"io/fs"
	"log"
	"math"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

type Catalog struct {
	services []Service
	bySlug   map[string]Service

	// custom holds the services the user added from an image reference
	// (internal/services/custom.go), in the shape the rest of the app reads a catalog
	// entry in. They live in the app's database, never in services/, and are set here
	// whenever that table changes so every lookup by slug finds them.
	mu     sync.RWMutex
	custom []Service
}

// CustomPrefix starts every custom service's slug, and no catalog slug may use it:
// an entry with such a slug would take over a user's service and hand it whatever
// capabilities, devices or ports the entry declares.
const CustomPrefix = "custom-"

// IsCustomSlug reports whether slug names a service the user added themselves.
func IsCustomSlug(slug string) bool {
	return strings.HasPrefix(slug, CustomPrefix)
}

// SetCustom replaces the user's custom services. Each must carry the custom- prefix
// and Custom set; anything else is dropped, so a custom definition can never shadow
// or stand in for a catalog entry.
// A mining: block is held to the same rules as a catalog entry's.
func (c *Catalog) SetCustom(services []Service) {
	kept := make([]Service, 0, len(services))
	for _, svc := range services {
		if !IsCustomSlug(svc.Slug) || !svc.Custom {
			continue
		}
		if _, clash := c.bySlug[svc.Slug]; clash {
			continue
		}
		svc.Mining = normalizeMining(svc.Slug, svc.Mining)
		kept = append(kept, svc)
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].Name < kept[j].Name })
	c.mu.Lock()
	c.custom = kept
	c.mu.Unlock()
}

func (c *Catalog) customList() []Service {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]Service(nil), c.custom...)
}

type Service struct {
	Name     string `json:"name" yaml:"name"`
	Slug     string `json:"slug" yaml:"slug"`
	Category string `json:"category" yaml:"category"`
	Status   string `json:"status" yaml:"status"`
	// Priority orders the catalog: entries with one come first, lowest number first,
	// before the usual category-then-name order; 0 means none. The web catalog sets it
	// on the services that pay the project best, so a reorder is a number change there.
	Priority int    `json:"priority" yaml:"priority"`
	Website  string `json:"website" yaml:"website"`
	// SupportURL is the provider's support page, offered when it has disabled the
	// account (a health signal with state account_disabled). Only https:// links are
	// kept; anything else is cleared at load.
	SupportURL       string            `json:"supportUrl" yaml:"support_url"`
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
	// Mining is a miner's own fee (services/_schema.yml `mining:`), which the deploy
	// wizard shows before the miner runs. Nil for anything that is not a miner.
	Mining *Mining `json:"mining" yaml:"mining"`
	// DeviceLink is set for providers that pay a device only once the user adds its
	// id to their account by hand (EarnApp, ProxyRack). Nil means nothing to link.
	DeviceLink *DeviceLink `json:"deviceLink" yaml:"device_link"`
	// ResurrectionChecked is the date ("YYYY-MM-DD") on which a human confirmed a
	// dead entry's programme really is gone even though its site still answers. It is
	// carried so the reason a dead service stays dead travels with the entry instead
	// of living only in the web repository's history.
	ResurrectionChecked string `json:"resurrectionChecked" yaml:"resurrection_checked"`
	// Rating is the web catalog's evidence for a provider's grade (verdicts, cons,
	// pros). It is checked at load and turned into RatingResult (rating.go); the
	// block itself never reaches the frontend.
	Rating Rating `json:"-" yaml:"rating"`
	// Obligations are the web catalog's reminders before a provider writes off money:
	// a claim window, a balance that expires, a dormancy clock (services/_schema.yml
	// `obligations:`). internal/obligations evaluates them into the dashboard's Money
	// waiting panel; the raw rules never reach the frontend, only the reminders due.
	Obligations []Obligation `json:"-" yaml:"obligations"`
	// Guest marks a desktop-only app that may run inside the Secure VM
	// (docs/design/secure-windows-vm.md). It is Desktop-only, appended from
	// catalog-overlay/. Nil means the app is never offered in the VM. A service with
	// requirements.container_prohibited never carries one. Nothing reads it yet.
	Guest *GuestConfig `json:"-" yaml:"guest"`
	// RatingResult is the grade computed from services/_ratings.yml, or "not rated".
	// Nil for a custom service, which nobody has reviewed. Never read from YAML.
	RatingResult *RatingResult `json:"ratingResult" yaml:"-"`
	SourcePath   string        `json:"sourcePath" yaml:"-"`
	ManualOnly   bool          `json:"manualOnly" yaml:"-"`
	// Custom marks a service the user added from an image reference rather than a
	// reviewed catalog entry. Never read from YAML.
	Custom bool `json:"custom" yaml:"-"`
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
	// Account is false when the provider has no account to sign up for (a
	// permissionless node, an identity that is only a wallet). The UI then labels
	// SignupURL "Visit", never "Create account" or "Sign up". nil means an account
	// is needed.
	Account *bool `json:"account" yaml:"account"`
}

// CodeAttributesURL reports whether a referral code is ANCHORED in a signup URL,
// which is a different question from whether the string appears in it. The code
// "grass" is a substring of "https://app.grass.io/register": that is a hostname
// coincidence, not attribution, and a check that accepts it would report a link as
// carrying a code that the provider will never read.
//
// A code counts only where a provider actually reads one, which is the placement the
// catalog schema documents: a query-parameter value (?ref=CODE), a bare query key
// (?CODE, spide's shape), or a whole path segment (/i/CODE, as earnapp, honeygain
// and uprock use). Anything else is a URL that has quietly lost its attribution.
//
// This is the Go half of scripts/referral_check.py in the web repository; the two
// have to agree, because a domain migration that drops the code is caught by
// whichever of them runs first.
func CodeAttributesURL(code, signupURL string) bool {
	code = strings.TrimSpace(code)
	signupURL = strings.TrimSpace(signupURL)
	if code == "" || signupURL == "" {
		return false
	}
	parsed, err := url.Parse(signupURL)
	if err != nil {
		return false
	}
	for key, values := range parsed.Query() {
		for _, value := range values {
			if code == value || (value == "" && code == key) {
				return true
			}
		}
	}
	// The escaped path, so a segment is compared as it is written in the URL rather
	// than as a decoded form nobody sends.
	for _, segment := range strings.Split(parsed.EscapedPath(), "/") {
		if segment == code {
			return true
		}
	}
	return false
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
	// AddressRead says how the web worker reads a manual address back from the
	// running container. Desktop has no payouts page and does not run it; it is
	// declared so the vendored key is read rather than silently dropped.
	AddressRead *AddressRead `json:"addressRead,omitempty" yaml:"address_read"`
	Notes       string       `json:"notes" yaml:"notes"`
}

// Obligation is one reminder from the web catalog. internal/obligations evaluates it
// into the dashboard's "Money waiting" panel, as the web app's app/obligations.py does.
type Obligation struct {
	// ID is kebab-case and unique in its file; the web app keys done state on it.
	ID string `json:"id" yaml:"id"`
	// Action is claim, withdraw or keep-active.
	Action string `json:"action" yaml:"action"`
	Title  string `json:"title" yaml:"title"`
	// Loss says what is lost if the deadline is missed, as a general fact.
	Loss string `json:"loss" yaml:"loss"`
	How  string `json:"how,omitempty" yaml:"how"`
	// URL is where the user acts, on the service's own domain. Empty means the
	// cashout dashboard, then the website.
	URL  string         `json:"url,omitempty" yaml:"url"`
	When ObligationWhen `json:"when" yaml:"when"`
	// WarnDays are the days before the due date to warn, descending. Empty means
	// 30, 7 and 1.
	WarnDays []int `json:"warnDays,omitempty" yaml:"warn_days"`
	// Source is the provider's own terms or docs, and Checked ("YYYY-MM-DD") when
	// someone last read it.
	Source  string `json:"source" yaml:"source"`
	Checked string `json:"checked" yaml:"checked"`
}

// ObligationWhen holds exactly one of three forms: a fixed Date ("YYYY-MM-DD"); a
// monthly window that opens on day On (1-28) of Every "month" and closes DueDays
// later; or a clock that runs Days from Since (over_minimum, last_activity,
// last_increase or last_payout).
type ObligationWhen struct {
	Date    string `json:"date,omitempty" yaml:"date"`
	Every   string `json:"every,omitempty" yaml:"every"`
	On      int    `json:"on,omitempty" yaml:"on"`
	DueDays int    `json:"dueDays,omitempty" yaml:"due_days"`
	Since   string `json:"since,omitempty" yaml:"since"`
	Days    int    `json:"days,omitempty" yaml:"days"`
}

// AddressRead is a read-only command run inside the container and the pattern
// whose first group is the address.
type AddressRead struct {
	Command []string `json:"command" yaml:"command"`
	Pattern string   `json:"pattern" yaml:"pattern"`
}

// DeviceLink is the step after a deploy that makes a device earn for the user: the
// env var holding the device id, where to claim it (${VAR} is filled from the
// deployed env, URL-encoded) and what to do there. The web catalog's loader refuses
// an IDEnv that is secret, so the id is safe to show.
type DeviceLink struct {
	IDEnv string `json:"idEnv" yaml:"id_env"`
	URL   string `json:"url" yaml:"url"`
	Steps string `json:"steps" yaml:"steps"`
	// IDShape is the regex of the device id's shape, and IDShapeUnique says the
	// shape fits this provider's ids only. An unlinked id claims its earnings for
	// whoever adds it first, so the web app masks what matches in every text shown
	// to anyone but the owner: other roles, the read-only feed, pushed alerts.
	// Desktop has no such reader. Its notices and container logs are shown only in
	// the owner's own window, and the fleet API and the pairing heartbeat carry
	// slugs and status, not text, so it reads the fields and has nothing to mask.
	IDShape       string `json:"idShape,omitempty" yaml:"id_shape"`
	IDShapeUnique bool   `json:"idShapeUnique,omitempty" yaml:"id_shape_unique"`
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
	// Entrypoint replaces the image's ENTRYPOINT when set. Storj uses it to give its
	// node a real stop grace: the image's supervisord otherwise kills the node 10
	// seconds after any stop, however long Docker is willing to wait.
	Entrypoint  []string `json:"entrypoint" yaml:"entrypoint"`
	NetworkMode string   `json:"networkMode" yaml:"network_mode"`
	// Runtime is the one container runtime this service works under ("runc"), set on
	// the container whatever the daemon's default is. Mysterium needs it: gVisor has
	// no iptables-nft and no kernel WireGuard, so the node starts there and carries
	// nothing.
	Runtime string `json:"runtime" yaml:"runtime"`
	// UIPort publishes a WebUI port on each address the named env var lists
	// (comma separated), instead of a static ports entry. See UIBindings.
	UIPort *UIPort `json:"uiPort,omitempty" yaml:"ui_port"`
	// UDPPortRange publishes the start:end range the named env var holds, 1:1, at
	// most MaxPorts ports. See ExpandUDPRange.
	UDPPortRange *UDPPortRange `json:"udpPortRange,omitempty" yaml:"udp_port_range"`
	// FenceGated services run only behind the LAN fence (internal/runtime/lanfence.go):
	// a deploy anywhere else is refused, and they have no compose export. Mysterium
	// holds NET_ADMIN in its own network namespace, and the fence is what keeps that
	// namespace off the LAN, this machine and the tailnet.
	FenceGated bool `json:"fenceGated" yaml:"fence_gated"`
	// NodeFirewall says where a node firewalls its own customers off private ranges
	// (the flag in Command carrying the ranges, the nat chain, the blackhole address).
	// The web worker reads it to check the node's isolation; Desktop parses it so the
	// field round-trips and the strict-decode test passes, and does not act on it.
	NodeFirewall NodeFirewall `json:"nodeFirewall" yaml:"node_firewall"`
	// CapAdd are the Linux capabilities the container needs on top of a cap_drop ALL
	// baseline. Getting this wrong does not crash the container: Mysterium without
	// SETUID and SETGID registers, looks healthy, and fails every session at setup.
	CapAdd []string `json:"capAdd" yaml:"cap_add"`
	// Devices are host devices to map in ("/dev/net/tun"). A device is a direct line
	// to the kernel, so only devices the runtime allow-lists may be requested, and
	// only by the service whose own entry declares them.
	Devices []string `json:"devices" yaml:"devices"`
	// GPU is the GPU the deploy passes in (gpu.go). Read from this computer's own
	// catalog, never from a deploy request. Not sent to the frontend.
	GPU *GPU `json:"-" yaml:"gpu"`
	// HealthSignals are log patterns that say whether the service is EARNING, which
	// container health cannot see. For services with no collector it is the only
	// signal there is.
	HealthSignals []HealthSignal `json:"healthSignals" yaml:"health_signals"`
	// HealthProbe is the web catalog's docker.health_probe block: a read-only command
	// the web worker runs inside a running node, with regex checks on its answer
	// (Mysterium's monitoring status and recent sessions). Neither schema file
	// describes it yet. Desktop parses it so the field round-trips and the
	// strict-decode test passes, and does not act on it. The loader makes it
	// JSON-safe (see jsonSafe), since the service list crosses the Wails bridge.
	HealthProbe map[string]any `json:"healthProbe,omitempty" yaml:"health_probe"`
	// AdvertisedAddressEnv names the ONE env var holding the address the network
	// dials this service back at, so it can be compared against the machine's
	// current egress address. Only that variable is ever copied out; the rest may
	// hold credentials.
	// TrafficStall is the web catalog's docker.traffic_stall block: alert when one
	// running copy sends almost nothing while the service's other copies are busy.
	// internal/stall judges it and stall_alerts.go raises the alert, as the web app's
	// app/traffic_stall.py does.
	TrafficStall         *TrafficStall  `json:"trafficStall,omitempty" yaml:"traffic_stall"`
	AdvertisedAddressEnv string         `json:"advertisedAddressEnv" yaml:"advertised_address_env"`
	Privileged           bool           `json:"privileged" yaml:"privileged"`
	StopTimeout          int            `json:"stopTimeout" yaml:"stop_timeout"`
	Resources            ResourceLimits `json:"resources" yaml:"resources"`
	Setup                string         `json:"setup" yaml:"setup"`
	Notes                string         `json:"notes" yaml:"notes"`
}

// TrafficStall is the optional docker.traffic_stall block (see services/_schema.yml).
// Share is the fraction of the other copies' median a copy must send in an hour,
// PeerMinMBPerHour the median below which the hour does not count, and Hours how many
// whole low hours in a row raise the alert.
type TrafficStall struct {
	Share            float64 `json:"share,omitempty" yaml:"share"`
	PeerMinMBPerHour float64 `json:"peerMinMbPerHour,omitempty" yaml:"peer_min_mb_per_hour"`
	Hours            int     `json:"hours,omitempty" yaml:"hours"`
}

// NodeFirewall is the optional docker.node_firewall block (see services/_schema.yml).
type NodeFirewall struct {
	Flag      string `json:"flag" yaml:"flag"`
	Chain     string `json:"chain" yaml:"chain"`
	Blackhole string `json:"blackhole" yaml:"blackhole"`
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
// explanation shown to the user, and State is "failing" (the default), "idle" or
// "account_disabled": the provider refuses to run the client, so restarting cannot
// help. The loader lowercases State and drops a signal whose state is none of these
// (see normalizeSignals), as the web catalog rejects it.
type HealthSignal struct {
	Pattern string `json:"pattern" yaml:"pattern"`
	Means   string `json:"means" yaml:"means"`
	State   string `json:"state" yaml:"state"`
	// Unless cancels the signal when it also appears in the same logs: the problem
	// is not there. WindowHours, when set, judges that many hours of logs instead
	// of the tail (see health.windowed); Mysterium writes about 11,000 lines an
	// hour, so the tail is a minute and cannot show that something never happened.
	Unless      string  `json:"unless,omitempty" yaml:"unless"`
	WindowHours float64 `json:"windowHours,omitempty" yaml:"window_hours"`
	// MinMatches is how many lines the pattern must match in the window (default 1).
	MinMatches int `json:"minMatches,omitempty" yaml:"min_matches"`
	// Restart, on a failing signal with WindowHours, says a restart clears the state:
	// while the signal holds, Desktop restarts the service itself, at most once every
	// 6 hours (see health.windowed). The loader drops a signal that sets it anywhere
	// else, as the web catalog rejects it.
	Restart bool `json:"restart,omitempty" yaml:"restart"`
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
//
// Args and FirewallBlock exist because the same program can need different handling
// per OS. Args is tokenized like Command and placed BEFORE it, so a global flag that
// only one OS needs still precedes the subcommand. FirewallBlock is Windows-only: the
// provider keeps an outbound Windows Firewall rule that blocks this binary from these
// networks for as long as the service is deployed, and refuses to start the binary
// when the rule cannot be put in place.
type NativeBinary struct {
	OS            string   `json:"os" yaml:"os"`
	Arch          string   `json:"arch" yaml:"arch"`
	URL           string   `json:"url" yaml:"url"`
	SHA256        string   `json:"sha256" yaml:"sha256"`
	Archive       string   `json:"archive" yaml:"archive"`
	Bin           string   `json:"bin" yaml:"bin"`
	Args          string   `json:"args,omitempty" yaml:"args"`
	FirewallBlock []string `json:"firewallBlock,omitempty" yaml:"firewall_block"`
}

// GuestConfig is the guest: block: how an app runs inside the Secure VM, per guest OS.
type GuestConfig struct {
	Windows *GuestWindows `json:"windows,omitempty" yaml:"windows"`
}

// GuestWindows describes one app inside the Windows guest. Process is the executable
// name the guest read matches. Publisher is the expected Authenticode subject of the
// official installer, in the form Windows prints it. DownloadPage is the vendor's
// official HTTPS page the user installs from. Signin says how the user signs in:
// "console" (by hand in the VM window) or "none".
type GuestWindows struct {
	Process      string `json:"process" yaml:"process"`
	Publisher    string `json:"publisher" yaml:"publisher"`
	DownloadPage string `json:"downloadPage" yaml:"download_page"`
	Signin       string `json:"signin" yaml:"signin"`
}

type EnvVar struct {
	Key         string `json:"key" yaml:"key"`
	Label       string `json:"label" yaml:"label"`
	Required    bool   `json:"required" yaml:"required"`
	Secret      bool   `json:"secret" yaml:"secret"`
	Description string `json:"description" yaml:"description"`
	Default     string `json:"default" yaml:"default"`
	// Pattern, when set, is a regular expression the WHOLE value must match. It
	// exists for values that land on a command line, where "127.0.0.1, 10.0.0.1"
	// becomes a stray argument and the service refuses to start.
	Pattern string `json:"pattern,omitempty" yaml:"pattern"`
	// PortRange, when set, means the value is "start:end" with
	// 1 <= start <= end <= 65535. A pattern checks the shape but not the order, and
	// Mysterium refuses to start on a reversed --udp.ports.
	PortRange bool `json:"portRange,omitempty" yaml:"port_range"`
	// PerDevice means the value is the id the provider knows this device by
	// (ProxyRack's UUID). A copy, proxied or on the box, gets its own value derived
	// from it, or is refused when none can be derived.
	PerDevice bool `json:"perDevice,omitempty" yaml:"per_device"`
	// Identifier means the value names this device to the provider (a device name
	// or id) without being a secret. The web app keeps these values, and the
	// PerDevice ones, after a service is removed so the owner can see and reuse
	// them. Desktop keeps the values it deployed with in its stored credentials,
	// which a removal does not delete, so it reads the flag and has nothing else
	// to do with it. One difference stays on purpose: a redeploy with a new value
	// replaces the stored one (SaveCredentials), where the web app keeps the old
	// value as retired. The user typed the new id on the one machine Desktop runs.
	Identifier bool `json:"identifier,omitempty" yaml:"identifier"`
	// SeedFile, when set, means the value is a wallet seed phrase that lives in a
	// file in the service's own volume instead (seedfile.go): Desktop never stores
	// it, never asks for it as a field and never passes it as a variable.
	SeedFile *SeedFile `json:"seedFile,omitempty" yaml:"seed_file"`
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
	ContainerProhibited bool `json:"containerProhibited" yaml:"container_prohibited"`
	// ProxyProhibited is set only when the provider's own terms forbid running its
	// software through a proxy or VPN; a proxied copy of the service is then refused
	// (internal/proxies.Refusal). nil means nobody has documented it, never that it
	// is allowed.
	ProxyProhibited *ProxyProhibited `json:"proxyProhibited,omitempty" yaml:"proxy_prohibited"`
	Note            string           `json:"note" yaml:"note"`
	// NoteColumn says which column Note is a footnote for (vps_ip, devices_per_ip).
	NoteColumn string `json:"noteColumn" yaml:"note_column"`
	// HostingNote is the web app's advice for a worker that reports a hosting
	// connection. It is read so the vendored entry loads, and never shown: Desktop
	// cannot see what kind of connection this computer is on (internal/preflight),
	// so it never reaches the frontend.
	HostingNote string `json:"-" yaml:"hosting_note"`
}

// Mining is the `mining:` block of a miner (services/_schema.yml).
type Mining struct {
	// DevFeePercent is the share of hashing time the miner gives its developers.
	// Nil means the catalog does not state a usable fee, which is never 0%.
	DevFeePercent *float64 `json:"devFeePercent" yaml:"dev_fee_percent"`
	// DevFeeSource is the https:// link that states the fee; anything else is
	// cleared at load.
	DevFeeSource string `json:"devFeeSource" yaml:"dev_fee_source"`
	// PoolFeeNote explains the default pool's fees and payout threshold.
	PoolFeeNote string `json:"poolFeeNote" yaml:"pool_fee_note"`
}

// ProxyProhibited is requirements.proxy_prohibited: the provider's own terms that
// forbid running its software through a proxy or VPN.
type ProxyProhibited struct {
	Source string `json:"source" yaml:"source"`
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
	// Accrual is "pool" for an unpaid mining balance still held by the pool.
	Accrual string `json:"accrual,omitempty" yaml:"accrual"`
	Type    string `json:"type" yaml:"type"`
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
	// QuietDays is how many days the balance can stand still while the service
	// works normally (a vetting period, batched settlements). The web app's
	// "gone quiet" heads-up waits at least this long. Desktop has no such
	// heads-up yet; the field is read so the value is there when it does.
	QuietDays int `json:"quietDays" yaml:"quiet_days"`
	// NodeRead says how to read one node's own earnings from inside its running
	// container, for a provider whose account API gives no balance or lifetime
	// (Mysterium: the cloud's earningsTotal is a trailing 30-day sum). Nil means
	// the service does not declare one.
	NodeRead *NodeRead `json:"nodeRead,omitempty" yaml:"node_read"`
}

// NodeRead is collector.node_read (services/_schema.yml): Command runs inside the
// node's container, read-only, and each regex's group 1 is taken from its output.
// SettledNet and SettledFees come both or neither; every match of each is summed.
type NodeRead struct {
	Command     []string `json:"command" yaml:"command"`
	Node        string   `json:"node" yaml:"node"`
	Balance     string   `json:"balance" yaml:"balance"`
	Lifetime    string   `json:"lifetime" yaml:"lifetime"`
	SettledNet  string   `json:"settledNet,omitempty" yaml:"settled_net"`
	SettledFees string   `json:"settledFees,omitempty" yaml:"settled_fees"`
}

// Health signal states the catalog allows. An absent state means failing.
const (
	SignalFailing         = "failing"
	SignalIdle            = "idle"
	SignalAccountDisabled = "account_disabled"
)

// normalize applies the web catalog's validation to the fields Desktop acts on,
// without refusing the whole entry: a user's app must still start when one entry is
// wrong. The vendored catalog is held to the strict rules by catalog tests instead.
func normalize(svc *Service) {
	// A pool tracked by its payout address can be set up without a provider login.
	if svc.Collector.Accrual == "pool" && svc.Payout.AddressSource == "collector" && svc.Referral.Account == nil {
		account := false
		svc.Referral.Account = &account
	}
	if !strings.HasPrefix(svc.SupportURL, "https://") {
		svc.SupportURL = ""
	}
	svc.Mining = normalizeMining(svc.Slug, svc.Mining)
	svc.Docker.HealthSignals = normalizeSignals(svc.Slug, svc.Docker.HealthSignals)
	if svc.Docker.HealthProbe != nil {
		svc.Docker.HealthProbe = jsonSafe(svc.Docker.HealthProbe).(map[string]any)
	}
}

// normalizeMining returns a copy of m held to the web catalog's rules, nil for nil.
// Catalog entries and custom services both pass through it.
func normalizeMining(slug string, m *Mining) *Mining {
	if m == nil {
		return nil
	}
	out := *m
	// The web catalog allows a fee from 0 to 10 percent. Anything else, NaN
	// included, is shown as not stated rather than as a number nobody checked.
	if fee := out.DevFeePercent; fee != nil && !(*fee >= 0 && *fee <= 10) {
		log.Printf("catalog: %s: mining.dev_fee_percent %v is not from 0 to 10, shown as not stated", slug, *fee)
		out.DevFeePercent = nil
	}
	if !strings.HasPrefix(out.DevFeeSource, "https://") {
		out.DevFeeSource = ""
	}
	return &out
}

// jsonSafe turns the map[any]any that yaml.v3 produces for a nested mapping with a
// non-string key (an unquoted number, say) into map[string]any, and a .nan, .inf or
// -.inf into its string form ("NaN", "+Inf", "-Inf"). encoding/json refuses both,
// and one such value would fail the whole service list on its way to the UI.
func jsonSafe(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, e := range t {
			t[k] = jsonSafe(e)
		}
		return t
	case map[any]any:
		m := make(map[string]any, len(t))
		for k, e := range t {
			m[fmt.Sprint(k)] = jsonSafe(e)
		}
		return m
	case []any:
		for i, e := range t {
			t[i] = jsonSafe(e)
		}
		return t
	case float64:
		if math.IsNaN(t) || math.IsInf(t, 0) {
			return fmt.Sprint(t)
		}
	}
	return v
}

// normalizeSignals lowercases each state and drops the signals the web catalog
// rejects: an unknown state (read as failing, it would accuse a service of a problem
// nobody declared), and an account_disabled one with no usable pattern or with
// window_hours (it is judged on a stopped container's last run, never a window), and
// one with restart: true that is not a failing signal with window_hours.
func normalizeSignals(slug string, signals []HealthSignal) []HealthSignal {
	if len(signals) == 0 {
		return signals
	}
	kept := make([]HealthSignal, 0, len(signals))
	for _, s := range signals {
		s.State = strings.ToLower(strings.TrimSpace(s.State))
		if !ValidSignal(s) {
			log.Printf("catalog: %s: dropped the health signal %q with state %q, which the web catalog rejects", slug, s.Pattern, s.State)
			continue
		}
		kept = append(kept, s)
	}
	return kept
}

// ValidSignal reports whether the web catalog would accept this health signal, with
// State already lowercased.
func ValidSignal(s HealthSignal) bool {
	// A restart is for a running copy stuck in a state a restart clears, which only
	// a windowed failing signal can show.
	if s.Restart && (s.WindowHours <= 0 || (s.State != "" && s.State != SignalFailing)) {
		return false
	}
	switch s.State {
	case "", SignalFailing, SignalIdle:
		return true
	case SignalAccountDisabled:
		if strings.TrimSpace(s.Pattern) == "" || s.WindowHours != 0 {
			return false
		}
		_, err := regexp.Compile("(?i)" + s.Pattern)
		return err == nil
	}
	return false
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
		if IsCustomSlug(svc.Slug) {
			log.Printf("catalog: %s: skipped, slug %q uses the reserved %q prefix", path, svc.Slug, CustomPrefix)
			return nil
		}
		svc.SourcePath = path
		normalize(&svc)
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
	rules, rulesErr := os.ReadFile(filepath.Join(root, RatingRulesFile))
	applyRatings(services, rules, rulesErr)
	return newCatalog(services), nil
}

func LoadEmbedded(fsys fs.FS) (*Catalog, error) {
	services, err := loadFromFS(fsys, "services")
	if err != nil || len(services) == 0 {
		return Load()
	}
	return newCatalog(services), nil
}

// List is every catalog entry, then the user's custom services.
func (c *Catalog) List() []Service {
	out := make([]Service, len(c.services))
	copy(out, c.services)
	return append(out, c.customList()...)
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
	return append(out, c.customList()...)
}

func (c *Catalog) Get(slug string) (Service, bool) {
	if svc, ok := c.bySlug[slug]; ok {
		return svc, true
	}
	if !IsCustomSlug(slug) {
		return Service{}, false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, svc := range c.custom {
		if svc.Slug == slug {
			return svc, true
		}
	}
	return Service{}, false
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
		if IsCustomSlug(svc.Slug) {
			log.Printf("catalog: %s: skipped, slug %q uses the reserved %q prefix", path, svc.Slug, CustomPrefix)
			return nil
		}
		svc.SourcePath = path
		normalize(&svc)
		// A service is tracked manually (no local lifecycle) only when it has neither a
		// Docker image nor a native binary. Having a native: block makes it deployable
		// via the NativeProcessProvider even with no image, so native-only services are
		// not manual-only. Docker-backed services (image set) are unaffected.
		svc.ManualOnly = svc.Docker.Image == "" && !svc.HasNative()
		services = append(services, svc)
		return nil
	})
	if err == nil {
		rules, rulesErr := fs.ReadFile(fsys, path.Join(root, RatingRulesFile))
		applyRatings(services, rules, rulesErr)
	}
	return services, err
}

func newCatalog(services []Service) *Catalog {
	sort.SliceStable(services, func(i, j int) bool { return catalogLess(services[i], services[j]) })
	bySlug := make(map[string]Service, len(services))
	for _, svc := range services {
		bySlug[svc.Slug] = svc
	}
	return &Catalog{services: services, bySlug: bySlug}
}

// catalogLess is the one catalog order: prioritised entries first (lowest number first),
// then category, then name. Kept in one place so the on-disk loader and newCatalog cannot
// drift apart.
func catalogLess(a, b Service) bool {
	ap, bp := a.Priority > 0, b.Priority > 0
	if ap != bp {
		return ap
	}
	if ap && a.Priority != b.Priority {
		return a.Priority < b.Priority
	}
	if a.Category != b.Category {
		return a.Category < b.Category
	}
	return a.Name < b.Name
}
