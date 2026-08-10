package service_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/themethaithian/go-db/internal/db"
	"github.com/themethaithian/go-db/internal/guard"
	"github.com/themethaithian/go-db/internal/service"
)

// These tests run the read path against a real MySQL 8, because the half of the
// Approval Gate that matters most here is the database's own: the read-only
// transaction reads execute inside. A fake cannot prove MySQL refuses a write —
// only MySQL can. They share the container started in
// connection_integration_test.go and skip when Docker is unavailable.

// adminPool opens a plain connection to the shared container, outside the
// facade. Tests need it to set up fixtures and to check afterwards what the
// table really holds: the facade deliberately offers no way to write.
func adminPool(t *testing.T, host string, port int) *sql.DB {
	t.Helper()

	cfg := mysql.NewConfig()
	cfg.Net, cfg.Addr = "tcp", fmt.Sprintf("%s:%d", host, port)
	cfg.User, cfg.Passwd = mysqlRootUser, mysqlRootPassword
	cfg.DBName = mysqlDatabase
	cfg.Timeout = 10 * time.Second

	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		t.Fatalf("admin connection settings: %v", err)
	}
	pool := sql.OpenDB(connector)
	t.Cleanup(func() { pool.Close() })
	return pool
}

// seed creates table and runs the given setup statements, dropping the table
// when the test ends.
func seed(t *testing.T, pool *sql.DB, table string, statements ...string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, err := pool.ExecContext(ctx, "DROP TABLE IF EXISTS "+table); err != nil {
		t.Fatalf("dropping %s: %v", table, err)
	}
	for _, statement := range statements {
		if _, err := pool.ExecContext(ctx, statement); err != nil {
			t.Fatalf("seeding %s with %q: %v", table, statement, err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec("DROP TABLE IF EXISTS " + table)
	})
}

// names reads a table's name column back, so a test can say what the table
// holds without going through the facade.
func names(t *testing.T, pool *sql.DB, table string) []string {
	t.Helper()

	rows, err := pool.Query("SELECT name FROM " + table + " ORDER BY id")
	if err != nil {
		t.Fatalf("reading %s back: %v", table, err)
	}
	defer rows.Close()

	var got []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scanning %s: %v", table, err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("reading %s back: %v", table, err)
	}
	return got
}

// connectedFacade returns a facade over the real MySQL adapter with one
// Profile connected to the shared container.
func connectedFacade(t *testing.T, host string, port int) *service.AppService {
	t.Helper()

	svc := newMySQLFacade(t)
	mustSave(t, svc, db.Profile{
		Name: "local", Host: host, Port: port, User: mysqlRootUser, Database: mysqlDatabase,
	}, mysqlRootPassword)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := svc.Connect(ctx, "local"); err != nil {
		t.Fatalf("Connect(local): %v", err)
	}
	return svc
}

func TestIntegrationRunQueryReturnsRows(t *testing.T) {
	host, port := requireMySQL(t)
	pool := adminPool(t, host, port)
	seed(t, pool, "read_users",
		"CREATE TABLE read_users (id INT PRIMARY KEY, name VARCHAR(32) NOT NULL, nickname VARCHAR(32) NULL, joined DATE NULL)",
		"INSERT INTO read_users VALUES (1,'ada',NULL,'1843-01-01'), (2,'grace','amazing','1959-06-01'), (3,'alan','','1936-05-28')",
	)
	svc := connectedFacade(t, host, port)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	got := svc.RunQuery(ctx, "local", "", "SELECT id, name, nickname, joined FROM read_users ORDER BY id", guard.OriginHuman)

	if got.Status != service.QueryOK {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.QueryOK, got.Message)
	}
	if !equalStrings(got.Columns, []string{"id", "name", "nickname", "joined"}) {
		t.Errorf("columns = %v, want [id name nickname joined]", got.Columns)
	}
	if len(got.Rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(got.Rows))
	}
	if got.Truncated {
		t.Error("a three-row result reports itself truncated")
	}

	for i, want := range []string{"ada", "grace", "alan"} {
		if got.Rows[i][1] == nil || *got.Rows[i][1] != want {
			t.Errorf("row %d name = %v, want %q", i, got.Rows[i][1], want)
		}
	}
	// A SQL NULL and an empty string are different values and must stay so.
	if got.Rows[0][2] != nil {
		t.Errorf("NULL nickname arrived as %q, want nil", *got.Rows[0][2])
	}
	if got.Rows[2][2] == nil || *got.Rows[2][2] != "" {
		t.Errorf("empty nickname arrived as %v, want an empty string", got.Rows[2][2])
	}
	// Values are rendered for display, in the server's own text form.
	if got.Rows[0][0] == nil || *got.Rows[0][0] != "1" {
		t.Errorf("id = %v, want \"1\"", got.Rows[0][0])
	}
	if got.Rows[1][3] == nil || *got.Rows[1][3] != "1959-06-01" {
		t.Errorf("date = %v, want \"1959-06-01\"", got.Rows[1][3])
	}
}

// TestIntegrationReadOnlyTransactionRejectsAWrite is the backstop, proven
// against MySQL rather than a fake. It deliberately bypasses the classifier —
// ReadQuery is handed an UPDATE, which is what a classifier miss looks like
// from the connection's side — and the database must refuse it.
func TestIntegrationReadOnlyTransactionRejectsAWrite(t *testing.T) {
	host, port := requireMySQL(t)
	pool := adminPool(t, host, port)
	seed(t, pool, "backstop_users",
		"CREATE TABLE backstop_users (id INT PRIMARY KEY, name VARCHAR(32) NOT NULL)",
		"INSERT INTO backstop_users VALUES (1,'ada'), (2,'grace')",
	)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := db.NewMySQLDriver().Open(ctx, db.Profile{
		Name: "direct", Host: host, Port: port, User: mysqlRootUser, Database: mysqlDatabase,
	}, mysqlRootPassword, nil)
	if err != nil {
		t.Fatalf("opening a connection: %v", err)
	}
	defer conn.Close()

	writes := []string{
		"UPDATE backstop_users SET name = 'mallory' WHERE id = 1",
		"DELETE FROM backstop_users WHERE id = 2",
		"INSERT INTO backstop_users VALUES (3,'eve')",
	}
	for _, write := range writes {
		t.Run(write, func(t *testing.T) {
			got, err := conn.ReadQuery(ctx, write)

			if !errors.Is(err, db.ErrWriteAttempt) {
				t.Fatalf("ReadQuery(%q) error = %v, want it to wrap db.ErrWriteAttempt", write, err)
			}
			if got.Columns != nil || got.Rows != nil {
				t.Errorf("a refused write returned data: %+v", got)
			}
		})
	}

	if got := names(t, pool, "backstop_users"); !equalStrings(got, []string{"ada", "grace"}) {
		t.Errorf("table holds %v, want [ada grace]: the read-only transaction let a write through", got)
	}

	// The refused transaction must have been rolled back and released, or the
	// connection would be unusable — and the Registry hands the same one out
	// for every later query.
	after, err := conn.ReadQuery(ctx, "SELECT name FROM backstop_users ORDER BY id")
	if err != nil {
		t.Fatalf("reading after a refused write: %v; the transaction was not cleaned up", err)
	}
	if len(after.Rows) != 2 {
		t.Errorf("got %d rows after a refused write, want 2", len(after.Rows))
	}
}

// TestIntegrationBackstopDoesNotCoverDDL pins the boundary of the second layer,
// which is not where you would guess it is.
//
// DDL causes an implicit commit in MySQL: CREATE, DROP, ALTER and TRUNCATE end
// the read-only transaction before they run, and so are never refused by it.
// The backstop covers DML and locking reads; DDL rests on the classifier alone.
// This test exists so that stays a known, tested fact rather than a surprise —
// if a future MySQL closes the gap the test fails and the comments can be
// relaxed, and until then nobody may weaken the classifier believing the
// database is behind it.
func TestIntegrationBackstopDoesNotCoverDDL(t *testing.T) {
	host, port := requireMySQL(t)
	pool := adminPool(t, host, port)
	seed(t, pool, "ddl_probe", "CREATE TABLE ddl_probe (id INT PRIMARY KEY)")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, err := db.NewMySQLDriver().Open(ctx, db.Profile{
		Name: "direct", Host: host, Port: port, User: mysqlRootUser, Database: mysqlDatabase,
	}, mysqlRootPassword, nil)
	if err != nil {
		t.Fatalf("opening a connection: %v", err)
	}
	defer conn.Close()

	// Each of these is DDL the read-only transaction lets through, and which
	// only the classifier stops.
	ddl := []string{
		"ALTER TABLE ddl_probe ADD COLUMN escaped INT",
		"TRUNCATE TABLE ddl_probe",
		"DROP TABLE ddl_probe",
	}
	for _, sql := range ddl {
		if _, err := conn.ReadQuery(ctx, sql); errors.Is(err, db.ErrWriteAttempt) {
			t.Errorf("the read-only transaction refused %q; MySQL has closed the DDL gap, "+
				"and the comments about the backstop's reach can be relaxed", sql)
		}

		// The layer that does stop it must be watertight.
		if got := guard.Classify(sql); got.Kind != guard.Mutation {
			t.Errorf("Classify(%q) = %q; the database does not stop this, so the classifier must", sql, got.Kind)
		}
	}
}

// TestIntegrationRunQueryWithholdsAMutation is the same claim one layer up: an
// UPDATE submitted to the facade is described but never performed. The Impact
// Preview does reach the database — as reads — and the table must be untouched
// afterwards all the same.
func TestIntegrationRunQueryWithholdsAMutation(t *testing.T) {
	host, port := requireMySQL(t)
	pool := adminPool(t, host, port)
	seed(t, pool, "gated_users",
		"CREATE TABLE gated_users (id INT PRIMARY KEY, name VARCHAR(32) NOT NULL)",
		"INSERT INTO gated_users VALUES (1,'ada'), (2,'grace')",
	)
	svc := connectedFacade(t, host, port)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for _, sql := range []string{
		"UPDATE gated_users SET name = 'mallory'",
		"DELETE FROM gated_users",
		"DROP TABLE gated_users",
		"SELECT 1; DROP TABLE gated_users",
	} {
		t.Run(sql, func(t *testing.T) {
			got := svc.RunQuery(ctx, "local", "", sql, guard.OriginHuman)

			if got.Status != service.QueryRequiresConfirmation {
				t.Errorf("status = %q, want %q (message: %s)", got.Status, service.QueryRequiresConfirmation, got.Message)
			}
			if got.Classification.Kind != guard.Mutation {
				t.Errorf("classification = %+v, want a mutation", got.Classification)
			}
			if got.PendingID == "" {
				t.Error("no pending ID; the human has nothing to confirm or cancel")
			}
		})
	}

	if got := names(t, pool, "gated_users"); !equalStrings(got, []string{"ada", "grace"}) {
		t.Errorf("table holds %v, want [ada grace]: a withheld mutation ran", got)
	}

	// Reads on the same Profile still work afterwards.
	read := svc.RunQuery(ctx, "local", "", "SELECT name FROM gated_users ORDER BY id", guard.OriginHuman)
	if read.Status != service.QueryOK {
		t.Fatalf("reading after withheld mutations: status = %q (%s)", read.Status, read.Message)
	}
	if len(read.Rows) != 2 {
		t.Errorf("got %d rows, want 2", len(read.Rows))
	}
}

func TestIntegrationRunQueryTruncatesAtTheRowCap(t *testing.T) {
	host, port := requireMySQL(t)
	pool := adminPool(t, host, port)
	seed(t, pool, "many_rows",
		"CREATE TABLE many_rows (id INT PRIMARY KEY)",
		// A self-cross-joined counter, rather than one deep recursion: MySQL
		// caps CTE recursion at 1000 by default, which is the very number
		// under test.
		fmt.Sprintf(`INSERT INTO many_rows (id)
			WITH RECURSIVE seq AS (SELECT 1 AS n UNION ALL SELECT n+1 FROM seq WHERE n < 100)
			SELECT (a.n-1)*100 + b.n FROM seq a CROSS JOIN seq b
			WHERE (a.n-1)*100 + b.n <= %d`, db.MaxRows+50),
	)
	svc := connectedFacade(t, host, port)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	got := svc.RunQuery(ctx, "local", "", "SELECT id FROM many_rows ORDER BY id", guard.OriginHuman)

	if got.Status != service.QueryOK {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.QueryOK, got.Message)
	}
	if len(got.Rows) != db.MaxRows {
		t.Errorf("got %d rows, want the cap of %d", len(got.Rows), db.MaxRows)
	}
	if !got.Truncated {
		t.Error("Truncated = false, want the cap reported")
	}
	if !strings.Contains(strings.ToLower(got.Message), "truncat") {
		t.Errorf("message = %q, want it to say the result was truncated", got.Message)
	}

	// A result that fits is not marked truncated.
	small := svc.RunQuery(ctx, "local", "", fmt.Sprintf("SELECT id FROM many_rows ORDER BY id LIMIT %d", db.MaxRows), guard.OriginHuman)
	if small.Truncated {
		t.Errorf("a result of exactly the cap (%d rows) reports itself truncated", db.MaxRows)
	}
	if len(small.Rows) != db.MaxRows {
		t.Errorf("got %d rows, want %d", len(small.Rows), db.MaxRows)
	}
}

func TestIntegrationRunQueryReportsASQLError(t *testing.T) {
	host, port := requireMySQL(t)
	svc := connectedFacade(t, host, port)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	got := svc.RunQuery(ctx, "local", "", "SELECT nope FROM no_such_table", guard.OriginHuman)

	if got.Status != service.QueryFailed {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.QueryFailed, got.Message)
	}
	if !strings.Contains(got.Message, "no_such_table") {
		t.Errorf("message = %q, want MySQL's own wording about the missing table", got.Message)
	}
	t.Logf("SQL error surfaced as: %s", got.Message)
}

// altSchema is a throwaway schema beside the Profile's own, created for the
// test and dropped after it. It exists so a test can prove that a selected
// database really is the connection's default schema: the only honest evidence
// is a table that exists in one schema and not in the other.
const altSchema = "godb_integration_alt"

func seedAltSchema(t *testing.T, pool *sql.DB, statements ...string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if _, err := pool.ExecContext(ctx, "DROP DATABASE IF EXISTS "+altSchema); err != nil {
		t.Fatalf("dropping %s: %v", altSchema, err)
	}
	if _, err := pool.ExecContext(ctx, "CREATE DATABASE "+altSchema); err != nil {
		t.Fatalf("creating %s: %v", altSchema, err)
	}
	for _, statement := range statements {
		if _, err := pool.ExecContext(ctx, statement); err != nil {
			t.Fatalf("seeding %s with %q: %v", altSchema, statement, err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec("DROP DATABASE IF EXISTS " + altSchema)
	})
}

// TestIntegrationRunQueryOnASelectedDatabase is the claim issue #32 rests on,
// proven against MySQL rather than a fake: a selected database is the
// connection's real default schema, so an unqualified statement resolves in it.
// The proof is asymmetric on purpose — the same statement must succeed on the
// schema that holds the table and fail on the one that does not.
func TestIntegrationRunQueryOnASelectedDatabase(t *testing.T) {
	host, port := requireMySQL(t)
	pool := adminPool(t, host, port)
	seedAltSchema(t, pool,
		"CREATE TABLE "+altSchema+".alt_only (id INT PRIMARY KEY, name VARCHAR(32) NOT NULL)",
		"INSERT INTO "+altSchema+".alt_only VALUES (1,'elsewhere')",
	)
	svc := connectedFacade(t, host, port)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const sql = "SELECT name FROM alt_only"

	got := svc.RunQuery(ctx, "local", altSchema, sql, guard.OriginHuman)
	if got.Status != service.QueryOK {
		t.Fatalf("status on %s = %q, want %q (message: %s)", altSchema, got.Status, service.QueryOK, got.Message)
	}
	if len(got.Rows) != 1 || got.Rows[0][0] == nil || *got.Rows[0][0] != "elsewhere" {
		t.Errorf("rows = %v, want the one row the other schema holds", got.Rows)
	}

	// The Profile's own connection has no such table, and must say so: if it
	// answered, the schema was never really selected.
	blank := svc.RunQuery(ctx, "local", "", sql, guard.OriginHuman)
	if blank.Status != service.QueryFailed {
		t.Fatalf("status on the Profile's own schema = %q, want %q (message: %s)",
			blank.Status, service.QueryFailed, blank.Message)
	}
	if !strings.Contains(blank.Message, "alt_only") {
		t.Errorf("message = %q, want MySQL's own words about the unknown table", blank.Message)
	}

	// DATABASE() is what the connection itself says its schema is — the thing
	// the design promises, asked of the server directly.
	where := svc.RunQuery(ctx, "local", altSchema, "SELECT DATABASE()", guard.OriginHuman)
	if where.Status != service.QueryOK {
		t.Fatalf("SELECT DATABASE() = %q (message: %s)", where.Status, where.Message)
	}
	if len(where.Rows) != 1 || where.Rows[0][0] == nil || *where.Rows[0][0] != altSchema {
		t.Errorf("DATABASE() = %v, want %q", where.Rows, altSchema)
	}
}

// A disconnected Profile takes its sibling connections with it: reconnecting
// and asking again must open a fresh one rather than hand back a connection
// whose Profile the human has closed.
func TestIntegrationDisconnectClosesTheSelectedDatabasesConnection(t *testing.T) {
	host, port := requireMySQL(t)
	pool := adminPool(t, host, port)
	seedAltSchema(t, pool, "CREATE TABLE "+altSchema+".alt_only (id INT PRIMARY KEY)")
	svc := connectedFacade(t, host, port)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if got := svc.RunQuery(ctx, "local", altSchema, "SELECT id FROM alt_only", guard.OriginHuman); got.Status != service.QueryOK {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.QueryOK, got.Message)
	}
	if err := svc.Disconnect("local"); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	got := svc.RunQuery(ctx, "local", altSchema, "SELECT id FROM alt_only", guard.OriginHuman)
	if got.Status != service.QueryNotConnected {
		t.Fatalf("status after Disconnect = %q, want %q (message: %s)", got.Status, service.QueryNotConnected, got.Message)
	}

	if err := svc.Connect(ctx, "local"); err != nil {
		t.Fatalf("reconnecting: %v", err)
	}
	if got := svc.RunQuery(ctx, "local", altSchema, "SELECT id FROM alt_only", guard.OriginHuman); got.Status != service.QueryOK {
		t.Errorf("status after reconnecting = %q, want %q (message: %s)", got.Status, service.QueryOK, got.Message)
	}
}
