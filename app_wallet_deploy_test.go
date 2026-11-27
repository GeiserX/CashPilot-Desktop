package main

import (
	"strings"
	"testing"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
)

// The wallet choices of the deploy form (DeployServiceWallet) and the reveal
// (RevealWalletSeed), driven against the fake daemon. app_wallet_test.go holds the
// rule and its helpers.

// A new wallet comes from the image's own generator, run offline with a local log
// and removed at once. The phrase reaches the window once, in the deploy's answer,
// and is in no container's settings, the store or the history.
func TestANewSiaWalletIsMadeOfflineAndShownOnce(t *testing.T) {
	app := newAppUnderTest(t)
	app.Docker.SetGeneratorOutput("Recovery Phrase: " + seedPhraseB + "\nAddress " + seedAddressB + "\n")

	result, err := app.DeployServiceWallet(siaSlug, siaSettings(), nil, nil, WalletChoice{Mode: runtime.WalletGenerate})
	if err != nil {
		t.Fatalf("DeployServiceWallet: %v", err)
	}
	if result.Wallet == nil || !result.Wallet.Created || result.Wallet.Phrase != seedPhraseB || result.Wallet.Address != seedAddressB {
		t.Fatalf("the answer does not carry the new wallet once: %+v", result.Wallet)
	}
	if result.Deployment.WalletAddress != seedAddressB {
		t.Errorf("the deployment does not carry the wallet's address: %q", result.Deployment.WalletAddress)
	}
	if dep, ok, _ := app.store.GetDeployment(siaSlug); !ok || dep.WalletAddress != seedAddressB {
		t.Errorf("the address was not recorded with the deployment: %+v", dep)
	}

	var generators int
	for _, c := range app.Docker.Created() {
		if c.Labels[runtime.LabelWalletHelper] != "generate" {
			continue
		}
		generators++
		// Its only variable is the GPU void stamp: never a credential.
		if c.NetworkMode != "none" || c.LogDriver != "json-file" || strings.Join(c.Env, ",") != "NVIDIA_VISIBLE_DEVICES=void" ||
			strings.Join(c.CapDrop, ",") != "ALL" || c.Tmpfs["/data"] == "" || strings.Join(c.Cmd, " ") != "seed" {
			t.Errorf("the generator is not offline, local-logged, unprivileged and diskless: %+v", c)
		}
	}
	if generators != 1 {
		t.Fatalf("%d generators ran, want 1", generators)
	}
	if names := app.Docker.Names(); len(names) != 1 || names[0] != siaContainer {
		t.Errorf("a wallet helper was left behind: %v", names)
	}
	assertSeedFile(t, app, seedPhraseB)
	assertSeedNowhere(t, app, seedPhraseB)

	// A redeploy that asks for a new wallet again keeps the one the node has: no
	// generator, no phrase in the answer, the file unchanged.
	again, err := app.DeployServiceWallet(siaSlug, siaSettings(), nil, nil, WalletChoice{Mode: runtime.WalletGenerate})
	if err != nil {
		t.Fatalf("second DeployServiceWallet: %v", err)
	}
	if again.Wallet == nil || again.Wallet.Created || again.Wallet.Phrase != "" || again.Wallet.Address != seedAddressB {
		t.Fatalf("a redeploy made or showed a wallet: %+v", again.Wallet)
	}
	generators = 0
	for _, c := range app.Docker.Created() {
		if c.Labels[runtime.LabelWalletHelper] == "generate" {
			generators++
		}
	}
	if generators != 1 {
		t.Errorf("the redeploy ran the generator again")
	}
	assertSeedFile(t, app, seedPhraseB)
}

// A pasted phrase is checked before anything runs, written into the file, and
// never stored. One that would replace the node's wallet is refused.
func TestAPastedWalletIsCheckedAndNeverReplacesOne(t *testing.T) {
	app := newAppUnderTest(t)

	broken := strings.Replace(seedPhraseA, "yellow", "year", 1) // valid words, wrong checksum
	if _, err := app.DeployServiceWallet(siaSlug, siaSettings(), nil, nil, WalletChoice{Mode: runtime.WalletImport, Phrase: broken}); err == nil ||
		!strings.Contains(err.Error(), "does not check out") || strings.Contains(err.Error(), "legal winner") {
		t.Fatalf("a phrase with a wrong checksum was not refused plainly: %v", err)
	}
	if len(app.Docker.Created()) != 0 {
		t.Fatalf("a refused phrase still created containers: %v", app.Docker.Names())
	}

	result, err := app.DeployServiceWallet(siaSlug, siaSettings(), nil, nil, WalletChoice{Mode: runtime.WalletImport, Phrase: "  " + strings.ToUpper(seedPhraseA) + " "})
	if err != nil {
		t.Fatalf("DeployServiceWallet (import): %v", err)
	}
	if result.Wallet == nil || !result.Wallet.Created || result.Wallet.Phrase != "" || result.Wallet.Address == "" {
		t.Fatalf("an import must write the file, show no phrase back and know the address: %+v", result.Wallet)
	}
	assertSeedFile(t, app, seedPhraseA)
	assertSeedNowhere(t, app, seedPhraseA)

	before, _ := app.Docker.Container(siaContainer)
	_, err = app.DeployServiceWallet(siaSlug, siaSettings(), nil, nil, WalletChoice{Mode: runtime.WalletImport, Phrase: seedPhraseB})
	if err == nil || !strings.Contains(err.Error(), "already has another wallet") {
		t.Fatalf("a second wallet replaced the node's: %v", err)
	}
	if after, _ := app.Docker.Container(siaContainer); after.ID != before.ID || after.State != "running" {
		t.Errorf("the refused import touched the running node: %+v", after)
	}
	assertSeedFile(t, app, seedPhraseA)
	assertSeedNowhere(t, app, seedPhraseB)
}

// A deploy that keeps the wallet of a node that has none is refused before any
// container is made, instead of starting a node that cannot run.
func TestKeepingAWalletThatDoesNotExistIsRefused(t *testing.T) {
	app := newAppUnderTest(t)
	_, err := app.DeployService(siaSlug, siaSettings(), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "has no wallet yet") {
		t.Fatalf("a deploy with no wallet anywhere was not refused: %v", err)
	}
	for _, c := range app.Docker.Created() {
		// The LAN fence's helper runs on every deploy, before the wallet step.
		if c.Labels[runtime.LabelWalletHelper] == "" && c.Labels["cashpilot.probe"] != "fence" {
			t.Errorf("the refused deploy created %s", c.Name)
		}
	}
	if names := app.Docker.Names(); len(names) != 0 {
		t.Errorf("the refused deploy left %v", names)
	}
}

// The reveal reads the file back, only after the service's name is typed, at most
// three tries in ten minutes, and each try goes into the history without the phrase.
func TestTheRevealIsGatedLimitedAndRecorded(t *testing.T) {
	app := newAppUnderTest(t)
	if _, err := app.DeployServiceWallet(siaSlug, siaSettings(), nil, nil, WalletChoice{Mode: runtime.WalletImport, Phrase: seedPhraseA}); err != nil {
		t.Fatalf("DeployServiceWallet: %v", err)
	}
	if _, err := app.RevealWalletSeed(siaSlug, "honeygain"); err == nil {
		t.Fatal("the reveal answered to the wrong name")
	}
	got, err := app.RevealWalletSeed(siaSlug, "  sia (HOSTD) ")
	if err != nil || got != seedPhraseA {
		t.Fatalf("RevealWalletSeed = %q, %v", got, err)
	}
	if _, err := app.RevealWalletSeed(siaSlug, "Sia (hostd)"); err != nil {
		t.Fatalf("the third try was refused: %v", err)
	}
	if _, err := app.RevealWalletSeed(siaSlug, "Sia (hostd)"); err == nil || !strings.Contains(err.Error(), "Wait") {
		t.Fatalf("a fourth try within ten minutes was not refused: %v", err)
	}
	if !app.walletRevealAllowed(siaSlug, time.Now().Add(walletRevealWindow+time.Minute)) {
		t.Fatal("the limit did not lift after the window")
	}
	if _, err := app.RevealWalletSeed("honeygain", "Honeygain"); err == nil {
		t.Fatal("a service with no seed file answered a reveal")
	}
	assertSeedNowhere(t, app, seedPhraseA)
}

// A node from before the seed file runs the wallet Desktop stored. Pasting another
// one is refused before anything is written, and the stored copy, possibly the
// only one, stays until a deploy moves it.
func TestAPastedWalletNeverReplacesAStoredOne(t *testing.T) {
	app := newAppUnderTest(t)
	legacy := siaSettings()
	legacy[siaSeedKey] = seedPhraseA
	if err := app.store.SaveCredentials(siaSlug, legacy); err != nil {
		t.Fatalf("store the old settings: %v", err)
	}
	_, err := app.DeployServiceWallet(siaSlug, siaSettings(), nil, nil, WalletChoice{Mode: runtime.WalletImport, Phrase: seedPhraseB})
	if err == nil || !strings.Contains(err.Error(), "already runs another wallet") {
		t.Fatalf("a pasted wallet replaced the stored one: %v", err)
	}
	if stored, _ := app.store.GetCredentials(siaSlug); stored[siaSeedKey] != seedPhraseA {
		t.Fatal("the refused import deleted the stored seed")
	}
	if _, ok := app.Docker.VolumeFile(siaVolume, siaSeedFile); ok {
		t.Fatal("the refused import wrote the wallet file")
	}
	if names := app.Docker.Names(); len(names) != 0 {
		t.Errorf("the refused import left %v", names)
	}
}

// A new wallet whose deploy then fails is taken back out of the file, so the retry
// makes and shows a new one instead of keeping one the owner never saw.
func TestANewWalletIsTakenBackWhenTheDeployFails(t *testing.T) {
	app := newAppUnderTest(t)
	app.Docker.SetGeneratorOutput("Recovery Phrase: " + seedPhraseB + "\nAddress " + seedAddressB + "\n")
	app.Docker.FailCreate(siaContainer, "port is already allocated")
	if _, err := app.DeployServiceWallet(siaSlug, siaSettings(), nil, nil, WalletChoice{Mode: runtime.WalletGenerate}); err == nil {
		t.Fatal("the deploy succeeded although its create failed")
	}
	if f, ok := app.Docker.VolumeFile(siaVolume, siaSeedFile); ok && strings.Contains(string(f.Data), seedPhraseB) {
		t.Fatal("the failed deploy left a wallet in the file that nobody was shown")
	}
	app.Docker.ClearFailures()
	result, err := app.DeployServiceWallet(siaSlug, siaSettings(), nil, nil, WalletChoice{Mode: runtime.WalletGenerate})
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if result.Wallet == nil || !result.Wallet.Created || result.Wallet.Phrase != seedPhraseB {
		t.Fatalf("the retry did not make and show a new wallet: %+v", result.Wallet)
	}
	assertSeedFile(t, app, seedPhraseB)
	assertSeedNowhere(t, app, seedPhraseB)
}

// A reveal for a service the app does not know, or a node that is not deployed,
// is refused plainly, and the failed read goes into the history without a phrase.
func TestARevealOfANodeThatIsNotThereSaysSo(t *testing.T) {
	app := newAppUnderTest(t)
	if _, err := app.RevealWalletSeed("no-such-service", "anything"); err == nil || !strings.Contains(err.Error(), "unknown service") {
		t.Fatalf("an unknown service: %v", err)
	}
	_, err := app.RevealWalletSeed(siaSlug, "Sia (hostd)")
	if err == nil || !strings.Contains(err.Error(), "is not deployed") {
		t.Fatalf("a reveal of a node that is not deployed: %v", err)
	}
	if !historyHas(t, app, "wallet_reveal_error") {
		t.Fatal("the failed reveal is not in the history")
	}
	assertSeedNowhere(t, app, seedPhraseA)
}
