package db_test

import (
	"context"
	"errors"
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

// newRegistry returns a Registry over the given fakes, with profiles saved and
// their secrets stored.
func newRegistry(t *testing.T, driver *dbtest.FakeDriver, tunnels *dbtest.FakeTunnels, profiles ...db.Profile) *db.Registry {
	t.Helper()

	store := db.NewProfileStore(t.TempDir(), dbtest.NewFakeKeychain())
	for _, profile := range profiles {
		if err := store.Save(profile, "s3cret"); err != nil {
			t.Fatalf("saving profile %q: %v", profile.Name, err)
		}
		driver.Accept(profile.Name, "s3cret")
	}
	return db.NewRegistryWithTunnels(driver, tunnels, store)
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
