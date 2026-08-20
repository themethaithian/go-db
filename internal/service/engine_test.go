package service_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/themethaithian/go-db/internal/db"
	"github.com/themethaithian/go-db/internal/db/dbtest"
	"github.com/themethaithian/go-db/internal/guard"
	"github.com/themethaithian/go-db/internal/service"
)

// These tests state what the facade does with a Profile that is not MySQL.
//
// The claim under test is the dispatch, not the verdicts: which classifier
// judges a statement is decided here, and what each one decides is specified in
// internal/guard. What must hold on every Engine is the shape of the answer —
// the gate is asked, a mutation is withheld whatever language it is written in,
// and an Engine go-db cannot judge yet is judged as a mutation rather than
// waved through.

// valueDriver is a db.Driver whose reads answer with the Result union's Value
// arm, as the Redis adapter does. dbtest.FakeDriver scripts ResultSets and tags
// them as tables, which is the one shape these tests must not be given.
//
// It records what it was asked, in the two lists dbtest keeps apart for the
// same reason: a mutation that was previewed but never run must not be able to
// hide among the preview's reads.
type valueDriver struct {
	mu       sync.Mutex
	reply    db.Reply
	affected int64
	reads    []string
	execs    []string
}

func (d *valueDriver) Open(context.Context, db.Profile, string, db.DialFunc) (db.Conn, error) {
	return &valueConn{driver: d}, nil
}

func (d *valueDriver) Reads() []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]string(nil), d.reads...)
}

func (d *valueDriver) Execs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]string(nil), d.execs...)
}

type valueConn struct{ driver *valueDriver }

func (valueConn) Ping(context.Context) error { return nil }

func (c *valueConn) ReadQuery(_ context.Context, command string) (db.Result, error) {
	c.driver.mu.Lock()
	defer c.driver.mu.Unlock()

	c.driver.reads = append(c.driver.reads, command)
	return db.ValueResult(c.driver.reply), nil
}

func (c *valueConn) Exec(_ context.Context, command string) (int64, error) {
	c.driver.mu.Lock()
	defer c.driver.mu.Unlock()

	c.driver.execs = append(c.driver.execs, command)
	return c.driver.affected, nil
}

func (valueConn) Close() error { return nil }

var (
	_ db.Driver = (*valueDriver)(nil)
	_ db.Conn   = (*valueConn)(nil)
)

// documentsDriver is a db.Driver whose reads answer with the Result union's
// Documents arm, as the MongoDB adapter does. It gives a service-level test a
// documents answer without dialling a real MongoDB — the substitution
// valueDriver makes for Redis, one arm over.
type documentsDriver struct {
	mu       sync.Mutex
	docs     db.DocumentSet
	affected int64
	reads    []string
	execs    []string
}

func (d *documentsDriver) Open(context.Context, db.Profile, string, db.DialFunc) (db.Conn, error) {
	return &documentsConn{driver: d}, nil
}

func (d *documentsDriver) Reads() []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]string(nil), d.reads...)
}

func (d *documentsDriver) Execs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([]string(nil), d.execs...)
}

type documentsConn struct{ driver *documentsDriver }

func (documentsConn) Ping(context.Context) error { return nil }

func (c *documentsConn) ReadQuery(_ context.Context, statement string) (db.Result, error) {
	c.driver.mu.Lock()
	defer c.driver.mu.Unlock()

	c.driver.reads = append(c.driver.reads, statement)
	return db.DocumentsResult(c.driver.docs.Documents, c.driver.docs.Truncated), nil
}

func (c *documentsConn) Exec(_ context.Context, statement string) (int64, error) {
	c.driver.mu.Lock()
	defer c.driver.mu.Unlock()

	c.driver.execs = append(c.driver.execs, statement)
	return c.driver.affected, nil
}

func (documentsConn) Close() error { return nil }

var (
	_ db.Driver = (*documentsDriver)(nil)
	_ db.Conn   = (*documentsConn)(nil)
)

// newEngineFacade builds an App Service over a Drivers map the test chooses,
// with one connected Profile named "store" on the given Engine.
func newEngineFacade(t *testing.T, engine db.Engine, drivers db.Drivers) *service.AppService {
	t.Helper()

	svc := service.NewWithDrivers(
		db.NewProfileStore(t.TempDir(), dbtest.NewFakeKeychain()), drivers,
		guard.NewJSONLAuditLog(t.TempDir()), nil,
	)
	mustSave(t, svc, db.Profile{Name: "store", Host: "store.internal", User: "app", Engine: engine}, "")
	if err := svc.Connect(context.Background(), "store"); err != nil {
		t.Fatalf("Connect(store): %v", err)
	}
	return svc
}

// newRedisFacade is newEngineFacade over an Engine whose answers are one typed
// value, with reply the answer every read gets.
func newRedisFacade(t *testing.T, reply db.Reply) (*service.AppService, *valueDriver) {
	t.Helper()

	driver := &valueDriver{reply: reply}
	return newEngineFacade(t, db.EngineRedis, db.Drivers{db.EngineRedis: driver}), driver
}

// newMongoFacade is newEngineFacade over an Engine whose answers are a list of
// documents, with docs the answer every read gets.
func newMongoFacade(t *testing.T, docs db.DocumentSet) (*service.AppService, *documentsDriver) {
	t.Helper()

	driver := &documentsDriver{docs: docs}
	return newEngineFacade(t, db.EngineMongoDB, db.Drivers{db.EngineMongoDB: driver}), driver
}

// handWrittenProfile writes a profiles.toml holding one Profile named "store",
// with settings the store's own Save would reject. It is the only way such a
// Profile exists — a human editing the file — and the facade has to survive
// one.
func handWrittenProfile(t *testing.T, dir string, settings ...string) {
	t.Helper()

	profile := append([]string{"[[profile]]", `name = "store"`, `host = "store.internal"`, `user = "app"`}, settings...)
	path := filepath.Join(dir, "profiles.toml")
	if err := os.WriteFile(path, []byte(strings.Join(profile, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// awaitConsole waits for the single entry the Approval Console is about to
// hold. The submitting call is blocked inside RunQuery, so the console is the
// only place from which its arrival can be seen.
func awaitConsole(t *testing.T, svc *service.AppService) guard.Waiting {
	t.Helper()

	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if waiting := svc.ListPendingApprovals(); len(waiting) == 1 {
			return waiting[0]
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("nothing reached the Approval Console")
	return guard.Waiting{}
}

// The classifier is chosen by the Engine, and by nothing else. GET is a Redis
// read and unparseable SQL; DELETE is a SQL mutation and an unknown Redis
// command. Handing a statement to the wrong Engine's classifier would be
// visible here as a verdict that came out right for the wrong language.
func TestClassifyPicksTheClassifierForTheEngine(t *testing.T) {
	svc := newQueryFacade(t, dbtest.NewFakeDriver())

	cases := []struct {
		name      string
		engine    db.Engine
		statement string
		wantRead  bool
		wantWords []string // substrings the reason must contain
	}{
		{"MySQL read", db.EngineMySQL, "SELECT 1", true, []string{"SELECT"}},
		{"MySQL mutation", db.EngineMySQL, "DELETE FROM users", false, []string{"DELETE"}},
		{"Redis read", db.EngineRedis, "GET user:1", true, []string{"GET"}},
		{"Redis mutation", db.EngineRedis, "DEL user:1", false, []string{"DEL"}},
		{"a Redis read is not SQL", db.EngineMySQL, "GET user:1", false, nil},
		{"SQL is not a Redis command", db.EngineRedis, "SELECT 1", false, []string{"SELECT"}},
		{"MongoDB read", db.EngineMongoDB, "db.users.find({})", true, []string{"find"}},
		{"MongoDB mutation", db.EngineMongoDB, "db.users.deleteMany({})", false, []string{"deleteMany"}},
		{"SQL is not the MongoDB grammar", db.EngineMongoDB, "SELECT 1", false, []string{"MongoDB"}},
		{"a MongoDB call is not SQL", db.EngineMySQL, "db.users.find({})", false, nil},
		{"an unset Engine is unjudged", db.Engine(""), "SELECT 1", false, nil},
		{"an unknown Engine is unjudged", db.Engine("postgres"), "SELECT 1", false, []string{"postgres"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := svc.Classify(c.engine, c.statement)

			if got.IsRead() != c.wantRead {
				t.Fatalf("Classify(%q, %q) = %+v, want read = %v", c.engine, c.statement, got, c.wantRead)
			}
			if got.Reason == "" {
				t.Error("reason is empty, want one line the human can read")
			}
			for _, word := range c.wantWords {
				if !strings.Contains(got.Reason, word) {
					t.Errorf("reason = %q, want it to mention %q", got.Reason, word)
				}
			}
		})
	}
}

// One Redis command line is one statement. The buffer is a span so the editor
// has something to target and submit; whether what is in it is one command is
// the classifier's to say, and it refuses a buffer holding two.
func TestSplitStatementsPerEngine(t *testing.T) {
	svc := newQueryFacade(t, dbtest.NewFakeDriver())

	t.Run("MySQL splits on the grammar", func(t *testing.T) {
		got := svc.SplitStatements(db.EngineMySQL, "SELECT 1; SELECT 2")

		if len(got) != 2 {
			t.Fatalf("got %d spans, want 2: %+v", len(got), got)
		}
	})

	t.Run("Redis is one span over the trimmed buffer", func(t *testing.T) {
		const buffer = "  GET user:1  "
		got := svc.SplitStatements(db.EngineRedis, buffer)

		if len(got) != 1 {
			t.Fatalf("got %d spans, want 1: %+v", len(got), got)
		}
		want := guard.StatementSpan{Start: 2, End: 12, Text: "GET user:1"}
		if got[0] != want {
			t.Errorf("span = %+v, want %+v", got[0], want)
		}
		if buffer[got[0].Start:got[0].End] != got[0].Text {
			t.Errorf("span text %q is not the buffer at [%d,%d)", got[0].Text, got[0].Start, got[0].End)
		}
	})

	t.Run("two Redis commands are still one span", func(t *testing.T) {
		const buffer = "GET a\nDEL b"
		got := svc.SplitStatements(db.EngineRedis, buffer)

		if len(got) != 1 || got[0].Text != buffer {
			t.Fatalf("spans = %+v, want the whole buffer as one span", got)
		}
		// And that span is refused, which is why offering it is honest.
		if svc.Classify(db.EngineRedis, got[0].Text).IsRead() {
			t.Error("a buffer holding two commands classified as a read")
		}
	})

	t.Run("nothing to run has no span", func(t *testing.T) {
		for _, engine := range []db.Engine{db.EngineMySQL, db.EngineRedis, db.EngineMongoDB} {
			if got := svc.SplitStatements(engine, "   \n "); len(got) != 0 {
				t.Errorf("%s: spans = %+v, want none", engine, got)
			}
		}
	})
}

// A read on an Engine whose answers are one typed value comes back as that
// value, in the Value field, with no columns and no rows: an empty table would
// be an answer the database never gave.
func TestRunQueryReturnsAValueAnswer(t *testing.T) {
	svc, driver := newRedisFacade(t, db.Reply{Kind: db.ReplyString, Text: "ada"})

	got := svc.RunQuery(context.Background(), "store", "", "GET user:1", guard.OriginHuman)

	if got.Status != service.QueryOK {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.QueryOK, got.Message)
	}
	if !got.Classification.IsRead() {
		t.Errorf("classification = %+v, want a read", got.Classification)
	}
	if got.Value == nil {
		t.Fatal("Value is nil, want the reply the database sent")
	}
	if got.Value.Kind != db.ReplyString || got.Value.Text != "ada" {
		t.Errorf("Value = %+v, want the string \"ada\"", *got.Value)
	}
	if got.Columns != nil || got.Rows != nil {
		t.Errorf("columns = %v, rows = %v, want neither for a value", got.Columns, got.Rows)
	}
	if got.Message == "" {
		t.Error("message is empty, want a line saying what came back")
	}
	if reads := driver.Reads(); len(reads) != 1 || reads[0] != "GET user:1" {
		t.Errorf("the database was asked %v, want the command as written, once", reads)
	}
}

// A nil reply is an answer — the key was not there — and not a missing one.
func TestRunQueryReportsANilValueAsAnAnswer(t *testing.T) {
	svc, _ := newRedisFacade(t, db.Reply{Kind: db.ReplyNil})

	got := svc.RunQuery(context.Background(), "store", "", "GET missing", guard.OriginHuman)

	if got.Status != service.QueryOK {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.QueryOK, got.Message)
	}
	if got.Value == nil || got.Value.Kind != db.ReplyNil {
		t.Fatalf("Value = %+v, want a nil reply", got.Value)
	}
}

// The wire shape of a MySQL read is frozen. The Value arm is additive: a table
// answer serialises byte for byte as it did before the union reached the
// facade, so nothing already rendering results has to change.
func TestMySQLResultJSONIsUnchanged(t *testing.T) {
	driver := dbtest.NewFakeDriver()
	driver.Answer("local", db.ResultSet{Columns: []string{"id"}, Rows: [][]*string{{str("1")}}})
	svc := newQueryFacade(t, driver)

	got, err := json.Marshal(svc.RunQuery(context.Background(), "local", "", "SELECT id FROM users", guard.OriginHuman))
	if err != nil {
		t.Fatalf("marshalling the result: %v", err)
	}

	const want = `{"status":"ok","classification":{"kind":"read","reason":"SELECT statement"},` +
		`"origin":"human","message":"1 row.","columns":["id"],"rows":[["1"]],` +
		`"truncated":false,"affected_rows":0}`
	if string(got) != want {
		t.Errorf("the wire shape of a MySQL read changed:\n got %s\nwant %s", got, want)
	}
}

// A read on an Engine whose answers are a list of documents comes back as
// that list, in the Documents field, with no columns, no rows and no value:
// an empty table or a nil value would be an answer the database never gave.
func TestRunQueryReturnsADocumentsAnswer(t *testing.T) {
	docs := db.DocumentSet{Documents: []json.RawMessage{json.RawMessage(`{"_id":1,"name":"ada"}`)}}
	svc, driver := newMongoFacade(t, docs)

	got := svc.RunQuery(context.Background(), "store", "", "db.users.find({})", guard.OriginHuman)

	if got.Status != service.QueryOK {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.QueryOK, got.Message)
	}
	if !got.Classification.IsRead() {
		t.Errorf("classification = %+v, want a read", got.Classification)
	}
	if got.Documents == nil {
		t.Fatal("Documents is nil, want the documents the database sent")
	}
	if len(got.Documents.Documents) != 1 || string(got.Documents.Documents[0]) != `{"_id":1,"name":"ada"}` {
		t.Errorf("Documents = %+v, want the one document as sent", got.Documents)
	}
	if got.Value != nil || got.Columns != nil || got.Rows != nil {
		t.Errorf("value = %v, columns = %v, rows = %v, want none of them for a documents answer",
			got.Value, got.Columns, got.Rows)
	}
	if got.Message != "1 document." {
		t.Errorf("message = %q, want %q", got.Message, "1 document.")
	}
	if reads := driver.Reads(); len(reads) != 1 || reads[0] != "db.users.find({})" {
		t.Errorf("the database was asked %v, want the call as written, once", reads)
	}
}

// The document-count message says how many came back and whether that is all
// of it, in the same wording summarise gives a table — see summariseDocuments.
func TestRunQueryDocumentCountMessageWording(t *testing.T) {
	one := func(body string) json.RawMessage { return json.RawMessage(body) }

	cases := []struct {
		name string
		docs db.DocumentSet
		want string
	}{
		{"no documents", db.DocumentSet{Documents: []json.RawMessage{}}, "0 documents."},
		{"one document", db.DocumentSet{Documents: []json.RawMessage{one("{}")}}, "1 document."},
		{
			"many documents",
			db.DocumentSet{Documents: []json.RawMessage{one("{}"), one("{}"), one("{}")}},
			"3 documents.",
		},
		{
			"truncated",
			db.DocumentSet{Documents: []json.RawMessage{one("{}")}, Truncated: true},
			"Showing the first 1 documents; the result was truncated.",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc, _ := newMongoFacade(t, c.docs)

			got := svc.RunQuery(context.Background(), "store", "", "db.users.find({})", guard.OriginHuman)

			if got.Message != c.want {
				t.Errorf("message = %q, want %q", got.Message, c.want)
			}
		})
	}
}

// A MongoDB mutation takes the route every mutation takes: withheld by the
// gate, shown with the Impact Preview there is none of yet, run only once a
// human has confirmed it, and its own affected count reported back — the
// Documents-armed Engine's turn through the same flow TestRedisMutationFlowsTheGate
// proves for the Value-armed one.
func TestMongoMutationFlowsTheGateWithNoPreview(t *testing.T) {
	svc, driver := newMongoFacade(t, db.DocumentSet{})
	driver.affected = 4
	ctx := context.Background()

	withheld := svc.RunQuery(ctx, "store", "", "db.users.deleteMany({})", guard.OriginHuman)

	if withheld.Status != service.QueryRequiresConfirmation {
		t.Fatalf("status = %q, want %q (message: %s)",
			withheld.Status, service.QueryRequiresConfirmation, withheld.Message)
	}
	if withheld.Preview == nil {
		t.Fatal("Preview is nil, want a preview that says there is none")
	}
	if withheld.Preview.Available {
		t.Errorf("Preview.Available = true for a MongoDB statement: %+v", withheld.Preview)
	}
	if !strings.Contains(withheld.Preview.Reason, "MongoDB") {
		t.Errorf("Preview.Reason = %q, want it to say MongoDB previews are not written yet", withheld.Preview.Reason)
	}
	if reads := driver.Reads(); len(reads) != 0 {
		t.Errorf("the preview asked the database %v, want nothing asked at all", reads)
	}
	if execs := driver.Execs(); len(execs) != 0 {
		t.Errorf("a withheld mutation executed %v", execs)
	}

	confirmed := svc.ConfirmPending(ctx, withheld.PendingID)

	if confirmed.Status != service.QueryExecuted {
		t.Fatalf("status = %q, want %q (message: %s)", confirmed.Status, service.QueryExecuted, confirmed.Message)
	}
	if confirmed.AffectedRows != 4 {
		t.Errorf("AffectedRows = %d, want 4 — what the driver said it changed", confirmed.AffectedRows)
	}
	if execs := driver.Execs(); len(execs) != 1 || execs[0] != "db.users.deleteMany({})" {
		t.Errorf("the database ran %v, want the command as written, once", execs)
	}
}

// A Redis mutation takes the route every mutation takes: withheld by the gate,
// shown with the Impact Preview there is none of, run only once a human has
// confirmed it, and recorded either way.
func TestRedisMutationFlowsTheGate(t *testing.T) {
	svc, driver := newRedisFacade(t, db.Reply{Kind: db.ReplyNil})
	driver.affected = 2
	ctx := context.Background()

	withheld := svc.RunQuery(ctx, "store", "", "DEL user:1 user:2", guard.OriginHuman)

	if withheld.Status != service.QueryRequiresConfirmation {
		t.Fatalf("status = %q, want %q (message: %s)",
			withheld.Status, service.QueryRequiresConfirmation, withheld.Message)
	}
	if withheld.Preview == nil {
		t.Fatal("Preview is nil, want a preview that says there is none")
	}
	if withheld.Preview.Available {
		t.Errorf("Preview.Available = true for a Redis command: %+v", withheld.Preview)
	}
	if !strings.Contains(withheld.Preview.Reason, "Redis") {
		t.Errorf("Preview.Reason = %q, want it to say Redis previews are not written yet", withheld.Preview.Reason)
	}
	if reads := driver.Reads(); len(reads) != 0 {
		t.Errorf("the preview asked the database %v, want nothing asked at all", reads)
	}
	if execs := driver.Execs(); len(execs) != 0 {
		t.Errorf("a withheld mutation executed %v", execs)
	}

	confirmed := svc.ConfirmPending(ctx, withheld.PendingID)

	if confirmed.Status != service.QueryExecuted {
		t.Fatalf("status = %q, want %q (message: %s)", confirmed.Status, service.QueryExecuted, confirmed.Message)
	}
	if confirmed.AffectedRows != 2 {
		t.Errorf("AffectedRows = %d, want 2 — what Redis said it changed", confirmed.AffectedRows)
	}
	if execs := driver.Execs(); len(execs) != 1 || execs[0] != "DEL user:1 user:2" {
		t.Errorf("the database ran %v, want the command as written, once", execs)
	}
}

// The Approval Gate's second policy over a Value-armed Engine: an AI's mutation
// waits, a human approves it, and the audit log takes the record without
// anything shaped like a table to write down.
func TestRedisMutationIsAuditedThroughTheApprovalConsole(t *testing.T) {
	driver := &valueDriver{reply: db.Reply{Kind: db.ReplyNil}, affected: 1}
	dir := t.TempDir()
	audit := guard.NewJSONLAuditLog(dir)
	svc := service.NewWithDrivers(
		db.NewProfileStore(t.TempDir(), dbtest.NewFakeKeychain()),
		db.Drivers{db.EngineRedis: driver}, audit, nil,
	)
	mustSave(t, svc, db.Profile{Name: "store", Host: "store.internal", User: "app", Engine: db.EngineRedis}, "")
	if err := svc.Connect(context.Background(), "store"); err != nil {
		t.Fatalf("Connect(store): %v", err)
	}

	done := make(chan service.QueryResult, 1)
	go func() {
		done <- svc.RunQuery(context.Background(), "store", "", "SET user:1 ada", guard.OriginAI)
	}()

	waiting := awaitConsole(t, svc)
	if ack := svc.ApprovePending(waiting.ID); !ack.OK {
		t.Fatalf("ApprovePending: %s", ack.Message)
	}

	got := <-done
	if got.Status != service.QueryExecuted {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.QueryExecuted, got.Message)
	}
	if records := auditRecords(t, dir); len(records) != 1 {
		t.Fatalf("the audit log holds %d records, want 1", len(records))
	}
}

// A MongoDB Profile is judged by the MongoDB classifier, which is what the
// dispatch is for. The verdicts themselves are specified in internal/guard;
// what is claimed here is that they arrive — a find() runs, a deleteMany() is
// withheld by the gate, and text that is not the grammar at all is withheld
// too rather than waved through as something nobody could judge.
//
// (The driver these run against answers with the Value arm, because the
// MongoDB adapter and its Documents arm are not written. The Engine's answers
// are not what this test is about.)
func TestMongoDBStatementsAreJudgedByTheGrammar(t *testing.T) {
	driver := &valueDriver{reply: db.Reply{Kind: db.ReplyNil}}
	svc := newEngineFacade(t, db.EngineMongoDB, db.Drivers{db.EngineMongoDB: driver})
	ctx := context.Background()

	t.Run("a find is a read and runs", func(t *testing.T) {
		got := svc.RunQuery(ctx, "store", "", "db.users.find({})", guard.OriginHuman)

		if got.Status != service.QueryOK {
			t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.QueryOK, got.Message)
		}
		if !got.Classification.IsRead() {
			t.Errorf("classification = %+v, want a read", got.Classification)
		}
		if reads := driver.Reads(); len(reads) != 1 || reads[0] != "db.users.find({})" {
			t.Errorf("the database was asked %v, want the call as written, once", reads)
		}
	})

	for _, tc := range []struct {
		name      string
		statement string
	}{
		{"a delete is withheld", "db.users.deleteMany({})"},
		{"a pipeline that writes is withheld", `db.users.aggregate([{$out: "copy"}])`},
		{"a second call in the buffer is withheld", "db.users.find({}); db.users.drop()"},
		{"text that is not the grammar is withheld", "DROP TABLE users"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			execsBefore := len(driver.Execs())

			got := svc.RunQuery(ctx, "store", "", tc.statement, guard.OriginHuman)

			if got.Status != service.QueryRequiresConfirmation {
				t.Fatalf("status = %q, want %q (message: %s)",
					got.Status, service.QueryRequiresConfirmation, got.Message)
			}
			if got.Classification.IsRead() {
				t.Errorf("classification = %+v, want a mutation", got.Classification)
			}
			if got.Preview == nil || got.Preview.Available {
				t.Errorf("Preview = %+v, want one that says there is no preview for a MongoDB statement", got.Preview)
			}
			if execs := driver.Execs(); len(execs) != execsBefore {
				t.Errorf("a withheld statement executed %v", execs)
			}
		})
	}
}

// An Engine nobody has heard of can only reach the facade one way — somebody
// wrote it into profiles.toml, which SaveProfile would have refused — and it is
// judged the way everything unproven is: no classifier, no proof, no execution.
func TestAnUnknownEngineFailsClosed(t *testing.T) {
	dir := t.TempDir()
	handWrittenProfile(t, dir, "engine = \"postgres\"")

	driver := &valueDriver{reply: db.Reply{Kind: db.ReplyNil}}
	svc := service.NewWithDrivers(
		db.NewProfileStore(dir, dbtest.NewFakeKeychain()),
		db.Drivers{db.Engine("postgres"): driver},
		guard.NewJSONLAuditLog(t.TempDir()), nil,
	)
	if err := svc.Connect(context.Background(), "store"); err != nil {
		t.Fatalf("Connect(store): %v", err)
	}

	got := svc.RunQuery(context.Background(), "store", "", "SELECT 1", guard.OriginHuman)

	if got.Status != service.QueryRequiresConfirmation {
		t.Fatalf("status = %q, want %q (message: %s)",
			got.Status, service.QueryRequiresConfirmation, got.Message)
	}
	if got.Classification.IsRead() {
		t.Errorf("classification = %+v, want a mutation", got.Classification)
	}
	if reads := driver.Reads(); len(reads) != 0 {
		t.Errorf("the database was asked %v, want nothing asked at all", reads)
	}
}

// The Engine a query is judged by is the saved Profile's, read at submission.
// No caller names it: an Origin that could choose its own classifier could
// choose the one that says yes.
func TestRunQueryJudgesByTheSavedProfilesEngine(t *testing.T) {
	svc, driver := newRedisFacade(t, db.Reply{Kind: db.ReplyString, Text: "ada"})

	// Provably read-only SQL, which is not a Redis command at all.
	got := svc.RunQuery(context.Background(), "store", "", "SELECT 1", guard.OriginHuman)

	if got.Status != service.QueryRequiresConfirmation {
		t.Fatalf("status = %q, want %q (message: %s)",
			got.Status, service.QueryRequiresConfirmation, got.Message)
	}
	if reads := driver.Reads(); len(reads) != 0 {
		t.Errorf("the database was asked %v, want nothing asked at all", reads)
	}
}

// A query naming a Profile that is not there is answered as it always was —
// there is nowhere to run it, in those words — and the statement is left
// unjudged rather than read as SQL because SQL is what it looks like: without a
// Profile there is no Engine, and without an Engine there is no classifier.
func TestRunQueryOnAnUnknownProfileIsUnjudged(t *testing.T) {
	svc := newQueryFacade(t, dbtest.NewFakeDriver())

	got := svc.RunQuery(context.Background(), "nowhere", "", "SELECT 1", guard.OriginHuman)

	if got.Status != service.QueryNotConnected {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.QueryNotConnected, got.Message)
	}
	if got.Classification.IsRead() {
		t.Errorf("classification = %+v, want a mutation", got.Classification)
	}
	if !strings.Contains(got.Message, "nowhere") {
		t.Errorf("message = %q, want it to name the Profile that is not there", got.Message)
	}
}

// Paging belongs to the Engine whose answers are rows in a table. What a window
// is, and which statements have one, is specified in internal/guard; what this
// pins is the dispatch and the one thing the facade adds — the page size of a
// statement with no LIMIT of its own is db.MaxRows, the cap that really bounded
// the result on screen, which guard cannot name because internal/db imports it.
func TestPagingPerEngine(t *testing.T) {
	svc := newQueryFacade(t, dbtest.NewFakeDriver())

	t.Run("a MySQL select with no limit is bounded by the driver's cap", func(t *testing.T) {
		got := svc.PageWindow(db.EngineMySQL, "SELECT * FROM users")

		if !got.Pageable {
			t.Fatalf("PageWindow refused: %s", got.Reason)
		}
		if got.Size != db.MaxRows || got.Offset != 0 {
			t.Errorf("window = %+v, want the first %d rows", got, db.MaxRows)
		}
	})

	t.Run("the next page is the statement rewritten, and it is still a read", func(t *testing.T) {
		got := svc.Repage(db.EngineMySQL, "SELECT * FROM users", db.MaxRows, db.MaxRows)

		if !got.Pageable {
			t.Fatalf("Repage refused: %s", got.Reason)
		}
		if !svc.Classify(db.EngineMySQL, got.SQL).IsRead() {
			t.Errorf("the next page %q is not a read", got.SQL)
		}
	})

	t.Run("an Engine whose answers are not rows has no window", func(t *testing.T) {
		for _, engine := range []db.Engine{db.EngineRedis, db.EngineMongoDB} {
			window := svc.PageWindow(engine, "GET user:1")
			if window.Pageable {
				t.Errorf("%s: window = %+v, want none", engine, window)
			}
			if window.Reason == "" {
				t.Errorf("%s: no window and no reason", engine)
			}
			if next := svc.Repage(engine, "GET user:1", 10, 10); next.Pageable {
				t.Errorf("%s: Repage produced %q", engine, next.SQL)
			}
		}
	})

	t.Run("an Engine go-db does not know has no window", func(t *testing.T) {
		got := svc.PageWindow(db.Engine("postgres"), "SELECT 1")

		if got.Pageable {
			t.Fatalf("window = %+v, want none", got)
		}
		if !strings.Contains(got.Reason, "postgres") {
			t.Errorf("reason = %q, want it to name the Engine", got.Reason)
		}
	})
}
