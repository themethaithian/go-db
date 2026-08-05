package service_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/themethaithian/go-db/internal/db"
	"github.com/themethaithian/go-db/internal/db/dbtest"
	"github.com/themethaithian/go-db/internal/guard"
	"github.com/themethaithian/go-db/internal/service"
)

// These run the human write path against a real MySQL 8, because the claims it
// makes are claims about a database. That the advisory count matches reality is
// only true if the rewrite is a correct rewrite of MySQL's semantics; that the
// count and the outcome can disagree is only observable if something else can
// change the data in between. A fake would agree with whatever we wrote.
//
// They share the container started in connection_integration_test.go and skip
// when Docker is unavailable.

// confirming returns a facade over the real MySQL adapter with one connected
// Profile, plus the directory its audit log is written to.
func confirming(t *testing.T, host string, port int) (*service.AppService, string) {
	t.Helper()
	return approving(t, host, port, 0)
}

// approving is confirming with the Approval Console's deadline shortened to
// something a test can afford to wait out. These run on the wall clock — the
// point of an integration test is that the real machinery is in play — so the
// timeout under test is short rather than faked.
func approving(t *testing.T, host string, port int, timeout time.Duration) (*service.AppService, string) {
	t.Helper()

	auditDir := t.TempDir()
	svc := service.NewWithApproval(
		db.NewProfileStore(t.TempDir(), dbtest.NewFakeKeychain()),
		db.NewMySQLDriver(),
		nil,
		guard.NewJSONLAuditLog(auditDir),
		nil,
		timeout,
		nil,
	)
	t.Cleanup(func() {
		if err := svc.Close(); err != nil {
			t.Errorf("closing the Connection Registry: %v", err)
		}
	})

	mustSave(t, svc, db.Profile{
		Name: "local", Host: host, Port: port, User: mysqlRootUser, Database: mysqlDatabase,
	}, mysqlRootPassword)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := svc.Connect(ctx, "local"); err != nil {
		t.Fatalf("Connect(local): %v", err)
	}
	return svc, auditDir
}

// rowsIn counts a table without going through the facade.
func rowsIn(t *testing.T, pool *sql.DB, table string) int {
	t.Helper()

	var count int
	if err := pool.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil {
		t.Fatalf("counting %s: %v", table, err)
	}
	return count
}

func timeout(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// TestIntegrationInlineConfirmUpdate is the whole path against a real server:
// the preview's count and sample are what MySQL would really touch, the
// confirmation changes exactly those rows, and the record pairs the two.
func TestIntegrationInlineConfirmUpdate(t *testing.T) {
	host, port := requireMySQL(t)
	pool := adminPool(t, host, port)
	seed(t, pool, "confirm_users",
		"CREATE TABLE confirm_users (id INT PRIMARY KEY, name VARCHAR(32) NOT NULL, stale TINYINT NOT NULL)",
		"INSERT INTO confirm_users VALUES (1,'ada',1), (2,'grace',1), (3,'alan',0)",
	)
	svc, auditDir := confirming(t, host, port)
	ctx := timeout(t)

	const update = "UPDATE confirm_users SET name = 'archived' WHERE stale = 1"
	pending := svc.RunQuery(ctx, "local", update, guard.OriginHuman)

	if pending.Status != service.QueryRequiresConfirmation {
		t.Fatalf("status = %q, want %q (message: %s)", pending.Status, service.QueryRequiresConfirmation, pending.Message)
	}
	if pending.Preview == nil || !pending.Preview.Available {
		t.Fatalf("preview = %+v, want an available preview", pending.Preview)
	}
	// The advisory count is the real one: two of the three rows are stale.
	if pending.Preview.Count != 2 {
		t.Errorf("advisory count = %d, want the 2 rows the WHERE clause matches", pending.Preview.Count)
	}
	if !equalStrings(pending.Preview.Columns, []string{"id", "name", "stale"}) {
		t.Errorf("sample columns = %v, want the table's columns", pending.Preview.Columns)
	}
	if len(pending.Preview.Rows) != 2 {
		t.Fatalf("sample holds %d rows, want the 2 affected ones", len(pending.Preview.Rows))
	}
	for i, want := range []string{"ada", "grace"} {
		if got := pending.Preview.Rows[i][1]; got == nil || *got != want {
			t.Errorf("sample row %d name = %v, want %q", i, got, want)
		}
	}

	// Nothing has happened yet.
	if got := names(t, pool, "confirm_users"); !equalStrings(got, []string{"ada", "grace", "alan"}) {
		t.Fatalf("table holds %v before confirmation, want it untouched", got)
	}

	confirmed := svc.ConfirmPending(ctx, pending.PendingID)
	if confirmed.Status != service.QueryExecuted {
		t.Fatalf("confirm = %q (message: %s)", confirmed.Status, confirmed.Message)
	}
	if confirmed.AffectedRows != 2 {
		t.Errorf("AffectedRows = %d, want 2", confirmed.AffectedRows)
	}
	if got := names(t, pool, "confirm_users"); !equalStrings(got, []string{"archived", "archived", "alan"}) {
		t.Errorf("table holds %v, want the two stale rows archived", got)
	}

	record := auditRecords(t, auditDir)
	if len(record) != 1 {
		t.Fatalf("the audit log holds %d records, want 1", len(record))
	}
	if record[0]["advisory_count"] != float64(2) || record[0]["affected_rows"] != float64(2) {
		t.Errorf("audit advisory=%v actual=%v, want both 2",
			record[0]["advisory_count"], record[0]["affected_rows"])
	}
	if record[0]["sql"] != update {
		t.Errorf("audit sql = %v, want the statement as submitted", record[0]["sql"])
	}
}

// TestIntegrationAdvisoryCountCanDiverge is why the preview is called advisory
// and why the log keeps both numbers. Between the preview and the keypress the
// data moved, and the record has to show it — this is the case that a design
// which recorded only the estimate, or only the outcome, could not describe.
func TestIntegrationAdvisoryCountCanDiverge(t *testing.T) {
	host, port := requireMySQL(t)
	pool := adminPool(t, host, port)
	seed(t, pool, "diverge_users",
		"CREATE TABLE diverge_users (id INT PRIMARY KEY, name VARCHAR(32) NOT NULL, stale TINYINT NOT NULL)",
		"INSERT INTO diverge_users VALUES (1,'ada',1), (2,'grace',1), (3,'alan',1)",
	)
	svc, auditDir := confirming(t, host, port)
	ctx := timeout(t)

	pending := svc.RunQuery(ctx, "local", "UPDATE diverge_users SET name = 'archived' WHERE stale = 1", guard.OriginHuman)
	if pending.Preview == nil || pending.Preview.Count != 3 {
		t.Fatalf("preview = %+v, want an advisory count of 3", pending.Preview)
	}

	// Somebody else changes the data while the human is deciding.
	if _, err := pool.ExecContext(ctx, "DELETE FROM diverge_users WHERE id = 3"); err != nil {
		t.Fatalf("changing the data behind the preview: %v", err)
	}

	confirmed := svc.ConfirmPending(ctx, pending.PendingID)
	if confirmed.Status != service.QueryExecuted {
		t.Fatalf("confirm = %q (message: %s)", confirmed.Status, confirmed.Message)
	}
	if confirmed.AffectedRows != 2 {
		t.Fatalf("AffectedRows = %d, want the 2 rows that were still there", confirmed.AffectedRows)
	}
	if !strings.Contains(confirmed.Message, "2") || !strings.Contains(confirmed.Message, "3") {
		t.Errorf("message = %q, want it to show that the outcome differed from the estimate", confirmed.Message)
	}

	records := auditRecords(t, auditDir)
	if len(records) != 1 {
		t.Fatalf("the audit log holds %d records, want 1", len(records))
	}
	if records[0]["advisory_count"] != float64(3) {
		t.Errorf("audit advisory_count = %v, want the 3 the human was shown", records[0]["advisory_count"])
	}
	if records[0]["affected_rows"] != float64(2) {
		t.Errorf("audit affected_rows = %v, want the 2 that really changed", records[0]["affected_rows"])
	}
}

// TestIntegrationDeleteWithoutWhereShowsTheWholeTable is the footgun the
// feature exists for. The count is the whole table, and MySQL agrees.
func TestIntegrationDeleteWithoutWhereShowsTheWholeTable(t *testing.T) {
	host, port := requireMySQL(t)
	pool := adminPool(t, host, port)
	seed(t, pool, "whole_table",
		"CREATE TABLE whole_table (id INT PRIMARY KEY, name VARCHAR(32) NOT NULL)",
		"INSERT INTO whole_table VALUES (1,'ada'), (2,'grace'), (3,'alan'), (4,'edsger')",
	)
	svc, _ := confirming(t, host, port)
	ctx := timeout(t)

	pending := svc.RunQuery(ctx, "local", "DELETE FROM whole_table", guard.OriginHuman)

	if pending.Preview == nil || !pending.Preview.Available {
		t.Fatalf("preview = %+v, want an available preview", pending.Preview)
	}
	if pending.Preview.Count != 4 {
		t.Errorf("advisory count = %d, want all 4 rows", pending.Preview.Count)
	}
	if len(pending.Preview.Rows) != 4 {
		t.Errorf("sample holds %d rows, want the 4 that fit under the cap of %d",
			len(pending.Preview.Rows), guard.PreviewSampleRows)
	}
	if !strings.Contains(pending.Message, "4") {
		t.Errorf("message = %q, want the human to see the number before they press a key", pending.Message)
	}
	if got := rowsIn(t, pool, "whole_table"); got != 4 {
		t.Errorf("the table lost %d rows to a preview", 4-got)
	}
}

// TestIntegrationSampleIsCappedAtTheLimit: the sample is a sample, not a
// second copy of the table, however many rows the mutation would touch.
func TestIntegrationSampleIsCappedAtTheLimit(t *testing.T) {
	host, port := requireMySQL(t)
	pool := adminPool(t, host, port)
	seed(t, pool, "many_affected",
		"CREATE TABLE many_affected (id INT PRIMARY KEY, name VARCHAR(32) NOT NULL)",
		`INSERT INTO many_affected (id, name)
			WITH RECURSIVE seq AS (SELECT 1 AS n UNION ALL SELECT n+1 FROM seq WHERE n < 50)
			SELECT n, CONCAT('row', n) FROM seq`,
	)
	svc, _ := confirming(t, host, port)
	ctx := timeout(t)

	pending := svc.RunQuery(ctx, "local", "UPDATE many_affected SET name = 'x'", guard.OriginHuman)

	if pending.Preview == nil || pending.Preview.Count != 50 {
		t.Fatalf("preview = %+v, want an advisory count of 50", pending.Preview)
	}
	if len(pending.Preview.Rows) != guard.PreviewSampleRows {
		t.Errorf("sample holds %d rows, want the cap of %d", len(pending.Preview.Rows), guard.PreviewSampleRows)
	}
}

// TestIntegrationDDLIsConfirmedWithoutAPreview: DDL cannot be rewritten into
// reads, and MySQL will not refuse it inside a read-only transaction either
// (see TestIntegrationBackstopDoesNotCoverDDL), so the confirmation is the only
// thing standing between the human and the statement. It still runs when they
// say so.
func TestIntegrationDDLIsConfirmedWithoutAPreview(t *testing.T) {
	host, port := requireMySQL(t)
	pool := adminPool(t, host, port)
	seed(t, pool, "made_by_confirmation")
	svc, auditDir := confirming(t, host, port)
	ctx := timeout(t)

	const ddl = "CREATE TABLE made_by_confirmation (id INT PRIMARY KEY)"
	pending := svc.RunQuery(ctx, "local", ddl, guard.OriginHuman)

	if pending.Status != service.QueryRequiresConfirmation {
		t.Fatalf("status = %q, want %q (message: %s)", pending.Status, service.QueryRequiresConfirmation, pending.Message)
	}
	if pending.Preview == nil || pending.Preview.Available {
		t.Fatalf("preview = %+v, want it to say there is none", pending.Preview)
	}
	if !strings.Contains(pending.Preview.Reason, "CREATE TABLE") {
		t.Errorf("no-preview reason = %q, want it to name the statement", pending.Preview.Reason)
	}

	confirmed := svc.ConfirmPending(ctx, pending.PendingID)
	if confirmed.Status != service.QueryExecuted {
		t.Fatalf("confirm = %q (message: %s)", confirmed.Status, confirmed.Message)
	}

	if got := rowsIn(t, pool, "made_by_confirmation"); got != 0 {
		t.Errorf("the new table holds %d rows, want 0", got)
	}

	records := auditRecords(t, auditDir)
	if len(records) != 1 {
		t.Fatalf("the audit log holds %d records, want 1", len(records))
	}
	if reason, _ := records[0]["no_preview_reason"].(string); !strings.Contains(reason, "CREATE TABLE") {
		t.Errorf("audit no_preview_reason = %q, want the reason there was none", reason)
	}
	if _, present := records[0]["advisory_count"]; present {
		t.Error("DDL recorded an advisory count it never had")
	}
}

// TestIntegrationCancelLeavesTheDataAlone: the refusal path, proven where it
// matters — against the rows themselves.
func TestIntegrationCancelLeavesTheDataAlone(t *testing.T) {
	host, port := requireMySQL(t)
	pool := adminPool(t, host, port)
	seed(t, pool, "cancelled_users",
		"CREATE TABLE cancelled_users (id INT PRIMARY KEY, name VARCHAR(32) NOT NULL)",
		"INSERT INTO cancelled_users VALUES (1,'ada'), (2,'grace')",
	)
	svc, auditDir := confirming(t, host, port)
	ctx := timeout(t)

	pending := svc.RunQuery(ctx, "local", "DELETE FROM cancelled_users", guard.OriginHuman)
	if pending.Status != service.QueryRequiresConfirmation {
		t.Fatalf("status = %q (message: %s)", pending.Status, pending.Message)
	}

	if got := svc.CancelPending(pending.PendingID); got.Status != service.QueryCancelled {
		t.Fatalf("cancel = %q (message: %s)", got.Status, got.Message)
	}

	if got := names(t, pool, "cancelled_users"); !equalStrings(got, []string{"ada", "grace"}) {
		t.Errorf("table holds %v, want [ada grace]: a cancelled DELETE ran", got)
	}

	records := auditRecords(t, auditDir)
	if len(records) != 1 {
		t.Fatalf("the audit log holds %d records, want the refusal recorded", len(records))
	}
	if records[0]["decision"] != "cancelled" {
		t.Errorf("audit decision = %v, want \"cancelled\"", records[0]["decision"])
	}
	if _, present := records[0]["affected_rows"]; present {
		t.Error("a cancelled DELETE recorded affected rows")
	}

	// Reads on the same Profile still work afterwards.
	read := svc.RunQuery(ctx, "local", "SELECT name FROM cancelled_users ORDER BY id", guard.OriginHuman)
	if read.Status != service.QueryOK || len(read.Rows) != 2 {
		t.Errorf("reading after a cancellation: status=%q rows=%d", read.Status, len(read.Rows))
	}
}

// TestIntegrationPreviewHoldsNoLocks is the reason the preview is a rewrite and
// not an execute-and-roll-back (ADR 0003). While a confirmation is open, the
// rows it describes must still be writable by everyone else — a preview that
// held row locks would block the whole table for as long as a human took to
// read it.
func TestIntegrationPreviewHoldsNoLocks(t *testing.T) {
	host, port := requireMySQL(t)
	pool := adminPool(t, host, port)
	seed(t, pool, "unlocked_users",
		"CREATE TABLE unlocked_users (id INT PRIMARY KEY, name VARCHAR(32) NOT NULL)",
		"INSERT INTO unlocked_users VALUES (1,'ada'), (2,'grace')",
	)
	svc, _ := confirming(t, host, port)
	ctx := timeout(t)

	pending := svc.RunQuery(ctx, "local", "UPDATE unlocked_users SET name = 'archived'", guard.OriginHuman)
	if pending.Status != service.QueryRequiresConfirmation {
		t.Fatalf("status = %q (message: %s)", pending.Status, pending.Message)
	}

	// The confirmation is still open. Another connection must be able to write
	// the very rows it is about, without waiting for a lock.
	locked, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := pool.ExecContext(locked, "UPDATE unlocked_users SET name = 'someone else' WHERE id = 1"); err != nil {
		t.Fatalf("writing a previewed row while the confirmation is open: %v; the preview is holding locks", err)
	}
}

// These run the AI write path against a real MySQL 8. The blocking is real —
// the query waits in a goroutine while the test plays the human at the
// Approval Console — so what these prove that a fake cannot is that the rows
// really moved, or really did not, on the far side of a decision that was made
// somewhere else entirely.

// submitAI runs an AI-originated query in its own goroutine, as the localhost
// API handler does for the MCP proxy, and returns the channel its result will
// arrive on.
func submitAI(svc *service.AppService, ctx context.Context, sql string) <-chan service.QueryResult {
	results := make(chan service.QueryResult, 1)
	go func() { results <- svc.RunQuery(ctx, "local", sql, guard.OriginAI) }()
	return results
}

// waitingInConsole polls the Approval Console until the submitted mutation
// shows up there. The submitting goroutine is real here, so the test watches
// for it to arrive rather than being handed a synchronisation point.
func waitingInConsole(t *testing.T, svc *service.AppService) guard.Waiting {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if waiting := svc.ListPendingApprovals(); len(waiting) == 1 {
			return waiting[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("nothing ever reached the Approval Console")
	return guard.Waiting{}
}

// decidedBy returns what the blocked caller finally got.
func decidedBy(t *testing.T, results <-chan service.QueryResult) service.QueryResult {
	t.Helper()

	select {
	case got := <-results:
		return got
	case <-time.After(30 * time.Second):
		t.Fatal("the AI's call never returned; it is stuck waiting for a decision")
		return service.QueryResult{}
	}
}

// TestIntegrationApprovalConsoleUpdate is the AI write path end to end against
// a real server: the query waits, a human approves it somewhere else, and the
// rows the preview described are the rows that actually change — with both
// numbers in the record.
func TestIntegrationApprovalConsoleUpdate(t *testing.T) {
	host, port := requireMySQL(t)
	pool := adminPool(t, host, port)
	seed(t, pool, "approved_users",
		"CREATE TABLE approved_users (id INT PRIMARY KEY, name VARCHAR(32) NOT NULL, stale TINYINT NOT NULL)",
		"INSERT INTO approved_users VALUES (1,'ada',1), (2,'grace',1), (3,'alan',0)",
	)
	svc, auditDir := confirming(t, host, port)
	ctx := timeout(t)

	const update = "UPDATE approved_users SET name = 'archived' WHERE stale = 1"
	results := submitAI(svc, ctx, update)

	waiting := waitingInConsole(t, svc)
	if waiting.SQL != update || waiting.Origin != guard.OriginAI {
		t.Fatalf("console entry = %+v, want the agent's statement", waiting.Pending)
	}
	if !waiting.Preview.Available || waiting.Preview.Count != 2 {
		t.Errorf("advisory count = %+v, want the 2 rows the WHERE clause matches", waiting.Preview)
	}
	if waiting.RemainingMillis <= 0 {
		t.Errorf("remaining = %dms, want time left on the default deadline", waiting.RemainingMillis)
	}

	// Nothing has happened while the query waits.
	if got := names(t, pool, "approved_users"); !equalStrings(got, []string{"ada", "grace", "alan"}) {
		t.Fatalf("table holds %v while the approval is pending, want it untouched", got)
	}

	if ack := svc.ApprovePending(waiting.ID); !ack.OK {
		t.Fatalf("ApprovePending = %+v", ack)
	}

	got := decidedBy(t, results)
	if got.Status != service.QueryExecuted {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.QueryExecuted, got.Message)
	}
	if got.AffectedRows != 2 {
		t.Errorf("AffectedRows = %d, want 2", got.AffectedRows)
	}
	if got := names(t, pool, "approved_users"); !equalStrings(got, []string{"archived", "archived", "alan"}) {
		t.Errorf("table holds %v, want the two stale rows archived", got)
	}

	records := auditRecords(t, auditDir)
	if len(records) != 1 {
		t.Fatalf("the audit log holds %d records, want 1", len(records))
	}
	for field, want := range map[string]any{
		"origin":         "ai",
		"decision":       "approved",
		"decider":        "human",
		"advisory_count": float64(2),
		"affected_rows":  float64(2),
		"sql":            update,
	} {
		if records[0][field] != want {
			t.Errorf("audit %s = %v, want %v", field, records[0][field], want)
		}
	}
}

// TestIntegrationApprovalConsoleRejectLeavesTheDataAlone: the refusal path,
// proven where it matters — against the rows themselves.
func TestIntegrationApprovalConsoleRejectLeavesTheDataAlone(t *testing.T) {
	host, port := requireMySQL(t)
	pool := adminPool(t, host, port)
	seed(t, pool, "rejected_users",
		"CREATE TABLE rejected_users (id INT PRIMARY KEY, name VARCHAR(32) NOT NULL)",
		"INSERT INTO rejected_users VALUES (1,'ada'), (2,'grace')",
	)
	svc, auditDir := confirming(t, host, port)
	ctx := timeout(t)

	results := submitAI(svc, ctx, "DELETE FROM rejected_users")
	waiting := waitingInConsole(t, svc)

	if ack := svc.RejectPending(waiting.ID); !ack.OK {
		t.Fatalf("RejectPending = %+v", ack)
	}

	got := decidedBy(t, results)
	if got.Status != service.QueryRejected {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.QueryRejected, got.Message)
	}
	if got := names(t, pool, "rejected_users"); !equalStrings(got, []string{"ada", "grace"}) {
		t.Errorf("table holds %v, want [ada grace]: a rejected DELETE ran", got)
	}

	records := auditRecords(t, auditDir)
	if len(records) != 1 {
		t.Fatalf("the audit log holds %d records, want the refusal recorded", len(records))
	}
	if records[0]["decision"] != "rejected" || records[0]["decider"] != "human" {
		t.Errorf("audit decision/decider = %v/%v, want rejected/human", records[0]["decision"], records[0]["decider"])
	}
	if _, present := records[0]["affected_rows"]; present {
		t.Error("a rejected DELETE recorded affected rows")
	}
}

// TestIntegrationApprovalTimeoutLeavesTheDataAlone is the auto-reject on the
// real clock: nobody was at the keyboard, the deadline passed, and the DELETE
// never reached the server. The deadline is short here for obvious reasons; the
// unit tests fire it rather than wait for it.
func TestIntegrationApprovalTimeoutLeavesTheDataAlone(t *testing.T) {
	host, port := requireMySQL(t)
	pool := adminPool(t, host, port)
	seed(t, pool, "unanswered_users",
		"CREATE TABLE unanswered_users (id INT PRIMARY KEY, name VARCHAR(32) NOT NULL)",
		"INSERT INTO unanswered_users VALUES (1,'ada'), (2,'grace')",
	)
	svc, auditDir := approving(t, host, port, 300*time.Millisecond)
	ctx := timeout(t)

	results := submitAI(svc, ctx, "DELETE FROM unanswered_users")

	got := decidedBy(t, results)
	if got.Status != service.QueryTimedOut {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.QueryTimedOut, got.Message)
	}
	if got := names(t, pool, "unanswered_users"); !equalStrings(got, []string{"ada", "grace"}) {
		t.Errorf("table holds %v, want [ada grace]: a DELETE nobody approved ran", got)
	}
	if pending := svc.ListPendingApprovals(); len(pending) != 0 {
		t.Errorf("the console still offers %+v after the deadline passed", pending)
	}

	records := auditRecords(t, auditDir)
	if len(records) != 1 {
		t.Fatalf("the audit log holds %d records, want the auto-reject recorded", len(records))
	}
	if records[0]["decision"] != "timeout" || records[0]["decider"] != "timeout" {
		t.Errorf("audit decision/decider = %v/%v, want timeout/timeout",
			records[0]["decision"], records[0]["decider"])
	}
	if records[0]["advisory_count"] != float64(2) {
		t.Errorf("audit advisory_count = %v, want the 2 rows nobody approved", records[0]["advisory_count"])
	}
}
