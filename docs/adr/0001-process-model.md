# 0001-process-model

## Status
Accepted

## Context
The application must support both human users (via a SQL editor) and AI agents (via MCP protocol) querying databases.
Both Origins need to follow the same safety policy through the Approval Gate, but with different interaction models.
The MCP server implementation needs to be deployable and testable independently while maintaining strong security guarantees.

## Decision
The app owns everything: all Profiles, the Connection Registry, SSH tunnels, the Approval Gate, and the audit log.
The MCP server is a pure stdio↔localhost-API proxy running as a subcommand (`mcp`) in the same binary.
The MCP server owns no connections, no logic, and no state—it is a thin adapter.

Authentication uses per-launch random tokens written to 0600 files.
The app's localhost API binds to 127.0.0.1 only, preventing access from other users and browsers (defeating CSRF/DNS-rebinding).

## Alternatives rejected

**(a) MCP server owns its own connections and credentials**
  Would duplicate Profile storage and the Approval Gate, creating a bypass path that contradicts the single-gate model. Rejected.

**(b) Bind the local API beyond 127.0.0.1 or skip the token**
  Exposes the database to other local processes and enables CSRF/DNS-rebinding attacks from browsers. Rejected.

**(c) Unix-socket transport instead of localhost + token**
  Simpler in principle, but token file suffices for v1. Deferred.

## Consequences
- MCP mode requires the app to already be running. The proxy cannot start independently.
- Mutations from MCP block (with ~2-minute timeout auto-reject) while approval is pending in the Approval Console.
- The same Approval Gate pipeline and policies apply to both editor and MCP Origins.
- One binary, one deployment unit, one connection pool, one audit log.
- Simplifies credential management and audit trail correlation.
