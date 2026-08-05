package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/themethaithian/go-db/internal/api"
	"github.com/themethaithian/go-db/internal/db"
	"github.com/themethaithian/go-db/internal/db/dbtest"
	"github.com/themethaithian/go-db/internal/guard"
	"github.com/themethaithian/go-db/internal/service"
)

// These tests state the pipe's contract: loopback-only, token-gated,
// Origin-hostile, and — once past the gate — a faithful pass-through to the
// facade. Which statements the Approval Gate withholds and why is specified
// in internal/guard and internal/service, not here: the query tests below
// assert one field deep, enough to prove the outcome crossed the wire intact.

func str(s string) *string { return &s }

func localProfile(name string) db.Profile {
	return db.Profile{Name: name, Host: "db.internal", Port: 3306, User: "root", Database: "app"}
}

func mustSave(t *testing.T, svc *service.AppService, profile db.Profile, password string) {
	t.Helper()
	if err := svc.SaveProfile(profile, password); err != nil {
		t.Fatalf("SaveProfile(%q): %v", profile.Name, err)
	}
}

// testServer is a Server started against a fresh facade, plus what a contract
// test needs to talk to it: the driver to script the database, and the live
// token to authenticate requests with.
type testServer struct {
	t      *testing.T
	dir    string
	srv    *api.Server
	svc    *service.AppService
	driver *dbtest.FakeDriver
	addr   string
	token  string
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()
	return newTestServerWithApproval(t, 0)
}

// newTestServerWithApproval is newTestServer with the Approval Console's
// deadline shortened, for the one test that lets a request time out rather than
// answering it.
func newTestServerWithApproval(t *testing.T, approvalTimeout time.Duration) *testServer {
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

	ts := &testServer{t: t, dir: dir, srv: srv, svc: svc, driver: driver, addr: srv.Addr()}
	ts.token = ts.readToken()
	return ts
}

func (ts *testServer) readToken() string {
	ts.t.Helper()

	data, err := os.ReadFile(filepath.Join(ts.dir, "api.json"))
	if err != nil {
		ts.t.Fatalf("reading token file: %v", err)
	}
	var payload struct {
		Port  int    `json:"port"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		ts.t.Fatalf("parsing token file: %v", err)
	}
	return payload.Token
}

// do sends a request with the given bearer token (empty means no
// Authorization header at all) and a JSON-encoded body (nil means none).
func (ts *testServer) do(method, path, token string, body any) *http.Response {
	ts.t.Helper()
	return ts.request(method, path, token, body, nil)
}

// request is do with a hook to mutate the request before it is sent, for the
// cases a bearer token is not enough to express — an Origin header, say.
func (ts *testServer) request(method, path, token string, body any, mutate func(*http.Request)) *http.Response {
	ts.t.Helper()

	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			ts.t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(data)
	}

	req, err := http.NewRequest(method, "http://"+ts.addr+path, reader)
	if err != nil {
		ts.t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if mutate != nil {
		mutate(req)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		ts.t.Fatalf("do request: %v", err)
	}
	return resp
}

// waitingInConsole polls the facade's Approval Console until the in-flight
// request's mutation shows up there. The request is real and on another
// goroutine, so the test watches for it rather than being handed a
// synchronisation point.
func (ts *testServer) waitingInConsole() guard.Waiting {
	ts.t.Helper()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if waiting := ts.svc.ListPendingApprovals(); len(waiting) == 1 {
			return waiting[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	ts.t.Fatal("nothing ever reached the Approval Console; the request is not waiting where it should be")
	return guard.Waiting{}
}

// awaited returns the response to the in-flight request, failing rather than
// hanging if it never arrives.
func (ts *testServer) awaited(responses <-chan *http.Response) *http.Response {
	ts.t.Helper()

	select {
	case resp := <-responses:
		return resp
	case <-time.After(30 * time.Second):
		ts.t.Fatal("/query never answered")
		return nil
	}
}

func TestListenerBindsLoopbackOnly(t *testing.T) {
	ts := newTestServer(t)

	host, _, err := net.SplitHostPort(ts.addr)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", ts.addr, err)
	}
	if host != "127.0.0.1" {
		t.Errorf("listener host = %q, want 127.0.0.1", host)
	}
}

func TestTokenFileLifecycle(t *testing.T) {
	driver := dbtest.NewFakeDriver()
	svc := service.NewWithDriver(
		db.NewProfileStore(t.TempDir(), dbtest.NewFakeKeychain()), driver,
		guard.NewJSONLAuditLog(t.TempDir()), nil,
	)
	dir := t.TempDir()
	srv := api.New(svc, dir)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	path := filepath.Join(dir, "api.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("token file missing after Start: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("token file perm = %o, want 0600", perm)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading token file: %v", err)
	}
	var payload struct {
		Port  int    `json:"port"`
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("parsing token file: %v", err)
	}
	if payload.Token == "" {
		t.Error("token file has an empty token")
	}

	_, portStr, err := net.SplitHostPort(srv.Addr())
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", srv.Addr(), err)
	}
	wantPort, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parsing listener port: %v", err)
	}
	if payload.Port != wantPort {
		t.Errorf("token file port = %d, want the live listener port %d", payload.Port, wantPort)
	}

	// The port the file names must actually be accepting connections: the file
	// is written after the listener is up, and this is the promise that matters.
	conn, err := net.Dial("tcp", srv.Addr())
	if err != nil {
		t.Errorf("dialing the port the token file names: %v", err)
	} else {
		conn.Close()
	}

	if err := srv.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("token file still present after Close (stat err = %v), want it removed", err)
	}
}

func TestAuthGatesEveryRequest(t *testing.T) {
	ts := newTestServer(t)

	cases := []struct {
		name       string
		token      string
		wantStatus int
	}{
		{name: "no token", token: "", wantStatus: http.StatusUnauthorized},
		{name: "wrong token", token: "0000000000000000000000000000000000000000000000000000000000000000", wantStatus: http.StatusUnauthorized},
		{name: "valid token", token: ts.token, wantStatus: http.StatusOK},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := ts.do(http.MethodGet, "/health", tc.token, nil)
			defer resp.Body.Close()

			if resp.StatusCode != tc.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.wantStatus)
			}
			if tc.wantStatus == http.StatusOK {
				var got struct {
					OK bool `json:"ok"`
				}
				if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
					t.Fatalf("decoding response: %v", err)
				}
				if !got.OK {
					t.Errorf("health = %+v, want ok", got)
				}
			}
		})
	}
}

func TestOriginHeaderRejectedEvenWithValidToken(t *testing.T) {
	ts := newTestServer(t)

	resp := ts.request(http.MethodGet, "/health", ts.token, nil, func(r *http.Request) {
		r.Header.Set("Origin", "http://evil.example")
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("status = %d, want 403: a valid token must not rescue a request carrying an Origin header", resp.StatusCode)
	}
}

func TestQueryReadPassesThroughFacade(t *testing.T) {
	ts := newTestServer(t)
	ts.driver.Accept("local", "s3cret")
	ts.driver.Answer("local", db.ResultSet{
		Columns: []string{"id"},
		Rows:    [][]*string{{str("1")}},
	})
	mustSave(t, ts.svc, localProfile("local"), "s3cret")
	if err := ts.svc.Connect(context.Background(), "local"); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	resp := ts.do(http.MethodPost, "/query", ts.token, map[string]string{
		"profile": "local",
		"sql":     "SELECT id FROM users",
		"origin":  "human",
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var result service.QueryResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if result.Status != service.QueryOK {
		t.Fatalf("status = %q, want %q (message: %s)", result.Status, service.QueryOK, result.Message)
	}
	// One field deep is enough: this proves the facade's rows crossed the
	// pipe, not that the read path itself is correct — that is query_test.go's job.
	if len(result.Rows) != 1 || result.Rows[0][0] == nil || *result.Rows[0][0] != "1" {
		t.Errorf("rows = %v, want the scripted row [[1]] to have come through", result.Rows)
	}
}

// TestQueryMutationBlocksUntilTheConsoleDecides is the one thing about this
// pipe that is not obvious: a POST to /query can stay open for minutes. The
// response does not arrive until a human answers the Approval Console, and
// when it does it carries what the mutation really did.
//
// Whether the gate should have withheld it, and what it records, is
// internal/service's claim. What this test states is that the pipe survives the
// wait — the server's write deadline does not cut it short, and the outcome
// crosses intact.
func TestQueryMutationBlocksUntilTheConsoleDecides(t *testing.T) {
	ts := newTestServer(t)
	ts.driver.Accept("local", "s3cret")
	ts.driver.Affect("local", 4)
	mustSave(t, ts.svc, localProfile("local"), "s3cret")
	if err := ts.svc.Connect(context.Background(), "local"); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	responses := make(chan *http.Response, 1)
	go func() {
		responses <- ts.do(http.MethodPost, "/query", ts.token, map[string]string{
			"profile": "local",
			"sql":     "DELETE FROM users",
			"origin":  "ai",
		})
	}()

	// The request is in flight and the facade is holding it: the mutation shows
	// up in the Approval Console while nothing has come back over the wire.
	waiting := ts.waitingInConsole()
	select {
	case resp := <-responses:
		resp.Body.Close()
		t.Fatal("/query answered before anybody decided the mutation")
	default:
	}

	if ack := ts.svc.ApprovePending(waiting.ID); !ack.OK {
		t.Fatalf("ApprovePending = %+v", ack)
	}

	resp := ts.awaited(responses)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var result service.QueryResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if result.Status != service.QueryExecuted {
		t.Fatalf("status = %q, want %q (message: %s)", result.Status, service.QueryExecuted, result.Message)
	}
	if result.AffectedRows != 4 {
		t.Errorf("affected_rows = %d, want the 4 the facade reported to have crossed the pipe", result.AffectedRows)
	}
}

// TestQueryMutationAnswersWhenTheApprovalTimesOut: nobody decides, the deadline
// passes, and the request is still answered — with the timed-out outcome rather
// than a dropped connection.
func TestQueryMutationAnswersWhenTheApprovalTimesOut(t *testing.T) {
	ts := newTestServerWithApproval(t, 200*time.Millisecond)
	ts.driver.Accept("local", "s3cret")
	mustSave(t, ts.svc, localProfile("local"), "s3cret")
	if err := ts.svc.Connect(context.Background(), "local"); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	resp := ts.do(http.MethodPost, "/query", ts.token, map[string]string{
		"profile": "local",
		"sql":     "DELETE FROM users",
		"origin":  "ai",
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var result service.QueryResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if result.Status != service.QueryTimedOut {
		t.Errorf("status = %q, want %q (message: %s)", result.Status, service.QueryTimedOut, result.Message)
	}
	if len(ts.driver.Execs("local")) != 0 {
		t.Errorf("a mutation nobody approved executed %v", ts.driver.Execs("local"))
	}
}

func TestQueryDefaultsMissingOriginToAI(t *testing.T) {
	ts := newTestServer(t)
	ts.driver.Accept("local", "s3cret")
	ts.driver.Answer("local", db.ResultSet{Columns: []string{"id"}, Rows: [][]*string{{str("1")}}})
	mustSave(t, ts.svc, localProfile("local"), "s3cret")
	if err := ts.svc.Connect(context.Background(), "local"); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	resp := ts.do(http.MethodPost, "/query", ts.token, map[string]string{
		"profile": "local",
		"sql":     "SELECT id FROM users",
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var result service.QueryResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if result.Origin != guard.OriginAI {
		t.Errorf("Origin = %q, want a missing origin defaulted to %q — the MCP proxy is the only expected remote caller", result.Origin, guard.OriginAI)
	}
}

func TestQueryRejectsInvalidOriginInBody(t *testing.T) {
	ts := newTestServer(t)
	ts.driver.Accept("local", "s3cret")
	mustSave(t, ts.svc, localProfile("local"), "s3cret")
	if err := ts.svc.Connect(context.Background(), "local"); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	resp := ts.do(http.MethodPost, "/query", ts.token, map[string]string{
		"profile": "local",
		"sql":     "SELECT 1",
		"origin":  "robot",
	})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestProfilesReportsConnectedState(t *testing.T) {
	ts := newTestServer(t)
	ts.driver.Accept("local", "s3cret")
	mustSave(t, ts.svc, localProfile("local"), "s3cret")
	mustSave(t, ts.svc, localProfile("other"), "s3cret")
	if err := ts.svc.Connect(context.Background(), "local"); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	resp := ts.do(http.MethodGet, "/profiles", ts.token, nil)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got []struct {
		Name      string `json:"name"`
		Connected bool   `json:"connected"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}

	byName := map[string]bool{}
	for _, p := range got {
		byName[p.Name] = p.Connected
	}
	if !byName["local"] {
		t.Errorf("profiles = %+v, want local reported connected", got)
	}
	if byName["other"] {
		t.Errorf("profiles = %+v, want other reported not connected", got)
	}
}

func TestConnectPassesThroughFacade(t *testing.T) {
	ts := newTestServer(t)
	ts.driver.Accept("local", "s3cret")
	mustSave(t, ts.svc, localProfile("local"), "s3cret")

	resp := ts.do(http.MethodPost, "/connect", ts.token, map[string]string{"profile": "local"})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got struct {
		OK      bool   `json:"ok"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !got.OK {
		t.Errorf("connect = %+v, want ok", got)
	}

	connected := ts.svc.ConnectedProfiles()
	if len(connected) != 1 || connected[0] != "local" {
		t.Errorf("ConnectedProfiles = %v, want [local]: the facade must actually have connected", connected)
	}
}
