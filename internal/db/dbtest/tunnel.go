package dbtest

import (
	"context"
	"errors"
	"net"
	"sync"

	"github.com/themethaithian/go-db/internal/db"
)

// FakeTunnels is an in-memory db.TunnelDialer standing in for a set of bastion
// hosts. No SSH happens: opening a tunnel records the Profile's configuration,
// and dialling through one records the address the driver asked for — which is
// the evidence that the database was reached from the bastion rather than from
// here.
//
// Failures are scripted per address: FailOpen for a bastion that will not let
// us in, FailDial for a database the bastion cannot reach. Both take the error
// a real dialler would return, so a test scripts db.ErrSSHAuthFailed itself
// rather than a stand-in for it.
//
// It tracks lifecycle so a test can prove a tunnel was not leaked: Opened
// reports every tunnel ever opened, OpenTunnels how many are open right now. It
// is safe for concurrent use.
type FakeTunnels struct {
	mu         sync.Mutex
	openFails  map[string]error // by bastion address
	dialFails  map[string]error // by database address
	opened     []db.SSHTunnel
	dials      []string
	open       int
	whenClosed func()
}

// NewFakeTunnels returns a dialler whose bastions all answer.
func NewFakeTunnels() *FakeTunnels {
	return &FakeTunnels{
		openFails: make(map[string]error),
		dialFails: make(map[string]error),
	}
}

// FailOpen makes opening a tunnel to the bastion at address (host:port) fail
// with err — an unreachable bastion, a refused key, an unknown host key.
func (t *FakeTunnels) FailOpen(address string, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.openFails[address] = err
}

// FailDial makes dialling address through any tunnel fail with err. It is how a
// test reaches the outcome only a tunnel has: the bastion let us in, and the
// database is not reachable from it.
func (t *FakeTunnels) FailDial(address string, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.dialFails[address] = err
}

// WhenClosed registers f to run at the moment a tunnel is closed, before the
// close is recorded. It is how a test asserts ordering against another fake —
// asking what the driver still holds open while the tunnel is closing.
func (t *FakeTunnels) WhenClosed(f func()) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.whenClosed = f
}

// Opened returns the tunnel configurations opened so far, in order, including
// tunnels since closed.
func (t *FakeTunnels) Opened() []db.SSHTunnel {
	t.mu.Lock()
	defer t.mu.Unlock()

	return append([]db.SSHTunnel(nil), t.opened...)
}

// Dials returns the addresses dialled through a tunnel, in order. It is the
// evidence that traffic went through the bastion.
func (t *FakeTunnels) Dials() []string {
	t.mu.Lock()
	defer t.mu.Unlock()

	return append([]string(nil), t.dials...)
}

// OpenTunnels returns how many tunnels are open right now. Anything but zero
// after everything is closed is a leaked SSH session.
func (t *FakeTunnels) OpenTunnels() int {
	t.mu.Lock()
	defer t.mu.Unlock()

	return t.open
}

// Open implements db.TunnelDialer.
func (t *FakeTunnels) Open(ctx context.Context, config db.SSHTunnel) (db.Tunnel, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err, failing := t.openFails[config.Address()]; failing {
		return nil, err
	}

	t.opened = append(t.opened, config)
	t.open++
	return &fakeTunnel{tunnels: t}, nil
}

// fakeTunnel is one open tunnel. It refuses use after close so a test holding a
// stale tunnel fails loudly — and so an integration-style assertion that a
// closed tunnel carries nothing has the same meaning here.
type fakeTunnel struct {
	tunnels *FakeTunnels

	mu     sync.Mutex
	closed bool
}

// Dial hands back one end of an in-memory pipe: the caller gets a net.Conn that
// closes cleanly, which is all a fake driver does with it.
func (t *fakeTunnel) Dial(ctx context.Context, address string) (net.Conn, error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil, errors.New("dbtest: dial through a closed tunnel")
	}
	t.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	t.tunnels.mu.Lock()
	defer t.tunnels.mu.Unlock()

	t.tunnels.dials = append(t.tunnels.dials, address)
	if err, failing := t.tunnels.dialFails[address]; failing {
		return nil, err
	}

	local, remote := net.Pipe()
	remote.Close()
	return local, nil
}

func (t *fakeTunnel) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return errors.New("dbtest: tunnel closed twice")
	}
	t.closed = true
	t.mu.Unlock()

	// The hook runs outside the dialler's lock so it may ask this FakeTunnels
	// what it holds, as an ordering assertion does.
	t.tunnels.mu.Lock()
	hook := t.tunnels.whenClosed
	t.tunnels.mu.Unlock()
	if hook != nil {
		hook()
	}

	t.tunnels.mu.Lock()
	defer t.tunnels.mu.Unlock()

	t.tunnels.open--
	return nil
}

var (
	_ db.TunnelDialer = (*FakeTunnels)(nil)
	_ db.Tunnel       = (*fakeTunnel)(nil)
)
