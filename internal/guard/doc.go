// Package guard implements the Approval Gate: the single pipeline that
// classifies every query and applies the Origin's policy to mutating ones.
//
// # Classification
//
// Two layers, because neither is enough alone (ADR 0002). Classify parses the
// statement with the TiDB parser and proves it read-only against a closed
// allowlist; anything else is a mutation. Reads then execute inside a read-only
// transaction, and a write the database refuses there comes back through
// Backstopped as a mutation after all. The layers do not overlap everywhere:
// MySQL commits implicitly before DDL, so for CREATE, DROP, ALTER and TRUNCATE
// the classifier is the only layer there is.
//
// Classify judges one buffer whole, and a buffer holding several statements is
// a mutation on that ground alone. SplitStatements is what lets the editor
// avoid that: it returns each statement's extent in the buffer, taken from the
// parser rather than from a scan for semicolons, so run-at-cursor submits one
// statement and each gets its own verdict. Text it cannot parse becomes one
// span running to the end of the buffer, which Classify then withholds.
//
// # Impact Preview
//
// PlanPreview rewrites a mutation into the reads that describe it — a COUNT and
// a small sample over the statement's own FROM and WHERE (ADR 0003). The
// mutation is never executed to produce its own preview, so nothing holds row
// locks while a human deliberates. The result is a Preview, which either
// carries an advisory count and sample or says outright that there is none and
// why: DDL, multi-table mutations and unparseable input have no preview, and
// that refusal is a first-class answer rather than an empty one.
//
// # Decisions
//
// A withheld mutation waits in a Queue as a Pending, identified by an opaque
// ID, and is decided exactly once — which is what makes one approval execute
// one statement. Policy differs by Origin, and the Queue keeps the two apart.
//
// A human's mutation is Added and Taken: the editor raises an Inline Confirm,
// the editor answers it, and nothing is blocked in between. An AI's is
// Submitted, which hands the submitting goroutine a Waiter it blocks on until a
// human Decides it in the Approval Console, ApprovalTimeout passes and it
// auto-rejects, or its caller gives up. Console lists what is waiting, oldest
// first, with the deadline each entry will decide itself at. Timer is the port
// that deadline is measured with, so a test expires an approval rather than
// waiting one out.
//
// Every decision is written through the AuditLog port as a Record, with the
// preview's advisory count beside the affected-row count the database really
// reported. The Decision says which path it took — confirmed and cancelled in
// the editor, approved, rejected and timeout in the console — and the Decider
// says whether a person was involved at all. NewJSONLAuditLog is the
// append-only JSONL adapter (ADR 0004); Clock is the port that makes a Record's
// timestamps testable.
//
// Nothing in this package opens a connection or runs a query. It plans, judges,
// and remembers; the App Service executes.
package guard
