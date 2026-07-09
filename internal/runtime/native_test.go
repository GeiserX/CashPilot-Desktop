package runtime

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	p.httpClient = srv.Client()
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

	if err := extractTarGz(buf.Bytes(), dest); err != nil {
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
