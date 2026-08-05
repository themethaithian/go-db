package db

import (
	"errors"

	"github.com/zalando/go-keyring"
)

// keychainService is the service name go-db registers its secrets under in the
// OS credential store (macOS Keychain, Windows Credential Manager, Linux
// secret-service).
const keychainService = "go-db"

// NewOSKeychain returns the Keychain backed by the OS credential store.
//
// This is a thin adapter over github.com/zalando/go-keyring: it holds no logic
// of its own beyond translating the library's not-found error into
// ErrSecretNotFound, and is therefore verified by build rather than by unit
// tests. Tests of code that depends on the Keychain port use the fake in
// internal/db/dbtest.
func NewOSKeychain() Keychain {
	return osKeychain{}
}

type osKeychain struct{}

func (osKeychain) Set(profileName, secret string) error {
	return keyring.Set(keychainService, profileName, secret)
}

func (osKeychain) Get(profileName string) (string, error) {
	secret, err := keyring.Get(keychainService, profileName)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrSecretNotFound
	}
	return secret, err
}

func (osKeychain) Delete(profileName string) error {
	err := keyring.Delete(keychainService, profileName)
	if errors.Is(err, keyring.ErrNotFound) {
		return ErrSecretNotFound
	}
	return err
}
