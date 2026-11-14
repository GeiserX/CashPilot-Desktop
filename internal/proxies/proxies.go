// Package proxies holds the rules for proxied copies that need no Docker and no
// database: what a proxy definition may contain, what the UI may see of it (never
// a secret), which services may run through a proxy, the traffic cap, and the
// exit-IP checks. It mirrors the web app's app/proxies.py (GeiserX/CashPilot
// docs/proxies.md); internal/runtime/proxied.go does the Docker side.
package proxies

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// Kinds are the proxy types a tunnel can carry.
var Kinds = []string{"socks5", "http", "tailscale"}

// SecretFields are the fields that are never shown back.
var SecretFields = []string{"username", "password", "auth_key"}

// DefaultThresholdPct is where a cap stops the copies by default. Usage is read once
// a minute, so the copies can run up to a minute past it.
const DefaultThresholdPct = 90

// GB is the decimal gigabyte providers sell.
const GB = 1_000_000_000

// ForbiddenCaps are the capabilities a proxied earner may never hold. It shares its
// tunnel's network namespace, so NET_ADMIN could flush the tunnel's firewall or put
// back a default route, NET_RAW could send frames out of the real interface past the
// output hook, and SYS_ADMIN covers both.
var ForbiddenCaps = map[string]bool{"NET_ADMIN": true, "NET_RAW": true, "SYS_ADMIN": true, "ALL": true}

var (
	hostRE     = regexp.MustCompile(`^[A-Za-z0-9.-]{1,253}$`)
	exitNodeRE = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,253}$`)
	// https://host[:port][/path]: no user name or password (the view shows this
	// field back), no query and no fragment.
	loginServerRE = regexp.MustCompile(`^https://[A-Za-z0-9.-]{1,253}(:[0-9]{1,5})?(/[A-Za-z0-9._~/-]*)?$`)
)

// Definition is a proxy as stored (encrypted) in the store.
type Definition struct {
	Name            string   `json:"name"`
	Kind            string   `json:"kind"`
	Host            string   `json:"host"`
	Port            int      `json:"port"`
	Username        string   `json:"username"`
	Password        string   `json:"password"`
	AuthKey         string   `json:"auth_key"`
	ExitNode        string   `json:"exit_node"`
	LoginServer     string   `json:"login_server"`
	Rotating        bool     `json:"rotating"`
	CapGB           *float64 `json:"cap_gb"`
	CapThresholdPct int      `json:"cap_threshold_pct"`
	ResetDay        *int     `json:"reset_day"`
}

// Counter is the last reading of one tunnel's interface counters.
type Counter struct {
	Rx        uint64 `json:"rx"`
	Tx        uint64 `json:"tx"`
	SeenAt    int64  `json:"seen_at"`
	StartedAt string `json:"started_at"`
}

// TestResult is the outcome of the last Test, kept so a failure outlives its toast.
type TestResult struct {
	OK    bool   `json:"ok"`
	At    string `json:"at"`
	Error string `json:"error"`
}

// State is what changes while a proxy is used. It holds no secret.
type State struct {
	UsedBytes     uint64             `json:"used_bytes"`
	PeriodStart   string             `json:"period_start,omitempty"`
	CappedAt      string             `json:"capped_at,omitempty"`
	CapReason     string             `json:"cap_reason,omitempty"`
	LastExitIP    string             `json:"last_exit_ip,omitempty"`
	LastCountry   string             `json:"last_country,omitempty"`
	LastCheckedAt string             `json:"last_checked_at,omitempty"`
	LastTest      *TestResult        `json:"last_test,omitempty"`
	Counters      map[string]Counter `json:"counters,omitempty"`
}

// InputError is a definition the owner has to fix; the message names the field.
type InputError struct{ msg string }

func (e *InputError) Error() string { return e.msg }

func inputErr(format string, args ...any) error { return &InputError{fmt.Sprintf(format, args...)} }

// IsInputError reports whether err is a definition the owner has to fix.
func IsInputError(err error) bool {
	var ie *InputError
	return errors.As(err, &ie)
}

// Clean validates what the form sent and returns the definition to store.
//
// On an edit (existing non-nil) a secret left blank keeps the stored value, because
// the form never shows secrets back; "clear_<field>" set to "true" removes it. Any
// other field left out keeps its stored value too. A stored secret is kept only
// while the proxy still points at the same place (host and port, or the Tailscale
// login server): when that changes the owner must type it again or clear it, so an
// edit never sends a stored credential to a server it was not entered for.
func Clean(values map[string]string, existing *Definition) (Definition, error) {
	var base Definition
	if existing != nil {
		base = *existing
	}
	get := func(key, fallback string) string {
		if v, ok := values[key]; ok {
			return strings.TrimSpace(v)
		}
		return fallback
	}
	out := Definition{}
	out.Name = truncate(get("name", base.Name), 80)
	if out.Name == "" {
		return Definition{}, inputErr("Give the proxy a name")
	}
	out.Kind = strings.ToLower(get("kind", base.Kind))
	if !contains(Kinds, out.Kind) {
		return Definition{}, inputErr("The type must be socks5, http or tailscale")
	}
	kept := map[string]bool{}
	secret := func(field, stored string) string {
		if isTrue(values["clear_"+field]) {
			return ""
		}
		if v, ok := values[field]; ok && v != "" {
			return v
		}
		kept[field] = stored != ""
		return stored
	}
	out.Username = secret("username", base.Username)
	out.Password = secret("password", base.Password)
	out.AuthKey = secret("auth_key", base.AuthKey)

	if out.Kind == "tailscale" {
		out.ExitNode = get("exit_node", base.ExitNode)
		out.LoginServer = get("login_server", base.LoginServer)
		if out.AuthKey == "" {
			return Definition{}, inputErr("A Tailscale proxy needs an auth key")
		}
		if !exitNodeRE.MatchString(out.ExitNode) {
			return Definition{}, inputErr("Name the exit node by its Tailscale name or IP")
		}
		if out.LoginServer != "" && !loginServerRE.MatchString(out.LoginServer) {
			return Definition{}, inputErr("The login server must be an https:// URL with no user name, password, query or fragment")
		}
		out.Username, out.Password = "", ""
	} else {
		out.Host = get("host", base.Host)
		port, err := strconv.Atoi(get("port", strconv.Itoa(base.Port)))
		if err != nil {
			port = 0
		}
		out.Port = port
		if !hostRE.MatchString(out.Host) {
			return Definition{}, inputErr("The host must be a host name or an IPv4 address")
		}
		if out.Port < 1 || out.Port > 65535 {
			return Definition{}, inputErr("The port must be between 1 and 65535")
		}
		out.AuthKey = ""
	}
	if existing != nil {
		if err := refuseRetargetedSecret(*existing, out, kept); err != nil {
			return Definition{}, err
		}
	}

	out.Rotating = base.Rotating
	if v, ok := values["rotating"]; ok {
		out.Rotating = isTrue(v)
	}

	capText := get("cap_gb", formatFloat(base.CapGB))
	if capText != "" && capText != "0" {
		gb, err := strconv.ParseFloat(capText, 64)
		if err != nil {
			return Definition{}, inputErr("The traffic cap is a number of GB, or empty for none")
		}
		// Below a megabyte the threshold rounds to nothing measurable and the
		// first tick would stop every copy.
		if gb < MinCapGB || gb > 1_000_000 {
			return Definition{}, inputErr("The traffic cap must be between 0.001 and 1,000,000 GB")
		}
		out.CapGB = &gb
	}
	pctText := get("cap_threshold_pct", strconv.Itoa(base.CapThresholdPct))
	pct := DefaultThresholdPct
	if pctText != "" && pctText != "0" {
		v, err := strconv.Atoi(pctText)
		if err != nil {
			return Definition{}, inputErr("The stop threshold is a percentage")
		}
		pct = v
	}
	if pct < 10 || pct > 100 {
		return Definition{}, inputErr("The stop threshold must be between 10 and 100 percent")
	}
	out.CapThresholdPct = pct
	resetText := get("reset_day", formatInt(base.ResetDay))
	if resetText != "" && resetText != "0" {
		day, err := strconv.Atoi(resetText)
		if err != nil {
			return Definition{}, inputErr("The reset day is a day of the month")
		}
		if day < 1 || day > 28 {
			return Definition{}, inputErr("The reset day must be between 1 and 28, so every month has it")
		}
		out.ResetDay = &day
	}
	return out, nil
}

// target is where a proxy's secrets are sent: the SOCKS/HTTP server, or the
// Tailscale control server. The exit node is not part of it: the auth key goes to
// the control server whatever exit node is picked. socks5 and http on the same host
// and port are the same server.
func target(d Definition) string {
	if d.Kind == "tailscale" {
		return "tailscale|" + d.LoginServer
	}
	return "net|" + strings.ToLower(d.Host) + "|" + strconv.Itoa(d.Port)
}

// refuseRetargetedSecret refuses an edit that would carry a stored secret to a new
// server.
func refuseRetargetedSecret(existing, out Definition, kept map[string]bool) error {
	// Only the fields the new kind uses: a kind change already wiped the rest.
	var carried []string
	if kept["username"] && out.Username != "" {
		carried = append(carried, "user")
	}
	if kept["password"] && out.Password != "" {
		carried = append(carried, "password")
	}
	if kept["auth_key"] && out.AuthKey != "" {
		carried = append(carried, "auth key")
	}
	if len(carried) == 0 || target(existing) == target(out) {
		return nil
	}
	where, clear := "host or port", ", or tick Remove to save without it"
	if out.Kind == "tailscale" {
		where, clear = "login server", ""
	}
	return inputErr("The %s changed, so type the %s again%s. CashPilot never sends a stored secret to a server it was not entered for.",
		where, strings.Join(carried, " and "), clear)
}

// SecretValues are the secret values of a definition, for redacting messages.
func (d Definition) SecretValues() []string {
	var out []string
	for _, v := range []string{d.Password, d.AuthKey, d.Username} {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// CapView is the traffic cap as the UI shows it.
type CapView struct {
	Enabled      bool   `json:"enabled"`
	CapBytes     uint64 `json:"capBytes"`
	ThresholdPct int    `json:"thresholdPct"`
	ResetDay     int    `json:"resetDay"`
	UsedBytes    uint64 `json:"usedBytes"`
	PeriodStart  string `json:"periodStart"`
	CappedAt     string `json:"cappedAt"`
	CapReason    string `json:"capReason"`
}

// CopyView is one proxied copy of a service as the UI shows it.
type CopyView struct {
	Slug         string `json:"slug"`
	Name         string `json:"name"`
	Status       string `json:"status"`
	TunnelStatus string `json:"tunnelStatus"`
	ExitIP       string `json:"exitIp"`
	ExitCountry  string `json:"exitCountry"`
	StoppedByCap bool   `json:"stoppedByCap"`
}

// View is what the UI receives for a proxy. Secrets appear only as "set".
type View struct {
	ID            int64           `json:"id"`
	Name          string          `json:"name"`
	Kind          string          `json:"kind"`
	Host          string          `json:"host"`
	Port          int             `json:"port"`
	ExitNode      string          `json:"exitNode"`
	LoginServer   string          `json:"loginServer"`
	Rotating      bool            `json:"rotating"`
	Secrets       map[string]bool `json:"secrets"`
	Cap           CapView         `json:"cap"`
	LastExitIP    string          `json:"lastExitIp"`
	LastCountry   string          `json:"lastCountry"`
	LastCheckedAt string          `json:"lastCheckedAt"`
	LastTest      *TestResult     `json:"lastTest"`
	Copies        []CopyView      `json:"copies"`
}

// PublicView is a proxy without its secrets.
func PublicView(id int64, d Definition, s State, copies []CopyView) View {
	if copies == nil {
		copies = []CopyView{}
	}
	return View{
		ID:          id,
		Name:        d.Name,
		Kind:        d.Kind,
		Host:        d.Host,
		Port:        d.Port,
		ExitNode:    d.ExitNode,
		LoginServer: d.LoginServer,
		Rotating:    d.Rotating,
		Secrets: map[string]bool{
			"username": d.Username != "",
			"password": d.Password != "",
			"auth_key": d.AuthKey != "",
		},
		Cap:           capView(d, s),
		LastExitIP:    s.LastExitIP,
		LastCountry:   s.LastCountry,
		LastCheckedAt: s.LastCheckedAt,
		LastTest:      s.LastTest,
		Copies:        copies,
	}
}

func capView(d Definition, s State) CapView {
	v := CapView{
		ThresholdPct: thresholdPct(d),
		UsedBytes:    s.UsedBytes,
		PeriodStart:  s.PeriodStart,
		CappedAt:     s.CappedAt,
		CapReason:    s.CapReason,
	}
	if c, ok := CapBytes(d); ok {
		v.Enabled, v.CapBytes = true, c
	}
	if d.ResetDay != nil {
		v.ResetDay = *d.ResetDay
	}
	return v
}

// Refusal says why svc cannot run through a proxy, or "".
//
// A proxied copy lives inside its tunnel's network namespace. Host networking,
// published ports and devices need the host's own network or hardware, and a volume
// would be shared with the copy on this machine.
func Refusal(svc catalog.Service) string {
	name := svc.Name
	if name == "" {
		name = svc.Slug
	}
	if name == "" {
		name = "This service"
	}
	dc := svc.Docker
	if dc.NetworkMode == "host" {
		return name + " uses the host's network, so it cannot be moved behind a proxy."
	}
	if strings.HasPrefix(dc.NetworkMode, "container:") {
		return name + " joins another container's network, so it cannot be moved behind a proxy."
	}
	if dc.FenceGated {
		return name + " runs only behind the LAN fence, so it cannot run through a proxy."
	}
	if dc.PublishesPorts() {
		return name + " publishes a port on the host, which a proxied copy cannot do."
	}
	if len(dc.Volumes) > 0 {
		return name + " keeps data in a volume or host directory, which a proxied copy would share."
	}
	if len(dc.Devices) > 0 {
		return name + " needs a device on the host, which a proxied copy does not get."
	}
	if dc.Privileged {
		return name + " asks for a privileged container, which could change or bypass the tunnel's firewall, so it cannot run behind a proxy."
	}
	if risky := RiskyCaps(dc.CapAdd); len(risky) > 0 {
		return fmt.Sprintf("%s asks for %s, which would let it change or bypass the tunnel's firewall, so it cannot run behind a proxy.", name, strings.Join(risky, ", "))
	}
	if p := svc.Requirements.ProxyProhibited; p != nil {
		if p.Source != "" {
			return fmt.Sprintf("%s's terms forbid running it through a proxy or VPN (%s).", name, p.Source)
		}
		return name + "'s terms forbid running it through a proxy or VPN."
	}
	return ""
}

// RiskyCaps are the forbidden capabilities among caps, upper-cased, sorted.
func RiskyCaps(caps []string) []string {
	seen := map[string]bool{}
	for _, c := range caps {
		name := strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(c)), "CAP_")
		if ForbiddenCaps[name] {
			seen[name] = true
		}
	}
	out := make([]string, 0, len(seen))
	for c := range seen {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// Traffic cap
// ---------------------------------------------------------------------------

// MinCapGB is the smallest cap accepted: 1 MB.
const MinCapGB = 0.001

// CapBytes is the cap in bytes; false when the proxy has none.
func CapBytes(d Definition) (uint64, bool) {
	if d.CapGB == nil || *d.CapGB <= 0 {
		return 0, false
	}
	return uint64(*d.CapGB * GB), true
}

func thresholdPct(d Definition) int {
	if d.CapThresholdPct == 0 {
		return DefaultThresholdPct
	}
	return d.CapThresholdPct
}

// ThresholdBytes is where the copies are stopped; false when the proxy has no cap.
func ThresholdBytes(d Definition) (uint64, bool) {
	c, ok := CapBytes(d)
	if !ok {
		return 0, false
	}
	return c * uint64(thresholdPct(d)) / 100, true
}

// BaselineKeep is how long a tunnel's counter baseline is kept after its last
// reading, so the traffic of a tunnel that kept running while the app was closed is
// counted once, not again from zero.
const BaselineKeep = 31 * 24 * time.Hour

// capDelta is the bytes a tunnel moved since the last counted reading. The tunnel's
// counters run for the container's whole life, so the difference on the same start
// is exact after any gap. A first sighting counts everything (the counter began at
// zero when the tunnel started). A counter that went back, or a new start time, is a
// restart: the new counter counts from zero.
func capDelta(prev *Counter, current uint64, prevValue uint64, startedAt string) uint64 {
	if prev == nil {
		return current
	}
	if startedAt != "" && prev.StartedAt != "" && startedAt != prev.StartedAt {
		return current
	}
	if current < prevValue {
		return current
	}
	return current - prevValue
}

// Traffic is one tunnel's counters, as the runtime read them.
type Traffic struct {
	TunnelID    string
	StartedAt   string
	Rx, Tx      *uint64
	ExitIP      string
	ExitCountry string
}

// Account folds one reading of a proxy's tunnels into its state. It returns the new
// state and whether this reading crossed the cap threshold. A missing counter is
// unknown, not zero.
func Account(s State, d Definition, readings []Traffic, now time.Time) (State, bool) {
	out := s
	counters := make(map[string]Counter, len(s.Counters))
	for k, v := range s.Counters {
		counters[k] = v
	}
	used := s.UsedBytes
	for _, r := range readings {
		if r.TunnelID == "" || r.Rx == nil || r.Tx == nil {
			continue
		}
		var prev *Counter
		if c, ok := counters[r.TunnelID]; ok {
			prev = &c
		}
		var prx, ptx uint64
		if prev != nil {
			prx, ptx = prev.Rx, prev.Tx
		}
		used += capDelta(prev, *r.Rx, prx, r.StartedAt)
		used += capDelta(prev, *r.Tx, ptx, r.StartedAt)
		counters[r.TunnelID] = Counter{Rx: *r.Rx, Tx: *r.Tx, SeenAt: now.Unix(), StartedAt: r.StartedAt}
		if ip := PublicIP(r.ExitIP); ip != "" {
			out.LastExitIP = ip
			out.LastCountry = r.ExitCountry
			out.LastCheckedAt = now.UTC().Format(time.RFC3339)
		}
	}
	for k, v := range counters {
		if now.Sub(time.Unix(v.SeenAt, 0)) >= BaselineKeep {
			delete(counters, k)
		}
	}
	out.Counters = counters
	out.UsedBytes = used
	limit, ok := ThresholdBytes(d)
	crossed := ok && out.CappedAt == "" && used >= limit
	if crossed {
		out.CappedAt = now.UTC().Format(time.RFC3339)
		out.CapReason = CapReason(d, now.UTC())
	}
	return out, crossed
}

// CapReason says why the copies were stopped.
func CapReason(d Definition, when time.Time) string {
	c, _ := CapBytes(d)
	name := d.Name
	if name == "" {
		name = "This proxy"
	}
	reason := fmt.Sprintf("%s reached %d%% of its %s GB cap on %s", name, thresholdPct(d), strconv.FormatFloat(float64(c)/GB, 'g', -1, 64), when.Format("2006-01-02"))
	if d.ResetDay != nil {
		return reason + fmt.Sprintf("; it resets on day %d of the month", *d.ResetDay)
	}
	return reason + "; reset it by hand to start the copies again"
}

// CapChange brings the cap state in line with an edited definition. It returns the
// new state and whether the copies the cap stopped should start again. A cap turned
// on starts a fresh period, so traffic from before it existed never counts. A cap
// turned off, or moved above what this period has used, lifts the stop.
func CapChange(old, updated Definition, s State, today time.Time) (State, bool) {
	_, newHas := CapBytes(updated)
	_, oldHas := CapBytes(old)
	if newHas && !oldHas {
		return ResetState(s, today), s.CappedAt != ""
	}
	if s.CappedAt == "" {
		return s, false
	}
	limit, ok := ThresholdBytes(updated)
	if !ok || s.UsedBytes < limit {
		out := s
		out.CappedAt, out.CapReason = "", ""
		return out, true
	}
	if !sameCap(old, updated) {
		out := s
		out.CapReason = CapReason(updated, today)
		return out, false
	}
	return s, false
}

func sameCap(a, b Definition) bool {
	return formatFloat(a.CapGB) == formatFloat(b.CapGB) && a.CapThresholdPct == b.CapThresholdPct &&
		formatInt(a.ResetDay) == formatInt(b.ResetDay) && a.Name == b.Name
}

// ResetState starts a new period: nothing used, no cap hit. Counter baselines stay,
// so traffic from before the reset is not counted again.
func ResetState(s State, today time.Time) State {
	out := s
	out.UsedBytes = 0
	out.PeriodStart = today.Format("2006-01-02")
	out.CappedAt, out.CapReason = "", ""
	return out
}

// ResetDue is true when the period that began on the last reset day has not been
// started yet.
func ResetDue(d Definition, s State, today time.Time) bool {
	if d.ResetDay == nil || *d.ResetDay == 0 {
		return false
	}
	day := *d.ResetDay
	y, m, dd := today.Date()
	boundary := time.Date(y, m, day, 0, 0, 0, 0, time.UTC)
	if dd < day {
		boundary = boundary.AddDate(0, -1, 0)
	}
	if s.PeriodStart == "" {
		return true
	}
	start, err := time.Parse("2006-01-02", s.PeriodStart)
	if err != nil {
		return true
	}
	return start.Before(boundary)
}

// ---------------------------------------------------------------------------
// Exit IPs
// ---------------------------------------------------------------------------

// PublicIP returns ip normalised when it is a public unicast address, else "". A
// private, loopback, link-local, CGNAT or unparseable address is never an exit a
// provider sees, so it is never grouped with anything.
func PublicIP(ip string) string {
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return ""
	}
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() {
		return ""
	}
	if addr.Is4() && netip.MustParsePrefix("100.64.0.0/10").Contains(addr) {
		return ""
	}
	return addr.String()
}

// Exit is one running proxied copy and where it leaves from.
type Exit struct {
	Slug     string
	ProxyID  int64
	ExitIP   string
	Rotating bool
}

// SameExit lists where slug already leaves from exitIP: this machine's own
// connection, or another proxied copy of it. Empty when the exit is unknown (unknown
// is not shared) or rotating.
func SameExit(slug string, exitIP string, ownIP string, ownRuns bool, others []Exit, self int64) []string {
	ip := PublicIP(exitIP)
	if ip == "" {
		return nil
	}
	var found []string
	if ownRuns && PublicIP(ownIP) == ip {
		found = append(found, "this machine's own connection")
	}
	for _, e := range others {
		if e.ProxyID == self || e.Slug != slug || e.Rotating {
			continue
		}
		if PublicIP(e.ExitIP) == ip {
			found = append(found, fmt.Sprintf("proxy %d", e.ProxyID))
		}
	}
	return found
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func isTrue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "on", "yes":
		return true
	}
	return false
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

func formatFloat(f *float64) string {
	if f == nil {
		return ""
	}
	return strconv.FormatFloat(*f, 'g', -1, 64)
}

func formatInt(i *int) string {
	if i == nil {
		return ""
	}
	return strconv.Itoa(*i)
}
