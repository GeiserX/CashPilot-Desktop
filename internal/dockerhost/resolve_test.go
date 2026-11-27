//go:build !windows

package dockerhost

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// shortHome clears every input Resolve reads and returns a fresh home under /tmp:
// macOS caps a socket path at 104 bytes and its TempDir is long. The default
// socket points into it, so the machine's own /var/run/docker.sock never counts.
func shortHome(t *testing.T) string {
	t.Helper()
	home, err := os.MkdirTemp("/tmp", "dh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	for _, key := range []string{"DOCKER_HOST", "DOCKER_CONTEXT", "DOCKER_CONFIG", "COLIMA_HOME", "XDG_RUNTIME_DIR"} {
		t.Setenv(key, "")
	}
	t.Setenv("HOME", home)
	saved := DefaultSocket
	DefaultSocket = filepath.Join(home, "default.sock")
	t.Cleanup(func() { DefaultSocket = saved })
	return home
}

// listen makes a unix socket at path, as a runtime that serves Docker does.
func listen(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
}

// storeContext writes a context's meta.json under configDir, as the docker CLI
// does, with body as the whole file.
func storeContext(t *testing.T, configDir, name, body string) {
	t.Helper()
	sum := sha256.Sum256([]byte(name))
	dir := filepath.Join(configDir, "contexts", "meta", hex.EncodeToString(sum[:]))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func hostMeta(host string) string {
	return `{"Name":"x","Endpoints":{"docker":{"Host":"` + host + `"}}}`
}

func TestResolveLeavesDOCKER_HOSTToTheClient(t *testing.T) {
	shortHome(t)
	t.Setenv("DOCKER_HOST", "tcp://10.0.0.9:2375")
	if ep := Resolve(); ep != (Endpoint{}) {
		t.Fatalf("Resolve = %+v, want nothing while DOCKER_HOST is set", ep)
	}
}

// DOCKER_CONTEXT wins over config.json, and the context store defaults to
// ~/.docker when DOCKER_CONFIG is unset.
func TestResolveTakesDOCKER_CONTEXTFirst(t *testing.T) {
	home := shortHome(t)
	config := filepath.Join(home, ".docker")
	storeContext(t, config, "colima", hostMeta("unix:///tmp/colima.sock"))
	storeContext(t, config, "other", hostMeta("unix:///tmp/other.sock"))
	if err := os.WriteFile(filepath.Join(config, "config.json"), []byte(`{"currentContext":"other"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_CONTEXT", " colima ")
	ep := Resolve()
	if ep.Host != "unix:///tmp/colima.sock" || ep.Context != "colima" || ep.Remote() || ep.TLS != nil {
		t.Fatalf("Resolve = %+v, want the DOCKER_CONTEXT context", ep)
	}
}

// A context the store cannot give an address for falls back to the sockets, and
// keeps its name so the status can say which context was current.
func TestAContextWithNoUsableAddressFallsBackToTheSockets(t *testing.T) {
	for _, tc := range []struct {
		name, config, meta, wantContext string
	}{
		{name: "no config.json", wantContext: ""},
		{name: "config.json is not JSON", config: `{`, wantContext: ""},
		{name: "the default context", config: `{"currentContext":"default"}`, wantContext: "default"},
		{name: "no meta.json", config: `{"currentContext":"gone"}`, wantContext: "gone"},
		{name: "meta.json is not JSON", config: `{"currentContext":"gone"}`, meta: `{`, wantContext: "gone"},
		{name: "meta.json has no host", config: `{"currentContext":"gone"}`, meta: hostMeta(" "), wantContext: "gone"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := shortHome(t)
			config := filepath.Join(home, "cfg")
			if err := os.MkdirAll(config, 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("DOCKER_CONFIG", config)
			if tc.config != "" {
				if err := os.WriteFile(filepath.Join(config, "config.json"), []byte(tc.config), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if tc.meta != "" {
				storeContext(t, config, "gone", tc.meta)
			}
			sock := filepath.Join(home, ".colima", "default", "docker.sock")
			listen(t, sock)
			ep := Resolve()
			if ep.Host != "unix://"+sock || ep.Context != tc.wantContext || ep.SSH != "" {
				t.Fatalf("Resolve = %+v, want colima's socket and context %q", ep, tc.wantContext)
			}
		})
	}
}

// With no home folder and no DOCKER_CONFIG there is no context store to read,
// and XDG_RUNTIME_DIR is the only fallback left.
func TestResolveWithNoHomeFolder(t *testing.T) {
	dir := shortHome(t)
	t.Setenv("HOME", "")
	xdg := filepath.Join(dir, "xdg")
	listen(t, filepath.Join(xdg, "docker.sock"))
	t.Setenv("XDG_RUNTIME_DIR", xdg)
	if ep := Resolve(); ep.Host != "unix://"+filepath.Join(xdg, "docker.sock") || ep.Context != "" {
		t.Fatalf("Resolve = %+v, want the XDG socket and no context", ep)
	}
	t.Setenv("DOCKER_CONTEXT", "nas")
	ep := Resolve()
	if ep.Host != "unix://"+filepath.Join(xdg, "docker.sock") || ep.Context != "nas" {
		t.Fatalf("Resolve = %+v, want the XDG socket", ep)
	}
}

// The sockets are tried in the docker CLI users' usual order, and the default
// socket, when it is there, beats them all.
func TestFallbackSocketOrder(t *testing.T) {
	home := shortHome(t)
	colima := filepath.Join(home, ".colima")
	xdg := filepath.Join(home, "xdg")
	t.Setenv("XDG_RUNTIME_DIR", xdg)
	order := []string{
		filepath.Join(colima, "default", "docker.sock"),
		filepath.Join(colima, "docker.sock"),
		filepath.Join(colima, "a", "docker.sock"),
		filepath.Join(colima, "b", "docker.sock"),
		filepath.Join(home, ".docker", "run", "docker.sock"),
		filepath.Join(home, ".docker", "desktop", "docker.sock"),
		filepath.Join(xdg, "docker.sock"),
	}
	if got := fallbackSocket(home); got != "" {
		t.Fatalf("fallbackSocket = %q with no socket anywhere", got)
	}
	// Make them from the last to the first: each new one must win.
	for i := len(order) - 1; i >= 0; i-- {
		listen(t, order[i])
		if got := fallbackSocket(home); got != "unix://"+order[i] {
			t.Fatalf("fallbackSocket = %q, want %q", got, order[i])
		}
	}
	listen(t, DefaultSocket)
	if got := fallbackSocket(home); got != "" {
		t.Fatalf("fallbackSocket = %q, want the client's default while it exists", got)
	}
}

// COLIMA_HOME moves colima's sockets, and a symlink to a socket counts.
func TestFallbackSocketFollowsCOLIMA_HOMEAndSymlinks(t *testing.T) {
	home := shortHome(t)
	real := filepath.Join(home, "real.sock")
	listen(t, real)
	colima := filepath.Join(home, "elsewhere")
	if err := os.MkdirAll(filepath.Join(colima, "default"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(colima, "default", "docker.sock")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("COLIMA_HOME", colima)
	if got := fallbackSocket(home); got != "unix://"+link {
		t.Fatalf("fallbackSocket = %q, want the symlink under COLIMA_HOME", got)
	}
	// A plain file is not a socket.
	if err := os.WriteFile(filepath.Join(home, "file"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if isSocket(filepath.Join(home, "file")) {
		t.Fatal("a plain file counted as a socket")
	}
}

// writeCert writes a self-signed certificate and its key as PEM files in dir.
func writeCert(t *testing.T, dir string) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "docker"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certFile, keyFile = filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

// A verifying tcp context trusts its ca.pem and presents its client certificate.
func TestATCPContextWithACAAndAClientCertificate(t *testing.T) {
	dir := currentContextAt(t, "tls", "tcp://10.0.0.5:2376", false)
	cert, key := writeCert(t, dir)
	raw, err := os.ReadFile(cert)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ca.pem"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	ep := Resolve()
	if ep.TLS == nil || ep.TLS.CA == "" || ep.TLS.Cert != cert || ep.TLS.Key != key || ep.TLS.SkipVerify {
		t.Fatalf("Resolve = %+v (TLS %+v), want the context's three files", ep, ep.TLS)
	}
	cfg, err := ep.TLS.Config()
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	if cfg.RootCAs == nil || len(cfg.Certificates) != 1 || cfg.InsecureSkipVerify {
		t.Fatalf("Config = roots %v, %d certificates, skip %v", cfg.RootCAs != nil, len(cfg.Certificates), cfg.InsecureSkipVerify)
	}
}

// A TLS file that cannot be used fails the config with an error, instead of a
// client that silently skips the check.
func TestTLSConfigRefusesFilesItCannotUse(t *testing.T) {
	dir := t.TempDir()
	cert, key := writeCert(t, dir)
	junk := filepath.Join(dir, "junk.pem")
	if err := os.WriteFile(junk, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]*TLS{
		"a missing CA":      {CA: filepath.Join(dir, "missing.pem")},
		"a CA with no cert": {CA: junk},
		"a key that is not": {Cert: cert, Key: junk},
	} {
		if _, err := tc.Config(); err == nil {
			t.Errorf("%s: Config gave no error", name)
		}
	}
	// SkipVerify skips the CA, as the docker CLI does, so a broken one is ignored.
	cfg, err := (&TLS{CA: junk, Cert: cert, Key: key, SkipVerify: true}).Config()
	if err != nil || cfg.RootCAs != nil || len(cfg.Certificates) != 1 {
		t.Fatalf("Config with SkipVerify = %v, %v", cfg, err)
	}
}
