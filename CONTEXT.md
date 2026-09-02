# go-db

A lightweight desktop DB client where every mutating query — human- or AI-originated — flows through one guard pipeline, with policy applied per Origin.

## Language

**Origin**:
The source that submitted a query: the human via the SQL editor, or an AI agent via the MCP server. Every query request carries its Origin.
_Avoid_: source, requester, caller

**Approval Gate**:
The single pipeline that classifies every query and applies the Origin's policy to mutating ones. There is exactly one gate; the editor and the MCP server are both clients of it.
_Avoid_: guard rail, safety check, MCP gate

**Inline Confirm**:
The gate's policy for human-originated mutating queries: a confirmation shown in place in the editor, carrying the Impact Preview. One extra keypress to proceed; never queued.
_Avoid_: safe mode, confirmation dialog

**Approval Console**:
The gate's policy for AI-originated mutating queries: the query waits in a visible queue until the human approves or rejects it, or it times out (auto-reject).
_Avoid_: approval queue (as a UI term), pending list

**Profile**:
A saved, named description of how to reach one database: host, credentials reference, an optional SSH tunnel, and optional TLS. Profiles are the only way any Origin names a database — the MCP server pins one explicitly; the editor selects one.
_Avoid_: connection (for the saved config), datasource

**Connection Registry**:
The set of currently open database connections, keyed by Profile. Multiple connections can be open at once (e.g. the editor on one Profile while the MCP server uses another).
_Avoid_: active connection (implies only one), connection pool (that's per-connection)

**Impact Preview**:
An advisory estimate of what a mutating query would do — affected-row count and a sample of affected rows — computed without executing the write. Some statements (e.g. DDL) have no preview and say so explicitly. Shown in both Inline Confirm and the Approval Console; the actual outcome is recorded alongside it after execution.
_Avoid_: dry run, preview rows

**Engine**:
The kind of database a Profile reaches — MySQL, Redis, or MongoDB. The Engine determines the statement language the editor accepts, the classifier that judges it, and the shape of results; everything else (the Approval Gate, both Origin policies, the Connection Registry, the audit log) is Engine-agnostic.
_Avoid_: database type, dialect, driver (for the kind)
