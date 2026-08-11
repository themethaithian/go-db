package service_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/themethaithian/go-db/internal/db"
	"github.com/themethaithian/go-db/internal/db/dbtest"
	"github.com/themethaithian/go-db/internal/guard"
	"github.com/themethaithian/go-db/internal/service"
)

// These tests state what the Database tree gets when the Profile it is
// expanding is not MySQL: a Redis Profile's keys, a MongoDB Profile's
// collections, and the single database each of those has.
//
// The claim under test is that introspection is just reads. Every answer here
// is produced by a statement going through db.Conn.ReadQuery — SCAN and
// db.getCollectionNames() — which is why the fakes below record what they were
// asked and every test says so: a schema call that reached the server by some
// private path would be one the Approval Gate never saw and the audit log never
// recorded.

// scriptedDriver is a db.Driver whose reads are answered by a function of the
// command, so a test can script a SCAN that pages. It is engine_test.go's
// valueDriver with the one reply replaced by a script — the same substitution,
// one degree of freedom further.
type scriptedDriver struct {
	mu     sync.Mutex
	answer func(command string) (db.Result, error)
	reads  []string
}

func (d *scriptedDriver) Open(context.Context, db.Profile, string, db.DialFunc) (db.Conn, error) {
	return &scriptedConn{driver: d}, nil
}

func (d *scriptedDriver) Reads() []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]string(nil), d.reads...)
}

type scriptedConn struct{ driver *scriptedDriver }

func (scriptedConn) Ping(context.Context) error { return nil }

func (c *scriptedConn) ReadQuery(_ context.Context, command string) (db.Result, error) {
	c.driver.mu.Lock()
	c.driver.reads = append(c.driver.reads, command)
	answer := c.driver.answer
	c.driver.mu.Unlock()

	return answer(command)
}

func (c *scriptedConn) Exec(_ context.Context, _ string) (int64, error) { return 0, nil }

func (scriptedConn) Close() error { return nil }

var (
	_ db.Driver = (*scriptedDriver)(nil)
	_ db.Conn   = (*scriptedConn)(nil)
)

// newScriptedFacade builds an App Service with one connected Profile named
// "store" on engine, whose every read is answered by answer. database is the
// Profile's own Database field, which is what the databases level reports for
// these Engines.
func newScriptedFacade(
	t *testing.T,
	engine db.Engine,
	database string,
	answer func(command string) (db.Result, error),
) (*service.AppService, *scriptedDriver) {
	t.Helper()

	driver := &scriptedDriver{answer: answer}
	svc := service.NewWithDrivers(
		db.NewProfileStore(t.TempDir(), dbtest.NewFakeKeychain()),
		db.Drivers{engine: driver},
		guard.NewJSONLAuditLog(t.TempDir()), nil,
	)
	mustSave(t, svc, db.Profile{
		Name: "store", Host: "store.internal", User: "app", Database: database, Engine: engine,
	}, "")
	if err := svc.Connect(context.Background(), "store"); err != nil {
		t.Fatalf("Connect(store): %v", err)
	}
	return svc, driver
}

// scanPage builds the reply a real SCAN gives: a two-element array holding the
// next cursor and the keys of this page.
func scanPage(cursor string, keys ...string) db.Result {
	items := make([]db.Reply, 0, len(keys))
	for _, key := range keys {
		items = append(items, db.Reply{Kind: db.ReplyString, Text: key})
	}
	return db.ValueResult(db.Reply{Kind: db.ReplyArray, Items: []db.Reply{
		{Kind: db.ReplyString, Text: cursor},
		{Kind: db.ReplyArray, Items: items},
	}})
}

// collectionsAnswer builds the reply db.getCollectionNames() gives: the
// Documents arm holding the one document the adapter renders the names as.
func collectionsAnswer(truncated bool, names ...string) db.Result {
	quoted := make([]string, 0, len(names))
	for _, name := range names {
		quoted = append(quoted, strconv.Quote(name))
	}
	document := json.RawMessage(`{"collections":[` + strings.Join(quoted, ",") + `]}`)
	return db.DocumentsResult([]json.RawMessage{document}, truncated)
}

func tableNames(tables []service.TableInfo) []string {
	names := make([]string, 0, len(tables))
	for _, table := range tables {
		names = append(names, table.Name)
	}
	return names
}

// A Redis Profile's "tables" are the keys in the database it is connected to,
// found by paging SCAN through the same guarded read path everything else uses.
func TestListTablesScansARedisKeyspace(t *testing.T) {
	svc, driver := newScriptedFacade(t, db.EngineRedis, "", func(string) (db.Result, error) {
		return scanPage("0", "user:2", "user:1", "session:9"), nil
	})

	got := svc.ListTables(context.Background(), "store", "0")

	if got.Status != service.SchemaOK {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.SchemaOK, got.Message)
	}
	if names := tableNames(got.Tables); !equalStrings(names, []string{"session:9", "user:1", "user:2"}) {
		t.Errorf("keys = %v, want them sorted by name", names)
	}
	if got.Truncated {
		t.Error("Truncated = true on a keyspace that was scanned whole")
	}
	for _, table := range got.Tables {
		if table.RowEstimate != nil {
			t.Errorf("key %q carries a row estimate of %d; a key has no rows to estimate", table.Name, *table.RowEstimate)
		}
	}
	if !strings.Contains(got.Message, "keys") {
		t.Errorf("message = %q, want it counted in keys", got.Message)
	}

	// The whole point of the design: this went through ReadQuery as SCAN, which
	// the classifier allows and the adapter re-checks.
	reads := driver.Reads()
	if len(reads) != 1 || reads[0] != "SCAN 0 COUNT 1000" {
		t.Errorf("the database was asked %v, want one SCAN from cursor 0", reads)
	}
}

// SCAN pages, and the cursor of one page is the argument of the next. Redis
// also promises nothing about duplicates across pages, so a key seen twice is
// listed once.
func TestListTablesPagesTheRedisScan(t *testing.T) {
	svc, driver := newScriptedFacade(t, db.EngineRedis, "", func(command string) (db.Result, error) {
		switch command {
		case "SCAN 0 COUNT 1000":
			return scanPage("17", "a", "b"), nil
		case "SCAN 17 COUNT 1000":
			return scanPage("0", "b", "c"), nil
		}
		return db.Result{}, fmt.Errorf("unscripted command %q", command)
	})

	got := svc.ListTables(context.Background(), "store", "0")

	if got.Status != service.SchemaOK {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.SchemaOK, got.Message)
	}
	if names := tableNames(got.Tables); !equalStrings(names, []string{"a", "b", "c"}) {
		t.Errorf("keys = %v, want a, b and c once each", names)
	}
	if got.Truncated {
		t.Error("Truncated = true on a scan that reached cursor 0")
	}
	if reads := driver.Reads(); len(reads) != 2 {
		t.Errorf("the database was asked %v, want one SCAN per page", reads)
	}
}

// A keyspace bigger than the tree will show is cut, and says so. The cap is
// what keeps expanding a Profile from pulling a million keys into a tool
// window — the performance budget is a feature.
func TestListTablesCapsTheRedisKeyspace(t *testing.T) {
	page := 0
	svc, driver := newScriptedFacade(t, db.EngineRedis, "", func(string) (db.Result, error) {
		keys := make([]string, 0, 400)
		for i := 0; i < 400; i++ {
			keys = append(keys, fmt.Sprintf("k%06d", page*400+i))
		}
		page++
		// The cursor never comes back to 0: there is always more.
		return scanPage(fmt.Sprint(page), keys...), nil
	})

	got := svc.ListTables(context.Background(), "store", "0")

	if got.Status != service.SchemaOK {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.SchemaOK, got.Message)
	}
	if len(got.Tables) != 1000 {
		t.Errorf("keys = %d, want the cap of 1000", len(got.Tables))
	}
	if !got.Truncated {
		t.Error("Truncated = false on a keyspace the scan never reached the end of")
	}
	if !strings.Contains(got.Message, "first") {
		t.Errorf("message = %q, want it to say the list was cut", got.Message)
	}
	if reads := driver.Reads(); len(reads) > 5 {
		t.Errorf("the database was asked %d times, want the scan to stop at the cap", len(reads))
	}
}

// A reply that is not a SCAN reply is reported as a failure with a line a human
// can read — never as an empty keyspace, which is a real answer a server can
// give and would be a lie here.
func TestListTablesReportsAMalformedScanReply(t *testing.T) {
	cases := []struct {
		name  string
		reply db.Result
	}{
		{"not an array", db.ValueResult(db.Reply{Kind: db.ReplyString, Text: "OK"})},
		{
			"an array of the wrong length",
			db.ValueResult(db.Reply{Kind: db.ReplyArray, Items: []db.Reply{{Kind: db.ReplyString, Text: "0"}}}),
		},
		{
			"a cursor that is not a number",
			db.ValueResult(db.Reply{Kind: db.ReplyArray, Items: []db.Reply{
				{Kind: db.ReplyString, Text: "0; FLUSHALL"},
				{Kind: db.ReplyArray},
			}}),
		},
		{
			"keys that are not strings",
			db.ValueResult(db.Reply{Kind: db.ReplyArray, Items: []db.Reply{
				{Kind: db.ReplyString, Text: "0"},
				{Kind: db.ReplyArray, Items: []db.Reply{{Kind: db.ReplyInteger, Integer: 1}}},
			}}),
		},
		{
			"a table, from an adapter that answered the wrong arm",
			db.TableResult(db.ResultSet{Columns: []string{"key"}}),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newScriptedFacade(t, db.EngineRedis, "", func(string) (db.Result, error) {
				return tc.reply, nil
			})

			got := svc.ListTables(context.Background(), "store", "0")

			if got.Status != service.SchemaFailed {
				t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.SchemaFailed, got.Message)
			}
			if got.Tables != nil {
				t.Errorf("Tables = %v, want none when the reply could not be read", got.Tables)
			}
			if got.Message == "" {
				t.Error("message is empty, want a line saying what came back instead")
			}
		})
	}
}

// The Redis databases level is the one index the Profile is connected on. It
// asks the server nothing: which index a connection is on is a fact about the
// Profile, and SELECT — the command that would move it — is refused by the
// classifier because the editor shares that connection.
func TestListDatabasesForRedisIsTheConnectedIndex(t *testing.T) {
	for _, tc := range []struct{ configured, want string }{
		{"", "0"},
		{"3", "3"},
	} {
		t.Run("database "+tc.configured, func(t *testing.T) {
			svc, driver := newScriptedFacade(t, db.EngineRedis, tc.configured, func(string) (db.Result, error) {
				return db.Result{}, fmt.Errorf("nothing should have been asked")
			})

			got := svc.ListDatabases(context.Background(), "store")

			if got.Status != service.SchemaOK {
				t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.SchemaOK, got.Message)
			}
			if !equalStrings(got.Databases, []string{tc.want}) {
				t.Errorf("databases = %v, want just %q", got.Databases, tc.want)
			}
			if reads := driver.Reads(); len(reads) != 0 {
				t.Errorf("the database was asked %v, want nothing asked at all", reads)
			}
		})
	}
}

// A MongoDB Profile's databases level is the database it names, for the same
// reason: the grammar's leading db is that database, and there is no statement
// in it that can reach another.
func TestListDatabasesForMongoIsTheProfilesOwn(t *testing.T) {
	svc, driver := newScriptedFacade(t, db.EngineMongoDB, "shop", func(string) (db.Result, error) {
		return db.Result{}, fmt.Errorf("nothing should have been asked")
	})

	got := svc.ListDatabases(context.Background(), "store")

	if got.Status != service.SchemaOK {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.SchemaOK, got.Message)
	}
	if !equalStrings(got.Databases, []string{"shop"}) {
		t.Errorf("databases = %v, want just the Profile's own database", got.Databases)
	}
	if reads := driver.Reads(); len(reads) != 0 {
		t.Errorf("the database was asked %v, want nothing asked at all", reads)
	}
}

// A MongoDB Profile's "tables" are its collections, read with the one
// database-level call the Approval Gate proved.
func TestListTablesListsMongoCollections(t *testing.T) {
	svc, driver := newScriptedFacade(t, db.EngineMongoDB, "shop", func(string) (db.Result, error) {
		return collectionsAnswer(false, "orders", "audit"), nil
	})

	got := svc.ListTables(context.Background(), "store", "shop")

	if got.Status != service.SchemaOK {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.SchemaOK, got.Message)
	}
	if names := tableNames(got.Tables); !equalStrings(names, []string{"audit", "orders"}) {
		t.Errorf("collections = %v, want them sorted by name", names)
	}
	if got.Truncated {
		t.Error("Truncated = true on a catalogue that came back whole")
	}
	if !strings.Contains(got.Message, "collections") {
		t.Errorf("message = %q, want it counted in collections", got.Message)
	}
	if reads := driver.Reads(); len(reads) != 1 || reads[0] != "db.getCollectionNames()" {
		t.Errorf("the database was asked %v, want the one database-level read", reads)
	}
}

// The adapter cuts the catalogue at its own cap, and the cut travels with it.
func TestListTablesCarriesTheMongoTruncation(t *testing.T) {
	svc, _ := newScriptedFacade(t, db.EngineMongoDB, "shop", func(string) (db.Result, error) {
		return collectionsAnswer(true, "orders"), nil
	})

	got := svc.ListTables(context.Background(), "store", "shop")

	if got.Status != service.SchemaOK {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.SchemaOK, got.Message)
	}
	if !got.Truncated {
		t.Error("Truncated = false on an answer the adapter cut")
	}
}

// An answer that is not the document this read produces is a failure, not an
// empty database.
func TestListTablesReportsAMalformedCollectionsReply(t *testing.T) {
	cases := []struct {
		name   string
		answer db.Result
	}{
		{"no documents", db.DocumentsResult(nil, false)},
		{
			"two documents",
			db.DocumentsResult([]json.RawMessage{json.RawMessage(`{"collections":[]}`), json.RawMessage(`{}`)}, false),
		},
		{"a document with no collections field", db.DocumentsResult([]json.RawMessage{json.RawMessage(`{"count":1}`)}, false)},
		{"a document that is not JSON", db.DocumentsResult([]json.RawMessage{json.RawMessage(`{`)}, false)},
		{"a value, from an adapter that answered the wrong arm", db.ValueResult(db.Reply{Kind: db.ReplyNil})},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newScriptedFacade(t, db.EngineMongoDB, "shop", func(string) (db.Result, error) {
				return tc.answer, nil
			})

			got := svc.ListTables(context.Background(), "store", "shop")

			if got.Status != service.SchemaFailed {
				t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.SchemaFailed, got.Message)
			}
			if got.Tables != nil {
				t.Errorf("Tables = %v, want none when the answer could not be read", got.Tables)
			}
		})
	}
}

// The failure the database reports is the failure the tree shows, on every
// Engine — a refused SCAN reads like a refused SELECT.
func TestListTablesReportsAnEngineFailure(t *testing.T) {
	svc, _ := newScriptedFacade(t, db.EngineRedis, "", func(string) (db.Result, error) {
		return db.Result{}, fmt.Errorf("db: NOPERM this user has no permissions to run the 'scan' command")
	})

	got := svc.ListTables(context.Background(), "store", "0")

	if got.Status != service.SchemaFailed {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.SchemaFailed, got.Message)
	}
	if !strings.Contains(got.Message, "NOPERM") {
		t.Errorf("message = %q, want the server's own wording kept", got.Message)
	}
	if strings.Contains(got.Message, "db: ") {
		t.Errorf("message = %q, want the package prefix stripped", got.Message)
	}
}

// Columns and indexes are MySQL's. A Redis key has neither and a MongoDB
// collection has no fixed ones, so the question is refused in those words
// rather than asked and failed — and nothing is sent to the server.
func TestListColumnsAndIndexesAreRefusedOffMySQL(t *testing.T) {
	for _, engine := range []db.Engine{db.EngineRedis, db.EngineMongoDB} {
		t.Run(string(engine), func(t *testing.T) {
			svc, driver := newScriptedFacade(t, engine, "shop", func(string) (db.Result, error) {
				return db.Result{}, fmt.Errorf("nothing should have been asked")
			})
			ctx := context.Background()

			columns := svc.ListColumns(ctx, "store", "shop", "users")
			if columns.Status != service.SchemaFailed {
				t.Errorf("ListColumns status = %q, want %q", columns.Status, service.SchemaFailed)
			}
			if columns.Columns != nil {
				t.Errorf("Columns = %v, want none", columns.Columns)
			}

			indexes := svc.ListIndexes(ctx, "store", "shop", "users")
			if indexes.Status != service.SchemaFailed {
				t.Errorf("ListIndexes status = %q, want %q", indexes.Status, service.SchemaFailed)
			}
			if indexes.Indexes != nil {
				t.Errorf("Indexes = %v, want none", indexes.Indexes)
			}

			if reads := driver.Reads(); len(reads) != 0 {
				t.Errorf("the database was asked %v, want nothing asked at all", reads)
			}
		})
	}
}

// A Profile that is not connected is answered the same way on every Engine:
// there is nowhere to ask, and nothing was asked.
func TestSchemaCallsOnADisconnectedNonSQLProfile(t *testing.T) {
	svc, _ := newScriptedFacade(t, db.EngineRedis, "", func(string) (db.Result, error) {
		return db.Result{}, fmt.Errorf("nothing should have been asked")
	})
	if err := svc.Disconnect("store"); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	if got := svc.ListDatabases(context.Background(), "store"); got.Status != service.SchemaNotConnected {
		t.Errorf("ListDatabases status = %q, want %q (message: %s)", got.Status, service.SchemaNotConnected, got.Message)
	}
	if got := svc.ListTables(context.Background(), "store", "0"); got.Status != service.SchemaNotConnected {
		t.Errorf("ListTables status = %q, want %q (message: %s)", got.Status, service.SchemaNotConnected, got.Message)
	}
}

// The wire shape of a MySQL table list is frozen. The Truncated field the Redis
// arm needed is additive: it is absent from a MySQL answer, so nothing already
// rendering the tree has to change.
func TestMySQLTableListJSONIsUnchanged(t *testing.T) {
	driver := dbtest.NewFakeDriver()
	driver.Answer("local", db.ResultSet{
		Columns: []string{"table_name", "table_rows"},
		Rows:    [][]*string{{str("orders"), str("42")}},
	})
	svc := newQueryFacade(t, driver)

	got, err := json.Marshal(svc.ListTables(context.Background(), "local", ""))
	if err != nil {
		t.Fatalf("marshalling the result: %v", err)
	}

	const want = `{"status":"ok","message":"1 table.","tables":[{"name":"orders","row_estimate":42}]}`
	if string(got) != want {
		t.Errorf("the wire shape of a MySQL table list changed:\n got %s\nwant %s", got, want)
	}
}
