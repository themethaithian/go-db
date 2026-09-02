package db

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
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

	// TLS is the optional transport encryption to Host. Nil means plaintext —
	// which is what every Profile written before this field existed says, and
	// what it should keep saying, since a client that quietly started
	// negotiating TLS would fail against every server that does not offer it.
	//
	// It is a pointer rather than a bool so that "TLS, with nothing waived"
	// has a shape of its own: a struct with every field at its zero value is
	// still a Profile that asked to be encrypted. It sits alongside SSH rather
	// than inside it because the two are independent — a tunnel encrypts the
	// hop to the bastion, and this encrypts the conversation with the database
	// at the far end.
	TLS *TLSSettings `toml:"tls,omitempty"`

	// Group is a free-text label the connection manager UI clusters Profiles
	// by. It is entirely the human's own naming — ProfileStore does not
	// validate it beyond the caller's own trimming — and empty means
	// ungrouped, which is why it is written omitempty: a Profile nobody
	// grouped round-trips as no key at all, exactly like an unset Engine
	// before it is normalized.
	Group string `toml:"group,omitempty"`
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

// TLSSettings describes how a Profile's connection is encrypted. Its presence
// is the whole of "use TLS"; the fields in it only relax what that means.
type TLSSettings struct {
	// SkipVerify accepts whatever certificate the server presents, without
	// checking who signed it or whether it names the host being reached.
	//
	// The trade-off is worth stating plainly, because the field is easy to
	// tick and hard to untick: the connection is still encrypted, so nobody
	// passively reading the wire learns the password or the data — but the
	// peer is not authenticated, so anything that can get in the middle of the
	// connection can present a certificate of its own and be believed. It
	// exists because the servers people actually have are often signed by a
	// self-signed certificate or an internal CA this machine has never been
	// told about, and refusing those outright would leave the human with no
	// way to connect at all.
	SkipVerify bool `toml:"skip_verify,omitempty"`
}

// tlsConfig returns the TLS settings this Profile's connection is made under,
// or nil for a plaintext one. Every adapter asks this same method rather than
// assembling a config of its own, so the Profile field means one thing
// whichever Engine it has — and a fresh config is returned each call, because
// the client libraries keep the value they are given.
func (p Profile) tlsConfig() *tls.Config {
	if p.TLS == nil {
		return nil
	}
	return &tls.Config{
		// TLS 1.0 and 1.1 are withdrawn, and Go's own default floor is 1.2 for
		// a client already. It is written out rather than inherited so that
		// the floor is this package's decision and not a default that could
		// move.
		MinVersion: tls.VersionTLS12,

		// The name checked against the certificate is the Host the human
		// wrote, not the address that was dialled. Host carries no port, so it
		// is the server name as it stands, and an IP literal is a legitimate
		// one — crypto/tls matches it against the certificate's IP SANs.
		//
		// It is also the right name through an SSH tunnel, where the dial goes
		// to a forwarded local port: the tunnel's far end opens the connection
		// to this very host, so this is the server being spoken to, and a
		// certificate naming it is the certificate that should be accepted.
		ServerName: p.Host,

		//nolint:gosec // G402 is the point of the field: the human asked, and TLSSettings.SkipVerify says what it costs.
		InsecureSkipVerify: p.TLS.SkipVerify,
	}
}

// certificateRejected reports whether err is this client refusing the server's
// certificate, rather than anything about reaching the server or its answer.
//
// It is the counterpart to tlsConfig, and the adapters use it for the same
// reason they share that: the failure a TLS Profile can newly produce should
// read the same on every Engine. It is deliberately neither ErrUnreachable nor
// ErrAuthFailed — the server was reached, and it rejected nothing; this end
// did. Saying "unreachable" would hide the only two fixes there are, which are
// to install the CA the certificate was signed by or to waive verification.
func certificateRejected(err error) bool {
	var verification *tls.CertificateVerificationError
	if errors.As(err, &verification) {
		return true
	}
	// The three x509 failures a verification error carries, checked in their
	// own right too: not every path to them arrives wrapped in one.
	var authority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	return errors.As(err, &authority) || errors.As(err, &hostname) || errors.As(err, &invalid)
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
