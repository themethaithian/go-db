package mongoql_test

import (
	"strings"
	"testing"

	"github.com/themethaithian/go-db/internal/mongoql"
)

// These tests are the executable specification of the grammar go-db accepts for
// MongoDB (ADR-0006). The grammar is deliberately small: it is not JavaScript,
// and every limit in it is a limit on purpose, because the classifier that
// reads its output is the only thing between an AI-written statement and the
// database.
//
// Two claims are tested here and nowhere else. The first is that a parse
// succeeds only on the one shape the grammar names — one call, one plain
// collection, one operation, JSON-ish arguments — so that a second call cannot
// be hidden after it, inside a string, or behind a function. The second is that
// what comes out is faithful: the collection and operation as written, and each
// argument rendered back as strict JSON the MongoDB driver can unmarshal.

func TestParseAcceptsTheGrammar(t *testing.T) {
	cases := []struct {
		name       string
		statement  string
		collection string
		verb       string
		args       []string // each argument's canonical JSON
	}{
		{
			name:       "a find with an empty filter",
			statement:  "db.users.find({})",
			collection: "users", verb: "find", args: []string{"{}"},
		},
		{
			name:       "no arguments at all",
			statement:  "db.users.estimatedDocumentCount()",
			collection: "users", verb: "estimatedDocumentCount", args: []string{},
		},
		{
			name:       "a filter and a projection",
			statement:  `db.users.find({name: "ada"}, {name: 1, _id: 0})`,
			collection: "users", verb: "find",
			args: []string{`{"name":"ada"}`, `{"name":1,"_id":0}`},
		},
		{
			name:       "quoted keys",
			statement:  `db.users.find({"name": "ada"})`,
			collection: "users", verb: "find", args: []string{`{"name":"ada"}`},
		},
		{
			name:       "dotted field names, which have to be quoted",
			statement:  `db.users.find({"address.city": "berlin"})`,
			collection: "users", verb: "find", args: []string{`{"address.city":"berlin"}`},
		},
		{
			name:       "operator keys start with a dollar",
			statement:  "db.users.find({age: {$gte: 18}})",
			collection: "users", verb: "find", args: []string{`{"age":{"$gte":18}}`},
		},
		{
			name:       "single-quoted strings",
			statement:  "db.users.find({name: 'ada'})",
			collection: "users", verb: "find", args: []string{`{"name":"ada"}`},
		},
		{
			name:       "a double quote inside a single-quoted string",
			statement:  `db.users.find({name: 'say "hi"'})`,
			collection: "users", verb: "find", args: []string{`{"name":"say \"hi\""}`},
		},
		{
			name:       "escapes inside a string",
			statement:  `db.users.find({note: "a\tb\nc\\d\"eé"})`,
			collection: "users", verb: "find", args: []string{`{"note":"a\tb\nc\\d\"eé"}`},
		},
		{
			name:       "an escaped single quote inside a single-quoted string",
			statement:  `db.users.find({name: 'it\'s'})`,
			collection: "users", verb: "find", args: []string{`{"name":"it's"}`},
		},
		{
			name:       "a surrogate pair",
			statement:  `db.users.find({emoji: "😀"})`,
			collection: "users", verb: "find", args: []string{`{"emoji":"😀"}`},
		},
		{
			name:       "numbers of every shape the grammar has",
			statement:  "db.metrics.find({a: 0, b: -1, c: 1.5, d: -0.25, e: 1e3, f: 2.5E-4, g: -0})",
			collection: "metrics", verb: "find",
			args: []string{`{"a":0,"b":-1,"c":1.5,"d":-0.25,"e":1e3,"f":2.5E-4,"g":-0}`},
		},
		{
			name:       "the three keywords",
			statement:  "db.users.find({active: true, deleted: false, note: null})",
			collection: "users", verb: "find",
			args: []string{`{"active":true,"deleted":false,"note":null}`},
		},
		{
			name:       "arrays, including empty and nested ones",
			statement:  "db.users.find({tags: [], ids: [1, 2, [3]], any: [{a: 1}]})",
			collection: "users", verb: "find",
			args: []string{`{"tags":[],"ids":[1,2,[3]],"any":[{"a":1}]}`},
		},
		{
			name:       "extended JSON is just an object",
			statement:  `db.users.find({_id: {"$oid": "5f1d7f2b1c9d440000a1b2c3"}})`,
			collection: "users", verb: "find",
			args: []string{`{"_id":{"$oid":"5f1d7f2b1c9d440000a1b2c3"}}`},
		},
		{
			name:       "a date as extended JSON",
			statement:  `db.events.find({at: {$date: "2026-08-11T00:00:00Z"}})`,
			collection: "events", verb: "find",
			args: []string{`{"at":{"$date":"2026-08-11T00:00:00Z"}}`},
		},
		{
			name:       "an aggregation pipeline",
			statement:  `db.orders.aggregate([{$match: {paid: true}}, {$group: {_id: "$region", n: {$sum: 1}}}])`,
			collection: "orders", verb: "aggregate",
			args: []string{`[{"$match":{"paid":true}},{"$group":{"_id":"$region","n":{"$sum":1}}}]`},
		},
		{
			name:       "a bare string argument",
			statement:  `db.users.distinct("country")`,
			collection: "users", verb: "distinct", args: []string{`"country"`},
		},
		{
			name:       "whitespace and newlines between every token",
			statement:  "  db\n  .\n  users\n  .\n  find\n  (\n    { name : \"ada\" }\n  )\n  ",
			collection: "users", verb: "find", args: []string{`{"name":"ada"}`},
		},
		{
			name:       "one trailing semicolon",
			statement:  "db.users.find({});",
			collection: "users", verb: "find", args: []string{"{}"},
		},
		{
			name:       "a trailing semicolon with whitespace around it",
			statement:  "db.users.find({})  ;  \n",
			collection: "users", verb: "find", args: []string{"{}"},
		},
		{
			name:       "an underscore-led collection and a digit in it",
			statement:  "db._audit2.find({})",
			collection: "_audit2", verb: "find", args: []string{"{}"},
		},
		{
			name:       "a closing parenthesis inside a string is not the end of the call",
			statement:  `db.users.find({name: ") db.other.drop("})`,
			collection: "users", verb: "find", args: []string{`{"name":") db.other.drop("}`},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			call, err := mongoql.Parse(tc.statement)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.statement, err)
			}
			if call.Collection != tc.collection {
				t.Errorf("Collection = %q, want %q", call.Collection, tc.collection)
			}
			if call.Verb != tc.verb {
				t.Errorf("Verb = %q, want %q", call.Verb, tc.verb)
			}
			if len(call.Args) != len(tc.args) {
				t.Fatalf("got %d arguments, want %d", len(call.Args), len(tc.args))
			}
			for i, want := range tc.args {
				if got := call.Args[i].JSON(); got != want {
					t.Errorf("argument %d rendered as %s, want %s", i, got, want)
				}
			}
		})
	}
}

// The database-level form: db.<verb>(<args>), naming no collection. It is the
// second and last shape the grammar has, and the parser tells the two apart by
// what follows the name after db — a bracket makes it an operation on the
// database, a dot makes it a collection.
//
// Parsing one is not accepting it: the classifier's own database-level
// allowlist is what decides whether it runs, and it holds one entry.
func TestParseAcceptsDatabaseLevelCalls(t *testing.T) {
	cases := []struct {
		name      string
		statement string
		verb      string
		args      []string
	}{
		{
			name:      "the collection names of the database",
			statement: "db.getCollectionNames()",
			verb:      "getCollectionNames", args: []string{},
		},
		{
			name:      "one trailing semicolon",
			statement: "db.getCollectionNames();",
			verb:      "getCollectionNames", args: []string{},
		},
		{
			name:      "whitespace between every token",
			statement: "  db\n  .\n  getCollectionNames\n  (\n  )\n  ",
			verb:      "getCollectionNames", args: []string{},
		},
		{
			// Parsed, and refused by the classifier — which is the division of
			// labour: the grammar reads the shape, the allowlist judges the verb.
			name:      "a database operation with arguments",
			statement: `db.runCommand({ping: 1})`,
			verb:      "runCommand", args: []string{`{"ping":1}`},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			call, err := mongoql.Parse(tc.statement)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.statement, err)
			}
			if !call.OnDatabase() {
				t.Errorf("OnDatabase() = false, want true for %q", tc.statement)
			}
			if call.Collection != "" {
				t.Errorf("Collection = %q, want empty — this form names none", call.Collection)
			}
			if call.Verb != tc.verb {
				t.Errorf("Verb = %q, want %q", call.Verb, tc.verb)
			}
			if len(call.Args) != len(tc.args) {
				t.Fatalf("got %d arguments, want %d", len(call.Args), len(tc.args))
			}
			for i, want := range tc.args {
				if got := call.Args[i].JSON(); got != want {
					t.Errorf("argument %d rendered as %s, want %s", i, got, want)
				}
			}
		})
	}
}

// The two forms are told apart, and a collection call still says so. Without
// this the database form would be a way to make a collection call look like
// something it is not to a caller that only reads Verb.
func TestParseTellsTheTwoFormsApart(t *testing.T) {
	collection, err := mongoql.Parse("db.users.find({})")
	if err != nil {
		t.Fatalf("Parse(collection call): %v", err)
	}
	if collection.OnDatabase() {
		t.Error("OnDatabase() = true for db.users.find({}), want false")
	}

	database, err := mongoql.Parse("db.getCollectionNames()")
	if err != nil {
		t.Fatalf("Parse(database call): %v", err)
	}
	if !database.OnDatabase() {
		t.Error("OnDatabase() = false for db.getCollectionNames(), want true")
	}
}

// Every refusal below is a Mutation once the classifier sees it, so the list is
// as much a safety claim as the classifier's own table: anything the parser
// lets through is something the verb allowlist then has to be right about.
func TestParseRefuses(t *testing.T) {
	cases := []struct {
		name      string
		statement string
		// says, when set, is a substring the error must contain, for the
		// refusals where telling the human what to write instead is the point.
		says string
	}{
		// Nothing to parse.
		{name: "empty", statement: ""},
		{name: "whitespace only", statement: "  \n\t "},

		// Not the grammar at all.
		{name: "SQL", statement: "SELECT 1", says: "db"},
		{name: "a Redis command", statement: "GET user:1", says: "db"},
		{name: "use database", statement: "use mydb", says: "db"},
		{name: "show collections", statement: "show collections", says: "db"},
		{name: "a bare db", statement: "db"},
		{name: "db with no operation", statement: "db.users"},
		{name: "db.users. with nothing after it", statement: "db.users."},
		{name: "a call with no parentheses", statement: "db.users.find"},
		{name: "an unclosed call", statement: "db.users.find({}"},
		{name: "a stray closing parenthesis", statement: "db.users.find)"},
		{name: "something that only starts like db", statement: "dbx.users.find({})", says: "db"},

		// Ways of naming a collection that the grammar does not have.
		{name: "getCollection", statement: `db.getCollection("users").find({})`, says: "collection"},
		{name: "bracket syntax", statement: `db["users"].find({})`, says: "collection"},
		{name: "a dotted collection name", statement: "db.reporting.users.find({})"},
		{name: "a collection starting with a digit", statement: "db.2users.find({})", says: "collection"},
		{name: "a collection with a dollar in it", statement: "db.us$ers.find({})"},
		{name: "a hyphenated collection", statement: "db.user-events.find({})"},

		// The database-level form is one call too, and nothing may be chained
		// onto it — which is also what db.getCollection("x").find({}) is.
		{name: "a database call with no parentheses", statement: "db.getCollectionNames"},
		{name: "a chained database call", statement: "db.getCollectionNames().length", says: "chaining"},
		{name: "a second call after a database call", statement: "db.getCollectionNames(); db.a.drop()", says: "one"},
		{name: "an unclosed database call", statement: "db.getCollectionNames("},

		// One call, exactly. A second one cannot hide in any splice position.
		{name: "two calls separated by a semicolon", statement: "db.a.find({}); db.b.drop()", says: "one"},
		{name: "two calls on two lines", statement: "db.a.find({})\ndb.b.drop()", says: "one"},
		{name: "two calls with no separator", statement: "db.a.find({}) db.b.drop()", says: "one"},
		{name: "a second call after two semicolons", statement: "db.a.find({});;", says: "one"},
		{name: "a chained call", statement: "db.a.find({}).limit(1)", says: "one"},
		{name: "a chained call on the collection", statement: "db.a.explain().find({})", says: "one"},
		{name: "trailing junk", statement: "db.a.find({}) // and now something else", says: "one"},
		{name: "a comment before the call", statement: "// comment\ndb.a.find({})"},
		{name: "a block comment inside the arguments", statement: "db.a.find({/* x */})"},
		{name: "a semicolon inside the arguments", statement: "db.a.find({}; db.b.drop())"},

		// Values are JSON, and nothing that has to be evaluated.
		{name: "ObjectId", statement: `db.users.find({_id: ObjectId("5f1d7f2b1c9d440000a1b2c3")})`, says: "$oid"},
		{name: "ISODate", statement: `db.events.find({at: ISODate("2026-08-11")})`, says: "$date"},
		{name: "new Date", statement: "db.events.find({at: new Date()})"},
		{name: "NumberLong", statement: `db.c.find({n: NumberLong("1")})`, says: "$numberLong"},
		{name: "a bare identifier as a value", statement: "db.users.find({name: ada})"},
		{name: "a variable", statement: "db.users.find(filter)"},
		{name: "undefined", statement: "db.users.find({a: undefined})"},
		{name: "NaN", statement: "db.users.find({a: NaN})"},
		{name: "a function expression", statement: "db.users.find({$where: function () { return true }})"},
		{name: "an arrow function", statement: "db.users.find({$where: () => true})"},
		{name: "a regex literal", statement: "db.users.find({name: /ada/i})"},
		{name: "a template literal", statement: "db.users.find({name: `ada`})"},
		{name: "string concatenation", statement: `db.users.find({name: "a" + "b"})`},

		// Strings, where a naive scanner would lose track of the nesting.
		{name: "an unterminated string", statement: `db.users.find({name: "ada})`, says: "string"},
		{name: "an unterminated single-quoted string", statement: "db.users.find({name: 'ada})", says: "string"},
		{name: "a string broken over two lines", statement: "db.users.find({name: \"a\nb\"})", says: "string"},
		{name: "a raw tab inside a string", statement: "db.users.find({name: \"a\tb\"})", says: "string"},
		{name: "an unknown escape", statement: `db.users.find({name: "a\qb"})`, says: "escape"},
		{name: "a truncated unicode escape", statement: `db.users.find({name: "\u12"})`, says: "escape"},
		{name: "an unpaired surrogate", statement: `db.users.find({name: "\ud83d"})`, says: "escape"},
		{name: "a trailing backslash", statement: `db.users.find({name: "a\`},

		// Object and array shapes.
		{name: "a missing colon", statement: "db.users.find({name \"ada\"})"},
		{name: "a missing value", statement: "db.users.find({name:})"},
		{name: "a trailing comma in an object", statement: "db.users.find({a: 1,})", says: "comma"},
		{name: "a trailing comma in an array", statement: "db.users.find({a: [1,]})", says: "comma"},
		{name: "a trailing comma in the argument list", statement: "db.users.find({},)", says: "comma"},
		{name: "a hole in an array", statement: "db.users.find({a: [1,,2]})"},
		{name: "an unclosed object", statement: "db.users.find({a: 1)"},
		{name: "an unclosed array", statement: "db.users.find([{a: 1}"},
		{name: "mismatched brackets", statement: "db.users.find({a: [1})"},
		{name: "a missing comma between arguments", statement: "db.users.find({} {})"},

		// Numbers that JSON does not have.
		{name: "a leading zero", statement: "db.c.find({a: 01})", says: "number"},
		{name: "a leading dot", statement: "db.c.find({a: .5})"},
		{name: "a trailing dot", statement: "db.c.find({a: 1.})", says: "number"},
		{name: "a leading plus", statement: "db.c.find({a: +1})"},
		{name: "hexadecimal", statement: "db.c.find({a: 0x1f})", says: "number"},
		{name: "an exponent with no digits", statement: "db.c.find({a: 1e})", says: "number"},
		{name: "a bare minus", statement: "db.c.find({a: -})", says: "number"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			call, err := mongoql.Parse(tc.statement)
			if err == nil {
				t.Fatalf("Parse(%q) = %+v, want a refusal", tc.statement, call)
			}
			assertUsableReason(t, err.Error())
			if tc.says != "" && !strings.Contains(err.Error(), tc.says) {
				t.Errorf("error = %q, want it to mention %q", err, tc.says)
			}
		})
	}
}

// The depth bound is not a style rule: a recursive-descent parser with no bound
// is a stack the input chooses the size of.
func TestParseBoundsNesting(t *testing.T) {
	nest := func(depth int) string {
		return "db.c.find(" + strings.Repeat("{a:", depth) + "1" + strings.Repeat("}", depth) + ")"
	}

	if _, err := mongoql.Parse(nest(24)); err != nil {
		t.Errorf("Parse at depth 24: %v, want it accepted — real filters nest a handful deep", err)
	}
	deep, err := mongoql.Parse(nest(5000))
	if err == nil {
		t.Fatalf("Parse at depth 5000 = %+v, want a refusal", deep)
	}
	assertUsableReason(t, err.Error())
	if !strings.Contains(err.Error(), "nested") {
		t.Errorf("error = %q, want it to say the value nests too deeply", err)
	}
}

func TestParseBoundsLength(t *testing.T) {
	huge := "db.c.find({a: \"" + strings.Repeat("x", 1<<20) + "\"})"

	_, err := mongoql.Parse(huge)
	if err == nil {
		t.Fatal("Parse of a megabyte-long statement succeeded, want a refusal")
	}
	assertUsableReason(t, err.Error())
	if !strings.Contains(err.Error(), "long") {
		t.Errorf("error = %q, want it to say the statement is too long", err)
	}
}

// A refused statement's text is written by whoever submitted it, and the
// refusal is quoted into the editor badge and the audit log. It says what is
// wrong and where; it is not a channel for echoing the input back.
func assertUsableReason(t *testing.T, reason string) {
	t.Helper()

	const longestReason = 240
	switch {
	case reason == "":
		t.Error("the refusal carries no reason; the human is told why their statement was stopped")
	case len(reason) > longestReason:
		t.Errorf("reason is %d bytes, want at most %d — it is one line in a badge:\n%s", len(reason), longestReason, reason)
	case strings.ContainsAny(reason, "\n\r\t"):
		t.Errorf("reason spans lines, want one line of prose:\n%q", reason)
	}
}

// The position is what makes a one-line refusal actionable: it says where in
// the statement the parser stopped, counted in characters a human can find.
func TestParseSaysWhereItStopped(t *testing.T) {
	_, err := mongoql.Parse("db.users.find({name: ada})")
	if err == nil {
		t.Fatal("Parse succeeded, want a refusal")
	}
	if !strings.Contains(err.Error(), "character 22") {
		t.Errorf("error = %q, want it to point at character 22, where ada starts", err)
	}
}

// The parsed value tree is the classifier's half of the contract: it asks
// whether a pipeline is an array, and what each stage's single operator is
// called. JSON() is the driver's half.
func TestParsedValuesAreInspectable(t *testing.T) {
	call, err := mongoql.Parse(`db.orders.aggregate([{$match: {paid: true}}, {$count: "n"}])`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(call.Args) != 1 {
		t.Fatalf("got %d arguments, want 1", len(call.Args))
	}

	pipeline := call.Args[0]
	if pipeline.Kind() != mongoql.KindArray {
		t.Fatalf("pipeline Kind = %v, want %v", pipeline.Kind(), mongoql.KindArray)
	}
	stages := pipeline.Elements()
	if len(stages) != 2 {
		t.Fatalf("got %d stages, want 2", len(stages))
	}

	var names []string
	for _, stage := range stages {
		if stage.Kind() != mongoql.KindObject {
			t.Fatalf("stage Kind = %v, want %v", stage.Kind(), mongoql.KindObject)
		}
		fields := stage.Fields()
		if len(fields) != 1 {
			t.Fatalf("stage has %d fields, want 1", len(fields))
		}
		names = append(names, fields[0].Name)
	}
	if names[0] != "$match" || names[1] != "$count" {
		t.Errorf("stage operators = %v, want [$match $count]", names)
	}

	if got := stages[0].Fields()[0].Value.JSON(); got != `{"paid":true}` {
		t.Errorf("$match argument = %s, want {\"paid\":true}", got)
	}
	if kind := stages[1].Fields()[0].Value.Kind(); kind != mongoql.KindString {
		t.Errorf("$count argument Kind = %v, want %v", kind, mongoql.KindString)
	}
}

// Elements and Fields answer for the kind they belong to and nothing else, so a
// caller that forgets to check the kind gets nothing rather than something from
// another arm.
func TestValueAccessorsAnswerOnlyForTheirKind(t *testing.T) {
	call, err := mongoql.Parse(`db.c.find({a: [1]}, "s", 2, true, null)`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	object, str, number, boolean, null := call.Args[0], call.Args[1], call.Args[2], call.Args[3], call.Args[4]
	kinds := []struct {
		value mongoql.Value
		want  mongoql.Kind
	}{
		{object, mongoql.KindObject},
		{str, mongoql.KindString},
		{number, mongoql.KindNumber},
		{boolean, mongoql.KindBool},
		{null, mongoql.KindNull},
	}
	for _, k := range kinds {
		if got := k.value.Kind(); got != k.want {
			t.Errorf("Kind() = %v, want %v", got, k.want)
		}
	}

	if got := object.Elements(); got != nil {
		t.Errorf("Elements() on an object = %v, want nil", got)
	}
	if got := str.Fields(); got != nil {
		t.Errorf("Fields() on a string = %v, want nil", got)
	}
	if got := object.Fields()[0].Value.Elements(); len(got) != 1 {
		t.Errorf("Elements() on an array gave %d, want 1", len(got))
	}
}

// The JSON an argument renders to is the argument as the parser understood it,
// not the bytes the human typed: single quotes become double, unquoted keys get
// quoted, and nothing the parser did not understand can survive the round trip
// into what the driver will unmarshal.
func TestJSONIsCanonical(t *testing.T) {
	call, err := mongoql.Parse(`db.c.find({ a : 'x' , 'b' : [ 1 , { c : "" } ] })`)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	const want = `{"a":"x","b":[1,{"c":""}]}`
	if got := call.Args[0].JSON(); got != want {
		t.Errorf("JSON() = %s, want %s", got, want)
	}
}
