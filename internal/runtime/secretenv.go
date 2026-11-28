package runtime

// Earner credentials outside Docker's container config (CASHPILOT_EARNER_SECRETS=file).
//
// Docker keeps a container's environment and command line in its config, so
// `docker inspect`, a copy of Docker's data folder and any tool with the Docker
// socket show every earner password in plain text. With this mode on, Desktop
// creates a catalog earner without the values of its secret variables
// (`secret: true` in its catalog entry):
//
//   - its environment has no secret variable, and its command line carries ${NAME}
//     where the catalog put a secret (Honeygain's -pass);
//   - between create and start, Desktop copies two files into the container at
//     /run/cashpilot-secret: env (the secret variables) and cashpilot-env (a static
//     program, cmd/cashpilot-env, embedded by internal/shimbin);
//   - the entrypoint is cashpilot-env, and the arguments are the earner's own
//     entrypoint and command. It sets the variables, fills each ${NAME} and execs the
//     earner. No shell is needed, so it works in any image.
//
// The files sit in the container's own writable layer, so `docker restart` and a
// reboot find them again and removing the container removes them. The web worker
// keeps them in a volume instead, because its socket proxy lets it write into a
// container only at that one path, which must then exist before the start.
//
// What this does not do: root on the host still reads the values from
// /proc/<pid>/environ and from the container's layer on disk, and any client with
// the Docker API can read the file with `docker cp` (the archive endpoint) or
// `docker exec`. It keeps them out of `docker inspect`, the container config and
// every copy of it.
//
// Off unless CASHPILOT_EARNER_SECRETS=file is set, so nothing changes for an existing
// install. A service opts out with docker.secrets_in_file: false; custom services
// always keep the plain environment. It is the same setting the web worker reads
// (app/secret_env.py in the CashPilot repository).

import (
	"archive/tar"
	"bytes"
	"log"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/secretshim"
	"github.com/GeiserX/CashPilot-Desktop/internal/shimbin"
	"github.com/moby/moby/api/types/container"
)

const (
	// EarnerSecretsEnv is the setting: "file" turns the mode on, anything else leaves it off.
	EarnerSecretsEnv = "CASHPILOT_EARNER_SECRETS"
	secretDir        = "/run/cashpilot-secret"
	secretShimName   = "cashpilot-env"
	secretEnvName    = "env"
	secretShimPath   = secretDir + "/" + secretShimName
	// labelSecretKeys holds the names (never the values) of the variables kept in the file.
	labelSecretKeys = "cashpilot.secret-env"
	// labelSecretEntrypoint is how many leading arguments were the earner's own
	// entrypoint, so its shape (container_drift.go) is the earner's.
	labelSecretEntrypoint = "cashpilot.secret-env-entrypoint"
)

// shimFor is the start program for an architecture family; tests replace it.
var shimFor = shimbin.For

// EarnerSecretsMode is the setting as this process reads it now: "file" or "env".
func EarnerSecretsMode() string {
	if strings.EqualFold(strings.TrimSpace(os.Getenv(EarnerSecretsEnv)), "file") {
		return "file"
	}
	return "env"
}

// EarnerSecretsReport is the mode as the heartbeat reports it: "unavailable" when the
// setting is on and this build carries no start program.
func EarnerSecretsReport() string {
	mode := EarnerSecretsMode()
	if mode == "file" && shimFor("amd64") == nil && shimFor("arm64") == nil {
		return "unavailable"
	}
	return mode
}

// secretKeys is the catalog's secret variables of svc, in catalog order. A wallet
// seed kept in its file (seedfile.go) never reaches the environment, so it is not one.
func secretKeys(svc catalog.Service) []string {
	var keys []string
	for _, item := range svc.Docker.Env {
		if item.Secret && item.SeedFile == nil && item.Key != "" {
			keys = append(keys, item.Key)
		}
	}
	return keys
}

// coversSecrets is whether the mode applies to svc at all.
func coversSecrets(svc catalog.Service, slug string) bool {
	if svc.Custom || catalog.IsCustomSlug(slug) {
		return false
	}
	if svc.Docker.SecretsInFile != nil && !*svc.Docker.SecretsInFile {
		return false
	}
	return len(secretKeys(svc)) > 0
}

// secretPlan is how one earner is created with its secrets in the file.
type secretPlan struct {
	env           []string
	entrypoint    []string
	cmd           []string
	keys          []string
	entrypointLen int
	file          []byte
	shim          []byte
	uid           int
	mode          int64
}

// imageRun is what the image runs and as whom, from its inspect.
type imageRun struct {
	Entrypoint, Cmd []string
	User            string
}

// planSecrets is the plan for this earner, or nil to create it as before: the mode is
// off, nothing is secret, or it cannot be done (logged). env is the full environment,
// cmd and entrypoint are what the container config would carry (nil keeps the image's).
func planSecrets(svc catalog.Service, slug string, env map[string]string, cmd, entrypoint []string, image imageRun, family string) *secretPlan {
	if EarnerSecretsMode() != "file" || !coversSecrets(svc, slug) {
		return nil
	}
	var vars []secretshim.Var
	for _, key := range secretKeys(svc) {
		if value := env[key]; value != "" {
			vars = append(vars, secretshim.Var{Name: key, Value: value})
		}
	}
	if len(vars) == 0 {
		return nil
	}
	shim := shimFor(family)
	if shim == nil {
		log.Printf("%s=file is set, but this build has no start program for %q; %s keeps its credentials in its environment", EarnerSecretsEnv, family, slug)
		return nil
	}
	secret := map[string]bool{}
	for _, v := range vars {
		if strings.ContainsRune(v.Value, 0) {
			log.Printf("A credential of %s holds a NUL byte; it keeps its credentials in its environment", slug)
			return nil
		}
		secret[v.Name] = true
	}
	var rest []string
	for key, value := range env {
		if secret[key] {
			continue
		}
		for _, v := range vars {
			if strings.Contains(value, v.Value) {
				// Another variable carries the secret: Docker would keep it after all.
				log.Printf("%s of %s holds a credential; it keeps its credentials in its environment", key, slug)
				return nil
			}
		}
		rest = append(rest, key+"="+value)
	}
	slices.Sort(rest)

	base := slices.Clone(image.Entrypoint)
	if entrypoint != nil {
		base = slices.Clone(entrypoint)
	}
	tokens := slices.Clone(cmd)
	if cmd == nil && entrypoint == nil {
		// Docker drops the image's CMD when the entrypoint is set on create.
		tokens = slices.Clone(image.Cmd)
	}
	for _, token := range append(slices.Clone(base), tokens...) {
		for _, v := range vars {
			if strings.Contains(token, "${"+v.Name+"}") {
				// The program would fill it in, changing an argument the earner was given as written.
				log.Printf("The command of %s already holds ${%s}; it keeps its credentials in its environment", slug, v.Name)
				return nil
			}
		}
	}
	for _, token := range base {
		for _, v := range vars {
			if strings.Contains(token, v.Value) {
				log.Printf("The entrypoint of %s holds a credential; it keeps its credentials in its environment", slug)
				return nil
			}
		}
	}
	for i, token := range tokens {
		tokens[i] = secretshim.Hide(token, vars)
	}
	argv := append(base, tokens...)
	if len(argv) == 0 {
		log.Printf("%s has no entrypoint or command to start; it keeps its credentials in its environment", slug)
		return nil
	}
	uid, mode := secretOwner(image.User)
	keys := make([]string, len(vars))
	for i, v := range vars {
		keys[i] = v.Name
	}
	return &secretPlan{
		env:           rest,
		entrypoint:    []string{secretShimPath},
		cmd:           argv,
		keys:          keys,
		entrypointLen: len(base),
		file:          secretshim.Encode(vars),
		shim:          shim,
		uid:           uid,
		mode:          mode,
	}
}

// secretOwner is the env file's owner and mode for the image's user. Root in the
// earner holds no CAP_DAC_OVERRIDE (every capability is dropped), so the file must
// belong to the user that reads it. A user named without its number cannot be
// resolved without the image's /etc/passwd: the file is then readable by every user
// in the container, which runs only the earner.
func secretOwner(user string) (int, int64) {
	name, _, _ := strings.Cut(strings.TrimSpace(user), ":")
	switch {
	case name == "" || name == "root" || name == "0":
		return 0, 0o400
	case isDigits(name):
		if uid, err := strconv.Atoi(name); err == nil {
			return uid, 0o400
		}
	}
	return 0, 0o444
}

func isDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// apply puts the plan into the container config.
func (p *secretPlan) apply(cfg *container.Config) {
	cfg.Env = slices.Clone(p.env)
	cfg.Entrypoint = slices.Clone(p.entrypoint)
	cfg.Cmd = slices.Clone(p.cmd)
	if cfg.Labels == nil {
		cfg.Labels = map[string]string{}
	}
	cfg.Labels[labelSecretKeys] = strings.Join(p.keys, ",")
	cfg.Labels[labelSecretEntrypoint] = strconv.Itoa(p.entrypointLen)
}

// archive is the folder as a tar for CopyToContainer at "/": the program for anyone
// to run, the variables for the earner's user. Only run/cashpilot-secret is in it,
// so the image's own /run keeps its owner and mode.
func (p *secretPlan) archive() []byte {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	now := time.Now()
	_ = tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: strings.TrimPrefix(secretDir, "/") + "/", Mode: 0o755, ModTime: now})
	for _, f := range []struct {
		name string
		data []byte
		uid  int
		mode int64
	}{
		{secretShimName, p.shim, 0, 0o555},
		{secretEnvName, p.file, p.uid, p.mode},
	} {
		_ = tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg,
			Name:     strings.TrimPrefix(secretDir, "/") + "/" + f.name,
			Size:     int64(len(f.data)),
			Mode:     f.mode,
			Uid:      f.uid,
			Gid:      f.uid,
			ModTime:  now,
		})
		_, _ = tw.Write(f.data)
	}
	_ = tw.Close()
	return buf.Bytes()
}

// secretShaped is whether a container was made by a plan.
func secretShaped(cfg *container.Config) bool {
	return cfg != nil && slices.Equal(cfg.Entrypoint, []string{secretShimPath}) && cfg.Labels[labelSecretKeys] != ""
}

// secretLogicalView is the earner's own entrypoint and the secret keys of a container
// made by a plan, for its shape.
func secretLogicalView(cfg *container.Config) ([]string, []string) {
	n, _ := strconv.Atoi(cfg.Labels[labelSecretEntrypoint])
	var entrypoint []string
	if n > 0 && n <= len(cfg.Cmd) {
		entrypoint = slices.Clone(cfg.Cmd[:n])
	}
	var keys []string
	for _, key := range strings.Split(cfg.Labels[labelSecretKeys], ",") {
		if key != "" {
			keys = append(keys, key)
		}
	}
	return entrypoint, keys
}
