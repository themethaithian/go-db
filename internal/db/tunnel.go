package db

import (
	"context"
	"errors"
	"fmt"
	"net"
)

// ErrBastionUnreachable reports that a Profile's SSH bastion was never reached:
// the jump host does not resolve, nothing is listening, or the dial timed out.
//
// It is an ErrUnreachable underneath — from the caller's side the database did
// not answer — with the hop named, so a Profile with a tunnel can say which of
// its two hosts is down instead of blaming the database for a bastion that is
// off.
var ErrBastionUnreachable = fmt.Errorf("%w: the bastion was never reached", ErrUnreachable)

// ErrSSHAuthFailed reports that the bastion answered and refused our SSH
// credentials. It is deliberately not ErrAuthFailed: the database's password is
// not the thing to change here, the SSH key is.
var ErrSSHAuthFailed = errors.New("db: the bastion refused the SSH credentials")

// ErrSSHHostKey reports that the bastion's host key is not one we trust — it is
// absent from known_hosts, it has changed, or it has been revoked.
//
// It is a refusal to connect, not a failure to: we reached the bastion and
// declined to talk to it, because a host key we cannot verify is
// indistinguishable from a machine in the middle. The fix is a human one —
// connect to the bastion once with ssh, or find out why its key changed — so it
// carries its own message rather than hiding inside "unreachable".
var ErrSSHHostKey = errors.New("db: the bastion's host key is not trusted")

// Tunnel is one open SSH session to a bastion, carrying a Profile's database
// traffic.
//
// It is deliberately not "a local port": nothing is published on this machine
// for another process to find. The only way through is Dial, which the driver
// holds as its dial path and nothing else can reach.
type Tunnel interface {
	// Dial opens a connection to address as seen from the bastion. The address
	// is resolved there, not here: a name that means nothing on this machine is
	// exactly what a tunnel is for.
	//
	// A database the bastion cannot reach comes back wrapping ErrUnreachable,
	// naming the address it tried.
	Dial(ctx context.Context, address string) (net.Conn, error)

	// Close ends the SSH session and everything still carried on it. The
	// Connection Registry owns the lifecycle and calls it exactly once, after
	// closing the connection the tunnel carried.
	Close() error
}

// TunnelDialer is the port through which a Profile's SSHTunnel becomes an open
// Tunnel. The x/crypto/ssh adapter in this package implements it; tests
// substitute a fake.
//
// Implementations must classify their failures before returning them, as a
// Driver does: a bastion that was never reached wraps ErrBastionUnreachable,
// refused credentials wrap ErrSSHAuthFailed, and an untrusted host key wraps
// ErrSSHHostKey. That keeps SSH's error shapes inside the adapter rather than
// spread across the Registry and the UI.
type TunnelDialer interface {
	// Open dials the bastion described by config and returns a live Tunnel. A
	// returned error means no session was left open.
	Open(ctx context.Context, config SSHTunnel) (Tunnel, error)
}

// tunnelledConn is a connection whose traffic runs through a tunnel it owns.
//
// The two are one lifetime: the Registry holds a tunnelledConn where it would
// hold a Conn, so every path that closes a connection closes its tunnel too,
// and no path can close one without the other.
type tunnelledConn struct {
	Conn
	tunnel Tunnel
}

// Close closes the connection first and the tunnel second. The order is not
// arbitrary: the connection's goodbye to the server travels on the tunnel, and
// pulling the tunnel out from under it turns an orderly close into a reset.
func (c tunnelledConn) Close() error {
	err := c.Conn.Close()
	if tunnelErr := c.tunnel.Close(); tunnelErr != nil && err == nil {
		err = tunnelErr
	}
	return err
}
