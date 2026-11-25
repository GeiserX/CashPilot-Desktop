package config

import (
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"
)

// AppDataDir is the folder that holds everything the app stores on disk: the
// config, the database, the native earners and the logs. The installed app and
// the portable exe read the same folder, so a user who moves from one to the
// other keeps everything.
func AppDataDir() (string, error) {
	return appDataDir()
}

// keychainEntries are every secret the app keeps in the OS keychain.
var keychainEntries = []string{keyringUser, keyringUserFleet, keyringUserUpstreamEnrol, keyringUserUpstreamWorker, pairingKey, pairingKeyAlt}

// DeleteKeychainSecrets removes every secret the app keeps in the OS keychain.
// An entry that is not there is not an error. It is part of deleting the user's
// data: without the master key the saved logins in the database cannot be read.
func DeleteKeychainSecrets() error {
	var errs []error
	for _, user := range keychainEntries {
		if err := credentials.Delete(keyringService, user); err != nil && !errors.Is(err, keyring.ErrNotFound) {
			errs = append(errs, fmt.Errorf("%s: %w", user, err))
		}
	}
	return errors.Join(errs...)
}
