package services

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/proxies"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

// Proxied copies: extra copies of a deployed service whose traffic leaves through a
// proxy instead of this machine's own connection, so one machine earns on several
// exit IPs. The rules live in internal/proxies, the Docker side in
// internal/runtime/proxied.go; this file ties them to the store and the cap.

// proxyNow is the clock the cap reads; a seam for tests.
var proxyNow = time.Now

// detectOwnIP is this machine's public IP, "" when it cannot be read. A tunnel whose
// exit is this IP is not ready, and a test that sees it fails. A seam for tests.
var detectOwnIP = func(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.ipify.org", nil)
	if err != nil {
		return ""
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
	return proxies.PublicIP(string(body))
}

// proxyState holds what the cap logic keeps between ticks. proxyMu serialises every
// read-modify-write of a proxy's state: the tick, a reset, an edit and a test all
// write that one column, and two that overlap would lose one write.
type proxyState struct {
	proxyMu sync.Mutex
	// copyMu holds one mutex per copy ("id/slug"). It orders an owner's Start or Stop,
	// a reset's start and the tick's retry of the same copy, so a stop that returned
	// is never undone by a retry that read the mark before it, and two starts never
	// overlap. Other copies never wait on it.
	copyMu sync.Map
	// last is the latest status read, for the UI.
	lastMu sync.Mutex
	last   []runtime.ProxiedCopy
	// unstopped are copies a capped proxy could not stop yet: proxy id -> slug -> why.
	unstopped map[int64]map[string]string
}

// ProxiedResult is what deploying a proxied copy reports.
type ProxiedResult struct {
	ExitIP  string `json:"exitIp"`
	Country string `json:"country"`
	Warning string `json:"warning"`
}

// ProxyTestResult is what Test reports.
type ProxyTestResult struct {
	ExitIP  string `json:"exitIp"`
	Country string `json:"country"`
	MS      int64  `json:"ms"`
}

func (m *Manager) proxier() (runtime.ProxyProvider, error) {
	provider, _ := m.resolveProvider("")
	p, ok := provider.(runtime.ProxyProvider)
	if !ok {
		return nil, errors.New("this runtime cannot run copies through a proxy; it needs Docker")
	}
	return p, nil
}

func decodeProxy(p store.Proxy) (proxies.Definition, proxies.State, error) {
	var d proxies.Definition
	var s proxies.State
	if err := json.Unmarshal(p.Definition, &d); err != nil {
		return d, s, fmt.Errorf("proxy %d: unreadable definition: %w", p.ID, err)
	}
	if len(p.State) > 0 {
		if err := json.Unmarshal(p.State, &s); err != nil {
			return d, s, fmt.Errorf("proxy %d: unreadable state: %w", p.ID, err)
		}
	}
	return d, s, nil
}

func (m *Manager) loadProxy(id int64) (proxies.Definition, proxies.State, error) {
	p, ok, err := m.store.GetProxy(id)
	if err != nil {
		return proxies.Definition{}, proxies.State{}, err
	}
	if !ok {
		return proxies.Definition{}, proxies.State{}, store.ErrProxyNotFound
	}
	return decodeProxy(p)
}

func (m *Manager) saveState(id int64, s proxies.State) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return m.store.UpdateProxyState(id, raw)
}

// patchState applies change to a fresh read of the proxy's state, under the lock.
func (m *Manager) patchState(id int64, change func(*proxies.State)) error {
	m.proxy.proxyMu.Lock()
	defer m.proxy.proxyMu.Unlock()
	_, s, err := m.loadProxy(id)
	if err != nil {
		return err
	}
	change(&s)
	return m.saveState(id, s)
}

// ListProxies returns every proxy without its secrets, with its copies as the last
// status read saw them.
func (m *Manager) ListProxies() ([]proxies.View, error) {
	stored, err := m.store.ListProxies()
	if err != nil {
		return nil, err
	}
	records, err := m.store.ListProxiedCopies(0)
	if err != nil {
		return nil, err
	}
	live := map[string]runtime.ProxiedCopy{}
	for _, c := range m.ProxiedCopies() {
		live[fmt.Sprintf("%d/%s", c.ProxyID, c.Slug)] = c
	}
	out := make([]proxies.View, 0, len(stored))
	for _, p := range stored {
		d, s, err := decodeProxy(p)
		if err != nil {
			return nil, err
		}
		var copies []proxies.CopyView
		for _, r := range records {
			if r.ProxyID != p.ID {
				continue
			}
			cv := proxies.CopyView{Slug: r.Slug, Name: m.serviceName(r.Slug), Status: "unknown", TunnelStatus: "unknown", StoppedByCap: r.StoppedByCap}
			if c, ok := live[fmt.Sprintf("%d/%s", p.ID, r.Slug)]; ok {
				cv.Status, cv.TunnelStatus, cv.ExitIP, cv.ExitCountry = c.Status, c.TunnelStatus, c.ExitIP, c.ExitCountry
			}
			copies = append(copies, cv)
		}
		out = append(out, proxies.PublicView(p.ID, d, s, copies))
	}
	return out, nil
}

func (m *Manager) serviceName(slug string) string {
	if svc, ok := m.catalog.Get(slug); ok && svc.Name != "" {
		return svc.Name
	}
	return slug
}

// SaveProxy creates a proxy (id 0) or edits one. On an edit a blank secret keeps the
// stored one. A cap turned on starts a fresh period; one turned off or raised above
// this period's use lifts the stop and starts the copies it stopped.
func (m *Manager) SaveProxy(ctx context.Context, id int64, values map[string]string) (proxies.View, error) {
	if id == 0 {
		d, err := proxies.Clean(values, nil)
		if err != nil {
			return proxies.View{}, err
		}
		raw, err := json.Marshal(d)
		if err != nil {
			return proxies.View{}, err
		}
		newID, err := m.store.InsertProxy(raw)
		if err != nil {
			return proxies.View{}, err
		}
		if d.ResetDay != nil && *d.ResetDay > 0 {
			// The first period starts now; with no start the first tick would take it
			// for a missed reset and zero what was counted so far.
			if err := m.saveState(newID, proxies.ResetState(proxies.State{}, proxyNow().UTC())); err != nil {
				return proxies.View{}, err
			}
		}
		return m.view(newID)
	}
	m.proxy.proxyMu.Lock()
	old, s, err := m.loadProxy(id)
	if err != nil {
		m.proxy.proxyMu.Unlock()
		return proxies.View{}, err
	}
	d, err := proxies.Clean(values, &old)
	if err != nil {
		m.proxy.proxyMu.Unlock()
		return proxies.View{}, err
	}
	raw, err := json.Marshal(d)
	if err == nil {
		err = m.store.UpdateProxyDefinition(id, raw)
	}
	var release bool
	if err == nil {
		var next proxies.State
		next, release = proxies.CapChange(old, d, s, proxyNow().UTC())
		err = m.saveState(id, next)
	}
	m.proxy.proxyMu.Unlock()
	if err != nil {
		return proxies.View{}, err
	}
	if release {
		if _, err := m.releaseCapped(ctx, id, d); err != nil {
			return proxies.View{}, err
		}
	}
	return m.view(id)
}

func (m *Manager) view(id int64) (proxies.View, error) {
	all, err := m.ListProxies()
	if err != nil {
		return proxies.View{}, err
	}
	for _, v := range all {
		if v.ID == id {
			return v, nil
		}
	}
	return proxies.View{}, store.ErrProxyNotFound
}

// DeleteProxy deletes a proxy that no copy runs through.
func (m *Manager) DeleteProxy(id int64) error {
	// Under the lock a copy being deployed records itself first, so it is either seen
	// here or finds the proxy gone.
	m.proxy.proxyMu.Lock()
	defer m.proxy.proxyMu.Unlock()
	if _, _, err := m.loadProxy(id); err != nil {
		return err
	}
	copies, err := m.store.ListProxiedCopies(id)
	if err != nil {
		return err
	}
	if len(copies) > 0 {
		return errors.New("remove the copies running through this proxy first")
	}
	return m.store.DeleteProxy(id)
}

func specOf(d proxies.Definition) runtime.ProxySpec {
	return runtime.ProxySpec{
		Kind: d.Kind, Host: d.Host, Port: d.Port, Username: d.Username, Password: d.Password,
		AuthKey: d.AuthKey, ExitNode: d.ExitNode, LoginServer: d.LoginServer,
	}
}

// TestProxy brings up a throwaway tunnel and reports the exit IP and country it gets.
// The outcome stays on the proxy, so a failure outlives the toast that showed it.
func (m *Manager) TestProxy(ctx context.Context, id int64) (ProxyTestResult, error) {
	d, _, err := m.loadProxy(id)
	if err != nil {
		return ProxyTestResult{}, err
	}
	p, err := m.proxier()
	if err != nil {
		return ProxyTestResult{}, err
	}
	exit, testErr := p.TestProxy(ctx, specOf(d), detectOwnIP(ctx))
	now := proxyNow().UTC().Format(time.RFC3339)
	if testErr != nil {
		msg := maskSecrets(testErr.Error(), d)
		_ = m.patchState(id, func(s *proxies.State) {
			s.LastTest = &proxies.TestResult{OK: false, At: now, Error: truncateText(msg, 400)}
		})
		return ProxyTestResult{}, errors.New(msg)
	}
	_ = m.patchState(id, func(s *proxies.State) {
		s.LastExitIP, s.LastCountry, s.LastCheckedAt = exit.IP, exit.Country, now
		s.LastTest = &proxies.TestResult{OK: true, At: now}
	})
	return ProxyTestResult{ExitIP: exit.IP, Country: exit.Country, MS: exit.MS}, nil
}

// maskSecrets is the last line of defence: every secret of the proxy, whatever shape
// an error took, becomes ***.
func maskSecrets(text string, d proxies.Definition) string {
	values := d.SecretValues()
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	for _, v := range values {
		text = strings.ReplaceAll(text, v, "***")
	}
	return text
}

func truncateText(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// ProxyRefusal is why slug cannot run through a proxy here, or "".
func (m *Manager) ProxyRefusal(slug string) string {
	svc, ok := m.catalog.Get(slug)
	if !ok {
		return "Unknown service"
	}
	dep, deployed, err := m.store.GetDeployment(slug)
	if err != nil {
		return "Could not read its deployment: " + err.Error()
	}
	if deployed {
		if err := refuseNonDocker(svc, dep); err != nil {
			return err.Error()
		}
	}
	if svc.Custom {
		revealed, err := m.revealCustom(slug)
		if err != nil {
			return err.Error()
		}
		svc = revealed
	}
	return proxies.Refusal(svc)
}

// refuseNonDocker refuses a deployment that does not run on Docker: only Docker can
// run a copy through a proxy. ProxyRefusal says it up front, DeployProxied enforces it.
func refuseNonDocker(svc catalog.Service, dep store.Deployment) error {
	if dep.Runtime != "" && dep.Runtime != defaultRuntimeKind {
		return fmt.Errorf("%s runs on the %s runtime; only Docker can run a copy through a proxy", svc.Name, dep.Runtime)
	}
	return nil
}

// DeployProxied runs a copy of slug through proxy id. The service must already be
// deployed here: the copy runs with its image and credentials, under its own device
// names. It holds the service's deploy lock, so a removal never lands between the
// copy's record and its start.
func (m *Manager) DeployProxied(ctx context.Context, slug string, id int64) (ProxiedResult, error) {
	defer m.lockDeploy(slug)()
	_, s, err := m.loadProxy(id)
	if err != nil {
		return ProxiedResult{}, err
	}
	if s.CappedAt != "" {
		return ProxiedResult{}, errors.New(orText(s.CapReason, "This proxy reached its traffic cap"))
	}
	svc, ok := m.catalog.Get(slug)
	if !ok {
		return ProxiedResult{}, fmt.Errorf("unknown service: %s", slug)
	}
	if err := refuseRetired(svc); err != nil {
		return ProxiedResult{}, err
	}
	dep, deployed, err := m.store.GetDeployment(slug)
	if err != nil {
		return ProxiedResult{}, err
	}
	if !deployed {
		return ProxiedResult{}, fmt.Errorf("deploy %s on this machine first; the proxied copy runs with its settings", svc.Name)
	}
	if err := refuseNonDocker(svc, dep); err != nil {
		return ProxiedResult{}, err
	}
	var credentials map[string]string
	if svc.Custom {
		revealed, release, err := m.holdCustom(slug)
		if err != nil {
			return ProxiedResult{}, err
		}
		defer release()
		svc = revealed
	} else {
		credentials, err = m.store.GetCredentials(slug)
		if err != nil {
			return ProxiedResult{}, err
		}
		if dep.CustomImage != "" {
			svc.Docker.Image = dep.CustomImage
			svc.Docker.ImageByArch = nil
		}
	}
	return m.runProxied(ctx, slug, id, svc, credentials, false)
}

// runProxied starts slug through proxy id: a copy beside the service, or with routed
// the service itself (DeployRouted), under its own device names.
func (m *Manager) runProxied(ctx context.Context, slug string, id int64, svc catalog.Service, credentials map[string]string, routed bool) (ProxiedResult, error) {
	if why := proxies.Refusal(svc); why != "" {
		return ProxiedResult{}, errors.New(why)
	}
	if err := validateRequired(svc, credentials); err != nil {
		return ProxiedResult{}, err
	}
	p, err := m.proxier()
	if err != nil {
		return ProxiedResult{}, err
	}
	ownIP := detectOwnIP(ctx)
	// The record goes in before the copy exists, under the proxy's lock: a copy with
	// no record would run outside the cap and the delete guard, and with no row to
	// stop it from.
	m.proxy.proxyMu.Lock()
	d, s, err := m.loadProxy(id)
	if err == nil && s.CappedAt != "" {
		err = errors.New(orText(s.CapReason, "This proxy reached its traffic cap"))
	}
	hadRecord := false
	if err == nil {
		var records []store.ProxiedCopy
		if records, err = m.store.ListProxiedCopies(id); err == nil {
			for _, r := range records {
				hadRecord = hadRecord || r.Slug == slug
			}
			err = m.store.SaveProxiedCopy(id, slug)
		}
	}
	m.proxy.proxyMu.Unlock()
	if err != nil {
		return ProxiedResult{}, err
	}
	m.store.RecordEvent(slug, "proxied_deploy_start", fmt.Sprintf("proxy %d", id))
	exit, err := p.DeployProxied(ctx, runtime.ProxiedDeploy{
		Slug: slug, ProxyID: id, Service: svc, Env: credentials, Proxy: specOf(d), OwnIP: ownIP, Routed: routed,
	})
	if err != nil {
		msg := maskSecrets(err.Error(), d)
		m.store.RecordEvent(slug, "proxied_deploy_error", msg)
		if !hadRecord {
			_ = m.store.DeleteProxiedCopy(id, slug)
		}
		return ProxiedResult{}, errors.New(msg)
	}
	m.store.RecordEvent(slug, "proxied_deployed", fmt.Sprintf("proxy %d", id))
	result := ProxiedResult{ExitIP: proxies.PublicIP(exit.IP), Country: exit.Country}
	// The exit IP is only known once the tunnel is up, so this check comes after.
	others := m.otherExits(id)
	_, ownRuns := m.runningHere(slug)
	if same := proxies.SameExit(slug, result.ExitIP, ownIP, ownRuns, others, id); len(same) > 0 {
		result.Warning = fmt.Sprintf("%s already leaves from %s on %s. ", svc.Name, result.ExitIP, strings.Join(same, ", "))
		if limit := svc.Requirements.DevicesPerIP; limit != nil && *limit == 1 {
			result.Warning += "Its provider allows one device per IP, so this copy earns nothing there."
		} else {
			result.Warning += "Copies on one IP share its bandwidth, and the provider may limit devices per IP."
		}
	}
	return result, nil
}

func orText(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}

// runningHere reports whether the service's own copy runs on this machine.
func (m *Manager) runningHere(slug string) (store.Deployment, bool) {
	dep, ok, err := m.store.GetDeployment(slug)
	return dep, err == nil && ok && dep.Status == "running"
}

// otherExits are the running proxied copies the last status read saw, with whether
// their proxy rotates.
func (m *Manager) otherExits(self int64) []proxies.Exit {
	rotating := map[int64]bool{}
	if stored, err := m.store.ListProxies(); err == nil {
		for _, p := range stored {
			if d, _, err := decodeProxy(p); err == nil && d.Rotating {
				rotating[p.ID] = true
			}
		}
	}
	var out []proxies.Exit
	for _, c := range m.ProxiedCopies() {
		if c.ProxyID == self || c.Status != "running" {
			continue
		}
		out = append(out, proxies.Exit{Slug: c.Slug, ProxyID: c.ProxyID, ExitIP: c.ExitIP, Rotating: rotating[c.ProxyID]})
	}
	return out
}

// StopProxied stops a proxied copy. A stop by hand is the owner's decision, so a
// later cap reset does not start it again.
func (m *Manager) StopProxied(ctx context.Context, slug string, id int64) error {
	mu := m.copyLock(id, slug)
	mu.Lock()
	defer mu.Unlock()
	p, err := m.proxier()
	if err != nil {
		return err
	}
	if _, err := p.StopProxied(ctx, slug, id, m.stopTimeout(slug)); err != nil {
		return err
	}
	// A flag left set would make the next reset start the copy again.
	if err := m.store.SetProxiedCopyCapped(id, slug, false); err != nil {
		return fmt.Errorf("the copy stopped, but could not be marked as stopped by you, so a cap reset could start it again: %w", err)
	}
	m.store.RecordEvent(slug, "proxied_stopped", fmt.Sprintf("proxy %d", id))
	return nil
}

// StartProxied starts a proxied copy, refused while its proxy is capped.
func (m *Manager) StartProxied(ctx context.Context, slug string, id int64) error {
	mu := m.copyLock(id, slug)
	mu.Lock()
	defer mu.Unlock()
	d, s, err := m.loadProxy(id)
	if err != nil {
		return err
	}
	if s.CappedAt != "" {
		return errors.New(orText(s.CapReason, "This proxy reached its traffic cap"))
	}
	p, err := m.proxier()
	if err != nil {
		return err
	}
	if _, err := p.StartProxied(ctx, slug, id, m.stopTimeout(slug), specOf(d), detectOwnIP(ctx)); err != nil {
		return errors.New(maskSecrets(err.Error(), d))
	}
	_ = m.store.SetProxiedCopyCapped(id, slug, false)
	m.store.RecordEvent(slug, "proxied_started", fmt.Sprintf("proxy %d", id))
	return nil
}

// RemoveProxied removes a proxied copy and its record. It holds the service's deploy
// lock, so it never lands between a deploy's copy record and the copy's start.
func (m *Manager) RemoveProxied(ctx context.Context, slug string, id int64) error {
	defer m.lockDeploy(slug)()
	return m.removeProxied(ctx, slug, id)
}

// removeProxied is RemoveProxied for a caller that already holds slug's deploy lock.
func (m *Manager) removeProxied(ctx context.Context, slug string, id int64) error {
	p, err := m.proxier()
	if err != nil {
		return err
	}
	if _, err := p.RemoveProxied(ctx, slug, id, m.stopTimeout(slug)); err != nil {
		return err
	}
	m.store.RecordEvent(slug, "proxied_removed", fmt.Sprintf("proxy %d", id))
	return m.store.DeleteProxiedCopy(id, slug)
}

// removeAllProxied removes every proxied copy of slug, when the service goes.
func (m *Manager) removeAllProxied(ctx context.Context, slug string) error {
	p, err := m.proxier()
	if err != nil {
		return nil // no Docker, no proxied copies
	}
	if _, err := p.RemoveAllProxied(ctx, slug); err != nil {
		return err
	}
	return m.store.DeleteProxiedCopiesOf(slug)
}

// ResetProxy starts a new traffic period by hand and starts the copies the cap
// stopped. It returns the copies it started.
func (m *Manager) ResetProxy(ctx context.Context, id int64) ([]string, error) {
	m.proxy.proxyMu.Lock()
	d, s, err := m.loadProxy(id)
	if err == nil {
		err = m.saveState(id, proxies.ResetState(s, proxyNow().UTC()))
	}
	m.proxy.proxyMu.Unlock()
	if err != nil {
		return nil, err
	}
	return m.releaseCapped(ctx, id, d)
}

// releaseCapped starts the copies the cap stopped. A copy the owner stopped stays
// stopped. An error means the copies could not even be listed or reached; a copy
// that failed to start is recorded as an event and keeps its flag.
func (m *Manager) releaseCapped(ctx context.Context, id int64, d proxies.Definition) ([]string, error) {
	m.proxy.lastMu.Lock()
	delete(m.proxy.unstopped, id)
	m.proxy.lastMu.Unlock()
	copies, err := m.store.ListProxiedCopies(id)
	if err != nil {
		return nil, fmt.Errorf("the cap was lifted, but its copies could not be listed to start them: %w", err)
	}
	var capped bool
	for _, c := range copies {
		capped = capped || c.StoppedByCap
	}
	if !capped {
		return nil, nil
	}
	p, err := m.proxier()
	if err != nil {
		return nil, fmt.Errorf("the cap was lifted, but its copies could not be started: %w", err)
	}
	ownIP := detectOwnIP(ctx)
	var started []string
	for _, c := range copies {
		if !c.StoppedByCap {
			continue
		}
		mu := m.copyLock(id, c.Slug)
		mu.Lock()
		_, err := p.StartProxied(ctx, c.Slug, id, m.stopTimeout(c.Slug), specOf(d), ownIP)
		if err == nil {
			_ = m.store.SetProxiedCopyCapped(id, c.Slug, false)
		}
		mu.Unlock()
		if err != nil {
			m.store.RecordEvent(c.Slug, "proxied_start_error", maskSecrets(err.Error(), d))
			continue
		}
		started = append(started, c.Slug)
	}
	return started, nil
}

// ProxiedCopies is the latest status read of the proxied copies on this machine.
func (m *Manager) ProxiedCopies() []runtime.ProxiedCopy {
	m.proxy.lastMu.Lock()
	defer m.proxy.lastMu.Unlock()
	return append([]runtime.ProxiedCopy(nil), m.proxy.last...)
}

// CapNotice is a proxy whose cap holds, for the notification list.
type CapNotice struct {
	ProxyID int64
	Name    string
	Message string
}

// CapNotices lists every proxy that is capped right now, from the store, so neither
// a restart nor a missed tick can drop a cap that still holds.
func (m *Manager) CapNotices() []CapNotice {
	stored, err := m.store.ListProxies()
	if err != nil {
		return nil
	}
	m.proxy.lastMu.Lock()
	defer m.proxy.lastMu.Unlock()
	var out []CapNotice
	for _, p := range stored {
		d, s, err := decodeProxy(p)
		if err != nil || s.CappedAt == "" {
			continue
		}
		msg := "Stopped: " + orText(s.CapReason, d.Name+" reached its traffic cap") + "."
		if still := m.proxy.unstopped[p.ID]; len(still) > 0 {
			names := make([]string, 0, len(still))
			for slug := range still {
				names = append(names, slug)
			}
			sort.Strings(names)
			msg = orText(s.CapReason, d.Name+" reached its traffic cap") + ". Could not stop yet, retrying every minute: " + strings.Join(names, ", ") + "."
		}
		out = append(out, CapNotice{ProxyID: p.ID, Name: d.Name, Message: msg})
	}
	return out
}

func copyRunning(c runtime.ProxiedCopy) bool {
	return c.Status == "running" || c.Status == "restarting" || c.TunnelStatus == "running" || c.TunnelStatus == "restarting"
}

// ProxyTick is the once-a-minute pass: read the proxied copies (which also starts an
// earner whose tunnel came back), add each tunnel's traffic to its proxy, stop the
// copies of a proxy at its cap, and start a new period where the reset day came. It
// returns the proxies that crossed their cap in this pass.
//
// While a cap holds, every pass that still sees a copy of that proxy running stops it
// again, so a copy started behind the app's back cannot keep spending the allowance.
func (m *Manager) ProxyTick(ctx context.Context) ([]CapNotice, error) {
	p, err := m.proxier()
	if err != nil {
		return nil, nil
	}
	status, err := p.ProxiedStatus(ctx)
	if err != nil {
		return nil, err
	}
	m.proxy.lastMu.Lock()
	m.proxy.last = status
	m.proxy.lastMu.Unlock()

	stored, err := m.store.ListProxies()
	if err != nil {
		return nil, err
	}
	now := proxyNow()
	var crossedNow []CapNotice
	for _, sp := range stored {
		id := sp.ID
		var readings []proxies.Traffic
		var mine []runtime.ProxiedCopy
		for _, c := range status {
			if c.ProxyID != id {
				continue
			}
			mine = append(mine, c)
			readings = append(readings, proxies.Traffic{
				TunnelID: c.TunnelContainerID, StartedAt: c.TunnelStartedAt,
				Rx: c.NetRxBytes, Tx: c.NetTxBytes, ExitIP: c.ExitIP, ExitCountry: c.ExitCountry,
			})
		}
		m.proxy.proxyMu.Lock()
		d, s, err := m.loadProxy(id)
		if err != nil {
			m.proxy.proxyMu.Unlock()
			continue
		}
		reset := proxies.ResetDue(d, s, now.UTC())
		if reset {
			s = proxies.ResetState(s, now.UTC())
		}
		next, crossed := proxies.Account(s, d, readings, now)
		saveErr := m.saveState(id, next)
		m.proxy.proxyMu.Unlock()
		if saveErr != nil {
			continue
		}
		if reset && next.CappedAt == "" {
			_, _ = m.releaseCapped(ctx, id, d)
		} else if next.CappedAt == "" {
			// A reset whose start failed leaves the copy marked as the cap's and down;
			// while the proxy stays uncapped, every pass tries again.
			m.restartCapStoppedDown(ctx, p, id, d, mine)
		}
		if next.CappedAt == "" {
			continue
		}
		targets := map[string]bool{}
		for _, c := range mine {
			if copyRunning(c) {
				targets[c.Slug] = true
			}
		}
		if crossed {
			// Every recorded copy is asked to stop. Only those that were running are
			// marked as stopped by the cap (see stopCapped), so one the owner stopped
			// stays stopped.
			if records, err := m.store.ListProxiedCopies(id); err == nil {
				for _, r := range records {
					targets[r.Slug] = true
				}
			}
		}
		m.stopCapped(ctx, p, id, d, targets)
		if crossed {
			crossedNow = append(crossedNow, CapNotice{ProxyID: id, Name: d.Name, Message: "Stopped: " + next.CapReason + "."})
		}
	}
	return crossedNow, nil
}

// restartCapStoppedDown starts this proxy's copies that are still marked as stopped
// by the cap and are not running. A copy the owner stopped is never marked, so it
// stays stopped; one that is running is left alone.
func (m *Manager) restartCapStoppedDown(ctx context.Context, p runtime.ProxyProvider, id int64, d proxies.Definition, status []runtime.ProxiedCopy) {
	records, err := m.store.ListProxiedCopies(id)
	if err != nil {
		return
	}
	// Only a running earner counts: a tunnel left up by a start that timed out is not
	// a copy that runs, so its mark stays and the next pass tries again.
	running := map[string]bool{}
	for _, c := range status {
		running[c.Slug] = c.Status == "running" || c.Status == "restarting"
	}
	var ownIP string
	detected := false // one lookup per pass, even when it fails
	for _, r := range records {
		if !r.StoppedByCap {
			continue
		}
		if running[r.Slug] {
			// Started, but clearing its mark failed: clear it now, or a later crash
			// of the copy would be read as the cap's stop.
			if err := m.store.SetProxiedCopyCapped(id, r.Slug, false); err != nil {
				m.store.RecordEvent(r.Slug, "proxied_mark_error", err.Error())
			}
			continue
		}
		if !detected {
			ownIP, detected = detectOwnIP(ctx), true
		}
		m.retryCapStart(ctx, p, id, r.Slug, ownIP)
	}
}

// copyLock is the mutex of one copy; see proxyState.copyMu.
func (m *Manager) copyLock(id int64, slug string) *sync.Mutex {
	mu, _ := m.proxy.copyMu.LoadOrStore(fmt.Sprintf("%d/%s", id, slug), &sync.Mutex{})
	return mu.(*sync.Mutex)
}

// retryCapStart starts one cap-stopped copy, unless an owner's Start or Stop, or a
// reset, got to it first: the mark is read again under the copy's lock, which those
// hold for their whole run. It reports whether the copy was started.
func (m *Manager) retryCapStart(ctx context.Context, p runtime.ProxyProvider, id int64, slug, ownIP string) bool {
	mu := m.copyLock(id, slug)
	mu.Lock()
	defer mu.Unlock()
	// The proxy as stored now: an edit while the retry waited reaches the copy, and a
	// cap that came back stops the retry.
	d, s, err := m.loadProxy(id)
	if err != nil || s.CappedAt != "" {
		return false
	}
	records, err := m.store.ListProxiedCopies(id)
	if err != nil {
		return false
	}
	marked := false
	for _, r := range records {
		marked = marked || (r.Slug == slug && r.StoppedByCap)
	}
	if !marked {
		return false
	}
	if _, err := p.StartProxied(ctx, slug, id, m.stopTimeout(slug), specOf(d), ownIP); err != nil {
		// Not an event: the reset or stop that left the copy marked recorded the first
		// failure, retries run every minute, and events feed the health score.
		return false
	}
	// Cleared inside the copy's lock, which stopCapped also takes, and only while the
	// cap is still lifted: a cap that came back during the start keeps the mark, so
	// the next stop and reset still treat the copy as the cap's.
	m.proxy.proxyMu.Lock()
	_, now, err := m.loadProxy(id)
	m.proxy.proxyMu.Unlock()
	if err == nil && now.CappedAt == "" {
		if err := m.store.SetProxiedCopyCapped(id, slug, false); err != nil {
			// The next pass sees it running and marked, and clears it then.
			m.store.RecordEvent(slug, "proxied_mark_error", err.Error())
		}
	}
	return true
}

func (m *Manager) stopCapped(ctx context.Context, p runtime.ProxyProvider, id int64, d proxies.Definition, targets map[string]bool) {
	slugs := make([]string, 0, len(targets))
	for slug := range targets {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	for _, slug := range slugs {
		m.stopCappedCopy(ctx, p, id, d, slug)
	}
}

// stopCappedCopy stops one copy at the cap under that copy's lock, so a retry's
// start and mark clear never interleave with the stop and its mark.
func (m *Manager) stopCappedCopy(ctx context.Context, p runtime.ProxyProvider, id int64, d proxies.Definition, slug string) {
	mu := m.copyLock(id, slug)
	mu.Lock()
	defer mu.Unlock()
	{
		wasRunning, err := p.StopProxied(ctx, slug, id, m.stopTimeout(slug))
		m.proxy.lastMu.Lock()
		if m.proxy.unstopped == nil {
			m.proxy.unstopped = map[int64]map[string]string{}
		}
		if m.proxy.unstopped[id] == nil {
			m.proxy.unstopped[id] = map[string]string{}
		}
		var pe *runtime.ProxyError
		switch {
		case err == nil, errors.As(err, &pe) && strings.HasPrefix(pe.Error(), "No proxied copy"):
			delete(m.proxy.unstopped[id], slug)
		default:
			m.proxy.unstopped[id][slug] = truncateText(maskSecrets(err.Error(), d), 200)
		}
		m.proxy.lastMu.Unlock()
		if err != nil || !wasRunning {
			return
		}
		// A reset or a cap change may have lifted the cap while the stop ran; then
		// the copy goes back up instead of being marked.
		m.proxy.proxyMu.Lock()
		fresh, s, loadErr := m.loadProxy(id)
		stillCapped := loadErr == nil && s.CappedAt != ""
		if stillCapped {
			_ = m.store.SetProxiedCopyCapped(id, slug, true)
		}
		m.proxy.proxyMu.Unlock()
		if stillCapped {
			m.store.RecordEvent(slug, "proxied_capped", fmt.Sprintf("proxy %d", id))
		} else if loadErr == nil {
			if _, err := p.StartProxied(ctx, slug, id, m.stopTimeout(slug), specOf(fresh), detectOwnIP(ctx)); err != nil {
				m.store.RecordEvent(slug, "proxied_start_error", maskSecrets(err.Error(), fresh))
				// Marked, so the next pass starts it; unmarked it would stay stopped.
				_ = m.store.SetProxiedCopyCapped(id, slug, true)
			}
		}
	}
}
