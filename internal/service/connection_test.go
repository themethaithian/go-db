package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/themethaithian/go-db/internal/db"
	"github.com/themethaithian/go-db/internal/db/dbtest"
	"github.com/themethaithian/go-db/internal/service"
)

// newConnectedFacade builds an App Service whose Connection Registry opens
// connections through driver rather than a real MySQL server.
func newConnectedFacade(t *testing.T, keychain db.Keychain, driver db.Driver) *service.AppService {
	t.Helper()
	return service.NewWithDriver(db.NewProfileStore(t.TempDir(), keychain), driver)
}

// localProfile is the Profile the fake driver's scripted outcomes are keyed by.
func localProfile(name string) db.Profile {
	return db.Profile{Name: name, Host: "db.internal", Port: 3306, User: "root", Database: "app"}
}

func TestTestConnectionOutcomes(t *testing.T) {
	cases := []struct {
		name string
		// script prepares the fake driver for the "local" Profile.
		script      func(d *dbtest.FakeDriver)
		probe       string
		wantStatus  service.ConnectionStatus
		wantMessage []string // substrings the message must contain
	}{
		{
			name:        "correct credentials succeed",
			script:      func(d *dbtest.FakeDriver) { d.Accept("local", "s3cret") },
			probe:       "local",
			wantStatus:  service.ConnectionOK,
			wantMessage: []string{"db.internal:3306", "root"},
		},
		{
			name:        "wrong password is an authentication failure",
			script:      func(d *dbtest.FakeDriver) { d.Accept("local", "a-different-password") },
			probe:       "local",
			wantStatus:  service.ConnectionAuthFailed,
			wantMessage: []string{"root", "db.internal:3306"},
		},
		{
			name:        "nothing listening is unreachable",
			script:      func(d *dbtest.FakeDriver) {},
			probe:       "local",
			wantStatus:  service.ConnectionUnreachable,
			wantMessage: []string{"db.internal:3306"},
		},
		{
			name:        "any other failure carries its own message",
			script:      func(d *dbtest.FakeDriver) { d.Fail("local", errors.New("unknown database \"app\"")) },
			probe:       "local",
			wantStatus:  service.ConnectionFailed,
			wantMessage: []string{"unknown database"},
		},
		{
			name:        "an unknown profile is reported as such",
			script:      func(d *dbtest.FakeDriver) { d.Accept("local", "s3cret") },
			probe:       "nope",
			wantStatus:  service.ConnectionUnknownProfile,
			wantMessage: []string{"nope"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			driver := dbtest.NewFakeDriver()
			tc.script(driver)
			svc := newConnectedFacade(t, dbtest.NewFakeKeychain(), driver)
			mustSave(t, svc, localProfile("local"), "s3cret")

			got := svc.TestConnection(context.Background(), tc.probe)

			if got.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q (message: %s)", got.Status, tc.wantStatus, got.Message)
			}
			if got.Message == "" {
				t.Fatal("outcome message is empty")
			}
			if strings.Contains(got.Message, "\n") {
				t.Errorf("outcome message spans lines, want one readable line:\n%s", got.Message)
			}
			for _, want := range tc.wantMessage {
				if !strings.Contains(got.Message, want) {
					t.Errorf("message %q does not mention %q", got.Message, want)
				}
			}
			if strings.Contains(got.Message, "s3cret") {
				t.Errorf("message leaks the password: %q", got.Message)
			}
		})
	}
}

func TestTestConnectionLeavesNothingOpen(t *testing.T) {
	driver := dbtest.NewFakeDriver()
	driver.Accept("local", "s3cret")
	svc := newConnectedFacade(t, dbtest.NewFakeKeychain(), driver)
	mustSave(t, svc, localProfile("local"), "s3cret")

	if got := svc.TestConnection(context.Background(), "local"); got.Status != service.ConnectionOK {
		t.Fatalf("TestConnection = %+v, want ok", got)
	}

	if open := driver.OpenProfiles(); len(open) != 0 {
		t.Errorf("test connection left %v open, want nothing", open)
	}
	if got := svc.ConnectedProfiles(); len(got) != 0 {
		t.Errorf("ConnectedProfiles = %v, want empty", got)
	}
}

func TestRegistryHoldsConnectionsForSeveralProfiles(t *testing.T) {
	driver := dbtest.NewFakeDriver()
	driver.Accept("editor", "s3cret")
	driver.Accept("agent", "s3cret")
	svc := newConnectedFacade(t, dbtest.NewFakeKeychain(), driver)
	mustSave(t, svc, localProfile("editor"), "s3cret")
	mustSave(t, svc, localProfile("agent"), "s3cret")

	ctx := context.Background()
	for _, name := range []string{"editor", "agent"} {
		if err := svc.Connect(ctx, name); err != nil {
			t.Fatalf("Connect(%q): %v", name, err)
		}
	}

	if got := svc.ConnectedProfiles(); !equalStrings(got, []string{"agent", "editor"}) {
		t.Fatalf("ConnectedProfiles = %v, want [agent editor]", got)
	}
	if got := driver.OpenProfiles(); !equalStrings(got, []string{"agent", "editor"}) {
		t.Fatalf("driver holds %v open, want [agent editor]", got)
	}
	for _, name := range []string{"editor", "agent"} {
		if err := svc.Ping(ctx, name); err != nil {
			t.Errorf("Ping(%q) on a simultaneously held connection: %v", name, err)
		}
	}
}

func TestDisconnectClosesOnlyTheNamedConnection(t *testing.T) {
	driver := dbtest.NewFakeDriver()
	driver.Accept("editor", "s3cret")
	driver.Accept("agent", "s3cret")
	svc := newConnectedFacade(t, dbtest.NewFakeKeychain(), driver)
	mustSave(t, svc, localProfile("editor"), "s3cret")
	mustSave(t, svc, localProfile("agent"), "s3cret")

	ctx := context.Background()
	for _, name := range []string{"editor", "agent"} {
		if err := svc.Connect(ctx, name); err != nil {
			t.Fatalf("Connect(%q): %v", name, err)
		}
	}

	if err := svc.Disconnect("editor"); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	if got := svc.ConnectedProfiles(); !equalStrings(got, []string{"agent"}) {
		t.Errorf("ConnectedProfiles = %v, want [agent]", got)
	}
	if got := driver.OpenProfiles(); !equalStrings(got, []string{"agent"}) {
		t.Errorf("driver holds %v open, want [agent]", got)
	}
	if err := svc.Ping(ctx, "agent"); err != nil {
		t.Errorf("Ping(agent) after disconnecting editor: %v", err)
	}
	if err := svc.Ping(ctx, "editor"); err == nil {
		t.Error("Ping(editor) after disconnect succeeded, want an error")
	}
}

func TestConnectFailures(t *testing.T) {
	cases := []struct {
		name    string
		script  func(d *dbtest.FakeDriver)
		connect string
		wantErr error
	}{
		{
			name:    "wrong password",
			script:  func(d *dbtest.FakeDriver) { d.Accept("local", "a-different-password") },
			connect: "local",
			wantErr: db.ErrAuthFailed,
		},
		{
			name:    "nothing listening",
			script:  func(d *dbtest.FakeDriver) {},
			connect: "local",
			wantErr: db.ErrUnreachable,
		},
		{
			name:    "unknown profile",
			script:  func(d *dbtest.FakeDriver) { d.Accept("local", "s3cret") },
			connect: "nope",
			wantErr: db.ErrProfileNotFound,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			driver := dbtest.NewFakeDriver()
			tc.script(driver)
			svc := newConnectedFacade(t, dbtest.NewFakeKeychain(), driver)
			mustSave(t, svc, localProfile("local"), "s3cret")

			err := svc.Connect(context.Background(), tc.connect)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Connect error = %v, want %v", err, tc.wantErr)
			}
			if got := svc.ConnectedProfiles(); len(got) != 0 {
				t.Errorf("ConnectedProfiles after a failed connect = %v, want empty", got)
			}
			if open := driver.OpenProfiles(); len(open) != 0 {
				t.Errorf("failed connect left %v open, want nothing", open)
			}
		})
	}
}

func TestConnectTwiceKeepsOneConnection(t *testing.T) {
	driver := dbtest.NewFakeDriver()
	driver.Accept("local", "s3cret")
	svc := newConnectedFacade(t, dbtest.NewFakeKeychain(), driver)
	mustSave(t, svc, localProfile("local"), "s3cret")

	ctx := context.Background()
	for range 2 {
		if err := svc.Connect(ctx, "local"); err != nil {
			t.Fatalf("Connect: %v", err)
		}
	}

	if got := driver.Opens(); got != 1 {
		t.Errorf("driver opened %d connections, want 1: reconnecting an already-connected Profile must be a no-op", got)
	}
	if got := svc.ConnectedProfiles(); !equalStrings(got, []string{"local"}) {
		t.Errorf("ConnectedProfiles = %v, want [local]", got)
	}
}

func TestDisconnectUnknownProfile(t *testing.T) {
	svc := newConnectedFacade(t, dbtest.NewFakeKeychain(), dbtest.NewFakeDriver())

	if err := svc.Disconnect("never-connected"); err == nil {
		t.Error("Disconnect on a Profile that is not connected succeeded, want an error")
	}
}

func TestCloseClosesEveryConnection(t *testing.T) {
	driver := dbtest.NewFakeDriver()
	driver.Accept("editor", "s3cret")
	driver.Accept("agent", "s3cret")
	svc := newConnectedFacade(t, dbtest.NewFakeKeychain(), driver)
	mustSave(t, svc, localProfile("editor"), "s3cret")
	mustSave(t, svc, localProfile("agent"), "s3cret")

	ctx := context.Background()
	for _, name := range []string{"editor", "agent"} {
		if err := svc.Connect(ctx, name); err != nil {
			t.Fatalf("Connect(%q): %v", name, err)
		}
	}

	if err := svc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if open := driver.OpenProfiles(); len(open) != 0 {
		t.Errorf("Close left %v open, want nothing", open)
	}
	if got := svc.ConnectedProfiles(); len(got) != 0 {
		t.Errorf("ConnectedProfiles after Close = %v, want empty", got)
	}
}

func TestConnectUsesTheStoredSecret(t *testing.T) {
	driver := dbtest.NewFakeDriver()
	driver.Accept("local", "rotated-secret")
	keychain := dbtest.NewFakeKeychain()
	svc := newConnectedFacade(t, keychain, driver)

	mustSave(t, svc, localProfile("local"), "original-secret")
	if err := svc.Connect(context.Background(), "local"); !errors.Is(err, db.ErrAuthFailed) {
		t.Fatalf("Connect with the stale secret = %v, want ErrAuthFailed", err)
	}

	// Rotating the secret through the facade must be what the next connect uses.
	mustSave(t, svc, localProfile("local"), "rotated-secret")
	if err := svc.Connect(context.Background(), "local"); err != nil {
		t.Fatalf("Connect after rotating the secret: %v", err)
	}
}
