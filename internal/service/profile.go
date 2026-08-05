package service

import "github.com/themethaithian/go-db/internal/db"

// ListProfiles returns every saved Profile, ordered by name.
func (s *AppService) ListProfiles() ([]db.Profile, error) {
	return s.profiles.List()
}

// GetProfile returns the Profile saved under name, or db.ErrProfileNotFound.
func (s *AppService) GetProfile(name string) (db.Profile, error) {
	return s.profiles.Get(name)
}

// SaveProfile creates or replaces the Profile saved under profile.Name.
//
// A non-empty password replaces the Profile's stored secret; an empty one
// leaves the keychain untouched, so editing a Profile without retyping its
// password keeps the existing secret.
func (s *AppService) SaveProfile(profile db.Profile, password string) error {
	return s.profiles.Save(profile, password)
}

// DeleteProfile removes the Profile saved under name along with its keychain
// secret. It reports db.ErrProfileNotFound if no such Profile exists.
func (s *AppService) DeleteProfile(name string) error {
	return s.profiles.Delete(name)
}
