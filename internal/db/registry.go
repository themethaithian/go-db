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
type Registry struct {
	driver   Driver
	profiles *ProfileStore

	mu    sync.Mutex
	conns map[string]Conn
}

// NewRegistry returns an empty Registry that opens connections for the
// Profiles in profiles through driver.
func NewRegistry(driver Driver, profiles *ProfileStore) *Registry {
	return &Registry{driver: driver, profiles: profiles, conns: make(map[string]Conn)}
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

	conn, err := r.driver.Open(ctx, profile, password)
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

	conn, err := r.driver.Open(ctx, profile, password)
	if err != nil {
		return err
	}
	defer conn.Close()

	return conn.Ping(ctx)
}
