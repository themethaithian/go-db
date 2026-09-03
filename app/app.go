// Package app is the Wails shell: a thin adapter that wires the embedded
// frontend into a native window. It owns no domain logic — that lives under
// internal/.
package app

import (
	"context"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/themethaithian/go-db/internal/db"
	"github.com/themethaithian/go-db/internal/guard"
	"github.com/themethaithian/go-db/internal/service"
)

//go:embed all:frontend/dist
var assets embed.FS

// Assets returns the embedded frontend build output.
func Assets() embed.FS {
	return assets
}

// App holds the Wails runtime context and the App Service facade. It exposes
// bound methods that delegate 1:1 to the facade — Wails marshals the
// domain types (db.Profile) to TS models for the frontend. It owns no logic
// of its own.
type App struct {
	ctx       context.Context
	svc       *service.AppService
	startedAt time.Time
	readyOnce sync.Once
	version   string
}

// New creates a new App instance backed by svc. startedAt is the process's
// launch time (main.go's package-level time.Now()), captured here so Ready
// can report launch-to-usable once the frontend has mounted. version is
// main.go's build-stamped version ("dev" outside a release build); it backs
// both the Version binding and the update check's notion of "current".
func New(svc *service.AppService, startedAt time.Time, version string) *App {
	return &App{svc: svc, startedAt: startedAt, version: version}
}

// Startup is called by Wails when the app starts, before the frontend is
// loaded. It saves the runtime context for later use.
func (a *App) Startup(ctx context.Context) {
	a.ctx = ctx
}

// Shutdown is called by Wails as the app closes. It closes every connection
// the Connection Registry holds so nothing is left dangling.
func (a *App) Shutdown(ctx context.Context) {
	a.svc.Close()
}

// ListProfiles returns every saved Profile, in saved order. The connection
// manager UI uses this both to render the Profile list and to populate the
// edit form, so no separate GetProfile binding is exposed.
func (a *App) ListProfiles() ([]db.Profile, error) {
	return a.svc.ListProfiles()
}

// SaveProfile creates or replaces the Profile saved under profile.Name. A
// non-empty password replaces the Profile's stored secret; an empty one
// leaves the keychain untouched.
func (a *App) SaveProfile(profile db.Profile, password string) error {
	return a.svc.SaveProfile(profile, password)
}

// ReorderProfiles rewrites the saved Profile order to match names exactly —
// the connection manager's drag-to-reorder. names must be a permutation of
// every saved Profile's name; a missing, extra, or duplicated name is
// rejected without changing anything.
func (a *App) ReorderProfiles(names []string) error {
	return a.svc.ReorderProfiles(names)
}

// DeleteProfile removes the Profile saved under name along with its
// keychain secret.
func (a *App) DeleteProfile(name string) error {
	return a.svc.DeleteProfile(name)
}

// TestConnection opens a throwaway connection for the named Profile and
// reports what happened, without touching the Connection Registry.
func (a *App) TestConnection(name string) service.ConnectionTest {
	return a.svc.TestConnection(a.ctx, name)
}

// Connect opens a connection for the named Profile and holds it in the
// Connection Registry.
func (a *App) Connect(name string) error {
	return a.svc.Connect(a.ctx, name)
}

// Disconnect closes the connection held for the named Profile.
func (a *App) Disconnect(name string) error {
	return a.svc.Disconnect(name)
}

// ConnectedProfiles returns the names of the Profiles currently connected.
func (a *App) ConnectedProfiles() []string {
	return a.svc.ConnectedProfiles()
}

// ListDatabases returns the schemas the named Profile's connection can see,
// for the DB Explorer's Database tree — the level between a Profile and its
// tables.
func (a *App) ListDatabases(profileName string) service.DatabaseList {
	return a.svc.ListDatabases(a.ctx, profileName)
}

// ListTables returns the tables in one database of the named Profile, for the
// DB Explorer's Database tree. An empty database means the Profile's own
// schema.
func (a *App) ListTables(profileName, database string) service.TableList {
	return a.svc.ListTables(a.ctx, profileName, database)
}

// FindKeys returns the keys of a Redis Profile's index whose names match text,
// searched on the server, for the Explorer's filter box: the tree's own list
// stops at the first thousand keys, and a key beyond them can only be found by
// asking Redis. It is Redis's alone — a MySQL schema comes back whole and is
// filtered where it is shown.
func (a *App) FindKeys(profileName, database, text string) service.TableList {
	return a.svc.FindKeys(a.ctx, profileName, database, text)
}

// ListColumns returns the columns of table in one database of the named
// Profile, for a table expanded in the DB Explorer's Database tree.
func (a *App) ListColumns(profileName, database, table string) service.ColumnList {
	return a.svc.ListColumns(a.ctx, profileName, database, table)
}

// ListIndexes returns the indexes on table in one database of the named
// Profile, for the Explorer's Structure view of a table.
func (a *App) ListIndexes(profileName, database, table string) service.IndexList {
	return a.svc.ListIndexes(a.ctx, profileName, database, table)
}

// Classify reports whether statement is provably read-only on engine, without
// connecting to anything or running it. The editor calls it as the human
// types, to drive the read/mutation badge, and passes the Engine of the
// Profile it is pointed at — the badge has to read the text in the language it
// is written in (ADR-0006).
//
// The Engine is the caller's here and the saved Profile's in RunQuery. Nothing
// runs on the strength of this answer, so the editor naming the Engine it is
// showing costs nothing and saves a Profile lookup per keystroke; what
// executes is judged by the Profile's own Engine, which no caller can choose.
//
// It crosses as a plain string and is converted here. Wails generates a TS
// type for a bound struct but not for a named string, so binding db.Engine
// itself would emit a reference to a type the models file never declares —
// widening the wire type is the adapter's job, and an Engine that means
// nothing classifies as a mutation like everything else unproven.
func (a *App) Classify(engine, statement string) guard.Classification {
	return a.svc.Classify(db.Engine(engine), statement)
}

// SplitStatements returns the extent of every statement in buffer, as engine's
// language separates them. The editor calls it to find the statement the
// cursor is in, so Run submits that one statement rather than the whole
// buffer. The offsets are into UTF-8 bytes.
func (a *App) SplitStatements(engine, buffer string) []guard.StatementSpan {
	return a.svc.SplitStatements(db.Engine(engine), buffer)
}

// PageWindow reports the window of rows statement asks for on engine: whether
// it can be paged at all, and which rows the result on screen is. The editor
// calls it with the statement it just ran, to decide whether to offer a next
// page and where that page begins.
//
// A statement with no LIMIT of its own was cut short at the driver's cap, and
// the window says so — the size it reports is the number of rows the human is
// looking at, not an unbounded one.
//
// The Engine crosses as a plain string for the reason Classify's does.
func (a *App) PageWindow(engine, statement string) guard.Window {
	return a.svc.PageWindow(db.Engine(engine), statement)
}

// Repage returns statement rewritten to ask for size rows starting after
// offset, for the editor's page buttons. It runs nothing: what comes back is
// SQL the editor submits through RunQuery like any statement the human typed,
// so the Approval Gate and the audit log see the text that really executes.
func (a *App) Repage(engine, statement string, size, offset int64) guard.Window {
	return a.svc.Repage(db.Engine(engine), statement, size, offset)
}

// RunQuery submits sql on the named Profile and database from the SQL editor.
// The editor is the human Origin — an AI query arrives only through the MCP
// server, which is not wired in yet — so this binding hardcodes
// guard.OriginHuman rather than accepting it as a parameter.
//
// An empty database is the Profile's own connection, which is what the editor
// sends until a human picks a database and what the Explorer always sends. A
// named one runs the statement on a connection whose default schema it is, so
// unqualified SQL means what the human picking it expects.
func (a *App) RunQuery(profileName, database, sql string) service.QueryResult {
	return a.svc.RunQuery(a.ctx, profileName, database, sql, guard.OriginHuman)
}

// ConfirmPending runs the mutation withheld under id — the Inline Confirm's
// one extra keypress.
func (a *App) ConfirmPending(id string) service.QueryResult {
	return a.svc.ConfirmPending(a.ctx, id)
}

// CancelPending discards the mutation withheld under id without running it.
func (a *App) CancelPending(id string) service.QueryResult {
	return a.svc.CancelPending(id)
}

// ListPendingApprovals returns every AI-originated mutation currently
// waiting in the Approval Console, oldest first. The console polls this to
// stay live.
func (a *App) ListPendingApprovals() []guard.Waiting {
	return a.svc.ListPendingApprovals()
}

// ApprovePending lets the Approval Console entry waiting under id run. The
// ack is not the query's result — that goes back to the AI Origin still
// blocked on it.
func (a *App) ApprovePending(id string) service.ApprovalAck {
	return a.svc.ApprovePending(id)
}

// RejectPending refuses the Approval Console entry waiting under id.
func (a *App) RejectPending(id string) service.ApprovalAck {
	return a.svc.RejectPending(id)
}

// MemoryUsageMB reports the app process's current memory footprint in
// megabytes. The status bar polls this to keep the performance budget
// (CLAUDE.md: idle RAM < 150 MB) continuously visible and self-checking.
//
// What's measured: the app process's own physical footprint — on darwin,
// the same number Activity Monitor's Memory column shows, via mach
// task_info(TASK_VM_INFO). WKWebView helper processes the OS launches to
// render the frontend are separate processes with their own footprint and
// are NOT included here; README's "Measured" section accounts for the
// whole picture by measuring them separately. If the platform call is
// unavailable or fails, this falls back to runtime.MemStats.Sys — memory
// obtained from the OS for the Go heap and stacks — which understates the
// true footprint but keeps the gauge alive rather than blank.
func (a *App) MemoryUsageMB() float64 {
	if bytes, ok := memoryUsageBytes(); ok {
		return float64(bytes) / (1024 * 1024)
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return float64(m.Sys) / (1024 * 1024)
}

// ExecutablePath returns the absolute path to the running app's own
// executable, with symlinks resolved. It backs the Connections view's MCP
// setup popover (issue #29): both the `claude mcp add` command and the
// Claude Desktop JSON snippet need the literal path to this binary, and
// only the running process knows what that is — a build-time constant would
// go stale the moment the .app bundle moved. This is a fact about how the
// process was launched, not domain logic, so it lives here in app/ rather
// than internal/.
func (a *App) ExecutablePath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("app: resolving executable path: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		return "", fmt.Errorf("app: resolving executable symlinks: %w", err)
	}
	return resolved, nil
}

// Version returns the running build's version — the git tag it was built
// from, or "dev" for a local/source build. The status bar's version chip
// reads this; CheckForUpdate uses the same value as its "current" side of
// the comparison.
func (a *App) Version() string {
	return a.version
}

// Ready is called once by the frontend on its first mount. It logs
// launch-to-usable — process start to first frontend paint — to stderr so
// the number stays observable on every run, proving the CLAUDE.md launch
// budget (< 2 s) rather than just asserting it. Kept permanently: it costs
// nothing at steady state and only ever fires once per process, guarded by
// readyOnce in case the frontend calls it more than once (e.g. a reload).
func (a *App) Ready() {
	a.readyOnce.Do(func() {
		elapsed := time.Since(a.startedAt)
		fmt.Fprintf(os.Stderr, "go-db: usable in %dms\n", elapsed.Milliseconds())
	})
}
