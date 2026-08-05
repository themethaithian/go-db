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
	cfg.ParseTime = true
	cfg.Timeout = dialTimeout

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
