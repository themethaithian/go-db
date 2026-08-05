package service

import "github.com/themethaithian/go-db/internal/db"

// AppService is the App Service facade. It owns the domain collaborators the
// adapters must not reach around: the ProfileStore and the Connection Registry
// today, the Approval Gate as later slices land.
type AppService struct {
	profiles *db.ProfileStore
	registry *db.Registry
}

// New returns an App Service backed by the given ProfileStore, connecting to
// MySQL.
func New(profiles *db.ProfileStore) *AppService {
	return NewWithDriver(profiles, db.NewMySQLDriver())
}

// NewWithDriver returns an App Service whose Connection Registry opens
// connections through driver. It is the seam tests use to substitute a fake
// database; the shipping binary calls New.
func NewWithDriver(profiles *db.ProfileStore, driver db.Driver) *AppService {
	return &AppService{
		profiles: profiles,
		registry: db.NewRegistry(driver, profiles),
	}
}

// Close closes every connection the Connection Registry holds. It is called on
// shutdown; the facade is reusable afterwards, with an empty Registry.
func (s *AppService) Close() error {
	return s.registry.Close()
}
