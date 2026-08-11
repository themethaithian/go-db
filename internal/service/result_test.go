package service_test

import (
	"context"
	"testing"

	"github.com/themethaithian/go-db/internal/db"
	"github.com/themethaithian/go-db/internal/db/dbtest"
	"github.com/themethaithian/go-db/internal/guard"
	"github.com/themethaithian/go-db/internal/service"
)

// The facade renders one shape of answer: a table. ADR-0006 makes a read's
// answer a tagged union, so an Engine whose adapter answers in documents or in
// one typed value can reach this package — and when it does, the facade must
// say it cannot render that rather than report success over nothing.
//
// These tests drive that from the only side it can be driven from today: a
// Conn that hands back a Result with no Table arm. The zero Result is the
// stand-in, because it is the one non-Table Result that exists before the
// MongoDB and Redis adapters do; the branch it exercises is the same branch
// every other kind will take, since the facade asks for the Table arm and acts
// on being refused, not on which other arm it was handed.

// untaggedDriver is a db.Driver whose connections read successfully and answer
// with a Result holding no Table arm.
type untaggedDriver struct{}

func (untaggedDriver) Open(context.Context, db.Profile, string, db.DialFunc) (db.Conn, error) {
	return untaggedConn{}, nil
}

type untaggedConn struct{}

func (untaggedConn) Ping(context.Context) error { return nil }

func (untaggedConn) ReadQuery(context.Context, string) (db.Result, error) {
	return db.Result{}, nil
}

func (untaggedConn) Exec(context.Context, string) (int64, error) { return 0, nil }

func (untaggedConn) Close() error { return nil }

var (
	_ db.Driver = untaggedDriver{}
	_ db.Conn   = untaggedConn{}
)

// untaggedFacade is one connected Profile over a driver that answers every read
// with a Result the facade cannot render.
func untaggedFacade(t *testing.T) *service.AppService {
	t.Helper()

	svc := newConnectedFacade(t, dbtest.NewFakeKeychain(), untaggedDriver{})
	mustSave(t, svc, localProfile("local"), "s3cret")
	if err := svc.Connect(context.Background(), "local"); err != nil {
		t.Fatalf("Connect(local): %v", err)
	}
	return svc
}

func TestRunQueryFailsLoudlyOnANonTableResult(t *testing.T) {
	svc := untaggedFacade(t)

	got := svc.RunQuery(context.Background(), "local", "", "SELECT 1", guard.OriginHuman)

	if got.Status != service.QueryFailed {
		t.Fatalf("status = %q, want %q: an answer the facade cannot render is a failure, not a result",
			got.Status, service.QueryFailed)
	}
	if got.Columns != nil || got.Rows != nil {
		t.Errorf("columns = %v, rows = %v, want nothing rendered", got.Columns, got.Rows)
	}
	if got.Message == "" {
		t.Error("message is empty, want it to say the answer's shape could not be rendered")
	}
}

// The schema introspection reads go the same way, through the same unwrapping:
// a tree that quietly shows no tables is worse than one that reports an error.
func TestSchemaReadsFailLoudlyOnANonTableResult(t *testing.T) {
	svc := untaggedFacade(t)
	ctx := context.Background()

	type outcome struct {
		name    string
		status  service.SchemaStatus
		message string
	}
	databases := svc.ListDatabases(ctx, "local")
	tables := svc.ListTables(ctx, "local", "")
	columns := svc.ListColumns(ctx, "local", "", "t")
	indexes := svc.ListIndexes(ctx, "local", "", "t")

	for _, got := range []outcome{
		{"ListDatabases", databases.Status, databases.Message},
		{"ListTables", tables.Status, tables.Message},
		{"ListColumns", columns.Status, columns.Message},
		{"ListIndexes", indexes.Status, indexes.Message},
	} {
		t.Run(got.name, func(t *testing.T) {
			if got.status != service.SchemaFailed {
				t.Errorf("status = %q, want %q", got.status, service.SchemaFailed)
			}
			if got.message == "" {
				t.Error("message is empty, want it to say the answer's shape could not be rendered")
			}
		})
	}
}

// An Impact Preview that cannot read its count is a missing preview, not a
// failed query — the established outcome for every other reason a count will
// not run, and a Result the facade cannot read is one more of them.
func TestPreviewReportsNoPreviewOnANonTableResult(t *testing.T) {
	svc := untaggedFacade(t)

	got := svc.RunQuery(context.Background(), "local", "", "DELETE FROM users WHERE id = 1", guard.OriginHuman)

	if got.Status != service.QueryRequiresConfirmation {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.QueryRequiresConfirmation, got.Message)
	}
	if got.Preview == nil {
		t.Fatal("Preview is nil, want a preview that says there is none")
	}
	if got.Preview.Available {
		t.Errorf("Preview.Available = true over an unreadable count: %+v", got.Preview)
	}
	if got.Preview.Reason == "" {
		t.Error("Preview.Reason is empty, want it to say why the rows could not be counted")
	}
}
