package db

import (
	"context"
	"errors"
)

// ErrAuthFailed reports that the server was reached but rejected the Profile's
// credentials. It is distinguishable from ErrUnreachable on purpose: the two
// call for different fixes, so the UI must be able to say which happened.
var ErrAuthFailed = errors.New("db: authentication failed")

// ErrUnreachable reports that the server was never reached — the host does not
// resolve, nothing is listening, the connection was refused, or the dial timed
// out.
var ErrUnreachable = errors.New("db: server unreachable")

// Driver is the port through which a Profile becomes an open connection.
// The MySQL adapter in this package implements it; tests substitute a fake.
//
// Implementations must classify their failures before returning them: a
// rejected password wraps ErrAuthFailed, a server that was never reached wraps
// ErrUnreachable, and anything else is returned wrapped but unclassified. That
// keeps error-number and network-error knowledge inside the adapter, where it
// belongs, rather than spread across callers.
type Driver interface {
	// Open dials the database described by profile, authenticating as
	// profile.User with password, and returns a live connection. A returned
	// Conn has already been verified to answer; a returned error means no
	// resources were left open.
	Open(ctx context.Context, profile Profile, password string) (Conn, error)
}

// Conn is one open connection to a database, held by the Connection Registry.
//
// The interface is deliberately minimal: query and execute arrive with the
// query pipeline, and the Approval Gate is the only thing that will hand them
// out. Nothing here exposes the underlying pool.
type Conn interface {
	// Ping verifies the connection is still usable.
	Ping(ctx context.Context) error
	// Close releases the connection. It is safe to call once; the Registry
	// owns the lifecycle and calls it exactly once per Conn.
	Close() error
}
