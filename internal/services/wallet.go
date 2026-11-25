package services

import (
	"context"
	"fmt"

	"github.com/GeiserX/CashPilot-Desktop/internal/catalog"
	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/siaseed"
)

// A service whose catalog entry keeps its wallet seed in a file (catalog.SeedFile,
// Sia's hostd) never has the seed stored here or passed as a variable. The runtime
// writes it into the service's own data volume (runtime/seedfile.go); this file is
// what the manager does around that:
//
//   - The seed is never saved: CredentialsToSave drops a typed one.
//   - A seed saved before this (Desktop used to keep it, encrypted, with the other
//     values) is kept only until a deploy has written it into the file, then deleted.
//     Saving the form without it does not delete it early, so no deploy can lose
//     the only copy Desktop had.
//   - The seed is never sent to the window: App.GetCredentials omits it.

// seedKey is the name of svc's seed-file variable, or "".
func seedKey(svc catalog.Service) string {
	if item, ok := svc.SeedVar(); ok {
		return item.Key
	}
	return ""
}

// HasSeedFile reports whether slug keeps its wallet seed in a file.
func (m *Manager) HasSeedFile(slug string) bool {
	svc, ok := m.catalog.Get(slug)
	return ok && seedKey(svc) != ""
}

// CredentialsToSave is values as the store may keep them for slug: without a typed
// wallet seed, which is never stored, and with one stored before the seed file
// existed, which stays until a deploy has moved it (see forgetStoredSeed).
func (m *Manager) CredentialsToSave(slug string, values map[string]string) (map[string]string, error) {
	svc, ok := m.catalog.Get(slug)
	if !ok {
		return values, nil
	}
	key := seedKey(svc)
	if key == "" {
		return values, nil
	}
	out := copyMap(runtime.StripSeedVars(svc, values))
	stored, err := m.store.GetCredentials(slug)
	if err != nil {
		return nil, err
	}
	if legacy := stored[key]; legacy != "" {
		out[key] = legacy
	}
	return out, nil
}

func copyMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// walletFor is the wallet request a deploy of svc hands the runtime, with the seed
// Desktop stored before the file existed, and credentials without any seed. A
// service with no seed file gets nil and its credentials unchanged.
func (m *Manager) walletFor(svc catalog.Service, slug string, credentials map[string]string, asked *runtime.WalletRequest) (*runtime.WalletRequest, map[string]string, error) {
	key := seedKey(svc)
	if key == "" {
		return nil, credentials, nil
	}
	req := runtime.WalletRequest{Mode: runtime.WalletKeep}
	if asked != nil {
		req.Mode, req.Phrase = asked.Mode, asked.Phrase
		if req.Mode == "" {
			req.Mode = runtime.WalletKeep
		}
	}
	// A caller that still passes the stored blob passes the seed with it; one that
	// passes the form's values does not, so the store is asked too.
	req.Stored = credentials[key]
	if req.Stored == "" {
		if stored, err := m.store.GetCredentials(slug); err == nil {
			req.Stored = stored[key]
		}
	}
	if err := runtime.CheckWalletRequest(svc, &req); err != nil {
		return nil, nil, err
	}
	return &req, runtime.StripSeedVars(svc, credentials), nil
}

// afterWallet records what a deploy did with the wallet: the public address, the
// stored seed deleted now that the file holds one, and a history line that never
// carries the phrase.
func (m *Manager) afterWallet(slug string, svc catalog.Service, result *runtime.WalletResult) {
	if result == nil {
		return
	}
	if err := m.store.SetWalletAddress(slug, result.Address); err != nil {
		m.store.RecordEvent(slug, "wallet_error", "the wallet address could not be recorded: "+err.Error())
	}
	m.forgetStoredSeed(slug, svc, result.Address)
	switch {
	case result.Migrated:
		m.store.RecordEvent(slug, "wallet_moved", "the wallet seed phrase moved out of the app and the container's settings into the node's data volume")
	case result.Created && result.Phrase != "":
		m.store.RecordEvent(slug, "wallet_created", "a new wallet was created in the node's data volume")
	case result.Created:
		m.store.RecordEvent(slug, "wallet_imported", "the pasted wallet seed phrase was written into the node's data volume")
	}
}

// forgetStoredSeed deletes the seed Desktop stored for slug before the seed file
// existed. Called only once the file holds a phrase, whose wallet is at address. A
// stored phrase that is a valid wallet goes only when it is that same wallet: one
// that differs may be the only copy of a funded wallet, so it stays (never shown to
// the window) and the history says so.
func (m *Manager) forgetStoredSeed(slug string, svc catalog.Service, address string) {
	key := seedKey(svc)
	stored, err := m.store.GetCredentials(slug)
	if err != nil || key == "" {
		return
	}
	value, ok := stored[key]
	if !ok {
		return
	}
	if item, _ := svc.SeedVar(); siaseed.Problem(value, item.SeedFile.Words) == "" {
		if same, err := siaseed.Address(value); err != nil || address == "" || same != address {
			m.store.RecordEvent(slug, "wallet_stored_differs", "the app still holds an older wallet seed phrase that is not the wallet in the node's data volume, so it was kept")
			return
		}
	}
	// ScrubCredential, not SaveCredentials: every earlier save left an encrypted
	// copy of the seed in the database file, and its key stays in the keychain.
	_, busy, err := m.store.ScrubCredential(slug, key)
	switch {
	case err != nil:
		m.store.RecordEvent(slug, "wallet_error", "the old stored copy of the wallet seed phrase could not be deleted: "+err.Error())
	case busy:
		m.store.RecordEvent(slug, "wallet_error", "the old stored copy of the wallet seed phrase was deleted, but the database log was busy, so an older copy may stay in it until the next checkpoint")
	}
}

// RevealWalletSeed reads slug's wallet seed phrase back from its file. The caller
// gates it (App.RevealWalletSeed); this only reads.
func (m *Manager) RevealWalletSeed(ctx context.Context, slug string) (string, error) {
	svc, ok := m.catalog.Get(slug)
	if !ok {
		return "", fmt.Errorf("unknown service: %s", slug)
	}
	if seedKey(svc) == "" {
		return "", fmt.Errorf("%s keeps no wallet seed", svc.Name)
	}
	reader, ok := m.providerForSlug(slug).(runtime.SeedReader)
	if !ok {
		return "", fmt.Errorf("this runtime cannot read %s's wallet file", svc.Name)
	}
	return reader.ReadWalletSeed(ctx, slug, svc)
}
