// Package mcp provides a stdio MCP (Model Context Protocol) server that acts as a pure proxy over the app's localhost API.
//
// The MCP server is a thin adapter: it receives MCP protocol messages over stdio, translates them to the app's
// localhost API, and returns results. It owns no connections, no profiles, and no logic—all those belong to the app.
// It is the one package in this repo that imports the MCP SDK, and it imports nothing from internal/db,
// internal/guard, or internal/service: it never reaches around the localhost API to a domain package directly.
//
// A server is pinned to exactly one Profile, named explicitly by the `go-db mcp <profile-name>` subcommand
// argument — never the app's active connection, which the editor may change independently. It exposes three
// tools, none of which accept a profile argument: list_tables, describe_table, and run_query. A mutating
// statement submitted to run_query is not executed; it comes back as the gate's own message explaining that it
// needs approval in the app (issue #11 is what lets it wait there instead).
//
// Authentication is token-file based: a per-launch random token is written to a 0600 file, used by the proxy to authenticate to localhost.
// The app binds 127.0.0.1 only, defeating other-user processes and browser CSRF/DNS-rebinding attacks.
//
// MCP server mode requires the app to already be running: every tool call re-reads the token file fresh, and any
// way the app turns out to be unreachable—missing file, stale port, refused connection—collapses to the same
// one-sentence tool error rather than a hang or a stack trace.
//
// This package exports a narrow API; the actual protocol handling and API translation logic is hidden.
package mcp
