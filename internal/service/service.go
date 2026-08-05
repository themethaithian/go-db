package service

import "github.com/themethaithian/go-db/internal/db"

// AppService is the App Service facade. It owns the domain collaborators the
// adapters must not reach around: the ProfileStore today, the Connection
// Registry and the Approval Gate as later slices land.
type AppService struct {
	profiles *db.ProfileStore
}

// New returns an App Service backed by the given ProfileStore.
func New(profiles *db.ProfileStore) *AppService {
	return &AppService{profiles: profiles}
}
