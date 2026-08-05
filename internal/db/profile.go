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
	// connection; otherwise Host is resolved and dialled on the bastion rather
	// than here, so it may be a name that means nothing on this machine.
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
//
// KeyFile is the path to a private key to authenticate with. It is optional,
// and the empty case is the usual one: the running SSH agent is asked first,
// then the default keys in ~/.ssh. Only the path is stored — the key stays
// where ssh keeps it, and a passphrase-protected key is expected to be held by
// the agent, since v1 asks for no SSH passwords.
type SSHTunnel struct {
	Host    string `toml:"host"`
	Port    int    `toml:"port"`
	User    string `toml:"user"`
	KeyFile string `toml:"key_file,omitempty"`
}

// Address returns the host:port of the bastion, defaulting an unset port to
// SSH's 22. Like Profile.Address it is how the tunnel names its host wherever
// one is shown or dialled, so a message and a dial agree.
func (t SSHTunnel) Address() string {
	port := t.Port
	if port == 0 {
		port = defaultSSHPort
	}
	return net.JoinHostPort(t.Host, strconv.Itoa(port))
}
