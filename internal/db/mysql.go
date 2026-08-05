package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"syscall"
	"time"

	"github.com/go-sql-driver/mysql"
)

// MySQL server error numbers this adapter reacts to. Everything else is passed
// through with the server's own message, which is already readable.
const (
	erAccessDenied           = 1045 // wrong user or password
	erAccessDeniedNoPassword = 1698 // rejected by the authentication plugin

	// erCantExecuteInReadOnlyTransaction is what MySQL answers a statement
	// that would write inside a READ ONLY transaction. It is the Approval
	// Gate's backstop speaking, and the one error number this adapter turns
	// into a domain outcome rather than a message.
	erCantExecuteInReadOnlyTransaction = 1792
)

const (
	defaultMySQLPort = 3306

	// dialTimeout bounds reaching the server, so an unreachable host fails
	// fast instead of hanging the UI. Query duration is bounded by the
	// caller's context, not by socket read/write timeouts, so a long-running
	// SELECT is not cut short.
	dialTimeout = 5 * time.Second

	// A desktop client holds few connections per Profile; the pool exists so
	// the editor and the Approval Gate do not serialise on one socket.
	maxOpenConns    = 4
	maxIdleConns    = 2
	connMaxIdleTime = 5 * time.Minute
	connMaxLifetime = 30 * time.Minute
)

// NewMySQLDriver returns the Driver that reaches MySQL over database/sql.
//
// It is the only place that knows MySQL error numbers and network error
// shapes: it translates them into ErrAuthFailed and ErrUnreachable so callers
// can tell a wrong password from an unreachable host without importing a
// driver package.
func NewMySQLDriver() Driver { return mysqlDriver{} }

type mysqlDriver struct{}

// Open dials profile's server and verifies the connection answers before
// returning it. Nothing is left open on failure.
func (mysqlDriver) Open(ctx context.Context, profile Profile, password string) (Conn, error) {
	if profile.SSH != nil {
		return nil, fmt.Errorf("db: profile %q connects through an SSH tunnel, which is not supported yet", profile.Name)
	}

	cfg := mysql.NewConfig()
	cfg.Net = "tcp"
	cfg.Addr = profile.Address()
	cfg.User = profile.User
	cfg.Passwd = password
	cfg.DBName = profile.Database
	cfg.Timeout = dialTimeout

	// Dates and times are left as the server wrote them. A read's job here is
	// to be displayed, and MySQL's own text form is the display form: parsing
	// a DATE into a time.Time only to choose a layout for it again would make
	// this adapter guess at a format the server already picked.
	cfg.ParseTime = false

	// A connector keeps the password in memory as a field; it is never
	// formatted into a DSN string that could reach a log or an error.
	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return nil, fmt.Errorf("db: connection settings for profile %q: %w", profile.Name, err)
	}

	pool := sql.OpenDB(connector)
	pool.SetMaxOpenConns(maxOpenConns)
	pool.SetMaxIdleConns(maxIdleConns)
	pool.SetConnMaxIdleTime(connMaxIdleTime)
	pool.SetConnMaxLifetime(connMaxLifetime)

	if err := pool.PingContext(ctx); err != nil {
		pool.Close()
		return nil, classify(err)
	}
	return &mysqlConn{pool: pool}, nil
}

// mysqlConn is one Profile's connection pool. The pool is private: callers get
// only what the Conn interface exposes.
type mysqlConn struct {
	pool *sql.DB
}

func (c *mysqlConn) Ping(ctx context.Context) error {
	if err := c.pool.PingContext(ctx); err != nil {
		return classify(err)
	}
	return nil
}

// ReadQuery runs sql inside a READ ONLY transaction and returns its rows.
//
// The transaction is the point of the method, not an implementation detail: it
// is what lets the app run a statement it only believes is a read. MySQL
// refuses any write inside one with error 1792, which becomes ErrWriteAttempt
// so the Approval Gate can take the query back.
func (c *mysqlConn) ReadQuery(ctx context.Context, sql string) (ResultSet, error) {
	// A read-only transaction is a property of one session, so the statement
	// and its transaction must share a connection; BeginTx pins one for us.
	tx, err := c.pool.BeginTx(ctx, &txReadOnly)
	if err != nil {
		return ResultSet{}, readError(err)
	}
	// Nothing in here commits: a transaction that only read has nothing to
	// commit, and rolling back is the one ending that is correct whether the
	// statement succeeded, failed, or was refused for writing.
	defer tx.Rollback() //nolint:errcheck // the transaction is read-only; a failed rollback changes nothing

	rows, err := tx.QueryContext(ctx, sql)
	if err != nil {
		return ResultSet{}, readError(err)
	}
	defer rows.Close()

	result, err := collect(rows)
	if err != nil {
		return ResultSet{}, readError(err)
	}
	return result, nil
}

// Exec runs an approved mutation directly on the pool — no transaction, so
// nothing holds locks while anyone deliberates, and DDL is not preceded by an
// implicit commit of something else.
//
// It reports the rows the server says it changed. A driver that cannot say is
// treated as nothing changed rather than as a failure: the statement ran, and
// claiming otherwise would be worse than an imprecise count.
func (c *mysqlConn) Exec(ctx context.Context, sql string) (int64, error) {
	result, err := c.pool.ExecContext(ctx, sql)
	if err != nil {
		return 0, classify(err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return 0, nil
	}
	return affected, nil
}

// txReadOnly is the transaction reads run in. Default isolation is deliberate:
// the read-only promise is what matters here, not a stricter snapshot.
var txReadOnly = sql.TxOptions{ReadOnly: true}

// collect drains rows into a ResultSet, stopping at MaxRows. It peeks one row
// past the cap so Truncated means "there was more", not "you asked for exactly
// the cap".
func collect(rows *sql.Rows) (ResultSet, error) {
	columns, err := rows.Columns()
	if err != nil {
		return ResultSet{}, err
	}

	// RawBytes hands over the driver's own buffer, valid only until the next
	// Next; every value is copied into a string before that happens.
	raw := make([]sql.RawBytes, len(columns))
	scan := make([]any, len(columns))
	for i := range raw {
		scan[i] = &raw[i]
	}

	result := ResultSet{Columns: columns, Rows: [][]*string{}}
	for rows.Next() {
		if len(result.Rows) == MaxRows {
			result.Truncated = true
			break
		}
		if err := rows.Scan(scan...); err != nil {
			return ResultSet{}, err
		}

		row := make([]*string, len(columns))
		for i, value := range raw {
			if value == nil {
				continue // SQL NULL, which no string stands in for
			}
			text := string(value)
			row[i] = &text
		}
		result.Rows = append(result.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return ResultSet{}, err
	}
	return result, nil
}

// readError classifies a failure from running a read. Only one distinction
// matters to the caller: the database refusing a write is a reroute to the
// Approval Gate, and everything else is a failure to report.
func readError(err error) error {
	var serverErr *mysql.MySQLError
	if errors.As(err, &serverErr) && serverErr.Number == erCantExecuteInReadOnlyTransaction {
		return fmt.Errorf("%w: %s", ErrWriteAttempt, serverErr.Message)
	}
	return classify(err)
}

func (c *mysqlConn) Close() error {
	if err := c.pool.Close(); err != nil {
		return fmt.Errorf("db: closing connection: %w", err)
	}
	return nil
}

// classify turns a driver or network error into one the caller can act on:
// ErrAuthFailed when the server rejected the credentials, ErrUnreachable when
// the server was never reached. Anything else keeps its own message — the
// server's wording is usually the most useful thing we could say.
func classify(err error) error {
	var serverErr *mysql.MySQLError
	if errors.As(err, &serverErr) {
		switch serverErr.Number {
		case erAccessDenied, erAccessDeniedNoPassword:
			return fmt.Errorf("%w: %s", ErrAuthFailed, serverErr.Message)
		}
		return fmt.Errorf("db: %s", serverErr.Message)
	}
	if unreachable(err) {
		return fmt.Errorf("%w: %v", ErrUnreachable, err)
	}
	return fmt.Errorf("db: connecting: %w", err)
}

// unreachable reports whether err means the server was never reached: DNS
// failure, refused or timed-out dial, or an unroutable address.
func unreachable(err error) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, mysql.ErrInvalidConn) {
		return true
	}
	for _, errno := range []syscall.Errno{
		syscall.ECONNREFUSED, syscall.ECONNRESET, syscall.EHOSTUNREACH,
		syscall.ENETUNREACH, syscall.ETIMEDOUT, syscall.EHOSTDOWN,
	} {
		if errors.Is(err, errno) {
			return true
		}
	}
	return false
}
