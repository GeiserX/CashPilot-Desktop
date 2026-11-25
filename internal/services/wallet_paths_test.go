package services

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
)

// The services side of the wallet seed file: what the form saves and shows, what
// a deploy records, when the old stored copy goes, and the reveal's refusals.

// The other public BIP-39 vector, never a wallet anybody uses.
const otherWalletPhrase = "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"

// historyOf is every runtime event of slug, as "event: detail".
func historyOf(t *testing.T, dir, slug string) []string {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, "cashpilot-desktop.db"))
	if err != nil {
		t.Fatalf("open the database: %v", err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT event, detail FROM runtime_events WHERE slug = ? ORDER BY id`, slug)
	if err != nil {
		t.Fatalf("read the history: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var event, detail string
		if err := rows.Scan(&event, &detail); err != nil {
			t.Fatalf("scan the history: %v", err)
		}
		out = append(out, event+": "+detail)
	}
	return out
}

func assertHistoryHoldsNoPhrase(t *testing.T, history []string) {
	t.Helper()
	for _, line := range history {
		for _, word := range []string{"legal winner", "abandon abandon"} {
			if strings.Contains(line, word) {
				t.Fatalf("the history holds a phrase: %q", line)
			}
		}
	}
}

// The form keeps a legacy seed until a deploy moves it to the file.
func TestSavingTheFormKeepsOnlyALegacySeed(t *testing.T) {
	st := newTestStore(t)
	m := NewManager(&fakeProvider{}, realCatalogForWallet(t), st)
	if !m.HasSeedFile("sia-hostd") || m.HasSeedFile("honeygain") || m.HasSeedFile("no-such-service") {
		t.Fatal("HasSeedFile does not follow the catalog")
	}

	typed := siaValues()
	typed["HOSTD_WALLET_SEED"] = walletTestPhrase
	toSave, err := m.CredentialsToSave("sia-hostd", typed)
	if err != nil {
		t.Fatalf("CredentialsToSave: %v", err)
	}
	if _, ok := toSave["HOSTD_WALLET_SEED"]; ok || toSave["HOSTD_API_PASSWORD"] == "" {
		t.Fatalf("a typed seed would be stored, or the settings were lost: %v", toSave)
	}
	if typed["HOSTD_WALLET_SEED"] == "" {
		t.Fatal("CredentialsToSave changed the caller's map")
	}

	legacy := siaValues()
	legacy["HOSTD_WALLET_SEED"] = otherWalletPhrase
	if err := st.SaveCredentials("sia-hostd", legacy); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}
	toSave, err = m.CredentialsToSave("sia-hostd", siaValues())
	if err != nil || toSave["HOSTD_WALLET_SEED"] != otherWalletPhrase {
		t.Fatalf("saving the form would drop the only stored copy before a deploy moved it: %v", err)
	}

	plain := map[string]string{"EMAIL": "a@b.c"}
	for _, slug := range []string{"honeygain", "no-such-service"} {
		if got, err := m.CredentialsToSave(slug, plain); err != nil || got["EMAIL"] != "a@b.c" {
			t.Fatalf("%s: CredentialsToSave = %v, %v", slug, got, err)
		}

	}
}

// What a deploy did with the wallet goes into the history in words, never with
// the phrase, and the address is kept with the deployment.
func TestEachWalletOutcomeIsRecordedWithoutThePhrase(t *testing.T) {
	for _, tc := range []struct {
		name   string
		wallet *runtime.WalletResult
		event  string
	}{
		{"created", &runtime.WalletResult{Created: true, Phrase: otherWalletPhrase, Address: "addr-new"}, "wallet_created"},
		{"imported", &runtime.WalletResult{Created: true, Address: "addr-pasted"}, "wallet_imported"},
		{"moved", &runtime.WalletResult{Created: true, Migrated: true, Address: walletTestAddress}, "wallet_moved"},
		{"kept", &runtime.WalletResult{Address: "addr-kept"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, st := newTestStoreInDir(t)
			provider := &fakeProvider{deployResult: runtime.ContainerInfo{Slug: "sia-hostd", Status: "running", Wallet: tc.wallet}}
			m := NewManager(provider, realCatalogForWallet(t), st)
			dep, err := m.DeployWith(context.Background(), "sia-hostd", siaValues(), DeployOptions{Wallet: &runtime.WalletRequest{Mode: runtime.WalletGenerate}})
			if err != nil {
				t.Fatalf("DeployWith: %v", err)
			}
			if stored, ok, _ := st.GetDeployment("sia-hostd"); !ok || stored.WalletAddress != tc.wallet.Address || dep.WalletAddress != tc.wallet.Address {
				t.Fatalf("the address was not kept: stored %q, returned %q", stored.WalletAddress, dep.WalletAddress)
			}
			history := historyOf(t, dir, "sia-hostd")
			assertHistoryHoldsNoPhrase(t, history)
			found := false
			for _, line := range history {
				if strings.HasPrefix(line, "wallet_") {
					if tc.event == "" || !strings.HasPrefix(line, tc.event+": ") {
						t.Fatalf("history line %q, want only %q", line, tc.event)
					}
					found = true
				}
			}
			if found != (tc.event != "") {
				t.Fatalf("history %v, want event %q", history, tc.event)
			}
		})
	}
}

// A request with no mode is a keep, and it carries the stored seed to the
// runtime, never in the env.
func TestAnEmptyWalletChoiceKeepsTheNodesWallet(t *testing.T) {
	st := newTestStore(t)
	legacy := siaValues()
	legacy["HOSTD_WALLET_SEED"] = walletTestPhrase
	if err := st.SaveCredentials("sia-hostd", legacy); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}
	provider := &fakeProvider{deployErr: errors.New("stop here")}
	m := NewManager(provider, realCatalogForWallet(t), st)
	_, _ = m.DeployWith(context.Background(), "sia-hostd", siaValues(), DeployOptions{Wallet: &runtime.WalletRequest{}})
	spec := provider.deploySpecs[0]
	if spec.Wallet == nil || spec.Wallet.Mode != runtime.WalletKeep || spec.Wallet.Stored != walletTestPhrase {
		t.Fatalf("an empty choice did not become a keep with the stored seed: %+v", spec.Wallet)
	}
	if _, ok := spec.Env["HOSTD_WALLET_SEED"]; ok {
		t.Fatal("the stored seed reached the env")
	}
}

// A stored value that is not a valid phrase is no wallet: once the file holds one,
// it goes, and nothing goes when nothing was stored.
func TestAStoredValueThatIsNoWalletIsDeleted(t *testing.T) {
	dir, st := newTestStoreInDir(t)
	legacy := siaValues()
	legacy["HOSTD_WALLET_SEED"] = "not a seed phrase"
	if err := st.SaveCredentials("sia-hostd", legacy); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}
	provider := &fakeProvider{deployResult: runtime.ContainerInfo{Slug: "sia-hostd", Status: "running", Wallet: &runtime.WalletResult{Created: true, Address: walletTestAddress}}}
	m := NewManager(provider, realCatalogForWallet(t), st)
	if _, err := m.DeployWith(context.Background(), "sia-hostd", siaValues(), DeployOptions{Wallet: &runtime.WalletRequest{Mode: runtime.WalletImport, Phrase: walletTestPhrase}}); err != nil {
		t.Fatalf("DeployWith: %v", err)
	}
	stored, _ := st.GetCredentials("sia-hostd")
	if _, ok := stored["HOSTD_WALLET_SEED"]; ok || stored["HOSTD_API_PASSWORD"] == "" {
		t.Fatalf("the stored non-wallet survived, or the settings went with it: %v", stored)
	}
	for _, line := range historyOf(t, dir, "sia-hostd") {
		if strings.HasPrefix(line, "wallet_error") || strings.HasPrefix(line, "wallet_stored_differs") {
			t.Fatalf("an ordinary delete was recorded as a problem: %q", line)
		}
	}

	// A second deploy with nothing stored changes nothing.
	if _, err := m.DeployWith(context.Background(), "sia-hostd", siaValues(), DeployOptions{}); err != nil {
		t.Fatalf("second DeployWith: %v", err)
	}
	if again, _ := st.GetCredentials("sia-hostd"); again["HOSTD_API_PASSWORD"] == "" {
		t.Fatal("a deploy with nothing stored lost the settings")
	}
}

// seedReaderProvider is a runtime that can read the wallet file back.
type seedReaderProvider struct {
	fakeProvider
	phrase string
	err    error
}

func (p *seedReaderProvider) ReadWalletSeed(context.Context, string, catalog.Service) (string, error) {
	return p.phrase, p.err
}

// The reveal asks the runtime only for a service with a seed file, and only one
// that can read it.
func TestTheRevealAsksOnlyARuntimeThatCanRead(t *testing.T) {
	m := NewManager(&fakeProvider{}, realCatalogForWallet(t), newTestStore(t))
	for slug, says := range map[string]string{"no-such-service": "unknown service", "honeygain": "keeps no wallet seed", "sia-hostd": "cannot read"} {
		if got, err := m.RevealWalletSeed(context.Background(), slug); got != "" || err == nil || !strings.Contains(err.Error(), says) {
			t.Fatalf("%s: RevealWalletSeed = %q, %v; want %q", slug, got, err, says)
		}
	}
	reader := &seedReaderProvider{phrase: walletTestPhrase}
	m = NewManager(reader, realCatalogForWallet(t), newTestStore(t))
	if got, err := m.RevealWalletSeed(context.Background(), "sia-hostd"); err != nil || got != walletTestPhrase {
		t.Fatalf("RevealWalletSeed = %q, %v", got, err)
	}
	reader.phrase, reader.err = "", errors.New("no wallet file yet")
	if _, err := m.RevealWalletSeed(context.Background(), "sia-hostd"); err == nil || err.Error() != "no wallet file yet" {
		t.Fatalf("the runtime's refusal was not passed on: %v", err)
	}
}
