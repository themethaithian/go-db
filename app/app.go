// Package app is the Wails shell: a thin adapter that wires the embedded
// frontend into a native window. It owns no domain logic — that lives under
// internal/.
package app

import (
	"context"
	"embed"
)

//go:embed all:frontend/dist
var assets embed.FS

// Assets returns the embedded frontend build output.
func Assets() embed.FS {
	return assets
}

// App holds the Wails runtime context. It intentionally exposes no bound
// methods yet; those arrive with the features that need them.
type App struct {
	ctx context.Context
}

// New creates a new App instance.
func New() *App {
	return &App{}
}

// Startup is called by Wails when the app starts, before the frontend is
// loaded. It saves the runtime context for later use.
func (a *App) Startup(ctx context.Context) {
	a.ctx = ctx
}
