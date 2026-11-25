package config

import (
	"errors"
	"testing"

	"github.com/zalando/go-keyring"
)

// Deleting the user's data takes every keychain secret, and an empty keychain is
// not an error.
func TestDeleteKeychainSecretsTakesEverySecret(t *testing.T) {
	keyring.MockInit()
	for _, user := range keychainEntries {
		if err := keyring.Set(keyringService, user, "v"); err != nil {
			t.Fatal(err)
		}
	}
	if err := keyring.Set("Other App", keyringUser, "keep"); err != nil {
		t.Fatal(err)
	}
	if err := DeleteKeychainSecrets(); err != nil {
		t.Fatalf("DeleteKeychainSecrets: %v", err)
	}
	for _, user := range keychainEntries {
		if _, err := keyring.Get(keyringService, user); !errors.Is(err, keyring.ErrNotFound) {
			t.Errorf("%s is still in the keychain (err %v)", user, err)
		}
	}
	if v, err := keyring.Get("Other App", keyringUser); err != nil || v != "keep" {
		t.Errorf("another app's secret was touched: %q, %v", v, err)
	}
	if err := DeleteKeychainSecrets(); err != nil {
		t.Errorf("a second delete, with nothing left, failed: %v", err)
	}
}

func TestDeleteKeychainSecretsReportsAKeychainError(t *testing.T) {
	keyring.MockInitWithError(errors.New("locked"))
	defer keyring.MockInit()
	if err := DeleteKeychainSecrets(); err == nil {
		t.Fatal("a keychain that refuses gave no error")
	}
}
