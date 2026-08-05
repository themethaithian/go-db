// Package dbtest provides fakes for the ports declared by package db, for use
// in the unit tests of packages that depend on them. It is test support only —
// nothing in the shipping binary imports it.
package dbtest

import (
	"sync"

	"github.com/themethaithian/go-db/internal/db"
)

// FakeKeychain is an in-memory db.Keychain. It behaves like the OS credential
// store — secrets keyed by Profile name, ErrSecretNotFound for absences — while
// letting a test read back what was stored. It is safe for concurrent use.
type FakeKeychain struct {
	mu      sync.Mutex
	secrets map[string]string
}

// NewFakeKeychain returns an empty FakeKeychain.
func NewFakeKeychain() *FakeKeychain {
	return &FakeKeychain{secrets: make(map[string]string)}
}

// Set stores secret under profileName, replacing any existing secret.
func (k *FakeKeychain) Set(profileName, secret string) error {
	k.mu.Lock()
	defer k.mu.Unlock()

	k.secrets[profileName] = secret
	return nil
}

// Get returns the secret stored under profileName, or db.ErrSecretNotFound.
func (k *FakeKeychain) Get(profileName string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	secret, ok := k.secrets[profileName]
	if !ok {
		return "", db.ErrSecretNotFound
	}
	return secret, nil
}

// Delete removes the secret stored under profileName, or reports
// db.ErrSecretNotFound if there was none.
func (k *FakeKeychain) Delete(profileName string) error {
	k.mu.Lock()
	defer k.mu.Unlock()

	if _, ok := k.secrets[profileName]; !ok {
		return db.ErrSecretNotFound
	}
	delete(k.secrets, profileName)
	return nil
}

var _ db.Keychain = (*FakeKeychain)(nil)
