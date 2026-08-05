package dbtest

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/themethaithian/go-db/internal/db"
)

// FakeDriver is an in-memory db.Driver standing in for a set of MySQL servers.
//
// It models what a real one does at connect time: a Profile the test has called
// Accept for is a server that exists and checks the password; any other Profile
// is an address nothing answers on, so opening it reports db.ErrUnreachable.
// Fail overrides either with an arbitrary error, for the outcomes that are
// neither authentication nor reachability.
//
// It tracks the connections it hands out so a test can assert lifecycle:
// OpenProfiles reports what is open right now, Opens how many were ever
// opened. It is safe for concurrent use.
type FakeDriver struct {
	mu        sync.Mutex
	passwords map[string]string // profiles that accept a connection
	failures  map[string]error  // forced outcomes, checked first
	open      map[string]int    // currently open connections, by Profile
	opens     int
}

// NewFakeDriver returns a FakeDriver for which no Profile is reachable yet.
func NewFakeDriver() *FakeDriver {
	return &FakeDriver{
		passwords: make(map[string]string),
		failures:  make(map[string]error),
		open:      make(map[string]int),
	}
}

// Accept makes profileName reachable, answering to exactly this password. A
// connection attempt with any other password fails with db.ErrAuthFailed, as a
// real server would.
func (d *FakeDriver) Accept(profileName, password string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.passwords[profileName] = password
	delete(d.failures, profileName)
}

// Fail makes every connection attempt for profileName report err, whatever its
// password. Use it for the outcomes Accept cannot express — an unknown
// database, say — by passing a plain error.
func (d *FakeDriver) Fail(profileName string, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.failures[profileName] = err
}

// OpenProfiles returns the names of the Profiles with a connection currently
// open, ordered by name. A Profile appears once however many connections it
// holds.
func (d *FakeDriver) OpenProfiles() []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	names := make([]string, 0, len(d.open))
	for name, count := range d.open {
		if count > 0 {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// Opens returns how many connections the driver has handed out in total,
// including ones since closed.
func (d *FakeDriver) Opens() int {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.opens
}

// Open implements db.Driver.
func (d *FakeDriver) Open(ctx context.Context, profile db.Profile, password string) (db.Conn, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err, ok := d.failures[profile.Name]; ok {
		return nil, err
	}

	want, reachable := d.passwords[profile.Name]
	if !reachable {
		return nil, fmt.Errorf("%w: nothing listening for profile %q", db.ErrUnreachable, profile.Name)
	}
	if password != want {
		return nil, fmt.Errorf("%w: wrong password for profile %q", db.ErrAuthFailed, profile.Name)
	}

	d.open[profile.Name]++
	d.opens++
	return &fakeConn{driver: d, profile: profile.Name}, nil
}

// fakeConn is one connection handed out by a FakeDriver. It reports itself
// closed to the driver so lifecycle assertions see it, and refuses use after
// close so a test that keeps a stale connection fails loudly.
type fakeConn struct {
	driver  *FakeDriver
	profile string

	mu     sync.Mutex
	closed bool
}

func (c *fakeConn) Ping(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return errors.New("dbtest: ping on a closed connection")
	}
	return ctx.Err()
}

func (c *fakeConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return errors.New("dbtest: connection closed twice")
	}
	c.closed = true

	c.driver.mu.Lock()
	defer c.driver.mu.Unlock()
	c.driver.open[c.profile]--
	return nil
}

var (
	_ db.Driver = (*FakeDriver)(nil)
	_ db.Conn   = (*fakeConn)(nil)
)
