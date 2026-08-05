// Package db manages database Profiles, the Connection Registry, MySQL driver integration, and SSH tunnel ports.
//
// A Profile is a named, saved description of how to reach one database: host, credentials reference, and optional SSH tunnel.
// Profiles are the only way any Origin names a database.
//
// The Connection Registry holds currently open database connections, keyed by Profile.
// Multiple connections can be open at once (e.g. the editor on one Profile while the MCP server uses another).
//
// This package holds the Keychain port (for password retrieval from OS keychain, never plaintext on disk),
// the Driver port MySQL is reached through, and the Tunnel port a Profile's bastion is reached through.
// A tunnelled Profile is dialled from its bastion rather than from this machine, and its tunnel lives and
// dies with the connection the Registry holds.
// It exports a narrow API; internal machinery is hidden from adapters.
package db
