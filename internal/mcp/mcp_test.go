package mcp_test

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/themethaithian/go-db/internal/api"
	"github.com/themethaithian/go-db/internal/db"
	"github.com/themethaithian/go-db/internal/db/dbtest"
	"github.com/themethaithian/go-db/internal/guard"
	gomcp "github.com/themethaithian/go-db/internal/mcp"
	"github.com/themethaithian/go-db/internal/service"
)

// These tests state the proxy's contract end to end: an in-memory MCP client
// drives the real go-sdk server the same way stdio would, which drives a
// real internal/api.Server backed by a fake driver, exactly as the shipping
// binary wires app <-> localhost API <-> mcp proxy. Which statements the
// Approval Gate withholds and why is internal/guard's and internal/service's
// claim, not this package's — these tests assert only that the gate's own
// verdict crosses the pipe intact and that the pinned Profile is the one it
// travels on.

func str(s string) *string { return &s }

func localProfile(name string) db.Profile {
	return db.Profile{Name: name, Host: "db.internal", Port: 3306, User: "root", Database: "app"}
}

// testApp starts a real internal/api.Server over a fake driver — the same
// construction internal/api's own contract tests use — and reports the
// directory its token file (api.json) lives in, which is exactly what a
// gomcp proxy is pointed at.
type testApp struct {
	t      *testing.T
	dir    string
	svc    *service.AppService
	driver *dbtest.FakeDriver
}

// approvalTimeout is how long a mutation waits in this app's Approval Console
// before it auto-rejects. It is short because these tests play both sides — the
// agent asking and the human answering — and the shipping deadline is two
// minutes.
const approvalTimeout = 300 * time.Millisecond

func newTestApp(t *testing.T) *testApp {
	t.Helper()

	driver := dbtest.NewFakeDriver()
	svc := service.NewWithApproval(
		db.NewProfileStore(t.TempDir(), dbtest.NewFakeKeychain()), driver,
		guard.NewJSONLAuditLog(t.TempDir()), nil, approvalTimeout, nil,
	)

	dir := t.TempDir()
	srv := api.New(svc, dir)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { srv.Close() })

	return &testApp{t: t, dir: dir, svc: svc, driver: driver}
}

// save saves and accepts a Profile without connecting it, for the lazy-
// connect tests that need a Profile the registry has not opened yet.
func (a *testApp) save(name, password string) {
	a.t.Helper()
	a.driver.Accept(name, password)
	if err := a.svc.SaveProfile(localProfile(name), password); err != nil {
		a.t.Fatalf("SaveProfile(%q): %v", name, err)
	}
}

// saveAndConnect is save plus an immediate Connect, for tests that don't
// care about the lazy-connect path.
func (a *testApp) saveAndConnect(name, password string) {
	a.t.Helper()
	a.save(name, password)
	if err := a.svc.Connect(context.Background(), name); err != nil {
		a.t.Fatalf("Connect(%q): %v", name, err)
	}
}

// waitingInConsole polls the app's Approval Console until the in-flight tool
// call's mutation shows up there — the test playing the human who notices it.
func (a *testApp) waitingInConsole(t *testing.T) guard.Waiting {
	t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if waiting := a.svc.ListPendingApprovals(); len(waiting) == 1 {
			return waiting[0]
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("nothing ever reached the Approval Console; the tool call is not waiting where it should be")
	return guard.Waiting{}
}

// connectClient wires an in-memory MCP client to a gomcp server pinned to
// profile and pointed at dir's api.json — the whole pipe the acceptance
// criteria describe, minus stdio itself, which the smoke test covers
// separately.
func connectClient(t *testing.T, dir, profile string) *sdkmcp.ClientSession {
	t.Helper()

	server := gomcp.NewServer(dir, profile)
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test-client", Version: "v0"}, nil)

	clientTransport, serverTransport := sdkmcp.NewInMemoryTransports()
	ctx := context.Background()

	if _, err := server.Connect(ctx, serverTransport, nil); err != nil {
		t.Fatalf("server.Connect: %v", err)
	}
	cs, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("client.Connect: %v", err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func callTool(t *testing.T, cs *sdkmcp.ClientSession, name string, args map[string]any) *sdkmcp.CallToolResult {
	t.Helper()

	res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%q): %v", name, err)
	}
	return res
}

func textContent(t *testing.T, res *sdkmcp.CallToolResult) string {
	t.Helper()

	if len(res.Content) != 1 {
		t.Fatalf("Content = %v, want exactly one block", res.Content)
	}
	tc, ok := res.Content[0].(*sdkmcp.TextContent)
	if !ok {
		t.Fatalf("Content[0] = %T, want *mcp.TextContent", res.Content[0])
	}
	return tc.Text
}

type resultPayload struct {
	Columns   []string    `json:"columns"`
	Rows      [][]*string `json:"rows"`
	Truncated bool        `json:"truncated"`
	RowCount  int         `json:"row_count"`
}

func TestListTablesReturnsScriptedRowsThroughTheWholePipe(t *testing.T) {
	app := newTestApp(t)
	app.saveAndConnect("demo", "s3cret")
	app.driver.AnswerQuery("demo", "SHOW TABLES", db.ResultSet{
		Columns: []string{"Tables_in_app"},
		Rows:    [][]*string{{str("users")}, {str("orders")}},
	})

	cs := connectClient(t, app.dir, "demo")
	res := callTool(t, cs, "list_tables", nil)

	if res.IsError {
		t.Fatalf("list_tables reported an error: %s", textContent(t, res))
	}
	var got resultPayload
	if err := json.Unmarshal([]byte(textContent(t, res)), &got); err != nil {
		t.Fatalf("decoding content: %v", err)
	}
	if got.RowCount != 2 || len(got.Rows) != 2 || *got.Rows[0][0] != "users" || *got.Rows[1][0] != "orders" {
		t.Errorf("payload = %+v, want the scripted rows [users orders]", got)
	}
	if len(got.Columns) != 1 || got.Columns[0] != "Tables_in_app" {
		t.Errorf("columns = %v, want [Tables_in_app]", got.Columns)
	}
}

func TestDescribeTableEscapesTheIdentifier(t *testing.T) {
	app := newTestApp(t)
	app.saveAndConnect("demo", "s3cret")
	app.driver.AnswerQuery("demo", "DESCRIBE `us``er`", db.ResultSet{
		Columns: []string{"Field"},
		Rows:    [][]*string{{str("id")}},
	})

	cs := connectClient(t, app.dir, "demo")
	res := callTool(t, cs, "describe_table", map[string]any{"table": "us`er"})

	if res.IsError {
		t.Fatalf("describe_table reported an error: %s", textContent(t, res))
	}

	// Assert the SQL the fake driver actually saw: the embedded backtick in
	// the table name must have been doubled, not passed through raw.
	queries := app.driver.Queries("demo")
	if len(queries) != 1 || queries[0] != "DESCRIBE `us``er`" {
		t.Errorf("driver saw %v, want exactly [\"DESCRIBE `us``er`\"]", queries)
	}
}

func TestDescribeTableRejectsEmptyName(t *testing.T) {
	app := newTestApp(t)
	app.saveAndConnect("demo", "s3cret")

	cs := connectClient(t, app.dir, "demo")
	res := callTool(t, cs, "describe_table", map[string]any{"table": ""})

	if !res.IsError {
		t.Fatalf("describe_table(\"\") did not report an error")
	}
	if len(app.driver.Queries("demo")) != 0 {
		t.Errorf("driver.Queries = %v, want nothing sent for an empty table name", app.driver.Queries("demo"))
	}
}

func TestRunQueryReadReturnsRows(t *testing.T) {
	app := newTestApp(t)
	app.saveAndConnect("demo", "s3cret")
	app.driver.Answer("demo", db.ResultSet{
		Columns: []string{"id"},
		Rows:    [][]*string{{str("1")}},
	})

	cs := connectClient(t, app.dir, "demo")
	res := callTool(t, cs, "run_query", map[string]any{"sql": "SELECT id FROM users"})

	if res.IsError {
		t.Fatalf("run_query reported an error: %s", textContent(t, res))
	}
	var got resultPayload
	if err := json.Unmarshal([]byte(textContent(t, res)), &got); err != nil {
		t.Fatalf("decoding content: %v", err)
	}
	if got.RowCount != 1 || *got.Rows[0][0] != "1" {
		t.Errorf("payload = %+v, want the scripted row [[1]]", got)
	}
}

// TestRunQueryMutationWaitsForApprovalThenRunsIt is the AI write path across
// the whole pipe: the tool call goes out, stalls in the app's Approval Console
// where a human answers it, and comes back carrying what the mutation really
// did. The wait is the feature — an agent that had been handed a token to poll
// with would simply not come back.
func TestRunQueryMutationWaitsForApprovalThenRunsIt(t *testing.T) {
	app := newTestApp(t)
	app.saveAndConnect("demo", "s3cret")
	app.driver.Affect("demo", 3)

	cs := connectClient(t, app.dir, "demo")

	results := make(chan *sdkmcp.CallToolResult, 1)
	go func() {
		res, err := cs.CallTool(context.Background(), &sdkmcp.CallToolParams{
			Name: "run_query", Arguments: map[string]any{"sql": "DELETE FROM users"},
		})
		if err != nil {
			t.Errorf("CallTool(run_query): %v", err)
		}
		results <- res
	}()

	if ack := app.svc.ApprovePending(app.waitingInConsole(t).ID); !ack.OK {
		t.Fatalf("ApprovePending = %+v", ack)
	}

	var res *sdkmcp.CallToolResult
	select {
	case res = <-results:
	case <-time.After(30 * time.Second):
		t.Fatal("the tool call never returned after the human approved it")
	}

	if res.IsError {
		t.Fatalf("an approved mutation reported a tool error: %s", textContent(t, res))
	}
	var got struct {
		Executed     bool  `json:"executed"`
		AffectedRows int64 `json:"affected_rows"`
	}
	if err := json.Unmarshal([]byte(textContent(t, res)), &got); err != nil {
		t.Fatalf("decoding content: %v", err)
	}
	if !got.Executed || got.AffectedRows != 3 {
		t.Errorf("payload = %+v, want the 3 rows the database reported to have crossed the pipe", got)
	}
	if execs := app.driver.Execs("demo"); len(execs) != 1 || execs[0] != "DELETE FROM users" {
		t.Errorf("driver.Execs = %v, want the statement once, as submitted", execs)
	}
}

// TestRunQueryMutationNobodyApprovesIsNotAToolError: the deadline passed. The
// agent is told so in the gate's own words rather than being handed a failure
// it might retry, and nothing ran.
func TestRunQueryMutationNobodyApprovesIsNotAToolError(t *testing.T) {
	app := newTestApp(t)
	app.saveAndConnect("demo", "s3cret")

	cs := connectClient(t, app.dir, "demo")
	res := callTool(t, cs, "run_query", map[string]any{"sql": "DELETE FROM users"})

	if res.IsError {
		t.Fatalf("an unapproved mutation must not be a tool error, got: %s", textContent(t, res))
	}
	if text := textContent(t, res); text == "" {
		t.Error("content is empty, want the gate's own explanation of why nothing happened")
	}
	if len(app.driver.Execs("demo")) != 0 {
		t.Errorf("driver.Execs = %v, want nothing executed: the mutation must never have run", app.driver.Execs("demo"))
	}
}

func TestEveryQueryCarriesThePinnedProfileOnly(t *testing.T) {
	app := newTestApp(t)
	app.saveAndConnect("demo", "s3cret")
	app.saveAndConnect("other", "s3cret")
	app.driver.AnswerQuery("demo", "SELECT 1", db.ResultSet{Columns: []string{"n"}, Rows: [][]*string{{str("demo-answer")}}})
	app.driver.AnswerQuery("other", "SELECT 1", db.ResultSet{Columns: []string{"n"}, Rows: [][]*string{{str("other-answer")}}})

	// A server pinned to "demo" has no tool argument through which a caller
	// could name "other" — this is the pin, proven by the pipe rather than
	// by reading the schema.
	cs := connectClient(t, app.dir, "demo")
	res := callTool(t, cs, "run_query", map[string]any{"sql": "SELECT 1"})
	if res.IsError {
		t.Fatalf("run_query reported an error: %s", textContent(t, res))
	}
	var got resultPayload
	if err := json.Unmarshal([]byte(textContent(t, res)), &got); err != nil {
		t.Fatalf("decoding content: %v", err)
	}
	if *got.Rows[0][0] != "demo-answer" {
		t.Errorf("rows = %v, want the pinned profile's own answer", got.Rows)
	}
	if len(app.driver.Queries("other")) != 0 {
		t.Errorf("driver.Queries(\"other\") = %v, want nothing: the pinned server never named it", app.driver.Queries("other"))
	}
}

func TestAppNotRunningReturnsReadableError(t *testing.T) {
	// No api.json in this directory at all: the app was never started.
	dir := t.TempDir()

	cs := connectClient(t, dir, "demo")
	res := callTool(t, cs, "list_tables", nil)

	if !res.IsError {
		t.Fatalf("list_tables succeeded against a directory with no api.json")
	}
	got := textContent(t, res)
	want := "go-db is not running — open the go-db app, then try again."
	if got != want {
		t.Errorf("message = %q, want %q", got, want)
	}
}

func TestStaleTokenFilePointingAtADeadPortFailsQuickly(t *testing.T) {
	dir := t.TempDir()

	// A port nothing is listening on: open a listener, note its port, close
	// it immediately so the port is free but answers nothing.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	deadPort := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	data, err := json.Marshal(map[string]any{"port": deadPort, "token": "deadbeef"})
	if err != nil {
		t.Fatalf("marshal token file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "api.json"), data, 0o600); err != nil {
		t.Fatalf("writing stale api.json: %v", err)
	}

	cs := connectClient(t, dir, "demo")

	start := time.Now()
	res := callTool(t, cs, "list_tables", nil)
	elapsed := time.Since(start)

	if !res.IsError {
		t.Fatalf("list_tables succeeded against a dead port")
	}
	want := "go-db is not running — open the go-db app, then try again."
	if got := textContent(t, res); got != want {
		t.Errorf("message = %q, want %q", got, want)
	}
	if elapsed > 3*time.Second {
		t.Errorf("a dead port took %s to fail — want a fast connection-refused, not a hang", elapsed)
	}
}

func TestLazyConnectHappensOnceThenTheQuerySucceeds(t *testing.T) {
	app := newTestApp(t)
	app.save("demo", "s3cret") // saved and reachable, but never connected
	app.driver.Answer("demo", db.ResultSet{
		Columns: []string{"id"},
		Rows:    [][]*string{{str("1")}},
	})

	cs := connectClient(t, app.dir, "demo")
	res := callTool(t, cs, "run_query", map[string]any{"sql": "SELECT id FROM users"})

	if res.IsError {
		t.Fatalf("run_query reported an error: %s", textContent(t, res))
	}
	var got resultPayload
	if err := json.Unmarshal([]byte(textContent(t, res)), &got); err != nil {
		t.Fatalf("decoding content: %v", err)
	}
	if got.RowCount != 1 || *got.Rows[0][0] != "1" {
		t.Errorf("payload = %+v, want the query to have succeeded after one lazy connect", got)
	}
	if opens := app.driver.Opens(); opens != 1 {
		t.Errorf("driver.Opens() = %d, want exactly 1: lazy connect must happen once, not once per retry", opens)
	}
}
