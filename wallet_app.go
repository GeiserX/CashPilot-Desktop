package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/GeiserX/CashPilot-Desktop/internal/runtime"
	"github.com/GeiserX/CashPilot-Desktop/internal/services"
	"github.com/GeiserX/CashPilot-Desktop/internal/store"
)

// The window's side of a wallet seed kept in a file (internal/runtime/seedfile.go):
// the deploy that makes, takes or keeps a wallet, and the reveal that reads it back.
// The phrase reaches the window twice at most: once when a new wallet is made, and
// each time the owner asks to reveal it. Neither is stored, logged or put in history.

// WalletChoice is the deploy form's wallet choice for a service that keeps its seed
// in a file: "generate" (a new wallet, the default), "import" (Phrase is the wallet
// the owner already has) or "keep" (the wallet the node has).
type WalletChoice struct {
	Mode   string `json:"mode"`
	Phrase string `json:"phrase"`
}

// WalletOutcome is what the deploy did with the wallet. Phrase is set only for a
// wallet made on this deploy: the window shows it once, asks for three of its words
// back, and drops it.
type WalletOutcome struct {
	Created  bool   `json:"created"`
	Migrated bool   `json:"migrated"`
	Address  string `json:"address"`
	Phrase   string `json:"phrase,omitempty"`
}

// WalletDeployResult is DeployServiceWallet's answer.
type WalletDeployResult struct {
	Deployment store.Deployment `json:"deployment"`
	// Wallet is nil for a service with no seed file.
	Wallet *WalletOutcome `json:"wallet"`
}

// DeployServiceWallet is DeployService for a service that keeps its wallet seed in
// a file (Sia's hostd), with the owner's wallet choice.
func (a *App) DeployServiceWallet(slug string, values map[string]string, image *string, instances *int, choice WalletChoice) (WalletDeployResult, error) {
	request := &runtime.WalletRequest{Mode: choice.Mode, Phrase: choice.Phrase}
	choice.Phrase = ""
	deployment, wallet, err := a.deployService(slug, values, services.DeployOptions{Image: image, Instances: instances, Wallet: request}, true)
	request.Phrase = ""
	if err != nil {
		return WalletDeployResult{}, err
	}
	result := WalletDeployResult{Deployment: deployment}
	if wallet != nil {
		result.Wallet = &WalletOutcome{Created: wallet.Created, Migrated: wallet.Migrated, Address: wallet.Address, Phrase: wallet.Phrase}
		wallet.Phrase = ""
	}
	return result, nil
}

// Reveals of a wallet seed are rate-limited per service: walletRevealLimit tries,
// refused ones included, in walletRevealWindow.
const (
	walletRevealLimit  = 3
	walletRevealWindow = 10 * time.Minute
)

// errRevealName is the refusal of a reveal whose typed name is wrong.
var errRevealName = errors.New("type the service's name exactly as shown to reveal its seed phrase")

// RevealWalletSeed reads slug's wallet seed phrase back from the file in its data
// volume, for the owner to write down.
//
// The gate is the strongest Desktop has for a sensitive action: the owner types the
// service's name (as a restore asks), it is checked here, not only in the window,
// and every try counts towards a limit of three in ten minutes. Desktop has no app
// password and no OS sign-in prompt, and anyone who can drive this desktop session
// can also run `docker cp` on the volume, so the gate guards against a slip and a
// glance at the screen, not against the account itself. Each reveal, and each
// refusal, goes into the service's history without the phrase.
func (a *App) RevealWalletSeed(slug, typedName string) (string, error) {
	if err := a.ready(); err != nil {
		return "", err
	}
	svc, ok := a.catalog.Get(slug)
	if !ok {
		return "", fmt.Errorf("unknown service: %s", slug)
	}
	if !a.services.HasSeedFile(slug) {
		return "", fmt.Errorf("%s keeps no wallet seed", svc.Name)
	}
	if !a.walletRevealAllowed(slug, time.Now()) {
		a.store.RecordEvent(slug, "wallet_reveal_refused", "too many tries")
		return "", fmt.Errorf("the seed phrase was asked for %d times in %d minutes. Wait a few minutes, then try again", walletRevealLimit, int(walletRevealWindow/time.Minute))
	}
	typed := strings.TrimSpace(typedName)
	if !strings.EqualFold(typed, svc.Name) && !strings.EqualFold(typed, slug) {
		a.store.RecordEvent(slug, "wallet_reveal_refused", "the typed name did not match")
		return "", errRevealName
	}
	phrase, err := a.services.RevealWalletSeed(a.ctx, slug)
	if err != nil {
		a.store.RecordEvent(slug, "wallet_reveal_error", err.Error())
		return "", err
	}
	a.store.RecordEvent(slug, "wallet_revealed", "the wallet seed phrase was shown in the window")
	return phrase, nil
}

// walletRevealAllowed counts a reveal try of slug and reports whether it is within
// the limit.
func (a *App) walletRevealAllowed(slug string, now time.Time) bool {
	a.walletRevealMu.Lock()
	if a.walletReveals == nil {
		a.walletReveals = newFleetRateLimiter(walletRevealLimit, walletRevealWindow)
	}
	limiter := a.walletReveals
	a.walletRevealMu.Unlock()
	return limiter.allow(slug, now)
}
