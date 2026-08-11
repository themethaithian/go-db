package db

import (
	"net"
	"strconv"
)

// defaultRedisPort and defaultMongoDBPort are those Engines' usual ports,
// following defaultMySQLPort's pattern (declared in mysql.go, the only
// adapter that exists yet). They live here rather than beside an adapter of
// their own because ADR-0006's Redis and MongoDB adapters do not exist yet;
// Profile.Address needs a default today regardless.
const (
	defaultRedisPort   = 6379
	defaultMongoDBPort = 27017
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

	// Engine is the kind of database this Profile reaches. It is written
	// omitempty so a Profile that never sets it round-trips as no key at all
	// rather than an explicit empty string; ProfileStore is what turns that
	// absence into EngineMySQL, both on load and before it is written back.
	Engine Engine `toml:"engine,omitempty"`

	// SSH is the optional tunnel used to reach Host. Nil means a direct
	// connection; otherwise Host is resolved and dialled on the bastion rather
	// than here, so it may be a name that means nothing on this machine.
	SSH *SSHTunnel `toml:"ssh,omitempty"`
}

// Address returns the host:port this Profile is reached at, defaulting an
// unset port to its Engine's usual one — 3306 for MySQL (and for an unset
// Engine, since ProfileStore normalizes that to MySQL before anything else
// sees it), 6379 for Redis, 27017 for MongoDB. It is how the Profile names
// its server anywhere one is shown or dialled, so both agree.
func (p Profile) Address() string {
	port := p.Port
	if port == 0 {
		port = defaultPort(p.Engine)
	}
	return net.JoinHostPort(p.Host, strconv.Itoa(port))
}

// defaultPort is the usual port for engine, used when a Profile does not
// name one of its own.
func defaultPort(engine Engine) int {
	switch engine {
	case EngineRedis:
		return defaultRedisPort
	case EngineMongoDB:
		return defaultMongoDBPort
	default:
		return defaultMySQLPort
	}
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
