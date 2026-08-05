// Package app is the Wails shell: a thin adapter that wires the embedded
// frontend into a native window. It owns no domain logic — that lives under
// internal/.
package app

import (
	"context"
	"embed"

	"github.com/themethaithian/go-db/internal/db"
	"github.com/themethaithian/go-db/internal/guard"
	"github.com/themethaithian/go-db/internal/service"
)

//go:embed all:frontend/dist
var assets embed.FS

// Assets returns the embedded frontend build output.
func Assets() embed.FS {
	return assets
}

// App holds the Wails runtime context and the App Service facade. It exposes
// bound methods that delegate 1:1 to the facade — Wails marshals the
// domain types (db.Profile) to TS models for the frontend. It owns no logic
// of its own.
type App struct {
	ctx context.Context
	svc *service.AppService
}

// New creates a new App instance backed by svc.
func New(svc *service.AppService) *App {
	return &App{svc: svc}
}

// Startup is called by Wails when the app starts, before the frontend is
// loaded. It saves the runtime context for later use.
func (a *App) Startup(ctx context.Context) {
	a.ctx = ctx
}

// Shutdown is called by Wails as the app closes. It closes every connection
// the Connection Registry holds so nothing is left dangling.
func (a *App) Shutdown(ctx context.Context) {
	a.svc.Close()
}

// ListProfiles returns every saved Profile, ordered by name. The connection
// manager UI uses this both to render the Profile list and to populate the
// edit form, so no separate GetProfile binding is exposed.
func (a *App) ListProfiles() ([]db.Profile, error) {
	return a.svc.ListProfiles()
}

// SaveProfile creates or replaces the Profile saved under profile.Name. A
// non-empty password replaces the Profile's stored secret; an empty one
// leaves the keychain untouched.
func (a *App) SaveProfile(profile db.Profile, password string) error {
	return a.svc.SaveProfile(profile, password)
}

// DeleteProfile removes the Profile saved under name along with its
// keychain secret.
func (a *App) DeleteProfile(name string) error {
	return a.svc.DeleteProfile(name)
}

// TestConnection opens a throwaway connection for the named Profile and
// reports what happened, without touching the Connection Registry.
func (a *App) TestConnection(name string) service.ConnectionTest {
	return a.svc.TestConnection(a.ctx, name)
}

// Connect opens a connection for the named Profile and holds it in the
// Connection Registry.
func (a *App) Connect(name string) error {
	return a.svc.Connect(a.ctx, name)
}

// Disconnect closes the connection held for the named Profile.
func (a *App) Disconnect(name string) error {
	return a.svc.Disconnect(name)
}

// ConnectedProfiles returns the names of the Profiles currently connected.
func (a *App) ConnectedProfiles() []string {
	return a.svc.ConnectedProfiles()
}

// Classify reports whether sql is provably read-only, without connecting to
// anything or running it. The editor calls it as the human types, to drive
// the read/mutation badge.
func (a *App) Classify(sql string) guard.Classification {
	return a.svc.Classify(sql)
}

// RunQuery submits sql on the named Profile from the SQL editor. The editor
// is the human Origin — an AI query arrives only through the MCP server,
// which is not wired in yet — so this binding hardcodes guard.OriginHuman
// rather than accepting it as a parameter.
func (a *App) RunQuery(profileName, sql string) service.QueryResult {
	return a.svc.RunQuery(a.ctx, profileName, sql, guard.OriginHuman)
}
