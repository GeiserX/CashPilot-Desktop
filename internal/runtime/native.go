package runtime

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/shirou/gopsutil/v4/process"
)

// NativeRuntimeKind is the runtime-kind key the NativeProcessProvider registers
// under in the services manager. A service that declares a native binary for the
// host is routed onto this runtime; image-only services stay on Docker.
const NativeRuntimeKind = "native"

const (
	// nativeLogCap bounds ONE generation of a service's native log file. The writer
	// keeps at most the current generation plus one rotated ".1" generation, so a
	// runaway process can use at most ~2*nativeLogCap on disk. This mirrors the
	// Docker path's maxLogBytes/maxPullLogLine hardening (bound what a service can
	// force us to store) at 1 MiB per generation.
	nativeLogCap = 1 << 20

	// maxDownloadBytes caps a single native-binary artifact download so a hostile or
	// misconfigured URL cannot exhaust memory/disk. Real headless earners are tens of
	// MB; 512 MiB is generous while still bounded.
	maxDownloadBytes = 512 << 20

	// maxExtractedBytes bounds the total bytes written while extracting one archive,
	// guarding against a decompression bomb inflating far past the (already-bounded)
	// compressed download.
	maxExtractedBytes = 1 << 30

	// defaultNativeStopTimeout is how long Stop waits after a graceful SIGTERM before
	// escalating to SIGKILL.
	defaultNativeStopTimeout = 15 * time.Second
)

// errStopRequested is returned by startOnce when a Stop raced the (re)launch, so the
// supervisor exits instead of resurrecting a process the caller just asked to stop.
var errStopRequested = errors.New("native process stop requested")

// NativeProcessProvider runs catalog services as supervised native child processes,
// with no container runtime. It downloads each service's SHA-256-pinned binary over
// HTTPS, verifies it (fail-closed on mismatch or a missing/invalid checksum),
// extracts it under <appDir>/native/<slug>/, and launches it with an argv built from
// the service's native.command via the same shell-safe tokenizeCommand/substitute
// path Docker uses. stdout+stderr are captured to a bounded rotating log; a
// supervisor goroutine restarts a crashed process with bounded backoff (the native
// equivalent of Docker's restart policy). A JSON registry at
// <appDir>/native/state.json records each managed process (pid, binary, args, source
// url/sha, startedAt, desired state) so List/Start/Stop/Remove survive an app restart.
//
// It implements runtime.Provider. ContainerInfo fields are repurposed for a process:
// ContainerID is the pid, Image is the source binary URL, Status is running/stopped,
// and CPU/memory come from gopsutil best-effort.
type NativeProcessProvider struct {
	baseDir   string
	statePath string

	// httpClient downloads binaries; overridable in tests. The HTTPS scheme is
	// enforced before any request regardless of the client.
	httpClient *http.Client

	// goos/goarch select the per-OS/arch binary; default to the build's
	// runtime.GOOS/GOARCH, overridable so tests can exercise selection deterministically.
	goos   string
	goarch string

	// Supervisor tuning. backoffMin/backoffMax bound the crash-restart backoff;
	// maxRestarts caps respawns (0 = unlimited, the production default) so tests can
	// assert bounded behavior. stopTimeout is the graceful-shutdown grace period.
	backoffMin  time.Duration
	backoffMax  time.Duration
	maxRestarts int
	stopTimeout time.Duration

	// statFn samples a pid's CPU%/memory and liveness; defaults to gopsutil, a seam
	// for tests. logCap bounds each log generation.
	statFn func(pid int) (cpuPercent, memoryMB float64, alive bool)
	logCap int64

	mu    sync.Mutex // guards procs
	procs map[string]*managedProcess

	regMu sync.Mutex // guards the on-disk registry (state.json)
}

// NewNativeProcessProvider builds a provider rooted at <appDir>/native with
// production defaults.
func NewNativeProcessProvider(appDir string) *NativeProcessProvider {
	base := filepath.Join(appDir, "native")
	return &NativeProcessProvider{
		baseDir:     base,
		statePath:   filepath.Join(base, "state.json"),
		httpClient:  &http.Client{Timeout: 10 * time.Minute},
		goos:        goruntime.GOOS,
		goarch:      goruntime.GOARCH,
		backoffMin:  1 * time.Second,
		backoffMax:  1 * time.Minute,
		maxRestarts: 0,
		stopTimeout: defaultNativeStopTimeout,
		statFn:      gopsutilStats,
		logCap:      nativeLogCap,
		procs:       make(map[string]*managedProcess),
	}
}

// managedProcess is a native child process the provider owns and supervises within
// the current app session.
type managedProcess struct {
	slug    string
	binPath string
	args    []string
	env     []string // service env only (host env is appended at exec time)
	res     catalog.ResourceLimits
	log     *rotatingLogWriter

	mu       sync.Mutex // guards cmd/stopping
	cmd      *exec.Cmd
	stopping bool

	stopOnce sync.Once
	stopCh   chan struct{} // closed on deliberate stop (wakes the backoff sleep)
	doneCh   chan struct{} // closed when the supervisor goroutine exits
}

func (mp *managedProcess) requestStop() { mp.stopOnce.Do(func() { close(mp.stopCh) }) }

func (mp *managedProcess) isStopping() bool {
	select {
	case <-mp.stopCh:
		return true
	default:
		return false
	}
}

func (mp *managedProcess) getCmd() *exec.Cmd {
	mp.mu.Lock()
	defer mp.mu.Unlock()
	return mp.cmd
}

func (mp *managedProcess) setCmd(c *exec.Cmd) {
	mp.mu.Lock()
	mp.cmd = c
	mp.mu.Unlock()
}

// beginStop atomically marks the process as stopping and returns the currently
// running cmd (if any). Because it and startOnce share mp.mu, a Stop can never miss a
// process the supervisor is in the middle of launching: either beginStop wins (and
// startOnce then returns errStopRequested) or startOnce wins (and beginStop returns
// the freshly-started cmd to signal). This closes the respawn race.
func (mp *managedProcess) beginStop() *exec.Cmd {
	mp.mu.Lock()
	defer mp.mu.Unlock()
	mp.stopping = true
	return mp.cmd
}

// Status reports the native runtime as always available: unlike Docker it has no
// external dependency (no daemon/socket), so it can always run a native process.
func (p *NativeProcessProvider) Status(ctx context.Context) Status {
	return Status{
		Available: true,
		Kind:      NativeRuntimeKind,
		Message:   "Native process runtime is ready — runs earners as supervised native processes, no container runtime required.",
		Tools:     map[string]string{},
	}
}

// Deploy downloads+verifies+extracts the service's native binary for this host and
// launches it as a supervised process. It mirrors DockerProvider.Deploy's shape
// (progress callbacks, redeploy replaces a prior instance) but targets a native
// process instead of a container.
func (p *NativeProcessProvider) Deploy(ctx context.Context, spec DeploySpec, progress func(string)) (ContainerInfo, error) {
	svc := spec.Service
	bin, ok := svc.NativeBinaryFor(p.goos, p.goarch)
	if !ok {
		return ContainerInfo{}, fmt.Errorf("%s has no native binary for %s/%s", svc.Name, p.goos, p.goarch)
	}

	slugDir := filepath.Join(p.baseDir, spec.Slug)
	binPath, err := p.ensureBinary(ctx, slugDir, bin, progress)
	if err != nil {
		return ContainerInfo{}, err
	}

	env := buildNativeEnv(svc, spec.Env)
	args := buildCommandArgs(svc.Native.Command, env)

	// Replace any prior instance (a managed child from this session or an orphan pid
	// from a previous one) before starting the new one, so redeploy never doubles up.
	p.stopInternal(spec.Slug, false)

	if progress != nil {
		progress("Starting native process")
	}
	mp, err := p.launchProcess(spec.Slug, binPath, args, envSlice(env), svc.Docker.Resources, registryDesc{URL: bin.URL, SHA256: bin.SHA256, Archive: bin.Archive})
	if err != nil {
		return ContainerInfo{}, err
	}

	pid := 0
	if cmd := mp.getCmd(); cmd != nil && cmd.Process != nil {
		pid = cmd.Process.Pid
	}
	return ContainerInfo{
		Slug:        spec.Slug,
		ContainerID: strconv.Itoa(pid),
		Name:        containerName(spec.Slug),
		Image:       bin.URL,
		Status:      "running",
	}, nil
}

// Start (re)launches a previously-deployed service from the registry. It is a no-op
// if the service is already running (as our child or as a live orphan pid).
func (p *NativeProcessProvider) Start(ctx context.Context, slug string) error {
	p.mu.Lock()
	mp := p.procs[slug]
	p.mu.Unlock()
	if mp != nil && p.procAlive(mp) {
		return nil
	}

	e, ok := p.readRegistry().Entries[slug]
	if !ok {
		return fmt.Errorf("native service %q is not deployed", slug)
	}
	if e.BinPath == "" || !fileExists(e.BinPath) {
		return fmt.Errorf("native binary for %q is missing; redeploy it", slug)
	}
	if e.PID > 0 {
		if _, _, alive := p.statFn(e.PID); alive {
			return nil
		}
	}
	_, err := p.launchProcess(slug, e.BinPath, e.Args, e.Env, catalog.ResourceLimits{}, registryDesc{URL: e.URL, SHA256: e.SHA256, Archive: e.Archive})
	return err
}

// Stop stops the service: a managed child is signalled gracefully then killed after
// the grace period; an orphan pid recorded by a previous session is signalled by pid.
// It marks the registry entry as desired=stopped so List reports it stopped.
func (p *NativeProcessProvider) Stop(ctx context.Context, slug string) error {
	if p.stopManaged(slug) {
		return p.mutateRegistry(func(reg *nativeRegistry) { markStopped(reg, slug) })
	}
	reg := p.readRegistry()
	e, ok := reg.Entries[slug]
	if !ok {
		return fmt.Errorf("native service %q is not running", slug)
	}
	if e.PID > 0 {
		p.signalPID(e.PID, false)
	}
	return p.mutateRegistry(func(reg *nativeRegistry) { markStopped(reg, slug) })
}

// Restart stops then starts the service.
func (p *NativeProcessProvider) Restart(ctx context.Context, slug string) error {
	if err := p.Stop(ctx, slug); err != nil {
		return err
	}
	return p.Start(ctx, slug)
}

// Remove stops the service, deletes its per-slug directory (binary + logs), and drops
// its registry entry — the native analogue of removing a container and its volumes.
func (p *NativeProcessProvider) Remove(ctx context.Context, slug string) error {
	p.stopInternal(slug, true)
	if err := os.RemoveAll(filepath.Join(p.baseDir, slug)); err != nil {
		return err
	}
	return p.mutateRegistry(func(reg *nativeRegistry) { delete(reg.Entries, slug) })
}

// Logs returns the tail of the service's captured stdout+stderr, reading the rotated
// previous generation then the current one, bounded so a huge log cannot be loaded in
// full. An absent log yields an empty string (not an error).
func (p *NativeProcessProvider) Logs(ctx context.Context, slug string, lines int) (string, error) {
	return readLogTail(filepath.Join(p.baseDir, slug, "log"), lines)
}

// List reports every registry entry as a ContainerInfo, checking each recorded pid's
// liveness (and best-effort CPU/memory) via gopsutil. A process that isn't reachable
// reports zeros and status "stopped"; it never panics on a dead/reused pid.
func (p *NativeProcessProvider) List(ctx context.Context) ([]ContainerInfo, error) {
	reg := p.readRegistry()
	slugs := make([]string, 0, len(reg.Entries))
	for slug := range reg.Entries {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)

	out := make([]ContainerInfo, 0, len(slugs))
	for _, slug := range slugs {
		e := reg.Entries[slug]
		var cpu, mem float64
		alive := false
		if e.PID > 0 {
			cpu, mem, alive = p.statFn(e.PID)
		}
		status := "stopped"
		if alive {
			status = "running"
		}
		out = append(out, ContainerInfo{
			Slug:        e.Slug,
			ContainerID: strconv.Itoa(e.PID),
			Name:        containerName(e.Slug),
			Image:       e.URL,
			Status:      status,
			CPUPercent:  cpu,
			MemoryMB:    mem,
		})
	}
	return out, nil
}

// registryDesc carries the source-artifact identity persisted with a launched
// process so List can report it and Start can relaunch it.
type registryDesc struct {
	URL     string
	SHA256  string
	Archive string
}

// launchProcess starts one process, registers it in the procs map and the on-disk
// registry, and spins up its supervisor. The first start is synchronous so Deploy/Start
// fail fast if the binary cannot exec; the supervisor thereafter owns Wait + respawn.
func (p *NativeProcessProvider) launchProcess(slug, binPath string, args, svcEnv []string, res catalog.ResourceLimits, desc registryDesc) (*managedProcess, error) {
	mp := &managedProcess{
		slug:    slug,
		binPath: binPath,
		args:    args,
		env:     svcEnv,
		res:     res,
		log:     newRotatingLogWriter(filepath.Join(p.baseDir, slug, "log"), p.logCap),
		stopCh:  make(chan struct{}),
		doneCh:  make(chan struct{}),
	}
	if err := p.startOnce(mp); err != nil {
		_ = mp.log.Close()
		return nil, err
	}
	p.mu.Lock()
	p.procs[slug] = mp
	p.mu.Unlock()

	if err := p.persistRunning(mp, desc); err != nil {
		// Non-fatal: the process IS running. Note it in the service's own log so the
		// deploy still succeeds; List will re-derive liveness from the pid next tick.
		fmt.Fprintf(mp.log, "cashpilot: warning: could not persist native registry: %v\n", err)
	}
	go p.supervise(mp)
	return mp, nil
}

// startOnce execs the process. It refuses (errStopRequested) if a Stop already marked
// the process stopping, closing the launch/stop race under mp.mu.
func (p *NativeProcessProvider) startOnce(mp *managedProcess) error {
	mp.mu.Lock()
	defer mp.mu.Unlock()
	if mp.stopping {
		return errStopRequested
	}
	// argv only, no shell: args come from buildCommandArgs (tokenize-then-substitute),
	// so a credential value's own metacharacters can never split into extra arguments.
	cmd := exec.Command(mp.binPath, mp.args...)
	cmd.Env = append(os.Environ(), mp.env...)
	cmd.Stdout = mp.log
	cmd.Stderr = mp.log
	applyNativeResourceLimits(cmd, mp.res)
	if err := cmd.Start(); err != nil {
		return err
	}
	mp.cmd = cmd
	return nil
}

// supervise waits for the managed process and restarts it with bounded exponential
// backoff when it exits unexpectedly (the native "restart policy"). It stops cleanly
// on a deliberate Stop and gives up after maxRestarts (0 = unlimited). A process that
// stays up longer than backoffMax resets the backoff, so an occasional crash after
// long uptime doesn't inherit a crash-loop's long delay.
func (p *NativeProcessProvider) supervise(mp *managedProcess) {
	defer close(mp.doneCh)
	backoff := p.backoffMin
	restarts := 0
	for {
		cmd := mp.getCmd()
		runStart := time.Now()
		if cmd != nil {
			_ = cmd.Wait()
		}
		if mp.isStopping() {
			return
		}
		restarts++
		if p.maxRestarts > 0 && restarts > p.maxRestarts {
			return
		}
		if cmd != nil && time.Since(runStart) >= p.backoffMax {
			backoff = p.backoffMin
		}
		select {
		case <-mp.stopCh:
			return
		case <-time.After(backoff):
		}
		if next := backoff * 2; next <= p.backoffMax {
			backoff = next
		} else {
			backoff = p.backoffMax
		}
		if err := p.startOnce(mp); err != nil {
			if mp.isStopping() {
				return
			}
			// Exec failed (e.g. transient). Clear cmd so the next iteration treats it
			// as another crash, backs off further, and retries — still bounded.
			mp.setCmd(nil)
			continue
		}
		p.updatePID(mp)
	}
}

// stopManaged stops and reaps a managed child owned by this session, returning true if
// one existed. It waits for the supervisor to exit (so no respawn survives) and closes
// the log. Returns false when there is no in-memory child for the slug (orphan case).
func (p *NativeProcessProvider) stopManaged(slug string) bool {
	p.mu.Lock()
	mp := p.procs[slug]
	if mp != nil {
		delete(p.procs, slug)
	}
	p.mu.Unlock()
	if mp == nil {
		return false
	}
	mp.requestStop()
	gracefulKill(mp.beginStop(), p.stopTimeout, mp.doneCh)
	<-mp.doneCh
	_ = mp.log.Close()
	return true
}

// stopInternal is the idempotent stop used by Deploy (before a redeploy) and Remove.
// It stops a managed child if present, otherwise signals a recorded orphan pid. force
// escalates straight to kill (used by Remove).
func (p *NativeProcessProvider) stopInternal(slug string, force bool) {
	if p.stopManaged(slug) {
		return
	}
	if e, ok := p.readRegistry().Entries[slug]; ok && e.PID > 0 {
		p.signalPID(e.PID, force)
	}
}

// procAlive reports whether a managed child's process is currently alive.
func (p *NativeProcessProvider) procAlive(mp *managedProcess) bool {
	cmd := mp.getCmd()
	if cmd == nil || cmd.Process == nil {
		return false
	}
	_, _, alive := p.statFn(cmd.Process.Pid)
	return alive
}

// signalPID stops a process by pid (an orphan from a previous session we don't own as
// a child). Graceful SIGTERM then, after the grace period, SIGKILL; force skips
// straight to kill. os.FindProcess never fails on Unix, and signalling a dead pid is a
// harmless ignored error, so this never panics.
func (p *NativeProcessProvider) signalPID(pid int, force bool) {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return
	}
	if force {
		_ = proc.Kill()
		return
	}
	_ = proc.Signal(syscall.SIGTERM)
	deadline := time.Now().Add(p.stopTimeout)
	for time.Now().Before(deadline) {
		if _, _, alive := p.statFn(pid); !alive {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	_ = proc.Kill()
}

// gracefulKill signals cmd's process to terminate, waits up to timeout for the
// supervisor to reap it (done closes), then escalates to SIGKILL. On Windows SIGTERM
// delivery is unsupported, so the timeout+Kill path is what actually stops the process
// there — acceptable for this best-effort v1.
func gracefulKill(cmd *exec.Cmd, timeout time.Duration, done <-chan struct{}) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-done:
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
	}
}

// applyNativeResourceLimits is the best-effort-v1 resource-limit hook. Cross-OS
// process resource capping (cgroups v2 on Linux, Job Objects on Windows,
// taskpolicy/setrlimit on macOS) is deliberately out of scope for the MVP: unlike a
// container, a native process has no single portable limit knob. It is wired here so a
// later phase can implement per-OS caps without touching the launch path.
//
// TODO(native-limits): apply res.MemLimit / OOM-priority equivalents per OS.
func applyNativeResourceLimits(cmd *exec.Cmd, res catalog.ResourceLimits) {
	_ = cmd
	_ = res
}

// buildNativeEnv resolves a native service's environment from its native.env defaults
// overlaid with the caller's credentials, then substitutes ${VAR} references — the
// native counterpart of buildEnv for the Docker path.
func buildNativeEnv(svc catalog.Service, overrides map[string]string) map[string]string {
	env := make(map[string]string)
	for _, item := range svc.Native.Env {
		if item.Default != "" {
			env[item.Key] = strings.ReplaceAll(item.Default, "{hostname}", "desktop")
		}
	}
	for key, value := range overrides {
		env[key] = value
	}
	for key, value := range env {
		env[key] = substitute(value, env)
	}
	return env
}

func gopsutilStats(pid int) (float64, float64, bool) {
	proc, err := process.NewProcess(int32(pid))
	if err != nil {
		return 0, 0, false
	}
	running, err := proc.IsRunning()
	if err != nil || !running {
		return 0, 0, false
	}
	cpu := 0.0
	if c, err := proc.CPUPercent(); err == nil {
		cpu = c
	}
	mem := 0.0
	if mi, err := proc.MemoryInfo(); err == nil && mi != nil {
		mem = float64(mi.RSS) / 1024 / 1024
	}
	return cpu, mem, true
}

func fileExists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// ---------------------------------------------------------------------------
// Download + verify + extract
// ---------------------------------------------------------------------------

// ensureBinary returns the path to the verified, extracted executable for bin,
// reusing an existing extraction when its recorded verified digest still matches (so a
// redeploy of the same pinned version does not re-download). Otherwise it downloads
// over HTTPS, verifies the SHA-256 (fail-closed), extracts, marks the executable 0700,
// and records the verified digest.
func (p *NativeProcessProvider) ensureBinary(ctx context.Context, slugDir string, bin catalog.NativeBinary, progress func(string)) (string, error) {
	if err := os.MkdirAll(slugDir, 0o700); err != nil {
		return "", err
	}
	targetBin := filepath.Join(slugDir, filepath.FromSlash(bin.Bin))
	markerPath := filepath.Join(slugDir, ".verified-sha256")

	if isHexSHA256(bin.SHA256) && fileExists(targetBin) {
		if recorded, err := os.ReadFile(markerPath); err == nil &&
			strings.EqualFold(strings.TrimSpace(string(recorded)), strings.TrimSpace(bin.SHA256)) {
			return targetBin, nil
		}
	}

	if progress != nil {
		progress("Downloading " + bin.URL)
	}
	data, err := p.download(ctx, bin.URL, bin.SHA256)
	if err != nil {
		return "", err
	}
	if progress != nil {
		progress("Verified SHA-256; extracting")
	}
	if err := extractArtifact(bin.Archive, data, slugDir, targetBin); err != nil {
		return "", err
	}
	if !fileExists(targetBin) {
		return "", fmt.Errorf("native binary %q not found after extracting %s", bin.Bin, bin.URL)
	}
	if err := os.Chmod(targetBin, 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(markerPath, []byte(strings.ToLower(strings.TrimSpace(bin.SHA256))), 0o600); err != nil {
		return "", err
	}
	return targetBin, nil
}

// download fetches and SHA-256-verifies an artifact. It refuses any non-HTTPS URL and
// any missing/invalid checksum BEFORE making a request, and returns the bytes only
// when the digest matches — so a caller can never end up execing unverified content.
func (p *NativeProcessProvider) download(ctx context.Context, url, expectedSHA string) ([]byte, error) {
	if !isHTTPS(url) {
		return nil, fmt.Errorf("refusing non-HTTPS native binary URL %q", url)
	}
	if !isHexSHA256(expectedSHA) {
		return nil, fmt.Errorf("native binary %q has no valid SHA-256 checksum (got %q); refusing to download an unverified binary", url, expectedSHA)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading %q: HTTP %d", url, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownloadBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxDownloadBytes {
		return nil, fmt.Errorf("native binary %q exceeds the %d-byte download cap", url, maxDownloadBytes)
	}
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	if !strings.EqualFold(got, strings.TrimSpace(expectedSHA)) {
		return nil, fmt.Errorf("native binary %q failed SHA-256 verification: got %s, want %s", url, got, expectedSHA)
	}
	return data, nil
}

func isHTTPS(url string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(url)), "https://")
}

// isHexSHA256 reports whether s is a 64-character hex string — a real SHA-256. This is
// what rejects placeholders like "TODO-verify": an unverifiable checksum can never
// pass, so the binary is never downloaded or executed.
func isHexSHA256(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// extractArtifact writes the executable(s) from a downloaded artifact into destDir.
// "none"/"" means the bytes ARE the raw binary (written to targetBin); tar.gz and zip
// are extracted with a path-traversal guard (symlinks and non-regular entries skipped)
// and a total-size cap.
func extractArtifact(archive string, data []byte, destDir, targetBin string) error {
	switch strings.ToLower(strings.TrimSpace(archive)) {
	case "", "none", "raw", "binary":
		if err := os.MkdirAll(filepath.Dir(targetBin), 0o700); err != nil {
			return err
		}
		return os.WriteFile(targetBin, data, 0o700)
	case "tar.gz", "tgz", "targz":
		return extractTarGz(data, destDir)
	case "zip":
		return extractZip(data, destDir)
	default:
		return fmt.Errorf("unsupported native archive format %q", archive)
	}
}

func extractTarGz(data []byte, destDir string) error {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	remaining := int64(maxExtractedBytes)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		target, ok := sanitizeExtractPath(destDir, hdr.Name)
		if !ok {
			continue
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			mode := os.FileMode(0o600)
			if hdr.FileInfo().Mode()&0o111 != 0 {
				mode = 0o700
			}
			if err := writeExtractedFile(target, tr, mode, &remaining); err != nil {
				return err
			}
		default:
			// Skip symlinks, hardlinks, devices, etc. (security: no link escapes).
		}
	}
	return nil
}

func extractZip(data []byte, destDir string) error {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return err
	}
	remaining := int64(maxExtractedBytes)
	for _, f := range zr.File {
		target, ok := sanitizeExtractPath(destDir, f.Name)
		if !ok {
			continue
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			continue
		}
		if f.Mode()&os.ModeSymlink != 0 {
			continue // skip symlinks (security)
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		mode := os.FileMode(0o600)
		if f.Mode()&0o111 != 0 {
			mode = 0o700
		}
		err = writeExtractedFile(target, rc, mode, &remaining)
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// writeExtractedFile copies one archive entry to disk, decrementing a shared byte
// budget and failing if the archive would inflate past maxExtractedBytes.
func writeExtractedFile(target string, src io.Reader, mode os.FileMode, remaining *int64) error {
	if *remaining <= 0 {
		return fmt.Errorf("archive exceeds the %d-byte extraction cap", int64(maxExtractedBytes))
	}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(src, *remaining+1))
	*remaining -= n
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if *remaining < 0 {
		return fmt.Errorf("archive exceeds the %d-byte extraction cap", int64(maxExtractedBytes))
	}
	return closeErr
}

// sanitizeExtractPath maps an archive entry name to a safe destination inside destDir,
// neutralising "../" traversal and absolute paths (Zip-Slip / tar-slip). ok is false
// for an entry that would escape destDir or resolves to nothing.
func sanitizeExtractPath(destDir, name string) (string, bool) {
	clean := filepath.Clean("/" + filepath.FromSlash(name))
	clean = strings.TrimPrefix(clean, string(os.PathSeparator))
	if clean == "" || clean == "." {
		return "", false
	}
	target := filepath.Join(destDir, clean)
	if target != destDir && !strings.HasPrefix(target, destDir+string(os.PathSeparator)) {
		return "", false
	}
	return target, true
}

// ---------------------------------------------------------------------------
// Bounded rotating log
// ---------------------------------------------------------------------------

// rotatingLogWriter appends a process's stdout+stderr to a file, rotating the current
// file to "<path>.1" and starting fresh once it reaches cap. At most two generations
// exist, so on-disk log size is bounded at ~2*cap regardless of how much a process
// emits — the native mirror of the Docker path's log-size hardening.
type rotatingLogWriter struct {
	path string
	cap  int64

	mu     sync.Mutex
	f      *os.File
	size   int64
	closed bool
}

func newRotatingLogWriter(path string, cap int64) *rotatingLogWriter {
	return &rotatingLogWriter{path: path, cap: cap}
}

func (w *rotatingLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, os.ErrClosed
	}
	if w.f == nil {
		if err := os.MkdirAll(filepath.Dir(w.path), 0o700); err != nil {
			return 0, err
		}
		f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return 0, err
		}
		if st, err := f.Stat(); err == nil {
			w.size = st.Size()
		}
		w.f = f
	}
	if w.cap > 0 && w.size+int64(len(p)) > w.cap {
		_ = w.f.Close()
		_ = os.Rename(w.path, w.path+".1")
		f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			w.f = nil
			return 0, err
		}
		w.f = f
		w.size = 0
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *rotatingLogWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.closed = true
	if w.f != nil {
		err := w.f.Close()
		w.f = nil
		return err
	}
	return nil
}

// readLogTail returns the last `lines` lines across the rotated (.1) and current log
// generations, each read from its end and bounded by maxLogBytes so a single enormous
// line cannot load unbounded memory.
func readLogTail(basePath string, lines int) (string, error) {
	if lines <= 0 {
		lines = 200
	}
	var buf []byte
	for _, path := range []string{basePath + ".1", basePath} {
		raw, err := readFileTail(path, maxLogBytes)
		if err != nil {
			continue
		}
		buf = append(buf, raw...)
	}
	return lastLines(string(buf), lines), nil
}

func readFileTail(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() > max {
		if _, err := f.Seek(st.Size()-max, io.SeekStart); err != nil {
			return nil, err
		}
	}
	return io.ReadAll(f)
}

func lastLines(s string, n int) string {
	if s == "" || n <= 0 {
		return ""
	}
	trimmed := strings.TrimRight(s, "\n")
	parts := strings.Split(trimmed, "\n")
	if len(parts) <= n {
		return strings.Join(parts, "\n")
	}
	return strings.Join(parts[len(parts)-n:], "\n")
}

// ---------------------------------------------------------------------------
// Persistent registry (state.json)
// ---------------------------------------------------------------------------

type nativeRegistry struct {
	Entries map[string]*nativeRegistryEntry `json:"entries"`
}

// nativeRegistryEntry is the persisted state for one native service. Args/Env are kept
// so Start can relaunch after an app restart. Env may hold service credentials, so the
// registry file is written 0600 inside the 0700 <appDir>/native dir (matching the
// existing 0600 credential-key file fallback in internal/config).
type nativeRegistryEntry struct {
	Slug      string   `json:"slug"`
	PID       int      `json:"pid"`
	BinPath   string   `json:"binPath"`
	Args      []string `json:"args,omitempty"`
	Env       []string `json:"env,omitempty"`
	URL       string   `json:"url"`
	SHA256    string   `json:"sha256"`
	Archive   string   `json:"archive"`
	StartedAt string   `json:"startedAt"`
	Desired   string   `json:"desired"` // "running" | "stopped"
}

func markStopped(reg *nativeRegistry, slug string) {
	if e, ok := reg.Entries[slug]; ok {
		e.PID = 0
		e.Desired = "stopped"
	}
}

// readRegistry returns a snapshot of the on-disk registry (empty if none/unreadable).
func (p *NativeProcessProvider) readRegistry() *nativeRegistry {
	p.regMu.Lock()
	defer p.regMu.Unlock()
	return p.loadRegistryLocked()
}

func (p *NativeProcessProvider) loadRegistryLocked() *nativeRegistry {
	reg := &nativeRegistry{Entries: map[string]*nativeRegistryEntry{}}
	raw, err := os.ReadFile(p.statePath)
	if err != nil {
		return reg
	}
	if err := json.Unmarshal(raw, reg); err != nil {
		return &nativeRegistry{Entries: map[string]*nativeRegistryEntry{}}
	}
	if reg.Entries == nil {
		reg.Entries = map[string]*nativeRegistryEntry{}
	}
	return reg
}

// mutateRegistry read-modify-writes the registry under regMu, persisting atomically
// (temp file + rename) with 0600 permissions.
func (p *NativeProcessProvider) mutateRegistry(fn func(*nativeRegistry)) error {
	p.regMu.Lock()
	defer p.regMu.Unlock()
	reg := p.loadRegistryLocked()
	fn(reg)
	if err := os.MkdirAll(p.baseDir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(reg, "", "  ")
	if err != nil {
		return err
	}
	tmp := p.statePath + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p.statePath)
}

func (p *NativeProcessProvider) persistRunning(mp *managedProcess, desc registryDesc) error {
	pid := 0
	if cmd := mp.getCmd(); cmd != nil && cmd.Process != nil {
		pid = cmd.Process.Pid
	}
	return p.mutateRegistry(func(reg *nativeRegistry) {
		reg.Entries[mp.slug] = &nativeRegistryEntry{
			Slug:      mp.slug,
			PID:       pid,
			BinPath:   mp.binPath,
			Args:      mp.args,
			Env:       mp.env,
			URL:       desc.URL,
			SHA256:    desc.SHA256,
			Archive:   desc.Archive,
			StartedAt: time.Now().UTC().Format(time.RFC3339),
			Desired:   "running",
		}
	})
}

// updatePID records a respawn's new pid (and restart time) into the registry.
func (p *NativeProcessProvider) updatePID(mp *managedProcess) {
	pid := 0
	if cmd := mp.getCmd(); cmd != nil && cmd.Process != nil {
		pid = cmd.Process.Pid
	}
	_ = p.mutateRegistry(func(reg *nativeRegistry) {
		if e, ok := reg.Entries[mp.slug]; ok {
			e.PID = pid
			e.StartedAt = time.Now().UTC().Format(time.RFC3339)
			e.Desired = "running"
		}
	})
}
