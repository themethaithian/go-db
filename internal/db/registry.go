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

// ErrEngineUnsupported reports that a Profile named an Engine the Connection
// Registry has no Driver for. It is checked before anything is dialled — no
// tunnel opened, no credentials touched — and it leaves the Registry exactly
// as it was: this build simply does not carry the adapter this Profile's
// Engine needs. A Profile with a zero Engine fails the same way rather than
// being defaulted to MySQL here; ProfileStore normalizes that at the store
// boundary, and a second normalization in the Registry would let the rule
// drift between the two.
var ErrEngineUnsupported = errors.New("db: no driver for engine")

// Drivers maps an Engine to the Driver that dials it. It is the whole of how
// the Connection Registry picks an adapter: a Profile names an Engine, the
// map is consulted once, in open, and whatever Driver is behind that key
// handles everything from there. A build that only ships MySQL carries a
// one-entry map; nothing else about the Registry changes when a second
// Engine's Driver is added to it.
type Drivers map[Engine]Driver

// Registry is the Connection Registry: the set of currently open connections,
// keyed by Profile name and the schema each one is open on. Several are open at
// once by design — the editor works on one Profile while the MCP server uses
// another.
//
// A Profile is connected once, on the schema its own Database names. Asking for
// any other schema opens a *sibling* connection beside it: the same server, the
// same credentials, the same tunnel settings, with that schema as the DSN's
// dbname and so as the connection's own default. Nothing here runs USE. A USE
// on a shared session would change the schema under every statement already in
// flight on it, and the whole point of selecting a database is that unqualified
// SQL, editability detection and generated UPDATEs resolve against it honestly.
//
// The Registry is the only thing that turns a Profile name into a live
// connection. It resolves the Profile and its keychain secret itself, so a
// password never travels through a caller: adapters name a Profile, and get
// back a connection or a classified error.
//
// It is safe for concurrent use. Dialling happens outside the lock, so a slow
// connect to one Profile never blocks another, and two callers racing for the
// same sibling keep one connection between them rather than leaking the loser.
//
// It also owns the tunnels: a Profile with an SSH tunnel gets one opened before
// its connection and closed after it, and nothing outside this file can hold
// one. A tunnel that outlived its connection would be an SSH session nobody is
// using and nobody can see. A sibling gets a tunnel of its own — correctness
// before economy, since a tunnel shared between connections would outlive
// whichever of them closed first and there is nowhere here to notice that.
//
// It picks a Driver by Engine, and nowhere else does: per ADR-0006 the
// Registry itself is Engine-agnostic, and the Profile's Engine is the only
// thing that decides which adapter dials it. That choice is made once, in
// open, by looking the Engine up in the Drivers map it was built with; a
// Profile whose Engine has no entry fails with ErrEngineUnsupported before
// anything is dialled, rather than falling back to any particular Engine.
type Registry struct {
	drivers  Drivers
	tunnels  TunnelDialer
	profiles *ProfileStore

	mu    sync.Mutex
	conns map[connKey]Conn
	// home is each connected Profile's own default schema, learned when it was
	// connected. A request for exactly that schema is a request for the
	// connection already held, not for a second one beside it on the same
	// place — and it is read from here rather than from the ProfileStore so an
	// edit to the Profile cannot silently re-point an open connection.
	home map[string]string
}

// connKey identifies one open connection: the Profile it belongs to, and the
// schema it is open on. A blank database is the Profile's own connection —
// the key every caller used before a database could be selected, and the one
// the localhost API and the MCP proxy still use.
type connKey struct {
	profile  string
	database string
}

// NewRegistry returns an empty Registry that opens connections for the
// Profiles in profiles, dialling each through whichever of drivers answers
// for its Engine, and tunnelling the ones that ask for it over SSH.
func NewRegistry(drivers Drivers, profiles *ProfileStore) *Registry {
	return NewRegistryWithTunnels(drivers, nil, profiles)
}

// NewRegistryWithTunnels is NewRegistry with the tunnel dialler chosen: the
// seam a test uses to substitute a fake bastion, and the way an integration
// test points the real one at a known_hosts file of its own. A nil tunnels
// means the real SSH dialler verifying against ~/.ssh/known_hosts, which is
// what the shipping binary gets.
func NewRegistryWithTunnels(drivers Drivers, tunnels TunnelDialer, profiles *ProfileStore) *Registry {
	if tunnels == nil {
		tunnels = NewSSHTunnels("")
	}
	return &Registry{
		drivers:  drivers,
		tunnels:  tunnels,
		profiles: profiles,
		conns:    make(map[connKey]Conn),
		home:     make(map[string]string),
	}
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
	_, connected := r.conns[connKey{profile: profileName}]
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
	_, raced := r.conns[connKey{profile: profileName}]
	if !raced {
		r.conns[connKey{profile: profileName}] = conn
		r.home[profileName] = profile.Database
	}
	r.mu.Unlock()

	if raced {
		// Another caller connected the same Profile while we were dialling;
		// theirs is already visible, so ours is the one to drop.
		conn.Close()
	}
	return nil
}

// Conn returns an open connection for the named Profile on the given database,
// or ErrNotConnected when the Profile is not connected at all.
//
// A blank database is the Profile's own connection: exactly the connection
// Connect opened, and what every caller that does not select a schema gets. So
// is the Profile's own Database by name — a human choosing the schema their
// Profile already names is choosing the connection they already have, and
// opening a second one to the same place would spend a connection to change
// nothing.
//
// Any other database is a sibling connection, opened here the first time it is
// asked for and held until the Profile disconnects. Opening one can fail the
// way any connect can — an unknown schema, a server that has gone away — and
// that failure is returned as the driver classified it, not as ErrNotConnected:
// the Profile is connected, this schema is simply not reachable.
func (r *Registry) Conn(ctx context.Context, profileName, database string) (Conn, error) {
	r.mu.Lock()
	_, connected := r.conns[connKey{profile: profileName}]
	if connected && database == r.home[profileName] {
		database = ""
	}
	key := connKey{profile: profileName, database: database}
	conn, held := r.conns[key]
	r.mu.Unlock()

	if !connected {
		return nil, fmt.Errorf("%w: %q", ErrNotConnected, profileName)
	}
	if held {
		return conn, nil
	}
	return r.openSibling(ctx, key)
}

// Connected returns the names of the Profiles currently connected, ordered by
// name. Sibling connections are not connections to a Profile of their own and
// do not appear: what is connected is a Profile, and that is what the UI shows.
func (r *Registry) Connected() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	names := make([]string, 0, len(r.conns))
	for key := range r.conns {
		if key.database == "" {
			names = append(names, key.profile)
		}
	}
	sort.Strings(names)
	return names
}

// Disconnect closes the connection held for the named Profile, and every
// sibling connection opened beside it, and forgets them all. A sibling belongs
// to the Profile that it was opened under; leaving one behind would keep a
// database session — and possibly an SSH tunnel — alive for a Profile the human
// has disconnected.
//
// It reports ErrNotConnected if there was none; closing an already-closed
// Profile is a caller mistake worth surfacing, not a silent no-op. It reports
// the first close failure, having attempted all of them.
func (r *Registry) Disconnect(profileName string) error {
	r.mu.Lock()
	conn, ok := r.conns[connKey{profile: profileName}]
	delete(r.conns, connKey{profile: profileName})
	delete(r.home, profileName)

	siblings := make([]Conn, 0, len(r.conns))
	for key, sibling := range r.conns {
		if key.profile == profileName {
			siblings = append(siblings, sibling)
			delete(r.conns, key)
		}
	}
	r.mu.Unlock()

	if !ok {
		return fmt.Errorf("%w: %q", ErrNotConnected, profileName)
	}

	err := conn.Close()
	for _, sibling := range siblings {
		if closeErr := sibling.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}
	return err
}

// Close closes every open connection — Profiles and their siblings alike — and
// empties the Registry. It reports the first close failure, having attempted
// all of them.
func (r *Registry) Close() error {
	r.mu.Lock()
	conns := r.conns
	r.conns = make(map[connKey]Conn)
	r.home = make(map[string]string)
	r.mu.Unlock()

	var firstErr error
	for _, conn := range conns {
		if err := conn.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// openSibling dials one more connection for a Profile, on key's schema, and
// holds it. It is only ever called for a Profile that was connected a moment
// ago, and for a schema that is not the one it connected on.
//
// Dialling happens outside the lock, like every other connect here, so two
// callers wanting the same schema at the same time can both dial. One of them
// wins the map and the other closes what it opened: a losing dial left open
// would be a connection nothing holds and nothing can close. The Profile going
// away mid-dial ends the same way — the sibling is closed rather than filed
// under a Profile that Disconnect has already swept.
func (r *Registry) openSibling(ctx context.Context, key connKey) (Conn, error) {
	profile, password, err := r.profiles.Credentials(key.profile)
	if err != nil {
		return nil, err
	}
	// The whole of a sibling: the Profile as saved, on another schema.
	profile.Database = key.database

	conn, err := r.open(ctx, profile, password)
	if err != nil {
		return nil, err
	}

	r.mu.Lock()
	_, stillConnected := r.conns[connKey{profile: key.profile}]
	existing, raced := r.conns[key]
	if stillConnected && !raced {
		r.conns[key] = conn
	}
	r.mu.Unlock()

	switch {
	case !stillConnected:
		conn.Close() //nolint:errcheck // the Profile is gone; ErrNotConnected is the answer worth giving
		return nil, fmt.Errorf("%w: %q", ErrNotConnected, key.profile)
	case raced:
		conn.Close() //nolint:errcheck // theirs is the one everyone can see; ours is the one to drop
		return existing, nil
	}
	return conn, nil
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
// This is the one place the Registry chooses a Driver: profile.Engine is
// looked up in drivers, and a miss fails with ErrEngineUnsupported before a
// tunnel is opened or anything else about profile is touched. Profiles
// reaching here always carry a concrete Engine — ProfileStore normalizes a
// zero one to EngineMySQL before any caller sees it — but a zero Engine is
// not treated as MySQL here either; it is simply another key the map does not
// have, so the normalization rule stays owned by one package.
//
// A Profile with a tunnel fails as one thing. If the bastion refuses us the
// database is never dialled, and if the database refuses us the tunnel is torn
// down before the error is returned — an SSH session left behind by a failed
// connect is a leak no later call could find.
func (r *Registry) open(ctx context.Context, profile Profile, password string) (Conn, error) {
	driver, ok := r.drivers[profile.Engine]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrEngineUnsupported, profile.Engine)
	}

	if profile.SSH == nil {
		return driver.Open(ctx, profile, password, nil)
	}

	tunnel, err := r.tunnels.Open(ctx, *profile.SSH)
	if err != nil {
		return nil, err
	}

	conn, err := driver.Open(ctx, profile, password, tunnel.Dial)
	if err != nil {
		tunnel.Close() //nolint:errcheck // the connect already failed; the caller's error is the one worth reporting
		return nil, err
	}
	return tunnelledConn{Conn: conn, tunnel: tunnel}, nil
}
