package guard_test

import (
	"strings"
	"testing"

	"github.com/themethaithian/go-db/internal/guard"
)

// This table is the executable specification of editor paging. The frontend
// reads a statement's window after it ran, then asks for the next one and runs
// what comes back through the ordinary RunQuery path — so every rewritten
// statement is asserted twice over: it is the SQL we expect, and the classifier
// still calls it a read. A paging rewrite that produced something unproven
// would be a way around the Approval Gate, which is the failure this file
// exists to prevent.
//
// The other half of the specification is the refusals. "This statement has no
// page to turn" is a state the answer states outright, with a reason a human
// can read, because the alternative — a window quietly reported as 1000 rows
// from 0 — would put a Next button under a statement that cannot honour it.

// testMaxRows stands in for db.MaxRows, the cap the driver really bounded the
// result at. guard cannot name it: internal/db imports this package.
const testMaxRows = 1000

type windowCase struct {
	name string
	sql  string
	// wantSize and wantOffset are the window the statement asks for.
	wantSize   int64
	wantOffset int64
	// notPageable, when set, is a substring the refusal reason must contain,
	// and says this statement must have no window at all.
	notPageable string
}

// A statement with no LIMIT of its own was bounded by the driver's cap, so that
// cap is its page size: it is the number of rows the human is looking at, and
// the next page starts after them.
func TestPageWindowReadsTheWindow(t *testing.T) {
	runWindowCases(t, []windowCase{
		{
			name:     "a select with no limit is bounded by the cap that bounded its result",
			sql:      "SELECT * FROM users",
			wantSize: testMaxRows,
		},
		{
			name:     "a select with its own limit asks for that many rows",
			sql:      "SELECT * FROM users LIMIT 25",
			wantSize: 25,
		},
		{
			// LIMIT offset, count — the older form, and the one that reads
			// backwards. Getting these two the wrong way round would page in
			// steps of the offset.
			name:       "limit with two arguments is offset then count",
			sql:        "SELECT * FROM users LIMIT 10, 25",
			wantSize:   25,
			wantOffset: 10,
		},
		{
			name:       "limit with an explicit offset",
			sql:        "SELECT * FROM users LIMIT 25 OFFSET 10",
			wantSize:   25,
			wantOffset: 10,
		},
		{
			name:       "an offset past the first page",
			sql:        "SELECT id FROM users ORDER BY id LIMIT 50 OFFSET 200",
			wantSize:   50,
			wantOffset: 200,
		},
		{
			// The standard spelling of the same thing.
			name:     "fetch first n rows only is a limit",
			sql:      "SELECT * FROM users ORDER BY id FETCH FIRST 3 ROWS ONLY",
			wantSize: 3,
		},
		{
			// Only the outermost window is the statement's own. A LIMIT inside
			// a derived table belongs to that table's rows, not to the page.
			name:     "a limit inside a derived table is not the statement's window",
			sql:      "SELECT * FROM (SELECT * FROM users LIMIT 5) AS recent",
			wantSize: testMaxRows,
		},
		{
			name:     "a limit inside a subquery in the where clause is not the statement's window",
			sql:      "SELECT * FROM users WHERE id IN (SELECT user_id FROM orders ORDER BY id LIMIT 3)",
			wantSize: testMaxRows,
		},
		{
			name:     "a limit inside a CTE is not the statement's window",
			sql:      "WITH recent AS (SELECT id FROM users ORDER BY id DESC LIMIT 5) SELECT * FROM recent",
			wantSize: testMaxRows,
		},
		{
			name:       "a CTE-fronted select has a window of its own",
			sql:        "WITH recent AS (SELECT id FROM users) SELECT * FROM recent LIMIT 7 OFFSET 7",
			wantSize:   7,
			wantOffset: 7,
		},
		{
			name:     "comments and ragged whitespace do not change the window",
			sql:      "/* nightly */\n  SELECT\n\t*   FROM users -- trailing\n",
			wantSize: testMaxRows,
		},
		{
			name:     "a select with no table still has a window",
			sql:      "SELECT 1",
			wantSize: testMaxRows,
		},
	})
}

// Every statement here has to say it has no page to turn, in a line the human
// can read. The frontend hides its pager on this answer, so a wrong "yes" here
// is a Next button that reruns the wrong statement.
func TestPageWindowRefusals(t *testing.T) {
	runWindowCases(t, []windowCase{
		// Only a SELECT is paged. A mutation has no result to page through,
		// and DDL has no rows at all.
		{name: "update", sql: "UPDATE users SET name = 'x' WHERE id = 1", notPageable: "UPDATE statement"},
		{name: "insert", sql: "INSERT INTO users (id) VALUES (1)", notPageable: "INSERT statement"},
		{name: "delete", sql: "DELETE FROM users WHERE id = 1", notPageable: "DELETE statement"},
		{name: "create table", sql: "CREATE TABLE t (a int)", notPageable: "CREATE TABLE statement"},
		{name: "drop table", sql: "DROP TABLE users", notPageable: "DROP TABLE statement"},

		// SHOW is a read, and it is still not a SELECT: MySQL takes a LIMIT on
		// some SHOW forms and not others, and the editor has no reason to
		// guess which.
		{name: "show tables", sql: "SHOW TABLES", notPageable: "SHOW statement"},
		{name: "describe", sql: "DESCRIBE users", notPageable: "statement"},

		// TABLE t and VALUES ROW(...) are SELECT-shaped in the parser and not
		// in the editor's hands; they are read as themselves rather than paged.
		{name: "table statement", sql: "TABLE users", notPageable: "TABLE statement"},

		// A set operation is deliberately out of scope for v1: a LIMIT written
		// at the end of a UNION binds to the whole result or to its last
		// branch depending on how it was written, and wrapping the union in a
		// derived table to page it safely would rename its columns.
		{name: "union", sql: "SELECT id FROM a UNION SELECT id FROM b", notPageable: "set operation"},
		{name: "union with a limit", sql: "SELECT id FROM a UNION SELECT id FROM b LIMIT 10", notPageable: "set operation"},
		{name: "union all", sql: "SELECT id FROM a UNION ALL SELECT id FROM b", notPageable: "set operation"},
		{name: "CTE-fronted union", sql: "WITH c AS (SELECT id FROM u) SELECT id FROM c UNION SELECT id FROM b", notPageable: "set operation"},

		// Statements the gate does not call reads are not paged, whatever
		// their shape. Paging them would rerun them.
		{name: "select into outfile", sql: "SELECT * FROM users INTO OUTFILE '/tmp/x'", notPageable: "INTO"},
		{name: "select for update", sql: "SELECT * FROM users FOR UPDATE", notPageable: "row locks"},

		// A window nobody can read is not a window to move.
		{name: "limit with a placeholder", sql: "SELECT * FROM users LIMIT ?", notPageable: "LIMIT"},
		{name: "limit zero asks for no rows", sql: "SELECT * FROM users LIMIT 0", notPageable: "LIMIT 0"},
		{
			// MySQL's way of saying "everything from row n on". It is not a
			// page, and it must not be truncated into one.
			name:        "a limit larger than a page can be",
			sql:         "SELECT * FROM users LIMIT 100, 18446744073709551615",
			notPageable: "LIMIT",
		},

		// Input the gate could not read is not input the pager can read.
		{name: "two statements", sql: "SELECT 1; SELECT 2", notPageable: "more than one statement"},
		{name: "garbage", sql: "not sql at all", notPageable: "could not be parsed"},
		{name: "empty", sql: "", notPageable: "no statement"},
		{name: "whitespace only", sql: "   \n\t ", notPageable: "no statement"},
	})
}

func runWindowCases(t *testing.T, cases []windowCase) {
	t.Helper()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := guard.PageWindow(tc.sql, testMaxRows)

			if tc.notPageable != "" {
				if got.Pageable {
					t.Fatalf("PageWindow(%q) reported %+v, want no window", tc.sql, got)
				}
				if got.Reason == "" {
					t.Fatal("no window and no reason; the human is told why there is none")
				}
				if !strings.Contains(got.Reason, tc.notPageable) {
					t.Errorf("reason = %q, want it to mention %q", got.Reason, tc.notPageable)
				}
				if strings.ContainsAny(got.Reason, "\n\r") {
					t.Errorf("reason spans lines, want one line of prose:\n%s", got.Reason)
				}
				if got.SQL != "" || got.Size != 0 || got.Offset != 0 {
					t.Errorf("a refused window carries a window: %+v", got)
				}

				// Nothing that has no window can be moved to one either.
				if moved := guard.Repage(tc.sql, 10, 10); moved.Pageable {
					t.Errorf("Repage(%q) produced %q for a statement with no window", tc.sql, moved.SQL)
				}
				return
			}

			if !got.Pageable {
				t.Fatalf("PageWindow(%q) refused: %s", tc.sql, got.Reason)
			}
			if got.Reason != "" {
				t.Errorf("a pageable statement carries a refusal reason: %q", got.Reason)
			}
			if got.Size != tc.wantSize {
				t.Errorf("Size = %d, want %d", got.Size, tc.wantSize)
			}
			if got.Offset != tc.wantOffset {
				t.Errorf("Offset = %d, want %d", got.Offset, tc.wantOffset)
			}
			if got.SQL != "" {
				t.Errorf("PageWindow describes a statement the caller already has, but returned SQL %q", got.SQL)
			}
		})
	}
}

type repageCase struct {
	name   string
	sql    string
	size   int64
	offset int64
	want   string
}

// The rewrite is the other half: the same statement asking for a different
// window, serialized by the parser rather than spliced together, so what runs
// is a statement the classifier can read.
func TestRepageRewritesTheWindow(t *testing.T) {
	runRepageCases(t, []repageCase{
		{
			name: "a statement with no limit gets one",
			sql:  "SELECT * FROM users",
			size: 1000, offset: 0,
			want: "SELECT * FROM `users` LIMIT 1000",
		},
		{
			name: "the second page of a statement with no limit",
			sql:  "SELECT * FROM users",
			size: 1000, offset: 1000,
			want: "SELECT * FROM `users` LIMIT 1000,1000",
		},
		{
			name: "a statement's own limit is overridden, not added to",
			sql:  "SELECT * FROM users LIMIT 25",
			size: 25, offset: 25,
			want: "SELECT * FROM `users` LIMIT 25,25",
		},
		{
			name: "the two-argument limit form is replaced whole",
			sql:  "SELECT * FROM users LIMIT 10, 25",
			size: 25, offset: 35,
			want: "SELECT * FROM `users` LIMIT 35,25",
		},
		{
			name: "the offset form is replaced whole",
			sql:  "SELECT * FROM users LIMIT 25 OFFSET 10",
			size: 25, offset: 0,
			want: "SELECT * FROM `users` LIMIT 25",
		},
		{
			name: "order by is what makes the pages line up, and it survives",
			sql:  "SELECT id, name FROM users WHERE active = 1 ORDER BY id DESC LIMIT 5 OFFSET 5",
			size: 5, offset: 10,
			want: "SELECT `id`,`name` FROM `users` WHERE `active`=1 ORDER BY `id` DESC LIMIT 10,5",
		},
		{
			// Only the outermost window moves. A derived table's own LIMIT is
			// part of what the statement means.
			name: "a limit inside a derived table is left alone",
			sql:  "SELECT * FROM (SELECT * FROM users LIMIT 5) AS recent",
			size: 2, offset: 4,
			want: "SELECT * FROM (SELECT * FROM `users` LIMIT 5) AS `recent` LIMIT 4,2",
		},
		{
			name: "a limit inside a subquery is left alone",
			sql:  "SELECT * FROM users WHERE id IN (SELECT user_id FROM orders ORDER BY id LIMIT 3)",
			size: 10, offset: 20,
			want: "SELECT * FROM `users` WHERE `id` IN (SELECT `user_id` FROM `orders` ORDER BY `id` LIMIT 3) LIMIT 20,10",
		},
		{
			name: "a CTE travels with the rewrite, inner limit and all",
			sql:  "WITH recent AS (SELECT id FROM users ORDER BY id DESC LIMIT 5) SELECT * FROM recent LIMIT 2",
			size: 2, offset: 2,
			want: "WITH `recent` AS (SELECT `id` FROM `users` ORDER BY `id` DESC LIMIT 5) SELECT * FROM `recent` LIMIT 2,2",
		},
		{
			name: "comments and ragged whitespace do not survive, and the statement does",
			sql:  "/* nightly */\n  SELECT   id,\n name FROM `users` WHERE note = 'a;b' -- trailing\n",
			size: 50, offset: 50,
			want: "SELECT `id`,`name` FROM `users` WHERE `note`=_UTF8MB4'a;b' LIMIT 50,50",
		},
		{
			name: "backticked identifiers with awkward characters keep their quoting",
			sql:  "SELECT `a b` FROM `my db`.`we``ird`",
			size: 10, offset: 0,
			want: "SELECT `a b` FROM `my db`.`we``ird` LIMIT 10",
		},
		{
			name: "fetch first n rows only is rewritten as a limit",
			sql:  "SELECT * FROM users ORDER BY id FETCH FIRST 3 ROWS ONLY",
			size: 3, offset: 3,
			want: "SELECT * FROM `users` ORDER BY `id` LIMIT 3,3",
		},
	})
}

// A window has to be a window: an empty page is not one, and a page cannot
// start before the first row.
func TestRepageRefusesWindowsThatAreNotWindows(t *testing.T) {
	for _, tc := range []struct {
		name   string
		size   int64
		offset int64
	}{
		{name: "no rows", size: 0, offset: 0},
		{name: "a negative page", size: -1, offset: 0},
		{name: "a negative offset", size: 10, offset: -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := guard.Repage("SELECT * FROM users", tc.size, tc.offset)
			if got.Pageable {
				t.Fatalf("Repage(size=%d, offset=%d) produced %q", tc.size, tc.offset, got.SQL)
			}
			if got.Reason == "" {
				t.Error("no window and no reason")
			}
		})
	}
}

func runRepageCases(t *testing.T, cases []repageCase) {
	t.Helper()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := guard.Repage(tc.sql, tc.size, tc.offset)

			if !got.Pageable {
				t.Fatalf("Repage(%q, %d, %d) refused: %s", tc.sql, tc.size, tc.offset, got.Reason)
			}
			if got.SQL != tc.want {
				t.Errorf("SQL  = %q\n want  %q", got.SQL, tc.want)
			}
			if got.Size != tc.size || got.Offset != tc.offset {
				t.Errorf("window = (%d, %d), want (%d, %d)", got.Size, got.Offset, tc.size, tc.offset)
			}
			if got.Reason != "" {
				t.Errorf("a rewritten statement carries a refusal reason: %q", got.Reason)
			}

			// The claim that makes paging safe: what the frontend will submit
			// is still a statement the gate proves read-only, and it says it
			// asks for exactly the window it was asked for.
			if verdict := guard.Classify(got.SQL); !verdict.IsRead() {
				t.Errorf("Repage produced %q, which the gate calls a %s (%s)", got.SQL, verdict.Kind, verdict.Reason)
			}
			again := guard.PageWindow(got.SQL, testMaxRows)
			if !again.Pageable || again.Size != tc.size || again.Offset != tc.offset {
				t.Errorf("reading the rewritten statement back gives %+v, want size %d offset %d", again, tc.size, tc.offset)
			}
		})
	}
}

// Paging is a loop — read the window, ask for the next one, read it back — so
// the rewrite has to be stable under repetition rather than accumulating
// clauses.
func TestRepageIsRepeatable(t *testing.T) {
	sql := "SELECT id FROM users ORDER BY id"
	const size = 100

	for page := range int64(4) {
		next := guard.Repage(sql, size, page*size)
		if !next.Pageable {
			t.Fatalf("page %d refused: %s", page, next.Reason)
		}
		window := guard.PageWindow(next.SQL, testMaxRows)
		if window.Size != size || window.Offset != page*size {
			t.Fatalf("page %d reads back as %+v", page, window)
		}
		sql = next.SQL
	}
	if want := "SELECT `id` FROM `users` ORDER BY `id` LIMIT 300,100"; sql != want {
		t.Errorf("after four pages the statement is %q, want %q", sql, want)
	}
}

// NoWindow is how a caller outside this package — the facade, for an Engine
// whose answers are not rows — says there is no window, in the same shape as
// every other refusal.
func TestNoWindowIsARefusal(t *testing.T) {
	none := guard.NoWindow("go-db does not page Redis answers")
	if none.Pageable {
		t.Error("NoWindow reports itself pageable")
	}
	if none.Reason == "" {
		t.Error("NoWindow carries no reason")
	}
	if none.SQL != "" || none.Size != 0 || none.Offset != 0 {
		t.Errorf("NoWindow carries a window: %+v", none)
	}
}

// The same parser-pooling hazard the classifier and the preview have: the
// window is read off an AST the parser owns, and the rewritten SQL is rendered
// from one, so returning the parser before either is finished with would let
// another goroutine rewrite the statement being paged.
func TestWindowingIsSafeUnderConcurrency(t *testing.T) {
	statements := []string{
		"SELECT * FROM users LIMIT 25 OFFSET 50",
		"SELECT id FROM orders ORDER BY id DESC",
		"WITH recent AS (SELECT id FROM users LIMIT 5) SELECT * FROM recent LIMIT 2",
		"SELECT id FROM a UNION SELECT id FROM b",
		"UPDATE users SET name = 'x'",
	}

	wantWindow := make([]guard.Window, len(statements))
	wantRepage := make([]guard.Window, len(statements))
	for i, sql := range statements {
		wantWindow[i] = guard.PageWindow(sql, testMaxRows)
		wantRepage[i] = guard.Repage(sql, 10, 20)
	}

	const rounds = 100
	failures := make(chan string, 2*rounds*len(statements))
	for range rounds {
		for i, sql := range statements {
			go func() {
				if got := guard.PageWindow(sql, testMaxRows); got != wantWindow[i] {
					failures <- "PageWindow(" + sql + ") differed under concurrency"
					return
				}
				failures <- ""
			}()
			go func() {
				if got := guard.Repage(sql, 10, 20); got != wantRepage[i] {
					failures <- "Repage(" + sql + ") differed under concurrency"
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
