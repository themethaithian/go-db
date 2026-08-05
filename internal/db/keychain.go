package db

import "errors"

// ErrSecretNotFound reports that the keychain holds no secret for a Profile.
// A Profile may legitimately have none — a passwordless local database, for
// instance — so callers treat it as an absence, not a failure.
var ErrSecretNotFound = errors.New("db: no secret stored for profile")

// Keychain is the port through which passwords reach the OS credential store.
// Secrets are keyed by Profile name and never written to disk by go-db.
//
// Implementations must report a missing secret as ErrSecretNotFound from both
// Get and Delete.
type Keychain interface {
	// Set stores secret for profileName, replacing any existing secret.
	Set(profileName, secret string) error
	// Get returns the secret stored for profileName, or ErrSecretNotFound.
	Get(profileName string) (string, error)
	// Delete removes the secret stored for profileName, or reports
	// ErrSecretNotFound if there was none.
	Delete(profileName string) error
}
