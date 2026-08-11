package db_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/themethaithian/go-db/internal/db"
	"github.com/themethaithian/go-db/internal/db/dbtest"
)

// These tests pin the Connection Registry's tunnel lifecycle: a Profile with an
// SSH tunnel gets one, a Profile without one does not, and the tunnel lives and
// dies with the connection it carries. They use the fake Driver and the fake
// TunnelDialer, so nothing here dials anything.

const (
	directHost  = "db.internal"
	fakeBastion = "bastion.example:22"
)

// tunnelled is a Profile reached through a bastion; direct is the same database
// reached without one.
func tunnelledProfile(name string) db.Profile {
	return db.Profile{
		Name: name, Host: directHost, Port: 3306, User: "root", Database: "app",
		SSH: &db.SSHTunnel{Host: "bastion.example", Port: 22, User: "jump"},
	}
}

func directProfile(name string) db.Profile {
	return db.Profile{Name: name, Host: directHost, Port: 3306, User: "root", Database: "app"}
}

// newRegistry returns a Registry over the given fake driver and fake tunnels,
// with profiles saved and their secrets stored. These tests are not about
// Engine selection, so every Profile here reaches driver as the Registry's
// sole MySQL entry — which is what a Profile with no Engine set becomes once
// saved, since ProfileStore normalizes it.
func newRegistry(t *testing.T, driver *dbtest.FakeDriver, tunnels *dbtest.FakeTunnels, profiles ...db.Profile) *db.Registry {
	t.Helper()
	return newRegistryWithDrivers(t, db.Drivers{db.EngineMySQL: driver}, tunnels, profiles...)
}

// newRegistryWithDrivers is newRegistry with the Drivers map given directly:
// the seam the Engine-selection tests use to hand two Profiles two different
// fake Drivers, or to save a Profile whose Engine the map has no Driver for at
// all.
func newRegistryWithDrivers(t *testing.T, drivers db.Drivers, tunnels *dbtest.FakeTunnels, profiles ...db.Profile) *db.Registry {
	t.Helper()

	store := db.NewProfileStore(t.TempDir(), dbtest.NewFakeKeychain())
	for _, profile := range profiles {
		if err := store.Save(profile, "s3cret"); err != nil {
			t.Fatalf("saving profile %q: %v", profile.Name, err)
		}
		engine := profile.Engine
		if engine == "" {
			engine = db.EngineMySQL // mirrors the normalization store.Save just did
		}
		if driver, ok := drivers[engine].(*dbtest.FakeDriver); ok {
			driver.Accept(profile.Name, "s3cret")
		}
	}
	return db.NewRegistryWithTunnels(drivers, tunnels, store)
}

// engineProfile is a direct Profile pinned to a given Engine — the shape the
// Engine-selection tests need, since directProfile always builds a
// zero-Engine (so MySQL-normalized) one.
func engineProfile(name string, engine db.Engine) db.Profile {
	return db.Profile{Name: name, Host: directHost, Port: 3306, User: "root", Database: "app", Engine: engine}
}

// tunnelledEngineProfile is engineProfile reached through a bastion, for
// proving a Driver miss fails before any tunnel is opened.
func tunnelledEngineProfile(name string, engine db.Engine) db.Profile {
	profile := engineProfile(name, engine)
	profile.SSH = &db.SSHTunnel{Host: "bastion.example", Port: 22, User: "jump"}
	return profile
}

// These tests pin how the Registry picks a Driver: a Profile's Engine chooses
// which entry of the Drivers map dials it, and a Profile whose Engine has no
// entry fails with ErrEngineUnsupported before anything else happens — no
// tunnel opened, no other Driver touched, the Registry left exactly as it was.

func TestConnectDialsTheDriverForTheProfilesEngine(t *testing.T) {
	mysql, redis := dbtest.NewFakeDriver(), dbtest.NewFakeDriver()
	tunnels := dbtest.NewFakeTunnels()
	drivers := db.Drivers{db.EngineMySQL: mysql, db.EngineRedis: redis}
	registry := newRegistryWithDrivers(t, drivers, tunnels, engineProfile("cache", db.EngineRedis))

	if err := registry.Connect(context.Background(), "cache"); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	if redis.Opens() != 1 {
		t.Errorf("the redis Driver opened %d connections, want 1", redis.Opens())
	}
	if mysql.Opens() != 0 {
		t.Errorf("the mysql Driver opened %d connections, want 0: the Profile's Engine is redis", mysql.Opens())
	}
}

func TestConnectWithNoDriverForTheEngineFailsBeforeDialling(t *testing.T) {
	mysql := dbtest.NewFakeDriver()
	tunnels := dbtest.NewFakeTunnels()
	drivers := db.Drivers{db.EngineMySQL: mysql}
	registry := newRegistryWithDrivers(t, drivers, tunnels, tunnelledEngineProfile("nosql", db.EngineMongoDB))

	err := registry.Connect(context.Background(), "nosql")
	if !errors.Is(err, db.ErrEngineUnsupported) {
		t.Fatalf("Connect error = %v, want ErrEngineUnsupported", err)
	}
	if got := registry.Connected(); len(got) != 0 {
		t.Errorf("Connected = %v after a Connect with no Driver for the Engine, want empty", got)
	}
	if mysql.Opens() != 0 {
		t.Errorf("%d connections opened, want none: the Profile's Engine has no Driver", mysql.Opens())
	}
	if opened := tunnels.Opened(); len(opened) != 0 {
		t.Errorf("opened %v tunnels, want none: ErrEngineUnsupported must fail before a tunnel is even asked for", opened)
	}
}

func TestTestWithNoDriverForTheEngineFailsBeforeDialling(t *testing.T) {
	mysql := dbtest.NewFakeDriver()
	tunnels := dbtest.NewFakeTunnels()
	drivers := db.Drivers{db.EngineMySQL: mysql}
	registry := newRegistryWithDrivers(t, drivers, tunnels, tunnelledEngineProfile("nosql", db.EngineMongoDB))

	err := registry.Test(context.Background(), "nosql")
	if !errors.Is(err, db.ErrEngineUnsupported) {
		t.Fatalf("Test error = %v, want ErrEngineUnsupported", err)
	}
	if opened := tunnels.Opened(); len(opened) != 0 {
		t.Errorf("opened %v tunnels, want none: ErrEngineUnsupported must fail before a tunnel is even asked for", opened)
	}
	if mysql.Opens() != 0 {
		t.Errorf("%d connections opened, want none: the Profile's Engine has no Driver", mysql.Opens())
	}
}

func TestConnectWithoutSSHOpensNoTunnel(t *testing.T) {
	driver, tunnels := dbtest.NewFakeDriver(), dbtest.NewFakeTunnels()
	registry := newRegistry(t, driver, tunnels, directProfile("local"))

	if err := registry.Connect(context.Background(), "local"); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	if opened := tunnels.Opened(); len(opened) != 0 {
		t.Errorf("a Profile without an SSH tunnel opened %v, want no tunnel", opened)
	}
	if dials := tunnels.Dials(); len(dials) != 0 {
		t.Errorf("a direct connection dialled %v through a tunnel, want nothing", dials)
	}
}

func TestConnectThroughTunnelDialsTheDatabaseFromTheBastion(t *testing.T) {
	driver, tunnels := dbtest.NewFakeDriver(), dbtest.NewFakeTunnels()
	profile := tunnelledProfile("remote")
	registry := newRegistry(t, driver, tunnels, profile)

	if err := registry.Connect(context.Background(), "remote"); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	opened := tunnels.Opened()
	if len(opened) != 1 {
		t.Fatalf("opened %d tunnels, want 1", len(opened))
	}
	if opened[0] != *profile.SSH {
		t.Errorf("tunnel opened with %+v, want the Profile's %+v", opened[0], *profile.SSH)
	}
	// The database address must be dialled from the bastion, not from here:
	// that is the whole point of the tunnel.
	if dials := tunnels.Dials(); len(dials) != 1 || dials[0] != profile.Address() {
		t.Errorf("dialled %v through the tunnel, want [%s]", dials, profile.Address())
	}
	if tunnels.OpenTunnels() != 1 {
		t.Errorf("%d tunnels open while connected, want 1", tunnels.OpenTunnels())
	}
}

func TestDisconnectClosesTheConnectionBeforeItsTunnel(t *testing.T) {
	driver, tunnels := dbtest.NewFakeDriver(), dbtest.NewFakeTunnels()
	registry := newRegistry(t, driver, tunnels, tunnelledProfile("remote"))

	// The connection's goodbye travels on the tunnel, so the tunnel must
	// outlive it by one step. This records what was still open at the moment
	// the tunnel closed.
	var openAtTunnelClose []string
	tunnels.WhenClosed(func() { openAtTunnelClose = driver.OpenProfiles() })

	if err := registry.Connect(context.Background(), "remote"); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if err := registry.Disconnect("remote"); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	if len(openAtTunnelClose) != 0 {
		t.Errorf("%v was still open when the tunnel closed, want the connection closed first", openAtTunnelClose)
	}
	if tunnels.OpenTunnels() != 0 {
		t.Errorf("%d tunnels open after Disconnect, want none", tunnels.OpenTunnels())
	}
	if open := driver.OpenProfiles(); len(open) != 0 {
		t.Errorf("Disconnect left %v open, want nothing", open)
	}
}

func TestFailedOpenClosesTheTunnelItWasGoing(t *testing.T) {
	driver, tunnels := dbtest.NewFakeDriver(), dbtest.NewFakeTunnels()
	registry := newRegistry(t, driver, tunnels, tunnelledProfile("remote"))
	// The bastion answers; the database behind it refuses the credentials.
	driver.Fail("remote", errors.New("db: unknown database \"app\""))

	if err := registry.Connect(context.Background(), "remote"); err == nil {
		t.Fatal("Connect succeeded, want the driver's failure")
	}

	if len(tunnels.Opened()) != 1 {
		t.Fatalf("opened %d tunnels, want 1", len(tunnels.Opened()))
	}
	if tunnels.OpenTunnels() != 0 {
		t.Errorf("%d tunnels left open by a failed connect, want none: a tunnel is a live SSH session and a leak here is a process leak", tunnels.OpenTunnels())
	}
	if got := registry.Connected(); len(got) != 0 {
		t.Errorf("Connected = %v after a failed connect, want empty", got)
	}
}

func TestConnectReportsTunnelFailures(t *testing.T) {
	cases := []struct {
		name    string
		script  func(t *dbtest.FakeTunnels)
		wantErr error
	}{
		{
			name: "the bastion is unreachable",
			script: func(tunnels *dbtest.FakeTunnels) {
				tunnels.FailOpen(fakeBastion, db.ErrBastionUnreachable)
			},
			// A bastion nothing answers on is still an unreachable database.
			wantErr: db.ErrUnreachable,
		},
		{
			name: "the bastion refuses the SSH key",
			script: func(tunnels *dbtest.FakeTunnels) {
				tunnels.FailOpen(fakeBastion, db.ErrSSHAuthFailed)
			},
			wantErr: db.ErrSSHAuthFailed,
		},
		{
			name: "the bastion's host key is not known",
			script: func(tunnels *dbtest.FakeTunnels) {
				tunnels.FailOpen(fakeBastion, db.ErrSSHHostKey)
			},
			wantErr: db.ErrSSHHostKey,
		},
		{
			name: "the database is unreachable from the bastion",
			script: func(tunnels *dbtest.FakeTunnels) {
				tunnels.FailDial("db.internal:3306", db.ErrUnreachable)
			},
			wantErr: db.ErrUnreachable,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			driver, tunnels := dbtest.NewFakeDriver(), dbtest.NewFakeTunnels()
			registry := newRegistry(t, driver, tunnels, tunnelledProfile("remote"))
			tc.script(tunnels)

			err := registry.Connect(context.Background(), "remote")
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Connect error = %v, want %v", err, tc.wantErr)
			}
			if tunnels.OpenTunnels() != 0 {
				t.Errorf("%d tunnels left open, want none", tunnels.OpenTunnels())
			}
			if got := registry.Connected(); len(got) != 0 {
				t.Errorf("Connected = %v, want empty", got)
			}
		})
	}
}

func TestRegistryHoldsTunnelledAndDirectConnectionsAtOnce(t *testing.T) {
	driver, tunnels := dbtest.NewFakeDriver(), dbtest.NewFakeTunnels()
	registry := newRegistry(t, driver, tunnels, tunnelledProfile("remote"), directProfile("local"))

	ctx := context.Background()
	for _, name := range []string{"remote", "local"} {
		if err := registry.Connect(ctx, name); err != nil {
			t.Fatalf("Connect(%q): %v", name, err)
		}
	}

	if got := registry.Connected(); len(got) != 2 {
		t.Fatalf("Connected = %v, want both profiles", got)
	}
	if tunnels.OpenTunnels() != 1 {
		t.Errorf("%d tunnels open, want 1: only the tunnelled Profile has a bastion", tunnels.OpenTunnels())
	}

	if err := registry.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if tunnels.OpenTunnels() != 0 {
		t.Errorf("%d tunnels open after Close, want none", tunnels.OpenTunnels())
	}
	if open := driver.OpenProfiles(); len(open) != 0 {
		t.Errorf("Close left %v open, want nothing", open)
	}
}

func TestTestConnectionThroughTunnelLeavesNothingOpen(t *testing.T) {
	driver, tunnels := dbtest.NewFakeDriver(), dbtest.NewFakeTunnels()
	registry := newRegistry(t, driver, tunnels, tunnelledProfile("remote"))

	if err := registry.Test(context.Background(), "remote"); err != nil {
		t.Fatalf("Test: %v", err)
	}

	if len(tunnels.Opened()) != 1 {
		t.Errorf("a connection test opened %d tunnels, want 1", len(tunnels.Opened()))
	}
	if tunnels.OpenTunnels() != 0 {
		t.Errorf("a connection test left %d tunnels open, want none", tunnels.OpenTunnels())
	}
	if open := driver.OpenProfiles(); len(open) != 0 {
		t.Errorf("a connection test left %v open, want nothing", open)
	}
	if got := registry.Connected(); len(got) != 0 {
		t.Errorf("a connection test left %v connected, want nothing", got)
	}
}

// The rest of this file pins the sibling connections a selected database is:
// a Profile connected once, plus one more connection per other schema anyone
// asked for, each opened on that schema as its DSN's dbname. The Editor
// selecting a database must be the connection's real default schema — nothing
// runs USE on a shared session — and that is what these tests state.

func TestConnOpensASiblingOnTheRequestedDatabase(t *testing.T) {
	driver, tunnels := dbtest.NewFakeDriver(), dbtest.NewFakeTunnels()
	registry := newRegistry(t, driver, tunnels, directProfile("local"))
	ctx := context.Background()

	if err := registry.Connect(ctx, "local"); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if _, err := registry.Conn(ctx, "local", "reporting"); err != nil {
		t.Fatalf("Conn(local, reporting): %v", err)
	}

	// "app" is the Profile's own; "reporting" is the sibling, and it must be
	// the DSN's dbname rather than a USE on the connection already open.
	if got := driver.OpenDatabases("local"); !equalStrings(got, []string{"app", "reporting"}) {
		t.Errorf("open connections on %v, want [app reporting]", got)
	}
}

func TestConnReusesAnOpenSibling(t *testing.T) {
	driver, tunnels := dbtest.NewFakeDriver(), dbtest.NewFakeTunnels()
	registry := newRegistry(t, driver, tunnels, directProfile("local"))
	ctx := context.Background()

	if err := registry.Connect(ctx, "local"); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := registry.Conn(ctx, "local", "reporting"); err != nil {
			t.Fatalf("Conn(local, reporting) #%d: %v", i, err)
		}
	}

	if driver.Opens() != 2 {
		t.Errorf("%d connections opened, want 2: the Profile's own and one sibling", driver.Opens())
	}
}

func TestConnWithoutADatabaseIsTheProfilesOwnConnection(t *testing.T) {
	driver, tunnels := dbtest.NewFakeDriver(), dbtest.NewFakeTunnels()
	registry := newRegistry(t, driver, tunnels, directProfile("local"))
	ctx := context.Background()

	if err := registry.Connect(ctx, "local"); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if _, err := registry.Conn(ctx, "local", ""); err != nil {
		t.Fatalf("Conn(local, \"\"): %v", err)
	}

	if driver.Opens() != 1 {
		t.Errorf("%d connections opened, want 1: the blank database is the connection already held", driver.Opens())
	}
}

// The Profile's own schema is not a sibling of itself. A human selecting the
// database their Profile already names must not quietly double the number of
// connections the app holds — the performance budget is a feature.
func TestConnOnTheProfilesOwnDatabaseOpensNothingNew(t *testing.T) {
	driver, tunnels := dbtest.NewFakeDriver(), dbtest.NewFakeTunnels()
	registry := newRegistry(t, driver, tunnels, directProfile("local"))
	ctx := context.Background()

	if err := registry.Connect(ctx, "local"); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if _, err := registry.Conn(ctx, "local", "app"); err != nil {
		t.Fatalf("Conn(local, app): %v", err)
	}

	if driver.Opens() != 1 {
		t.Errorf("%d connections opened, want 1: \"app\" is already this connection's default schema", driver.Opens())
	}
}

func TestConnDoesNotOpenASiblingForAProfileThatIsNotConnected(t *testing.T) {
	driver, tunnels := dbtest.NewFakeDriver(), dbtest.NewFakeTunnels()
	registry := newRegistry(t, driver, tunnels, directProfile("local"))

	_, err := registry.Conn(context.Background(), "local", "reporting")
	if !errors.Is(err, db.ErrNotConnected) {
		t.Fatalf("Conn error = %v, want ErrNotConnected", err)
	}
	if driver.Opens() != 0 {
		t.Errorf("%d connections opened, want none: a sibling of nothing is nothing", driver.Opens())
	}
}

func TestDisconnectClosesTheProfilesSiblings(t *testing.T) {
	driver, tunnels := dbtest.NewFakeDriver(), dbtest.NewFakeTunnels()
	registry := newRegistry(t, driver, tunnels, tunnelledProfile("remote"))
	ctx := context.Background()

	if err := registry.Connect(ctx, "remote"); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	for _, database := range []string{"reporting", "archive"} {
		if _, err := registry.Conn(ctx, "remote", database); err != nil {
			t.Fatalf("Conn(remote, %s): %v", database, err)
		}
	}
	// Correctness before economy: a sibling is a connection of its own and
	// goes through a tunnel of its own (issue #32).
	if tunnels.OpenTunnels() != 3 {
		t.Fatalf("%d tunnels open, want 3: one per connection", tunnels.OpenTunnels())
	}

	if err := registry.Disconnect("remote"); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	if open := driver.OpenDatabases("remote"); len(open) != 0 {
		t.Errorf("Disconnect left connections on %v open, want none", open)
	}
	if tunnels.OpenTunnels() != 0 {
		t.Errorf("%d tunnels open after Disconnect, want none: a sibling's tunnel is a live SSH session too", tunnels.OpenTunnels())
	}
	if got := registry.Connected(); len(got) != 0 {
		t.Errorf("Connected = %v after Disconnect, want empty", got)
	}
}

func TestCloseClosesSiblingsToo(t *testing.T) {
	driver, tunnels := dbtest.NewFakeDriver(), dbtest.NewFakeTunnels()
	registry := newRegistry(t, driver, tunnels, directProfile("local"))
	ctx := context.Background()

	if err := registry.Connect(ctx, "local"); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if _, err := registry.Conn(ctx, "local", "reporting"); err != nil {
		t.Fatalf("Conn(local, reporting): %v", err)
	}

	if err := registry.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if open := driver.OpenDatabases("local"); len(open) != 0 {
		t.Errorf("Close left connections on %v open, want none", open)
	}
}

// Two views asking for the same database at the same moment is the ordinary
// case — a tab switch and an autocomplete fetch race constantly. Dialling
// happens outside the lock, so both may dial; only one may be kept, and the
// other must be closed rather than left open with nobody holding it.
func TestConcurrentSiblingRequestsKeepOneConnection(t *testing.T) {
	driver, tunnels := dbtest.NewFakeDriver(), dbtest.NewFakeTunnels()
	registry := newRegistry(t, driver, tunnels, directProfile("local"))
	ctx := context.Background()

	if err := registry.Connect(ctx, "local"); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	const racers = 8
	conns := make([]db.Conn, racers)
	errs := make([]error, racers)
	var start, done sync.WaitGroup
	start.Add(1)
	done.Add(racers)
	for i := 0; i < racers; i++ {
		go func() {
			defer done.Done()
			start.Wait()
			conns[i], errs[i] = registry.Conn(ctx, "local", "reporting")
		}()
	}
	start.Done()
	done.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("Conn #%d: %v", i, err)
		}
	}
	if got := driver.OpenDatabases("local"); !equalStrings(got, []string{"app", "reporting"}) {
		t.Errorf("open connections on %v, want [app reporting]: a losing dial must be closed, not leaked", got)
	}
	// Every caller must have been handed a connection that still works — a
	// racer given the connection that was closed would be a use-after-close.
	for i, conn := range conns {
		if err := conn.Ping(ctx); err != nil {
			t.Errorf("Ping on the connection handed to racer %d: %v", i, err)
		}
	}
}

// equalStrings reports whether two string slices hold the same values in the
// same order.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
