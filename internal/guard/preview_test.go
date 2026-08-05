package guard_test

import (
	"strings"
	"testing"

	"github.com/themethaithian/go-db/internal/guard"
)

// This table is the executable specification of the Impact Preview rewrite: a
// mutation is turned into reads that describe it, and the mutation itself is
// never one of them. Two claims are asserted for every planned case — the SQL
// is what we expect, and the classifier calls it a read — because the second is
// what makes the first safe. A preview that could write is worse than no
// preview at all.
//
// The other half of the specification is the refusals. "No preview" is a state
// the plan states outright, with a reason a human can read; a preview that
// quietly came back empty would be indistinguishable from one that found no
// rows, and those two mean opposite things.

type previewCase struct {
	name string
	sql  string
	// wantCount and wantSample are the plan's SQL, exactly.
	wantCount  string
	wantSample string
	// wantStatic is the count read off the statement itself, for the forms
	// that need no query to count (INSERT ... VALUES).
	wantStatic int64
	// noPreview, when set, is a substring the refusal reason must contain, and
	// says this statement must not be previewable at all.
	noPreview string
}

func TestPlanPreviewUpdate(t *testing.T) {
	runPreviewCases(t, []previewCase{
		{
			name:       "update with a where clause",
			sql:        "UPDATE users SET name = 'x' WHERE id = 1",
			wantCount:  "SELECT COUNT(1) FROM `users` WHERE `id`=1",
			wantSample: "SELECT * FROM `users` WHERE `id`=1 LIMIT 5",
		},
		{
			// The whole-table preview is the point of the feature, not an edge
			// case: an UPDATE with no WHERE is exactly the footgun the human
			// needs to see before confirming.
			name:       "update with no where clause previews the whole table",
			sql:        "UPDATE users SET name = 'x'",
			wantCount:  "SELECT COUNT(1) FROM `users`",
			wantSample: "SELECT * FROM `users` LIMIT 5",
		},
		{
			name:       "update through an alias",
			sql:        "UPDATE users u SET u.name = 'x' WHERE u.id = 1",
			wantCount:  "SELECT COUNT(1) FROM `users` AS `u` WHERE `u`.`id`=1",
			wantSample: "SELECT * FROM `users` AS `u` WHERE `u`.`id`=1 LIMIT 5",
		},
		{
			name:       "schema-qualified table",
			sql:        "UPDATE app.users SET name = 'x' WHERE id = 1",
			wantCount:  "SELECT COUNT(1) FROM `app`.`users` WHERE `id`=1",
			wantSample: "SELECT * FROM `app`.`users` WHERE `id`=1 LIMIT 5",
		},
		{
			name:       "backticked identifiers with awkward characters",
			sql:        "UPDATE `my db`.`we``ird` SET `a b` = 1 WHERE `x y` = 2",
			wantCount:  "SELECT COUNT(1) FROM `my db`.`we``ird` WHERE `x y`=2",
			wantSample: "SELECT * FROM `my db`.`we``ird` WHERE `x y`=2 LIMIT 5",
		},
		{
			// A LIMIT on the mutation caps what it touches, so the count has to
			// be capped the same way: the mutation's own rows are counted
			// inside a derived table that carries its ORDER BY and LIMIT.
			name:       "update with order by and limit",
			sql:        "UPDATE users SET name = 'x' WHERE stale = 1 ORDER BY id LIMIT 3",
			wantCount:  "SELECT COUNT(1) FROM (SELECT 1 FROM `users` WHERE `stale`=1 ORDER BY `id` LIMIT 3) AS `impact_preview`",
			wantSample: "SELECT * FROM (SELECT * FROM `users` WHERE `stale`=1 ORDER BY `id` LIMIT 3) AS `impact_preview` LIMIT 5",
		},
		{
			name:       "update with a subquery in the where clause",
			sql:        "UPDATE users SET name = 'x' WHERE id IN (SELECT user_id FROM orders)",
			wantCount:  "SELECT COUNT(1) FROM `users` WHERE `id` IN (SELECT `user_id` FROM `orders`)",
			wantSample: "SELECT * FROM `users` WHERE `id` IN (SELECT `user_id` FROM `orders`) LIMIT 5",
		},
		{
			name:       "comments and ragged whitespace do not change the plan",
			sql:        "/* nightly */\n  UPDATE\n\tusers   SET name='x'\n WHERE id = 1 -- trailing\n",
			wantCount:  "SELECT COUNT(1) FROM `users` WHERE `id`=1",
			wantSample: "SELECT * FROM `users` WHERE `id`=1 LIMIT 5",
		},
	})
}

func TestPlanPreviewDelete(t *testing.T) {
	runPreviewCases(t, []previewCase{
		{
			name:       "delete with a where clause",
			sql:        "DELETE FROM users WHERE id = 1",
			wantCount:  "SELECT COUNT(1) FROM `users` WHERE `id`=1",
			wantSample: "SELECT * FROM `users` WHERE `id`=1 LIMIT 5",
		},
		{
			name:       "delete with no where clause previews the whole table",
			sql:        "delete from users",
			wantCount:  "SELECT COUNT(1) FROM `users`",
			wantSample: "SELECT * FROM `users` LIMIT 5",
		},
		{
			name:       "delete with order by and limit",
			sql:        "DELETE FROM users WHERE stale = 1 ORDER BY id DESC LIMIT 10",
			wantCount:  "SELECT COUNT(1) FROM (SELECT 1 FROM `users` WHERE `stale`=1 ORDER BY `id` DESC LIMIT 10) AS `impact_preview`",
			wantSample: "SELECT * FROM (SELECT * FROM `users` WHERE `stale`=1 ORDER BY `id` DESC LIMIT 10) AS `impact_preview` LIMIT 5",
		},
		{
			// The CTE has to travel with the rewrite or the preview cannot
			// resolve the name the WHERE clause refers to.
			name:       "delete fed by a CTE",
			sql:        "WITH doomed AS (SELECT id FROM users WHERE stale = 1) DELETE FROM users WHERE id IN (SELECT id FROM doomed)",
			wantCount:  "WITH `doomed` AS (SELECT `id` FROM `users` WHERE `stale`=1) SELECT COUNT(1) FROM `users` WHERE `id` IN (SELECT `id` FROM `doomed`)",
			wantSample: "WITH `doomed` AS (SELECT `id` FROM `users` WHERE `stale`=1) SELECT * FROM `users` WHERE `id` IN (SELECT `id` FROM `doomed`) LIMIT 5",
		},
	})
}

func TestPlanPreviewInsert(t *testing.T) {
	runPreviewCases(t, []previewCase{
		{
			// The rows are in the statement, so counting them needs no query —
			// and no sample either: the human is looking at the values.
			name:       "insert values counts its tuples without asking the database",
			sql:        "INSERT INTO users (id, name) VALUES (1,'a'), (2,'b'), (3,'c')",
			wantStatic: 3,
		},
		{
			name:       "insert set is one row",
			sql:        "INSERT INTO users SET id = 1, name = 'a'",
			wantStatic: 1,
		},
		{
			name:       "insert select counts the source select",
			sql:        "INSERT INTO users (id) SELECT id FROM staging WHERE ready = 1",
			wantCount:  "SELECT COUNT(1) FROM (SELECT `id` FROM `staging` WHERE `ready`=1) AS `impact_preview`",
			wantSample: "SELECT * FROM (SELECT `id` FROM `staging` WHERE `ready`=1) AS `impact_preview` LIMIT 5",
		},
		{
			name:       "insert from a union",
			sql:        "INSERT INTO users (id) SELECT id FROM a UNION SELECT id FROM b",
			wantCount:  "SELECT COUNT(1) FROM (SELECT `id` FROM `a` UNION SELECT `id` FROM `b`) AS `impact_preview`",
			wantSample: "SELECT * FROM (SELECT `id` FROM `a` UNION SELECT `id` FROM `b`) AS `impact_preview` LIMIT 5",
		},
	})
}

// TestPlanPreviewRefusals is the other half of the contract. Every statement
// here has to say it cannot be previewed, in a line the human can read.
func TestPlanPreviewRefusals(t *testing.T) {
	runPreviewCases(t, []previewCase{
		// DDL changes shape, not rows; there is nothing to select.
		{name: "create table", sql: "CREATE TABLE t (a int)", noPreview: "CREATE TABLE"},
		{name: "drop table", sql: "DROP TABLE users", noPreview: "DROP TABLE"},
		{name: "alter table", sql: "ALTER TABLE users ADD COLUMN x int", noPreview: "ALTER TABLE"},
		{name: "truncate", sql: "TRUNCATE TABLE users", noPreview: "TRUNCATE TABLE"},

		// More than one table: which rows change depends on the join, and a
		// COUNT over the join would not answer the question that was asked.
		{name: "multi-table update", sql: "UPDATE a, b SET a.x = b.x WHERE a.id = b.id", noPreview: "more than one table"},
		{name: "multi-table update with an explicit join", sql: "UPDATE a JOIN b ON a.id = b.id SET a.x = b.x", noPreview: "more than one table"},
		{name: "multi-table delete", sql: "DELETE t1 FROM t1 JOIN t2 ON t1.id = t2.id", noPreview: "more than one table"},
		{name: "multi-table delete with USING", sql: "DELETE FROM t1 USING t1, t2 WHERE t1.id = t2.id", noPreview: "more than one table"},

		// REPLACE deletes and inserts; the rows it removes are not in its text.
		{name: "replace", sql: "REPLACE INTO users (id) VALUES (1)", noPreview: "REPLACE"},

		// The rows are in a file on the server.
		{name: "load data", sql: "LOAD DATA INFILE '/tmp/x' INTO TABLE users", noPreview: "LOAD DATA"},

		// Statements whose effect is not in their own text.
		{name: "call", sql: "CALL do_something(1)", noPreview: "CALL statement"},
		{name: "set", sql: "SET autocommit = 0", noPreview: "SET"},
		{name: "grant", sql: "GRANT SELECT ON *.* TO 'x'@'%'", noPreview: "GRANT"},

		// Input the gate could not read is not input the rewriter can read.
		{name: "garbage", sql: "not sql at all", noPreview: "could not be parsed"},
		{name: "empty", sql: "", noPreview: "no statement"},
		{name: "whitespace only", sql: "   \n\t ", noPreview: "no statement"},
		{name: "two statements", sql: "DELETE FROM a; DELETE FROM b", noPreview: "more than one statement"},

		// A read reaches the rewriter only through the database backstop — the
		// classifier called it a read and the database refused it as a write —
		// so the refusal must not reassure. It says what is true of every
		// statement here: what it would change cannot be selected in advance.
		{name: "select refused by the backstop", sql: "SELECT record_visit(1)", noPreview: "SELECT statement has no rows that can be shown in advance"},
	})
}

func runPreviewCases(t *testing.T, cases []previewCase) {
	t.Helper()

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan, ok := guard.PlanPreview(tc.sql)

			if tc.noPreview != "" {
				if ok {
					t.Fatalf("PlanPreview(%q) planned %+v, want no preview", tc.sql, plan)
				}
				if plan.Reason == "" {
					t.Fatal("no preview and no reason; the human is told why there is none")
				}
				if !strings.Contains(plan.Reason, tc.noPreview) {
					t.Errorf("reason = %q, want it to mention %q", plan.Reason, tc.noPreview)
				}
				if strings.ContainsAny(plan.Reason, "\n\r") {
					t.Errorf("reason spans lines, want one line of prose:\n%s", plan.Reason)
				}
				if plan.CountSQL != "" || plan.SampleSQL != "" {
					t.Errorf("a refused plan carries SQL: count=%q sample=%q", plan.CountSQL, plan.SampleSQL)
				}
				return
			}

			if !ok {
				t.Fatalf("PlanPreview(%q) refused: %s", tc.sql, plan.Reason)
			}
			if plan.Reason != "" {
				t.Errorf("a planned preview carries a refusal reason: %q", plan.Reason)
			}
			if plan.CountSQL != tc.wantCount {
				t.Errorf("CountSQL  = %q\n     want   %q", plan.CountSQL, tc.wantCount)
			}
			if plan.SampleSQL != tc.wantSample {
				t.Errorf("SampleSQL = %q\n     want   %q", plan.SampleSQL, tc.wantSample)
			}
			if plan.StaticCount != tc.wantStatic {
				t.Errorf("StaticCount = %d, want %d", plan.StaticCount, tc.wantStatic)
			}
			if plan.CountSQL == "" && plan.StaticCount == 0 {
				t.Error("the plan neither counts nor knows a count; a preview must produce one")
			}

			// The claim that makes the rewrite safe: everything the plan asks
			// the database to run is provably read-only.
			for _, query := range []string{plan.CountSQL, plan.SampleSQL} {
				if query == "" {
					continue
				}
				if got := guard.Classify(query); !got.IsRead() {
					t.Errorf("the plan would run %q, which the gate calls a %s (%s)", query, got.Kind, got.Reason)
				}
			}
		})
	}
}

// TestPlanPreviewNeverEchoesTheMutation is the safety claim stated on its own:
// whatever the rewrite produces, it is never the statement it was given. A plan
// that handed the mutation back would execute it to preview it.
func TestPlanPreviewNeverEchoesTheMutation(t *testing.T) {
	mutations := []string{
		"UPDATE users SET name = 'x' WHERE id = 1",
		"DELETE FROM users",
		"DELETE FROM users WHERE stale = 1 ORDER BY id LIMIT 3",
		"INSERT INTO users (id) SELECT id FROM staging",
		"UPDATE `users` SET `name` = 'x'",
	}
	for _, sql := range mutations {
		plan, ok := guard.PlanPreview(sql)
		if !ok {
			t.Fatalf("PlanPreview(%q) refused: %s", sql, plan.Reason)
		}
		for _, query := range []string{plan.CountSQL, plan.SampleSQL} {
			if query == "" {
				continue
			}
			upper := strings.ToUpper(query)
			for _, verb := range []string{"UPDATE ", "DELETE ", "INSERT "} {
				if strings.Contains(upper, verb) {
					t.Errorf("the plan for %q would run %q, which contains %q", sql, query, strings.TrimSpace(verb))
				}
			}
		}
	}
}

// TestPlanPreviewIsSafeUnderConcurrency guards the same parser pooling the
// classifier depends on: the plan is built from an AST the parser owns, so
// returning the parser before the SQL has been rendered would let another
// goroutine rewrite the statement being planned.
func TestPlanPreviewIsSafeUnderConcurrency(t *testing.T) {
	statements := []string{
		"UPDATE users SET name = 'x' WHERE id = 1",
		"DELETE FROM orders WHERE total > 100 ORDER BY id LIMIT 5",
		"INSERT INTO users (id) VALUES (1), (2)",
		"CREATE TABLE t (a int)",
		"UPDATE a, b SET a.x = b.x",
	}

	want := make([]guard.PreviewPlan, len(statements))
	for i, sql := range statements {
		want[i], _ = guard.PlanPreview(sql)
	}

	const rounds = 100
	failures := make(chan string, rounds*len(statements))
	for range rounds {
		for i, sql := range statements {
			go func() {
				if got, _ := guard.PlanPreview(sql); got != want[i] {
					failures <- "PlanPreview(" + sql + ") differed under concurrency"
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

// TestNoPreviewIsAFirstClassState pins the difference between "there is no
// preview" and "the preview found nothing", which mean opposite things.
func TestNoPreviewIsAFirstClassState(t *testing.T) {
	none := guard.NoPreview("no preview is available for a CREATE TABLE statement")
	if none.Available {
		t.Error("NoPreview reports itself available")
	}
	if none.Reason == "" {
		t.Error("NoPreview carries no reason")
	}

	empty := guard.Preview{Available: true, Count: 0}
	if !empty.Available {
		t.Error("a preview that matched no rows reports itself unavailable")
	}
	if empty.Reason != "" {
		t.Errorf("an available preview carries a refusal reason: %q", empty.Reason)
	}
}
