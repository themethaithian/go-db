// Package mcp provides a stdio MCP (Model Context Protocol) server that acts as a pure proxy over the app's localhost API.
//
// The MCP server is a thin adapter: it receives MCP protocol messages over stdio, translates them to the app's
// localhost API, and returns results. It owns no connections, no profiles, and no logic—all those belong to the app.
//
// Authentication is token-file based: a per-launch random token is written to a 0600 file, used by the proxy to authenticate to localhost.
// The app binds 127.0.0.1 only, defeating other-user processes and browser CSRF/DNS-rebinding attacks.
//
// MCP server mode requires the app to already be running. Mutations block (with ~2-min timeout auto-reject)
// while approval is pending in the app's Approval Console.
//
// This package exports a narrow API; the actual protocol handling and API translation logic is hidden.
package mcp
