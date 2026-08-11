package guard_test

import (
	"strings"
	"testing"

	"github.com/themethaithian/go-db/internal/guard"
)

// This table is the executable specification of the MongoDB half of the
// Approval Gate's safety claim, and like its Redis sibling it carries the whole
// weight: MongoDB has no read-only transaction to back the classifier up
// (ADR-0006), so an operation called a read here runs with nothing behind it.
// Every arguable case is therefore a Mutation — calling a read a mutation costs
// a keypress, calling a mutation a read loses a collection.
//
// Reasons are asserted wherever the wording is the point: the traps are
// operations a reasonable person would call reads, and the grammar refusals
// have to teach the small grammar rather than just say no.

type mongoCase struct {
	name      string
	statement string
	want      guard.Kind
	// reason, when set, is a substring the Reason must contain.
	reason string
}

func TestClassifyMongoReads(t *testing.T) {
	runMongoCases(t, []mongoCase{
		{name: "find", statement: "db.users.find({})", want: guard.Read, reason: "find"},
		{name: "find with a filter", statement: `db.users.find({name: "ada"})`, want: guard.Read},
		{name: "find with a projection", statement: "db.users.find({}, {name: 1})", want: guard.Read},
		{name: "find with no arguments", statement: "db.users.find()", want: guard.Read},
		{name: "findOne", statement: "db.users.findOne({_id: 1})", want: guard.Read, reason: "findOne"},
		{name: "countDocuments", statement: "db.users.countDocuments({active: true})", want: guard.Read, reason: "countDocuments"},
		{name: "estimatedDocumentCount", statement: "db.users.estimatedDocumentCount()", want: guard.Read, reason: "estimatedDocumentCount"},
		{name: "distinct", statement: `db.users.distinct("country")`, want: guard.Read, reason: "distinct"},
		{name: "distinct with a filter", statement: `db.users.distinct("country", {active: true})`, want: guard.Read},

		// A $where or a $function inside a filter evaluates JavaScript on the
		// server, and there is no write available to it: the expression
		// languages return values, they do not issue operations. That is why a
		// read verb is judged by its name and its arguments are not inspected —
		// except for aggregate, whose argument is a list of operations.
		{name: "find with a where clause", statement: `db.users.find({$where: "this.a > 1"})`, want: guard.Read},

		// Whitespace, newlines and one trailing semicolon are the editor's, not
		// the statement's.
		{name: "leading whitespace", statement: "   db.users.find({})", want: guard.Read},
		{name: "leading newlines", statement: "\n\ndb.users.find({})", want: guard.Read},
		{name: "wrapped across lines", statement: "db.users.find({\n  name: \"ada\"\n})", want: guard.Read},
		{name: "a trailing semicolon", statement: "db.users.find({});", want: guard.Read},
		{name: "a trailing newline", statement: "db.users.find({})\n", want: guard.Read},

		// Pipelines whose every stage is on the stage allowlist.
		{name: "an empty pipeline", statement: "db.orders.aggregate([])", want: guard.Read, reason: "aggregate"},
		{
			name:      "a match and a group",
			statement: `db.orders.aggregate([{$match: {paid: true}}, {$group: {_id: "$region", n: {$sum: 1}}}])`,
			want:      guard.Read, reason: "aggregate",
		},
		{
			name:      "sorting and paging",
			statement: "db.orders.aggregate([{$sort: {at: -1}}, {$skip: 10}, {$limit: 5}, {$count: \"n\"}])",
			want:      guard.Read,
		},
		{
			name:      "reshaping stages",
			statement: `db.orders.aggregate([{$project: {n: 1}}, {$addFields: {m: 2}}, {$set: {k: 3}}, {$unset: "n"}, {$replaceRoot: {newRoot: "$d"}}, {$unwind: "$items"}])`,
			want:      guard.Read,
		},
		{
			name:      "a quoted stage operator",
			statement: `db.orders.aggregate([{"$match": {paid: true}}])`,
			want:      guard.Read,
		},
		{
			name:      "aggregate with an options document",
			statement: "db.orders.aggregate([{$match: {}}], {allowDiskUse: true})",
			want:      guard.Read,
		},

		// The subpipeline-bearing stages, whose insides are analysed by the
		// same rule rather than trusted.
		{
			name:      "a facet of clean subpipelines",
			statement: `db.orders.aggregate([{$facet: {a: [{$match: {}}], b: [{$count: "n"}]}}])`,
			want:      guard.Read,
		},
		{
			name:      "a lookup with local and foreign fields",
			statement: `db.orders.aggregate([{$lookup: {from: "users", localField: "u", foreignField: "_id", as: "user"}}])`,
			want:      guard.Read,
		},
		{
			name:      "a lookup with a clean subpipeline",
			statement: `db.orders.aggregate([{$lookup: {from: "users", pipeline: [{$match: {}}], as: "user"}}])`,
			want:      guard.Read,
		},
		{
			name:      "a unionWith naming a collection",
			statement: `db.orders.aggregate([{$unionWith: "archive"}])`,
			want:      guard.Read,
		},
		{
			name:      "a unionWith with a clean subpipeline",
			statement: `db.orders.aggregate([{$unionWith: {coll: "archive", pipeline: [{$match: {}}]}}])`,
			want:      guard.Read,
		},
		{
			name:      "a graphLookup",
			statement: `db.people.aggregate([{$graphLookup: {from: "people", startWith: "$m", connectFromField: "m", connectToField: "_id", as: "chain"}}])`,
			want:      guard.Read,
		},
	})
}

func TestClassifyMongoMutations(t *testing.T) {
	runMongoCases(t, []mongoCase{
		// The floor: nothing that writes is on the list, and the refusal names
		// the operation the human wrote.
		{name: "insertOne", statement: "db.users.insertOne({name: \"ada\"})", want: guard.Mutation, reason: "insertOne"},
		{name: "insertMany", statement: "db.users.insertMany([{}])", want: guard.Mutation, reason: "insertMany"},
		{name: "updateOne", statement: "db.users.updateOne({}, {$set: {a: 1}})", want: guard.Mutation, reason: "updateOne"},
		{name: "updateMany", statement: "db.users.updateMany({}, {$set: {a: 1}})", want: guard.Mutation, reason: "updateMany"},
		{name: "replaceOne", statement: "db.users.replaceOne({}, {})", want: guard.Mutation, reason: "replaceOne"},
		{name: "deleteOne", statement: "db.users.deleteOne({})", want: guard.Mutation, reason: "deleteOne"},
		{name: "deleteMany", statement: "db.users.deleteMany({})", want: guard.Mutation, reason: "deleteMany"},
		{name: "drop", statement: "db.users.drop()", want: guard.Mutation, reason: "drop"},
		{name: "remove", statement: "db.users.remove({})", want: guard.Mutation, reason: "remove"},
		{name: "bulkWrite", statement: "db.users.bulkWrite([])", want: guard.Mutation, reason: "bulkWrite"},
		{name: "createIndex", statement: "db.users.createIndex({name: 1})", want: guard.Mutation, reason: "createIndex"},
		{name: "dropIndex", statement: `db.users.dropIndex("name_1")`, want: guard.Mutation},
		{name: "renameCollection", statement: `db.users.renameCollection("people")`, want: guard.Mutation},

		// Operations a reasonable person would call reads. Each of these is
		// listed in the trap table so the refusal says what they missed.
		{name: "count", statement: "db.users.count({})", want: guard.Mutation, reason: "countDocuments"},
		{name: "mapReduce", statement: `db.users.mapReduce(1, 2, {out: "totals"})`, want: guard.Mutation, reason: "out"},
		{name: "watch", statement: "db.users.watch([])", want: guard.Mutation, reason: "open"},
		{name: "findAndModify", statement: "db.users.findAndModify({})", want: guard.Mutation, reason: "writes"},
		{name: "findOneAndUpdate", statement: "db.users.findOneAndUpdate({}, {})", want: guard.Mutation, reason: "writes"},
		{name: "findOneAndReplace", statement: "db.users.findOneAndReplace({}, {})", want: guard.Mutation, reason: "writes"},
		{name: "findOneAndDelete", statement: "db.users.findOneAndDelete({})", want: guard.Mutation, reason: "writes"},
		{name: "explain", statement: "db.users.explain()", want: guard.Mutation, reason: "explain"},
		{name: "stats", statement: "db.users.stats()", want: guard.Mutation, reason: "stats"},
		{name: "validate", statement: "db.users.validate()", want: guard.Mutation, reason: "validate"},
		{name: "getIndexes", statement: "db.users.getIndexes()", want: guard.Mutation, reason: "getIndexes"},

		// Unknown operations, which is what a typo and a MongoDB 9 addition
		// look like from here. Both wait at the gate.
		{name: "an unknown operation", statement: "db.users.frobnicate({})", want: guard.Mutation, reason: "frobnicate"},
		{name: "a read's name with a typo", statement: "db.users.fnid({})", want: guard.Mutation, reason: "fnid"},
		{name: "a read's name in the wrong case", statement: "db.users.FIND({})", want: guard.Mutation, reason: "FIND"},
		{name: "a prefix of a read", statement: "db.users.fin({})", want: guard.Mutation},

		// The two writing stages, wherever they appear.
		{
			name:      "a pipeline ending in out",
			statement: `db.orders.aggregate([{$match: {}}, {$out: "totals"}])`,
			want:      guard.Mutation, reason: "$out",
		},
		{
			name:      "out as the only stage",
			statement: `db.orders.aggregate([{$out: "totals"}])`,
			want:      guard.Mutation, reason: "$out",
		},
		{
			name:      "a quoted out",
			statement: `db.orders.aggregate([{"$out": "totals"}])`,
			want:      guard.Mutation, reason: "$out",
		},
		{
			name:      "a pipeline ending in merge",
			statement: `db.orders.aggregate([{$merge: {into: "totals"}}])`,
			want:      guard.Mutation, reason: "$merge",
		},
		{
			name:      "out hidden inside a facet",
			statement: `db.orders.aggregate([{$facet: {a: [{$match: {}}, {$out: "totals"}]}}])`,
			want:      guard.Mutation, reason: "$out",
		},
		{
			name:      "merge hidden inside a lookup subpipeline",
			statement: `db.orders.aggregate([{$lookup: {from: "u", pipeline: [{$merge: {into: "t"}}], as: "x"}}])`,
			want:      guard.Mutation, reason: "$merge",
		},
		{
			name:      "out hidden inside a unionWith subpipeline",
			statement: `db.orders.aggregate([{$unionWith: {coll: "a", pipeline: [{$out: "t"}]}}])`,
			want:      guard.Mutation, reason: "$out",
		},
		{
			name:      "out nested two subpipelines deep",
			statement: `db.orders.aggregate([{$facet: {a: [{$lookup: {from: "u", pipeline: [{$out: "t"}], as: "x"}}]}}])`,
			want:      guard.Mutation, reason: "$out",
		},

		// The stage list is closed, so an unknown stage is a mutation whether
		// or not it writes: a blocklist of today's two writing stages would
		// inherit every writing stage MongoDB adds after this table was written.
		{
			name:      "an unknown stage",
			statement: "db.orders.aggregate([{$frobnicate: {}}])",
			want:      guard.Mutation, reason: "$frobnicate",
		},
		{
			name:      "a stage nobody has proved yet",
			statement: "db.orders.aggregate([{$collStats: {}}])",
			want:      guard.Mutation, reason: "$collStats",
		},
		{
			name:      "an unknown stage inside a facet",
			statement: "db.orders.aggregate([{$facet: {a: [{$frobnicate: {}}]}}])",
			want:      guard.Mutation, reason: "$frobnicate",
		},
		{
			name:      "a stage operator that is not even a name",
			statement: `db.orders.aggregate([{"not a stage name": 1}])`,
			want:      guard.Mutation, reason: "stage",
		},

		// A pipeline go-db cannot read as a list of stages is a pipeline whose
		// stages it cannot judge, which is the same thing as a mutation.
		{name: "aggregate with no pipeline", statement: "db.orders.aggregate()", want: guard.Mutation, reason: "pipeline"},
		{name: "a pipeline that is not an array", statement: "db.orders.aggregate({$match: {}})", want: guard.Mutation, reason: "array"},
		{name: "a pipeline that is a string", statement: `db.orders.aggregate("[]")`, want: guard.Mutation, reason: "array"},
		{name: "a stage that is not an object", statement: `db.orders.aggregate(["$match"])`, want: guard.Mutation, reason: "stage"},
		{name: "a stage that is an array", statement: "db.orders.aggregate([[{$match: {}}]])", want: guard.Mutation, reason: "stage"},
		{name: "a stage with two operators", statement: "db.orders.aggregate([{$match: {}, $limit: 1}])", want: guard.Mutation, reason: "stage"},
		{name: "a stage with no operator", statement: "db.orders.aggregate([{}])", want: guard.Mutation, reason: "stage"},
		{name: "aggregate with more than a pipeline and options", statement: "db.orders.aggregate([], {}, {})", want: guard.Mutation, reason: "pipeline"},
		{name: "a facet whose subpipeline is not an array", statement: `db.orders.aggregate([{$facet: {a: {$match: {}}}}])`, want: guard.Mutation, reason: "array"},
		{name: "a facet that is not an object", statement: "db.orders.aggregate([{$facet: []}])", want: guard.Mutation, reason: "$facet"},
		{name: "a lookup that is not an object", statement: `db.orders.aggregate([{$lookup: "users"}])`, want: guard.Mutation, reason: "$lookup"},
		{name: "a lookup whose pipeline is not an array", statement: `db.orders.aggregate([{$lookup: {from: "u", pipeline: 1, as: "x"}}])`, want: guard.Mutation, reason: "array"},
		{name: "a unionWith that is neither a name nor an object", statement: "db.orders.aggregate([{$unionWith: 1}])", want: guard.Mutation, reason: "$unionWith"},

		// Nothing to run.
		{name: "empty", statement: "", want: guard.Mutation, reason: "no statement"},
		{name: "whitespace only", statement: "  \n\t ", want: guard.Mutation, reason: "no statement"},

		// Not the grammar. The refusal says so — MongoDB is judged here, it is
		// not unjudgeable — and it names the shape go-db does accept.
		{name: "SQL", statement: "SELECT 1", want: guard.Mutation, reason: "db."},
		{name: "a Redis command", statement: "GET user:1", want: guard.Mutation, reason: "db."},
		{name: "use", statement: "use mydb", want: guard.Mutation, reason: "MongoDB"},
		{name: "show collections", statement: "show collections", want: guard.Mutation, reason: "MongoDB"},
		{name: "getCollection", statement: `db.getCollection("users").find({})`, want: guard.Mutation, reason: "collection"},
		{name: "bracket syntax", statement: `db["users"].find({})`, want: guard.Mutation, reason: "collection"},
		{name: "a dotted collection", statement: "db.reporting.users.find({})", want: guard.Mutation},
		{name: "a chained call", statement: "db.users.find({}).limit(10)", want: guard.Mutation},
		{name: "a cursor helper", statement: "db.users.find({}).forEach(printjson)", want: guard.Mutation},

		// One call per buffer, and a second one cannot hide in any position.
		{name: "a read then a drop", statement: "db.users.find({}); db.users.drop()", want: guard.Mutation},
		{name: "a read then a drop on two lines", statement: "db.users.find({})\ndb.users.drop()", want: guard.Mutation},
		{name: "a read then a drop with no separator", statement: "db.users.find({}) db.users.drop()", want: guard.Mutation},
		{name: "two reads are still two calls", statement: "db.a.find({}); db.b.find({})", want: guard.Mutation},

		// A second call inside a string is a string, and a closing parenthesis
		// inside a string does not end the call.
		{name: "a drop inside a string value", statement: `db.users.find({name: "); db.users.drop("})`, want: guard.Read},
		{name: "an unterminated string", statement: `db.users.find({name: "})`, want: guard.Mutation, reason: "string"},

		// Values are JSON. Anything that would have to be evaluated is refused,
		// and the refusal says what to write instead where there is an answer.
		{name: "ObjectId", statement: `db.users.find({_id: ObjectId("5f1d7f2b1c9d440000a1b2c3")})`, want: guard.Mutation, reason: "$oid"},
		{name: "ISODate", statement: `db.events.find({at: ISODate("2026-08-11")})`, want: guard.Mutation, reason: "$date"},
		{name: "a bare identifier", statement: "db.users.find({name: ada})", want: guard.Mutation},
		{name: "a function", statement: "db.users.find({$where: function () { return true }})", want: guard.Mutation},

		// A refusal is not a place to echo the input back into the badge and
		// the audit log.
		{name: "a very long collection name", statement: "db." + strings.Repeat("a", 5000) + ".find({})", want: guard.Mutation},
		{name: "a very long operation name", statement: "db.users." + strings.Repeat("a", 5000) + "({})", want: guard.Mutation},
		{name: "a very long stage operator", statement: `db.o.aggregate([{"$` + strings.Repeat("a", 5000) + `": 1}])`, want: guard.Mutation},
		{name: "a megabyte of statement", statement: "db.users.find({a: \"" + strings.Repeat("x", 1<<20) + "\"})", want: guard.Mutation, reason: "long"},
	})
}

// The depth bound belongs to the parser, and the classifier inherits it: a
// filter nobody could have written by hand is refused rather than descended
// into.
func TestClassifyMongoRefusesDeepNesting(t *testing.T) {
	deep := "db.c.find(" + strings.Repeat("{a:", 5000) + "1" + strings.Repeat("}", 5000) + ")"

	if got := guard.ClassifyMongo(deep); got.IsRead() {
		t.Errorf("ClassifyMongo of a 5000-deep filter = %+v, want a mutation", got)
	}
}

func runMongoCases(t *testing.T, cases []mongoCase) {
	t.Helper()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := guard.ClassifyMongo(tc.statement)

			if got.Kind != tc.want {
				t.Errorf("ClassifyMongo(%.80q).Kind = %q, want %q (reason: %s)", tc.statement, got.Kind, tc.want, got.Reason)
			}
			if got.Reason == "" {
				t.Fatalf("ClassifyMongo(%.80q) gave no reason; the human is told why their statement was stopped", tc.statement)
			}
			if strings.ContainsAny(got.Reason, "\n\r\t") {
				t.Errorf("reason spans lines, want one line of prose:\n%q", got.Reason)
			}
			if len(got.Reason) > 280 {
				t.Errorf("reason is %d bytes, want a line that fits a badge:\n%s", len(got.Reason), got.Reason)
			}
			if tc.reason != "" && !strings.Contains(got.Reason, tc.reason) {
				t.Errorf("ClassifyMongo(%.80q).Reason = %q, want it to mention %q", tc.statement, got.Reason, tc.reason)
			}
			if got.IsRead() != (tc.want == guard.Read) {
				t.Errorf("IsRead() = %v, disagrees with Kind %q", got.IsRead(), got.Kind)
			}
		})
	}
}
