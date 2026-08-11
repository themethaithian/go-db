// Command go-db is the single binary for the go-db desktop app. By default
// it launches the Wails desktop shell; `go-db mcp <profile-name>` instead
// runs a stdio MCP proxy pinned to that Profile.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/themethaithian/go-db/app"
	"github.com/themethaithian/go-db/internal/api"
	"github.com/themethaithian/go-db/internal/db"
	"github.com/themethaithian/go-db/internal/guard"
	"github.com/themethaithian/go-db/internal/mcp"
	"github.com/themethaithian/go-db/internal/service"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
)

// processStart is the process's launch time, captured as early as possible
// so App.Ready can report launch-to-usable (CLAUDE.md budget: < 2 s) once
// the frontend calls it on first mount. It is not meaningful for the `mcp`
// subcommand, which never constructs an App.
var processStart = time.Now()

// version is stamped at build time by the release workflow via
// `-ldflags "-X main.version=vX.Y.Z"`, taken from the git tag. Local/dev
// builds never pass that flag, so they report "dev" — the App's update
// check treats "dev" as never outdated rather than nagging a developer
// running off source.
var version = "dev"

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

	// go-redis logs dial failures through a process-global logger of its own,
	// straight to stderr with a "redis:" prefix and a file and line from
	// inside the library. A Profile pointed at a server that is not up is an
	// ordinary thing to have saved, and the app already tells the human about
	// it where they can see it — so the library's copy is routed here, in the
	// shell, into one attributable line. This is a fact about a third-party
	// package's global state, which is exactly the kind of thing main owns and
	// internal/ must not touch.
	redis.SetLogger(redisLogger{})

	svc := service.New(
		db.NewProfileStore(configDir, db.NewOSKeychain()),
		guard.NewJSONLAuditLog(configDir),
	)
	shell := app.New(svc, processStart, version)

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

// redisLogger is where go-redis's own log output goes: one line on stderr,
// attributed to go-db rather than to a package the human has never heard of,
// and without the library's file-and-line stamp. It carries the same
// information to the same place; what it drops is the noise.
type redisLogger struct{}

func (redisLogger) Printf(_ context.Context, format string, v ...any) {
	fmt.Fprintf(os.Stderr, "go-db: "+strings.TrimSuffix(format, "\n")+"\n", v...)
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
