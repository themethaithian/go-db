# 0003-impact-preview-by-select-rewrite

## Status
Accepted

## Context
Before a human or AI approves a mutation, they need to know what it will do.
Impact Preview should show:
- The affected-row count (how many rows will be modified)
- A sample of affected rows (what they look like)

Computing this preview without executing the mutation is crucial: executing the write holds row locks during deliberation, risking timeouts and contention.
Rejected alternative: execute and rollback. This does not work well in MySQL—DDL statements cause implicit commits and cannot roll back.

## Decision
Impact Preview is advisory and computed by rewriting the mutation into a COUNT + LIMIT-ed sample SELECT.
The mutation is never executed for preview purposes.
Example rewrite: `UPDATE users SET status='inactive' WHERE created < '2020-01-01'` becomes:
1. `SELECT COUNT(*) FROM users WHERE created < '2020-01-01'` to get the count
2. `SELECT * FROM users WHERE created < '2020-01-01' LIMIT 100` to get a sample

For statements that cannot be rewritten (e.g. DDL), explicitly say "no preview available".

The actual affected-row count from the mutation is recorded in the audit log after execution.

## Consequences
- Previews are fast and do not hold locks.
- Users and AI see what they are about to mutate before committing.
- DDL and other non-rewritable statements gracefully decline preview.
- The actual vs. predicted counts can be compared in the audit log for learning and debugging.
