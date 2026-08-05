# 0002-two-layer-query-classification.md

## Status
Accepted

## Context
The Approval Gate must decide whether a query is read-only or mutating.
Misclassification is a safety hazard: a mutation classified as read-only bypasses the gate; a read classified as mutating creates friction for the user.

Static analysis can classify most statements (SELECT, INSERT, UPDATE, DELETE, DDL, etc.), but some corner cases are hard to detect statically.
For example, a function call in a SELECT might perform side effects (writes), or EXPLAIN ANALYZE of a write statement might pass through as a SELECT.

## Decision
Use a two-layer classification strategy:

**Layer 1: Static classifier**
Parse the query using the TiDB parser (pure Go, MySQL-compatible).
Use this to classify most statements as read-only or mutating.
Anything that cannot be proven read-only statically enters the gate.

**Layer 2: Database backstop**
Reads execute inside `START TRANSACTION READ ONLY`.
If the database rejects a write (e.g. mutation function in SELECT, EXPLAIN ANALYZE of a write, writing CTE), reroute the query into the gate for human approval.

## Alternatives rejected

**(a) Regex or keyword classification**
  Trivially fooled by comments, CTEs, and EXPLAIN ANALYZE of writes. Rejected for safety.

**(b) Trust the parser alone with no database-enforced backstop**
  Mutating functions inside SELECT and parser gaps would slip writes through. Violates defense-in-depth. Rejected.

**(c) Classify at a SQL-proxy layer outside the app**
  Adds an always-on network component and contradicts the app-owns-everything model. Rejected.

## Consequences
- Most queries are classified statically and execute quickly.
- A small number of ambiguous queries pay a small overhead (one extra transaction start/rollback).
- No false positives: every mutation is either gated upfront or caught at the DB layer.
- The TiDB parser dependency brings excellent MySQL compatibility and is actively maintained.
