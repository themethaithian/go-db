package guard_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/themethaithian/go-db/internal/guard"
)

// This table is the executable specification of what the editor's Run button
// aims at. A span is a place in the buffer, so every case is checked twice
// over: the text is what was expected, and the offsets independently cut that
// same text back out of the input. A split that reported the right text at the
// wrong offsets would run the wrong statement, which is the failure this file
// exists to prevent.

type splitCase struct {
	name string
	sql  string
	// want is the Text of each expected span, in order.
	want []string
}

func TestSplitStatementsFindsBoundaries(t *testing.T) {
	runSplitCases(t, []splitCase{
		{name: "one statement without a semicolon", sql: "SELECT 1", want: []string{"SELECT 1"}},
		{name: "one statement with a semicolon", sql: "SELECT 1;", want: []string{"SELECT 1;"}},
		{name: "two statements", sql: "SELECT 1; SELECT 2;", want: []string{"SELECT 1;", "SELECT 2;"}},
		{
			name: "the last statement has no semicolon",
			sql:  "SELECT 1; SELECT 2",
			want: []string{"SELECT 1;", "SELECT 2"},
		},
		{
			name: "a read and a write in one buffer are separate statements",
			sql:  "SELECT * FROM users; DELETE FROM users WHERE id = 1;",
			want: []string{"SELECT * FROM users;", "DELETE FROM users WHERE id = 1;"},
		},
		{
			name: "surrounding whitespace is not part of a statement",
			sql:  "\n\n   SELECT 1;   \n\n",
			want: []string{"SELECT 1;"},
		},
		{
			name: "empty statements do not become spans",
			sql:  "SELECT 1;;SELECT 2",
			want: []string{"SELECT 1;", "SELECT 2"},
		},
		{
			name: "nor do they leave their semicolons on the next statement",
			sql:  "SELECT 1 ;\n  ;  \nSELECT 2",
			want: []string{"SELECT 1 ;", "SELECT 2"},
		},
	})
}

// A semicolon that is not a boundary is the whole reason this is the parser's
// job. Every case here splits wrongly under any scan of the raw bytes.
func TestSplitStatementsIgnoresSemicolonsThatAreNotBoundaries(t *testing.T) {
	runSplitCases(t, []splitCase{
		{
			name: "semicolon inside a string literal",
			sql:  "SELECT 'a;b'; SELECT 2",
			want: []string{"SELECT 'a;b';", "SELECT 2"},
		},
		{
			name: "semicolon inside a string literal, alone in the buffer",
			sql:  "INSERT INTO t (s) VALUES ('a;b')",
			want: []string{"INSERT INTO t (s) VALUES ('a;b')"},
		},
		{
			name: "semicolon inside a block comment",
			sql:  "SELECT 1 /* ; */ + 1",
			want: []string{"SELECT 1 /* ; */ + 1"},
		},
		{
			name: "semicolon inside a line comment",
			sql:  "SELECT 1 -- ;\n, 2",
			want: []string{"SELECT 1 -- ;\n, 2"},
		},
		{
			name: "a whole statement hidden in a comment is not one",
			sql:  "SELECT 1 /* ; DROP TABLE users; */",
			want: []string{"SELECT 1 /* ; DROP TABLE users; */"},
		},
	})
}

// A comment above a statement is how the human labels it, so it stays inside
// that statement's span and a cursor resting on it finds the statement it
// describes. A comment after the last statement labels nothing and is in no
// span at all.
func TestSplitStatementsAttributesComments(t *testing.T) {
	runSplitCases(t, []splitCase{
		{
			name: "line comment between statements",
			sql:  "SELECT 1;\n-- about the next one\nSELECT 2;\n",
			want: []string{"SELECT 1;", "-- about the next one\nSELECT 2;"},
		},
		{
			name: "block comment between statements",
			sql:  "SELECT 1;\n/* about the next one */\nSELECT 2;",
			want: []string{"SELECT 1;", "/* about the next one */\nSELECT 2;"},
		},
		{
			name: "line comment with a semicolon in it, between statements",
			sql:  "SELECT 1; -- drop;me\nSELECT 2",
			want: []string{"SELECT 1;", "-- drop;me\nSELECT 2"},
		},
		{
			name: "comment before the first statement",
			sql:  "/* nightly report */ SELECT 1;",
			want: []string{"/* nightly report */ SELECT 1;"},
		},
		{
			name: "comment after the last statement belongs to nothing",
			sql:  "SELECT 1; -- done",
			want: []string{"SELECT 1;"},
		},
	})
}

func TestSplitStatementsFindsNothingToRun(t *testing.T) {
	runSplitCases(t, []splitCase{
		{name: "empty input", sql: "", want: nil},
		{name: "whitespace only", sql: "  \n\t \r\n ", want: nil},
		{name: "bare semicolon", sql: ";", want: nil},
		{name: "several bare semicolons", sql: " ; ; ; ", want: nil},
		{name: "block comment only", sql: "/* nothing here */", want: nil},
		{name: "line comment only", sql: "-- nothing here", want: nil},
	})
}

// The fail-closed contract. Whatever the parser could not read becomes one span
// running to the end of the input: a place in the buffer the editor can still
// target, never a guess at where a broken statement ends.
func TestSplitStatementsFailsClosedOnUnparseableInput(t *testing.T) {
	runSplitCases(t, []splitCase{
		{name: "garbage alone", sql: "not sql at all", want: []string{"not sql at all"}},
		{
			name: "garbage alone, padded",
			sql:  "   not sql at all   ",
			want: []string{"not sql at all"},
		},
		{
			name: "a statement then a garbage tail",
			sql:  "SELECT 1; not sql at all",
			want: []string{"SELECT 1;", "not sql at all"},
		},
		{
			name: "two statements then a garbage tail",
			sql:  "SELECT 1; SELECT 2; nope(",
			want: []string{"SELECT 1;", "SELECT 2;", "nope("},
		},
		{
			name: "a half-typed statement is a tail",
			sql:  "SELECT 1;\nSELECT ",
			want: []string{"SELECT 1;", "SELECT"},
		},
		{
			name: "an unclosed quote swallows the rest of the buffer",
			sql:  "SELECT 1; SELECT 'a",
			want: []string{"SELECT 1;", "SELECT 'a"},
		},
		{
			name: "an unclosed block comment swallows the rest of the buffer",
			sql:  "SELECT 1; SELECT 2 /* unfinished",
			want: []string{"SELECT 1;", "SELECT 2 /* unfinished"},
		},
		// Garbage in the middle takes everything after it too. The statements
		// beyond it may well be parseable on their own, but the semicolons
		// between them have not been proven to be boundaries, and inventing
		// boundaries is the one thing this function will not do.
		{
			name: "garbage in the middle takes the rest with it",
			sql:  "SELECT 1; !!! ; SELECT 2;",
			want: []string{"SELECT 1;", "!!! ; SELECT 2;"},
		},
		{
			name: "garbage at the front takes everything",
			sql:  "!!! ; SELECT 1;",
			want: []string{"!!! ; SELECT 1;"},
		},
	})
}

// TestSplitStatementsGivesUpOnAPathologicalBuffer pins the bound on the search
// for the leading statements. Finding them costs one parse per candidate
// boundary, and the editor splits on every keystroke, so the search stops after
// a fixed number of failures and falls back to the answer it can always give:
// one span, which the gate withholds. The shape that reaches it — hundreds of
// semicolons behind a broken first statement — is not the shape of a buffer
// someone is typing in.
func TestSplitStatementsGivesUpOnAPathologicalBuffer(t *testing.T) {
	sql := "!!!;" + strings.Repeat("SELECT 1;", 200)

	got := guard.SplitStatements(sql)

	if len(got) != 1 || got[0].Text != sql {
		t.Fatalf("SplitStatements gave %d spans, want the whole buffer as one", len(got))
	}
	if kind := guard.Classify(got[0].Text).Kind; kind != guard.Mutation {
		t.Errorf("the fallback span classifies as %q; giving up has to fail closed", kind)
	}
}

// CRLF arrives from a pasted buffer and from Windows-authored SQL. The
// carriage returns are whitespace and belong to no statement, and the offsets
// have to keep counting them, because the buffer the editor is holding still
// contains them.
func TestSplitStatementsHandlesCRLF(t *testing.T) {
	runSplitCases(t, []splitCase{
		{
			name: "two statements separated by CRLF",
			sql:  "SELECT 1;\r\nSELECT 2;\r\n",
			want: []string{"SELECT 1;", "SELECT 2;"},
		},
		{
			name: "a multi-line statement with CRLF",
			sql:  "SELECT\r\n  1\r\nFROM dual;\r\nSELECT 2",
			want: []string{"SELECT\r\n  1\r\nFROM dual;", "SELECT 2"},
		},
	})
}

// TestSplitStatementsOffsetsAreExact pins the numbers themselves for a
// multi-line buffer, rather than only the round trip. The editor turns a cursor
// position into a span by comparing it against these, so an offset that is
// consistently wrong in both directions — right text, wrong place — would run
// the statement the human was not looking at.
func TestSplitStatementsOffsetsAreExact(t *testing.T) {
	const sql = "SELECT\n  id\nFROM users;\n\nUPDATE users\n  SET name = 'x';\n"

	want := []guard.StatementSpan{
		{Start: 0, End: 23, Text: "SELECT\n  id\nFROM users;"},
		{Start: 25, End: 55, Text: "UPDATE users\n  SET name = 'x';"},
	}

	got := guard.SplitStatements(sql)
	if !slices.Equal(got, want) {
		t.Fatalf("SplitStatements(%q) =\n  %+v\nwant\n  %+v", sql, got, want)
	}
}

// TestSplitStatementsFeedsTheClassifier joins the two halves of the editor's
// story: each span is submitted on its own, so each gets its own verdict, and
// the tail the splitter could not read is withheld rather than run.
func TestSplitStatementsFeedsTheClassifier(t *testing.T) {
	const sql = "SELECT 1; DELETE FROM users; DROP TABLE"

	spans := guard.SplitStatements(sql)
	want := []guard.Kind{guard.Read, guard.Mutation, guard.Mutation}
	if len(spans) != len(want) {
		t.Fatalf("SplitStatements(%q) = %+v, want %d spans", sql, spans, len(want))
	}
	for i, span := range spans {
		if got := guard.Classify(span.Text); got.Kind != want[i] {
			t.Errorf("Classify(%q).Kind = %q, want %q", span.Text, got.Kind, want[i])
		}
	}
}

func runSplitCases(t *testing.T, cases []splitCase) {
	t.Helper()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := guard.SplitStatements(tc.sql)

			if got == nil {
				t.Fatalf("SplitStatements(%q) = nil; an empty answer is still a list", tc.sql)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("SplitStatements(%q) = %+v, want %d spans %q", tc.sql, got, len(tc.want), tc.want)
			}

			previousEnd := 0
			for i, span := range got {
				if span.Text != tc.want[i] {
					t.Errorf("span %d text = %q, want %q", i, span.Text, tc.want[i])
				}
				// The offsets are checked against the input rather than against
				// the expected text, so a span cannot pass by carrying a text
				// that does not come from where it says it does.
				if span.Start < 0 || span.End > len(tc.sql) || span.Start >= span.End {
					t.Fatalf("span %d = [%d,%d) is not a range inside %d bytes", i, span.Start, span.End, len(tc.sql))
				}
				if slice := tc.sql[span.Start:span.End]; slice != span.Text {
					t.Errorf("span %d = [%d,%d), which cuts %q out of the input, not %q",
						i, span.Start, span.End, slice, span.Text)
				}
				if span.Start < previousEnd {
					t.Errorf("span %d starts at %d, inside the span before it, which ended at %d",
						i, span.Start, previousEnd)
				}
				previousEnd = span.End
			}
		})
	}
}

// TestSplitStatementsIsSafeUnderConcurrency guards the parser pooling, on the
// same terms as the classifier's: the statement texts a split hands back are
// slices of the parser's own buffers, and a parser returned to the pool while
// those are still being read would have another goroutine rewrite them. Under
// -race this catches the sharing; the comparison against the serial answer
// catches spans quietly attributed to the wrong buffer.
func TestSplitStatementsIsSafeUnderConcurrency(t *testing.T) {
	buffers := []string{
		"SELECT 1; SELECT 2;",
		"SELECT 'a;b'; DELETE FROM users WHERE id = 1;",
		"SELECT\n  id\nFROM users;\n\nUPDATE users SET name = 'x';",
		"SELECT 1;\n-- about the next one\nSELECT 2;\n",
		"SELECT 1; not sql at all",
		"not sql at all",
		"SELECT 1; !!! ; SELECT 2;",
		"",
	}

	// The answers to beat, computed one at a time.
	want := make([][]guard.StatementSpan, len(buffers))
	for i, sql := range buffers {
		want[i] = guard.SplitStatements(sql)
	}

	const rounds = 100
	failures := make(chan string, rounds*len(buffers))
	for range rounds {
		for i, sql := range buffers {
			go func() {
				if got := guard.SplitStatements(sql); !slices.Equal(got, want[i]) {
					failures <- fmt.Sprintf("SplitStatements(%q) = %+v under concurrency, want %+v", sql, got, want[i])
					return
				}
				failures <- ""
			}()
		}
	}
	for range cap(failures) {
		if failure := <-failures; failure != "" {
			t.Fatal(failure)
		}
	}
}
