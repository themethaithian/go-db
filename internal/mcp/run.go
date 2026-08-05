package mcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// ErrProfileRequired reports that Run was called without a Profile name. The
// mcp subcommand's Profile argument is required and positional; there is no
// default to fall back to, because falling back to the app's active
// connection is exactly the behaviour issue #10 rules out.
var ErrProfileRequired = errors.New("mcp: profile name is required")

// Run starts a stdio MCP server pinned to profileName and blocks until the
// client disconnects. It is what the `go-db mcp <profile-name>` subcommand
// calls into.
func Run(profileName string) error {
	if profileName == "" {
		return ErrProfileRequired
	}

	configDir, err := defaultConfigDir()
	if err != nil {
		return err
	}

	server := NewServer(configDir, profileName)
	return server.Run(context.Background(), &sdkmcp.StdioTransport{})
}

// defaultConfigDir duplicates db.DefaultProfileDir's path logic rather than
// importing internal/db: internal/mcp is the MCP adapter and reaches the app
// over HTTP only, never through a domain package directly. The two must stay
// in step because this is where the proxy looks for api.json.
func defaultConfigDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("mcp: locating user config dir: %w", err)
	}
	return filepath.Join(dir, "go-db"), nil
}
