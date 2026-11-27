package macvm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/i18n"
	"github.com/GeiserX/CashPilot-Desktop/internal/securevm"
	"github.com/shirou/gopsutil/v4/host"
)

// AppSpec is one earner the Security page lists, as the app hands it over.
type AppSpec struct {
	Slug string
	Name string
	// Refused is the catalog's guest.refused, or "terms" for a service whose
	// terms forbid containers.
	Refused string
	MacOS   *catalog.GuestMacOS
	// OnHost is whether CashPilot runs it on this computer now.
	OnHost bool
}

// Engine is the macOS Secure VM as the app sees it. It runs as the user; the
// root side is the daemon, reached through request files and the admin step.
type Engine struct {
	P        Paths
	R        Runner
	TartHome string
	Tools    Tools
	Client   *http.Client
	// Exe is this app's executable: the admin step runs it as root.
	Exe        string
	AppVersion string
	// Apps lists the earners the page shows.
	Apps func() []AppSpec

	mu       sync.Mutex
	busy     string
	step     string
	fraction float64
	lastErr  string
	apps     map[string]AppState
	appsAt   time.Time
}

// NewEngine is the product engine for the user running the app.
func NewEngine(appVersion string, apps func() []AppSpec) (*Engine, error) {
	u, err := user.Current()
	if err != nil {
		return nil, err
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return nil, err
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return nil, err
	}
	return &Engine{
		P: ProductPaths(), R: Exec{}, TartHome: TartHome(u.HomeDir),
		Tools:  ToolsAt(filepath.Join(cache, "CashPilot", "securevm")),
		Client: &http.Client{Timeout: 0}, Exe: exe, AppVersion: appVersion, Apps: apps,
	}, nil
}

var _ securevm.Engine = (*Engine)(nil)

func (e *Engine) guest() Guest { return Guest{Tart: TartExe(e.P.Bin), TartHome: e.TartHome} }

// begin marks an action as running; only one runs at a time.
func (e *Engine) begin(action string) (securevm.Progress, func(error) error, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.busy != "" {
		return nil, nil, errors.New(i18n.T("go.macvm.busy", "action", e.busy))
	}
	e.busy, e.step, e.fraction, e.lastErr = action, "", -1, ""
	progress := func(step string, fraction float64) {
		e.mu.Lock()
		e.step, e.fraction = step, fraction
		e.mu.Unlock()
	}
	end := func(err error) error {
		e.mu.Lock()
		e.busy, e.step, e.fraction = "", "", -1
		if err != nil {
			e.lastErr = err.Error()
		}
		e.appsAt = time.Time{}
		e.mu.Unlock()
		return err
	}
	return progress, end, nil
}

// tee sends progress to both the engine's own state and the caller's.
func tee(a, b securevm.Progress) securevm.Progress {
	return func(step string, f float64) {
		a(step, f)
		if b != nil {
			b(step, f)
		}
	}
}

func (e *Engine) installed() (Install, bool) {
	var inst Install
	if err := ReadJSON(filepath.Join(e.P.State, InstallFile), &inst); err != nil {
		return inst, false
	}
	return inst, true
}

func (e *Engine) vmExists() bool {
	_, err := os.Stat(filepath.Join(VMDir(e.TartHome), "config.json"))
	return err == nil
}

// Status reads the daemon's files and the guest's apps. It changes nothing.
func (e *Engine) Status(ctx context.Context) securevm.Status {
	st := securevm.Status{
		Platform: "macos", Network: NetAbsent, Fence: FenceUnproven, Fraction: -1,
		Cost: securevm.Cost{
			MemoryGB: float64(GuestMemoryMB) / 1024, Cores: GuestCPUs, DiskGB: GuestDiskGB,
			DownloadGB: float64(IPSWPin.Size) / 1e9,
		},
	}
	e.mu.Lock()
	st.Busy, st.Step, st.Fraction, st.LastError = e.busy, e.step, e.fraction, e.lastErr
	e.mu.Unlock()

	st.Available = true
	for _, c := range Preflight(ctx, e.R, e.TartHome, false) {
		if (c.ID == "apple-silicon" || c.ID == "macos") && c.Problem != "" {
			st.Available, st.Unavailable = false, c.Problem
		}
	}
	inst, installed := e.installed()
	st.Installed = installed
	st.UpdateNeeded = installed && e.AppVersion != "" && inst.App != e.AppVersion
	if installed && e.vmExists() {
		if _, err := os.Stat(PreparedFile(e.TartHome)); err != nil {
			st.Unfinished = true
		}
	}
	if st.Installed {
		if s, err := ReadStatus(e.P); err == nil {
			st.Network, st.Fence, st.Reason, st.Changes = s.Network, s.Fence, s.Reason, s.Changes
			st.ProvedAt, st.Proving, st.StartError, st.GuestLimit = s.ProvedAt, s.Proving, s.StartError, s.GuestLimit
			st.FreeDisk, st.Blocked, st.BlockedSince = s.FreeDisk, s.Blocked, s.BlockedSince
			if age := time.Since(s.WrittenAt); age > 3*GuardInterval && st.Busy == "" {
				st.Reason = i18n.T("go.macvm.guardStale", "age", age.Round(time.Minute).String())
			}
			if boot, err := host.BootTimeWithContext(ctx); err == nil {
				fromBeforeBoot(&st, s.WrittenAt, time.Unix(int64(boot), 0))
			}
		} else {
			st.Reason = i18n.T("go.macvm.guardSilent")
		}
		if st.GuestLimit {
			st.StartError = i18n.T("go.macvm.guestLimit")
		}
		var pr securevm.Proof
		if ReadJSON(filepath.Join(e.P.State, ProofFile), &pr) == nil {
			st.Proof = &pr
		}
	}
	if st.FreeDisk == 0 {
		st.FreeDisk, _ = FreeBytes(existingParent(e.TartHome))
	}
	st.LowDisk = st.FreeDisk > 0 && st.FreeDisk < LowDisk
	st.Apps = e.appList(ctx, st)
	return st
}

// fromBeforeBoot drops a status that the guard wrote before this boot of the
// Mac. Until the guard's first pass after the boot, the file still says what
// it saw before the shutdown, for example "connected" while the VM is stopped.
func fromBeforeBoot(st *securevm.Status, written, boot time.Time) {
	if !written.Before(boot) {
		return
	}
	st.Network, st.Reason = NetOff, i18n.T("go.macvm.guardBooting")
	st.Changes, st.Blocked, st.BlockedSince = nil, nil, time.Time{}
	st.StartError, st.GuestLimit = "", false
}

// appList maps the specs and the guest's read into the page's rows. The guest
// is read at most every 30 s, and only while the VM runs.
func (e *Engine) appList(ctx context.Context, st securevm.Status) []securevm.App {
	if e.Apps == nil {
		return nil
	}
	specs := e.Apps()
	running := st.Network == NetConnected || st.Network == NetDisconnected
	e.mu.Lock()
	fresh := time.Since(e.appsAt) < 30*time.Second
	states := e.apps
	e.mu.Unlock()
	if running && !fresh && st.Busy == "" {
		guestApps := map[string]catalog.GuestMacOS{}
		for _, s := range specs {
			if s.MacOS != nil {
				guestApps[s.Slug] = *s.MacOS
			}
		}
		if len(guestApps) > 0 {
			c, cancel := context.WithTimeout(ctx, 20*time.Second)
			got, err := e.guest().ReadApps(c, guestApps)
			cancel()
			if err == nil {
				states = got
			}
			e.mu.Lock()
			e.apps, e.appsAt = states, time.Now()
			e.mu.Unlock()
		}
	}
	if !running {
		states = nil
	}
	var out []securevm.App
	for _, s := range specs {
		a := securevm.App{Slug: s.Slug, Name: s.Name, Installed: securevm.Unknown, Running: securevm.Unknown, SignedIn: securevm.Unknown}
		a.CanVM, a.Locked = securevm.Lock(s.Refused, s.MacOS != nil)
		if s.MacOS != nil {
			a.Note = s.MacOS.Note
		}
		if g, ok := states[s.Slug]; ok {
			a.Installed, a.Running, a.SignedIn = g.Installed, g.Running, g.SignedIn
		}
		switch {
		case a.Installed == securevm.Yes:
			a.Placement = securevm.VM
			a.Layers = securevm.GuestLayers()
			if st.Network != NetConnected || st.Fence != FenceHeld {
				a.Detail = i18n.T("go.macvm.appCutOff")
			} else if a.Running == securevm.No {
				a.Detail = i18n.T("go.macvm.appNotRunning")
			} else if a.SignedIn == securevm.No {
				a.Detail = i18n.T("go.macvm.appSignedOut")
			}
		case s.OnHost:
			a.Placement = securevm.Host
		}
		out = append(out, a)
	}
	return out
}

// adminStep runs this app's root role through macOS's administrator prompt.
// It returns what the role printed.
func (e *Engine) adminStep(ctx context.Context, prompt string, args ...string) (string, error) {
	if strings.ContainsAny(e.Exe, "\"\\") {
		return "", errors.New(i18n.T("go.macvm.badExePath"))
	}
	q := func(s string) string {
		return `quoted form of "` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
	}
	parts := []string{q(e.Exe), q("--" + Flag)}
	for _, a := range args {
		parts = append(parts, q(a))
	}
	script := "do shell script (" + strings.Join(parts, ` & " " & `) + `) with prompt "` +
		strings.ReplaceAll(prompt, `"`, `'`) + `" with administrator privileges`
	cmd := exec.CommandContext(ctx, "/usr/bin/osascript", "-e", script)
	var errb strings.Builder
	cmd.Stderr = &errb
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(errb.String())
		if strings.Contains(msg, "-128") {
			return string(out), errors.New(i18n.T("go.macvm.adminCancelled"))
		}
		return string(out), errors.New(i18n.T("go.macvm.adminFailed", "detail", msg))
	}
	return string(out), nil
}

// Install makes the Secure VM: downloads the tools and macOS, creates the VM,
// runs the one admin step, waits for the fence proof and prepares the earner
// account. Each step resumes where an earlier, interrupted Install stopped.
func (e *Engine) Install(ctx context.Context, progress securevm.Progress) (err error) {
	p, end, err := e.begin("install")
	if err != nil {
		return err
	}
	defer func() { err = end(err) }()
	progress = tee(p, progress)

	progress(i18n.T("go.macvm.stepCheck"), -1)
	if probs := Problems(Preflight(ctx, e.R, e.TartHome, !e.vmExists())); len(probs) > 0 {
		return errors.New(strings.Join(probs, " "))
	}
	if err := os.MkdirAll(e.Tools.Dir, 0o700); err != nil {
		return err
	}
	if err := FetchTools(ctx, e.Client, e.Tools, func(s string, f float64) { progress(i18n.T("go.macvm.stepTools"), f) }); err != nil {
		return err
	}
	if !e.vmExists() {
		ipsw := IPSWPath(e.TartHome)
		if err := os.MkdirAll(filepath.Dir(ipsw), 0o755); err != nil {
			return err
		}
		if err := Download(ctx, e.Client, IPSWPin, ipsw, func(s string, f float64) { progress(i18n.T("go.macvm.stepIPSW"), f) }); err != nil {
			return err
		}
		if err := CreateVM(ctx, e.Tools.Tart, e.TartHome, ipsw, func(s string, f float64) { progress(i18n.T("go.macvm.stepCreate"), f) }); err != nil {
			return err
		}
		// The VM holds its own copy; the image only takes space now.
		_ = os.Remove(ipsw)
	}

	// A root side from this version whose fence holds needs no second
	// approval: Install only finishes the guest.
	if !e.rootCurrent() {
		progress(i18n.T("go.macvm.stepApproval"), -1)
		u, err := user.Current()
		if err != nil {
			return err
		}
		since := time.Now()
		if _, err := e.adminStep(ctx, i18n.T("go.macvm.promptInstall"),
			"install", "--owner", u.Username, "--tart-home", e.TartHome, "--tools", e.Tools.Dir, "--app-version", e.AppVersion); err != nil {
			return err
		}

		progress(i18n.T("go.macvm.stepProve"), -1)
		pr, err := e.waitProof(ctx, since, 30*time.Minute)
		if err != nil {
			return err
		}
		if !pr.Green {
			return errors.New(i18n.T("go.macvm.proofRed", "why", pr.Why))
		}
	}
	if err := e.prepareGuest(ctx, progress); err != nil {
		return err
	}
	return os.WriteFile(PreparedFile(e.TartHome), nil, 0o600)
}

// rootCurrent is whether the admin step already ran for this version and the
// guard holds the fence on a connected VM.
func (e *Engine) rootCurrent() bool {
	inst, ok := e.installed()
	if !ok || e.AppVersion == "" || inst.App != e.AppVersion {
		return false
	}
	s, err := ReadStatus(e.P)
	return err == nil && s.Network == NetConnected && s.Fence == FenceHeld
}

// prepareGuest makes the earner account and restarts the VM so it logs in.
func (e *Engine) prepareGuest(ctx context.Context, progress securevm.Progress) error {
	progress(i18n.T("go.macvm.stepPrepare"), -1)
	g := e.guest()
	if err := e.waitAgent(ctx, g); err != nil {
		return err
	}
	fresh, err := g.Provision(ctx)
	if err != nil {
		return err
	}
	if !fresh {
		return nil
	}
	progress(i18n.T("go.macvm.stepRestart"), -1)
	// `tart stop` forces the guest off when it does not shut down in time, so
	// flush its disk first.
	c, cancel := context.WithTimeout(ctx, time.Minute)
	_, _ = g.Exec(c, "", "/bin/sync")
	cancel()
	since := time.Now()
	if err := e.request(RequestRestart); err != nil {
		return err
	}
	// The restart stops the guest first, so wait for a status written after
	// the request with the VM held and connected again.
	deadline := time.Now().Add(10 * time.Minute)
	for {
		if s, err := ReadStatus(e.P); err == nil && s.WrittenAt.After(since.Add(5*time.Second)) &&
			s.Network == NetConnected && s.Fence == FenceHeld {
			break
		}
		if time.Now().After(deadline) {
			return errors.New(i18n.T("go.macvm.noComeback"))
		}
		if err := sleep(ctx, 5*time.Second); err != nil {
			return err
		}
	}
	return e.waitAgent(ctx, g)
}

func (e *Engine) waitAgent(ctx context.Context, g Guest) error {
	deadline := time.Now().Add(agentWait)
	for !g.Ready(ctx) {
		if time.Now().After(deadline) {
			return errors.New(i18n.T("go.macvm.noAgent"))
		}
		if err := sleep(ctx, 5*time.Second); err != nil {
			return err
		}
	}
	return nil
}

// request drops a request for the daemon.
func (e *Engine) request(name string) error {
	if !IsRequest(name) {
		return fmt.Errorf("unknown request %q", name)
	}
	if err := os.WriteFile(filepath.Join(e.P.Request, name), nil, 0o600); err != nil {
		return fmt.Errorf("%s: %w", i18n.T("go.macvm.noGuard"), err)
	}
	return nil
}

// waitProof waits for a proof finished after since.
func (e *Engine) waitProof(ctx context.Context, since time.Time, limit time.Duration) (securevm.Proof, error) {
	deadline := time.Now().Add(limit)
	for {
		var pr securevm.Proof
		if ReadJSON(filepath.Join(e.P.State, ProofFile), &pr) == nil && pr.At.After(since) {
			if s, err := ReadStatus(e.P); err == nil && !s.Proving {
				return pr, nil
			}
		}
		if s, err := ReadStatus(e.P); err == nil && s.StartError != "" && s.WrittenAt.After(since) && !s.Proving {
			if s.GuestLimit {
				return pr, errors.New(i18n.T("go.macvm.guestLimit"))
			}
			return pr, errors.New(i18n.T("go.macvm.noStart", "detail", s.StartError))
		}
		if time.Now().After(deadline) {
			return pr, errors.New(i18n.T("go.macvm.proofTimeout"))
		}
		if err := sleep(ctx, 3*time.Second); err != nil {
			return pr, err
		}
	}
}

// Verify runs the fence proof again and returns it.
func (e *Engine) Verify(ctx context.Context, progress securevm.Progress) (pr securevm.Proof, err error) {
	p, end, err := e.begin("verify")
	if err != nil {
		return pr, err
	}
	defer func() { err = end(err) }()
	progress = tee(p, progress)
	if _, ok := e.installed(); !ok {
		return pr, errors.New(i18n.T("go.macvm.notInstalled"))
	}
	progress(i18n.T("go.macvm.stepProve"), -1)
	since := time.Now()
	if err := e.request(RequestProve); err != nil {
		return pr, err
	}
	return e.waitProof(ctx, since, 30*time.Minute)
}

// Undo removes everything Install made, then checks for itself what is left.
func (e *Engine) Undo(ctx context.Context, progress securevm.Progress) (res securevm.RemoveResult, err error) {
	p, end, err := e.begin("undo")
	if err != nil {
		return res, err
	}
	defer func() { err = end(err) }()
	progress = tee(p, progress)
	progress(i18n.T("go.macvm.stepApproval"), -1)
	u, err := user.Current()
	if err != nil {
		return res, err
	}
	out, aerr := e.adminStep(ctx, i18n.T("go.macvm.promptRemove"),
		"remove", "--owner", u.Username, "--tart-home", e.TartHome, "--tools", e.Tools.Dir)
	var rep RemoveReport
	if jerr := json.Unmarshal([]byte(strings.TrimSpace(out)), &rep); jerr != nil {
		if aerr != nil {
			return res, aerr
		}
		return res, fmt.Errorf("%s: %w", i18n.T("go.macvm.noReport"), jerr)
	}
	progress(i18n.T("go.macvm.stepLeft"), -1)
	res = securevm.RemoveResult{Removed: rep.Removed, Kept: rep.Kept, Left: rep.Left}
	for _, l := range e.leftAsUser(ctx) {
		if !contains(res.Left, l) {
			res.Left = append(res.Left, l)
		}
	}
	if len(res.Left) > 0 {
		return res, errors.New(i18n.T("go.macvm.leftBehind", "n", len(res.Left)))
	}
	return res, nil
}

// leftAsUser is the check the app runs itself after Undo: every file,
// process and launchd job it can see without root.
func (e *Engine) leftAsUser(ctx context.Context) []string {
	var left []string
	for _, p := range []string{e.P.Root, e.P.Plist, VMDir(e.TartHome), IPSWPath(e.TartHome), e.Tools.Dir} {
		if _, err := os.Lstat(p); err == nil {
			left = append(left, p)
		}
	}
	if _, err := e.R.Run(ctx, "/bin/launchctl", "print", "system/"+Label); err == nil {
		left = append(left, "the daemon "+Label+" is still loaded")
	}
	if ps, err := e.R.Run(ctx, "/bin/ps", "-axww", "-o", "args="); err == nil {
		for _, l := range strings.Split(ps, "\n") {
			if strings.Contains(l, "tart run "+VMName) || strings.Contains(l, e.P.Bin+"/") {
				left = append(left, "a process: "+strings.TrimSpace(l))
			}
		}
	}
	return left
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func (e *Engine) spec(slug string) (AppSpec, error) {
	if e.Apps != nil {
		for _, s := range e.Apps() {
			if s.Slug == slug {
				return s, nil
			}
		}
	}
	return AppSpec{}, fmt.Errorf("unknown app %q", slug)
}

// Place puts an earner in the VM or takes it out. The app moves the host
// side; this is the guest side only.
func (e *Engine) Place(ctx context.Context, slug string, where securevm.Placement, progress securevm.Progress) (err error) {
	s, err := e.spec(slug)
	if err != nil {
		return err
	}
	p, end, err := e.begin("place")
	if err != nil {
		return err
	}
	defer func() { err = end(err) }()
	progress = tee(p, progress)
	if where == securevm.Host {
		if _, ok := e.installed(); !ok || s.MacOS == nil {
			return nil // nothing of it is in a VM
		}
		progress(i18n.T("go.macvm.stepTakeOut", "app", s.Name), -1)
		return e.guest().RemoveApp(ctx, slug, *s.MacOS)
	}
	if ok, why := securevm.Lock(s.Refused, s.MacOS != nil); !ok {
		return errors.New(why)
	}
	st, err := ReadStatus(e.P)
	if err != nil || st.Network != NetConnected || st.Fence != FenceHeld {
		return errors.New(i18n.T("go.macvm.notHeld"))
	}
	g := e.guest()
	if s.MacOS.Rosetta && g.HasRosetta(ctx) != securevm.Yes {
		progress(i18n.T("go.macvm.stepRosetta"), -1)
		if err := g.Rosetta(ctx); err != nil {
			return err
		}
	}
	progress(i18n.T("go.macvm.stepInstallApp", "app", s.Name), -1)
	return g.InstallApp(ctx, slug, *s.MacOS)
}

// OpenScreen opens the VM's screen in Screen Sharing, with the earner's app
// in front when slug names one.
func (e *Engine) OpenScreen(ctx context.Context, slug string) error {
	if slug != "" {
		s, err := e.spec(slug)
		if err != nil {
			return err
		}
		if s.MacOS != nil {
			_ = e.guest().Front(ctx, *s.MacOS)
		}
	}
	b, err := os.ReadFile(filepath.Join(e.P.Owner, VNCFile))
	if err != nil {
		return errors.New(i18n.T("go.macvm.noScreen"))
	}
	u := strings.TrimSpace(string(b))
	if !vncURL.MatchString(u) {
		return errors.New(i18n.T("go.macvm.badScreen"))
	}
	_, err = e.R.Run(ctx, "/usr/bin/open", u)
	return err
}

// Start asks the guard to start the VM with the fence.
func (e *Engine) Start(ctx context.Context) error {
	if _, ok := e.installed(); !ok {
		return errors.New(i18n.T("go.macvm.notInstalled"))
	}
	return e.request(RequestStart)
}

// Stop stops the VM. The guard starts it again when the Mac starts.
func (e *Engine) Stop(ctx context.Context) error {
	if _, ok := e.installed(); !ok {
		return nil
	}
	cmd := exec.CommandContext(ctx, TartExe(e.P.Bin), "stop", VMName)
	cmd.Env = append(os.Environ(), "TART_HOME="+e.TartHome)
	if out, err := cmd.CombinedOutput(); err != nil {
		return errors.New(i18n.T("go.macvm.noStop", "detail", strings.TrimSpace(string(out))))
	}
	return nil
}
