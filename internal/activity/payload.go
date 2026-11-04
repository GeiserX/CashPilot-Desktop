package activity

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

// Caps on one sample, as the web UI applies them (app/activity_ui.py).
const (
	MaxOut          = 40
	MaxIn           = 20
	MaxDestinations = 400
	MaxSamples      = 64
)

// Statuses a sample can have. "native" is Desktop's own: a program run
// natively, not in a container, which no helper can join.
var Statuses = []string{"ok", "host_network", "no_network", "sandboxed", "probe_failed", "skipped_budget", "native"}

// Classes are the kinds of destination procnet.classify names.
var Classes = []string{"infra", "metadata", "docker_host", "peer", "docker_peer", "lan", "tailnet", "link_local", "multicast", "public"}

var states = map[string]bool{"est": true, "syn_sent": true, "syn_recv": true, "closed": true, "other": true}

// CgroupReasons are why the helper could not tell a host-network earner's
// sockets apart, each with the plain words the page shows.
var CgroupReasons = map[string]string{
	"cgroup_v1":           "this machine uses cgroup v1; a host on cgroup v2 is needed",
	"cgroup_missing":      "its cgroup was not found",
	"sock_diag_refused":   "the kernel refused the socket list",
	"kernel_no_cgroup_id": "this kernel does not report a socket's cgroup",
}

// ProbeResult is what one helper printed, checked.
type ProbeResult struct {
	Netns       string
	Counters    *[6]int64
	EstMax      *int64
	SynSentMax  *int64
	UDPBound    *int64
	Remotes     *int64
	Truncated   bool
	ListenPorts []int
	Out         []Dest
	In          []Dest
	Attribution string
	Reason      string
}

type probeJSON struct {
	V           int             `json:"v"`
	Netns       *string         `json:"netns"`
	SnmpEnd     json.RawMessage `json:"snmp_end"`
	Summary     *summaryJSON    `json:"summary"`
	Classified  bool            `json:"classified"`
	Attribution string          `json:"attribution"`
	Reason      string          `json:"reason"`
}

type summaryJSON struct {
	EstMax      *int64            `json:"est_max"`
	SynSentMax  *int64            `json:"syn_sent_max"`
	UDPBound    *int64            `json:"udp_bound"`
	ListenPorts []json.RawMessage `json:"listen_ports"`
	Remotes     *int64            `json:"remotes_total"`
	Truncated   bool              `json:"truncated"`
	Out         []json.RawMessage `json:"out"`
	In          []json.RawMessage `json:"in"`
}

// ErrBadOutput is a helper that printed something we do not read.
var ErrBadOutput = errors.New("bad_output")

// ParseProbe checks a helper's output. Every row that is not exactly what
// procnet prints is dropped, and the caps are applied again: the helper runs
// beside an earner, so its output is checked like anything else from outside.
func ParseProbe(raw []byte) (ProbeResult, error) {
	var p probeJSON
	if err := json.Unmarshal(raw, &p); err != nil || p.V != 1 || p.Summary == nil {
		return ProbeResult{}, ErrBadOutput
	}
	if !p.Classified {
		// The vendored procnet classifies when given PROBE_CTX; an unclassified
		// reading means the helper ran some other script.
		return ProbeResult{}, ErrBadOutput
	}
	r := ProbeResult{Attribution: clip(p.Attribution, 16), Reason: clip(p.Reason, 32), Truncated: p.Summary.Truncated}
	if p.Netns != nil {
		r.Netns = clip(*p.Netns, 64)
	}
	r.Counters = counters(p.SnmpEnd)
	r.EstMax, r.SynSentMax, r.UDPBound, r.Remotes = bounded(p.Summary.EstMax), bounded(p.Summary.SynSentMax), bounded(p.Summary.UDPBound), bounded(p.Summary.Remotes)
	seenPorts := map[int]bool{}
	for i, x := range p.Summary.ListenPorts {
		if i >= 20 {
			break
		}
		var n int
		if json.Unmarshal(x, &n) == nil && n >= 0 && n <= 65535 && !seenPorts[n] {
			seenPorts[n] = true
			r.ListenPorts = append(r.ListenPorts, n)
		}
	}
	sort.Ints(r.ListenPorts)
	if r.ListenPorts == nil {
		r.ListenPorts = []int{}
	}
	r.Out, r.In = []Dest{}, []Dest{}
	for i, row := range p.Summary.Out {
		if i >= MaxOut {
			r.Truncated = true
			break
		}
		if d, ok := outRow(row); ok {
			r.Out = append(r.Out, d)
		}
	}
	for i, row := range p.Summary.In {
		if i >= MaxIn {
			r.Truncated = true
			break
		}
		if d, ok := inRow(row); ok {
			r.In = append(r.In, d)
		}
	}
	return r, nil
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func bounded(v *int64) *int64 {
	if v == nil || *v < 0 || *v > 1<<62 {
		return nil
	}
	x := *v
	return &x
}

func counters(raw json.RawMessage) *[6]int64 {
	var snmp struct {
		TCP map[string]*int64 `json:"tcp"`
		UDP map[string]*int64 `json:"udp"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &snmp) != nil || snmp.TCP == nil {
		return nil
	}
	var out [6]int64
	for i, k := range []string{"ActiveOpens", "PassiveOpens", "AttemptFails", "EstabResets"} {
		v := snmp.TCP[k]
		if v == nil {
			return nil
		}
		out[i] = *v
	}
	for i, k := range []string{"InDatagrams", "OutDatagrams"} {
		out[4+i] = -1
		if v := snmp.UDP[k]; v != nil {
			out[4+i] = *v
		}
	}
	return &out
}

// NormalIP is an address as text, IPv4-mapped IPv6 folded to IPv4, or "".
func NormalIP(s string) string {
	a, err := netip.ParseAddr(s)
	if err != nil {
		return ""
	}
	return a.Unmap().String()
}

func isClass(c string) bool {
	for _, k := range Classes {
		if k == c {
			return true
		}
	}
	return false
}

func outRow(raw json.RawMessage) (Dest, bool) {
	var row []json.RawMessage
	if json.Unmarshal(raw, &row) != nil || len(row) != 6 {
		return Dest{}, false
	}
	var proto, ip, class string
	var port, seen int
	var st map[string]json.RawMessage
	if json.Unmarshal(row[0], &proto) != nil || json.Unmarshal(row[1], &ip) != nil || json.Unmarshal(row[2], &port) != nil ||
		json.Unmarshal(row[3], &class) != nil || json.Unmarshal(row[4], &seen) != nil {
		return Dest{}, false
	}
	_ = json.Unmarshal(row[5], &st)
	ip = NormalIP(ip)
	if (proto != "tcp" && proto != "udp") || ip == "" || port < 0 || port > 65535 || !isClass(class) || seen < 1 || seen > 10 {
		return Dest{}, false
	}
	d := Dest{Proto: proto, IP: ip, Port: port, Class: class, Seen: seen, States: map[string]int{}}
	for k, v := range st {
		var n int
		if states[k] && json.Unmarshal(v, &n) == nil && n >= 0 && n <= 10000 {
			d.States[k] = n
		}
	}
	return d, true
}

func inRow(raw json.RawMessage) (Dest, bool) {
	var row []json.RawMessage
	if json.Unmarshal(raw, &row) != nil || len(row) != 4 {
		return Dest{}, false
	}
	var ip, class string
	var port, seen int
	if json.Unmarshal(row[0], &ip) != nil || json.Unmarshal(row[1], &port) != nil || json.Unmarshal(row[2], &class) != nil ||
		json.Unmarshal(row[3], &seen) != nil {
		return Dest{}, false
	}
	ip = NormalIP(ip)
	if ip == "" || port < 0 || port > 65535 || !isClass(class) || seen < 1 || seen > 10 {
		return Dest{}, false
	}
	return Dest{IP: ip, Port: port, Class: class, Seen: seen}, true
}

// BuildSample turns one group's helper reading into its sample.
func BuildSample(name string, g *Group, r ProbeResult, deltas *Deltas, now float64) Sample {
	if g.Kind == "cgroup" && r.Reason != "" {
		// The helper ran but could not tell this earner's sockets from the host's.
		deltas.Forget(g.Owner.ID)
		return baseSample(name, g.Members, g.Mode, "host_network", r.Reason)
	}
	s := baseSample(name, g.Members, g.Mode, "ok", "")
	if g.Kind == "cgroup" {
		// The host's counters are the machine's, not this earner's: no deltas.
		s.Reason = "host_cgroup"
		deltas.Forget(g.Owner.ID)
	} else {
		deltas.Apply(g.Owner.ID, r.Netns, r.Counters, now, &s)
	}
	s.EstMax, s.SynSentMax, s.UDPBound, s.RemotesTotal = r.EstMax, r.SynSentMax, r.UDPBound, r.Remotes
	s.Truncated = r.Truncated
	s.ListenPorts = r.ListenPorts
	if !g.OwnerEarner {
		// The namespace belongs to a non-earner (a VPN or Tailscale sidecar) that
		// other apps may share: only the counters are kept.
		s.Reason = "shared_namespace"
		return s
	}
	s.Out, s.In = r.Out, r.In
	return s
}

// Fit enforces the destinations cap across a window, keeping each sample's
// first rows and marking the ones that lost rows.
func Fit(w *Window) {
	if len(w.Samples) > MaxSamples {
		w.Samples = w.Samples[:MaxSamples]
	}
	budget := MaxDestinations
	for i := range w.Samples {
		s := &w.Samples[i]
		if len(s.Out)+len(s.In) > budget {
			if len(s.Out) > budget {
				s.Out = s.Out[:budget]
			}
			s.In = s.In[:max(0, min(len(s.In), budget-len(s.Out)))]
			s.Truncated = true
		}
		budget -= len(s.Out) + len(s.In)
	}
}

// Alert rules, as in app/activity_ui.py.
var (
	LanAlertClasses = []string{"lan", "tailnet", "docker_host", "docker_peer", "metadata", "link_local"}
	MailPorts       = []int{25, 465, 587, 2525}
	classLabels     = map[string]string{
		"lan":         "your local network",
		"tailnet":     "your tailnet",
		"docker_host": "the Docker host itself",
		"docker_peer": "another container on its Docker network (not an earner)",
		"metadata":    "a cloud metadata address",
		"link_local":  "a link-local address",
	}
)

// Cooldowns per alert kind, in hours.
const (
	MailCooldownHours  = 24
	LanCooldownHours   = 6
	SpikeCooldownHours = 12
	SpikeMinHours      = 72
	SpikeRatio         = 10
	SpikeBytesFloor    = 1 << 30
	SpikeOpensFloor    = 2000
)

// Finding is one alert a window raises. It never carries an address.
type Finding struct {
	Kind      string
	Container string
	Detail    string
	Message   string
}

// Subject is the cooldown key of a finding.
func (f Finding) Subject() string { return f.Container + ":" + f.Detail }

func inInts(v int, list []int) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func joinInts(v []int) string {
	parts := make([]string, len(v))
	for i, n := range v {
		parts[i] = fmt.Sprint(n)
	}
	return strings.Join(parts, ", ")
}

// AlertFindings is the mail and LAN alerts of one window. Mail: outbound TCP to
// a public address on a mail port, in any state. LAN: any outbound destination
// in LanAlertClasses.
func AlertFindings(w Window) []Finding {
	var found []Finding
	for _, s := range w.Samples {
		var mail []Dest
		ports := map[int]bool{}
		for _, d := range s.Out {
			if d.Proto == "tcp" && d.Class == "public" && inInts(d.Port, MailPorts) {
				mail = append(mail, d)
				ports[d.Port] = true
			}
		}
		if len(mail) > 0 {
			found = append(found, Finding{Kind: "activity_mail", Container: s.Container, Message: fmt.Sprintf(
				"%s opened mail connections to %d destination(s) on the internet (port %s). Earners have no reason to send mail; this is how an exit node gets your address listed as a spam source.",
				s.Container, len(mail), joinInts(sortedInts(ports)))})
		}
		byClass := map[string][]Dest{}
		for _, d := range s.Out {
			for _, c := range LanAlertClasses {
				if d.Class == c {
					byClass[c] = append(byClass[c], d)
				}
			}
		}
		classes := make([]string, 0, len(byClass))
		for c := range byClass {
			classes = append(classes, c)
		}
		sort.Strings(classes)
		for _, cls := range classes {
			rows := byClass[cls]
			pending := false
			p := map[int]bool{}
			for _, d := range rows {
				if d.States["syn_sent"] > 0 {
					pending = true
				}
				p[d.Port] = true
			}
			ports := sortedInts(p)
			if len(ports) > 5 {
				ports = ports[:5]
			}
			var msg string
			if cls == "metadata" {
				msg = fmt.Sprintf("%s tried to reach a cloud metadata address (port %s). On a cloud server that address can hand out credentials for the whole account.", s.Container, joinInts(ports))
			} else {
				msg = fmt.Sprintf("%s tried to reach %s: %d destination(s), port %s.", s.Container, classLabels[cls], len(rows), joinInts(ports))
			}
			if pending {
				msg += " Some were still pending (no answer yet), which usually means a firewall dropped them."
			}
			found = append(found, Finding{Kind: "activity_lan", Container: s.Container, Detail: cls, Message: msg})
		}
	}
	return found
}

func sortedInts(m map[int]bool) []int {
	out := make([]int, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Ints(out)
	return out
}

// Spike is true when latest is a spike against history (the hours before it).
// Unknown hours are skipped, never read as zero, and no verdict is given on
// fewer than SpikeMinHours known hours.
func Spike(history []*float64, latest *float64, floor float64) bool {
	var usable []float64
	for _, v := range history {
		if v != nil {
			usable = append(usable, *v)
		}
	}
	if latest == nil || len(usable) < SpikeMinHours {
		return false
	}
	sort.Float64s(usable)
	n := len(usable)
	median := usable[n/2]
	if n%2 == 0 {
		median = (usable[n/2-1] + usable[n/2]) / 2
	}
	return *latest > floor && *latest > SpikeRatio*median
}
