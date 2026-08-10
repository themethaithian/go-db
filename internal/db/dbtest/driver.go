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
// Reads are scripted per Profile with Answer, AnswerQuery, RejectWrites, and
// FailQuery; Queries reports what was actually asked, which is how a test
// proves a mutation never reached the database.
//
// Writes are scripted with Affect and FailExec, and Execs reports the
// statements that were executed. The two lists are kept apart on purpose: a
// test asserting that a mutation was previewed but not run is asking whether
// its statement is in Execs, and an Exec that never happened must not be
// hidden among the preview's reads.
//
// It tracks the connections it hands out so a test can assert lifecycle:
// OpenProfiles reports what is open right now, Opens how many were ever
// opened. It is safe for concurrent use.
//
// A Profile can have more than one connection open at a time, each on its own
// default schema — that is what the Connection Registry's sibling connections
// are — so everything recorded per Profile is also recorded per (Profile, DSN
// dbname). Scripting stays per Profile, since a fake server answers the same
// whichever schema it was opened on; the *In accessors are what a test asks
// when the question is which connection a statement went down.
type FakeDriver struct {
	mu        sync.Mutex
	passwords map[string]string       // profiles that accept a connection
	failures  map[string]error        // forced outcomes, checked first
	open      map[string]int          // currently open connections, by Profile
	openIn    map[target]int          // the same, by Profile and DSN dbname
	answers   map[string]db.ResultSet // scripted read results, by Profile
	byQuery   map[string]db.ResultSet // scripted read results, by Profile and statement
	refusals  map[string]error        // scripted read failures, by Profile
	queries   map[string][]string     // statements ReadQuery was given
	queriesIn map[target][]string     // the same, by Profile and DSN dbname
	affected  map[string]int64        // scripted affected-row counts, by Profile
	execFails map[string]error        // scripted write failures, by Profile
	execs     map[string][]string     // statements Exec was given
	execsIn   map[target][]string     // the same, by Profile and DSN dbname
	opens     int
}

// target is one connection's identity as this fake sees it: the Profile it was
// opened for and the schema its DSN named. It is the fake's mirror of the
// Registry's own key, and it is what makes "which connection ran this" a
// question a test can ask.
type target struct {
	profile  string
	database string
}

// NewFakeDriver returns a FakeDriver for which no Profile is reachable yet.
func NewFakeDriver() *FakeDriver {
	return &FakeDriver{
		passwords: make(map[string]string),
		failures:  make(map[string]error),
		open:      make(map[string]int),
		openIn:    make(map[target]int),
		answers:   make(map[string]db.ResultSet),
		byQuery:   make(map[string]db.ResultSet),
		refusals:  make(map[string]error),
		queries:   make(map[string][]string),
		queriesIn: make(map[target][]string),
		affected:  make(map[string]int64),
		execFails: make(map[string]error),
		execs:     make(map[string][]string),
		execsIn:   make(map[target][]string),
	}
}

// Answer makes every read on profileName return result. A Profile with no
// scripted answer fails the read loudly rather than returning nothing, so a
// query a test did not expect to run cannot pass silently.
func (d *FakeDriver) Answer(profileName string, result db.ResultSet) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.answers[profileName] = result
	delete(d.refusals, profileName)
}

// AnswerQuery makes reads of exactly sql on profileName return result. It takes
// precedence over Answer, which is how a test scripts an Impact Preview: the
// count query and the sample query are two different statements and must give
// two different answers.
func (d *FakeDriver) AnswerQuery(profileName, sql string, result db.ResultSet) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.byQuery[profileName+"\x00"+sql] = result
	delete(d.refusals, profileName)
}

// Affect makes every Exec on profileName report that it changed rows.
func (d *FakeDriver) Affect(profileName string, rows int64) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.affected[profileName] = rows
	delete(d.execFails, profileName)
}

// FailExec makes every Exec on profileName fail with err.
func (d *FakeDriver) FailExec(profileName string, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.execFails[profileName] = err
	delete(d.affected, profileName)
}

// Execs returns the statements Exec was given for profileName, in order. An
// empty result is the evidence that nothing was written.
func (d *FakeDriver) Execs(profileName string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]string(nil), d.execs[profileName]...)
}

// ExecsIn is Execs narrowed to the connection whose DSN named database — the
// evidence that a confirmed mutation ran on the schema it was classified
// under, and not on the Profile's own connection beside it.
func (d *FakeDriver) ExecsIn(profileName, database string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]string(nil), d.execsIn[target{profileName, database}]...)
}

// exec performs one write for profileName, recording it first. A Profile with
// no scripted outcome still succeeds, reporting no rows changed: a mutation
// reaching a database that was never told what to say is a test bug about
// Execs, and it should be read there rather than as a failed statement.
func (d *FakeDriver) exec(at target, sql string) (int64, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.execs[at.profile] = append(d.execs[at.profile], sql)
	d.execsIn[at] = append(d.execsIn[at], sql)

	if err, failed := d.execFails[at.profile]; failed {
		return 0, err
	}
	return d.affected[at.profile], nil
}

// RejectWrites makes profileName's server behave like the Approval Gate's
// backstop: every read is refused with db.ErrWriteAttempt, as MySQL refuses a
// write inside a READ ONLY transaction. It is how a test reproduces a
// classifier miss without a real database.
func (d *FakeDriver) RejectWrites(profileName string) {
	d.FailQuery(profileName, fmt.Errorf("%w: dbtest: profile %q refuses writes", db.ErrWriteAttempt, profileName))
}

// FailQuery makes every read on profileName fail with err — an unknown column,
// a dropped connection, anything the database says no to.
func (d *FakeDriver) FailQuery(profileName string, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.refusals[profileName] = err
	delete(d.answers, profileName)
}

// Queries returns the statements ReadQuery was given for profileName, in order.
// An empty result is the evidence that nothing was executed.
func (d *FakeDriver) Queries(profileName string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]string(nil), d.queries[profileName]...)
}

// QueriesIn is Queries narrowed to the connection whose DSN named database —
// the evidence that a statement, or an Impact Preview's count, went down the
// connection for the schema it was submitted against.
func (d *FakeDriver) QueriesIn(profileName, database string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]string(nil), d.queriesIn[target{profileName, database}]...)
}

// readQuery answers one read for at's connection, recording it first.
func (d *FakeDriver) readQuery(at target, sql string) (db.ResultSet, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.queries[at.profile] = append(d.queries[at.profile], sql)
	d.queriesIn[at] = append(d.queriesIn[at], sql)

	if err, refused := d.refusals[at.profile]; refused {
		return db.ResultSet{}, err
	}
	if answer, scripted := d.byQuery[at.profile+"\x00"+sql]; scripted {
		return answer, nil
	}
	if answer, scripted := d.answers[at.profile]; scripted {
		return answer, nil
	}
	return db.ResultSet{}, fmt.Errorf("dbtest: profile %q has no scripted result for %q", at.profile, sql)
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

// OpenDatabases returns the DSN dbname of every connection currently open for
// profileName, ordered by name and with one entry per connection — so two
// connections on the same schema appear twice, which is what a leaked sibling
// looks like.
func (d *FakeDriver) OpenDatabases(profileName string) []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	databases := make([]string, 0, len(d.openIn))
	for at, count := range d.openIn {
		if at.profile != profileName {
			continue
		}
		for i := 0; i < count; i++ {
			databases = append(databases, at.database)
		}
	}
	sort.Strings(databases)
	return databases
}

// Opens returns how many connections the driver has handed out in total,
// including ones since closed.
func (d *FakeDriver) Opens() int {
	d.mu.Lock()
	defer d.mu.Unlock()

	return d.opens
}

// Open implements db.Driver.
//
// A non-nil dial is used before anything else is decided, as a real driver
// would use it: a Profile whose tunnel cannot reach the database fails to open
// even though the database itself is scripted to answer.
func (d *FakeDriver) Open(ctx context.Context, profile db.Profile, password string, dial db.DialFunc) (db.Conn, error) {
	if dial != nil {
		conn, err := dial(ctx, profile.Address())
		if err != nil {
			return nil, err
		}
		// Nothing is sent over it; the dial itself is the evidence that this
		// connection went the way the Profile said it should.
		conn.Close()
	}

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

	// profile.Database is the schema this connection's DSN names, which is how
	// a sibling connection differs from the Profile's own.
	at := target{profile: profile.Name, database: profile.Database}
	d.open[profile.Name]++
	d.openIn[at]++
	d.opens++
	return &fakeConn{driver: d, at: at}, nil
}

// fakeConn is one connection handed out by a FakeDriver. It reports itself
// closed to the driver so lifecycle assertions see it, and refuses use after
// close so a test that keeps a stale connection fails loudly.
type fakeConn struct {
	driver *FakeDriver
	at     target

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

func (c *fakeConn) ReadQuery(ctx context.Context, sql string) (db.ResultSet, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return db.ResultSet{}, errors.New("dbtest: read on a closed connection")
	}
	c.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return db.ResultSet{}, err
	}
	return c.driver.readQuery(c.at, sql)
}

func (c *fakeConn) Exec(ctx context.Context, sql string) (int64, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return 0, errors.New("dbtest: exec on a closed connection")
	}
	c.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return 0, err
	}
	return c.driver.exec(c.at, sql)
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
	c.driver.open[c.at.profile]--
	c.driver.openIn[c.at]--
	return nil
}

var (
	_ db.Driver = (*FakeDriver)(nil)
	_ db.Conn   = (*fakeConn)(nil)
)
