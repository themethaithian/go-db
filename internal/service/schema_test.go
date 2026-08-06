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

// These tests state the schema introspection path the Database tree calls:
// what the facade asks information_schema, and how the scripted answer
// round-trips into TableInfo and ColumnInfo. Escaping and not-connected
// behaviour are specified here; the tree itself is a UI concern outside this
// package.

func TestListTablesReturnsTablesOrderedByName(t *testing.T) {
	driver := dbtest.NewFakeDriver()
	driver.Answer("local", db.ResultSet{
		Columns: []string{"table_name", "table_rows"},
		Rows: [][]*string{
			{str("orders"), str("42")},
			// A view: information_schema reports no row estimate for one.
			{str("recent_orders"), nil},
		},
	})
	svc := newQueryFacade(t, driver)

	got := svc.ListTables(context.Background(), "local")

	if got.Status != service.SchemaOK {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.SchemaOK, got.Message)
	}
	if len(got.Tables) != 2 {
		t.Fatalf("got %d tables, want 2", len(got.Tables))
	}
	if got.Tables[0].Name != "orders" {
		t.Errorf("Tables[0].Name = %q, want %q", got.Tables[0].Name, "orders")
	}
	if got.Tables[0].RowEstimate == nil || *got.Tables[0].RowEstimate != 42 {
		t.Errorf("Tables[0].RowEstimate = %v, want 42", got.Tables[0].RowEstimate)
	}
	if got.Tables[1].Name != "recent_orders" {
		t.Errorf("Tables[1].Name = %q, want %q", got.Tables[1].Name, "recent_orders")
	}
	if got.Tables[1].RowEstimate != nil {
		t.Errorf("Tables[1].RowEstimate = %v, want nil for a NULL estimate", *got.Tables[1].RowEstimate)
	}

	// The query must ask only about the connected schema, never a name a
	// caller could supply.
	queries := driver.Queries("local")
	if len(queries) != 1 {
		t.Fatalf("got %d queries, want 1", len(queries))
	}
	if !strings.Contains(queries[0], "information_schema.tables") ||
		!strings.Contains(queries[0], "table_schema = DATABASE()") ||
		!strings.Contains(queries[0], "ORDER BY table_name") {
		t.Errorf("query = %q, want it to read information_schema.tables for DATABASE(), ordered by name", queries[0])
	}
}

func TestListTablesOnAProfileThatIsNotConnected(t *testing.T) {
	driver := dbtest.NewFakeDriver()
	svc := newConnectedFacade(t, dbtest.NewFakeKeychain(), driver)
	mustSave(t, svc, localProfile("local"), "s3cret")

	got := svc.ListTables(context.Background(), "local")

	if got.Status != service.SchemaNotConnected {
		t.Errorf("status = %q, want %q (message: %s)", got.Status, service.SchemaNotConnected, got.Message)
	}
	if !strings.Contains(got.Message, "local") {
		t.Errorf("message %q does not name the Profile", got.Message)
	}
	if got.Tables != nil {
		t.Errorf("Tables = %v, want nil when not connected", got.Tables)
	}
}

func TestListTablesReportsADatabaseFailure(t *testing.T) {
	driver := dbtest.NewFakeDriver()
	driver.FailQuery("local", errors.New("db: Unknown database 'app'"))
	svc := newQueryFacade(t, driver)

	got := svc.ListTables(context.Background(), "local")

	if got.Status != service.SchemaFailed {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.SchemaFailed, got.Message)
	}
	if !strings.Contains(got.Message, "Unknown database") {
		t.Errorf("message = %q, want the database's own wording kept", got.Message)
	}
	if strings.Contains(got.Message, "db: ") {
		t.Errorf("message = %q, want the package prefix stripped", got.Message)
	}
}

func TestListColumnsParsesNullableAndKey(t *testing.T) {
	driver := dbtest.NewFakeDriver()
	driver.Answer("local", db.ResultSet{
		Columns: []string{"column_name", "column_type", "is_nullable", "column_key"},
		Rows: [][]*string{
			{str("id"), str("int"), str("NO"), str("PRI")},
			{str("email"), str("varchar(255)"), str("NO"), str("UNI")},
			{str("author_id"), str("int"), str("YES"), str("MUL")},
			{str("bio"), str("text"), str("YES"), str("")},
		},
	})
	svc := newQueryFacade(t, driver)

	got := svc.ListColumns(context.Background(), "local", "users")

	if got.Status != service.SchemaOK {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.SchemaOK, got.Message)
	}
	if len(got.Columns) != 4 {
		t.Fatalf("got %d columns, want 4", len(got.Columns))
	}

	want := []service.ColumnInfo{
		{Name: "id", DataType: "int", Nullable: false, Key: "PRI"},
		{Name: "email", DataType: "varchar(255)", Nullable: false, Key: "UNI"},
		{Name: "author_id", DataType: "int", Nullable: true, Key: "MUL"},
		{Name: "bio", DataType: "text", Nullable: true, Key: ""},
	}
	for i, w := range want {
		if got.Columns[i] != w {
			t.Errorf("Columns[%d] = %+v, want %+v", i, got.Columns[i], w)
		}
	}

	queries := driver.Queries("local")
	if len(queries) != 1 {
		t.Fatalf("got %d queries, want 1", len(queries))
	}
	if !strings.Contains(queries[0], "information_schema.columns") ||
		!strings.Contains(queries[0], "table_schema = DATABASE()") ||
		!strings.Contains(queries[0], "ORDER BY ordinal_position") {
		t.Errorf("query = %q, want it to read information_schema.columns for DATABASE(), ordered by position", queries[0])
	}
}

// TestListColumnsEscapesTheTableName is the requirement that a table name
// never reaches the database by string concatenation: a name carrying a
// single quote must arrive as a doubled-quote literal, not break out of the
// WHERE clause.
func TestListColumnsEscapesTheTableName(t *testing.T) {
	driver := dbtest.NewFakeDriver()
	driver.Answer("local", db.ResultSet{
		Columns: []string{"column_name", "column_type", "is_nullable", "column_key"},
		Rows:    [][]*string{{str("id"), str("int"), str("NO"), str("PRI")}},
	})
	svc := newQueryFacade(t, driver)

	got := svc.ListColumns(context.Background(), "local", "o'brien")

	if got.Status != service.SchemaOK {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.SchemaOK, got.Message)
	}

	const want = "SELECT column_name, column_type, is_nullable, column_key FROM information_schema.columns " +
		"WHERE table_schema = DATABASE() AND table_name = 'o''brien' ORDER BY ordinal_position"
	queries := driver.Queries("local")
	if len(queries) != 1 {
		t.Fatalf("got %d queries, want 1", len(queries))
	}
	if queries[0] != want {
		t.Errorf("query = %q, want exactly %q", queries[0], want)
	}
}

func TestListColumnsOfAnUnknownTableIsEmptyNotAnError(t *testing.T) {
	driver := dbtest.NewFakeDriver()
	driver.Answer("local", db.ResultSet{
		Columns: []string{"column_name", "column_type", "is_nullable", "column_key"},
		Rows:    nil,
	})
	svc := newQueryFacade(t, driver)

	got := svc.ListColumns(context.Background(), "local", "does_not_exist")

	if got.Status != service.SchemaOK {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.SchemaOK, got.Message)
	}
	if len(got.Columns) != 0 {
		t.Errorf("Columns = %v, want empty for an unknown table", got.Columns)
	}
}

func TestListColumnsOnAProfileThatIsNotConnected(t *testing.T) {
	driver := dbtest.NewFakeDriver()
	svc := newConnectedFacade(t, dbtest.NewFakeKeychain(), driver)
	mustSave(t, svc, localProfile("local"), "s3cret")

	got := svc.ListColumns(context.Background(), "local", "users")

	if got.Status != service.SchemaNotConnected {
		t.Errorf("status = %q, want %q (message: %s)", got.Status, service.SchemaNotConnected, got.Message)
	}
	if !strings.Contains(got.Message, "local") {
		t.Errorf("message %q does not name the Profile", got.Message)
	}
	if got.Columns != nil {
		t.Errorf("Columns = %v, want nil when not connected", got.Columns)
	}
}

func TestListColumnsReportsADatabaseFailure(t *testing.T) {
	driver := dbtest.NewFakeDriver()
	driver.FailQuery("local", errors.New("db: Unknown database 'app'"))
	svc := newQueryFacade(t, driver)

	got := svc.ListColumns(context.Background(), "local", "users")

	if got.Status != service.SchemaFailed {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.SchemaFailed, got.Message)
	}
	if !strings.Contains(got.Message, "Unknown database") {
		t.Errorf("message = %q, want the database's own wording kept", got.Message)
	}
}
