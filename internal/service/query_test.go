package service_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/themethaithian/go-db/internal/db"
	"github.com/themethaithian/go-db/internal/db/dbtest"
	"github.com/themethaithian/go-db/internal/guard"
	"github.com/themethaithian/go-db/internal/service"
)

// These tests state the read path's flow through the facade: what runs, what
// does not, and what the UI is told. Which statements are reads is specified in
// internal/guard, not here.

// newQueryFacade builds an App Service with one connected Profile, over a fake
// database the test scripts.
func newQueryFacade(t *testing.T, driver *dbtest.FakeDriver) *service.AppService {
	t.Helper()

	svc := newConnectedFacade(t, dbtest.NewFakeKeychain(), driver)
	driver.Accept("local", "s3cret")
	mustSave(t, svc, localProfile("local"), "s3cret")
	if err := svc.Connect(context.Background(), "local"); err != nil {
		t.Fatalf("Connect(local): %v", err)
	}
	return svc
}

func str(s string) *string { return &s }

func TestRunQueryReturnsTheRows(t *testing.T) {
	driver := dbtest.NewFakeDriver()
	driver.Answer("local", db.ResultSet{
		Columns: []string{"id", "name", "deleted_at"},
		Rows: [][]*string{
			{str("1"), str("ada"), nil},
			{str("2"), str("grace"), str("2024-01-02 03:04:05")},
		},
	})
	svc := newQueryFacade(t, driver)

	got := svc.RunQuery(context.Background(), "local", "SELECT id, name, deleted_at FROM users", guard.OriginHuman)

	if got.Status != service.QueryOK {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.QueryOK, got.Message)
	}
	if !got.Classification.IsRead() {
		t.Errorf("classification = %+v, want a read", got.Classification)
	}
	if got.Origin != guard.OriginHuman {
		t.Errorf("Origin = %q, want it echoed back as %q", got.Origin, guard.OriginHuman)
	}
	if !equalStrings(got.Columns, []string{"id", "name", "deleted_at"}) {
		t.Errorf("columns = %v, want [id name deleted_at]", got.Columns)
	}
	if len(got.Rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(got.Rows))
	}
	if got.Rows[0][1] == nil || *got.Rows[0][1] != "ada" {
		t.Errorf("row 0 col 1 = %v, want \"ada\"", got.Rows[0][1])
	}
	if got.Rows[0][2] != nil {
		t.Errorf("row 0 col 2 = %q, want NULL to arrive as nil", *got.Rows[0][2])
	}
	if got.Truncated {
		t.Error("result reports itself truncated, want not truncated")
	}

	// The statement must reach the database exactly as written.
	if queries := driver.Queries("local"); !equalStrings(queries, []string{"SELECT id, name, deleted_at FROM users"}) {
		t.Errorf("the database was asked %v, want the statement as written, once", queries)
	}
}

func TestRunQueryReportsTruncation(t *testing.T) {
	driver := dbtest.NewFakeDriver()
	driver.Answer("local", db.ResultSet{
		Columns:   []string{"id"},
		Rows:      [][]*string{{str("1")}},
		Truncated: true,
	})
	svc := newQueryFacade(t, driver)

	got := svc.RunQuery(context.Background(), "local", "SELECT id FROM users", guard.OriginHuman)

	if got.Status != service.QueryOK {
		t.Fatalf("status = %q, want %q", got.Status, service.QueryOK)
	}
	if !got.Truncated {
		t.Error("Truncated = false, want the cap to be visible in the result")
	}
	if !strings.Contains(strings.ToLower(got.Message), "truncat") {
		t.Errorf("message = %q, want it to say the result was truncated", got.Message)
	}
}

// TestRunQueryWithholdsMutations is the gate refusing to execute an unapproved
// mutation. What happens next is the Origin's policy — Inline Confirm here,
// covered in approval_test.go — and the claim this test makes is the one that
// holds for every Origin: the statement the human typed does not reach the
// database, as a read or as a write, until it has been decided.
func TestRunQueryWithholdsMutations(t *testing.T) {
	for _, sql := range []string{
		"DELETE FROM users WHERE id = 1",
		"UPDATE users SET name = 'a'",
		"DROP TABLE users",
		"EXPLAIN ANALYZE DELETE FROM users",
		"SELECT 1; DROP TABLE users",
		"SELECT * FROM users FOR UPDATE",
		"this is not sql",
	} {
		t.Run(sql, func(t *testing.T) {
			driver := dbtest.NewFakeDriver()
			svc := newQueryFacade(t, driver)

			got := svc.RunQuery(context.Background(), "local", sql, guard.OriginHuman)

			if got.Status != service.QueryRequiresConfirmation {
				t.Errorf("status = %q, want %q (message: %s)", got.Status, service.QueryRequiresConfirmation, got.Message)
			}
			if got.Classification.Kind != guard.Mutation {
				t.Errorf("classification = %+v, want a mutation", got.Classification)
			}
			if got.Classification.Reason == "" {
				t.Error("no reason given for withholding the query")
			}
			if !strings.Contains(got.Message, got.Classification.Reason) {
				t.Errorf("message %q does not carry the reason %q", got.Message, got.Classification.Reason)
			}
			if got.Columns != nil || got.Rows != nil {
				t.Errorf("a withheld query returned data: columns=%v rows=%v", got.Columns, got.Rows)
			}

			// The point of the test: the statement itself went nowhere. Reads
			// the Impact Preview asked for are a different matter — they are
			// rewrites, and internal/guard proves they cannot write.
			if execs := driver.Execs("local"); len(execs) != 0 {
				t.Errorf("the database executed %v, want nothing: a mutation must not run before it is confirmed", execs)
			}
			for _, asked := range driver.Queries("local") {
				if asked == sql {
					t.Errorf("the mutation was sent to the database as a read: %q", asked)
				}
			}
		})
	}
}

// TestRunQueryRerouteOnBackstop covers a classifier miss: the statement looked
// like a read, and the database refused it inside the read-only transaction.
// The result must show the reclassification, not a bare failure.
func TestRunQueryRerouteOnBackstop(t *testing.T) {
	driver := dbtest.NewFakeDriver()
	driver.RejectWrites("local")
	svc := newQueryFacade(t, driver)

	// A SELECT calling a function that writes: provably read-only to the
	// classifier, refused by the database. The Origin decides what happens next
	// — the AI's route is TestAIBackstoppedReadWaitsForApproval — and what this
	// test states holds for both: the reroute is visible in the result.
	got := svc.RunQuery(context.Background(), "local", "SELECT record_visit(1)", guard.OriginHuman)

	if got.Status != service.QueryRequiresConfirmation {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.QueryRequiresConfirmation, got.Message)
	}
	if got.Classification.Kind != guard.Mutation {
		t.Errorf("classification = %+v, want the reroute to be visible as a mutation", got.Classification)
	}
	if got.Classification != guard.Backstopped() {
		t.Errorf("classification = %+v, want %+v", got.Classification, guard.Backstopped())
	}
	if !strings.Contains(got.Message, got.Classification.Reason) {
		t.Errorf("message %q does not say why the query was rerouted", got.Message)
	}
	if got.Origin != guard.OriginHuman {
		t.Errorf("Origin = %q, want %q", got.Origin, guard.OriginHuman)
	}
	if got.Rows != nil {
		t.Errorf("a rerouted query returned rows: %v", got.Rows)
	}
}

func TestRunQueryOnAProfileThatIsNotConnected(t *testing.T) {
	cases := []struct {
		name    string
		profile string
	}{
		{name: "a saved Profile with no open connection", profile: "local"},
		{name: "a Profile that does not exist", profile: "nope"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			driver := dbtest.NewFakeDriver()
			svc := newConnectedFacade(t, dbtest.NewFakeKeychain(), driver)
			mustSave(t, svc, localProfile("local"), "s3cret")

			got := svc.RunQuery(context.Background(), tc.profile, "SELECT 1", guard.OriginHuman)

			if got.Status != service.QueryNotConnected {
				t.Errorf("status = %q, want %q (message: %s)", got.Status, service.QueryNotConnected, got.Message)
			}
			if !strings.Contains(got.Message, tc.profile) {
				t.Errorf("message %q does not name the Profile %q", got.Message, tc.profile)
			}
			if strings.Contains(got.Message, "\n") {
				t.Errorf("message spans lines, want one readable line:\n%s", got.Message)
			}
			// The classification still holds: the statement was a read, there
			// was just nowhere to run it.
			if !got.Classification.IsRead() {
				t.Errorf("classification = %+v, want the read verdict to survive", got.Classification)
			}
		})
	}
}

func TestRunQueryReportsADatabaseFailure(t *testing.T) {
	driver := dbtest.NewFakeDriver()
	driver.FailQuery("local", errors.New("db: Unknown column 'nope' in 'field list'"))
	svc := newQueryFacade(t, driver)

	got := svc.RunQuery(context.Background(), "local", "SELECT nope FROM users", guard.OriginHuman)

	if got.Status != service.QueryFailed {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.QueryFailed, got.Message)
	}
	if !strings.Contains(got.Message, "Unknown column") {
		t.Errorf("message = %q, want the database's own wording kept", got.Message)
	}
	if strings.Contains(got.Message, "db: ") {
		t.Errorf("message = %q, want the package prefix stripped", got.Message)
	}
}

// TestClassifyIsExposedForTheEditor covers the badge the editor shows before
// anything is submitted: the same verdict RunQuery will reach, without a
// connection or a round trip.
func TestClassifyIsExposedForTheEditor(t *testing.T) {
	svc := newConnectedFacade(t, dbtest.NewFakeKeychain(), dbtest.NewFakeDriver())

	for _, tc := range []struct {
		sql  string
		want guard.Kind
	}{
		{"SELECT * FROM users", guard.Read},
		{"DELETE FROM users", guard.Mutation},
		{"", guard.Mutation},
	} {
		got := svc.Classify(tc.sql)
		if got.Kind != tc.want {
			t.Errorf("Classify(%q).Kind = %q, want %q", tc.sql, got.Kind, tc.want)
		}
		if got.Reason == "" {
			t.Errorf("Classify(%q) gave no reason to show", tc.sql)
		}
	}
}

func TestRunQueryCarriesEitherOrigin(t *testing.T) {
	for _, origin := range []guard.Origin{guard.OriginHuman, guard.OriginAI} {
		t.Run(string(origin), func(t *testing.T) {
			driver := dbtest.NewFakeDriver()
			driver.Answer("local", db.ResultSet{Columns: []string{"1"}, Rows: [][]*string{{str("1")}}})
			svc := newQueryFacade(t, driver)

			got := svc.RunQuery(context.Background(), "local", "SELECT 1", origin)

			if got.Status != service.QueryOK {
				t.Fatalf("status = %q, want %q", got.Status, service.QueryOK)
			}
			if got.Origin != origin {
				t.Errorf("Origin = %q, want %q", got.Origin, origin)
			}
		})
	}
}
