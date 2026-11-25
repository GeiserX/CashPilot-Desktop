package services

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
)

// The public BIP-39 test vector, never a wallet anybody uses.
const walletTestPhrase = "legal winner thank year wave sausage worth useful legal winner thank yellow"

// walletTestAddress is the address go.sia.tech/coreutils derives for walletTestPhrase.
const walletTestAddress = "5a7da9320b1d8cb37801eafa7065175df235be95096e401a187b5a98fda50428ef9e7566c29f"

func realCatalogForWallet(t *testing.T) *catalog.Catalog {
	t.Helper()
	cat, err := catalog.LoadEmbedded(os.DirFS(filepath.Join("..", "..")))
	if err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	return cat
}

func siaValues() map[string]string {
	return map[string]string{"HOSTD_API_PASSWORD": "a long passphrase", "SIA_STORAGE_DIR": "/srv/sia"}
}

// The seed is no longer a field the owner fills in, so a deploy without it is valid
// and the settings show as configured: the wallet choice makes, takes or keeps it.
func TestASeedKeptInAFileIsNotARequiredField(t *testing.T) {
	m := NewManager(&fakeProvider{}, realCatalogForWallet(t), newTestStore(t))
	if err := m.ValidateCredentials("sia-hostd", siaValues()); err != nil {
		t.Fatalf("ValidateCredentials asked for the seed: %v", err)
	}
	if !m.RequiredCredentialsMet("sia-hostd", siaValues()) {
		t.Fatal("Sia shows as not configured without a stored seed")
	}
	if err := m.ValidateCredentials("sia-hostd", map[string]string{"SIA_STORAGE_DIR": "/srv/sia"}); err == nil {
		t.Fatal("the dashboard password stopped being required")
	}
}

// The runtime gets the seed only as the wallet request, never in the env, and the
// stored copy is deleted only once a deploy has put it in the file: a deploy that
// fails keeps it, so the next one can still move it.
func TestTheStoredSeedGoesOnlyAfterTheFileHoldsIt(t *testing.T) {
	st := newTestStore(t)
	legacy := siaValues()
	legacy["HOSTD_WALLET_SEED"] = walletTestPhrase
	if err := st.SaveCredentials("sia-hostd", legacy); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}
	provider := &fakeProvider{deployErr: errors.New("pull failed")}
	m := NewManager(provider, realCatalogForWallet(t), st)

	if _, err := m.DeployWith(context.Background(), "sia-hostd", siaValues(), DeployOptions{}); err == nil {
		t.Fatal("the failing deploy succeeded")
	}
	spec := provider.deploySpecs[0]
	if _, ok := spec.Env["HOSTD_WALLET_SEED"]; ok {
		t.Fatal("the seed reached the runtime as a variable")
	}
	if spec.Wallet == nil || spec.Wallet.Mode != runtime.WalletKeep || spec.Wallet.Stored != walletTestPhrase {
		t.Fatalf("the runtime was not asked to keep the wallet with the stored seed: %+v", spec.Wallet)
	}
	if stored, _ := st.GetCredentials("sia-hostd"); stored["HOSTD_WALLET_SEED"] != walletTestPhrase {
		t.Fatal("a failed deploy deleted the only stored copy of the seed")
	}

	provider.deployErr = nil
	provider.deployResult = runtime.ContainerInfo{Slug: "sia-hostd", Status: "running", Wallet: &runtime.WalletResult{Created: true, Migrated: true, Address: walletTestAddress}}
	dep, err := m.DeployWith(context.Background(), "sia-hostd", legacy, DeployOptions{})
	if err != nil {
		t.Fatalf("DeployWith: %v", err)
	}
	if dep.WalletAddress != walletTestAddress {
		t.Errorf("WalletAddress = %q", dep.WalletAddress)
	}
	stored, _ := st.GetCredentials("sia-hostd")
	if _, ok := stored["HOSTD_WALLET_SEED"]; ok {
		t.Fatal("the stored seed survived a deploy that put it in the file")
	}
	if stored["HOSTD_API_PASSWORD"] == "" {
		t.Fatal("deleting the seed deleted the other settings")
	}
	if last := provider.deploySpecs[len(provider.deploySpecs)-1]; strings.Contains(strings.Join(mapValues(last.Env), " "), walletTestPhrase) {
		t.Fatal("a caller that passed the stored blob got the seed into the env")
	}
}

// A stored phrase that is a different wallet from the one the file holds is never
// deleted: it may be the only copy of a funded wallet.
func TestAStoredSeedOfAnotherWalletIsKept(t *testing.T) {
	st := newTestStore(t)
	legacy := siaValues()
	legacy["HOSTD_WALLET_SEED"] = walletTestPhrase
	if err := st.SaveCredentials("sia-hostd", legacy); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}
	other := "a2a3773f76136bdb05a0ff79a0f4fcc2826436794f8db36db6408355c5ca32345002db43c2eb"
	provider := &fakeProvider{deployResult: runtime.ContainerInfo{Slug: "sia-hostd", Status: "running", Wallet: &runtime.WalletResult{Address: other}}}
	m := NewManager(provider, realCatalogForWallet(t), st)
	if _, err := m.DeployWith(context.Background(), "sia-hostd", siaValues(), DeployOptions{}); err != nil {
		t.Fatalf("DeployWith: %v", err)
	}
	if stored, _ := st.GetCredentials("sia-hostd"); stored["HOSTD_WALLET_SEED"] != walletTestPhrase {
		t.Fatal("a deploy deleted a stored seed of another wallet than the file's")
	}
}

// A pasted phrase that is not one is refused before the runtime is asked anything.
func TestABadPastedPhraseIsRefusedBeforeTheRuntime(t *testing.T) {
	provider := &fakeProvider{}
	m := NewManager(provider, realCatalogForWallet(t), newTestStore(t))
	_, err := m.DeployWith(context.Background(), "sia-hostd", siaValues(), DeployOptions{Wallet: &runtime.WalletRequest{Mode: runtime.WalletImport, Phrase: "legal winner thank"}})
	if err == nil || provider.deployCalls != 0 {
		t.Fatalf("a 3-word phrase reached the runtime (err %v, calls %d)", err, provider.deployCalls)
	}
}

func mapValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}
