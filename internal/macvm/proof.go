package macvm

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/securevm"
)

// VerdictFile holds the last green proof's scope (Proven).
const VerdictFile = "proven.json"

// proofWait is how long a step waits for late arrivals after its probes.
var proofWait = 3 * time.Second

// Target is one place the guest must not reach.
type Target struct {
	Label string
	Addr  netip.Addr
	// Zone is an IPv6 link-local zone, as the guest names its interface.
	Zone string
	// Canary is whether a host canary listens there; otherwise the evidence
	// is a packet capture on Iface.
	Canary bool
	Iface  string
}

func (t Target) dial() string {
	if t.Zone != "" {
		return t.Addr.String() + "%" + t.Zone
	}
	return t.Addr.String()
}

// hostTargets reads the host's own addresses (every IPv4 and IPv6 address on
// an up interface but loopback), the router and the owner's public IPs.
func hostTargets(gw netip.Addr, bridge string, router netip.Addr, routerIf string, deny []netip.Addr) []Target {
	var ts []Target
	ifs, _ := net.Interfaces()
	n := 0
	for _, ifc := range ifs {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			p, err := netip.ParsePrefix(a.String())
			if err != nil {
				continue
			}
			ip := p.Addr()
			if ip == gw {
				continue
			}
			t := Target{Addr: ip.WithZone(""), Canary: true}
			switch {
			case ip.Is6() && ip.IsLinkLocalUnicast():
				if ifc.Name != bridge {
					continue // the guest is on one link: the bridge's
				}
				t.Zone = "en0"
			}
			n++
			t.Label = fmt.Sprintf("host%d-%s", n, ifc.Name)
			ts = append(ts, t)
		}
	}
	ts = append(ts, Target{Label: "gateway", Addr: gw, Canary: true})
	if router.Is4() {
		ts = append(ts, Target{Label: "router", Addr: router, Iface: routerIf})
	}
	for i, a := range deny {
		ts = append(ts, Target{Label: fmt.Sprintf("own-ip%d", i+1), Addr: a, Iface: routerIf})
	}
	return ts
}

// canary listens for tokens on one port, TCP and UDP, every address.
type canary struct {
	port uint16
	mu   sync.Mutex
	got  map[string]bool // "<token>-<label>"
	ls   []net.Listener
	pcs  []net.PacketConn
}

func newCanary(port uint16) (*canary, error) {
	c := &canary{port: port, got: map[string]bool{}}
	for _, nw := range []string{"tcp4", "tcp6"} {
		l, err := net.Listen(nw, fmt.Sprintf(":%d", port))
		if err != nil {
			c.close()
			return nil, err
		}
		c.ls = append(c.ls, l)
		go c.accept(l)
	}
	for _, nw := range []string{"udp4", "udp6"} {
		pc, err := net.ListenPacket(nw, fmt.Sprintf(":%d", port))
		if err != nil {
			c.close()
			return nil, err
		}
		c.pcs = append(c.pcs, pc)
		go c.read(pc)
	}
	return c, nil
}

func (c *canary) note(b []byte) {
	for _, line := range strings.Fields(string(b)) {
		c.mu.Lock()
		c.got[line] = true
		c.mu.Unlock()
	}
}

func (c *canary) accept(l net.Listener) {
	for {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			b := make([]byte, 256)
			n, _ := conn.Read(b)
			c.note(b[:n])
		}()
	}
}

func (c *canary) read(pc net.PacketConn) {
	b := make([]byte, 512)
	for {
		n, _, err := pc.ReadFrom(b)
		if err != nil {
			return
		}
		c.note(b[:n])
	}
}

func (c *canary) arrived(token string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for k := range c.got {
		if strings.HasPrefix(k, token+"-") {
			out = append(out, strings.TrimPrefix(k, token+"-"))
		}
	}
	slices.Sort(out)
	return out
}

func (c *canary) close() {
	for _, l := range c.ls {
		l.Close()
	}
	for _, pc := range c.pcs {
		pc.Close()
	}
}

// capture counts packets toward addrs on port leaving iface, for targets with
// no canary (the router, the owner's public IPs).
type capture struct {
	cmd  *exec.Cmd
	mu   sync.Mutex
	seen map[string]bool
}

func startCapture(ctx context.Context, iface string, port uint16, addrs []netip.Addr) (*capture, error) {
	if iface == "" || len(addrs) == 0 {
		return &capture{seen: map[string]bool{}}, nil
	}
	var hosts []string
	for _, a := range addrs {
		hosts = append(hosts, "dst host "+a.String())
	}
	filter := fmt.Sprintf("(%s) and (tcp or udp) and dst port %d", strings.Join(hosts, " or "), port)
	cmd := exec.CommandContext(ctx, "/usr/sbin/tcpdump", "-n", "-l", "-i", iface, filter)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := &capture{cmd: cmd, seen: map[string]bool{}}
	go func() {
		sc := bufio.NewScanner(out)
		for sc.Scan() {
			for _, a := range addrs {
				if strings.Contains(sc.Text(), "> "+a.String()+".") {
					c.mu.Lock()
					c.seen[a.String()] = true
					c.mu.Unlock()
				}
			}
		}
	}()
	// tcpdump needs a moment before it captures.
	time.Sleep(time.Second)
	return c, nil
}

func (c *capture) stop() map[string]bool {
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
		_ = c.cmd.Wait()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seen
}

// probeScript runs in the guest as root. It sends "<token>-<label>" to every
// target on stdin, over TCP and UDP, all at once. What it prints only says a
// probe was sent: the verdict reads the host's canaries and captures.
const probeScript = `tok=$1; port=$2
while read -r label addr; do
  case $addr in *:*) v=-6 ;; *) v=-4 ;; esac
  ( printf '%s-%s\n' "$tok" "$label" | /usr/bin/nc $v -G 2 -w 2 "$addr" "$port" >/dev/null 2>&1
    printf '%s-%s\n' "$tok" "$label" | /usr/bin/nc $v -u -w 1 "$addr" "$port" >/dev/null 2>&1
    echo "sent $label" ) &
done
wait`

// functionScript checks that the guest reaches the internet and public DNS.
// The fence blocks the gateway, which DHCP names as the DNS server, so it
// first sets public resolvers on every network service, as provision.sh does.
const functionScript = `/usr/sbin/networksetup -listallnetworkservices | tail -n +2 | sed 's/^\*//' | while IFS= read -r svc; do /usr/sbin/networksetup -setdnsservers "$svc" 9.9.9.9 149.112.112.112; done
/usr/bin/curl -fsS -m 20 https://www.apple.com/library/test/success.html | grep -q Success && echo internet-ok
/usr/bin/dscacheutil -q host -a name www.apple.com | grep -q ip_address && echo dns-ok`

// bonjourScript lists the Bonjour services the guest can see for 4 seconds.
const bonjourScript = `/usr/bin/dns-sd -B _services._dns-sd._udp local. > /tmp/cp-bonjour 2>&1 & p=$!; sleep 4; kill $p 2>/dev/null; grep -c ' Add ' /tmp/cp-bonjour; rm -f /tmp/cp-bonjour`

// guestRun runs a script in the guest as root through the agent, as the owner.
func (d *Daemon) guestRun(ctx context.Context, inst Install, stdin, script string, args ...string) (string, error) {
	a := AsOwnerArgs(inst, d.P.Bin, append([]string{TartExe(d.P.Bin), "exec", "-i", VMName, "/bin/sh", "-c", script, "cp"}, args...)...)
	cmd := exec.CommandContext(ctx, "/bin/launchctl", a...)
	cmd.Stdin = strings.NewReader(stdin)
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.Output()
	return string(out), err
}

// waitAgent waits until the guest agent answers.
func (d *Daemon) waitAgent(ctx context.Context, inst Install) error {
	deadline := d.now().Add(agentWait)
	for {
		c, cancel := context.WithTimeout(ctx, 15*time.Second)
		_, err := d.guestRun(c, inst, "", "/usr/bin/true")
		cancel()
		if err == nil {
			return nil
		}
		if d.now().After(deadline) {
			return fmt.Errorf("the guest agent did not answer within %s", agentWait)
		}
		if err := sleep(ctx, 5*time.Second); err != nil {
			return err
		}
	}
}

func randomBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func newToken() string { return "cpm-" + hex.EncodeToString(randomBytes(8)) }

func randomPort() uint16 { return 40000 + binary.BigEndian.Uint16(randomBytes(2))%20000 }

// prover runs one proof.
type prover struct {
	d      *Daemon
	inst   Install
	deny   []netip.Addr
	port   uint16
	can    *canary
	steps  []securevm.ProofStep
	router netip.Addr
	rIface string
}

func (p *prover) add(id, kind, outcome, detail string) {
	p.steps = append(p.steps, securevm.ProofStep{ID: id, Kind: kind, Outcome: outcome, Detail: detail})
	p.d.logf("proof %s (%s): %s %s", id, kind, outcome, detail)
}

// bridgeNow is the guest's bridge as it is now.
func (p *prover) bridgeNow(ctx context.Context) (Bridge, error) {
	var desired Desired
	if err := ReadJSON(p.d.state(DesiredFile), &desired); err != nil {
		return Bridge{}, err
	}
	bs, err := p.d.bridges(ctx)
	if err != nil {
		return Bridge{}, err
	}
	for _, b := range bs {
		if b.Name == desired.Bridge {
			return b, nil
		}
	}
	return Bridge{}, fmt.Errorf("the guest's bridge %s is gone", desired.Bridge)
}

// connectFor connects the guest's port for a proof step: the step itself is
// the fence's test.
func (p *prover) connectFor(ctx context.Context, b Bridge) error {
	var desired Desired
	_ = ReadJSON(p.d.state(DesiredFile), &desired)
	if b.HasMember(desired.Port) {
		return nil
	}
	_, err := p.d.R.Run(ctx, "/sbin/ifconfig", b.Name, "addm", desired.Port)
	return err
}

// send runs the probe for targets and returns what arrived at the canaries
// and what the captures saw.
func (p *prover) send(ctx context.Context, targets []Target) (arrived []string, captured []string, err error) {
	token := newToken()
	byIface := map[string][]netip.Addr{}
	for _, t := range targets {
		if !t.Canary && t.Iface != "" {
			byIface[t.Iface] = append(byIface[t.Iface], t.Addr)
		}
	}
	var caps []*capture
	for ifc, addrs := range byIface {
		c, err := startCapture(ctx, ifc, p.port, addrs)
		if err != nil {
			return nil, nil, fmt.Errorf("packet capture on %s: %w", ifc, err)
		}
		caps = append(caps, c)
	}
	var in strings.Builder
	for _, t := range targets {
		fmt.Fprintf(&in, "%s %s\n", t.Label, t.dial())
	}
	c, cancel := context.WithTimeout(ctx, 90*time.Second)
	_, err = p.d.guestRun(c, p.inst, in.String(), probeScript, token, fmt.Sprint(p.port))
	cancel()
	_ = sleep(ctx, proofWait)
	for _, cp := range caps {
		for a := range cp.stop() {
			for _, t := range targets {
				if t.Addr.String() == a {
					captured = append(captured, t.Label)
				}
			}
		}
	}
	if err != nil {
		return nil, nil, fmt.Errorf("the guest could not run the probe: %w", err)
	}
	return p.can.arrived(token), captured, nil
}

// red opens holes to targets and needs each one to get through.
func (p *prover) red(ctx context.Context, id string, targets []Target) bool {
	arrived, captured, err := p.send(ctx, targets)
	if err != nil {
		p.add(id, securevm.KindRed, securevm.Broken, err.Error())
		return false
	}
	var missing []string
	for _, t := range targets {
		if !slices.Contains(arrived, t.Label) && !slices.Contains(captured, t.Label) {
			missing = append(missing, t.Label)
		}
	}
	if len(missing) > 0 {
		p.add(id, securevm.KindRed, securevm.Broken, "the hole was open but nothing arrived from "+strings.Join(missing, ", ")+", so this check proves nothing")
		return false
	}
	p.add(id, securevm.KindRed, securevm.WentRed, "through the hole: "+strings.Join(append(arrived, captured...), ", "))
	return true
}

// green needs nothing to get through.
func (p *prover) green(ctx context.Context, id, witness string, targets []Target, witnessed func() (bool, string)) bool {
	arrived, captured, err := p.send(ctx, targets)
	if err != nil {
		p.add(id, securevm.KindGreen, securevm.Broken, err.Error())
		return false
	}
	if leaked := append(arrived, captured...); len(leaked) > 0 {
		p.add(id, securevm.KindGreen, securevm.Leak, "reached: "+strings.Join(leaked, ", "))
		return false
	}
	if witnessed != nil {
		if ok, why := witnessed(); !ok {
			p.add(id, securevm.KindGreen, securevm.NotProven, "nothing arrived, but "+why)
			return false
		}
	}
	p.add(id, securevm.KindGreen, securevm.StayedGreen, fmt.Sprintf("nothing of %d targets arrived%s", len(targets), witness))
	return true
}

// counters reads the anchor's drop counters.
func (p *prover) counters(ctx context.Context) map[string]uint64 {
	out, _ := p.d.R.Run(ctx, "/sbin/pfctl", "-a", Anchor, "-vsr")
	return RuleCounters(out)
}

// Prove runs the proof and records its verdict. Each red step opens one hole
// in each layer on purpose and must see the guest's probe arrive; each green
// step must see nothing arrive. The guest is cut whenever no step runs, and
// the guard connects it afterwards only on a green verdict.
//
//	boot A (softnet with holes for the four red targets)
//	  red: LAN address, gateway (host canaries), router, own public IP (captures)
//	  L2 alone: pf without holes; nothing may arrive, pf must count the drops
//	boot B (production softnet)
//	  L1 alone: pf with the holes; nothing may arrive
//	  green: the production fence; nothing of every target may arrive
//	  Bonjour: the guest sees none of the host's services
//	  function: the guest reaches the internet and public DNS
func (d *Daemon) Prove(ctx context.Context) securevm.Proof {
	pf := securevm.Proof{At: d.now()}
	st := Status{Version: StatusVersion, WrittenAt: d.now(), VM: VMName, Network: NetDisconnected, Fence: FenceUnproven, Proving: true, Reason: "CashPilot is checking the fence"}
	d.writeStatus(&st)
	p := &prover{d: d, port: randomPort()}
	defer func() {
		pf.Steps = p.steps
		_ = WriteJSON(d.state(ProofFile), pf, 0o644)
		if pf.Green {
			build := ""
			if d.HostBuild != nil {
				build, _ = d.HostBuild(ctx)
			}
			_ = WriteJSON(d.state(VerdictFile), Proven{At: pf.At, HostBuild: build, Tart: p.inst.Tart, Softnet: p.inst.Softnet, Green: true}, 0o644)
		} else {
			_ = os.Remove(d.state(VerdictFile))
		}
		d.Pass(context.WithoutCancel(ctx), false)
	}()
	fail := func(why string) securevm.Proof {
		pf.Why = why
		d.logf("proof: not green: %s", why)
		return pf
	}
	var err error
	if p.inst, err = d.loadInstall(); err != nil {
		return fail(err.Error())
	}
	raw, err := os.ReadFile(d.state(DenyFile))
	if err != nil {
		return fail(err.Error())
	}
	if p.deny, err = ParseDeny(string(raw)); err != nil {
		return fail(err.Error())
	}
	if out, err := d.R.Run(ctx, "/sbin/route", "-n", "get", "default"); err == nil {
		p.router, p.rIface, _ = DefaultRoute(out)
	}
	if p.can, err = newCanary(p.port); err != nil {
		return fail("could not open the host canary: " + err.Error())
	}
	defer p.can.close()
	// The host's own positive control: a token sent from the host must reach
	// its canary, or a silent green would mean a deaf canary.
	ctlTok := newToken()
	if conn, err := net.DialTimeout("tcp4", fmt.Sprintf("127.0.0.1:%d", p.port), 2*time.Second); err == nil {
		fmt.Fprintf(conn, "%s-control\n", ctlTok)
		conn.Close()
	}
	_ = sleep(ctx, 500*time.Millisecond)
	if !slices.Contains(p.can.arrived(ctlTok), "control") {
		p.add("canary", securevm.KindCheck, securevm.Broken, "the host canary did not hear the host's own token")
		return fail("the host canary does not work")
	}
	p.add("canary", securevm.KindCheck, securevm.Held, "the host canary heard the host's own token")

	// Boot A: softnet with holes.
	d.stopVM(ctx, p.inst)
	lanIP := netip.Addr{}
	if ifc, err := net.InterfaceByName(p.rIface); err == nil {
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if pp, err := netip.ParsePrefix(a.String()); err == nil && pp.Addr().Is4() {
				lanIP = pp.Addr()
				break
			}
		}
	}
	var redTargets []Target
	var holes []Hole
	if lanIP.IsValid() {
		redTargets = append(redTargets, Target{Label: "lan", Addr: lanIP, Canary: true})
		holes = append(holes, Hole{Addr: lanIP, Port: p.port})
	}
	holes = append(holes, Hole{Gateway: true, Port: p.port})
	if p.router.Is4() {
		redTargets = append(redTargets, Target{Label: "router", Addr: p.router, Iface: p.rIface})
		holes = append(holes, Hole{Addr: p.router, Port: p.port})
	}
	if len(p.deny) > 0 {
		redTargets = append(redTargets, Target{Label: "own-ip", Addr: p.deny[0], Iface: p.rIface})
		holes = append(holes, Hole{Addr: p.deny[0], Port: p.port})
	}
	if err := d.Start(ctx, StartOptions{Holes: holes}); err != nil {
		return fail("could not start the VM for the proof: " + err.Error())
	}
	if err := d.waitAgent(ctx, p.inst); err != nil {
		return fail(err.Error())
	}
	b, err := p.bridgeNow(ctx)
	if err != nil {
		return fail(err.Error())
	}
	gwT := Target{Label: "gateway", Addr: b.Addr, Canary: true}
	redTargets = append(redTargets, gwT)
	if err := d.loadFence(ctx, b, p.deny, holes); err != nil {
		return fail(err.Error())
	}
	if err := p.connectFor(ctx, b); err != nil {
		return fail(err.Error())
	}
	okRed := p.red(ctx, "red-both-layers-open", redTargets)

	// L2 alone: softnet still open, pf closed. pf must count the drops.
	if err := d.loadFence(ctx, b, p.deny, nil); err != nil {
		return fail(err.Error())
	}
	before := p.counters(ctx)
	okL2 := p.green(ctx, "l2-pf-alone", ", and pf counted the drops", redTargets, func() (bool, string) {
		after := p.counters(ctx)
		if after["cp-deny"]+after["cp-host-gw"] > before["cp-deny"]+before["cp-host-gw"] {
			return true, ""
		}
		return false, "pf did not count a drop, so the probes may never have reached it"
	})
	p.cutNow(ctx)

	// Boot B: production softnet.
	d.stopVM(ctx, p.inst)
	if err := d.Start(ctx, StartOptions{}); err != nil {
		return fail("could not restart the VM with the fence: " + err.Error())
	}
	if err := d.waitAgent(ctx, p.inst); err != nil {
		return fail(err.Error())
	}
	if b, err = p.bridgeNow(ctx); err != nil {
		return fail(err.Error())
	}
	gwB := Target{Label: "gateway", Addr: b.Addr, Canary: true}
	l1Targets := append(slices.DeleteFunc(slices.Clone(redTargets), func(t Target) bool { return t.Label == "gateway" }), gwB)
	l1Holes := slices.Clone(holes)
	if err := d.loadFence(ctx, b, p.deny, l1Holes); err != nil {
		return fail(err.Error())
	}
	if err := p.connectFor(ctx, b); err != nil {
		return fail(err.Error())
	}
	okL1 := p.green(ctx, "l1-softnet-alone", "; the red step proved these probes get through an open hole", l1Targets, nil)

	// The production fence, every target.
	if err := d.loadFence(ctx, b, p.deny, nil); err != nil {
		return fail(err.Error())
	}
	all := hostTargets(b.Addr, b.Name, p.router, p.rIface, p.deny)
	okGreen := p.green(ctx, "green-production", "", all, nil)

	okBonjour := false
	if out, err := d.guestRun(ctx, p.inst, "", bonjourScript); err != nil {
		p.add("bonjour", securevm.KindGreen, securevm.Broken, "the guest could not browse: "+err.Error())
	} else if n := strings.TrimSpace(out); n != "0" {
		p.add("bonjour", securevm.KindGreen, securevm.Leak, "the guest saw "+n+" Bonjour services")
	} else {
		okBonjour = true
		p.add("bonjour", securevm.KindGreen, securevm.StayedGreen, "the guest saw no Bonjour service")
	}
	okFunc := false
	if out, err := d.guestRun(ctx, p.inst, "", functionScript); err == nil && strings.Contains(out, "internet-ok") && strings.Contains(out, "dns-ok") {
		okFunc = true
		p.add("function", securevm.KindCheck, securevm.Held, "the guest reaches the internet and public DNS")
	} else {
		p.add("function", securevm.KindCheck, securevm.Broken, "the guest does not reach the internet through the fence")
	}
	okDevices, why := d.devicesOff(ctx, p.inst)
	if okDevices {
		p.add("devices", securevm.KindCheck, securevm.Held, why)
	} else {
		p.add("devices", securevm.KindCheck, securevm.Broken, why)
	}
	p.cutNow(ctx)
	pf.Green = okRed && okL2 && okL1 && okGreen && okBonjour && okFunc && okDevices
	if pf.Green {
		pf.Why = "every hole went red when opened, and with the fence in place nothing reached this Mac, the LAN, the router or this connection's public IP"
	} else {
		pf.Why = "at least one step did not pass; the Secure VM stays off the network"
	}
	return pf
}

// cutNow takes the guest's port off its bridge between steps.
func (p *prover) cutNow(ctx context.Context) {
	var desired Desired
	_ = ReadJSON(p.d.state(DesiredFile), &desired)
	if desired.Bridge != "" && desired.Port != "" {
		_, _ = p.d.R.Run(ctx, "/sbin/ifconfig", desired.Bridge, "deletem", desired.Port)
	}
}

// devicesOff checks the running Tart's switches and the VM's folder: no
// audio, no clipboard, no shared folder.
func (d *Daemon) devicesOff(ctx context.Context, inst Install) (bool, string) {
	ps, err := d.R.Run(ctx, "/bin/ps", "-axww", "-o", "args=")
	if err != nil {
		return false, "could not read the processes"
	}
	tart := TartExe(d.P.Bin)
	for _, l := range strings.Split(ps, "\n") {
		i := strings.Index(l, "/tart run "+VMName+" ")
		if i < 0 {
			continue
		}
		exe := strings.TrimSpace(l[:i+len("/tart")])
		switch {
		case exe == tart:
		case strings.HasSuffix(exe, " "+tart):
			continue // the sudo that started our Tart, with its path as an argument
		default:
			return false, "the VM runs from another Tart"
		}
		for _, want := range []string{"--no-audio", "--no-clipboard"} {
			if !strings.Contains(l, want) {
				return false, "Tart runs without " + want
			}
		}
		if strings.Contains(l, "--dir") || strings.Contains(l, "--disk") || strings.Contains(l, "--rosetta") {
			return false, "Tart shares a folder or a disk with the guest"
		}
		return true, "no audio, no clipboard, no shared folder"
	}
	return false, "Tart is not running the VM"
}
