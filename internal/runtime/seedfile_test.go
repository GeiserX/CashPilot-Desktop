package runtime

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// The public BIP-39 test vectors, never a wallet anybody uses.
const (
	testPhraseA = "legal winner thank year wave sausage worth useful legal winner thank yellow"
	testPhraseB = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
)

func seedService() catalog.Service {
	return catalog.Service{
		Name: "Sia (hostd)", Slug: "sia-hostd",
		Payout: catalog.Payout{Chain: "sia"},
		Docker: catalog.DockerConfig{
			Image:   "ghcr.io/siafoundation/hostd@sha256:" + strings.Repeat("a", 64),
			Volumes: []string{"sia-hostd-data:/data", "${SIA_STORAGE_DIR}:/storage"},
			Env: []catalog.EnvVar{
				{Key: "HOSTD_WALLET_SEED", Required: true, Secret: true, SeedFile: &catalog.SeedFile{
					Path: "/data/hostd.yml", Key: "recoveryPhrase", Words: 12, Generate: []string{"seed"},
					PhrasePattern:  `(?m)^Recovery Phrase: ([a-z]+(?: [a-z]+){11})$`,
					AddressPattern: `(?m)^Address ([0-9a-f]{76})$`,
				}},
				{Key: "HOSTD_API_PASSWORD", Required: true, Secret: true},
				{Key: "SIA_STORAGE_DIR", Required: true},
			},
		},
	}
}

var seedMounts = []mount.Mount{{Type: mount.TypeVolume, Source: "sia-hostd-data", Target: "/data"}}

// fakeSeedDaemon is the part of Docker the wallet steps use. Files are kept per
// volume, as the archive API writes them through any container that mounts it.
type fakeSeedDaemon struct {
	containers map[string]client.ContainerCreateOptions // by name
	ids        map[string]string                        // id -> name
	files      map[string][]byte                        // "volume/rel" -> content
	modes      map[string]int64
	output     string
	exitCode   int64
	writes     int
	removed    []string
	calls      []string
	// fail makes one kind of call fail: "create <name>", "start <name>",
	// "inspect <name>", "wait", "logs", "copyto", or "copyfrom <n>" for the n-th
	// read (1-based). logsRaw replaces the log stream; rawTar replaces what a read
	// of a path returns; afterWrite runs after each write.
	fail       map[string]error
	logsRaw    []byte
	rawTar     map[string][]byte
	afterWrite func()
	reads      int
}

func (f *fakeSeedDaemon) failing(op string) error {
	if f.fail == nil {
		return nil
	}
	return f.fail[op]
}

func newFakeSeedDaemon() *fakeSeedDaemon {
	return &fakeSeedDaemon{containers: map[string]client.ContainerCreateOptions{}, ids: map[string]string{}, files: map[string][]byte{}, modes: map[string]int64{}}
}

func (f *fakeSeedDaemon) name(id string) string {
	if n, ok := f.ids[id]; ok {
		return n
	}
	return strings.TrimPrefix(id, "id-")
}

func (f *fakeSeedDaemon) ContainerCreate(_ context.Context, o client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	if _, taken := f.containers[o.Name]; taken {
		return client.ContainerCreateResult{}, errors.New("name in use")
	}
	if err := f.failing("create " + o.Name); err != nil {
		return client.ContainerCreateResult{}, err
	}
	f.calls = append(f.calls, "create "+o.Name)
	f.containers[o.Name] = o
	id := "id-" + o.Name
	f.ids[id] = o.Name
	return client.ContainerCreateResult{ID: id}, nil
}

func (f *fakeSeedDaemon) ContainerStart(_ context.Context, id string, _ client.ContainerStartOptions) (client.ContainerStartResult, error) {
	f.calls = append(f.calls, "start "+f.name(id))
	return client.ContainerStartResult{}, f.failing("start " + f.name(id))
}

func (f *fakeSeedDaemon) ContainerWait(context.Context, string, client.ContainerWaitOptions) client.ContainerWaitResult {
	if err := f.failing("wait"); err != nil {
		errs := make(chan error, 1)
		errs <- err
		return client.ContainerWaitResult{Result: make(chan container.WaitResponse), Error: errs}
	}
	res := make(chan container.WaitResponse, 1)
	res <- container.WaitResponse{StatusCode: f.exitCode}
	return client.ContainerWaitResult{Result: res, Error: make(chan error)}
}

func (f *fakeSeedDaemon) ContainerLogs(context.Context, string, client.ContainerLogsOptions) (client.ContainerLogsResult, error) {
	if err := f.failing("logs"); err != nil {
		return nil, err
	}
	if f.logsRaw != nil {
		return io.NopCloser(bytes.NewReader(f.logsRaw)), nil
	}
	payload := []byte(f.output)
	header := []byte{1, 0, 0, 0, byte(len(payload) >> 24), byte(len(payload) >> 16), byte(len(payload) >> 8), byte(len(payload))}
	return io.NopCloser(bytes.NewReader(append(header, payload...))), nil
}

func (f *fakeSeedDaemon) ContainerRemove(_ context.Context, id string, _ client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	name := f.name(id)
	f.removed = append(f.removed, name)
	delete(f.containers, name)
	return client.ContainerRemoveResult{}, nil
}

func (f *fakeSeedDaemon) ContainerInspect(_ context.Context, id string, _ client.ContainerInspectOptions) (client.ContainerInspectResult, error) {
	if err := f.failing("inspect " + f.name(id)); err != nil {
		return client.ContainerInspectResult{}, err
	}
	o, ok := f.containers[f.name(id)]
	if !ok {
		return client.ContainerInspectResult{}, cerrdefs.ErrNotFound
	}
	return client.ContainerInspectResult{Container: container.InspectResponse{ID: "id-" + o.Name, Config: o.Config}}, nil
}

// volumePath is the volume and relative path p falls in for the named container.
func (f *fakeSeedDaemon) volumePath(id, p string) (string, bool) {
	o := f.containers[f.name(id)]
	if o.HostConfig == nil {
		return "", false
	}
	for _, m := range o.HostConfig.Mounts {
		if p == m.Target || strings.HasPrefix(p, m.Target+"/") {
			return m.Source + "/" + strings.TrimPrefix(strings.TrimPrefix(p, m.Target), "/"), true
		}
	}
	return "", false
}

func (f *fakeSeedDaemon) CopyToContainer(_ context.Context, id string, o client.CopyToContainerOptions) (client.CopyToContainerResult, error) {
	f.writes++
	if err := f.failing("copyto"); err != nil {
		return client.CopyToContainerResult{}, err
	}
	tr := tar.NewReader(o.Content)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			if hook := f.afterWrite; hook != nil {
				f.afterWrite = nil // once: a restore after it writes as asked
				hook()
			}
			return client.CopyToContainerResult{}, nil
		}
		if err != nil {
			return client.CopyToContainerResult{}, err
		}
		key, ok := f.volumePath(id, path.Join(o.DestinationPath, hdr.Name))
		if !ok {
			return client.CopyToContainerResult{}, errors.New("not in a volume")
		}
		f.files[key], _ = io.ReadAll(tr)
		f.modes[key] = hdr.Mode
	}
}

func (f *fakeSeedDaemon) CopyFromContainer(_ context.Context, id string, o client.CopyFromContainerOptions) (client.CopyFromContainerResult, error) {
	f.reads++
	if err := f.failing(fmt.Sprintf("copyfrom %d", f.reads)); err != nil {
		return client.CopyFromContainerResult{}, err
	}
	if raw, ok := f.rawTar[o.SourcePath]; ok {
		return client.CopyFromContainerResult{Content: io.NopCloser(bytes.NewReader(raw))}, nil
	}
	key, ok := f.volumePath(id, o.SourcePath)
	data, found := f.files[key]
	if !ok || !found {
		return client.CopyFromContainerResult{}, cerrdefs.ErrNotFound
	}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	_ = tw.WriteHeader(&tar.Header{Name: path.Base(o.SourcePath), Mode: 0o600, Size: int64(len(data)), Typeflag: tar.TypeReg})
	_, _ = tw.Write(data)
	_ = tw.Close()
	return client.CopyFromContainerResult{Content: io.NopCloser(&buf)}, nil
}

func TestBuildEnvNeverPassesASeedFileVariable(t *testing.T) {
	env := buildEnv(seedService(), map[string]string{"HOSTD_WALLET_SEED": testPhraseA, "HOSTD_API_PASSWORD": "pw", "SIA_STORAGE_DIR": "/srv/sia"})
	if _, ok := env["HOSTD_WALLET_SEED"]; ok {
		t.Fatal("buildEnv passed the wallet seed into the container's environment")
	}
	if env["HOSTD_API_PASSWORD"] != "pw" || env["SIA_STORAGE_DIR"] != "/srv/sia" {
		t.Fatalf("buildEnv dropped an ordinary setting: %v", env)
	}
	native := buildNativeEnv(seedService(), map[string]string{"HOSTD_WALLET_SEED": testPhraseA})
	if _, ok := native["HOSTD_WALLET_SEED"]; ok {
		t.Fatal("buildNativeEnv passed the wallet seed into a process's environment")
	}
}

func TestBuildPortsBindsAnAddressedPortToThatAddress(t *testing.T) {
	ports, bindings, err := buildPorts([]string{"127.0.0.1:9980:9980", "9981:9981", "9984:9984/udp"})
	if err != nil {
		t.Fatalf("buildPorts: %v", err)
	}
	if len(ports) != 3 {
		t.Fatalf("ports = %v", ports)
	}
	for port, list := range bindings {
		want := ""
		if port.Num() == 9980 {
			want = "127.0.0.1"
		}
		got := ""
		if list[0].HostIP.IsValid() {
			got = list[0].HostIP.String()
		}
		if got != want {
			t.Errorf("%s binds to %q, want %q", port, got, want)
		}
	}
	for _, bad := range []string{"localhost:9980:9980", "1:2:3:4", ":9980:9980", "9980"} {
		if _, _, err := buildPorts([]string{bad}); err == nil {
			t.Errorf("buildPorts accepted %q", bad)
		}
	}
	if keys := catalogPortKeys([]string{"127.0.0.1:9980:9980"}); !keys["9980/tcp"] {
		t.Errorf("drift does not read an addressed port: %v", keys)
	}
}

// The phrase the node ran with is the wallet holding its funds, so a deploy whose
// file has none writes it in, preferring what the running container carries to
// what Desktop stored, and never makes a new one over it.
func TestTheRunningWalletWinsOverAStoredOneAndANewOne(t *testing.T) {
	f := newFakeSeedDaemon()
	req := &WalletRequest{Mode: WalletGenerate, Stored: testPhraseB}
	result, err := prepareWallet(context.Background(), f, seedService(), "img", "cashpilot-sia-hostd", seedMounts, req, testPhraseA, nil)
	if err != nil {
		t.Fatalf("prepareWallet: %v", err)
	}
	if !result.Created || !result.Migrated || result.Phrase != "" {
		t.Fatalf("result = %+v, want a migrated wallet with no phrase shown", result)
	}
	if got := string(f.files["sia-hostd-data/hostd.yml"]); !strings.Contains(got, testPhraseA) {
		t.Fatalf("the file holds %q, want the running wallet", got)
	}
	if f.modes["sia-hostd-data/hostd.yml"] != 0o600 {
		t.Errorf("the file is mode %o, want 0600", f.modes["sia-hostd-data/hostd.yml"])
	}
	for _, call := range f.calls {
		if strings.Contains(call, "wallet-new") {
			t.Fatalf("a generator ran although the node had a wallet: %v", f.calls)
		}
	}
	helper := f.removed
	if len(helper) != 1 || helper[0] != "cashpilot-sia-hostd-wallet" {
		t.Errorf("removed %v, want the file helper only", helper)
	}
}

// A file the node already has is kept whole: its other settings stay, and an
// invalid one is refused rather than overwritten.
func TestTheNodesConfigFileIsNeverClobbered(t *testing.T) {
	f := newFakeSeedDaemon()
	f.files["sia-hostd-data/hostd.yml"] = []byte("http:\n  address: :9980\n")
	result, err := prepareWallet(context.Background(), f, seedService(), "img", "n", seedMounts, &WalletRequest{Mode: WalletImport, Phrase: testPhraseA}, "", nil)
	if err != nil || !result.Created {
		t.Fatalf("prepareWallet: %+v, %v", result, err)
	}
	got := string(f.files["sia-hostd-data/hostd.yml"])
	if !strings.Contains(got, "address: :9980") || !strings.Contains(got, "recoveryPhrase: "+testPhraseA) {
		t.Fatalf("the file lost its settings or did not gain the phrase:\n%s", got)
	}

	f = newFakeSeedDaemon()
	f.files["sia-hostd-data/hostd.yml"] = []byte("- just\n- a list\n")
	if _, err := prepareWallet(context.Background(), f, seedService(), "img", "n", seedMounts, &WalletRequest{Mode: WalletGenerate}, "", nil); err == nil || !strings.Contains(err.Error(), "not a YAML mapping") {
		t.Fatalf("an invalid file was not refused: %v", err)
	}
	if f.writes != 0 {
		t.Fatal("an invalid file was overwritten")
	}
}

// A generator that fails writes nothing and leaves nothing behind, and the error
// never repeats what it printed.
func TestAFailedGeneratorWritesNothing(t *testing.T) {
	f := newFakeSeedDaemon()
	f.exitCode = 2
	f.output = "Recovery Phrase: " + testPhraseB + "\n"
	_, err := prepareWallet(context.Background(), f, seedService(), "img", "n", seedMounts, &WalletRequest{Mode: WalletGenerate}, "", nil)
	if err == nil || strings.Contains(err.Error(), "abandon") {
		t.Fatalf("a failed generator was not refused cleanly: %v", err)
	}
	if f.writes != 0 || len(f.containers) != 0 {
		t.Fatalf("a failed generator wrote %d files or left %v", f.writes, f.containers)
	}

	f = newFakeSeedDaemon()
	f.output = "something else entirely\n"
	if _, err := prepareWallet(context.Background(), f, seedService(), "img", "n", seedMounts, &WalletRequest{Mode: WalletGenerate}, "", nil); err == nil {
		t.Fatal("output with no phrase was taken as a wallet")
	}
	if f.writes != 0 {
		t.Fatal("output with no phrase was written")
	}
}

// A container under a helper's name that is not a wallet helper belongs to
// someone else: it is neither used nor removed.
func TestAForeignContainerUnderTheHelpersNameIsLeftAlone(t *testing.T) {
	f := newFakeSeedDaemon()
	f.containers["n-wallet"] = client.ContainerCreateOptions{Name: "n-wallet", Config: &container.Config{Labels: map[string]string{"owner": "someone"}}}
	if _, err := prepareWallet(context.Background(), f, seedService(), "img", "n", seedMounts, nil, "", nil); err == nil || !strings.Contains(err.Error(), "belongs to something else") {
		t.Fatalf("a foreign container was not refused: %v", err)
	}
	if len(f.removed) != 0 {
		t.Fatalf("a foreign container was removed: %v", f.removed)
	}
}

func TestTheRevealReadsOnlyItsOwnContainer(t *testing.T) {
	f := newFakeSeedDaemon()
	f.files["sia-hostd-data/hostd.yml"] = []byte("recoveryPhrase: " + testPhraseA + "\n")
	f.containers["cashpilot-sia-hostd"] = client.ContainerCreateOptions{
		Name:       "cashpilot-sia-hostd",
		Config:     &container.Config{Labels: map[string]string{LabelManaged: "true", LabelRuntime: desktopRuntime, LabelService: "sia-hostd"}},
		HostConfig: &container.HostConfig{Mounts: seedMounts},
	}
	got, err := readWalletSeed(context.Background(), f, "sia-hostd", seedService())
	if err != nil || got != testPhraseA {
		t.Fatalf("readWalletSeed = %q, %v", got, err)
	}
	f.containers["cashpilot-sia-hostd"] = client.ContainerCreateOptions{Name: "cashpilot-sia-hostd", Config: &container.Config{}, HostConfig: &container.HostConfig{Mounts: seedMounts}}
	if _, err := readWalletSeed(context.Background(), f, "sia-hostd", seedService()); err == nil {
		t.Fatal("the reveal read a container that is not the service's")
	}
}

// A pasted phrase never replaces the wallet a node from before the seed file ran
// with (in its container's env or the app's store): that wallet holds the funds.
// The same wallet pasted again is the move.
func TestAPastedWalletNeverReplacesTheOneTheNodeRan(t *testing.T) {
	for _, tc := range []struct{ name, live, stored string }{
		{"env", testPhraseA, ""},
		{"store", "", testPhraseA},
	} {
		f := newFakeSeedDaemon()
		req := &WalletRequest{Mode: WalletImport, Phrase: testPhraseB, Stored: tc.stored}
		_, err := prepareWallet(context.Background(), f, seedService(), "img", "cashpilot-sia-hostd", seedMounts, req, tc.live, nil)
		var werr *WalletError
		if !errors.As(err, &werr) || werr.Code != "wallet_exists" {
			t.Fatalf("%s: a pasted wallet replaced the one the node ran: %v", tc.name, err)
		}
		if strings.Contains(err.Error(), "legal") || strings.Contains(err.Error(), "abandon") {
			t.Fatalf("%s: the refusal holds a phrase", tc.name)
		}
		if _, ok := f.files["sia-hostd-data/hostd.yml"]; ok {
			t.Fatalf("%s: the refused import wrote the file", tc.name)
		}

		f = newFakeSeedDaemon()
		req = &WalletRequest{Mode: WalletImport, Phrase: testPhraseA, Stored: tc.stored}
		result, err := prepareWallet(context.Background(), f, seedService(), "img", "cashpilot-sia-hostd", seedMounts, req, tc.live, nil)
		if err != nil || !result.Migrated {
			t.Fatalf("%s: pasting the node's own wallet was not the move: %+v, %v", tc.name, result, err)
		}
	}
}

// A new or pasted wallet comes back out of the file when the deploy that wrote it
// fails; a moved one stays, since the node ran it already.
func TestAFailedDeployTakesItsNewWalletBackOut(t *testing.T) {
	f := newFakeSeedDaemon()
	f.files["sia-hostd-data/hostd.yml"] = []byte("http:\n  address: :9980\n")
	result, err := prepareWallet(context.Background(), f, seedService(), "img", "n", seedMounts, &WalletRequest{Mode: WalletImport, Phrase: testPhraseA}, "", nil)
	if err != nil {
		t.Fatalf("prepareWallet: %v", err)
	}
	cause := errors.New("port is taken")
	if got := result.Undo(cause); got != cause {
		t.Fatalf("Undo = %v, want the deploy's own error", got)
	}
	if got := string(f.files["sia-hostd-data/hostd.yml"]); got != "http:\n  address: :9980\n" {
		t.Fatalf("the file was not put back as it was:\n%s", got)
	}

	f = newFakeSeedDaemon()
	result, err = prepareWallet(context.Background(), f, seedService(), "img", "n", seedMounts, &WalletRequest{Mode: WalletImport, Phrase: testPhraseA}, "", nil)
	if err != nil {
		t.Fatalf("prepareWallet: %v", err)
	}
	_ = result.Undo(cause)
	if got := string(f.files["sia-hostd-data/hostd.yml"]); strings.Contains(got, testPhraseA) {
		t.Fatal("a file that did not exist still holds the new wallet after the undo")
	}

	f = newFakeSeedDaemon()
	result, err = prepareWallet(context.Background(), f, seedService(), "img", "n", seedMounts, &WalletRequest{Mode: WalletKeep, Stored: testPhraseA}, "", nil)
	if err != nil || !result.Migrated {
		t.Fatalf("prepareWallet (move): %+v, %v", result, err)
	}
	_ = result.Undo(cause)
	if got := string(f.files["sia-hostd-data/hostd.yml"]); !strings.Contains(got, testPhraseA) {
		t.Fatal("the undo took out a wallet the node already ran")
	}
	if (*WalletResult)(nil).Undo(cause) != cause {
		t.Fatal("Undo on nil changed the error")
	}
}
