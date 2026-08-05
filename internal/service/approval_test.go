package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/themethaithian/go-db/internal/db"
	"github.com/themethaithian/go-db/internal/db/dbtest"
	"github.com/themethaithian/go-db/internal/guard"
	"github.com/themethaithian/go-db/internal/service"
)

// These tests state the human write path end to end at the facade seam: a
// mutation is previewed without being run, waits for one confirmation, and
// whatever the human decides is written to the audit log with what the preview
// predicted next to what actually happened.
//
// The audit assertions go through the real JSONL adapter rather than a fake.
// The file is the deliverable — it is what someone reads a week later to find
// out what was run — so a test that only proved a port was called would be
// testing the wrong thing.

// gateStart is the instant the fake clock counts from.
var gateStart = time.Date(2026, 8, 5, 9, 30, 0, 0, time.UTC)

// fakeClock hands out instants a second apart, so the order of the timestamps
// in a record is exact rather than approximate.
type fakeClock struct {
	mu   sync.Mutex
	next time.Time
}

func newFakeClock(start time.Time) *fakeClock { return &fakeClock{next: start} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.next
	c.next = c.next.Add(time.Second)
	return now
}

// gate is a facade with one connected Profile over a fake database, a real
// JSONL audit log, and a clock the test drives.
type gate struct {
	svc      *service.AppService
	driver   *dbtest.FakeDriver
	clock    *fakeClock
	auditDir string
}

func newGate(t *testing.T) *gate {
	t.Helper()

	driver := dbtest.NewFakeDriver()
	clock := newFakeClock(gateStart)
	auditDir := t.TempDir()

	svc := service.NewWithDriver(
		db.NewProfileStore(t.TempDir(), dbtest.NewFakeKeychain()),
		driver,
		guard.NewJSONLAuditLog(auditDir),
		clock.Now,
	)
	t.Cleanup(func() {
		if err := svc.Close(); err != nil {
			t.Errorf("closing the Connection Registry: %v", err)
		}
	})

	driver.Accept("local", "s3cret")
	mustSave(t, svc, localProfile("local"), "s3cret")
	if err := svc.Connect(context.Background(), "local"); err != nil {
		t.Fatalf("Connect(local): %v", err)
	}
	return &gate{svc: svc, driver: driver, clock: clock, auditDir: auditDir}
}

// scriptPreview teaches the fake database to answer the Impact Preview of sql:
// count from the plan's count query, sample from its sample query. The plan
// comes from the gate itself rather than from a hand-written string, so these
// tests pin the flow and internal/guard pins the rewrite.
func (g *gate) scriptPreview(t *testing.T, sql string, count int64, sample db.ResultSet) {
	t.Helper()

	plan, ok := guard.PlanPreview(sql)
	if !ok {
		t.Fatalf("PlanPreview(%q) refused: %s; this test needs a previewable statement", sql, plan.Reason)
	}
	g.driver.AnswerQuery("local", plan.CountSQL, db.ResultSet{
		Columns: []string{"COUNT(1)"},
		Rows:    [][]*string{{str(strconv.FormatInt(count, 10))}},
	})
	g.driver.AnswerQuery("local", plan.SampleSQL, sample)
}

// records reads back what the audit log holds, one decoded record per line.
func (g *gate) records(t *testing.T) []map[string]any {
	t.Helper()
	return auditRecords(t, g.auditDir)
}

// auditRecords decodes the JSONL audit log in dir. A log that was never written
// holds no records, which is a real answer: it is how a test says that nothing
// was decided.
func auditRecords(t *testing.T, dir string) []map[string]any {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(dir, guard.AuditFileName))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("reading the audit log: %v", err)
	}

	var records []map[string]any
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("audit line is not JSON: %v\n%s", err, line)
		}
		records = append(records, record)
	}
	return records
}

// onlyRecord returns the single audit record, failing if there is not exactly
// one: an extra record means a request was logged as a decision.
func (g *gate) onlyRecord(t *testing.T) map[string]any {
	t.Helper()

	records := g.records(t)
	if len(records) != 1 {
		t.Fatalf("the audit log holds %d records, want exactly 1: %v", len(records), records)
	}
	return records[0]
}

const updateOne = "UPDATE users SET name = 'x' WHERE id = 1"

func sampleRows() db.ResultSet {
	return db.ResultSet{
		Columns: []string{"id", "name"},
		Rows:    [][]*string{{str("1"), str("ada")}},
	}
}

// TestRunQueryPreviewsAMutationInsteadOfRunningIt is Inline Confirm's first
// half: the human sees what the statement would do, computed entirely from
// reads, and the statement itself has not run.
func TestRunQueryPreviewsAMutationInsteadOfRunningIt(t *testing.T) {
	g := newGate(t)
	g.scriptPreview(t, updateOne, 3, sampleRows())

	got := g.svc.RunQuery(context.Background(), "local", updateOne, guard.OriginHuman)

	if got.Status != service.QueryRequiresConfirmation {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.QueryRequiresConfirmation, got.Message)
	}
	if got.PendingID == "" {
		t.Fatal("no pending ID; there is nothing for a confirmation to name")
	}
	if got.Classification.Kind != guard.Mutation {
		t.Errorf("classification = %+v, want a mutation", got.Classification)
	}
	if got.Preview == nil {
		t.Fatal("no Impact Preview on the result")
	}
	if !got.Preview.Available {
		t.Fatalf("preview reports itself unavailable: %s", got.Preview.Reason)
	}
	if got.Preview.Count != 3 {
		t.Errorf("advisory count = %d, want the 3 the count query returned", got.Preview.Count)
	}
	if !equalStrings(got.Preview.Columns, []string{"id", "name"}) {
		t.Errorf("sample columns = %v, want [id name]", got.Preview.Columns)
	}
	if len(got.Preview.Rows) != 1 || got.Preview.Rows[0][1] == nil || *got.Preview.Rows[0][1] != "ada" {
		t.Errorf("sample rows = %v, want the row the sample query returned", got.Preview.Rows)
	}
	if !strings.Contains(got.Message, "3") {
		t.Errorf("message = %q, want it to carry the advisory count", got.Message)
	}

	// The whole point: the mutation was described, not performed.
	if execs := g.driver.Execs("local"); len(execs) != 0 {
		t.Errorf("the database executed %v while previewing; a preview must never run the mutation", execs)
	}
	for _, asked := range g.driver.Queries("local") {
		if asked == updateOne {
			t.Errorf("the mutation itself was sent as a read: %q", asked)
		}
	}
	if len(g.records(t)) != 0 {
		t.Error("a request with no decision was written to the audit log")
	}
}

// TestConfirmPendingExecutesOnceAndRecordsBoth is the second half. One call
// runs the statement exactly as submitted, and the record pairs what was
// predicted with what happened.
func TestConfirmPendingExecutesOnceAndRecordsBoth(t *testing.T) {
	g := newGate(t)
	g.scriptPreview(t, updateOne, 3, sampleRows())
	g.driver.Affect("local", 2)

	pending := g.svc.RunQuery(context.Background(), "local", updateOne, guard.OriginHuman)
	got := g.svc.ConfirmPending(context.Background(), pending.PendingID)

	if got.Status != service.QueryExecuted {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.QueryExecuted, got.Message)
	}
	if got.AffectedRows != 2 {
		t.Errorf("AffectedRows = %d, want the 2 the database reported", got.AffectedRows)
	}
	if !strings.Contains(got.Message, "2") {
		t.Errorf("message = %q, want it to say how many rows changed", got.Message)
	}

	// The statement runs exactly as the human wrote it, and exactly once.
	if execs := g.driver.Execs("local"); !equalStrings(execs, []string{updateOne}) {
		t.Fatalf("the database executed %v, want the original statement once", execs)
	}

	record := g.onlyRecord(t)
	for field, want := range map[string]any{
		"origin":         "human",
		"profile":        "local",
		"sql":            updateOne,
		"decision":       "confirmed",
		"decider":        "human",
		"advisory_count": float64(3),
		"affected_rows":  float64(2),
	} {
		if record[field] != want {
			t.Errorf("audit %s = %v, want %v", field, record[field], want)
		}
	}

	// The timestamps come from the injected clock, in the order the events
	// happened: the request first, then the decision, then the execution.
	requested, decided, executed := record["requested_at"], record["decided_at"], record["executed_at"]
	if requested != gateStart.Format(time.RFC3339Nano) {
		t.Errorf("requested_at = %v, want the moment the preview was made (%s)", requested, gateStart.Format(time.RFC3339Nano))
	}
	for _, pair := range [][2]any{{requested, decided}, {decided, executed}} {
		first, second := pair[0].(string), pair[1].(string)
		if !(first < second) {
			t.Errorf("audit timestamps out of order: %s then %s", first, second)
		}
	}
}

// TestCancelPendingRunsNothing: the other decision. It is still a decision, so
// it is still recorded — a cancelled DROP TABLE is exactly the kind of thing
// someone wants to find in the log later.
func TestCancelPendingRunsNothing(t *testing.T) {
	g := newGate(t)
	g.scriptPreview(t, updateOne, 3, sampleRows())

	pending := g.svc.RunQuery(context.Background(), "local", updateOne, guard.OriginHuman)
	got := g.svc.CancelPending(pending.PendingID)

	if got.Status != service.QueryCancelled {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.QueryCancelled, got.Message)
	}
	if execs := g.driver.Execs("local"); len(execs) != 0 {
		t.Fatalf("a cancelled mutation executed %v", execs)
	}

	record := g.onlyRecord(t)
	if record["decision"] != "cancelled" {
		t.Errorf("audit decision = %v, want \"cancelled\"", record["decision"])
	}
	if record["advisory_count"] != float64(3) {
		t.Errorf("audit advisory_count = %v, want the 3 the human was shown", record["advisory_count"])
	}
	for _, absent := range []string{"affected_rows", "executed_at"} {
		if _, present := record[absent]; present {
			t.Errorf("a cancelled mutation recorded %s = %v", absent, record[absent])
		}
	}

	// The confirmation is gone: cancelling is final.
	if after := g.svc.ConfirmPending(context.Background(), pending.PendingID); after.Status != service.QueryUnknownPending {
		t.Errorf("confirming a cancelled mutation = %q, want %q", after.Status, service.QueryUnknownPending)
	}
}

// TestConfirmPendingTwiceRunsOnce is the claim that one keypress is one
// execution. A confirmation the UI sends twice — a double keypress, a retried
// call — must not run the statement twice.
func TestConfirmPendingTwiceRunsOnce(t *testing.T) {
	g := newGate(t)
	g.scriptPreview(t, updateOne, 3, sampleRows())
	g.driver.Affect("local", 2)

	pending := g.svc.RunQuery(context.Background(), "local", updateOne, guard.OriginHuman)
	if first := g.svc.ConfirmPending(context.Background(), pending.PendingID); first.Status != service.QueryExecuted {
		t.Fatalf("first confirmation = %q (%s)", first.Status, first.Message)
	}

	second := g.svc.ConfirmPending(context.Background(), pending.PendingID)
	if second.Status != service.QueryUnknownPending {
		t.Errorf("second confirmation = %q, want %q", second.Status, service.QueryUnknownPending)
	}
	if execs := g.driver.Execs("local"); len(execs) != 1 {
		t.Errorf("the database executed %v, want the statement exactly once", execs)
	}
	if records := g.records(t); len(records) != 1 {
		t.Errorf("the audit log holds %d records, want 1: a confirmation that ran nothing is not a decision", len(records))
	}
}

func TestConfirmPendingUnknownID(t *testing.T) {
	g := newGate(t)

	got := g.svc.ConfirmPending(context.Background(), "never-issued")

	if got.Status != service.QueryUnknownPending {
		t.Fatalf("status = %q, want %q", got.Status, service.QueryUnknownPending)
	}
	if got.Message == "" {
		t.Error("no message explaining that there is nothing to confirm")
	}
	if execs := g.driver.Execs("local"); len(execs) != 0 {
		t.Errorf("an unknown ID executed %v", execs)
	}
	if len(g.records(t)) != 0 {
		t.Error("an unknown ID was written to the audit log")
	}
}

func TestCancelPendingUnknownID(t *testing.T) {
	g := newGate(t)

	if got := g.svc.CancelPending("never-issued"); got.Status != service.QueryUnknownPending {
		t.Errorf("status = %q, want %q", got.Status, service.QueryUnknownPending)
	}
}

// TestRunQueryWithoutAPreview: DDL cannot be rewritten into reads, and says so
// rather than showing a count of nothing. It is still confirmable — no preview
// is not a refusal to run.
func TestRunQueryWithoutAPreview(t *testing.T) {
	const ddl = "CREATE TABLE audit_probe (id INT PRIMARY KEY)"
	g := newGate(t)
	g.driver.Affect("local", 0)

	pending := g.svc.RunQuery(context.Background(), "local", ddl, guard.OriginHuman)

	if pending.Status != service.QueryRequiresConfirmation {
		t.Fatalf("status = %q, want %q (message: %s)", pending.Status, service.QueryRequiresConfirmation, pending.Message)
	}
	if pending.Preview == nil {
		t.Fatal("no Impact Preview on the result; \"there is none\" still has to be said")
	}
	if pending.Preview.Available {
		t.Fatalf("preview = %+v, want it to report that there is none", pending.Preview)
	}
	if !strings.Contains(pending.Preview.Reason, "CREATE TABLE") {
		t.Errorf("no-preview reason = %q, want it to name the statement", pending.Preview.Reason)
	}
	if len(g.driver.Queries("local")) != 0 {
		t.Errorf("the database was asked %v to preview a statement that has no preview", g.driver.Queries("local"))
	}

	got := g.svc.ConfirmPending(context.Background(), pending.PendingID)
	if got.Status != service.QueryExecuted {
		t.Fatalf("confirming DDL = %q, want %q (message: %s)", got.Status, service.QueryExecuted, got.Message)
	}
	if execs := g.driver.Execs("local"); !equalStrings(execs, []string{ddl}) {
		t.Fatalf("the database executed %v, want the DDL once", execs)
	}

	record := g.onlyRecord(t)
	if _, present := record["advisory_count"]; present {
		t.Errorf("a statement with no preview recorded an advisory count of %v", record["advisory_count"])
	}
	reason, _ := record["no_preview_reason"].(string)
	if !strings.Contains(reason, "CREATE TABLE") {
		t.Errorf("audit no_preview_reason = %q, want the reason there was no preview", reason)
	}
	if record["affected_rows"] != float64(0) {
		t.Errorf("audit affected_rows = %v, want the 0 rows DDL changes", record["affected_rows"])
	}
}

// TestPreviewDegradesWhenTheDatabaseRefusesIt: the preview is advisory, so a
// preview that will not run is a missing preview, not a failed query. The
// human still gets to decide — with the database's own words about why they
// are deciding blind.
func TestPreviewDegradesWhenTheDatabaseRefusesIt(t *testing.T) {
	g := newGate(t)
	g.driver.FailQuery("local", errors.New("db: Unknown column 'nope' in 'where clause'"))
	g.driver.Affect("local", 1)

	const sql = "DELETE FROM users WHERE nope = 1"
	got := g.svc.RunQuery(context.Background(), "local", sql, guard.OriginHuman)

	if got.Status != service.QueryRequiresConfirmation {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.QueryRequiresConfirmation, got.Message)
	}
	if got.Preview == nil || got.Preview.Available {
		t.Fatalf("preview = %+v, want it to report that there is none", got.Preview)
	}
	if !strings.Contains(got.Preview.Reason, "Unknown column") {
		t.Errorf("no-preview reason = %q, want the database's own wording", got.Preview.Reason)
	}
	if execs := g.driver.Execs("local"); len(execs) != 0 {
		t.Errorf("a failed preview executed %v", execs)
	}

	if confirmed := g.svc.ConfirmPending(context.Background(), got.PendingID); confirmed.Status != service.QueryExecuted {
		t.Errorf("confirming after a failed preview = %q (%s)", confirmed.Status, confirmed.Message)
	}
}

// TestConfirmPendingReportsAnExecutionFailure: the decision was made and is
// recorded, and the failure is recorded with it. A mutation that was approved
// and then failed is exactly what someone reads the log to find.
func TestConfirmPendingReportsAnExecutionFailure(t *testing.T) {
	g := newGate(t)
	g.scriptPreview(t, updateOne, 3, sampleRows())
	g.driver.FailExec("local", errors.New("db: Duplicate entry '1' for key 'PRIMARY'"))

	pending := g.svc.RunQuery(context.Background(), "local", updateOne, guard.OriginHuman)
	got := g.svc.ConfirmPending(context.Background(), pending.PendingID)

	if got.Status != service.QueryFailed {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.QueryFailed, got.Message)
	}
	if !strings.Contains(got.Message, "Duplicate entry") {
		t.Errorf("message = %q, want the database's own wording", got.Message)
	}

	record := g.onlyRecord(t)
	if record["decision"] != "confirmed" {
		t.Errorf("audit decision = %v, want \"confirmed\": the human did confirm it", record["decision"])
	}
	if failure, _ := record["error"].(string); !strings.Contains(failure, "Duplicate entry") {
		t.Errorf("audit error = %q, want what the database said", failure)
	}
	if _, present := record["affected_rows"]; present {
		t.Errorf("a failed statement recorded affected_rows = %v", record["affected_rows"])
	}
}

// TestBackstopReroutesTheHumanIntoConfirmation: a statement the classifier
// called a read and the database refused as a write takes the same route as a
// mutation the classifier caught — Inline Confirm, not a bare refusal.
func TestBackstopReroutesTheHumanIntoConfirmation(t *testing.T) {
	g := newGate(t)
	g.driver.RejectWrites("local")
	g.driver.Affect("local", 1)

	got := g.svc.RunQuery(context.Background(), "local", "SELECT record_visit(1)", guard.OriginHuman)

	if got.Status != service.QueryRequiresConfirmation {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.QueryRequiresConfirmation, got.Message)
	}
	if got.Classification != guard.Backstopped() {
		t.Errorf("classification = %+v, want %+v", got.Classification, guard.Backstopped())
	}
	if got.PendingID == "" {
		t.Fatal("a rerouted query has nothing to confirm")
	}
	if got.Preview == nil || got.Preview.Available {
		t.Errorf("preview = %+v, want no preview for a statement nobody can rewrite", got.Preview)
	}

	confirmed := g.svc.ConfirmPending(context.Background(), got.PendingID)
	if confirmed.Status != service.QueryExecuted {
		t.Fatalf("confirming a rerouted query = %q (%s)", confirmed.Status, confirmed.Message)
	}
	if execs := g.driver.Execs("local"); !equalStrings(execs, []string{"SELECT record_visit(1)"}) {
		t.Errorf("the database executed %v, want the statement as written", execs)
	}
	if record := g.onlyRecord(t); record["decision"] != "confirmed" {
		t.Errorf("audit decision = %v, want \"confirmed\"", record["decision"])
	}
}

// TestAIMutationsWaitForTheApprovalConsole pins the Origin split. Inline
// Confirm is the human's policy; an AI-originated mutation is withheld for the
// Approval Console, which is a later slice — and until it exists, nothing
// previews or queues on its behalf.
func TestAIMutationsWaitForTheApprovalConsole(t *testing.T) {
	g := newGate(t)

	got := g.svc.RunQuery(context.Background(), "local", updateOne, guard.OriginAI)

	if got.Status != service.QueryRequiresApproval {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.QueryRequiresApproval, got.Message)
	}
	if got.PendingID != "" {
		t.Errorf("an AI mutation was queued as %q; the Approval Console is a later slice", got.PendingID)
	}
	if execs := g.driver.Execs("local"); len(execs) != 0 {
		t.Errorf("an AI mutation executed %v", execs)
	}
}

// TestReadsAreUntouchedByTheGate: nothing about the write path may reach the
// read path. A SELECT still runs, and leaves nothing behind.
func TestReadsAreUntouchedByTheGate(t *testing.T) {
	g := newGate(t)
	g.driver.Answer("local", db.ResultSet{Columns: []string{"id"}, Rows: [][]*string{{str("1")}}})

	got := g.svc.RunQuery(context.Background(), "local", "SELECT id FROM users", guard.OriginHuman)

	if got.Status != service.QueryOK {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.QueryOK, got.Message)
	}
	if got.PendingID != "" || got.Preview != nil {
		t.Errorf("a read produced a confirmation: id=%q preview=%+v", got.PendingID, got.Preview)
	}
	if len(g.records(t)) != 0 {
		t.Error("a read was written to the audit log")
	}
	if execs := g.driver.Execs("local"); len(execs) != 0 {
		t.Errorf("a read reached Exec: %v", execs)
	}
}

// TestRunQueryMutationOnADisconnectedProfile: there is nowhere to preview it
// and nowhere to run it, so it is reported as what it is rather than queued
// against a connection that does not exist.
func TestRunQueryMutationOnADisconnectedProfile(t *testing.T) {
	g := newGate(t)
	if err := g.svc.Disconnect("local"); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	got := g.svc.RunQuery(context.Background(), "local", updateOne, guard.OriginHuman)

	if got.Status != service.QueryNotConnected {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.QueryNotConnected, got.Message)
	}
	if got.PendingID != "" {
		t.Error("a mutation was queued against a Profile with no connection")
	}
}

// TestConfirmPendingAfterTheProfileIsDisconnected: the human decided, so the
// decision is recorded — with the reason nothing ran.
func TestConfirmPendingAfterTheProfileIsDisconnected(t *testing.T) {
	g := newGate(t)
	g.scriptPreview(t, updateOne, 3, sampleRows())

	pending := g.svc.RunQuery(context.Background(), "local", updateOne, guard.OriginHuman)
	if err := g.svc.Disconnect("local"); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	got := g.svc.ConfirmPending(context.Background(), pending.PendingID)
	if got.Status != service.QueryNotConnected {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.QueryNotConnected, got.Message)
	}

	record := g.onlyRecord(t)
	if record["decision"] != "confirmed" {
		t.Errorf("audit decision = %v, want \"confirmed\"", record["decision"])
	}
	if _, present := record["executed_at"]; present {
		t.Error("a statement that never ran recorded an execution time")
	}
	if failure, _ := record["error"].(string); failure == "" {
		t.Error("audit error is empty; the record does not say why nothing ran")
	}
}

// TestInsertValuesIsCountedWithoutAskingTheDatabase: the rows are in the
// statement, so the preview needs no query at all — and there is no sample,
// which is a preview that is available and simply has none.
func TestInsertValuesIsCountedWithoutAskingTheDatabase(t *testing.T) {
	g := newGate(t)
	g.driver.Affect("local", 2)

	const insert = "INSERT INTO users (id, name) VALUES (1,'a'), (2,'b')"
	got := g.svc.RunQuery(context.Background(), "local", insert, guard.OriginHuman)

	if got.Status != service.QueryRequiresConfirmation {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, service.QueryRequiresConfirmation, got.Message)
	}
	if got.Preview == nil || !got.Preview.Available {
		t.Fatalf("preview = %+v, want an available preview", got.Preview)
	}
	if got.Preview.Count != 2 {
		t.Errorf("advisory count = %d, want the 2 tuples in the statement", got.Preview.Count)
	}
	if got.Preview.HasSample() {
		t.Errorf("an INSERT ... VALUES offered a sample: %v", got.Preview.Rows)
	}
	if queries := g.driver.Queries("local"); len(queries) != 0 {
		t.Errorf("the database was asked %v to count rows the statement already lists", queries)
	}
}

// TestPendingConfirmationsAreIndependent: two confirmations can be open at
// once — one Profile, two statements, or the editor and a second window — and
// deciding one must not touch the other.
func TestPendingConfirmationsAreIndependent(t *testing.T) {
	g := newGate(t)
	const other = "DELETE FROM users WHERE id = 2"
	g.scriptPreview(t, updateOne, 3, sampleRows())
	g.scriptPreview(t, other, 1, sampleRows())
	g.driver.Affect("local", 1)

	first := g.svc.RunQuery(context.Background(), "local", updateOne, guard.OriginHuman)
	second := g.svc.RunQuery(context.Background(), "local", other, guard.OriginHuman)
	if first.PendingID == second.PendingID {
		t.Fatal("two confirmations share one ID")
	}
	if first.Preview.Count != 3 || second.Preview.Count != 1 {
		t.Errorf("previews crossed: %d and %d, want 3 and 1", first.Preview.Count, second.Preview.Count)
	}

	if got := g.svc.CancelPending(first.PendingID); got.Status != service.QueryCancelled {
		t.Fatalf("cancelling the first = %q", got.Status)
	}
	if got := g.svc.ConfirmPending(context.Background(), second.PendingID); got.Status != service.QueryExecuted {
		t.Fatalf("confirming the second = %q (%s)", got.Status, got.Message)
	}
	if execs := g.driver.Execs("local"); !equalStrings(execs, []string{other}) {
		t.Errorf("the database executed %v, want only the confirmed statement", execs)
	}
	if records := g.records(t); len(records) != 2 {
		t.Fatalf("the audit log holds %d records, want one per decision", len(records))
	}
}
