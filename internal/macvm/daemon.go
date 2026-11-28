package macvm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// requestPoll is how often the daemon looks for a request from the app.
const requestPoll = 2 * time.Second

// Run is the daemon: at load (the Mac's boot, or the install) it starts the VM
// with the fence, then guards it every GuardInterval and answers the app's
// requests. One loop does all of it, so a proof and a guard pass never run at
// the same time.
func (d *Daemon) Run(ctx context.Context) error {
	if _, err := d.loadInstall(); err != nil {
		return err
	}
	d.Pass(ctx, true) // cut first: at boot nothing is proven to be in place
	d.start(ctx)
	next := d.now().Add(GuardInterval)
	tick := time.NewTicker(requestPoll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
		for _, r := range d.takeRequests() {
			switch r {
			case RequestStart:
				d.start(ctx)
			case RequestRestart:
				d.restart(ctx)
			case RequestProve:
				d.Prove(ctx)
			}
			next = d.now()
		}
		if d.now().Before(next) {
			continue
		}
		next = d.now().Add(GuardInterval)
		st := d.Pass(ctx, false)
		// Only a new start applies a new deny list. It runs also when the cut
		// had to stop the VM: restart checks that the VM stopped first.
		if slicesContains(st.Changes, "own-ip") {
			d.logf("restarting %s with the new deny list", VMName)
			d.restart(ctx)
		}
		if st.Fence == FenceReprove && st.Network != NetOff {
			d.logf("macOS or a tool changed: proving the fence again")
			d.Prove(ctx)
		}
	}
}

func slicesContains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// restart stops the VM and starts it again. A VM that does not stop is not
// started: the error goes into the status, and the guard keeps it cut.
func (d *Daemon) restart(ctx context.Context) {
	inst, err := d.loadInstall()
	if err != nil {
		d.startErr = err
		d.Pass(ctx, false)
		return
	}
	d.stopVM(ctx, inst)
	if run, err := d.running(ctx, inst); err != nil || run {
		if err == nil {
			err = errors.New("the Secure VM did not stop, so it was not started again")
		}
		d.logf("restart: %v", err)
		d.startErr = err
		d.Pass(ctx, false)
		return
	}
	d.start(ctx)
}

// start starts the VM and records why a start failed in the status.
func (d *Daemon) start(ctx context.Context) {
	err := d.Start(ctx, StartOptions{})
	for i := 0; i < 3 && errors.Is(err, errPortAmbiguous); i++ {
		if sleep(ctx, 5*time.Second) != nil {
			break
		}
		err = d.Start(ctx, StartOptions{})
	}
	// The error stays in every status until a start succeeds, so a pass
	// right after it cannot wipe it before the app reads it.
	d.startErr = err
	st := d.Pass(ctx, false)
	if err != nil {
		d.logf("start failed: %v", err)
		return
	}
	// A green proof that still covers this host lets the pass above connect.
	// Without one, prove now: the guest was made by the admin step, and the
	// proof runs before any earner is installed.
	if st.Fence == FenceUnproven || st.Fence == FenceReprove {
		var pr Proven
		if ReadJSON(d.state(VerdictFile), &pr) != nil || !pr.Green || st.Fence == FenceReprove {
			d.Prove(ctx)
		}
	}
}

// takeRequests reads and deletes the request files. Only the names count.
func (d *Daemon) takeRequests() []string {
	entries, err := os.ReadDir(d.P.Request)
	if err != nil {
		return nil
	}
	var out []string
	// A request counts once: only a plain file, and only after its removal,
	// so an entry that cannot be removed never runs again at every poll.
	for _, e := range entries {
		name := e.Name()
		if err := os.Remove(filepath.Join(d.P.Request, name)); err != nil {
			continue
		}
		if e.Type().IsRegular() && IsRequest(name) && !slicesContains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

// echoServices answer with the caller's public IPv4 address as plain text.
var echoServices = []string{"https://api.ipify.org", "https://ipv4.icanhazip.com", "https://checkip.amazonaws.com"}

// PublicIPv4 asks the echo services for this host's public IPv4 addresses.
// A host behind two lines can see two; each answer counts.
func PublicIPv4(ctx context.Context) ([]netip.Addr, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	var out []netip.Addr
	var lastErr error
	for _, u := range echoServices {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return nil, err
		}
		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		// An address is at most 15 bytes and a newline; read to the end, and
		// refuse a longer answer or a read error.
		b, rerr := io.ReadAll(io.LimitReader(resp.Body, 65))
		resp.Body.Close()
		if rerr != nil || len(b) > 64 {
			lastErr = errors.New(u + " gave no IPv4 address")
			continue
		}
		a, err := netip.ParseAddr(strings.TrimSpace(string(b)))
		if err != nil || !a.Is4() {
			lastErr = errors.New(u + " gave no IPv4 address")
			continue
		}
		if !slicesContainsAddr(out, a) {
			out = append(out, a)
		}
	}
	if len(out) == 0 {
		return nil, lastErr
	}
	return out, nil
}

func slicesContainsAddr(s []netip.Addr, a netip.Addr) bool {
	for _, x := range s {
		if x == a {
			return true
		}
	}
	return false
}

// HostBuild reads this Mac's macOS build.
func HostBuild(ctx context.Context) (string, error) {
	out, err := Exec{}.Run(ctx, "/usr/bin/sw_vers", "-buildVersion")
	return strings.TrimSpace(out), err
}

// NewDaemon is the product daemon.
func NewDaemon(logf func(string, ...any)) *Daemon {
	return &Daemon{
		P: ProductPaths(), R: Exec{}, Logf: logf,
		PublicIPs: PublicIPv4, HostBuild: HostBuild,
	}
}
