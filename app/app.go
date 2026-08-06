// Package app is the Wails shell: a thin adapter that wires the embedded
// frontend into a native window. It owns no domain logic — that lives under
// internal/.
package app

import (
	"context"
	"embed"
	"fmt"
	"os"
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
}

// New creates a new App instance backed by svc. startedAt is the process's
// launch time (main.go's package-level time.Now()), captured here so Ready
// can report launch-to-usable once the frontend has mounted.
func New(svc *service.AppService, startedAt time.Time) *App {
	return &App{svc: svc, startedAt: startedAt}
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

// ListProfiles returns every saved Profile, ordered by name. The connection
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

// ListTables returns the tables in the named Profile's schema, for the
// DB Explorer's Database tree.
func (a *App) ListTables(profileName string) service.TableList {
	return a.svc.ListTables(a.ctx, profileName)
}

// ListColumns returns the columns of table in the named Profile's schema,
// for a table expanded in the DB Explorer's Database tree.
func (a *App) ListColumns(profileName, table string) service.ColumnList {
	return a.svc.ListColumns(a.ctx, profileName, table)
}

// ListIndexes returns the indexes on table in the named Profile's schema,
// for the Explorer's Structure view of a table.
func (a *App) ListIndexes(profileName, table string) service.IndexList {
	return a.svc.ListIndexes(a.ctx, profileName, table)
}

// Classify reports whether sql is provably read-only, without connecting to
// anything or running it. The editor calls it as the human types, to drive
// the read/mutation badge.
func (a *App) Classify(sql string) guard.Classification {
	return a.svc.Classify(sql)
}

// RunQuery submits sql on the named Profile from the SQL editor. The editor
// is the human Origin — an AI query arrives only through the MCP server,
// which is not wired in yet — so this binding hardcodes guard.OriginHuman
// rather than accepting it as a parameter.
func (a *App) RunQuery(profileName, sql string) service.QueryResult {
	return a.svc.RunQuery(a.ctx, profileName, sql, guard.OriginHuman)
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
