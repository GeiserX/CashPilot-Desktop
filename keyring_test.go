package main

import (
	"sync"

	"github.com/zalando/go-keyring"
)

// The upstream mock has a shared map and resets its provider on every
// MockInit call. Use the same lock for access and for reset or failure injection.
type lockedTestKeyring struct {
	mu sync.Mutex
}

var testKeyring lockedTestKeyring

func (r *lockedTestKeyring) Get(service, user string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return keyring.Get(service, user)
}

func (r *lockedTestKeyring) Set(service, user, password string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return keyring.Set(service, user, password)
}

func (r *lockedTestKeyring) Delete(service, user string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return keyring.Delete(service, user)
}

func (r *lockedTestKeyring) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	keyring.MockInit()
}

func (r *lockedTestKeyring) fail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	keyring.MockInitWithError(err)
}
