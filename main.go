// Command go-db is the single binary for the go-db desktop app. By default
// it launches the Wails desktop shell; `go-db mcp <profile-name>` instead
// runs a stdio MCP proxy pinned to that Profile.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/themethaithian/go-db/app"
	"github.com/themethaithian/go-db/internal/api"
	"github.com/themethaithian/go-db/internal/db"
	"github.com/themethaithian/go-db/internal/guard"
	"github.com/themethaithian/go-db/internal/mcp"
	"github.com/themethaithian/go-db/internal/service"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "mcp" {
		runMCP(os.Args[2:])
		return
	}

	// One directory holds everything go-db keeps on disk: the Profiles and the
	// Approval Gate's audit log. Passwords are not among them — those live in
	// the OS keychain and never touch disk.
	configDir, err := db.DefaultProfileDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "go-db:", err)
		os.Exit(1)
	}

	svc := service.NewWithDriver(
		db.NewProfileStore(configDir, db.NewOSKeychain()),
		db.NewMySQLDriver(),
		guard.NewJSONLAuditLog(configDir),
		time.Now,
	)
	shell := app.New(svc)

	// The local API is a second, thin adapter over the same facade: a
	// loopback-only, token-gated pipe the MCP proxy talks to. It shares the
	// app's config dir for its token file and follows the shell's own
	// lifecycle — up after Startup, down before Shutdown finishes.
	apiServer := api.New(svc, configDir)

	err = wails.Run(&options.App{
		Title:  "go-db",
		Width:  1024,
		Height: 768,
		Assets: app.Assets(),
		OnStartup: func(ctx context.Context) {
			shell.Startup(ctx)
			if err := apiServer.Start(); err != nil {
				fmt.Fprintln(os.Stderr, "go-db: starting local API:", err)
			}
		},
		OnShutdown: func(ctx context.Context) {
			apiServer.Close()
			shell.Shutdown(ctx)
		},
		Bind: []interface{}{shell},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "go-db:", err)
		os.Exit(1)
	}
}

// runMCP handles the `go-db mcp <profile-name>` subcommand: a required
// positional Profile argument, and nothing else. args is os.Args with "mcp"
// itself already stripped.
func runMCP(args []string) {
	if len(args) < 1 || args[0] == "" {
		fmt.Fprintln(os.Stderr, "usage: go-db mcp <profile-name>")
		os.Exit(2)
	}

	if err := mcp.Run(args[0]); err != nil {
		fmt.Fprintln(os.Stderr, "go-db mcp:", err)
		os.Exit(1)
	}
}
