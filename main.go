// Command go-db is the single binary for the go-db desktop app. By default
// it launches the Wails desktop shell; `go-db mcp` instead runs the MCP
// stdio proxy (not yet implemented).
package main

import (
	"fmt"
	"os"

	"github.com/themethaithian/go-db/app"
	"github.com/themethaithian/go-db/internal/db"
	"github.com/themethaithian/go-db/internal/service"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "mcp" {
		fmt.Fprintln(os.Stderr, "go-db mcp: proxy not yet implemented")
		os.Exit(1)
	}

	profileDir, err := db.DefaultProfileDir()
	if err != nil {
		fmt.Fprintln(os.Stderr, "go-db:", err)
		os.Exit(1)
	}

	svc := service.NewWithDriver(db.NewProfileStore(profileDir, db.NewOSKeychain()), db.NewMySQLDriver())
	shell := app.New(svc)

	err = wails.Run(&options.App{
		Title:      "go-db",
		Width:      1024,
		Height:     768,
		Assets:     app.Assets(),
		OnStartup:  shell.Startup,
		OnShutdown: shell.Shutdown,
		Bind:       []interface{}{shell},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "go-db:", err)
		os.Exit(1)
	}
}
