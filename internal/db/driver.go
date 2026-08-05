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

// ErrWriteAttempt reports that the database refused a statement because it
// tried to write inside the read-only transaction reads execute in.
//
// It is the Approval Gate's second layer firing: the statement was classified a
// read, and the database disagreed. Callers reroute it to the gate rather than
// showing it as a failure — the query was not wrong, it was misjudged.
var ErrWriteAttempt = errors.New("db: the statement tried to write inside a read-only transaction")

// MaxRows is the most rows a ReadQuery returns. A desktop client renders what a
// human reads, and the performance budget is a feature: an unbounded SELECT
// would put the whole table in memory to draw a screenful. A result cut short
// says so, so the number on screen is never mistaken for the whole answer.
const MaxRows = 1000

// ResultSet is one read's answer, rendered for display.
//
// Values are the server's own text form — the exact digits of a DECIMAL, the
// date as MySQL wrote it — so nothing is reshaped by a round trip through Go's
// types on the way to a table cell. A nil value is SQL NULL, which no string
// can stand in for: an empty cell and an absent one mean different things.
type ResultSet struct {
	Columns []string    `json:"columns"`
	Rows    [][]*string `json:"rows"`
	// Truncated reports that the result was cut off at MaxRows and there were
	// more rows to come.
	Truncated bool `json:"truncated"`
}

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
// The interface is deliberately minimal, and asymmetric on purpose: it can read
// and it cannot write. There is no Exec here and there will not be one until
// the Approval Gate has something approved to hand it, so a mutation that
// escapes the classifier has nowhere to go. Nothing here exposes the underlying
// pool.
type Conn interface {
	// Ping verifies the connection is still usable.
	Ping(ctx context.Context) error

	// ReadQuery runs one statement inside a read-only transaction and returns
	// its rows, at most MaxRows of them.
	//
	// The transaction is the second layer of the Approval Gate, and the reason
	// this method is safe to call with a statement the classifier only
	// believes is a read: the database checks the claim too. A statement the
	// database refuses for writing comes back wrapping ErrWriteAttempt, the
	// transaction is rolled back, and the connection is left usable.
	//
	// The layer has one known gap, and it is not the obvious one. DDL causes
	// an implicit commit in MySQL, so CREATE, DROP, ALTER and TRUNCATE end the
	// read-only transaction before they run and are never refused by it. DML
	// and locking reads are covered; DDL rests on the classifier alone. This
	// is why the classifier's allowlist is closed rather than a blocklist, and
	// why nothing may be added to it without proof.
	//
	// Implementations must not interpolate anything into sql; it is executed
	// exactly as given.
	ReadQuery(ctx context.Context, sql string) (ResultSet, error)

	// Close releases the connection. It is safe to call once; the Registry
	// owns the lifecycle and calls it exactly once per Conn.
	Close() error
}
