package service_test

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/themethaithian/go-db/internal/db"
	"github.com/themethaithian/go-db/internal/db/dbtest"
	"github.com/themethaithian/go-db/internal/guard"
	"github.com/themethaithian/go-db/internal/service"
)

// These tests exercise the App Service facade over the real MySQL adapter,
// against a throwaway MySQL 8 container. They skip — never fail — when Docker
// is not available, so `go test ./...` stays green on a machine without it.

const (
	mysqlImage        = "mysql:8"
	mysqlRootUser     = "root"
	mysqlRootPassword = "integration-root-pw"
	mysqlDatabase     = "godb_integration"

	// dockerTimeout bounds the `docker` calls the helper makes; readyTimeout
	// bounds the wait for the server to accept connections (MySQL 8 runs a
	// short initialisation and restarts once before it listens on TCP).
	dockerTimeout = 30 * time.Second
	readyTimeout  = 90 * time.Second
)

// The container is started at most once per package run and torn down by
// TestMain, so every integration test in this file shares it.
var (
	mysqlOnce   sync.Once
	mysqlAddr   string // host:port the container's MySQL is reachable on
	mysqlSkip   string // why the container is unavailable, if it is
	mysqlDocker string // container id, empty when nothing was started
)

func TestMain(m *testing.M) {
	code := m.Run()
	stopMySQLContainer()
	os.Exit(code)
}

// requireMySQL returns the host and port of the shared throwaway MySQL server,
// starting it on first use. It skips the calling test when Docker is not
// available, or fails it when Docker is available but the server would not come
// up (a broken container is a real failure, not a reason to skip).
func requireMySQL(t *testing.T) (host string, port int) {
	t.Helper()

	mysqlOnce.Do(startMySQLContainer)
	if mysqlSkip != "" {
		t.Skipf("skipping integration test: %s", mysqlSkip)
	}
	if mysqlAddr == "" {
		t.Fatal("MySQL container did not start")
	}

	host, portText, err := net.SplitHostPort(mysqlAddr)
	if err != nil {
		t.Fatalf("parsing container address %q: %v", mysqlAddr, err)
	}
	port, err = strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("parsing container port %q: %v", portText, err)
	}
	return host, port
}

// startMySQLContainer runs a throwaway MySQL 8 on a random loopback port with
// its data directory on tmpfs (initialisation is the slow part, and none of it
// needs to survive). It records why it gave up in mysqlSkip rather than
// failing: Docker is optional on a developer machine.
func startMySQLContainer() {
	if out, err := dockerRun("info", "--format", "{{.ServerVersion}}"); err != nil {
		mysqlSkip = fmt.Sprintf("Docker is not available (%v: %s); start it (e.g. `colima start`) to run this test", err, condense(out))
		return
	}

	id, err := dockerRun("run", "--detach", "--rm",
		"--publish", "127.0.0.1::3306",
		"--env", "MYSQL_ROOT_PASSWORD="+mysqlRootPassword,
		"--env", "MYSQL_DATABASE="+mysqlDatabase,
		"--tmpfs", "/var/lib/mysql:rw,size=512m",
		mysqlImage,
		"--skip-log-bin", "--skip-performance-schema",
	)
	if err != nil {
		mysqlSkip = fmt.Sprintf("could not start %s (%v: %s)", mysqlImage, err, condense(id))
		return
	}
	mysqlDocker = strings.TrimSpace(id)

	addr, err := dockerRun("port", mysqlDocker, "3306/tcp")
	if err != nil {
		mysqlSkip = fmt.Sprintf("could not read the container's mapped port (%v: %s)", err, condense(addr))
		stopMySQLContainer()
		return
	}
	// `docker port` may report several bindings; the first loopback one wins.
	for line := range strings.SplitSeq(strings.TrimSpace(addr), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			mysqlAddr = line
			break
		}
	}

	if err := waitForMySQL(mysqlAddr); err != nil {
		mysqlSkip = fmt.Sprintf("MySQL in %s never became ready: %v", mysqlImage, err)
		stopMySQLContainer()
		mysqlAddr = ""
	}
}

func stopMySQLContainer() {
	if mysqlDocker == "" {
		return
	}
	// --rm on the container removes it once stopped.
	_, _ = dockerRun("stop", "--time", "0", mysqlDocker)
	mysqlDocker = ""
}

// condense flattens command output into one line, so a skip reason reads as a
// sentence rather than a wall of docker chatter.
func condense(out string) string {
	return strings.Join(strings.Fields(out), " ")
}

func dockerRun(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), dockerTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	return string(out), err
}

// quietLogger swallows the driver's chatter about connections dropped while
// the server is still initialising; those are expected here.
type quietLogger struct{}

func (quietLogger) Print(...any) {}

// waitForMySQL polls addr until the server answers a query. MySQL 8 accepts
// connections briefly during initialisation and then restarts, so readiness
// means several consecutive successful queries, not the first one.
func waitForMySQL(addr string) error {
	_ = mysql.SetLogger(quietLogger{})
	defer mysql.SetLogger(log.New(os.Stderr, "[mysql] ", log.Ldate|log.Ltime|log.Lshortfile))

	cfg := mysql.NewConfig()
	cfg.Net, cfg.Addr = "tcp", addr
	cfg.User, cfg.Passwd = mysqlRootUser, mysqlRootPassword
	cfg.Timeout = 2 * time.Second
	cfg.ReadTimeout = 2 * time.Second

	connector, err := mysql.NewConnector(cfg)
	if err != nil {
		return err
	}
	pool := sql.OpenDB(connector)
	defer pool.Close()

	deadline := time.Now().Add(readyTimeout)
	streak, lastErr := 0, error(nil)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		var one int
		lastErr = pool.QueryRowContext(ctx, "SELECT 1").Scan(&one)
		cancel()

		if lastErr == nil {
			if streak++; streak == 3 {
				return nil
			}
		} else {
			streak = 0
		}
		time.Sleep(500 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("timed out after %s", readyTimeout)
	}
	return lastErr
}

// closedPort returns a loopback port nothing is listening on.
func closedPort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("releasing the reserved port: %v", err)
	}
	return port
}

// newMySQLFacade builds an App Service over the real MySQL adapter.
func newMySQLFacade(t *testing.T) *service.AppService {
	t.Helper()

	svc := service.New(db.NewProfileStore(t.TempDir(), dbtest.NewFakeKeychain()), guard.NewJSONLAuditLog(t.TempDir()))
	t.Cleanup(func() {
		if err := svc.Close(); err != nil {
			t.Errorf("closing the Connection Registry: %v", err)
		}
	})
	return svc
}

func TestIntegrationTestConnectionOutcomes(t *testing.T) {
	host, port := requireMySQL(t)
	dead := closedPort(t)

	cases := []struct {
		name       string
		profile    db.Profile
		password   string
		wantStatus service.ConnectionStatus
	}{
		{
			name:       "correct credentials",
			profile:    db.Profile{Name: "good", Host: host, Port: port, User: mysqlRootUser, Database: mysqlDatabase},
			password:   mysqlRootPassword,
			wantStatus: service.ConnectionOK,
		},
		{
			name:       "wrong password",
			profile:    db.Profile{Name: "wrong-password", Host: host, Port: port, User: mysqlRootUser, Database: mysqlDatabase},
			password:   "not-the-root-password",
			wantStatus: service.ConnectionAuthFailed,
		},
		{
			name:       "unknown user",
			profile:    db.Profile{Name: "unknown-user", Host: host, Port: port, User: "nobody", Database: mysqlDatabase},
			password:   "whatever",
			wantStatus: service.ConnectionAuthFailed,
		},
		{
			name:       "nothing listening on the port",
			profile:    db.Profile{Name: "dead-port", Host: "127.0.0.1", Port: dead, User: mysqlRootUser, Database: mysqlDatabase},
			password:   mysqlRootPassword,
			wantStatus: service.ConnectionUnreachable,
		},
		{
			name:       "host does not resolve",
			profile:    db.Profile{Name: "bad-host", Host: "no-such-host.invalid", Port: 3306, User: mysqlRootUser},
			password:   mysqlRootPassword,
			wantStatus: service.ConnectionUnreachable,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newMySQLFacade(t)
			mustSave(t, svc, tc.profile, tc.password)

			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()

			got := svc.TestConnection(ctx, tc.profile.Name)
			if got.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q (message: %s)", got.Status, tc.wantStatus, got.Message)
			}
			t.Logf("%s -> %s: %s", tc.profile.Name, got.Status, got.Message)

			if strings.Contains(got.Message, tc.password) {
				t.Errorf("message leaks the password: %q", got.Message)
			}
			if got := svc.ConnectedProfiles(); len(got) != 0 {
				t.Errorf("TestConnection left %v connected, want nothing", got)
			}
		})
	}
}

func TestIntegrationRegistryHoldsTwoConnections(t *testing.T) {
	host, port := requireMySQL(t)
	svc := newMySQLFacade(t)

	// Two Profiles reaching the same server, as the editor and the MCP server
	// would: the Registry is keyed by Profile name, not by address.
	for _, name := range []string{"editor", "agent"} {
		mustSave(t, svc, db.Profile{
			Name: name, Host: host, Port: port, User: mysqlRootUser, Database: mysqlDatabase,
		}, mysqlRootPassword)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for _, name := range []string{"editor", "agent"} {
		if err := svc.Connect(ctx, name); err != nil {
			t.Fatalf("Connect(%q): %v", name, err)
		}
	}

	if got := svc.ConnectedProfiles(); !equalStrings(got, []string{"agent", "editor"}) {
		t.Fatalf("ConnectedProfiles = %v, want [agent editor]", got)
	}
	// Both must still be usable while the other is held.
	for _, name := range []string{"editor", "agent"} {
		if err := svc.Ping(ctx, name); err != nil {
			t.Errorf("Ping(%q) while both connections are held: %v", name, err)
		}
	}

	if err := svc.Disconnect("editor"); err != nil {
		t.Fatalf("Disconnect(editor): %v", err)
	}
	if got := svc.ConnectedProfiles(); !equalStrings(got, []string{"agent"}) {
		t.Errorf("ConnectedProfiles = %v, want [agent]", got)
	}
	if err := svc.Ping(ctx, "agent"); err != nil {
		t.Errorf("Ping(agent) after disconnecting editor: %v", err)
	}
}
