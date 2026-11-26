package runtime

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/siaseed"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
	"gopkg.in/yaml.v3"
)

// A wallet seed kept in a file in the service's own data volume, and nowhere else
// (the Desktop half of GeiserX/CashPilot's app/wallet_seed.py).
//
// Sia's hostd needs a seed phrase, and the phrase IS the wallet: whoever reads it can
// spend the node's Siacoin and its locked collateral. It used to be a secret env var,
// which Docker keeps in plain text in the container's settings (docker inspect,
// config.v2.json, /proc/<pid>/environ). The catalog now marks the variable with
// seed_file (catalog.SeedFile), and the phrase lives only in that file:
//
//   - New wallet. The service's own image makes one, offline, in a throwaway
//     container; the phrase goes into the file and back to the caller once, so the
//     owner can write it down.
//   - Existing wallet. The owner pastes the phrase; it is checked, then written.
//   - Keep. A redeploy finds the file and leaves it alone.
//   - Migration. A node deployed before this ran with the phrase in its env (and
//     Desktop kept a copy). When the file has none, that phrase is written into it.
//
// The file is read and written through a helper container that mounts the volume
// and is never started, so the write goes through the daemon on every platform
// (Docker Desktop and Podman keep volumes inside their VM, where no host path
// reaches). buildEnv drops the variable, so it never reaches a container again.
//
// Nothing here logs a phrase, puts one in an error or a progress line, or keeps one
// past the call that handles it.

// The wallet choices a deploy can make.
const (
	// WalletGenerate makes a new wallet when the node has none.
	WalletGenerate = "generate"
	// WalletImport writes the phrase the owner pasted.
	WalletImport = "import"
	// WalletKeep uses the wallet the node already has.
	WalletKeep = "keep"
)

// LabelWalletHelper marks the containers that read, write or make a wallet, so a
// leftover one is recognisable and is never taken for a service.
const LabelWalletHelper = "cashpilot.wallet-helper"

// walletGenerateTimeout bounds the generator. It only prints two lines.
const walletGenerateTimeout = 60 * time.Second

// maxSeedFileBytes caps what is read back from the file or the generator.
const maxSeedFileBytes = 64 << 10

// WalletRequest is what a deploy asks of a service's wallet seed file. A nil one is
// WalletKeep.
type WalletRequest struct {
	Mode string
	// Phrase is the pasted phrase, for WalletImport only.
	Phrase string
	// Stored is the phrase Desktop kept for this service before the file existed. It
	// is written into the file when the file has none and the running container
	// carries no phrase of its own; it never replaces a file's phrase.
	Stored string
}

// WalletResult is what the deploy did with the wallet. It is never persisted and
// never serialised as part of anything else: json:"-" on ContainerInfo.Wallet.
type WalletResult struct {
	// Created is true when this deploy wrote the file.
	Created bool
	// Migrated is true when what it wrote is the phrase the node ran with before.
	Migrated bool
	// Address is the wallet's public address, "" when it cannot be worked out.
	Address string
	// Phrase is set only for a wallet made on this deploy, to be shown once.
	Phrase string
	// undo takes a new or pasted wallet back out of the file after the deploy that
	// wrote it failed; nil when nothing needs undoing.
	undo func(cause error) error
}

// Undo takes back what the wallet step wrote when the deploy then failed with
// cause, and answers the error to return. Safe on nil.
func (r *WalletResult) Undo(cause error) error {
	if r == nil || r.undo == nil || cause == nil {
		return cause
	}
	return r.undo(cause)
}

// WalletError is a wallet step that must not go ahead. Its message never holds a
// phrase. Code is the same code the web worker answers with.
type WalletError struct {
	Code    string
	Message string
}

func (e *WalletError) Error() string { return e.Message }

func walletErr(code, format string, args ...any) error {
	return &WalletError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// seedClient is the part of the Docker client the wallet steps need, so a test can
// stand in for the daemon.
type seedClient interface {
	ContainerCreate(ctx context.Context, options client.ContainerCreateOptions) (client.ContainerCreateResult, error)
	ContainerStart(ctx context.Context, containerID string, options client.ContainerStartOptions) (client.ContainerStartResult, error)
	ContainerWait(ctx context.Context, containerID string, options client.ContainerWaitOptions) client.ContainerWaitResult
	ContainerLogs(ctx context.Context, containerID string, options client.ContainerLogsOptions) (client.ContainerLogsResult, error)
	ContainerRemove(ctx context.Context, containerID string, options client.ContainerRemoveOptions) (client.ContainerRemoveResult, error)
	ContainerInspect(ctx context.Context, containerID string, options client.ContainerInspectOptions) (client.ContainerInspectResult, error)
	CopyToContainer(ctx context.Context, containerID string, options client.CopyToContainerOptions) (client.CopyToContainerResult, error)
	CopyFromContainer(ctx context.Context, containerID string, options client.CopyFromContainerOptions) (client.CopyFromContainerResult, error)
}

// StripSeedVars returns env without the value of any variable svc keeps in a seed
// file. The map passed in is not changed.
func StripSeedVars(svc catalog.Service, env map[string]string) map[string]string {
	item, ok := svc.SeedVar()
	if !ok {
		return env
	}
	if _, present := env[item.Key]; !present {
		return env
	}
	out := make(map[string]string, len(env))
	for k, v := range env {
		if k != item.Key {
			out[k] = v
		}
	}
	return out
}

// walletAddress is the public address of phrase's wallet when svc's chain is one
// Desktop can work it out for (Sia), else "".
func walletAddress(svc catalog.Service, phrase string) string {
	if !strings.EqualFold(svc.Payout.Chain, "sia") {
		return ""
	}
	address, err := siaseed.Address(phrase)
	if err != nil {
		return ""
	}
	return address
}

// checkPhrase is siaseed.Problem as a WalletError.
func checkPhrase(phrase string, words int) error {
	if problem := siaseed.Problem(phrase, words); problem != "" {
		return walletErr("wallet_phrase", "That seed phrase cannot be used: %s. Nothing was deployed.", problem)
	}
	return nil
}

// CheckWalletRequest refuses a request that cannot go ahead before anything is
// touched: an unknown mode, or a pasted phrase that is not a valid one.
func CheckWalletRequest(svc catalog.Service, req *WalletRequest) error {
	item, ok := svc.SeedVar()
	if !ok || req == nil {
		return nil
	}
	if problem := svc.SeedFileProblem(); problem != "" {
		return fmt.Errorf("the catalog entry's seed_file is not valid (%s), so the wallet cannot be kept safely. Nothing was deployed", problem)
	}
	switch req.Mode {
	case WalletGenerate, WalletKeep, "":
		return nil
	case WalletImport:
		if strings.TrimSpace(req.Phrase) == "" {
			return walletErr("wallet_phrase", "Paste the seed phrase of the wallet to use. Nothing was deployed.")
		}
		return checkPhrase(req.Phrase, item.SeedFile.Words)
	}
	return walletErr("wallet_mode", "Unknown wallet choice %q. Nothing was deployed.", req.Mode)
}

// seedEnvValue is the value of key in Docker's []string{"KEY=value"}, or "".
func seedEnvValue(entries []string, key string) string {
	for _, entry := range entries {
		if k, v, ok := strings.Cut(entry, "="); ok && k == key {
			return v
		}
	}
	return ""
}

// prepareWallet puts the wallet in its file before the service's container is
// created. mounts are the mounts that container will have; liveSeed is the phrase
// the container it replaces carries in its env ("" when none). It returns nil, nil
// for a service with no seed file. Every refusal comes before anything is written.
func prepareWallet(ctx context.Context, cli seedClient, svc catalog.Service, image, name string, mounts []mount.Mount, req *WalletRequest, liveSeed string, progress func(string)) (*WalletResult, error) {
	item, ok := svc.SeedVar()
	if !ok {
		return nil, nil
	}
	if err := CheckWalletRequest(svc, req); err != nil {
		return nil, err
	}
	if problem := svc.SeedFileProblem(); problem != "" {
		return nil, fmt.Errorf("the catalog entry's seed_file is not valid (%s), so the wallet cannot be kept safely. Nothing was deployed", problem)
	}
	spec := *item.SeedFile
	mode, pasted, stored := WalletKeep, "", ""
	if req != nil {
		if req.Mode != "" {
			mode = req.Mode
		}
		pasted = siaseed.Normalize(req.Phrase)
		stored = siaseed.Normalize(req.Stored)
	}

	folder := path.Dir(spec.Path)
	var volume *mount.Mount
	for i := range mounts {
		if strings.TrimRight(mounts[i].Target, "/") == folder {
			volume = &mounts[i]
			break
		}
	}
	if volume == nil {
		return nil, walletErr("wallet_volume", "This deploy mounts nothing at %s, where %s keeps its wallet. Nothing was deployed.", folder, svc.Name)
	}

	helper, err := createFileHelper(ctx, cli, image, name+"-wallet", *volume, folder)
	if err != nil {
		return nil, err
	}
	defer removeHelper(cli, helper)

	raw, err := readContainerFile(ctx, cli, helper, spec.Path)
	if err != nil {
		return nil, fmt.Errorf("could not read %s's wallet file (%v). Nothing was deployed", svc.Name, err)
	}
	data, current, err := parseSeedFile(raw, spec.Key)
	if err != nil {
		return nil, err
	}
	if current != "" {
		if mode == WalletImport && pasted != current {
			return nil, walletErr("wallet_exists", "This node already has another wallet, and CashPilot does not replace one. Reveal its seed phrase from the service's page first; to change wallets, remove the service with its data. Nothing was deployed.")
		}
		return &WalletResult{Address: walletAddress(svc, current)}, nil
	}

	// The file has no phrase. What the node ran with before (the replaced
	// container's env, then the app's stored copy) wins over everything: that
	// wallet holds the funds, and a new or pasted one would leave them behind with
	// their only copy deleted. A pasted phrase that is the same wallet is the move.
	legacy := ""
	for _, candidate := range []string{siaseed.Normalize(liveSeed), stored} {
		if candidate != "" && siaseed.Problem(candidate, spec.Words) == "" {
			legacy = candidate
			break
		}
	}
	var phrase, address string
	migrated := false
	switch {
	case legacy != "" && mode == WalletImport && pasted != legacy:
		return nil, walletErr("wallet_exists", "This node already runs another wallet, and CashPilot does not replace one. Deploy it once with \"Keep this node's wallet\", which moves that wallet into its data volume, and reveal its seed phrase from the service's card; to change wallets, remove the service with its data. Nothing was deployed.")
	case legacy != "":
		phrase, migrated = legacy, true
	case mode == WalletImport:
		phrase = pasted
	case mode == WalletKeep:
		return nil, walletErr("wallet_missing", "%s has no wallet yet. Deploy it from its card and choose a new wallet, or paste the seed phrase of the one you have. Nothing was deployed.", svc.Name)
	default:
		if progress != nil {
			progress("Creating a new wallet, offline")
		}
		phrase, address, err = generateWallet(ctx, cli, image, name+"-wallet-new", spec)
		if err != nil {
			return nil, err
		}
	}

	if data == nil {
		data = map[string]any{}
	}
	data[spec.Key] = phrase
	content, err := yaml.Marshal(data)
	if err != nil {
		return nil, errors.New("could not write the wallet file. Nothing was deployed")
	}
	archive, err := secretFileTar(path.Base(spec.Path), content)
	if err != nil {
		return nil, errors.New("could not write the wallet file. Nothing was deployed")
	}
	if progress != nil {
		progress("Writing the wallet into " + svc.Name + "'s data volume")
	}
	if _, err := cli.CopyToContainer(ctx, helper, client.CopyToContainerOptions{DestinationPath: folder, Content: bytes.NewReader(archive)}); err != nil {
		return nil, walletErr("wallet_write", "Docker did not write the wallet file (%v). Nothing was deployed.", err)
	}
	// What the file held before this write: the bytes it had, or, when there was
	// none, an empty mapping (hostd reads it as no settings, the next deploy as no
	// wallet).
	prior := raw
	if prior == nil {
		prior = []byte("{}\n")
	}
	back, err := readContainerFile(ctx, cli, helper, spec.Path)
	if err != nil {
		return nil, restoreSeedFile(cli, helper, folder, spec.Path, prior, walletErr("wallet_write", "The wallet file could not be read back (%v). Nothing was deployed.", err))
	}
	if _, got, err := parseSeedFile(back, spec.Key); err != nil || got != phrase {
		return nil, restoreSeedFile(cli, helper, folder, spec.Path, prior, walletErr("wallet_write", "The wallet file did not read back as written. Nothing was deployed."))
	}
	if address == "" {
		address = walletAddress(svc, phrase)
	}
	result := &WalletResult{Created: true, Migrated: migrated, Address: address}
	if mode == WalletGenerate && !migrated {
		result.Phrase = phrase
	}
	if !migrated {
		// A new or pasted wallet is taken back out if the deploy then fails: the
		// owner never saw a new one, and the next deploy must not keep it silently.
		// A moved one stays: it is the wallet the node already ran.
		result.undo = func(cause error) error {
			return undoWallet(cli, image, name, *volume, folder, spec.Path, prior, cause)
		}
	}
	return result, nil
}

// undoWallet puts back the bytes the wallet file held before a deploy wrote a new
// wallet into it (prior), after cause stopped that deploy. It uses a fresh
// context: a cancelled deploy is still undone. The answer is cause, with a line
// when the undo itself failed.
func undoWallet(cli seedClient, image, name string, volume mount.Mount, folder, file string, prior []byte, cause error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	helper, err := createFileHelper(ctx, cli, image, name+"-wallet", volume, folder)
	if err != nil {
		return walletKept(cause, file)
	}
	defer removeHelper(cli, helper)
	return restoreSeedFile(cli, helper, folder, file, prior, cause)
}

// restoreSeedFile writes prior back as file through helper, after cause, and
// answers cause, with a line when the write failed.
func restoreSeedFile(cli seedClient, helper, folder, file string, prior []byte, cause error) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	archive, err := secretFileTar(path.Base(file), prior)
	if err == nil {
		_, err = cli.CopyToContainer(ctx, helper, client.CopyToContainerOptions{DestinationPath: folder, Content: bytes.NewReader(archive)})
	}
	if err != nil {
		return walletKept(cause, file)
	}
	return cause
}

// walletKept is cause with the line that the new wallet may still be in file.
func walletKept(cause error, file string) error {
	return fmt.Errorf("%w. The new wallet stays in %s in the node's data volume and its seed phrase was not shown: deploy again and keep this node's wallet, then reveal its seed phrase from the service's card", cause, file)
}

// createFileHelper creates, and never starts, a container that mounts volume at
// folder: no network, no capabilities, local logs. The archive API reads and writes
// a stopped container's volumes.
func createFileHelper(ctx context.Context, cli seedClient, image, name string, volume mount.Mount, folder string) (string, error) {
	if err := removeLeftoverHelper(ctx, cli, name); err != nil {
		return "", err
	}
	volume.Target = folder
	volume.ReadOnly = false
	created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: name,
		Config: &container.Config{
			Image:           image,
			Entrypoint:      []string{"true"},
			Labels:          map[string]string{LabelWalletHelper: "file"},
			NetworkDisabled: true,
		},
		HostConfig: helperHostConfig([]mount.Mount{volume}),
	})
	if err != nil {
		return "", fmt.Errorf("could not create the helper that reads the wallet file (%v). Nothing was deployed", err)
	}
	return created.ID, nil
}

// helperHostConfig is the HostConfig of a wallet helper: no network, no
// capabilities, nothing to escalate with, and a json-file log, which is deleted with
// the container. With the daemon's default driver (journald, syslog, a log service)
// whatever a helper prints would outlive it, and the generator prints the phrase.
func helperHostConfig(mounts []mount.Mount) *container.HostConfig {
	pids := int64(64)
	return &container.HostConfig{
		NetworkMode: "none",
		Mounts:      mounts,
		CapDrop:     []string{"ALL"},
		SecurityOpt: []string{"no-new-privileges:true"},
		LogConfig:   container.LogConfig{Type: "json-file"},
		Resources:   container.Resources{Memory: 256 << 20, PidsLimit: &pids},
	}
}

// removeLeftoverHelper removes a wallet helper a deploy that died midway left under
// name. Anything else under the name is not ours, and stops the deploy.
func removeLeftoverHelper(ctx context.Context, cli seedClient, name string) error {
	got, err := cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("could not check %s (%v). Nothing was deployed", name, err)
	}
	if got.Container.Config == nil || got.Container.Config.Labels[LabelWalletHelper] == "" {
		return fmt.Errorf("%s belongs to something else, so the wallet helper cannot use the name. Remove or rename it. Nothing was deployed", name)
	}
	removeHelper(cli, got.Container.ID)
	return nil
}

// removeHelper removes a helper, with a fresh context so a cancelled deploy still
// cleans up. Its anonymous volumes go with it; a named one never does.
func removeHelper(cli seedClient, id string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _ = cli.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
}

// generateWallet runs the image's own generator, offline, in a container that is
// removed as soon as its output is read, and returns the phrase and the address it
// printed.
func generateWallet(ctx context.Context, cli seedClient, image, name string, spec catalog.SeedFile) (string, string, error) {
	if err := removeLeftoverHelper(ctx, cli, name); err != nil {
		return "", "", err
	}
	host := helperHostConfig(nil)
	// The image keeps its data under a VOLUME. A tmpfs there means the generator
	// leaves no anonymous volume behind and writes nothing to disk.
	host.Tmpfs = map[string]string{path.Dir(spec.Path): "size=16m"}
	created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: name,
		Config: &container.Config{
			Image:           image,
			Cmd:             append([]string(nil), spec.Generate...),
			Labels:          map[string]string{LabelWalletHelper: "generate"},
			NetworkDisabled: true,
			// It runs the service's own image, so it gets the void stamp like the
			// service's container (gpu.go): no GPU through an nvidia default runtime.
			Env: []string{nvidiaVisible + "=void"},
		},
		HostConfig: host,
	})
	if err != nil {
		return "", "", walletErr("wallet_generate", "Docker could not create the container that makes a new wallet (%v). Nothing was deployed.", err)
	}
	defer removeHelper(cli, created.ID)

	waitCtx, cancel := context.WithTimeout(ctx, walletGenerateTimeout)
	defer cancel()
	// Registered before the start, so an exit that comes at once is not missed.
	wait := cli.ContainerWait(waitCtx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNextExit})
	if _, err := cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return "", "", walletErr("wallet_generate", "Docker could not start the container that makes a new wallet (%v). Nothing was deployed.", err)
	}
	select {
	case res := <-wait.Result:
		if res.StatusCode != 0 {
			return "", "", walletErr("wallet_generate", "The node's image could not make a wallet (exit %d). Nothing was deployed.", res.StatusCode)
		}
	case err := <-wait.Error:
		return "", "", walletErr("wallet_generate", "Making a new wallet did not finish (%v). Nothing was deployed.", err)
	}
	logs, err := cli.ContainerLogs(ctx, created.ID, client.ContainerLogsOptions{ShowStdout: true})
	if err != nil {
		return "", "", walletErr("wallet_generate", "The new wallet could not be read from its container (%v). Nothing was deployed.", err)
	}
	defer logs.Close()
	var stdout bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, io.Discard, io.LimitReader(logs, maxSeedFileBytes)); err != nil {
		return "", "", walletErr("wallet_generate", "The new wallet could not be read from its container. Nothing was deployed.")
	}
	output := stdout.String()
	stdout.Reset()
	phraseRe, err1 := regexp.Compile(spec.PhrasePattern)
	addressRe, err2 := regexp.Compile(spec.AddressPattern)
	if err1 != nil || err2 != nil {
		return "", "", errors.New("the catalog entry's seed_file patterns do not compile. Nothing was deployed")
	}
	phrase := ""
	if m := phraseRe.FindStringSubmatch(output); len(m) == 2 {
		phrase = siaseed.Normalize(m[1])
	}
	if phrase == "" || siaseed.Problem(phrase, spec.Words) != "" {
		return "", "", walletErr("wallet_generate", "The node's image did not print a wallet CashPilot could read. Nothing was deployed.")
	}
	address := ""
	if m := addressRe.FindStringSubmatch(output); len(m) == 2 {
		address = m[1]
	}
	return phrase, address, nil
}

// readContainerFile is the file at p in container id, nil when there is none.
func readContainerFile(ctx context.Context, cli seedClient, id, p string) ([]byte, error) {
	res, err := cli.CopyFromContainer(ctx, id, client.CopyFromContainerOptions{SourcePath: p})
	if cerrdefs.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer res.Content.Close()
	tr := tar.NewReader(io.LimitReader(res.Content, maxSeedFileBytes*2))
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		if hdr.Size > maxSeedFileBytes {
			return nil, errors.New("the file is larger than a wallet file can be")
		}
		return io.ReadAll(tr)
	}
}

// parseSeedFile is the file's YAML mapping and the phrase under key ("" when the
// file or the key is absent). A file that is not a YAML mapping is refused rather
// than overwritten: it is the node's config, and may hold more than the phrase.
func parseSeedFile(raw []byte, key string) (map[string]any, string, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, "", nil
	}
	var data map[string]any
	if err := yaml.Unmarshal(raw, &data); err != nil {
		return nil, "", walletErr("wallet_file_invalid", "The node's config file is not a YAML mapping, so CashPilot did not change it. Fix or remove it, then deploy again. Nothing was deployed.")
	}
	value, _ := data[key].(string)
	return data, siaseed.Normalize(value), nil
}

// secretFileTar is a tar holding one file, owned by root and readable by root only.
func secretFileTar(name string, content []byte) ([]byte, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(content)), Uid: 0, Gid: 0, ModTime: time.Now(), Typeflag: tar.TypeReg}); err != nil {
		return nil, err
	}
	if _, err := tw.Write(content); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// SeedReader is a runtime that can read a deployed service's wallet seed file back
// (the Docker one).
type SeedReader interface {
	ReadWalletSeed(ctx context.Context, slug string, svc catalog.Service) (string, error)
}

// ReadWalletSeed is the phrase in the seed file of slug's container, running or not.
func (p *DockerProvider) ReadWalletSeed(ctx context.Context, slug string, svc catalog.Service) (string, error) {
	cli, err := dockerClient()
	if err != nil {
		return "", err
	}
	defer cli.Close()
	return readWalletSeed(ctx, cli, slug, svc)
}

func readWalletSeed(ctx context.Context, cli seedClient, slug string, svc catalog.Service) (string, error) {
	item, ok := svc.SeedVar()
	if !ok {
		return "", walletErr("wallet_none", "%s keeps no wallet seed.", svc.Name)
	}
	name := containerName(slug)
	got, err := cli.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return "", walletErr("wallet_missing", "%s is not deployed on this machine, so there is no wallet file to read.", svc.Name)
	}
	if err != nil {
		return "", fmt.Errorf("could not check %s: %w", name, err)
	}
	if got.Container.Config == nil || got.Container.Config.Labels[LabelService] != slug {
		return "", fmt.Errorf("%s belongs to something else, so it was not read", name)
	}
	raw, err := readContainerFile(ctx, cli, got.Container.ID, item.SeedFile.Path)
	if err != nil {
		return "", fmt.Errorf("could not read %s's wallet file: %w", svc.Name, err)
	}
	_, phrase, err := parseSeedFile(raw, item.SeedFile.Key)
	if err != nil {
		return "", err
	}
	if phrase == "" {
		return "", walletErr("wallet_missing", "%s has no wallet file yet. Redeploy it to move its wallet into one.", svc.Name)
	}
	return phrase, nil
}
