package health

import (
	"context"
	"log"
	"net"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/dockerhost"
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

// Restarter restarts a service that a signal with restart: true found stuck, unless
// the owner stopped it after listedAt. *services.Manager, narrowed.
type Restarter interface {
	RestartStuck(ctx context.Context, slug string, listedAt time.Time, reason string) error
}

// restartEvery is the least time between two automatic restarts of one service, so
// a service a restart does not help (the computer has no internet) is not restarted
// over and over.
const restartEvery = 6 * time.Hour

// restartTimeout bounds one automatic restart: a stop can wait for the catalog's
// stop grace period first.
const restartTimeout = 5 * time.Minute

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
	// A remote Docker endpoint, from DOCKER_HOST or the current Docker context,
	// runs the node on another machine, whose address this computer cannot know.
	if remoteDocker(os.Getenv("DOCKER_HOST")) || dockerhost.Resolve().Remote() {
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
	// restarted is when each service was last restarted by itself. It outlives
	// the entry, so a stop and start, or a failed read, does not reset the wait.
	restarted map[string]time.Time
	now       func() time.Time
	wg        sync.WaitGroup
}

var windows = &windowCache{entries: map[string]*windowEntry{}, restarted: map[string]time.Time{}, now: time.Now}

// target is one copy the windowed check reads and may restart. key is its entry in
// the cache: the slug for a service's first copy, so its verdict and wait stay where
// they always were, and Copy.key for any other copy.
type target struct {
	key   string
	label string
	read  func(ctx context.Context, since time.Duration, match *regexp.Regexp, lines int) (string, error)
	vars  func() map[string]string
	// restart is nil when the runtime cannot restart this copy.
	restart func(ctx context.Context, listedAt time.Time, reason string) error
}

// serviceTarget is a service's first copy, read and restarted through reader.
func serviceTarget(reader WindowReader, slug string) target {
	t := target{
		key:   slug,
		label: slug,
		read: func(ctx context.Context, since time.Duration, match *regexp.Regexp, lines int) (string, error) {
			return reader.LogsMatching(ctx, slug, since, match, lines)
		},
		vars: func() map[string]string { return reader.SignalVars(slug) },
	}
	if r, ok := reader.(Restarter); ok {
		t.restart = func(ctx context.Context, listedAt time.Time, reason string) error {
			return r.RestartStuck(ctx, slug, listedAt, reason)
		}
	}
	return t
}

// hits returns the last verdict for slug and starts a fresh check in the
// background when that verdict is older than windowEvery. listedAt is when the
// caller began reading the container state that says slug is up.
func (c *windowCache) hits(reader WindowReader, slug string, signals []catalog.HealthSignal, listedAt time.Time) []Hit {
	return c.hitsOf(serviceTarget(reader, slug), signals, listedAt)
}

// hitsOf is hits for any copy.
func (c *windowCache) hitsOf(t target, signals []catalog.HealthSignal, listedAt time.Time) []Hit {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[t.key]
	if e == nil {
		e = &windowEntry{}
		c.entries[t.key] = e
	}
	if !e.running && (e.checked.IsZero() || c.now().Sub(e.checked) >= windowEvery) {
		e.running = true
		c.wg.Add(1)
		go c.refresh(t, signals, listedAt)
	}
	return e.hits
}

// forget drops a copy's verdict: it stopped, so what its logs said no longer holds.
func (c *windowCache) forget(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.entries[key]; e != nil && !e.running {
		delete(c.entries, key)
	}
}

// refresh judges every windowed signal. A failed read keeps the last verdict, so a
// runtime blip does not clear a real problem.
func (c *windowCache) refresh(t target, signals []catalog.HealthSignal, listedAt time.Time) {
	defer c.wg.Done()
	ctx, cancel := context.WithTimeout(context.Background(), windowTimeout)
	defer cancel()
	var hits []Hit
	failed := false
	restartDue := false
	for _, s := range signals {
		re, err := regexp.Compile(windowFilter(s))
		if err != nil {
			continue
		}
		since := time.Duration(min(s.WindowHours, 24) * float64(time.Hour))
		logs, err := t.read(ctx, since, re, 1000)
		if err != nil {
			failed = true
			break
		}
		if WindowedHit(logs, s) {
			hits = append(hits, Hit{
				Pattern: s.Pattern,
				Means:   fillVars(meansOrDefault(s.Means), t.vars()),
				State:   signalState(s.State),
			})
			restartDue = restartDue || s.Restart
		}
	}
	if !failed && restartDue && t.restart != nil {
		c.restart(t, listedAt, hits)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[t.key]
	if e == nil {
		e = &windowEntry{}
		c.entries[t.key] = e
	}
	e.running = false
	e.checked = c.now()
	if !failed {
		e.hits = hits
	}
}

// restart restarts a copy stuck in a state its catalog says a restart clears, at
// most once every restartEvery for that copy. The wait starts at the try, so a
// restart that failed is not tried again before it either.
func (c *windowCache) restart(t target, listedAt time.Time, hits []Hit) {
	c.mu.Lock()
	last, ok := c.restarted[t.key]
	if ok && c.now().Sub(last) < restartEvery {
		c.mu.Unlock()
		return
	}
	if c.restarted == nil {
		c.restarted = map[string]time.Time{}
	}
	c.restarted[t.key] = c.now()
	c.mu.Unlock()
	means := make([]string, 0, len(hits))
	for _, h := range hits {
		means = append(means, h.Means)
	}
	ctx, cancel := context.WithTimeout(context.Background(), restartTimeout)
	defer cancel()
	if err := t.restart(ctx, listedAt, strings.Join(means, " ")); err != nil {
		log.Printf("health: could not restart %s by itself: %v", t.label, err)
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
