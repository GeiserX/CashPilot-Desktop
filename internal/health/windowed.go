package health

import (
	"context"
	"net"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// WindowReader reads hours of a service's logs, filtered, and the values its
// signal texts are filled from. *services.Manager, narrowed.
type WindowReader interface {
	LogsMatching(ctx context.Context, slug string, since time.Duration, match *regexp.Regexp, lines int) (string, error)
	SignalVars(slug string) map[string]string
}

// windowEvery is how often a windowed signal is judged again. Reading hours of a
// chatty service's logs costs a second or two, so it runs in the background and a
// dashboard refresh only ever reads the last verdict.
const windowEvery = 30 * time.Minute

const windowTimeout = 60 * time.Second

// IsWindowed reports whether a signal is judged over hours of logs, not the tail.
func IsWindowed(s catalog.HealthSignal) bool { return s.WindowHours > 0 }

// windowFilter is the one regex the window is filtered with: the pattern or its unless.
func windowFilter(s catalog.HealthSignal) string {
	parts := []string{"(?:" + s.Pattern + ")"}
	if s.Unless != "" {
		parts = append(parts, "(?:"+s.Unless+")")
	}
	return "(?i)" + strings.Join(parts, "|")
}

// WindowedHit is true when the pattern matches at least MinMatches lines of the
// window and the unless pattern none. No line at all is no hit: a node nobody has
// used has nothing to judge.
func WindowedHit(lines string, s catalog.HealthSignal) bool {
	pattern, err := regexp.Compile("(?i)" + s.Pattern)
	if err != nil || s.Pattern == "" || lines == "" {
		return false
	}
	matched := 0
	for _, line := range strings.Split(lines, "\n") {
		if pattern.MatchString(line) {
			matched++
		}
	}
	if matched < max(1, s.MinMatches) {
		return false
	}
	if s.Unless != "" {
		unless, err := regexp.Compile("(?i)" + s.Unless)
		if err != nil || unless.MatchString(lines) {
			return false
		}
	}
	return true
}

var placeholder = regexp.MustCompile(`\$\{(\w+)\}`)

// fillVars fills ${VAR} in a signal's text. ${HOST_ADDRESS} is this computer's
// LAN address, so a hint can name the router rule to create.
func fillVars(text string, vars map[string]string) string {
	return placeholder.ReplaceAllStringFunc(text, func(m string) string {
		key := placeholder.FindStringSubmatch(m)[1]
		if v, ok := vars[key]; ok && v != "" {
			return v
		}
		if key == "HOST_ADDRESS" {
			return lanAddress()
		}
		return m
	})
}

// lanAddress is the source address of this computer's default route, when it sits
// on an ordinary LAN interface. Dialling UDP sends nothing; it only asks the kernel
// which address it would use. Behind a VPN that address is the tunnel's, which no
// home router can forward to, so a tunnel or point-to-point interface, or Docker's
// and VMs' 172.16.0.0/12, gives the generic text instead.
var lanAddress = func() string {
	const unknown = "this computer's LAN address"
	// A remote Docker endpoint runs the node on another machine, whose address
	// this computer cannot know.
	if remoteDocker(os.Getenv("DOCKER_HOST")) {
		return "the LAN address of the machine running Docker"
	}
	conn, err := net.Dial("udp", "192.0.2.1:9")
	if err != nil {
		return unknown
	}
	defer conn.Close()
	addr, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || addr.IP.To4() == nil || !addr.IP.IsPrivate() || dockerPools.Contains(addr.IP) {
		return unknown
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return unknown
	}
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.Equal(addr.IP) {
				if iface.Flags&net.FlagUp != 0 && iface.Flags&(net.FlagPointToPoint|net.FlagLoopback) == 0 && !tunnelName(iface.Name) {
					return addr.IP.String()
				}
				return unknown
			}
		}
	}
	return unknown
}

// remoteDocker reports a DOCKER_HOST that is not a local socket or pipe.
func remoteDocker(host string) bool {
	return host != "" && !strings.HasPrefix(host, "unix://") && !strings.HasPrefix(host, "npipe://")
}

var dockerPools = &net.IPNet{IP: net.IPv4(172, 16, 0, 0), Mask: net.CIDRMask(12, 32)}

// tunnelName reports interface names VPN clients use.
func tunnelName(name string) bool {
	for _, prefix := range []string{"utun", "tun", "tap", "ppp", "wg", "tailscale", "gpd", "ipsec"} {
		if strings.HasPrefix(strings.ToLower(name), prefix) {
			return true
		}
	}
	return false
}

type windowEntry struct {
	checked time.Time
	running bool
	hits    []Hit
}

// windowCache holds the last verdict of each service's windowed signals.
type windowCache struct {
	mu      sync.Mutex
	entries map[string]*windowEntry
	now     func() time.Time
	wg      sync.WaitGroup
}

var windows = &windowCache{entries: map[string]*windowEntry{}, now: time.Now}

// hits returns the last verdict for slug and starts a fresh check in the
// background when that verdict is older than windowEvery.
func (c *windowCache) hits(reader WindowReader, slug string, signals []catalog.HealthSignal) []Hit {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[slug]
	if e == nil {
		e = &windowEntry{}
		c.entries[slug] = e
	}
	if !e.running && (e.checked.IsZero() || c.now().Sub(e.checked) >= windowEvery) {
		e.running = true
		c.wg.Add(1)
		go c.refresh(reader, slug, signals)
	}
	return e.hits
}

// forget drops a service's verdict: it stopped, so what its logs said no longer holds.
func (c *windowCache) forget(slug string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.entries[slug]; e != nil && !e.running {
		delete(c.entries, slug)
	}
}

// refresh judges every windowed signal. A failed read keeps the last verdict, so a
// runtime blip does not clear a real problem.
func (c *windowCache) refresh(reader WindowReader, slug string, signals []catalog.HealthSignal) {
	defer c.wg.Done()
	ctx, cancel := context.WithTimeout(context.Background(), windowTimeout)
	defer cancel()
	var hits []Hit
	failed := false
	for _, s := range signals {
		re, err := regexp.Compile(windowFilter(s))
		if err != nil {
			continue
		}
		since := time.Duration(min(s.WindowHours, 24) * float64(time.Hour))
		logs, err := reader.LogsMatching(ctx, slug, since, re, 1000)
		if err != nil {
			failed = true
			break
		}
		if WindowedHit(logs, s) {
			hits = append(hits, Hit{
				Pattern: s.Pattern,
				Means:   fillVars(meansOrDefault(s.Means), reader.SignalVars(slug)),
				State:   signalState(s.State),
			})
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[slug]
	if e == nil {
		e = &windowEntry{}
		c.entries[slug] = e
	}
	e.running = false
	e.checked = c.now()
	if !failed {
		e.hits = hits
	}
}

// windowedOnly splits a service's signals: the ones judged over a window, and the rest.
func windowedOnly(signals []catalog.HealthSignal) (windowed, tail []catalog.HealthSignal) {
	for _, s := range signals {
		if IsWindowed(s) {
			windowed = append(windowed, s)
		} else {
			tail = append(tail, s)
		}
	}
	return windowed, tail
}
