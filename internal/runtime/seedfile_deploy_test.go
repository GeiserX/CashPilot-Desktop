package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// The wallet step inside DockerProvider.Deploy, against the fake daemon: the
// replaced container's seed moves into the file, a deploy that fails takes its new
// wallet back out, a restore's create is refused, and the reveal reads the file of
// the deployed container.

func siaSpec(wallet *WalletRequest) DeploySpec {
	return DeploySpec{
		Slug:    "sia-hostd",
		Service: seedService(),
		Env:     map[string]string{"HOSTD_API_PASSWORD": "pw", "SIA_STORAGE_DIR": "/srv/sia"},
		Wallet:  wallet,
	}
}

// A node from before the seed file runs with the phrase in its env. Its
// replacement gets the phrase in its file, and no seed variable.
func TestADeployMovesTheReplacedContainersSeedIntoItsFile(t *testing.T) {
	docker, p, _ := rollbackDaemon(t)
	cli, err := client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		t.Fatalf("client: %v", err)
	}
	defer cli.Close()
	if _, err := cli.ContainerCreate(context.Background(), client.ContainerCreateOptions{
		Name: "cashpilot-sia-hostd",
		Config: &container.Config{
			Image:  seedService().Docker.Image,
			Env:    []string{"HOSTD_WALLET_SEED=" + testPhraseA, "HOSTD_API_PASSWORD=pw"},
			Labels: map[string]string{LabelManaged: "true", LabelRuntime: desktopRuntime, LabelService: "sia-hostd"},
		},
	}); err != nil {
		t.Fatalf("create the old node: %v", err)
	}

	info, err := p.Deploy(context.Background(), siaSpec(&WalletRequest{Mode: WalletKeep}), nil)
	if err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	if info.Wallet == nil || !info.Wallet.Migrated || info.Wallet.Phrase != "" {
		t.Fatalf("Wallet = %+v, want the running wallet moved, no phrase shown", info.Wallet)
	}
	f, ok := docker.VolumeFile("sia-hostd-data", "hostd.yml")
	if !ok || !strings.Contains(string(f.Data), "recoveryPhrase: "+testPhraseA) || f.Mode&0o077 != 0 {
		t.Fatalf("the file does not hold the running wallet, root-only: %q mode %o", f.Data, f.Mode)
	}
	node, _ := docker.Container("cashpilot-sia-hostd")
	for _, kv := range node.Env {
		if strings.HasPrefix(kv, "HOSTD_WALLET_SEED=") {
			t.Fatal("the replacement still carries the seed variable")
		}
	}

	// The reveal reads the same file back through the deployed container.
	got, err := p.ReadWalletSeed(context.Background(), "sia-hostd", seedService())
	if err != nil || got != testPhraseA {
		t.Fatalf("ReadWalletSeed = %q, %v", got, err)
	}
}

// A wallet step that refuses stops the deploy before the running node is
// touched, and before anything is created on a fresh one.
func TestARefusedWalletStepTouchesNoContainer(t *testing.T) {
	docker, p, _ := rollbackDaemon(t)
	if _, err := p.Deploy(context.Background(), siaSpec(&WalletRequest{Mode: WalletKeep}), nil); walletCode(err) != "wallet_missing" {
		t.Fatalf("a fresh deploy with no wallet: %v", err)
	}
	if names := managedNames(docker, "sia-hostd"); len(names) != 0 {
		t.Fatalf("the refused fresh deploy left %v", names)
	}

	docker.SetGeneratorOutput(generatorOutput)
	if _, err := p.Deploy(context.Background(), siaSpec(&WalletRequest{Mode: WalletGenerate}), nil); err != nil {
		t.Fatalf("Deploy: %v", err)
	}
	before, _ := docker.Container("cashpilot-sia-hostd")
	_, err := p.Deploy(context.Background(), siaSpec(&WalletRequest{Mode: WalletImport, Phrase: testPhraseA}), nil)
	if walletCode(err) != "wallet_exists" {
		t.Fatalf("pasting another wallet over the node's: %v", err)
	}
	after, _ := docker.Container("cashpilot-sia-hostd")
	if after.ID != before.ID || after.State != "running" {
		t.Fatalf("the refused import touched the running node: %+v", after)
	}
}

// A new wallet whose container Docker then refuses is taken back out of the file,
// on a fresh deploy and on a replacement, so the retry makes and shows a new one.
func TestARefusedCreateOrStartTakesTheNewWalletBackOut(t *testing.T) {
	for _, step := range []string{"create", "start"} {
		t.Run(step, func(t *testing.T) {
			docker, p, _ := rollbackDaemon(t)
			docker.SetGeneratorOutput(generatorOutput)
			if step == "create" {
				docker.FailCreate("cashpilot-sia-hostd", portRefusal)
			} else {
				docker.FailStartOfNext("cashpilot-sia-hostd", portRefusal)
			}
			_, err := p.Deploy(context.Background(), siaSpec(&WalletRequest{Mode: WalletGenerate}), nil)
			if err == nil || !strings.Contains(err.Error(), "port is already allocated") {
				t.Fatalf("Deploy = %v, want Docker's refusal", err)
			}
			if strings.Contains(err.Error(), "stays in") {
				t.Fatalf("the undo failed: %v", err)
			}
			if f, ok := docker.VolumeFile("sia-hostd-data", "hostd.yml"); ok && strings.Contains(string(f.Data), "recoveryPhrase") {
				t.Fatalf("the refused deploy left its wallet in the file: %q", f.Data)
			}
			info, err := p.Deploy(context.Background(), siaSpec(&WalletRequest{Mode: WalletGenerate}), nil)
			if err != nil || info.Wallet == nil || info.Wallet.Phrase != testPhraseB {
				t.Fatalf("the retry did not make and show a new wallet: %+v, %v", info.Wallet, err)
			}
		})
	}
}

// A restore's create of a service with a seed file is refused before anything is
// made: the restore could not check the file, and the node would run no wallet.
func TestARestoreCreateOfASeedFileServiceIsRefused(t *testing.T) {
	docker, p, _ := rollbackDaemon(t)
	spec := siaSpec(nil)
	spec.CreateOnly = true
	if _, err := p.Deploy(context.Background(), spec, nil); err == nil || !strings.Contains(err.Error(), "nothing was restored") {
		t.Fatalf("a restore create of Sia was not refused: %v", err)
	}
	if names := managedNames(docker, "sia-hostd"); len(names) != 0 {
		t.Fatalf("the refused restore created %v", names)
	}
}
