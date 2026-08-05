package service

import (
	"time"

	"github.com/themethaithian/go-db/internal/db"
	"github.com/themethaithian/go-db/internal/guard"
)

// AppService is the App Service facade. It owns the domain collaborators the
// adapters must not reach around: the ProfileStore, the Connection Registry,
// and the Approval Gate's queue of withheld mutations and its audit log.
type AppService struct {
	profiles *db.ProfileStore
	registry *db.Registry

	// pending holds the mutations waiting for a decision. It lives here rather
	// than in an adapter because a confirmation must find the statement the
	// gate withheld, not one the UI sends back: the ID is the only thing that
	// crosses the boundary.
	pending *guard.Queue
	audit   guard.AuditLog
	clock   guard.Clock
}

// New returns an App Service backed by the given ProfileStore, connecting to
// MySQL and recording every gate decision in audit.
func New(profiles *db.ProfileStore, audit guard.AuditLog) *AppService {
	return NewWithDriver(profiles, db.NewMySQLDriver(), audit, time.Now)
}

// NewWithDriver returns an App Service whose Connection Registry opens
// connections through driver and whose gate decisions are timestamped by clock.
// It is the seam tests use to substitute a fake database and a clock they
// control; the shipping binary calls New. A nil clock means time.Now.
func NewWithDriver(profiles *db.ProfileStore, driver db.Driver, audit guard.AuditLog, clock guard.Clock) *AppService {
	if clock == nil {
		clock = time.Now
	}
	return &AppService{
		profiles: profiles,
		registry: db.NewRegistry(driver, profiles),
		pending:  guard.NewQueue(clock),
		audit:    audit,
		clock:    clock,
	}
}

// Close closes every connection the Connection Registry holds. It is called on
// shutdown; the facade is reusable afterwards, with an empty Registry.
//
// Mutations still waiting for a decision are dropped, which is the right
// ending: an unanswered confirmation is a mutation nobody approved.
func (s *AppService) Close() error {
	return s.registry.Close()
}
