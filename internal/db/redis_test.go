package db

// These are the Redis adapter's unit tests. They are an internal test — package
// db, not db_test — because what they pin is machinery this package hides on
// purpose: the tokenizer, the reply-tree builder and its caps, and the database
// index a Profile's Database means for this Engine. None of that is exported,
// and a test that walked an exported copy would be pinning the copy.
//
// Everything that needs a server is in redis_integration_test.go.

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/big"
	"strings"
	"testing"

	"github.com/themethaithian/go-db/internal/guard"
)

func TestTokenizeRedisCommandSplitsOnWhitespace(t *testing.T) {
	for _, test := range []struct {
		name    string
		command string
		want    []string
	}{
		{"one command and one key", "GET key", []string{"GET", "key"}},
		{"runs of spaces are one separator", "  GET    key   ", []string{"GET", "key"}},
		{"tabs separate too", "GET\tkey", []string{"GET", "key"}},
		{"nothing at all is no arguments", "   ", nil},
		{"a container command keeps its subcommand", "CONFIG GET maxmemory", []string{"CONFIG", "GET", "maxmemory"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := tokenizeRedisCommand(test.command)
			if err != nil {
				t.Fatalf("tokenizeRedisCommand(%q): %v", test.command, err)
			}
			assertTokens(t, test.command, got, test.want)
		})
	}
}

func TestTokenizeRedisCommandReadsQuotedArguments(t *testing.T) {
	for _, test := range []struct {
		name    string
		command string
		want    []string
	}{
		{"a double-quoted argument holds its spaces", `SET k "hello world"`, []string{"SET", "k", "hello world"}},
		{"a single-quoted argument holds its spaces", `SET k 'hello world'`, []string{"SET", "k", "hello world"}},
		{"an escaped double quote is a quote", `SET k "say \"hi\""`, []string{"SET", "k", `say "hi"`}},
		{"an escaped single quote is a quote", `SET k 'it\'s'`, []string{"SET", "k", "it's"}},
		{"the letter escapes are their characters", `SET k "a\nb\tc\rd\be\af"`, []string{"SET", "k", "a\nb\tc\rd\be\af"}},
		{"an unknown escape is the character itself", `SET k "a\qb"`, []string{"SET", "k", "aqb"}},
		{"a hex escape is one byte", `SET k "\x41\x62"`, []string{"SET", "k", "Ab"}},
		{"a short hex escape is not one", `SET k "\x4"`, []string{"SET", "k", "x4"}},
		{"a backslash is literal inside single quotes", `SET k 'a\nb'`, []string{"SET", "k", `a\nb`}},
		{"an empty quoted argument is an empty argument", `SET k ""`, []string{"SET", "k", ""}},
		{"quotes may open inside a bare argument", `GET foo"bar baz"`, []string{"GET", "foobar baz"}},
		{"a quoted run may be followed by another argument", `GET "a b" "c d"`, []string{"GET", "a b", "c d"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := tokenizeRedisCommand(test.command)
			if err != nil {
				t.Fatalf("tokenizeRedisCommand(%q): %v", test.command, err)
			}
			assertTokens(t, test.command, got, test.want)
		})
	}
}

// TestTokenizeRedisCommandRefusesUnbalancedQuotes is the loud-failure half. A
// command line nobody can split is not run on a best guess: the argument the
// human meant is exactly what is in doubt, and Redis would be handed a
// different one.
func TestTokenizeRedisCommandRefusesUnbalancedQuotes(t *testing.T) {
	for _, test := range []struct {
		name    string
		command string
		want    error
	}{
		{"a double quote that never closes", `GET "key`, errRedisUnterminatedQuote},
		{"a single quote that never closes", `GET 'key`, errRedisUnterminatedQuote},
		{"a closing quote escaped away", `GET "key\"`, errRedisUnterminatedQuote},
		{"an empty double quote that never closes", `GET "`, errRedisUnterminatedQuote},
		{"text glued to a closing double quote", `GET "key"x`, errRedisQuoteRun},
		{"text glued to a closing single quote", `GET 'key'x`, errRedisQuoteRun},
		{"two quoted runs with no space between", `GET "a""b"`, errRedisQuoteRun},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := tokenizeRedisCommand(test.command)
			if err == nil {
				t.Fatalf("tokenizeRedisCommand(%q) = %q, want it refused", test.command, got)
			}
			if !errors.Is(err, test.want) {
				t.Errorf("tokenizeRedisCommand(%q) error = %v, want it to wrap %v", test.command, err, test.want)
			}
		})
	}
}

// TestTokenizeRedisCommandAgreesWithTheClassifier is the invariant the whole
// arrangement rests on: the tokens the classifier judged are the tokens that
// reach the server. The classifier reads the first field, and for a container
// the second, with strings.Fields; this test asserts that for every line it
// calls a read, the tokenizer produces exactly those.
//
// It holds for a reason rather than by luck. A quote anywhere in the first or
// second field puts a character in it that guard.commandName refuses, so the
// classifier calls the line a Mutation and it never reaches the tokenizer at
// all — which leaves only fields the two agree about by construction.
func TestTokenizeRedisCommandAgreesWithTheClassifier(t *testing.T) {
	for _, command := range []string{
		"GET key",
		"  get   key  ",
		`GET "a b"`,
		`GET foo"bar baz"`,
		"CONFIG GET maxmemory",
		"  config\tget\tmaxmemory ",
		`OBJECT ENCODING "a key"`,
		"LRANGE list 0 -1",
	} {
		t.Run(command, func(t *testing.T) {
			if verdict := guard.ClassifyRedis(command); !verdict.IsRead() {
				t.Fatalf("ClassifyRedis(%q) = %s, want a read for this case to say anything", command, verdict.Reason)
			}

			tokens, err := tokenizeRedisCommand(command)
			if err != nil {
				t.Fatalf("tokenizeRedisCommand(%q): %v", command, err)
			}
			fields := strings.Fields(command)
			if len(tokens) == 0 {
				t.Fatalf("tokenizeRedisCommand(%q) produced no arguments for a line the classifier read", command)
			}
			if tokens[0] != fields[0] {
				t.Errorf("the classifier judged command %q and the tokenizer will send %q", fields[0], tokens[0])
			}
			// The second token only has to agree where the classifier read it,
			// which is exactly the container commands.
			if len(fields) > 1 && len(tokens) > 1 && isRedisContainerLine(command) && tokens[1] != fields[1] {
				t.Errorf("the classifier judged subcommand %q and the tokenizer will send %q", fields[1], tokens[1])
			}
		})
	}
}

// isRedisContainerLine reports whether command names one of the container
// commands whose subcommand the classifier reads. The list is spelled out here
// rather than borrowed from guard: guard keeps its tables unexported, and a
// test that reached for them would be asking this package to widen someone
// else's API.
func isRedisContainerLine(command string) bool {
	fields := strings.Fields(command)
	if len(fields) == 0 {
		return false
	}
	switch strings.ToUpper(fields[0]) {
	case "CONFIG", "CLIENT", "OBJECT", "XINFO", "COMMAND", "MEMORY", "LATENCY", "ACL":
		return true
	}
	return false
}

// TestQuoteRedisArgumentRoundTripsThroughTheTokenizer is the property the
// quoter exists for, and the only one worth asserting: whatever bytes go in
// come back out of the tokenizer as one argument, in the position they were
// written into. Quoting that is merely plausible is quoting that names a
// different key, and a caller building SCAN ... MATCH from a human's typing has
// no way to notice.
//
// The pairing is checked against the tokenizer rather than against an expected
// piece of text, because the tokenizer is what the argument actually meets: the
// adapter re-splits the command line it is handed, so a round trip through it
// is the whole contract.
func TestQuoteRedisArgumentRoundTripsThroughTheTokenizer(t *testing.T) {
	for _, test := range []struct {
		name     string
		argument string
	}{
		{"a plain key", "user:1"},
		{"an empty argument", ""},
		{"spaces, which would otherwise be two arguments", "hello world"},
		{"a double quote, which would otherwise close the run", `say "hi"`},
		{"a backslash, which would otherwise escape its neighbour", `c:\keys\1`},
		{"a backslash before a quote", `trailing\`},
		{"a single quote, which needs nothing inside a double-quoted run", "it's"},
		{"a newline, which the classifier refuses as a second command", "a\nb"},
		{"a tab", "a\tb"},
		{"a carriage return", "a\rb"},
		{"a NUL", "a\x00b"},
		{"the delete character", "a\x7fb"},
		{"an x that is not a hex escape", `a\x4`},
		{"Thai text, whose bytes must arrive as themselves", "ผู้ใช้:๑"},
		{"the glob metacharacters a MATCH pattern is made of", `user:*?[1-9]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := "SCAN 0 MATCH " + QuoteRedisArgument(test.argument) + " COUNT 10"

			tokens, err := tokenizeRedisCommand(command)
			if err != nil {
				t.Fatalf("tokenizeRedisCommand(%q): %v", command, err)
			}
			assertTokens(t, command, tokens, []string{"SCAN", "0", "MATCH", test.argument, "COUNT", "10"})
		})
	}
}

func TestRedisDatabaseIndex(t *testing.T) {
	for _, test := range []struct {
		name     string
		database string
		want     int
		wantErr  bool
	}{
		{"no database is database zero", "", 0, false},
		{"whitespace is no database", "   ", 0, false},
		{"zero is zero", "0", 0, false},
		{"a number is that index", "7", 7, false},
		{"surrounding whitespace is the editor's", " 3 ", 3, false},
		{"a name is not an index", "production", 0, true},
		{"a negative index is not an index", "-1", 0, true},
		{"a fraction is not an index", "1.5", 0, true},
		{"a number with a name after it is not an index", "0 production", 0, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := redisDatabaseIndex(test.database)
			switch {
			case test.wantErr && err == nil:
				t.Fatalf("redisDatabaseIndex(%q) = %d, want it refused rather than defaulted", test.database, got)
			case test.wantErr:
				if !errors.Is(err, errRedisDatabaseIndex) {
					t.Errorf("redisDatabaseIndex(%q) error = %v, want it to wrap %v", test.database, err, errRedisDatabaseIndex)
				}
			case err != nil:
				t.Fatalf("redisDatabaseIndex(%q): %v", test.database, err)
			case got != test.want:
				t.Errorf("redisDatabaseIndex(%q) = %d, want %d", test.database, got, test.want)
			}
		})
	}
}

// TestRedisReadQueryRefusesAMutation is the adapter's own second layer firing.
// The connection it runs on has no client at all, which is the point: if the
// refusal did not happen before execution, the nil client would panic instead
// of returning an error, so a passing test is evidence the command never left
// this process.
func TestRedisReadQueryRefusesAMutation(t *testing.T) {
	for _, command := range []string{
		"DEL key",
		"SET key value",
		"FLUSHALL",
		"MULTI",
		"SORT mylist STORE dest",
		"NOTACOMMAND key",
		"",
		"GET a\nDEL b",
	} {
		t.Run(command, func(t *testing.T) {
			conn := &redisConn{}

			result, err := conn.ReadQuery(context.Background(), command)
			if err == nil {
				t.Fatalf("ReadQuery(%q) = %v, want it refused", command, result.Kind())
			}
			if !errors.Is(err, ErrWriteAttempt) {
				t.Errorf("ReadQuery(%q) error = %v, want it to wrap ErrWriteAttempt so the gate takes the command back", command, err)
			}
		})
	}
}

func TestBuildRedisReplyCarriesEveryScalar(t *testing.T) {
	for _, test := range []struct {
		name  string
		value any
		want  Reply
	}{
		{"a nil reply", nil, Reply{Kind: ReplyNil}},
		{"a string reply", "hello", Reply{Kind: ReplyString, Text: "hello"}},
		{"an empty string is not a nil", "", Reply{Kind: ReplyString}},
		{"an integer reply", int64(-42), Reply{Kind: ReplyInteger, Integer: -42}},
		{"a double reply", 1.5, Reply{Kind: ReplyDouble, Double: 1.5}},
		{"a boolean reply", true, Reply{Kind: ReplyBoolean, Boolean: true}},
		{"a big number keeps its digits", big.NewInt(9), Reply{Kind: ReplyString, Text: "9"}},
		{"an error inside a reply is a value", errors.New("WRONGTYPE nope"), Reply{Kind: ReplyError, Text: "WRONGTYPE nope"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := buildRedisReply(test.value, 0)
			if got.Kind != test.want.Kind {
				t.Fatalf("Kind = %q, want %q", got.Kind, test.want.Kind)
			}
			if got.Text != test.want.Text || got.Integer != test.want.Integer ||
				got.Double != test.want.Double || got.Boolean != test.want.Boolean {
				t.Errorf("buildRedisReply(%#v) = %#v, want %#v", test.value, got, test.want)
			}
			if len(got.Items) != 0 || len(got.Entries) != 0 || got.Truncated {
				t.Errorf("buildRedisReply(%#v) = %#v, want a leaf with no children and no cut", test.value, got)
			}
		})
	}
}

// TestBuildRedisReplyCarriesADoubleJSONCannotWrite covers the three values a
// RESP double can hold that JSON has no syntax for. One of them inside a tree
// would fail the serialisation of the whole answer, so they are carried as
// RESP's own tokens instead of as a double nobody can send on.
func TestBuildRedisReplyCarriesADoubleJSONCannotWrite(t *testing.T) {
	for _, test := range []struct {
		value float64
		want  string
	}{
		{math.Inf(1), "inf"},
		{math.Inf(-1), "-inf"},
		{math.NaN(), "nan"},
	} {
		t.Run(test.want, func(t *testing.T) {
			got := buildRedisReply(test.value, 0)

			if got.Kind != ReplyString || got.Text != test.want {
				t.Errorf("buildRedisReply(%v) = %#v, want the string %q", test.value, got, test.want)
			}
		})
	}

	if got := buildRedisReply(2.5, 0); got.Kind != ReplyDouble || got.Double != 2.5 {
		t.Errorf("buildRedisReply(2.5) = %#v, want a finite double to stay a double", got)
	}
}

func TestBuildRedisReplyKeepsAnArrayInOrder(t *testing.T) {
	got := buildRedisReply([]any{"a", int64(2), nil, []any{"nested"}}, 0)

	if got.Kind != ReplyArray {
		t.Fatalf("Kind = %q, want %q", got.Kind, ReplyArray)
	}
	if got.Truncated {
		t.Error("Truncated = true, want a four-element array to arrive whole")
	}
	if len(got.Items) != 4 {
		t.Fatalf("Items = %d, want 4", len(got.Items))
	}
	if got.Items[0].Text != "a" || got.Items[1].Integer != 2 || got.Items[2].Kind != ReplyNil {
		t.Errorf("Items = %#v, want the array's own order preserved", got.Items)
	}
	if got.Items[3].Kind != ReplyArray || len(got.Items[3].Items) != 1 {
		t.Errorf("Items[3] = %#v, want the nested array to be a nested array", got.Items[3])
	}
}

// TestBuildRedisReplyOrdersAMapByItsKeys pins the one place the tree does not
// simply mirror what arrived. RESP3 maps reach this package as a Go map, whose
// iteration order is deliberately random, so a map rendered twice would render
// differently twice. Sorting by the key's text is what makes a reply the same
// answer every time it is asked for.
func TestBuildRedisReplyOrdersAMapByItsKeys(t *testing.T) {
	got := buildRedisReply(map[any]any{"zulu": "z", "alpha": "a", "mike": int64(3)}, 0)

	if got.Kind != ReplyMap {
		t.Fatalf("Kind = %q, want %q", got.Kind, ReplyMap)
	}
	var keys []string
	for _, entry := range got.Entries {
		keys = append(keys, entry.Key.Text)
	}
	if len(keys) != 3 || keys[0] != "alpha" || keys[1] != "mike" || keys[2] != "zulu" {
		t.Fatalf("keys = %v, want [alpha mike zulu]", keys)
	}
	if got.Entries[1].Value.Kind != ReplyInteger || got.Entries[1].Value.Integer != 3 {
		t.Errorf("entry mike = %#v, want the integer 3", got.Entries[1].Value)
	}
}

// TestBuildRedisReplyCapsAContainer is MaxRows' argument on the other shape:
// LRANGE k 0 -1 over a million-element list is the unbounded SELECT, and the
// answer that was cut has to say so.
func TestBuildRedisReplyCapsAContainer(t *testing.T) {
	t.Run("an array at the cap is not truncated", func(t *testing.T) {
		got := buildRedisReply(redisTestArray(MaxReplyItems), 0)

		if len(got.Items) != MaxReplyItems {
			t.Errorf("Items = %d, want %d", len(got.Items), MaxReplyItems)
		}
		if got.Truncated {
			t.Error("Truncated = true, want exactly the cap to mean nothing was dropped")
		}
	})

	t.Run("an array past the cap is cut and says so", func(t *testing.T) {
		got := buildRedisReply(redisTestArray(MaxReplyItems+1), 0)

		if len(got.Items) != MaxReplyItems {
			t.Errorf("Items = %d, want %d", len(got.Items), MaxReplyItems)
		}
		if !got.Truncated {
			t.Error("Truncated = false, want the cut marked so a short list is not read as the whole one")
		}
	})

	t.Run("a map past the cap is cut and says so", func(t *testing.T) {
		pairs := make(map[any]any, MaxReplyItems+1)
		for i := 0; i <= MaxReplyItems; i++ {
			pairs[redisTestKey(i)] = int64(i)
		}

		got := buildRedisReply(pairs, 0)

		if len(got.Entries) != MaxReplyItems {
			t.Errorf("Entries = %d, want %d", len(got.Entries), MaxReplyItems)
		}
		if !got.Truncated {
			t.Error("Truncated = false, want the cut marked")
		}
	})

	t.Run("the cut is marked on the container that was cut", func(t *testing.T) {
		got := buildRedisReply([]any{"first", redisTestArray(MaxReplyItems + 1)}, 0)

		if got.Truncated {
			t.Error("the outer array reports Truncated, want the marker on the container that was actually cut")
		}
		if !got.Items[1].Truncated {
			t.Error("the inner array does not report Truncated, want the marker where the cut happened")
		}
	})
}

// TestBuildRedisReplyCapsNesting covers the dimension the element cap does not.
// A tree deeper than anything Redis's own commands answer with is a shape we
// did not expect, and it stops being built at a known depth rather than
// whenever it happens to end.
func TestBuildRedisReplyCapsNesting(t *testing.T) {
	var deepest any = "leaf"
	for i := 0; i < MaxReplyDepth+3; i++ {
		deepest = []any{deepest}
	}

	got := buildRedisReply(deepest, 0)

	node := got
	for depth := 0; depth < MaxReplyDepth; depth++ {
		if node.Kind != ReplyArray {
			t.Fatalf("at depth %d the tree is %q, want an array all the way to the cap", depth, node.Kind)
		}
		if node.Truncated {
			t.Fatalf("at depth %d the tree is already cut, want %d levels first", depth, MaxReplyDepth)
		}
		if len(node.Items) != 1 {
			t.Fatalf("at depth %d there are %d items, want 1", depth, len(node.Items))
		}
		node = node.Items[0]
	}

	if node.Kind != ReplyArray {
		t.Fatalf("the cut node is %q, want the array it stands for", node.Kind)
	}
	if !node.Truncated {
		t.Error("the cut node does not report Truncated, want the nesting cap marked like any other cut")
	}
	if len(node.Items) != 0 {
		t.Errorf("the cut node holds %d items, want the descent to have stopped", len(node.Items))
	}
}

func redisTestArray(size int) []any {
	items := make([]any, size)
	for i := range items {
		items[i] = int64(i)
	}
	return items
}

// redisTestKey names a map key that sorts the way its number reads, so a cap
// test is not also a test of how integers sort as text.
func redisTestKey(i int) string { return fmt.Sprintf("key-%06d", i) }

func assertTokens(t *testing.T, command string, got, want []string) {
	t.Helper()

	if len(got) != len(want) {
		t.Fatalf("tokenizeRedisCommand(%q) = %q, want %q", command, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tokenizeRedisCommand(%q) = %q, want %q", command, got, want)
		}
	}
}
