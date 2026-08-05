package guard_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/themethaithian/go-db/internal/guard"
)

// The audit log is the product's memory of every gate decision, so these tests
// go through the real JSONL adapter rather than a fake: the file format is part
// of the contract (ADR 0004), and a fake would only prove the port compiles.

// auditLines reads back what the log wrote, one record per line, failing if any
// line is not a JSON object on its own.
func auditLines(t *testing.T, dir string) []map[string]any {
	t.Helper()

	data, err := os.ReadFile(filepath.Join(dir, guard.AuditFileName))
	if err != nil {
		t.Fatalf("reading the audit log: %v", err)
	}
	text := string(data)
	if text != "" && !strings.HasSuffix(text, "\n") {
		t.Error("the audit log does not end in a newline; the next append would join two records")
	}

	var records []map[string]any
	for i, line := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
		if line == "" {
			t.Fatalf("line %d of the audit log is empty", i+1)
		}
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("line %d is not one JSON object: %v\n%s", i+1, err, line)
		}
		records = append(records, record)
	}
	return records
}

func confirmedRecord() guard.Record {
	advisory, affected := int64(3), int64(2)
	executed := at.Add(2 * time.Second)
	return guard.Record{
		RequestedAt:    at,
		DecidedAt:      at.Add(time.Second),
		ExecutedAt:     &executed,
		Origin:         guard.OriginHuman,
		Profile:        "local",
		SQL:            "UPDATE users SET name = 'x' WHERE id = 1",
		Classification: guard.Classify("UPDATE users SET name = 'x' WHERE id = 1"),
		AdvisoryCount:  &advisory,
		Decision:       guard.Confirmed,
		Decider:        guard.DeciderHuman,
		AffectedRows:   &affected,
	}
}

// TestJSONLAuditLogWritesOneRecordPerLine is the shape of the file, field by
// field. The advisory count and the actual affected rows sit side by side on
// purpose: comparing what was predicted with what happened is the reason the
// record exists (ADR 0003).
func TestJSONLAuditLogWritesOneRecordPerLine(t *testing.T) {
	dir := t.TempDir()
	log := guard.NewJSONLAuditLog(dir)

	if err := log.Append(confirmedRecord()); err != nil {
		t.Fatalf("Append: %v", err)
	}

	records := auditLines(t, dir)
	if len(records) != 1 {
		t.Fatalf("the log holds %d records, want 1", len(records))
	}
	got := records[0]

	for field, want := range map[string]any{
		"requested_at":   "2026-08-05T10:00:00Z",
		"decided_at":     "2026-08-05T10:00:01Z",
		"executed_at":    "2026-08-05T10:00:02Z",
		"origin":         "human",
		"profile":        "local",
		"sql":            "UPDATE users SET name = 'x' WHERE id = 1",
		"decision":       "confirmed",
		"decider":        "human",
		"advisory_count": float64(3),
		"affected_rows":  float64(2),
	} {
		if got[field] != want {
			t.Errorf("%s = %v (%T), want %v", field, got[field], got[field], want)
		}
	}

	classification, ok := got["classification"].(map[string]any)
	if !ok {
		t.Fatalf("classification = %v, want the gate's verdict and its reason", got["classification"])
	}
	if classification["kind"] != "mutation" {
		t.Errorf("classification.kind = %v, want \"mutation\"", classification["kind"])
	}
	if reason, _ := classification["reason"].(string); !strings.Contains(reason, "UPDATE") {
		t.Errorf("classification.reason = %q, want it to name the statement", reason)
	}

	if _, present := got["no_preview_reason"]; present {
		t.Error("a record with an advisory count also carries a no-preview reason")
	}
	if _, present := got["error"]; present {
		t.Error("a successful execution recorded an error")
	}
}

// TestJSONLAuditLogOmitsWhatDidNotHappen: a cancelled mutation never ran, so
// there is no execution time and no affected-row count. Recording a zero would
// be a lie that reads as "it ran and changed nothing".
func TestJSONLAuditLogOmitsWhatDidNotHappen(t *testing.T) {
	dir := t.TempDir()
	log := guard.NewJSONLAuditLog(dir)

	if err := log.Append(guard.Record{
		RequestedAt:     at,
		DecidedAt:       at.Add(time.Second),
		Origin:          guard.OriginHuman,
		Profile:         "local",
		SQL:             "DROP TABLE users",
		Classification:  guard.Classify("DROP TABLE users"),
		NoPreviewReason: "no preview is available for a DROP TABLE statement",
		Decision:        guard.Cancelled,
		Decider:         guard.DeciderHuman,
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got := auditLines(t, dir)[0]
	if got["decision"] != "cancelled" {
		t.Errorf("decision = %v, want \"cancelled\"", got["decision"])
	}
	if got["no_preview_reason"] != "no preview is available for a DROP TABLE statement" {
		t.Errorf("no_preview_reason = %v, want the reason there was no preview", got["no_preview_reason"])
	}
	for _, absent := range []string{"executed_at", "affected_rows", "advisory_count"} {
		if _, present := got[absent]; present {
			t.Errorf("a cancelled mutation recorded %s = %v", absent, got[absent])
		}
	}
}

// TestJSONLAuditLogAppends: the log is append-only, across records and across
// processes. Rewriting it would destroy the only evidence of what was run.
func TestJSONLAuditLogAppends(t *testing.T) {
	dir := t.TempDir()

	if err := guard.NewJSONLAuditLog(dir).Append(confirmedRecord()); err != nil {
		t.Fatalf("first Append: %v", err)
	}
	// A second log over the same directory stands in for the next run of the
	// app; it must extend the file, not replace it.
	second := confirmedRecord()
	second.SQL = "DELETE FROM users WHERE id = 2"
	if err := guard.NewJSONLAuditLog(dir).Append(second); err != nil {
		t.Fatalf("second Append: %v", err)
	}

	records := auditLines(t, dir)
	if len(records) != 2 {
		t.Fatalf("the log holds %d records, want 2", len(records))
	}
	if records[0]["sql"] != "UPDATE users SET name = 'x' WHERE id = 1" {
		t.Errorf("the first record was overwritten: %v", records[0]["sql"])
	}
	if records[1]["sql"] != "DELETE FROM users WHERE id = 2" {
		t.Errorf("the second record = %v, want it appended after the first", records[1]["sql"])
	}
}

// TestJSONLAuditLogCreatesItsDirectory: the app hands the log its config
// directory, which may not exist on a first run.
func TestJSONLAuditLogCreatesItsDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "go-db", "nested")

	if err := guard.NewJSONLAuditLog(dir).Append(confirmedRecord()); err != nil {
		t.Fatalf("Append into a directory that does not exist yet: %v", err)
	}

	if len(auditLines(t, dir)) != 1 {
		t.Error("the record was not written")
	}
}

// TestJSONLAuditLogPermissions: the log holds every statement the human ran,
// which is not other users' business.
func TestJSONLAuditLogPermissions(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "go-db")

	if err := guard.NewJSONLAuditLog(dir).Append(confirmedRecord()); err != nil {
		t.Fatalf("Append: %v", err)
	}

	file, err := os.Stat(filepath.Join(dir, guard.AuditFileName))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := file.Mode().Perm(); perm != 0o600 {
		t.Errorf("audit log mode = %04o, want 0600", perm)
	}
	created, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if perm := created.Mode().Perm(); perm != 0o700 {
		t.Errorf("audit directory mode = %04o, want 0700", perm)
	}
}

// TestJSONLAuditLogIsSafeUnderConcurrency: the editor and the MCP server decide
// independently, and two records interleaved mid-line would corrupt both.
func TestJSONLAuditLogIsSafeUnderConcurrency(t *testing.T) {
	dir := t.TempDir()
	log := guard.NewJSONLAuditLog(dir)

	const writers = 20
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for i := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			record := confirmedRecord()
			record.SQL = strings.Repeat("x", i+1)
			errs <- log.Append(record)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	records := auditLines(t, dir)
	if len(records) != writers {
		t.Fatalf("the log holds %d records, want %d", len(records), writers)
	}
	lengths := make(map[int]bool)
	for _, record := range records {
		lengths[len(record["sql"].(string))] = true
	}
	if len(lengths) != writers {
		t.Errorf("%d distinct records survived, want %d", len(lengths), writers)
	}
}

// TestAuditLogPortIsImplementedByTheAdapter keeps the port and its adapter
// honest: the domain declares AuditLog, and the JSONL writer is one
// implementation of it, not the definition.
func TestAuditLogPortIsImplementedByTheAdapter(t *testing.T) {
	var log guard.AuditLog = guard.NewJSONLAuditLog(t.TempDir())
	if log == nil {
		t.Fatal("NewJSONLAuditLog returned nothing")
	}
}
