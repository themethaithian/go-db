package service_test

import (
	"context"
	"testing"
	"time"

	"github.com/themethaithian/go-db/internal/service"
)

// These tests run schema introspection against a real MySQL 8, because the
// claim worth proving here is what information_schema actually reports for a
// real table — row estimates, is_nullable, column_key — not just that this
// package parses whatever a fake hands it. They share the container started in
// connection_integration_test.go and skip when Docker is unavailable.

func TestIntegrationListTablesReturnsCreatedTables(t *testing.T) {
	host, port := requireMySQL(t)
	pool := adminPool(t, host, port)
	seed(t, pool, "schema_orders",
		"CREATE TABLE schema_orders (id INT PRIMARY KEY, total DECIMAL(10,2) NOT NULL)",
		"INSERT INTO schema_orders VALUES (1, 9.99), (2, 19.99)",
	)
	seed(t, pool, "schema_customers",
		"CREATE TABLE schema_customers (id INT PRIMARY KEY, email VARCHAR(255) NOT NULL)",
		"INSERT INTO schema_customers VALUES (1, 'ada@example.com')",
	)
	svc := connectedFacade(t, host, port)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	got := svc.ListTables(ctx, "local")

	if got.Status != service.SchemaOK {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.SchemaOK, got.Message)
	}

	byName := make(map[string]service.TableInfo, len(got.Tables))
	for _, table := range got.Tables {
		byName[table.Name] = table
	}
	for _, want := range []string{"schema_customers", "schema_orders"} {
		table, ok := byName[want]
		if !ok {
			t.Errorf("ListTables did not report %q; got %+v", want, got.Tables)
			continue
		}
		// It is an estimate — InnoDB's statistics sampling, not a COUNT(*) —
		// so the only thing worth pinning is that it is a real, non-negative
		// number rather than absent.
		if table.RowEstimate == nil {
			t.Errorf("%q: RowEstimate is nil, want an estimate for a base table", want)
		} else if *table.RowEstimate < 0 {
			t.Errorf("%q: RowEstimate = %d, want >= 0", want, *table.RowEstimate)
		}
	}

	// Ordered by name: schema_customers before schema_orders.
	var positions []string
	for _, table := range got.Tables {
		if table.Name == "schema_customers" || table.Name == "schema_orders" {
			positions = append(positions, table.Name)
		}
	}
	if len(positions) != 2 || positions[0] != "schema_customers" || positions[1] != "schema_orders" {
		t.Errorf("relative order = %v, want [schema_customers schema_orders]", positions)
	}
}

func TestIntegrationListColumnsReturnsColumnMetadata(t *testing.T) {
	host, port := requireMySQL(t)
	pool := adminPool(t, host, port)
	seed(t, pool, "schema_articles",
		`CREATE TABLE schema_articles (
			id INT PRIMARY KEY,
			slug VARCHAR(64) NOT NULL UNIQUE,
			author_id INT NULL,
			body TEXT NULL,
			INDEX idx_author (author_id)
		)`,
	)
	svc := connectedFacade(t, host, port)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	got := svc.ListColumns(ctx, "local", "schema_articles")

	if got.Status != service.SchemaOK {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.SchemaOK, got.Message)
	}

	want := []service.ColumnInfo{
		{Name: "id", DataType: "int", Nullable: false, Key: "PRI"},
		{Name: "slug", DataType: "varchar(64)", Nullable: false, Key: "UNI"},
		{Name: "author_id", DataType: "int", Nullable: true, Key: "MUL"},
		{Name: "body", DataType: "text", Nullable: true, Key: ""},
	}
	if len(got.Columns) != len(want) {
		t.Fatalf("got %d columns, want %d: %+v", len(got.Columns), len(want), got.Columns)
	}
	for i, w := range want {
		if got.Columns[i] != w {
			t.Errorf("Columns[%d] = %+v, want %+v (ordinal_position order)", i, got.Columns[i], w)
		}
	}
}

// TestIntegrationListColumnsEscapesAQuotedTableName proves the escaping holds
// against real MySQL, not just the fake: MySQL allows a single quote inside a
// backtick-quoted identifier, and the WHERE clause built for it must survive
// that name without the query breaking or matching the wrong table.
//
// It sets up its own fixture rather than using the shared seed helper: seed's
// DROP statement splices the table name in unquoted, which a quote in the name
// would break.
func TestIntegrationListColumnsEscapesAQuotedTableName(t *testing.T) {
	host, port := requireMySQL(t)
	pool := adminPool(t, host, port)

	const table = "schema_o'brien"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := pool.ExecContext(ctx, "DROP TABLE IF EXISTS `"+table+"`"); err != nil {
		t.Fatalf("dropping %s: %v", table, err)
	}
	if _, err := pool.ExecContext(ctx, "CREATE TABLE `"+table+"` (id INT PRIMARY KEY)"); err != nil {
		t.Fatalf("creating %s: %v", table, err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec("DROP TABLE IF EXISTS `" + table + "`")
	})

	svc := connectedFacade(t, host, port)

	got := svc.ListColumns(ctx, "local", "schema_o'brien")

	if got.Status != service.SchemaOK {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.SchemaOK, got.Message)
	}
	if len(got.Columns) != 1 || got.Columns[0].Name != "id" {
		t.Errorf("Columns = %+v, want exactly the one column of schema_o'brien", got.Columns)
	}
}

func TestIntegrationListTablesOnAProfileThatIsNotConnected(t *testing.T) {
	svc := newMySQLFacade(t)

	got := svc.ListTables(context.Background(), "never-connected")

	if got.Status != service.SchemaNotConnected {
		t.Errorf("status = %q, want %q (message: %s)", got.Status, service.SchemaNotConnected, got.Message)
	}
}
