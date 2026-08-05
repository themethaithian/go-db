package guard_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/themethaithian/go-db/internal/guard"
)

// This table is the executable specification of the product's central safety
// claim: nothing that could write reaches the database without passing the
// Approval Gate. Every case states a verdict, and the verdicts are asymmetric
// on purpose — calling a read a mutation costs a keypress, calling a mutation a
// read loses data. When a case is arguable, it is Mutation.
//
// Reasons are asserted only where the wording is the point (the classifier is
// what tells the human why their query was stopped); elsewhere the test only
// insists a reason exists and reads as one line.

type classCase struct {
	name string
	sql  string
	want guard.Kind
	// reason, when set, is a substring the Reason must contain.
	reason string
}

func TestClassifyReads(t *testing.T) {
	runClassCases(t, []classCase{
		{name: "trivial select", sql: "SELECT 1", want: guard.Read, reason: "SELECT"},
		{name: "lowercase select", sql: "select 1", want: guard.Read},
		{name: "mixed case and whitespace", sql: "  SeLeCt\n\t 1  ", want: guard.Read},
		{name: "trailing semicolon is still one statement", sql: "SELECT 1;", want: guard.Read},
		{name: "select with a join", sql: "SELECT u.id, o.total FROM users u JOIN orders o ON o.user_id = u.id", want: guard.Read},
		{name: "select with a subquery", sql: "SELECT * FROM users WHERE id IN (SELECT user_id FROM orders)", want: guard.Read},
		{name: "select with a derived table", sql: "SELECT u.id FROM users u LEFT JOIN (SELECT user_id, COUNT(*) c FROM orders GROUP BY user_id) agg ON agg.user_id = u.id", want: guard.Read},
		{name: "union", sql: "SELECT 1 UNION SELECT 2", want: guard.Read},
		{name: "union all with order and limit", sql: "SELECT id FROM a UNION ALL SELECT id FROM b ORDER BY id LIMIT 10", want: guard.Read},
		{name: "parenthesised select", sql: "(SELECT 1)", want: guard.Read},
		{name: "read-only CTE", sql: "WITH recent AS (SELECT * FROM orders WHERE created_at > NOW()) SELECT * FROM recent", want: guard.Read},
		{name: "nested read-only CTEs", sql: "WITH a AS (SELECT 1 AS x), b AS (SELECT x FROM a) SELECT * FROM b", want: guard.Read},
		{name: "show tables", sql: "SHOW TABLES", want: guard.Read, reason: "SHOW"},
		{name: "show create table", sql: "SHOW CREATE TABLE users", want: guard.Read},
		{name: "show variables", sql: "SHOW VARIABLES LIKE 'version'", want: guard.Read},
		{name: "describe", sql: "DESCRIBE users", want: guard.Read, reason: "DESCRIBE"},
		{name: "desc abbreviation", sql: "DESC users", want: guard.Read},
		{name: "explain of a read", sql: "EXPLAIN SELECT * FROM users", want: guard.Read, reason: "EXPLAIN"},
		{name: "explain analyze of a read", sql: "EXPLAIN ANALYZE SELECT * FROM users", want: guard.Read},
		{name: "use", sql: "USE analytics", want: guard.Read, reason: "USE"},
		{name: "table statement", sql: "TABLE users", want: guard.Read},

		// A comment cannot smuggle a statement in either direction: its
		// contents are inert, so a read that mentions DROP in a comment or a
		// string literal is still a read.
		{name: "comment before a read", sql: "/* nightly report */ SELECT 1", want: guard.Read},
		{name: "line comment before a read", sql: "-- nightly report\nSELECT 1", want: guard.Read},
		{name: "a write hidden in a comment is inert", sql: "SELECT 1 /* ; DROP TABLE users */", want: guard.Read},
		{name: "a write inside a string literal is inert", sql: "SELECT 'DELETE FROM users' AS sql_text", want: guard.Read},

		// Layer 1 cannot see what a function does; a side-effecting function
		// inside a SELECT is Layer 2's job (the read-only transaction), not a
		// blocklist's. Classifying this Read is the documented, deliberate
		// division of labour between the two layers.
		{name: "side-effecting function is left to the backstop", sql: "SELECT SLEEP(0)", want: guard.Read},
	})
}

func TestClassifyMutations(t *testing.T) {
	runClassCases(t, []classCase{
		// Plain DML and DDL.
		{name: "insert", sql: "INSERT INTO users (name) VALUES ('a')", want: guard.Mutation, reason: "INSERT"},
		{name: "replace", sql: "REPLACE INTO users (id) VALUES (1)", want: guard.Mutation},
		{name: "update", sql: "UPDATE users SET name = 'a' WHERE id = 1", want: guard.Mutation, reason: "UPDATE"},
		{name: "delete", sql: "DELETE FROM users WHERE id = 1", want: guard.Mutation, reason: "DELETE"},
		{name: "lowercase delete", sql: "delete from users", want: guard.Mutation},
		{name: "delete across lines", sql: "UPDATE\n  users\nSET name = 'x'", want: guard.Mutation},
		{name: "truncate", sql: "TRUNCATE TABLE users", want: guard.Mutation},
		{name: "drop table", sql: "DROP TABLE users", want: guard.Mutation, reason: "DROP TABLE"},
		{name: "create table", sql: "CREATE TABLE t (a int)", want: guard.Mutation, reason: "CREATE TABLE"},
		{name: "alter table", sql: "ALTER TABLE users ADD COLUMN x int", want: guard.Mutation},
		{name: "create index", sql: "CREATE INDEX i ON users (name)", want: guard.Mutation},
		{name: "grant", sql: "GRANT SELECT ON *.* TO 'x'@'%'", want: guard.Mutation},

		// EXPLAIN inherits the verdict of the statement it explains. EXPLAIN
		// ANALYZE genuinely runs it, and plain EXPLAIN of a write is refused
		// too: one rule is easier to audit than two, and it errs safe.
		{name: "explain analyze of a delete executes it", sql: "EXPLAIN ANALYZE DELETE FROM users", want: guard.Mutation, reason: "EXPLAIN ANALYZE"},
		{name: "explain of a delete", sql: "EXPLAIN DELETE FROM users", want: guard.Mutation, reason: "DELETE"},
		{name: "explain format of an update", sql: "EXPLAIN FORMAT='brief' UPDATE users SET name = 'a'", want: guard.Mutation},

		// A CTE in front of a write does not make it a read.
		{name: "CTE feeding a delete", sql: "WITH doomed AS (SELECT id FROM users WHERE stale = 1) DELETE FROM users WHERE id IN (SELECT id FROM doomed)", want: guard.Mutation, reason: "DELETE"},
		{name: "writing CTE", sql: "WITH c AS (INSERT INTO users VALUES (1) RETURNING id) SELECT * FROM c", want: guard.Mutation, reason: "could not be parsed"},

		// Multi-statement input is refused whole. The driver runs with
		// multiStatements disabled, so this never reaches the server as one
		// script — but classifying only the first statement would be a
		// straightforward way to smuggle the second one past a future caller.
		{name: "read followed by a drop", sql: "SELECT 1; DROP TABLE users", want: guard.Mutation, reason: "more than one statement"},
		{name: "two reads are still multi-statement", sql: "SELECT 1; SELECT 2", want: guard.Mutation, reason: "more than one statement"},

		// Comments do not hide a write.
		{name: "block comment before a delete", sql: "/* hi */ DELETE FROM users", want: guard.Mutation, reason: "DELETE"},
		{name: "line comment before a drop", sql: "-- innocuous\nDROP TABLE users", want: guard.Mutation},
		{name: "version comment executing a delete", sql: "/*! DELETE FROM users */", want: guard.Mutation},

		// Locking reads take row locks and hold them for the transaction: they
		// are refused so a read can never block a writer.
		{name: "select for update", sql: "SELECT * FROM users FOR UPDATE", want: guard.Mutation, reason: "lock"},
		{name: "lock in share mode", sql: "SELECT * FROM users LOCK IN SHARE MODE", want: guard.Mutation, reason: "lock"},
		{name: "select for share", sql: "SELECT * FROM users FOR SHARE", want: guard.Mutation, reason: "lock"},
		{name: "for update in a derived table", sql: "SELECT * FROM (SELECT * FROM users FOR UPDATE) x", want: guard.Mutation, reason: "lock"},
		{name: "for update in a subquery", sql: "SELECT * FROM users WHERE id IN (SELECT id FROM orders FOR UPDATE)", want: guard.Mutation, reason: "lock"},
		{name: "for update inside a CTE", sql: "WITH c AS (SELECT * FROM users FOR UPDATE) SELECT * FROM c", want: guard.Mutation, reason: "lock"},
		{name: "for update in a union branch", sql: "(SELECT 1) UNION (SELECT id FROM users FOR UPDATE)", want: guard.Mutation, reason: "lock"},

		// A SELECT that writes a file is a write.
		{name: "select into outfile", sql: "SELECT * FROM users INTO OUTFILE '/tmp/users.csv'", want: guard.Mutation, reason: "file"},
		{name: "select into dumpfile", sql: "SELECT * FROM users INTO DUMPFILE '/tmp/u'", want: guard.Mutation},

		// Statements whose effect is not visible in their own text.
		{name: "call a procedure", sql: "CALL do_something(1)", want: guard.Mutation, reason: "CALL"},
		{name: "do", sql: "DO SLEEP(1)", want: guard.Mutation, reason: "DO"},
		{name: "set a session variable", sql: "SET autocommit = 0", want: guard.Mutation, reason: "SET"},
		{name: "set sql mode", sql: "SET SESSION sql_mode = ''", want: guard.Mutation},
		{name: "prepare hides its statement", sql: "PREPARE s FROM 'DELETE FROM users'", want: guard.Mutation},
		{name: "execute", sql: "EXECUTE s", want: guard.Mutation},

		// Table and transaction control.
		{name: "lock tables", sql: "LOCK TABLES users WRITE", want: guard.Mutation, reason: "LOCK TABLES"},
		{name: "unlock tables", sql: "UNLOCK TABLES", want: guard.Mutation},
		{name: "load data", sql: "LOAD DATA INFILE '/tmp/x' INTO TABLE users", want: guard.Mutation, reason: "LOAD DATA"},
		{name: "start transaction", sql: "START TRANSACTION", want: guard.Mutation},
		{name: "commit", sql: "COMMIT", want: guard.Mutation},
		{name: "rollback", sql: "ROLLBACK", want: guard.Mutation},
		{name: "flush", sql: "FLUSH TABLES", want: guard.Mutation},
		{name: "kill", sql: "KILL 42", want: guard.Mutation},
		{name: "analyze table", sql: "ANALYZE TABLE users", want: guard.Mutation},
		{name: "optimize table", sql: "OPTIMIZE TABLE users", want: guard.Mutation},
		{name: "handler", sql: "HANDLER users OPEN", want: guard.Mutation},

		// Anything the parser cannot make sense of is a mutation: an
		// unreadable statement is by definition not provably read-only.
		{name: "garbage", sql: "not sql at all", want: guard.Mutation, reason: "could not be parsed"},
		{name: "truncated statement", sql: "SELECT", want: guard.Mutation, reason: "could not be parsed"},
		{name: "unbalanced quote", sql: "SELECT * FROM users WHERE name = 'a", want: guard.Mutation},
		{name: "empty input", sql: "", want: guard.Mutation, reason: "no statement"},
		{name: "whitespace only", sql: "   \n\t ", want: guard.Mutation, reason: "no statement"},
		{name: "bare semicolon", sql: ";", want: guard.Mutation, reason: "no statement"},
		{name: "comment only", sql: "/* nothing here */", want: guard.Mutation, reason: "no statement"},
	})
}

func runClassCases(t *testing.T, cases []classCase) {
	t.Helper()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := guard.Classify(tc.sql)

			if got.Kind != tc.want {
				t.Errorf("Classify(%q).Kind = %q, want %q (reason: %s)", tc.sql, got.Kind, tc.want, got.Reason)
			}
			if got.Reason == "" {
				t.Fatalf("Classify(%q) gave no reason; the human is told why their query was stopped", tc.sql)
			}
			if strings.ContainsAny(got.Reason, "\n\r") {
				t.Errorf("reason spans lines, want one line of prose:\n%s", got.Reason)
			}
			if tc.reason != "" && !strings.Contains(got.Reason, tc.reason) {
				t.Errorf("Classify(%q).Reason = %q, want it to mention %q", tc.sql, got.Reason, tc.reason)
			}
			if got.IsRead() != (tc.want == guard.Read) {
				t.Errorf("IsRead() = %v, disagrees with Kind %q", got.IsRead(), got.Kind)
			}
		})
	}
}

// TestClassifyIsSafeUnderConcurrency guards the parser pooling. The editor and
// the MCP server classify at the same time, and a parser handed to a second
// goroutine while the first is still reading the AST it built does not crash —
// it rewrites the statement under judgement. A verdict on the wrong statement
// is the worst failure this package has, so the test runs under -race and also
// checks the whole verdict, reason included, against the serial answer.
func TestClassifyIsSafeUnderConcurrency(t *testing.T) {
	statements := []string{
		"SELECT * FROM users WHERE id IN (SELECT user_id FROM orders)",
		"DELETE FROM users WHERE id = 1",
		"WITH c AS (SELECT 1) SELECT * FROM c",
		"EXPLAIN ANALYZE DELETE FROM users",
		"SELECT * FROM users FOR UPDATE",
		"DROP TABLE users",
		"SHOW TABLES",
		"not sql at all",
	}

	// The answers to beat, computed one at a time.
	want := make([]guard.Classification, len(statements))
	for i, sql := range statements {
		want[i] = guard.Classify(sql)
	}

	const rounds = 100
	failures := make(chan string, rounds*len(statements))
	for range rounds {
		for i, sql := range statements {
			go func() {
				if got := guard.Classify(sql); got != want[i] {
					failures <- fmt.Sprintf("Classify(%q) = %+v under concurrency, want %+v", sql, got, want[i])
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

// TestBackstopped pins the Classification the database backstop produces: a
// query the static classifier called a read, which the database then refused to
// run inside a read-only transaction, is a mutation from that moment on.
func TestBackstopped(t *testing.T) {
	got := guard.Backstopped()

	if got.Kind != guard.Mutation {
		t.Errorf("Backstopped().Kind = %q, want %q", got.Kind, guard.Mutation)
	}
	if got.IsRead() {
		t.Error("Backstopped() reports itself a read")
	}
	if got.Reason == "" {
		t.Fatal("Backstopped() gave no reason")
	}
	for _, want := range []string{"read-only transaction", "write"} {
		if !strings.Contains(got.Reason, want) {
			t.Errorf("Backstopped().Reason = %q, want it to mention %q", got.Reason, want)
		}
	}
}

// TestOrigins pins the two Origins every query request carries. Policy differs
// per Origin from the Approval Gate slice on; the type exists now so the seam
// does not change when it lands.
func TestOrigins(t *testing.T) {
	if guard.OriginHuman == guard.OriginAI {
		t.Fatal("the two Origins are indistinguishable")
	}
	for _, origin := range []guard.Origin{guard.OriginHuman, guard.OriginAI} {
		if string(origin) == "" {
			t.Errorf("Origin %#v has no name to log or serialise", origin)
		}
		if !origin.Valid() {
			t.Errorf("Origin %q reports itself invalid", origin)
		}
	}
	for _, origin := range []guard.Origin{"", "human ", "robot"} {
		if origin.Valid() {
			t.Errorf("Origin %q reports itself valid", origin)
		}
	}
}
