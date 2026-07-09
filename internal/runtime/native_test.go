package runtime

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
)

// TestMain lets the compiled test binary double as the deterministic "downloaded
// native binary": when re-invoked with CASHPILOT_NATIVE_STUB=1 it runs a tiny stub
// (prints a known line, records its pid to a marker file, then blocks until signalled)
// instead of the test suite. This proves the whole download->verify->extract->exec->
// supervise->stop path with a real child process and NO network to any real vendor
// binary — the artifact bytes are the test binary itself, served from an in-process
// TLS server.
func TestMain(m *testing.M) {
	if os.Getenv("CASHPILOT_NATIVE_STUB") == "1" {
		runNativeStub()
		return
	}
	os.Exit(m.Run())
}

func runNativeStub() {
	if marker := os.Getenv("CASHPILOT_STUB_MARKER"); marker != "" {
		_ = os.WriteFile(marker, []byte(strconv.Itoa(os.Getpid())), 0o600)
	}
	// Emit a known line so a test can assert stdout is captured into the log.
	os.Stdout.WriteString("cashpilot-native-stub alive pid=" + strconv.Itoa(os.Getpid()) + "\n")
	// Crash-loop mode: append this run to a shared file and exit immediately so the
	// supervisor treats every launch as a fast crash (used to prove the bounded-restart
	// cap without waiting on real backoff).
	if os.Getenv("CASHPILOT_STUB_EXIT") == "1" {
		if runs := os.Getenv("CASHPILOT_STUB_RUNS"); runs != "" {
			if f, err := os.OpenFile(runs, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
				_, _ = f.WriteString(strconv.Itoa(os.Getpid()) + "\n")
				_ = f.Close()
			}
		}
		os.Exit(0)
	}
	// Stay alive until the supervisor signals/kills us (default SIGTERM disposition
	// terminates the process on Unix; Windows relies on Kill after the grace period).
	select {}
}

// --- helpers ---------------------------------------------------------------

var (
	stubOnce  sync.Once
	stubBytes []byte
	stubErr   error
)

// stubBinaryBytes returns the bytes of the currently-running test binary, read once
// and cached. Serving these as the "download" makes the child a copy of this binary
// that runs runNativeStub via TestMain.
func stubBinaryBytes(t *testing.T) []byte {
	t.Helper()
	stubOnce.Do(func() {
		self, err := os.Executable()
		if err != nil {
			stubErr = err
			return
		}
		stubBytes, stubErr = os.ReadFile(self)
	})
	if stubErr != nil {
		t.Fatalf("reading test binary for stub: %v", stubErr)
	}
	return stubBytes
}

func stubBinName() string {
	if goruntime.GOOS == "windows" {
		return "stub.exe"
	}
	return "stub"
}

func stubEnv(marker string) map[string]string {
	return map[string]string{"CASHPILOT_NATIVE_STUB": "1", "CASHPILOT_STUB_MARKER": marker}
}

func sha256hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func newTestNativeProvider(t *testing.T) *NativeProcessProvider {
	t.Helper()
	p := NewNativeProcessProvider(t.TempDir())
	p.backoffMin = 10 * time.Millisecond
	p.backoffMax = 40 * time.Millisecond
	p.maxRestarts = 20
	p.stopTimeout = 3 * time.Second
	return p
}

// serveArtifact serves the given bytes over an in-process TLS server and points the
// provider's client at it. The HTTPS scheme is real (httptest TLS), so the provider's
// HTTPS-only guard is exercised, not bypassed.
func serveArtifact(t *testing.T, p *NativeProcessProvider, artifact []byte) string {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(artifact)
	}))
	t.Cleanup(srv.Close)
	client := srv.Client()
	client.CheckRedirect = httpsOnlyRedirect // mirror the production download redirect policy
	p.httpClient = client
	return srv.URL + "/stub"
}

func nativeService(slug, url, sha, archive string) catalog.Service {
	return catalog.Service{
		Name: slug,
		Slug: slug,
		Native: catalog.NativeConfig{
			Binaries: []catalog.NativeBinary{{
				OS:      goruntime.GOOS,
				Arch:    goruntime.GOARCH,
				URL:     url,
				SHA256:  sha,
				Archive: archive,
				Bin:     stubBinName(),
			}},
		},
	}
}

func tarGzArtifact(t *testing.T, name string, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "./" + name, Mode: 0o755, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func zipArtifact(t *testing.T, name string, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	fh := &zip.FileHeader{Name: name, Method: zip.Deflate}
	fh.SetMode(0o755)
	w, err := zw.CreateHeader(fh)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

func listEntry(t *testing.T, p *NativeProcessProvider, slug string) (ContainerInfo, bool) {
	t.Helper()
	infos, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	for _, ci := range infos {
		if ci.Slug == slug {
			return ci, true
		}
	}
	return ContainerInfo{}, false
}

// --- tests -----------------------------------------------------------------

// TestNativeDeployTarGzExecAndStop is the end-to-end proof: a tar.gz-packaged stub is
// downloaded from an HTTPS server, SHA-256-verified, extracted, exec'd, reported
// running by List, its stdout captured by Logs, and cleanly stopped by Stop.
func TestNativeDeployTarGzExecAndStop(t *testing.T) {
	p := newTestNativeProvider(t)
	art := tarGzArtifact(t, stubBinName(), stubBinaryBytes(t))
	url := serveArtifact(t, p, art)
	marker := filepath.Join(t.TempDir(), "marker")
	svc := nativeService("stubsvc", url, sha256hex(art), "tar.gz")

	info, err := p.Deploy(context.Background(), DeploySpec{Slug: "stubsvc", Service: svc, Env: stubEnv(marker)}, nil)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if info.Status != "running" || info.Image != url {
		t.Fatalf("unexpected deploy info: %+v", info)
	}

	if !waitFor(t, 5*time.Second, func() bool {
		ci, ok := listEntry(t, p, "stubsvc")
		return ok && ci.Status == "running"
	}) {
		t.Fatal("service never reported running")
	}

	// The binary actually executed: the stub wrote its pid to the marker file.
	if !waitFor(t, 5*time.Second, func() bool {
		_, err := os.Stat(marker)
		return err == nil
	}) {
		t.Fatal("stub marker never written (binary did not execute)")
	}

	logs, err := p.Logs(context.Background(), "stubsvc", 100)
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if !strings.Contains(logs, "cashpilot-native-stub alive") {
		t.Fatalf("captured logs missing stub output: %q", logs)
	}

	if err := p.Stop(context.Background(), "stubsvc"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !waitFor(t, 5*time.Second, func() bool {
		ci, ok := listEntry(t, p, "stubsvc")
		return ok && ci.Status == "stopped"
	}) {
		t.Fatal("service never reported stopped after Stop")
	}
}

// TestNativeDeployRawBinary covers the archive:"none" path (the url IS the executable).
func TestNativeDeployRawBinary(t *testing.T) {
	p := newTestNativeProvider(t)
	art := stubBinaryBytes(t)
	url := serveArtifact(t, p, art)
	marker := filepath.Join(t.TempDir(), "marker")
	svc := nativeService("rawsvc", url, sha256hex(art), "none")

	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: "rawsvc", Service: svc, Env: stubEnv(marker)}, nil); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	t.Cleanup(func() { _ = p.Remove(context.Background(), "rawsvc") })

	if !waitFor(t, 5*time.Second, func() bool {
		ci, ok := listEntry(t, p, "rawsvc")
		return ok && ci.Status == "running"
	}) {
		t.Fatal("raw-binary service never reported running")
	}
}

// TestNativeDeployZipArchive covers the zip extraction path.
func TestNativeDeployZipArchive(t *testing.T) {
	p := newTestNativeProvider(t)
	art := zipArtifact(t, stubBinName(), stubBinaryBytes(t))
	url := serveArtifact(t, p, art)
	marker := filepath.Join(t.TempDir(), "marker")
	svc := nativeService("zipsvc", url, sha256hex(art), "zip")

	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: "zipsvc", Service: svc, Env: stubEnv(marker)}, nil); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	t.Cleanup(func() { _ = p.Remove(context.Background(), "zipsvc") })

	if !waitFor(t, 5*time.Second, func() bool {
		ci, ok := listEntry(t, p, "zipsvc")
		return ok && ci.Status == "running"
	}) {
		t.Fatal("zip service never reported running")
	}
}

// TestNativeDeployRejectsChecksumMismatch proves the security-critical property: a
// valid artifact served under a WRONG checksum is refused and NOTHING is executed.
func TestNativeDeployRejectsChecksumMismatch(t *testing.T) {
	p := newTestNativeProvider(t)
	art := stubBinaryBytes(t)
	url := serveArtifact(t, p, art)
	marker := filepath.Join(t.TempDir(), "marker")
	// A syntactically valid but incorrect digest (64 hex chars) so it passes the
	// format gate and fails only on the real content comparison.
	svc := nativeService("badsvc", url, strings.Repeat("a", 64), "none")

	_, err := p.Deploy(context.Background(), DeploySpec{Slug: "badsvc", Service: svc, Env: stubEnv(marker)}, nil)
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("expected SHA-256 verification error, got %v", err)
	}
	// Give any (erroneously) spawned process a moment, then assert nothing ran.
	time.Sleep(200 * time.Millisecond)
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("stub executed despite a checksum mismatch")
	}
	if _, ok := listEntry(t, p, "badsvc"); ok {
		t.Fatal("a rejected deploy was recorded in the registry")
	}
}

// TestNativeDownloadRejectsInsecureAndUnverified pins the pre-request guards: a
// non-HTTPS URL and a missing/placeholder checksum are refused before any network I/O.
func TestNativeDownloadRejectsInsecureAndUnverified(t *testing.T) {
	p := newTestNativeProvider(t)
	valid := strings.Repeat("0", 64)
	if _, err := p.download(context.Background(), "http://example.test/x", valid); err == nil {
		t.Fatal("expected a non-HTTPS URL to be rejected")
	}
	if _, err := p.download(context.Background(), "https://example.test/x", "TODO-verify"); err == nil {
		t.Fatal("expected a placeholder checksum to be rejected")
	}
	if _, err := p.download(context.Background(), "https://example.test/x", ""); err == nil {
		t.Fatal("expected a missing checksum to be rejected")
	}
}

// TestNativeDeploySelectsBinaryByOSArch proves per-OS/arch selection: an unmatched
// entry yields a clear error and execs nothing; a matching one runs.
func TestNativeDeploySelectsBinaryByOSArch(t *testing.T) {
	p := newTestNativeProvider(t)
	art := stubBinaryBytes(t)
	url := serveArtifact(t, p, art)

	// Only a non-host os/arch declared -> no native binary for this host.
	svc := catalog.Service{Name: "sel", Slug: "sel", Native: catalog.NativeConfig{Binaries: []catalog.NativeBinary{
		{OS: "plan9", Arch: "mips", URL: url, SHA256: sha256hex(art), Archive: "none", Bin: "stub"},
	}}}
	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: "sel", Service: svc}, nil); err == nil {
		t.Fatal("expected a no-native-binary error for an unmatched os/arch")
	}

	// Add a matching entry among the others -> it is selected and runs.
	marker := filepath.Join(t.TempDir(), "marker")
	svc.Native.Binaries = append(svc.Native.Binaries, catalog.NativeBinary{
		OS: goruntime.GOOS, Arch: goruntime.GOARCH, URL: url, SHA256: sha256hex(art), Archive: "none", Bin: stubBinName(),
	})
	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: "sel", Service: svc, Env: stubEnv(marker)}, nil); err != nil {
		t.Fatalf("Deploy with matching arch: %v", err)
	}
	t.Cleanup(func() { _ = p.Remove(context.Background(), "sel") })
	if !waitFor(t, 5*time.Second, func() bool {
		ci, ok := listEntry(t, p, "sel")
		return ok && ci.Status == "running"
	}) {
		t.Fatal("matching-arch binary did not run")
	}
}

// TestNativeRegistryPersistsAcrossInstances proves the state.json registry survives:
// a second provider over the same app dir sees the running deployment.
func TestNativeRegistryPersistsAcrossInstances(t *testing.T) {
	dir := t.TempDir()
	a := NewNativeProcessProvider(dir)
	a.backoffMin = 10 * time.Millisecond
	a.backoffMax = 40 * time.Millisecond
	a.stopTimeout = 3 * time.Second

	art := tarGzArtifact(t, stubBinName(), stubBinaryBytes(t))
	url := serveArtifact(t, a, art)
	marker := filepath.Join(t.TempDir(), "marker")
	svc := nativeService("persist", url, sha256hex(art), "tar.gz")

	if _, err := a.Deploy(context.Background(), DeploySpec{Slug: "persist", Service: svc, Env: stubEnv(marker)}, nil); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	t.Cleanup(func() { _ = a.Remove(context.Background(), "persist") })
	if !waitFor(t, 5*time.Second, func() bool {
		ci, ok := listEntry(t, a, "persist")
		return ok && ci.Status == "running"
	}) {
		t.Fatal("service not running under provider A")
	}

	b := NewNativeProcessProvider(dir)
	ci, ok := listEntry(t, b, "persist")
	if !ok {
		t.Fatal("provider B did not see the persisted registry entry")
	}
	if ci.Status != "running" {
		t.Fatalf("provider B sees status %q, want running", ci.Status)
	}
	if ci.Image != url {
		t.Fatalf("provider B sees image %q, want %q", ci.Image, url)
	}
}

// TestNativeSupervisorRespawnsKilledStub proves the "restart policy": a killed managed
// process is respawned (bounded backoff) under a NEW pid.
func TestNativeSupervisorRespawnsKilledStub(t *testing.T) {
	p := newTestNativeProvider(t)
	art := stubBinaryBytes(t)
	url := serveArtifact(t, p, art)
	marker := filepath.Join(t.TempDir(), "marker")
	svc := nativeService("resp", url, sha256hex(art), "none")

	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: "resp", Service: svc, Env: stubEnv(marker)}, nil); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	t.Cleanup(func() { _ = p.Remove(context.Background(), "resp") })

	var pid1 int
	if !waitFor(t, 5*time.Second, func() bool {
		ci, ok := listEntry(t, p, "resp")
		if ok && ci.Status == "running" {
			pid1, _ = strconv.Atoi(ci.ContainerID)
			return pid1 > 0
		}
		return false
	}) {
		t.Fatal("no initial running pid")
	}

	// Kill the running child out from under the supervisor.
	if proc, err := os.FindProcess(pid1); err == nil {
		_ = proc.Kill()
	}

	// The supervisor must restart it with a fresh pid.
	if !waitFor(t, 8*time.Second, func() bool {
		ci, ok := listEntry(t, p, "resp")
		if !ok || ci.Status != "running" {
			return false
		}
		pid2, _ := strconv.Atoi(ci.ContainerID)
		return pid2 > 0 && pid2 != pid1
	}) {
		t.Fatalf("supervisor did not respawn after the process was killed (pid1=%d)", pid1)
	}
}

// TestNativeStopSignalsOrphanFromRegistry proves Stop works for a process recorded in
// the registry but NOT owned as a child of this session (an orphan surviving an app
// restart): it is signalled by pid and marked stopped.
func TestNativeStopSignalsOrphanFromRegistry(t *testing.T) {
	p := newTestNativeProvider(t)

	binPath := filepath.Join(t.TempDir(), stubBinName())
	if err := os.WriteFile(binPath, stubBinaryBytes(t), 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "marker")
	cmd := exec.Command(binPath)
	cmd.Env = append(os.Environ(), "CASHPILOT_NATIVE_STUB=1", "CASHPILOT_STUB_MARKER="+marker)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start orphan: %v", err)
	}
	pid := cmd.Process.Pid
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-done })

	if err := p.mutateRegistry(func(reg *nativeRegistry) {
		reg.Entries["orphan"] = &nativeRegistryEntry{Slug: "orphan", PID: pid, BinPath: binPath, URL: "https://example.test/x", Desired: "running"}
	}); err != nil {
		t.Fatalf("seed registry: %v", err)
	}

	if ci, ok := listEntry(t, p, "orphan"); !ok || ci.Status != "running" {
		t.Fatalf("orphan not reported running: ok=%v", ok)
	}
	if err := p.Stop(context.Background(), "orphan"); err != nil {
		t.Fatalf("Stop orphan: %v", err)
	}
	if !waitFor(t, 5*time.Second, func() bool {
		_, _, alive := p.statFn(pid)
		return !alive
	}) {
		t.Fatal("orphan process still alive after Stop")
	}
	if ci, _ := listEntry(t, p, "orphan"); ci.Status != "stopped" {
		t.Fatalf("orphan status %q after Stop, want stopped", ci.Status)
	}
}

// TestNativeListReportsLivenessFromStatFn pins the List mapping using a fake sampler:
// a dead pid reports stopped/zero, a live pid reports running with its stats.
func TestNativeListReportsLivenessFromStatFn(t *testing.T) {
	p := newTestNativeProvider(t)
	if err := p.mutateRegistry(func(reg *nativeRegistry) {
		reg.Entries["x"] = &nativeRegistryEntry{Slug: "x", PID: 4242, URL: "https://example.test/x", Desired: "running"}
	}); err != nil {
		t.Fatal(err)
	}

	p.statFn = func(pid int) (float64, float64, bool) { return 0, 0, false }
	ci, ok := listEntry(t, p, "x")
	if !ok || ci.Status != "stopped" || ci.CPUPercent != 0 || ci.MemoryMB != 0 {
		t.Fatalf("dead pid: %+v ok=%v", ci, ok)
	}

	p.statFn = func(pid int) (float64, float64, bool) { return 12.5, 34.0, true }
	ci, _ = listEntry(t, p, "x")
	if ci.Status != "running" || ci.CPUPercent != 12.5 || ci.MemoryMB != 34.0 {
		t.Fatalf("live pid: %+v", ci)
	}
}

// TestNativeStatusAlwaysAvailable pins that the native runtime advertises itself as
// available with no external dependency.
func TestNativeStatusAlwaysAvailable(t *testing.T) {
	p := newTestNativeProvider(t)
	s := p.Status(context.Background())
	if !s.Available || s.Kind != NativeRuntimeKind {
		t.Fatalf("unexpected native status: %+v", s)
	}
}

// TestExtractTarGzNeutralizesTraversal proves the extraction guard: a tar entry that
// tries to escape the destination directory does not create a file outside it.
func TestExtractTarGzNeutralizesTraversal(t *testing.T) {
	dest := t.TempDir()
	escaped := filepath.Join(filepath.Dir(dest), "escaped-"+filepath.Base(dest))

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	payload := []byte("x")
	if err := tw.WriteHeader(&tar.Header{Name: "../" + filepath.Base(escaped), Mode: 0o600, Size: int64(len(payload)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatal(err)
	}
	_ = tw.Close()
	_ = gz.Close()

	if err := extractTarGz(buf.Bytes(), dest, maxExtractedBytes); err != nil {
		t.Fatalf("extractTarGz: %v", err)
	}
	if _, err := os.Stat(escaped); err == nil {
		t.Fatalf("traversal entry escaped to %s", escaped)
	}
}

// TestSanitizeExtractPathStaysInsideDest checks that hostile names never resolve
// outside the destination directory.
func TestSanitizeExtractPathStaysInsideDest(t *testing.T) {
	dest := t.TempDir()
	for _, name := range []string{"../evil", "../../a/b", "/abs/evil", "foo/bar", "./myst"} {
		target, ok := sanitizeExtractPath(dest, name)
		if ok && target != dest && !strings.HasPrefix(target, dest+string(os.PathSeparator)) {
			t.Fatalf("name %q escaped dest: %q", name, target)
		}
	}
}

// TestRotatingLogWriterBoundsSize proves logs are bounded: the current generation
// never exceeds the cap, a rotated ".1" generation is kept, and the tail is readable.
func TestRotatingLogWriterBoundsSize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log")
	w := newRotatingLogWriter(path, 1024)
	line := strings.Repeat("a", 200) + "\n"
	for i := 0; i < 50; i++ {
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat current: %v", err)
	}
	if st.Size() > 1024 {
		t.Fatalf("current log %d bytes exceeds cap 1024", st.Size())
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("rotated generation missing: %v", err)
	}
	tail, err := readLogTail(path, 5)
	if err != nil {
		t.Fatalf("readLogTail: %v", err)
	}
	if !strings.Contains(tail, "aaa") {
		t.Fatalf("tail unexpectedly empty: %q", tail)
	}
}

// TestNativeReuseVerifiedBinarySkipsRedownload proves a redeploy of the same pinned
// version reuses the extracted binary (the server would 500 on a second hit).
func TestNativeReuseVerifiedBinarySkipsRedownload(t *testing.T) {
	p := newTestNativeProvider(t)
	art := stubBinaryBytes(t)

	var hits int32
	var mu sync.Mutex
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits++
		n := hits
		mu.Unlock()
		if n > 1 {
			http.Error(w, "should not re-download", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(art)
	}))
	t.Cleanup(srv.Close)
	p.httpClient = srv.Client()
	url := srv.URL + "/stub"

	slugDir := filepath.Join(p.baseDir, "reuse")
	bin := catalog.NativeBinary{OS: goruntime.GOOS, Arch: goruntime.GOARCH, URL: url, SHA256: sha256hex(art), Archive: "none", Bin: stubBinName()}

	if _, err := p.ensureBinary(context.Background(), slugDir, bin, nil); err != nil {
		t.Fatalf("first ensureBinary: %v", err)
	}
	// Second call must reuse the verified extraction and not hit the server again.
	if _, err := p.ensureBinary(context.Background(), slugDir, bin, nil); err != nil {
		t.Fatalf("second ensureBinary (should reuse): %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if hits != 1 {
		t.Fatalf("expected exactly 1 download, got %d", hits)
	}
}

// stubRoundTripper is a deterministic http.RoundTripper for exercising download's
// transport- and body-error paths without a real (or even in-process) server.
type stubRoundTripper struct {
	resp *http.Response
	err  error
}

func (s stubRoundTripper) RoundTrip(*http.Request) (*http.Response, error) { return s.resp, s.err }

// errorReadCloser is a response body whose first Read fails, exercising download's
// io.ReadAll error branch.
type errorReadCloser struct{}

func (errorReadCloser) Read([]byte) (int, error) { return 0, errors.New("simulated body read error") }
func (errorReadCloser) Close() error             { return nil }

// TestNativeDownloadNon2xx pins that a non-2xx response is a download error (nothing is
// verified or executed on a 404/500).
func TestNativeDownloadNon2xx(t *testing.T) {
	p := newTestNativeProvider(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	p.httpClient = srv.Client()
	if _, err := p.download(context.Background(), srv.URL+"/x", strings.Repeat("0", 64)); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("expected an HTTP 404 download error, got %v", err)
	}
}

// TestNativeDownloadEnforcesSizeCap proves the (test-lowered) download byte cap rejects
// an over-large artifact.
func TestNativeDownloadEnforcesSizeCap(t *testing.T) {
	p := newTestNativeProvider(t)
	p.maxDownload = 8
	payload := bytes.Repeat([]byte("z"), 64)
	url := serveArtifact(t, p, payload)
	if _, err := p.download(context.Background(), url, sha256hex(payload)); err == nil || !strings.Contains(err.Error(), "download cap") {
		t.Fatalf("expected a download-cap error, got %v", err)
	}
}

// TestNativeDownloadBodyReadError covers the body-read error path via a fake transport.
func TestNativeDownloadBodyReadError(t *testing.T) {
	p := newTestNativeProvider(t)
	p.httpClient = &http.Client{Transport: stubRoundTripper{resp: &http.Response{
		StatusCode: http.StatusOK,
		Body:       errorReadCloser{},
		Header:     make(http.Header),
	}}}
	if _, err := p.download(context.Background(), "https://stub.invalid/x", strings.Repeat("0", 64)); err == nil {
		t.Fatal("expected a body-read error to propagate")
	}
}

// TestNativeDownloadTransportError covers the transport/connection error path.
func TestNativeDownloadTransportError(t *testing.T) {
	p := newTestNativeProvider(t)
	p.httpClient = &http.Client{Transport: stubRoundTripper{err: errors.New("dial tcp: simulated failure")}}
	if _, err := p.download(context.Background(), "https://stub.invalid/x", strings.Repeat("0", 64)); err == nil {
		t.Fatal("expected a transport error to propagate")
	}
}

// TestNativeDeployRejectsCorruptArchives proves a valid-checksum but structurally
// corrupt tar.gz/zip fails extraction (and nothing is executed).
func TestNativeDeployRejectsCorruptArchives(t *testing.T) {
	for _, archive := range []string{"tar.gz", "zip"} {
		p := newTestNativeProvider(t)
		garbage := []byte("this is definitely not a valid " + archive + " archive payload")
		url := serveArtifact(t, p, garbage)
		svc := nativeService("corrupt", url, sha256hex(garbage), archive)
		if _, err := p.Deploy(context.Background(), DeploySpec{Slug: "corrupt", Service: svc}, nil); err == nil {
			t.Fatalf("archive %q: expected a corrupt-archive error, got nil", archive)
		}
	}
}

// TestNativeExtractionSizeCapRejectsBomb proves the extraction byte cap rejects an entry
// that would inflate past the budget (decompression-bomb guard), for both tar.gz and zip.
func TestNativeExtractionSizeCapRejectsBomb(t *testing.T) {
	dest := t.TempDir()
	big := bytes.Repeat([]byte("A"), 4096) // > the 1 KiB budget below
	if err := extractTarGz(tarGzArtifact(t, "stub", big), dest, 1024); err == nil || !strings.Contains(err.Error(), "extraction cap") {
		t.Fatalf("tar.gz: expected an extraction-cap error, got %v", err)
	}
	if err := extractZip(zipArtifact(t, "stub", big), dest, 1024); err == nil || !strings.Contains(err.Error(), "extraction cap") {
		t.Fatalf("zip: expected an extraction-cap error, got %v", err)
	}
}

// TestNativeTarGzSkipsNonRegularEntries proves extractTarGz skips a symlink entry (no
// link escape) while still extracting a regular file and creating a directory entry.
func TestNativeTarGzSkipsNonRegularEntries(t *testing.T) {
	dest := t.TempDir()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "subdir/", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "evil-link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd", Mode: 0o777}); err != nil {
		t.Fatal(err)
	}
	payload := []byte("real")
	if err := tw.WriteHeader(&tar.Header{Name: "stub", Typeflag: tar.TypeReg, Mode: 0o755, Size: int64(len(payload))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatal(err)
	}
	_ = tw.Close()
	_ = gz.Close()

	if err := extractTarGz(buf.Bytes(), dest, maxExtractedBytes); err != nil {
		t.Fatalf("extractTarGz: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dest, "evil-link")); err == nil {
		t.Fatal("symlink entry was not skipped")
	}
	if _, err := os.Stat(filepath.Join(dest, "stub")); err != nil {
		t.Fatalf("regular entry not extracted: %v", err)
	}
	if st, err := os.Stat(filepath.Join(dest, "subdir")); err != nil || !st.IsDir() {
		t.Fatalf("directory entry not created: %v", err)
	}
}

// TestNativeZipSkipsNonRegularEntries proves extractZip skips a symlink entry while
// extracting a regular file and creating a directory entry.
func TestNativeZipSkipsNonRegularEntries(t *testing.T) {
	dest := t.TempDir()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	dh := &zip.FileHeader{Name: "subdir/"}
	dh.SetMode(os.ModeDir | 0o755)
	if _, err := zw.CreateHeader(dh); err != nil {
		t.Fatal(err)
	}
	lh := &zip.FileHeader{Name: "evil-link", Method: zip.Deflate}
	lh.SetMode(os.ModeSymlink | 0o777)
	lw, err := zw.CreateHeader(lh)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = lw.Write([]byte("/etc/passwd"))
	fh := &zip.FileHeader{Name: "stub", Method: zip.Deflate}
	fh.SetMode(0o755)
	fw, err := zw.CreateHeader(fh)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write([]byte("real"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	if err := extractZip(buf.Bytes(), dest, maxExtractedBytes); err != nil {
		t.Fatalf("extractZip: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(dest, "evil-link")); err == nil {
		t.Fatal("zip symlink entry was not skipped")
	}
	if _, err := os.Stat(filepath.Join(dest, "stub")); err != nil {
		t.Fatalf("zip regular entry not extracted: %v", err)
	}
	if st, err := os.Stat(filepath.Join(dest, "subdir")); err != nil || !st.IsDir() {
		t.Fatalf("zip directory entry not created: %v", err)
	}
}

// TestNativeDeployMissingDeclaredBin proves that an archive lacking the declared bin
// yields a clear error (the executable never materialises, so nothing runs).
func TestNativeDeployMissingDeclaredBin(t *testing.T) {
	p := newTestNativeProvider(t)
	art := tarGzArtifact(t, "someother", stubBinaryBytes(t)) // archive lacks the "stub" bin
	url := serveArtifact(t, p, art)
	svc := nativeService("nobin", url, sha256hex(art), "tar.gz")
	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: "nobin", Service: svc}, nil); err == nil ||
		!strings.Contains(err.Error(), "not found after extracting") {
		t.Fatalf("expected a missing-bin error, got %v", err)
	}
}

// TestNativeLogsTailAndAbsent covers Logs on an absent log (empty, no error) and on a
// present log (tail of the last N lines).
func TestNativeLogsTailAndAbsent(t *testing.T) {
	p := newTestNativeProvider(t)
	out, err := p.Logs(context.Background(), "ghost", 10)
	if err != nil || out != "" {
		t.Fatalf("absent log: out=%q err=%v", out, err)
	}
	logPath := filepath.Join(p.baseDir, "haslog", "log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("line-a\nline-b\nline-c\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = p.Logs(context.Background(), "haslog", 2)
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if !strings.Contains(out, "line-c") || !strings.Contains(out, "line-b") || strings.Contains(out, "line-a") {
		t.Fatalf("unexpected tail (want last 2 lines): %q", out)
	}
}

// TestReadFileTailSeeksPastCap exercises readFileTail's seek-to-tail branch for a file
// larger than the requested cap.
func TestReadFileTailSeeksPastCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "big")
	content := append(bytes.Repeat([]byte("x"), 100), []byte("\nTAIL")...)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := readFileTail(path, 10)
	if err != nil {
		t.Fatalf("readFileTail: %v", err)
	}
	if len(raw) != 10 || !strings.HasSuffix(string(raw), "TAIL") {
		t.Fatalf("expected the last 10 bytes ending in TAIL, got %q", raw)
	}
}

// TestNativeListToleratesCorruptRegistry proves a malformed state.json is treated as an
// empty registry (List never errors or panics on garbage on disk).
func TestNativeListToleratesCorruptRegistry(t *testing.T) {
	p := newTestNativeProvider(t)
	if err := os.MkdirAll(p.baseDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.statePath, []byte("{ this is not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}
	infos, err := p.List(context.Background())
	if err != nil {
		t.Fatalf("List on corrupt registry: %v", err)
	}
	if len(infos) != 0 {
		t.Fatalf("expected no entries from a corrupt registry, got %d", len(infos))
	}
}

// TestNativeStopUnknownAndAlreadyStopped covers Stop of an unknown slug (clear error)
// and of an already-stopped entry (no-op success, never signals).
func TestNativeStopUnknownAndAlreadyStopped(t *testing.T) {
	p := newTestNativeProvider(t)
	if err := p.Stop(context.Background(), "nope"); err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("Stop unknown: got %v", err)
	}
	if err := p.mutateRegistry(func(reg *nativeRegistry) {
		reg.Entries["done"] = &nativeRegistryEntry{Slug: "done", PID: 0, URL: "https://example.test/x", Desired: "stopped"}
	}); err != nil {
		t.Fatal(err)
	}
	if err := p.Stop(context.Background(), "done"); err != nil {
		t.Fatalf("Stop already-stopped: %v", err)
	}
}

// TestNativeRemoveUnknownIsNoError proves Remove of an unknown slug is a clean no-op.
func TestNativeRemoveUnknownIsNoError(t *testing.T) {
	p := newTestNativeProvider(t)
	if err := p.Remove(context.Background(), "ghost"); err != nil {
		t.Fatalf("Remove unknown: %v", err)
	}
}

// TestNativeStartErrorsAndOrphanNoop covers Start's not-deployed and missing-binary
// errors and its no-op for a still-recognised (identity-matching) orphan.
func TestNativeStartErrorsAndOrphanNoop(t *testing.T) {
	p := newTestNativeProvider(t)
	if err := p.Start(context.Background(), "nope"); err == nil || !strings.Contains(err.Error(), "not deployed") {
		t.Fatalf("Start not-deployed: %v", err)
	}
	if err := p.mutateRegistry(func(reg *nativeRegistry) {
		reg.Entries["gone"] = &nativeRegistryEntry{Slug: "gone", BinPath: filepath.Join(t.TempDir(), "absent"), URL: "https://example.test/x", Desired: "stopped"}
	}); err != nil {
		t.Fatal(err)
	}
	if err := p.Start(context.Background(), "gone"); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("Start missing-binary: %v", err)
	}

	binPath := filepath.Join(t.TempDir(), stubBinName())
	if err := os.WriteFile(binPath, stubBinaryBytes(t), 0o700); err != nil {
		t.Fatal(err)
	}
	// A recognised live orphan: the live identity matches what we persisted -> no-op.
	p.identityFn = func(int) (string, int64, bool) { return "/live/exe", 42, true }
	if err := p.mutateRegistry(func(reg *nativeRegistry) {
		reg.Entries["orph"] = &nativeRegistryEntry{Slug: "orph", PID: 12345, BinPath: binPath, ExePath: "/live/exe", CreateTime: 42, URL: "https://example.test/x", Desired: "running"}
	}); err != nil {
		t.Fatal(err)
	}
	if err := p.Start(context.Background(), "orph"); err != nil {
		t.Fatalf("Start recognised-orphan (want no-op): %v", err)
	}
	p.mu.Lock()
	_, launched := p.procs["orph"]
	p.mu.Unlock()
	if launched {
		t.Fatal("Start relaunched a still-running orphan")
	}
}

// TestNativeSupervisorStopsAfterMaxRestarts proves the supervisor gives up after
// maxRestarts respawns: a stub that exits immediately runs exactly maxRestarts+1 times.
func TestNativeSupervisorStopsAfterMaxRestarts(t *testing.T) {
	p := newTestNativeProvider(t)
	p.maxRestarts = 2
	p.backoffMin = 5 * time.Millisecond
	p.backoffMax = 10 * time.Millisecond
	art := stubBinaryBytes(t)
	url := serveArtifact(t, p, art)
	runs := filepath.Join(t.TempDir(), "runs")
	svc := nativeService("flap", url, sha256hex(art), "none")
	env := map[string]string{"CASHPILOT_NATIVE_STUB": "1", "CASHPILOT_STUB_EXIT": "1", "CASHPILOT_STUB_RUNS": runs}

	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: "flap", Service: svc, Env: env}, nil); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	t.Cleanup(func() { _ = p.Remove(context.Background(), "flap") })

	p.mu.Lock()
	mp := p.procs["flap"]
	p.mu.Unlock()
	if mp == nil {
		t.Fatal("no managed process recorded")
	}
	select {
	case <-mp.doneCh:
	case <-time.After(5 * time.Second):
		t.Fatal("supervisor did not stop after maxRestarts")
	}

	data, err := os.ReadFile(runs)
	if err != nil {
		t.Fatalf("read runs: %v", err)
	}
	if got := len(strings.Fields(strings.TrimSpace(string(data)))); got != 3 {
		t.Fatalf("stub ran %d times, want 3 (initial launch + maxRestarts=2)", got)
	}
}

// TestNativeMismatchedPIDNotSignalled is the PID-reuse guard proof: a registry entry
// whose recorded identity no longer matches the live process at that pid is reported
// stopped and is NEVER signalled — so a recycled pid cannot make us kill an unrelated
// same-UID process.
func TestNativeMismatchedPIDNotSignalled(t *testing.T) {
	p := newTestNativeProvider(t)

	// A real, live bystander process that must survive: it stands in for an unrelated
	// process the OS reassigned our old pid to.
	binPath := filepath.Join(t.TempDir(), stubBinName())
	if err := os.WriteFile(binPath, stubBinaryBytes(t), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binPath)
	cmd.Env = append(os.Environ(), "CASHPILOT_NATIVE_STUB=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start bystander: %v", err)
	}
	pid := cmd.Process.Pid
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-done })

	// The registry claims this pid was ours, WITH a recorded identity...
	if err := p.mutateRegistry(func(reg *nativeRegistry) {
		reg.Entries["reused"] = &nativeRegistryEntry{Slug: "reused", PID: pid, ExePath: "/original/exe", CreateTime: 1000, URL: "https://example.test/x", Desired: "running"}
	}); err != nil {
		t.Fatal(err)
	}
	// ...but the live process at that pid reports a DIFFERENT identity (pid reuse). Keep
	// bare liveness "alive" so a regression that ignored identity would wrongly signal.
	p.identityFn = func(int) (string, int64, bool) { return "/some/other/exe", 2000, true }
	p.statFn = func(int) (float64, float64, bool) { return 0, 0, true }

	if ci, _ := listEntry(t, p, "reused"); ci.Status != "stopped" {
		t.Fatalf("mismatched pid reported %q, want stopped", ci.Status)
	}
	if err := p.Stop(context.Background(), "reused"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	time.Sleep(200 * time.Millisecond)
	select {
	case <-done:
		t.Fatal("Stop signalled a pid whose identity did not match (killed an unrelated process)")
	default:
	}
	if ci, _ := listEntry(t, p, "reused"); ci.Status != "stopped" {
		t.Fatalf("after Stop, status %q, want stopped", ci.Status)
	}
}

// TestNativeRejectsBinEscapingSlugDir is the raw-write containment proof: a bin path that
// escapes the per-service directory is rejected before anything is written or executed.
func TestNativeRejectsBinEscapingSlugDir(t *testing.T) {
	p := newTestNativeProvider(t)
	art := stubBinaryBytes(t)
	url := serveArtifact(t, p, art)
	marker := filepath.Join(t.TempDir(), "marker")
	svc := nativeService("esc", url, sha256hex(art), "none")
	svc.Native.Binaries[0].Bin = "../../evil" // escapes <base>/esc

	_, err := p.Deploy(context.Background(), DeploySpec{Slug: "esc", Service: svc, Env: stubEnv(marker)}, nil)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "service directory") {
		t.Fatalf("expected a containment error, got %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("stub executed despite an escaping bin path")
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(p.baseDir), "evil")); statErr == nil {
		t.Fatal("an escaping bin path was written outside the service dir")
	}
	if _, ok := listEntry(t, p, "esc"); ok {
		t.Fatal("a rejected deploy was recorded in the registry")
	}
}

// TestNativeDownloadRefusesHTTPRedirect is the redirect-downgrade proof: a redirect from
// the pinned HTTPS URL to an http:// location is refused (no plaintext hop).
func TestNativeDownloadRefusesHTTPRedirect(t *testing.T) {
	p := newTestNativeProvider(t)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://downgrade.invalid/evil", http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	client := srv.Client()
	client.CheckRedirect = httpsOnlyRedirect
	p.httpClient = client

	_, err := p.download(context.Background(), srv.URL+"/x", strings.Repeat("0", 64))
	if err == nil {
		t.Fatal("expected the http:// redirect to be refused")
	}
	if !strings.Contains(strings.ToUpper(err.Error()), "HTTPS") {
		t.Fatalf("expected an HTTPS-downgrade refusal, got %v", err)
	}
}

// TestHTTPSOnlyRedirectPolicy unit-tests the redirect policy: https hops are allowed,
// non-https hops refused, and an over-long chain refused.
func TestHTTPSOnlyRedirectPolicy(t *testing.T) {
	mk := func(raw string) *http.Request {
		req, err := http.NewRequest(http.MethodGet, raw, nil)
		if err != nil {
			t.Fatalf("build request %q: %v", raw, err)
		}
		return req
	}
	if err := httpsOnlyRedirect(mk("https://ok.test/next"), nil); err != nil {
		t.Fatalf("https redirect should be allowed: %v", err)
	}
	if err := httpsOnlyRedirect(mk("http://bad.test/next"), nil); err == nil {
		t.Fatal("http redirect should be refused")
	}
	if err := httpsOnlyRedirect(mk("https://ok.test/loop"), make([]*http.Request, 10)); err == nil {
		t.Fatal("a redirect chain past the cap should be refused")
	}
}

// TestNativeStartNoopWhenManagedChildAlive covers Start's fast path: a still-alive
// managed child (procAlive) makes Start a no-op with no relaunch.
func TestNativeStartNoopWhenManagedChildAlive(t *testing.T) {
	p := newTestNativeProvider(t)
	art := stubBinaryBytes(t)
	url := serveArtifact(t, p, art)
	marker := filepath.Join(t.TempDir(), "marker")
	svc := nativeService("noop", url, sha256hex(art), "none")
	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: "noop", Service: svc, Env: stubEnv(marker)}, nil); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	t.Cleanup(func() { _ = p.Remove(context.Background(), "noop") })
	var pid1 string
	if !waitFor(t, 5*time.Second, func() bool {
		ci, ok := listEntry(t, p, "noop")
		if ok && ci.Status == "running" {
			pid1 = ci.ContainerID
			return true
		}
		return false
	}) {
		t.Fatal("service not running")
	}
	if err := p.Start(context.Background(), "noop"); err != nil {
		t.Fatalf("Start (no-op expected): %v", err)
	}
	ci, _ := listEntry(t, p, "noop")
	if ci.Status != "running" || ci.ContainerID != pid1 {
		t.Fatalf("Start relaunched instead of no-op: pid1=%s now=%+v", pid1, ci)
	}
}

// TestNativeRestartStopsThenStarts covers Restart (Stop then Start relaunches).
func TestNativeRestartStopsThenStarts(t *testing.T) {
	p := newTestNativeProvider(t)
	art := stubBinaryBytes(t)
	url := serveArtifact(t, p, art)
	marker := filepath.Join(t.TempDir(), "marker")
	svc := nativeService("rst", url, sha256hex(art), "none")
	if _, err := p.Deploy(context.Background(), DeploySpec{Slug: "rst", Service: svc, Env: stubEnv(marker)}, nil); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	t.Cleanup(func() { _ = p.Remove(context.Background(), "rst") })
	if !waitFor(t, 5*time.Second, func() bool {
		ci, ok := listEntry(t, p, "rst")
		return ok && ci.Status == "running"
	}) {
		t.Fatal("not running before restart")
	}
	if err := p.Restart(context.Background(), "rst"); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if !waitFor(t, 5*time.Second, func() bool {
		ci, ok := listEntry(t, p, "rst")
		return ok && ci.Status == "running"
	}) {
		t.Fatal("not running after restart")
	}
}

// TestNativeRemoveForceKillsOrphan covers Remove's force-stop of a registry orphan:
// the pid is force-killed, its per-slug dir removed, and its entry dropped.
func TestNativeRemoveForceKillsOrphan(t *testing.T) {
	p := newTestNativeProvider(t)
	binPath := filepath.Join(t.TempDir(), stubBinName())
	if err := os.WriteFile(binPath, stubBinaryBytes(t), 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binPath)
	cmd.Env = append(os.Environ(), "CASHPILOT_NATIVE_STUB=1")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start orphan: %v", err)
	}
	pid := cmd.Process.Pid
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); close(done) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-done })

	slugDir := filepath.Join(p.baseDir, "orphrm")
	if err := os.MkdirAll(slugDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := p.mutateRegistry(func(reg *nativeRegistry) {
		reg.Entries["orphrm"] = &nativeRegistryEntry{Slug: "orphrm", PID: pid, BinPath: binPath, URL: "https://example.test/x", Desired: "running"}
	}); err != nil {
		t.Fatal(err)
	}
	if err := p.Remove(context.Background(), "orphrm"); err != nil {
		t.Fatalf("Remove orphan: %v", err)
	}
	if !waitFor(t, 5*time.Second, func() bool { _, _, alive := p.statFn(pid); return !alive }) {
		t.Fatal("orphan still alive after force Remove")
	}
	if _, err := os.Stat(slugDir); err == nil {
		t.Fatal("slug dir not removed by Remove")
	}
	if _, ok := listEntry(t, p, "orphrm"); ok {
		t.Fatal("registry entry not dropped by Remove")
	}
}

// TestBuildNativeEnvResolvesDefaultsAndOverrides covers buildNativeEnv: {hostname}
// expansion, ${VAR} substitution, caller overrides, and skipping empty defaults.
func TestBuildNativeEnvResolvesDefaultsAndOverrides(t *testing.T) {
	svc := catalog.Service{Native: catalog.NativeConfig{Env: []catalog.EnvVar{
		{Key: "NODE", Default: "node-{hostname}"},
		{Key: "REF", Default: "${NODE}-suffix"},
		{Key: "EMPTY"},
	}}}
	env := buildNativeEnv(svc, map[string]string{"EXTRA": "v", "NODE": "override"})
	if env["NODE"] != "override" {
		t.Fatalf("override not applied: %q", env["NODE"])
	}
	if env["REF"] != "override-suffix" {
		t.Fatalf("substitution wrong: %q", env["REF"])
	}
	if env["EXTRA"] != "v" {
		t.Fatalf("extra override missing: %q", env["EXTRA"])
	}
	if _, ok := env["EMPTY"]; ok {
		t.Fatalf("an empty-default key should be skipped, got %q", env["EMPTY"])
	}
}

// TestNativeExtractAndIdentityHelpers covers extractArtifact's unsupported-format branch
// and processIdentity's pid<=0 and failed-sample branches.
func TestNativeExtractAndIdentityHelpers(t *testing.T) {
	if err := extractArtifact("rar", []byte("x"), t.TempDir(), filepath.Join(t.TempDir(), "b"), maxExtractedBytes); err == nil ||
		!strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("extractArtifact unsupported format: %v", err)
	}
	p := newTestNativeProvider(t)
	if exe, ct := p.processIdentity(0); exe != "" || ct != 0 {
		t.Fatalf("processIdentity(0) = %q,%d; want empty", exe, ct)
	}
	p.identityFn = func(int) (string, int64, bool) { return "ignored", 9, false }
	if exe, ct := p.processIdentity(123); exe != "" || ct != 0 {
		t.Fatalf("processIdentity failed-sample = %q,%d; want empty", exe, ct)
	}
}
