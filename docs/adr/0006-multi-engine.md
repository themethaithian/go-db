# 0006-multi-engine.md

## Status
Accepted (targets v2 — the v1 scope freeze in CLAUDE.md still stands)

## Context
v1 is MySQL-only. The decision is to add Redis and MongoDB in v2, without adding
a second safety model: every statement, on every Engine, must still flow through
the one Approval Gate with policy applied per Origin.

The gate's current machinery is SQL-shaped in three places: the classifier
parses SQL (ADR-0002), the read backstop is a MySQL `READ ONLY` transaction
(ADR-0002), and results are a flat columns-and-rows table. Redis speaks flat
commands and returns typed values; MongoDB speaks operations on collections and
returns nested documents. Neither offers a read-only transaction to back the
classifier up.

## Decision

**Engine is a first-class attribute of a Profile** (`mysql | redis | mongodb`).
The Engine determines three things — the statement language the editor accepts,
the classifier that judges it, and the shape of results. Everything else (the
gate, both Origin policies, the Connection Registry, the audit log, the MCP
server) is Engine-agnostic and does not branch on it.

**The gate stays one pipeline over opaque statement text.** The editor and the
MCP server submit text; the Profile's Engine selects which classifier judges it.
Classifiers remain closed allowlists that fail closed: anything unparseable or
unknown is a Mutation.

**Per-engine classification:**
- *MySQL* — unchanged (ADR-0002: TiDB parser + `READ ONLY` backstop).
- *Redis* — tokenize the command line; classify by a static command table
  checked into the code, seeded from Redis's own `readonly`/`write` command
  flags and pinned by an integration test against `COMMAND INFO`. Unknown or
  unlisted commands are Mutations.
- *MongoDB* — the editor accepts a restricted `db.<collection>.<verb>(<args>)`
  grammar parsed by our own small parser, **not** JavaScript: mongosh's full JS
  is unclassifiable without evaluating it, and evaluating it is the hazard.
  Closed verb allowlist for reads (`find`, `countDocuments`, `aggregate`, …);
  an `aggregate` pipeline containing `$out` or `$merge` is a Mutation.

**The backstop is per-engine, and its absence is accepted.** Redis and MongoDB
have no equivalent of a read-only transaction, so for them the classifier is
the only layer. This is the same posture ADR-0002 already accepts for MySQL
DDL, now covering two whole Engines — which is why both new classifiers must
be closed allowlists whose every entry carries proof, and why nothing may be
added to them without it.

**Results become a tagged union**, not a wider table: `Table` (the existing
ResultSet, unchanged for SQL), `Documents` (a list of JSON documents, for
MongoDB), and `Value` (one typed reply tree, for Redis). The UI selects the
view by tag; the shell around it is Engine-agnostic.

**Impact Preview stays advisory and per-engine.** SQL keeps the SELECT rewrite
(ADR-0003). Redis previews by read-command rewrite where one exists (`DEL k…` →
`EXISTS k…` count). MongoDB previews mutations carrying a filter via
`countDocuments` on that filter. Everything else has no preview and says so —
an already-established outcome, not a new state.

**Drivers:** `go-redis/v9` and the official `mongo-go-driver`, each as one more
adapter behind the existing Driver/Conn ports in `internal/db`.

## Alternatives rejected

**(a) Force all results into the tabular ResultSet.**
Nested documents flattened into cells are lossy and unreadable; Redis type
information disappears. Rejected — the result shape is part of what an Engine
means.

**(b) Accept full mongosh JavaScript in the editor.**
Cannot be classified without evaluation, and evaluation executes the hazard
being classified. Rejected for safety; the restricted grammar covers the
day-to-day operations and fails closed on everything else.

**(c) Require a second, read-only database account as a backstop for
Redis/Mongo.**
A real second layer, but it presumes provisioning the user may not control.
Rejected as a requirement; possible future hardening for Profiles that opt in.

**(d) Per-engine gates or per-engine apps.**
Violates the single-gate model that is the product's reason to exist. Rejected.

## Consequences
- The Conn port's read path returns the tagged Result union instead of
  ResultSet; the MySQL adapter wraps its existing behaviour unchanged.
- The editor gains a per-engine language mode; the results pane gains document
  and value views. The Profile picker, Inline Confirm, the Approval Console,
  and the audit log do not change shape.
- The MCP server's tools keep their shape (statement text in, result out); the
  pinned Profile now also pins the Engine.
- Safety weight shifts: for two of three Engines the classifier carries
  everything. Growth of either allowlist requires the same proof discipline
  ADR-0002 demands for DDL.
