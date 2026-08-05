package db

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
)

// ErrNotConnected reports that the Connection Registry holds no open
// connection for a Profile.
var ErrNotConnected = errors.New("db: profile is not connected")

// Registry is the Connection Registry: the set of currently open connections,
// keyed by Profile name. Several are open at once by design — the editor works
// on one Profile while the MCP server uses another.
//
// The Registry is the only thing that turns a Profile name into a live
// connection. It resolves the Profile and its keychain secret itself, so a
// password never travels through a caller: adapters name a Profile, and get
// back a connection or a classified error.
//
// It is safe for concurrent use. Dialling happens outside the lock, so a slow
// connect to one Profile never blocks another.
//
// It also owns the tunnels: a Profile with an SSH tunnel gets one opened before
// its connection and closed after it, and nothing outside this file can hold
// one. A tunnel that outlived its connection would be an SSH session nobody is
// using and nobody can see.
type Registry struct {
	driver   Driver
	tunnels  TunnelDialer
	profiles *ProfileStore

	mu    sync.Mutex
	conns map[string]Conn
}

// NewRegistry returns an empty Registry that opens connections for the Profiles
// in profiles through driver, tunnelling the ones that ask for it over SSH.
func NewRegistry(driver Driver, profiles *ProfileStore) *Registry {
	return NewRegistryWithTunnels(driver, nil, profiles)
}

// NewRegistryWithTunnels is NewRegistry with the tunnel dialler chosen: the
// seam a test uses to substitute a fake bastion, and the way an integration
// test points the real one at a known_hosts file of its own. A nil tunnels
// means the real SSH dialler verifying against ~/.ssh/known_hosts, which is
// what the shipping binary gets.
func NewRegistryWithTunnels(driver Driver, tunnels TunnelDialer, profiles *ProfileStore) *Registry {
	if tunnels == nil {
		tunnels = NewSSHTunnels("")
	}
	return &Registry{driver: driver, tunnels: tunnels, profiles: profiles, conns: make(map[string]Conn)}
}

// Connect opens a connection for the named Profile and holds it.
//
// It is idempotent: connecting a Profile that is already connected keeps the
// existing connection and reports success, so two adapters racing to connect
// the same Profile end up sharing one. To pick up rotated credentials or an
// edited host, Disconnect first.
//
// A failure leaves the Registry unchanged and returns a classified error:
// ErrProfileNotFound, ErrAuthFailed, or ErrUnreachable where they apply.
func (r *Registry) Connect(ctx context.Context, profileName string) error {
	r.mu.Lock()
	_, connected := r.conns[profileName]
	r.mu.Unlock()
	if connected {
		return nil
	}

	profile, password, err := r.profiles.Credentials(profileName)
	if err != nil {
		return err
	}

	conn, err := r.open(ctx, profile, password)
	if err != nil {
		return err
	}

	r.mu.Lock()
	_, raced := r.conns[profileName]
	if !raced {
		r.conns[profileName] = conn
	}
	r.mu.Unlock()

	if raced {
		// Another caller connected the same Profile while we were dialling;
		// theirs is already visible, so ours is the one to drop.
		conn.Close()
	}
	return nil
}

// Conn returns the open connection held for the named Profile, or
// ErrNotConnected.
func (r *Registry) Conn(profileName string) (Conn, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	conn, ok := r.conns[profileName]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrNotConnected, profileName)
	}
	return conn, nil
}

// Connected returns the names of the Profiles currently connected, ordered by
// name.
func (r *Registry) Connected() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	names := make([]string, 0, len(r.conns))
	for name := range r.conns {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Disconnect closes the connection held for the named Profile and forgets it.
// It reports ErrNotConnected if there was none; closing an already-closed
// Profile is a caller mistake worth surfacing, not a silent no-op.
func (r *Registry) Disconnect(profileName string) error {
	r.mu.Lock()
	conn, ok := r.conns[profileName]
	delete(r.conns, profileName)
	r.mu.Unlock()

	if !ok {
		return fmt.Errorf("%w: %q", ErrNotConnected, profileName)
	}
	return conn.Close()
}

// Close closes every open connection and empties the Registry. It reports the
// first close failure, having attempted all of them.
func (r *Registry) Close() error {
	r.mu.Lock()
	conns := r.conns
	r.conns = make(map[string]Conn)
	r.mu.Unlock()

	var firstErr error
	for _, conn := range conns {
		if err := conn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Test opens a throwaway connection for the named Profile, verifies it
// answers, and closes it again. The Registry is left unchanged — a connection
// test never becomes an open connection.
//
// It returns nil when the database answered, and otherwise the same classified
// errors as Connect.
func (r *Registry) Test(ctx context.Context, profileName string) error {
	profile, password, err := r.profiles.Credentials(profileName)
	if err != nil {
		return err
	}

	conn, err := r.open(ctx, profile, password)
	if err != nil {
		return err
	}
	defer conn.Close()

	return conn.Ping(ctx)
}

// open dials one connection for profile, through a tunnel when the Profile asks
// for one. The returned Conn owns whatever it needs to be closed with: closing
// it closes the tunnel too, so no caller has to remember there was one.
//
// A Profile with a tunnel fails as one thing. If the bastion refuses us the
// database is never dialled, and if the database refuses us the tunnel is torn
// down before the error is returned — an SSH session left behind by a failed
// connect is a leak no later call could find.
func (r *Registry) open(ctx context.Context, profile Profile, password string) (Conn, error) {
	if profile.SSH == nil {
		return r.driver.Open(ctx, profile, password, nil)
	}

	tunnel, err := r.tunnels.Open(ctx, *profile.SSH)
	if err != nil {
		return nil, err
	}

	conn, err := r.driver.Open(ctx, profile, password, tunnel.Dial)
	if err != nil {
		tunnel.Close() //nolint:errcheck // the connect already failed; the caller's error is the one worth reporting
		return nil, err
	}
	return tunnelledConn{Conn: conn, tunnel: tunnel}, nil
}
