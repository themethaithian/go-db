# go-db

A lightweight, beautiful desktop DB client with a human-approval gate for AI. Go + Wails v2 (Mac-first), Svelte 5 + TypeScript + Tailwind v4 frontend, MySQL first.

## Read these first

- `CONTEXT.md` — ubiquitous language. Use these exact terms — Origin, Approval Gate, Inline Confirm, Approval Console, Impact Preview, Profile, Connection Registry — in code, tests, and commits.
- `docs/adr/` — binding architecture decisions. Do not re-litigate them without writing a new ADR.

## Architecture (Hexagonal + Deep Modules)

All domain logic lives in `internal/`; the Wails app and the MCP proxy are thin adapters. Dependencies point inward — `internal/*` never imports Wails, HTTP, or MCP SDK types.

- `internal/db/` — Profile store (TOML + OS keychain), Connection Registry, MySQL driver, SSH tunnel port
- `internal/guard/` — Approval Gate: query classifier (TiDB parser + `READ ONLY` txn backstop), Impact Preview (SELECT rewrite, advisory), approval queue, AuditLog port (JSONL)
- `internal/mcp/` — MCP stdio server; a pure proxy over the app's localhost API (token-file auth). Owns no connections.
- `app/` — Wails shell + Svelte frontend

Deep modules: each package exports a narrow API and hides its machinery. Before exporting a symbol, ask whether an adapter genuinely needs it. Ports are Go interfaces declared by the domain package that needs them; adapters implement them.

## Engineering workflow

- **TDD for all domain logic**: write the failing test first. Unit tests use fakes for ports; driver/integration tests run against real MySQL in Docker (`colima start` first). Scaffold and UI work is verified by build + smoke check instead — no ceremony tests for glue.
- **Agentic engineering**: Fable 5 is the orchestrator — it plans, decomposes, reviews diffs, and never implements large tasks inline. Implementation is delegated to worker subagents: **Opus** for design-heavy core domain, **Sonnet** for standard implementation, **Haiku** for mechanical or documentation tasks. Work strictly one task at a time; a task is done only when its tests pass and its diff is reviewable in one sitting.

## Commands

- `wails dev` — run the app in dev mode
- `wails build` — production build
- `go test ./...` — all Go tests
- `npm run check` (in the frontend dir) — Svelte/TS typecheck

## Hard constraints

- v1 scope shipped: connection manager, single SQL editor, results table (pagination), approval console, MCP server mode. v2 is underway and adds exactly one thing: multi-engine support — Redis and MongoDB per ADR-0006, MySQL behaviour unchanged. **No** ER diagrams, export, Postgres, or PII masking until this is done. Owner-approved exceptions so far (2026-08-20): query tabs, and the editor opening `.sql` files into a tab with Run all — every statement still meets the Approval Gate individually.
- Performance budget is a feature: idle RAM < 150 MB, launch-to-usable < 2 s, live RAM usage shown in the app status bar.
- Passwords never touch disk in plaintext — OS keychain only.
- Node on this machine is 20.11 → Vite stays pinned to v6 until Node ≥ 20.19.
