package db

import (
	"net"
	"strconv"
)

// Profile is a saved, named description of how to reach one database.
// Profiles are the only way any Origin names a database: the MCP server pins
// one explicitly, the editor selects one.
//
// Name is the key: it identifies the Profile in the ProfileStore and keys the
// Profile's password in the Keychain. A Profile never carries the password
// itself — that lives only in the OS keychain.
type Profile struct {
	Name     string `toml:"name"`
	Host     string `toml:"host"`
	Port     int    `toml:"port"`
	User     string `toml:"user"`
	Database string `toml:"database,omitempty"`

	// SSH is the optional tunnel used to reach Host. Nil means a direct
	// connection. The tunnel is persisted but not yet dialled; the SSH tunnel
	// port is declared here and implemented in a later slice.
	SSH *SSHTunnel `toml:"ssh,omitempty"`
}

// Address returns the host:port this Profile is reached at, defaulting an
// unset port to MySQL's 3306. It is how the Profile names its server anywhere
// one is shown or dialled, so both agree.
func (p Profile) Address() string {
	port := p.Port
	if port == 0 {
		port = defaultMySQLPort
	}
	return net.JoinHostPort(p.Host, strconv.Itoa(port))
}

// SSHTunnel describes the jump host a Profile's connection is tunnelled
// through. Its credentials, like a Profile's, never touch disk.
type SSHTunnel struct {
	Host string `toml:"host"`
	Port int    `toml:"port"`
	User string `toml:"user"`
}
