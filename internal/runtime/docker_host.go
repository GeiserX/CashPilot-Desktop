package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	goruntime "runtime"
	"sort"
	"strings"
)

// dockerEndpoint is where the Docker CLI would connect when DOCKER_HOST is
// unset, and the context it took that from. The moby client reads DOCKER_HOST
// and nothing else, so without this an app started from the Finder never finds
// colima, OrbStack or a Docker Desktop that only serves its own socket.
type dockerEndpoint struct {
	Host    string   // empty: the client's own default (/var/run/docker.sock)
	Context string   // the current Docker context's name, when one is set
	TLS     []string // the context's ca.pem, cert.pem and key.pem, "" when absent; nil: no TLS
}

// resolveDockerEndpoint finds the engine the way the docker CLI does: the
// current context (DOCKER_CONTEXT, then currentContext in the CLI's config), and
// with no context the default socket. When the default socket is missing it also
// tries the sockets the common runtimes create in the home folder. Windows keeps
// the client's default pipe, which Docker Desktop serves in every mode.
func resolveDockerEndpoint() dockerEndpoint {
	if goruntime.GOOS == "windows" || os.Getenv("DOCKER_HOST") != "" {
		return dockerEndpoint{}
	}
	home, _ := os.UserHomeDir()
	configDir := os.Getenv("DOCKER_CONFIG")
	if configDir == "" && home != "" {
		configDir = filepath.Join(home, ".docker")
	}
	name := currentDockerContext(configDir)
	if name != "" && name != "default" {
		if ep, ok := contextEndpoint(configDir, name); ok {
			return ep
		}
	}
	return dockerEndpoint{Host: fallbackSocket(home), Context: name}
}

// currentDockerContext is DOCKER_CONTEXT, or currentContext in config.json.
func currentDockerContext(configDir string) string {
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
// context store, contexts/meta/<sha256 of the name>/meta.json. An ssh endpoint
// needs the CLI's ssh helper, which this client does not have, so it does not
// count.
func contextEndpoint(configDir, name string) (dockerEndpoint, bool) {
	if configDir == "" {
		return dockerEndpoint{}, false
	}
	sum := sha256.Sum256([]byte(name))
	id := hex.EncodeToString(sum[:])
	raw, err := os.ReadFile(filepath.Join(configDir, "contexts", "meta", id, "meta.json"))
	if err != nil {
		return dockerEndpoint{}, false
	}
	var meta struct {
		Endpoints struct {
			Docker struct {
				Host string `json:"Host"`
			} `json:"docker"`
		} `json:"Endpoints"`
	}
	if json.Unmarshal(raw, &meta) != nil {
		return dockerEndpoint{}, false
	}
	host := strings.TrimSpace(meta.Endpoints.Docker.Host)
	if host == "" || strings.HasPrefix(host, "ssh://") {
		return dockerEndpoint{}, false
	}
	ep := dockerEndpoint{Host: host, Context: name}
	if strings.HasPrefix(host, "tcp://") {
		dir := filepath.Join(configDir, "contexts", "tls", id, "docker")
		found := false
		for _, file := range []string{"ca.pem", "cert.pem", "key.pem"} {
			path := filepath.Join(dir, file)
			if _, err := os.Stat(path); err != nil {
				path = ""
			}
			found = found || path != ""
			ep.TLS = append(ep.TLS, path)
		}
		if !found {
			ep.TLS = nil
		}
	}
	return ep, true
}

// defaultDockerSocket is the client's own default on macOS and Linux. A var so
// tests can point it at a temp folder.
var defaultDockerSocket = "/var/run/docker.sock"

// fallbackSocket is empty when the default socket exists, so the client keeps its
// default. Otherwise it is the first socket a common runtime left in the home
// folder: colima (default profile first, then the old single-profile path, then
// any other profile), Docker Desktop on macOS and Linux, and rootless Docker.
func fallbackSocket(home string) string {
	if isSocket(defaultDockerSocket) {
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
