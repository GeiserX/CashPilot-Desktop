//go:build !windows

package dockerhost

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

// currentContextAt stores a current context the way the docker CLI does, under a fresh
// DOCKER_CONFIG, and returns the folder its TLS files go in.
func currentContextAt(t *testing.T, name, host string, skipVerify bool) string {
	t.Helper()
	config := t.TempDir()
	for _, key := range []string{"DOCKER_HOST", "DOCKER_CONTEXT", "COLIMA_HOME", "XDG_RUNTIME_DIR"} {
		t.Setenv(key, "")
	}
	t.Setenv("DOCKER_CONFIG", config)
	t.Setenv("HOME", t.TempDir())
	sum := sha256.Sum256([]byte(name))
	id := hex.EncodeToString(sum[:])
	meta := filepath.Join(config, "contexts", "meta", id)
	if err := os.MkdirAll(meta, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"Name":"` + name + `","Metadata":{},"Endpoints":{"docker":{"Host":"` + host + `","SkipTLSVerify":` + strconv.FormatBool(skipVerify) + `}}}`
	if err := os.WriteFile(filepath.Join(meta, "meta.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(config, "config.json"), []byte(`{"currentContext":"`+name+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	tls := filepath.Join(config, "contexts", "tls", id, "docker")
	if err := os.MkdirAll(tls, 0o755); err != nil {
		t.Fatal(err)
	}
	return tls
}

// An ssh context is reported as one, with no host: the client cannot dial it, and
// a local socket in its place would run the earners somewhere else.
func TestAnSSHContextIsReportedNotReplaced(t *testing.T) {
	currentContextAt(t, "nas", "ssh://me@nas", false)
	if err := os.MkdirAll(filepath.Join(os.Getenv("HOME"), ".colima", "default"), 0o755); err != nil {
		t.Fatal(err)
	}
	ep := Resolve()
	if ep.SSH != "ssh://me@nas" || ep.Host != "" || ep.Context != "nas" || !ep.Remote() {
		t.Fatalf("Resolve = %+v, want the ssh context reported with no host", ep)
	}
}

// A tcp context keeps its SkipTLSVerify, and a client certificate without its key
// is left out, as the docker CLI does, instead of failing the whole client.
func TestATCPContextKeepsItsTLSSettings(t *testing.T) {
	dir := currentContextAt(t, "box", "tcp://10.0.0.5:2376", true)
	if err := os.WriteFile(filepath.Join(dir, "cert.pem"), []byte("not used without a key"), 0o600); err != nil {
		t.Fatal(err)
	}
	ep := Resolve()
	if ep.Host != "tcp://10.0.0.5:2376" || ep.TLS == nil || !ep.TLS.SkipVerify || ep.TLS.Cert == "" || ep.TLS.Key != "" || !ep.Remote() {
		t.Fatalf("Resolve = %+v (TLS %+v)", ep, ep.TLS)
	}
	cfg, err := ep.TLS.Config()
	if err != nil {
		t.Fatalf("Config: %v", err)
	}
	if !cfg.InsecureSkipVerify || len(cfg.Certificates) != 0 {
		t.Fatalf("Config = skip %v, %d certificates; want skip and none", cfg.InsecureSkipVerify, len(cfg.Certificates))
	}

	// A tcp context with no TLS files and no SkipTLSVerify is plain HTTP.
	currentContextAt(t, "plain", "tcp://10.0.0.5:2375", false)
	if ep := Resolve(); ep.TLS != nil {
		t.Fatalf("a plain tcp context got TLS %+v", ep.TLS)
	}
}

func TestRemote(t *testing.T) {
	for _, tc := range []struct {
		ep   Endpoint
		want bool
	}{
		{Endpoint{}, false},
		{Endpoint{Host: "unix:///Users/me/.colima/default/docker.sock"}, false},
		{Endpoint{Host: "npipe:////./pipe/docker_engine"}, false},
		{Endpoint{Host: "tcp://192.168.1.9:2375"}, true},
		{Endpoint{SSH: "ssh://me@nas"}, true},
	} {
		if got := tc.ep.Remote(); got != tc.want {
			t.Errorf("%+v.Remote() = %v, want %v", tc.ep, got, tc.want)
		}
	}
}
