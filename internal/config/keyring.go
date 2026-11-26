package config

import "github.com/zalando/go-keyring"

type credentialKeyring interface {
	Get(service, user string) (string, error)
	Set(service, user, password string) error
	Delete(service, user string) error
}

type osCredentialKeyring struct{}

func (osCredentialKeyring) Get(service, user string) (string, error) {
	return keyring.Get(service, user)
}

func (osCredentialKeyring) Set(service, user, password string) error {
	return keyring.Set(service, user, password)
}

func (osCredentialKeyring) Delete(service, user string) error {
	return keyring.Delete(service, user)
}

var credentials credentialKeyring = osCredentialKeyring{}

// SetCredentialKeyringForTest installs a test keyring for this process.
// Call once in TestMain, before starting any tests or their goroutines.
func SetCredentialKeyringForTest(store credentialKeyring) {
	credentials = store
}
