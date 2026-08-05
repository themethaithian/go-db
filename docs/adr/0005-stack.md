# 0005-stack

## Status
Accepted

## Context
The application must be lightweight, beautiful, performant, and maintainable across macOS, Windows, and Linux.
Frontend and backend need distinct technology choices that fit their constraints.
Build and deployment tooling must be reliable and well-supported.

## Decision
**Backend**: Go 1.26+
  - Single-threaded goroutine-per-connection model fits database workloads well.
  - Statically typed, compiles to a single binary with no runtime dependencies.
  - TiDB parser and other libraries have good MySQL support.

**Frontend**: Svelte 5 + TypeScript + Tailwind v4 + Vite v6
  - Svelte compiles to efficient code with minimal bundle size.
  - TypeScript for safety.
  - Tailwind v4 for fast, consistent styling with design tokens.
  - Vite v6 pinned (dev environment has Node 20.11; Vite 7 requires Node ≥ 20.19).

**Desktop shell**: Wails v2 (Mac-first, structure ready for Win/Linux CI later)
  - Go backend talks to Svelte frontend over a local HTTP bridge.
  - Native window and menu support without Electron complexity.

**SQL editor**: CodeMirror 6 (language mode for SQL, syntax highlighting, line wrapping).

**Database first**: MySQL (Postgres deferred to v2—reverses original Postgres-first plan).

**One binary**: The app and MCP proxy are both in one binary; the `mcp` subcommand selects the mode.

**Architecture**: Hexagonal + Deep Modules
  - All domain logic in `internal/`, thin adapters in `app/` (Wails) and `internal/mcp/`.
  - Ports are Go interfaces declared by the domain package that needs them.
  - Adapters implement those interfaces.
  - No circular dependencies; dependencies point inward.

**Design**: Dark-mode-first with design tokens defined at the first commit. Light mode is a CSS override.

## Alternatives rejected

**(a) Electron shell**
  150+ MB footprint contradicts the < 150 MB idle-RAM budget. Wails chosen instead.

**(b) Postgres-first**
  Original plan reversed. MySQL chosen for v1 to narrow scope; Postgres follows in v2.

## Consequences
- Single binary is easy to distribute and install.
- Go backend means small footprint and fast startup (< 2 s).
- Svelte keeps the frontend code concise and the bundle small.
- Wails avoids Electron's 150+ MB footprint.
- Pinned Vite v6 is stable but Node version is a constraint for development.
- MySQL-first simplifies v1 and leaves room for Postgres adaptation in v2.
- Hexagonal + Deep Modules makes testing and port swaps straightforward.
