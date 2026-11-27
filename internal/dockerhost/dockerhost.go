// Package dockerhost finds the Docker engine the way the docker CLI does when
// DOCKER_HOST is unset. The moby client reads DOCKER_HOST and nothing else, so
// without this an app started from the Finder never finds colima, OrbStack or a
// Docker Desktop that only serves its own socket. It imports nothing of this
// module, so the runtime and the health checks can both ask it.
package dockerhost

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"
)

// Endpoint is where the Docker CLI would connect when DOCKER_HOST is unset, and
// the context it took that from.
type Endpoint struct {
	Host    string // empty: the client's own default (/var/run/docker.sock)
	Context string // the current Docker context's name, when one is set
	// SSH is the address of a current context that connects over ssh. The moby
	// client cannot dial it, so Host stays empty and the caller must refuse rather
	// than use a local engine the user did not choose.
	SSH string
	// TLS is the context's TLS for a tcp endpoint; nil means plain HTTP.
	TLS *TLS
}

// TLS is a context's TLS settings, as the docker CLI keeps them.
type TLS struct {
	CA, Cert, Key string // the context's ca.pem, cert.pem and key.pem; "" when absent
	SkipVerify    bool   // the context's SkipTLSVerify
}

// Config builds the client's TLS config as the docker CLI does: the CA when the
// context verifies, the client certificate only when both its files are there,
// and no verification when the context's SkipTLSVerify says so.
func (t *TLS) Config() (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: t.SkipVerify}
	if t.CA != "" && !t.SkipVerify {
		pem, err := os.ReadFile(t.CA)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("%s holds no certificate", t.CA)
		}
		cfg.RootCAs = pool
	}
	if t.Cert != "" && t.Key != "" {
		pair, err := tls.LoadX509KeyPair(t.Cert, t.Key)
		if err != nil {
			return nil, err
		}
		cfg.Certificates = []tls.Certificate{pair}
	}
	return cfg, nil
}

// Remote reports an endpoint on another machine: not a local socket or pipe.
func (e Endpoint) Remote() bool {
	return e.SSH != "" || (e.Host != "" && !strings.HasPrefix(e.Host, "unix://") && !strings.HasPrefix(e.Host, "npipe://"))
}

// DefaultSocket is the client's own default on macOS and Linux. A var so tests
// can point it at a temp folder.
var DefaultSocket = "/var/run/docker.sock"

// Resolve finds the engine the way the docker CLI does: the current context
// (DOCKER_CONTEXT, then currentContext in the CLI's config), and with no context
// the default socket. When the default socket is missing it also tries the
// sockets the common runtimes create in the home folder. Windows keeps the
// client's default pipe, which Docker Desktop serves in every mode. With
// DOCKER_HOST set it returns nothing: the client reads that itself.
func Resolve() Endpoint {
	if goruntime.GOOS == "windows" || os.Getenv("DOCKER_HOST") != "" {
		return Endpoint{}
	}
	home, _ := os.UserHomeDir()
	configDir := os.Getenv("DOCKER_CONFIG")
	if configDir == "" && home != "" {
		configDir = filepath.Join(home, ".docker")
	}
	name := currentContext(configDir)
	if name != "" && name != "default" {
		if ep, ok := contextEndpoint(configDir, name); ok {
			return ep
		}
	}
	return Endpoint{Host: fallbackSocket(home), Context: name}
}

// currentContext is DOCKER_CONTEXT, or currentContext in config.json.
func currentContext(configDir string) string {
	if name := strings.TrimSpace(os.Getenv("DOCKER_CONTEXT")); name != "" {
		return name
	}
	if configDir == "" {
		return ""
	}
	raw, err := os.ReadFile(filepath.Join(configDir, "config.json"))
	if err != nil {
		return ""
	}
	var cfg struct {
		CurrentContext string `json:"currentContext"`
	}
	if json.Unmarshal(raw, &cfg) != nil {
		return ""
	}
	return strings.TrimSpace(cfg.CurrentContext)
}

// contextEndpoint reads the docker endpoint of a named context from the CLI's
// context store, contexts/meta/<sha256 of the name>/meta.json.
func contextEndpoint(configDir, name string) (Endpoint, bool) {
	if configDir == "" {
		return Endpoint{}, false
	}
	sum := sha256.Sum256([]byte(name))
	id := hex.EncodeToString(sum[:])
	raw, err := os.ReadFile(filepath.Join(configDir, "contexts", "meta", id, "meta.json"))
	if err != nil {
		return Endpoint{}, false
	}
	var meta struct {
		Endpoints struct {
			Docker struct {
				Host          string `json:"Host"`
				SkipTLSVerify bool   `json:"SkipTLSVerify"`
			} `json:"docker"`
		} `json:"Endpoints"`
	}
	if json.Unmarshal(raw, &meta) != nil {
		return Endpoint{}, false
	}
	host := strings.TrimSpace(meta.Endpoints.Docker.Host)
	switch {
	case host == "":
		return Endpoint{}, false
	case strings.HasPrefix(host, "ssh://"):
		return Endpoint{Context: name, SSH: host}, true
	}
	ep := Endpoint{Host: host, Context: name}
	if strings.HasPrefix(host, "tcp://") {
		dir := filepath.Join(configDir, "contexts", "tls", id, "docker")
		file := func(base string) string {
			path := filepath.Join(dir, base)
			if _, err := os.Stat(path); err != nil {
				return ""
			}
			return path
		}
		t := &TLS{CA: file("ca.pem"), Cert: file("cert.pem"), Key: file("key.pem"), SkipVerify: meta.Endpoints.Docker.SkipTLSVerify}
		if t.CA != "" || t.Cert != "" || t.Key != "" || t.SkipVerify {
			ep.TLS = t
		}
	}
	return ep, true
}

// fallbackSocket is empty when the default socket exists, so the client keeps its
// default. Otherwise it is the first socket a common runtime left in the home
// folder: colima (default profile first, then the old single-profile path, then
// any other profile), Docker Desktop on macOS and Linux, and rootless Docker.
func fallbackSocket(home string) string {
	if isSocket(DefaultSocket) {
		return ""
	}
	var candidates []string
	colima := os.Getenv("COLIMA_HOME")
	if colima == "" && home != "" {
		colima = filepath.Join(home, ".colima")
	}
	if colima != "" {
		candidates = append(candidates,
			filepath.Join(colima, "default", "docker.sock"),
			filepath.Join(colima, "docker.sock"))
		profiles, _ := filepath.Glob(filepath.Join(colima, "*", "docker.sock"))
		sort.Strings(profiles)
		candidates = append(candidates, profiles...)
	}
	if home != "" {
		candidates = append(candidates,
			filepath.Join(home, ".docker", "run", "docker.sock"),
			filepath.Join(home, ".docker", "desktop", "docker.sock"))
	}
	if xdg := os.Getenv("XDG_RUNTIME_DIR"); xdg != "" {
		candidates = append(candidates, filepath.Join(xdg, "docker.sock"))
	}
	for _, path := range candidates {
		if isSocket(path) {
			return "unix://" + path
		}
	}
	return ""
}

// isSocket follows symlinks: colima's ~/.colima/default/docker.sock is one on
// some installs.
func isSocket(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode()&fs.ModeSocket != 0
}
