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
// ID. It can be taken exactly once, which is what makes one confirmation
// execute one statement. Policy differs by Origin: a human's mutation raises an
// Inline Confirm and a queue entry; an AI's waits in the Approval Console.
//
// Every decision is written through the AuditLog port as a Record, with the
// preview's advisory count beside the affected-row count the database really
// reported. NewJSONLAuditLog is the append-only JSONL adapter (ADR 0004); Clock
// is the port that makes a Record's timestamps testable.
//
// Nothing in this package opens a connection or runs a query. It plans, judges,
// and remembers; the App Service executes.
package guard
