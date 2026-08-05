// Package guard implements the Approval Gate, the single pipeline that classifies every query and applies Origin-specific policy to mutating ones.
//
// The Approval Gate has two layers:
// 1. Query classifier using TiDB parser (pure Go, MySQL-compatible) to classify statements as read-only or mutating.
//    Anything not provably read-only enters the gate.
// 2. Backstop: reads execute inside START TRANSACTION READ ONLY; DB-rejected writes reroute into the gate.
//
// Impact Preview is an advisory estimate of what a mutating query would do—affected-row count and a sample of affected rows.
// It is computed by rewriting the mutation into a COUNT + LIMIT-ed sample SELECT (never executing the write).
// Some statements (e.g. DDL) have no preview and say so explicitly.
//
// For human-originated (editor) mutating queries: Inline Confirm policy shows a confirmation in place in the editor,
// carrying the Impact Preview, with one extra keypress to proceed.
//
// For AI-originated (MCP) mutating queries: Approval Console policy queues the query in a visible list,
// where the human approves or rejects it, or it times out (auto-reject).
//
// The AuditLog port records every query attempt and outcome.
// This package exports a narrow API; internal machinery (classifier, rewriter, queue logic) is hidden.
package guard
