package db

// These are the MongoDB adapter's unit tests. They are an internal test —
// package db, not db_test — because what they pin is machinery this package
// hides on purpose: the argument shapes each operation accepts, the documents
// go-db renders itself for answers that are not documents, and the refusal that
// happens before anything is dialled. None of that is exported, and a test that
// walked an exported copy would be pinning the copy.
//
// Everything that needs a server is in mongo_integration_test.go.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/themethaithian/go-db/internal/guard"
	"github.com/themethaithian/go-db/internal/mongoql"
)

// TestMongoReadQueryRefusesAMutation is the adapter's own second layer firing.
// The connection it runs on has no client and no database at all, which is the
// point: if the refusal did not happen before execution, the nil database would
// panic instead of returning an error, so a passing test is evidence the
// statement never left this process.
func TestMongoReadQueryRefusesAMutation(t *testing.T) {
	for _, statement := range []string{
		`db.users.deleteMany({})`,
		`db.users.insertOne({name: "a"})`,
		`db.users.updateOne({}, {$set: {a: 1}})`,
		`db.users.drop()`,
		`db.users.findOneAndUpdate({}, {$set: {a: 1}})`,
		`db.users.mapReduce(1, 2, {out: "elsewhere"})`,
		`db.users.aggregate([{$match: {}}, {$out: "elsewhere"}])`,
		`db.users.aggregate([{$match: {}}, {$merge: {into: "elsewhere"}}])`,
		`db.users.notAnOperation({})`,
		`SELECT 1`,
		`db.users.find({}); db.users.drop()`,
		``,
	} {
		t.Run(statement, func(t *testing.T) {
			conn := &mongoConn{}

			result, err := conn.ReadQuery(context.Background(), statement)
			if err == nil {
				t.Fatalf("ReadQuery(%q) = %v, want it refused", statement, result.Kind())
			}
			if !errors.Is(err, ErrWriteAttempt) {
				t.Errorf("ReadQuery(%q) error = %v, want it to wrap ErrWriteAttempt so the gate takes the statement back", statement, err)
			}
		})
	}
}

// TestMongoReadVerbsAreClassifiedReads pins the agreement Exec's refusal rests
// on: every operation this adapter calls a read is one the Approval Gate's
// MongoDB classifier also calls a read.
//
// The two lists are separate on purpose — guard keeps its allowlist unexported,
// and reaching for it would be asking another package to widen its API — so
// this is where they are held together. A verb in this package's list that the
// classifier calls a Mutation would be one Exec describes as "a read" while
// ReadQuery refuses it.
func TestMongoReadVerbsAreClassifiedReads(t *testing.T) {
	arguments := map[string]string{
		"distinct":  `"name"`,
		"aggregate": `[{$match: {}}]`,
	}

	for verb := range mongoReadVerbs {
		t.Run(verb, func(t *testing.T) {
			statement := "db.users." + verb + "(" + arguments[verb] + ")"

			if verdict := guard.ClassifyMongo(statement); !verdict.IsRead() {
				t.Errorf("ClassifyMongo(%q) = %s, want the classifier and this adapter to agree on what reads", statement, verdict.Reason)
			}
		})
	}
}

// TestMongoExecRefusesWhatItCannotWrite covers both halves of Exec's closed set.
// A read that arrived at the write path is a caller's bug and says so; an
// operation nobody has implemented is a gap in this adapter and says that
// instead. Neither is a silent no-op, which is the failure that would matter:
// the Approval Gate would record an approved mutation that never happened.
func TestMongoExecRefusesWhatItCannotWrite(t *testing.T) {
	for _, test := range []struct {
		statement string
		wants     string
	}{
		{`db.users.find({})`, "is a read"},
		{`db.users.countDocuments()`, "is a read"},
		{`db.users.aggregate([{$match: {}}])`, "is a read"},
		{`db.users.createIndex({name: 1})`, "does not run"},
		{`db.users.findOneAndUpdate({}, {})`, "does not run"},
		{`db.users.bulkWrite([])`, "does not run"},
		{`db.users.renameCollection("other")`, "does not run"},
	} {
		t.Run(test.statement, func(t *testing.T) {
			conn := &mongoConn{}

			affected, err := conn.Exec(context.Background(), test.statement)
			if err == nil {
				t.Fatalf("Exec(%q) = %d, want it refused rather than run", test.statement, affected)
			}
			if !strings.Contains(err.Error(), test.wants) {
				t.Errorf("Exec(%q) error = %v, want it to say %q", test.statement, err, test.wants)
			}
			if affected != 0 {
				t.Errorf("Exec(%q) affected = %d on a refusal, want 0", test.statement, affected)
			}
		})
	}
}

// TestMongoExecRefusesAStatementItCannotParse keeps the write path from running
// on a best guess. Exec does not classify — a human already approved this
// statement by ID — but it still has to read the same grammar the approval was
// given for.
func TestMongoExecRefusesAStatementItCannotParse(t *testing.T) {
	conn := &mongoConn{}

	if affected, err := conn.Exec(context.Background(), `db.users.insertOne({`); err == nil {
		t.Fatalf("Exec on an unparseable statement = %d, want it refused", affected)
	}
}

// TestMongoFilterAndProjectionReadsTheFindShape pins what find() and findOne()
// accept. The refusals are the load-bearing half: mongosh would take a third
// argument or a chained .limit(), and go-db's grammar takes neither, so a call
// whose shape it cannot read is refused rather than run with the extras
// dropped.
func TestMongoFilterAndProjectionReadsTheFindShape(t *testing.T) {
	t.Run("no arguments is every document", func(t *testing.T) {
		filter, projection, err := mongoFilterAndProjection("find", parseMongoArgs(t, `db.c.find()`))
		if err != nil {
			t.Fatalf("find(): %v", err)
		}
		if filter == nil || len(filter) != 0 {
			t.Errorf("filter = %v, want the empty filter", filter)
		}
		if projection != nil {
			t.Errorf("projection = %v, want none", projection)
		}
	})

	t.Run("one argument is the filter", func(t *testing.T) {
		filter, projection, err := mongoFilterAndProjection("find", parseMongoArgs(t, `db.c.find({name: 'a', n: {$gte: 2}})`))
		if err != nil {
			t.Fatalf("find(filter): %v", err)
		}
		if len(filter) != 2 || filter[0].Key != "name" || filter[0].Value != "a" {
			t.Errorf("filter = %v, want the two fields as written, in order", filter)
		}
		if projection != nil {
			t.Errorf("projection = %v, want none", projection)
		}
	})

	t.Run("the second argument is the projection", func(t *testing.T) {
		_, projection, err := mongoFilterAndProjection("find", parseMongoArgs(t, `db.c.find({}, {name: 1})`))
		if err != nil {
			t.Fatalf("find(filter, projection): %v", err)
		}
		if len(projection) != 1 || projection[0].Key != "name" {
			t.Errorf("projection = %v, want the one field asked for", projection)
		}
	})

	for _, test := range []struct {
		name      string
		statement string
	}{
		{"a third argument is not part of the shape", `db.c.find({}, {}, {})`},
		{"a filter that is not a document", `db.c.find([1, 2])`},
		{"a null filter is not an empty one", `db.c.find(null)`},
		{"a projection that is not a document", `db.c.find({}, "name")`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := mongoFilterAndProjection("find", parseMongoArgs(t, test.statement)); err == nil {
				t.Errorf("%s was accepted, want it refused loudly", test.statement)
			}
		})
	}
}

func TestMongoOptionalFilterReadsTheCountShape(t *testing.T) {
	filter, err := mongoOptionalFilter("countDocuments", parseMongoArgs(t, `db.c.countDocuments()`))
	if err != nil || len(filter) != 0 {
		t.Errorf("countDocuments() = %v, %v; want the empty filter", filter, err)
	}

	filter, err = mongoOptionalFilter("countDocuments", parseMongoArgs(t, `db.c.countDocuments({done: true})`))
	if err != nil || len(filter) != 1 || filter[0].Key != "done" || filter[0].Value != true {
		t.Errorf("countDocuments(filter) = %v, %v; want the filter as written", filter, err)
	}

	if _, err := mongoOptionalFilter("countDocuments", parseMongoArgs(t, `db.c.countDocuments({}, {})`)); err == nil {
		t.Error("countDocuments({}, {}) was accepted, want it refused loudly")
	}
}

// TestMongoWriteArgumentsReadsTheUpdateShape pins the write path's one mapped
// option. An option key go-db does not map is refused rather than dropped: a
// mutation runs after a human approved that exact text, and running a
// three-quarters version of it is the failure this refusal exists for.
func TestMongoWriteArgumentsReadsTheUpdateShape(t *testing.T) {
	filter, update, upsert, err := mongoWriteArguments("updateOne", "update", parseMongoArgs(t, `db.c.updateOne({n: 1}, {$set: {n: 2}})`))
	if err != nil {
		t.Fatalf("updateOne(filter, update): %v", err)
	}
	if len(filter) != 1 || filter[0].Key != "n" {
		t.Errorf("filter = %v, want the one field written", filter)
	}
	if len(update) != 1 || update[0].Key != "$set" {
		t.Errorf("update = %v, want the $set as written", update)
	}
	if upsert {
		t.Error("upsert = true with no options document, want false")
	}

	if _, _, upsert, err = mongoWriteArguments("updateOne", "update", parseMongoArgs(t, `db.c.updateOne({}, {$set: {}}, {upsert: true})`)); err != nil || !upsert {
		t.Errorf("updateOne with {upsert: true} = %v, %v; want upsert on", upsert, err)
	}

	for _, test := range []struct {
		name      string
		statement string
	}{
		{"no update at all", `db.c.updateOne({})`},
		{"a fourth argument", `db.c.updateOne({}, {}, {}, {})`},
		{"an option go-db does not map", `db.c.updateOne({}, {}, {arrayFilters: []})`},
		{"upsert that is not a flag", `db.c.updateOne({}, {}, {upsert: 1})`},
		{"an options argument that is not a document", `db.c.updateOne({}, {}, true)`},
		{"an update that is not a document", `db.c.updateOne({}, [{$set: {}}])`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, _, _, err := mongoWriteArguments("updateOne", "update", parseMongoArgs(t, test.statement)); err == nil {
				t.Errorf("%s was accepted, want it refused loudly", test.statement)
			}
		})
	}
}

// TestApplyMongoAggregateOptionsIsAClosedSet is the same discipline one shape
// over. go-db sends the four options it can say exactly what it does with, and
// refuses the rest — including the ones the driver could take, because sending
// an option nobody wrote down is how a pipeline stops being the pipeline that
// was approved.
func TestApplyMongoAggregateOptionsIsAClosedSet(t *testing.T) {
	for _, statement := range []string{
		`db.c.aggregate([], {allowDiskUse: true})`,
		`db.c.aggregate([], {comment: "why"})`,
		`db.c.aggregate([], {hint: "name_1"})`,
		`db.c.aggregate([], {hint: {name: 1}})`,
		`db.c.aggregate([], {let: {n: 1}})`,
		`db.c.aggregate([], {allowDiskUse: false, comment: "why", let: {}})`,
		`db.c.aggregate([], {})`,
	} {
		t.Run(statement, func(t *testing.T) {
			args := parseMongoArgs(t, statement)
			if err := applyMongoAggregateOptions(mongoAggregateSettings(), args[1]); err != nil {
				t.Errorf("%s: %v", statement, err)
			}
		})
	}

	for _, test := range []struct {
		name      string
		statement string
	}{
		{"an option go-db does not map", `db.c.aggregate([], {batchSize: 10})`},
		{"an option that only looks harmless", `db.c.aggregate([], {bypassDocumentValidation: true})`},
		{"a collation go-db has not written down", `db.c.aggregate([], {collation: {locale: "en"}})`},
		{"allowDiskUse that is not a flag", `db.c.aggregate([], {allowDiskUse: "yes"})`},
		{"a comment that is not text", `db.c.aggregate([], {comment: 1})`},
		{"a hint that is neither a name nor keys", `db.c.aggregate([], {hint: 1})`},
		{"options that are not a document", `db.c.aggregate([], "allowDiskUse")`},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := parseMongoArgs(t, test.statement)
			if err := applyMongoAggregateOptions(mongoAggregateSettings(), args[1]); err == nil {
				t.Errorf("%s was accepted, want it refused loudly", test.statement)
			}
		})
	}
}

// TestMongoRefusalsDoNotEchoTheCaller pins the discipline internal/mongoql
// keeps and this adapter inherits: a refusal is quoted into the editor badge
// and the audit log, and the text that caused it was written by whoever
// submitted the statement.
func TestMongoRefusalsDoNotEchoTheCaller(t *testing.T) {
	const secret = "sekritOptionName"

	args := parseMongoArgs(t, `db.c.aggregate([], {`+secret+`: 1})`)
	err := applyMongoAggregateOptions(mongoAggregateSettings(), args[1])
	if err == nil {
		t.Fatal("an unknown aggregate option was accepted, want it refused")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("refusal = %v, want it not to quote the caller's own text back", err)
	}
}

// TestMongoCountDocumentIsGoDBsOwnRendering says out loud what the shape is. A
// count is a number, and the Documents arm carries documents, so go-db writes
// one — this test is the record of which field it writes, and of the two
// counting operations sharing it.
func TestMongoCountDocumentIsGoDBsOwnRendering(t *testing.T) {
	result, err := mongoCountDocument(42)
	if err != nil {
		t.Fatalf("mongoCountDocument: %v", err)
	}

	set, ok := result.Documents()
	if !ok {
		t.Fatal("a count is not the Documents arm, want every read tagged the one way")
	}
	if len(set.Documents) != 1 {
		t.Fatalf("documents = %d, want the one document a count is rendered as", len(set.Documents))
	}
	if got := string(set.Documents[0]); got != `{"count":42}` {
		t.Errorf("count document = %s, want {\"count\":42}", got)
	}
	if set.Truncated {
		t.Error("Truncated = true on a count, want a count never to be cut")
	}
}

// TestRenderMongoDocumentIsRelaxedExtendedJSON pins the flavour the arm
// carries. The two halves are what relaxed writes plainly — the values JSON has
// syntax for — and what it keeps wrapped, which is everything else. Canonical
// would wrap all of it, and a document view where every number is a two-key
// object is a view of the encoding rather than of the data.
func TestRenderMongoDocumentIsRelaxedExtendedJSON(t *testing.T) {
	id, err := bson.ObjectIDFromHex("507f1f77bcf86cd799439011")
	if err != nil {
		t.Fatalf("building an object id: %v", err)
	}

	rendered, err := renderMongoDocument(bson.D{
		{Key: "_id", Value: id},
		{Key: "n", Value: int32(1)},
		{Key: "big", Value: int64(2)},
		{Key: "ratio", Value: 1.5},
		{Key: "name", Value: "a"},
		{Key: "done", Value: true},
		{Key: "missing", Value: nil},
		{Key: "markup", Value: "<b>&</b>"},
	})
	if err != nil {
		t.Fatalf("renderMongoDocument: %v", err)
	}

	const want = `{"_id":{"$oid":"507f1f77bcf86cd799439011"},"n":1,"big":2,"ratio":1.5,"name":"a","done":true,"missing":null,"markup":"<b>&</b>"}`
	if string(rendered) != want {
		t.Errorf("rendered = %s,\nwant       %s", rendered, want)
	}
	if !json.Valid(rendered) {
		t.Error("the rendering is not valid JSON, and the arm is carried as JSON")
	}
}

// mongoAggregateSettings is a fresh options builder for one case of the
// aggregate-options tests.
func mongoAggregateSettings() *options.AggregateOptionsBuilder { return options.Aggregate() }

// parseMongoArgs parses one statement in go-db's MongoDB grammar and returns
// its arguments. The tests build arguments this way rather than by hand because
// a mongoql.Value cannot be built from outside its package — which is the
// point: what these functions receive is what the parser produced.
func parseMongoArgs(t *testing.T, statement string) []mongoql.Value {
	t.Helper()

	call, err := mongoql.Parse(statement)
	if err != nil {
		t.Fatalf("parsing the test's own statement %q: %v", statement, err)
	}
	return call.Args
}
