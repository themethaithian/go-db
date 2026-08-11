package service_test

// The Database tree browsing a real Redis, through the real adapter and the
// real Approval Gate.
//
// The unit tests beside this one script SCAN replies and pin what the loop does
// with them. What they cannot say is whether a real server answers the shape
// the loop is written against — a paging cursor, a page of keys, cursor 0 at
// the end — or whether SCAN survives the classifier and the adapter's own
// second check on the way out. That is what this proves, and it is the reason
// the whole feature is built on reads rather than on a port of its own: the
// same path everything else takes, taken by introspection too.
//
// It skips — never fails — when Docker is not available, like every other
// integration test in this repo, and shares the dockerRun and condense helpers
// with connection_integration_test.go.

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/themethaithian/go-db/internal/db"
	"github.com/themethaithian/go-db/internal/db/dbtest"
	"github.com/themethaithian/go-db/internal/guard"
	"github.com/themethaithian/go-db/internal/service"
)

const (
	redisImage = "redis:7"

	// The index this test browses. Its own, so the keyspace it lists is the one
	// it seeded and nothing else.
	redisIndex = "4"

	redisReadyTimeout = 30 * time.Second
)

var (
	redisOnce   sync.Once
	redisAddr   string // host:port the container's Redis is reachable on
	redisSkip   string // why the container is unavailable, if it is
	redisDocker string // container id, empty when nothing was started
)

// requireRedis returns the address of the shared throwaway Redis, starting it
// on first use. Like requireMySQL, it skips when Docker is missing and fails
// when Docker is there and the server would not come up.
func requireRedis(t *testing.T) string {
	t.Helper()

	redisOnce.Do(startRedisContainer)
	if redisSkip != "" {
		t.Skipf("skipping integration test: %s", redisSkip)
	}
	if redisAddr == "" {
		t.Fatal("Redis container did not start")
	}
	return redisAddr
}

func startRedisContainer() {
	if out, err := dockerRun("info", "--format", "{{.ServerVersion}}"); err != nil {
		redisSkip = fmt.Sprintf("Docker is not available (%v: %s); start it (e.g. `colima start`) to run this test", err, condense(out))
		return
	}

	id, err := dockerRun("run", "--detach", "--rm", "--publish", "127.0.0.1::6379", redisImage)
	if err != nil {
		redisSkip = fmt.Sprintf("could not start %s (%v: %s)", redisImage, err, condense(id))
		return
	}
	redisDocker = strings.TrimSpace(id)

	addr, err := dockerRun("port", redisDocker, "6379/tcp")
	if err != nil {
		redisSkip = fmt.Sprintf("could not read the container's mapped port (%v: %s)", err, condense(addr))
		stopRedisContainer()
		return
	}
	for line := range strings.SplitSeq(strings.TrimSpace(addr), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			redisAddr = line
			break
		}
	}

	if err := waitForRedis(redisAddr); err != nil {
		redisSkip = fmt.Sprintf("Redis in %s never became ready: %v", redisImage, err)
		stopRedisContainer()
		redisAddr = ""
	}
}

func stopRedisContainer() {
	if redisDocker == "" {
		return
	}
	_, _ = dockerRun("stop", "--time", "0", redisDocker)
	redisDocker = ""
}

// waitForRedis polls addr until something accepts a connection there. Redis
// listens once and stays listening — there is no restart to ride out, so one
// successful dial is readiness.
func waitForRedis(addr string) error {
	deadline := time.Now().Add(redisReadyTimeout)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			return conn.Close()
		}
		lastErr = err
		time.Sleep(200 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("timed out after %s", redisReadyTimeout)
	}
	return lastErr
}

// The Explorer's Redis introspection end to end: a real keyspace, listed by the
// facade the tree calls, over the adapter that re-checks every read.
func TestIntegrationListTablesScansARealKeyspace(t *testing.T) {
	address := requireRedis(t)
	host, port := splitHostPort(t, address)

	svc := service.New(db.NewProfileStore(t.TempDir(), dbtest.NewFakeKeychain()), guard.NewJSONLAuditLog(t.TempDir()))
	t.Cleanup(func() {
		if err := svc.Close(); err != nil {
			t.Errorf("closing the Connection Registry: %v", err)
		}
	})

	profile := db.Profile{
		Name: "keyspace", Host: host, Port: port, Database: redisIndex, Engine: db.EngineRedis,
	}
	if err := svc.SaveProfile(profile, ""); err != nil {
		t.Fatalf("SaveProfile: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := svc.Connect(ctx, "keyspace"); err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// Seeded through the adapter's write path rather than the facade's: what is
	// under test is the read, and putting 300 keys through the Approval Gate
	// one Inline Confirm at a time would be testing the gate again.
	seed, err := db.NewRedisDriver().Open(ctx, profile, "", nil)
	if err != nil {
		t.Fatalf("opening a seeding connection: %v", err)
	}
	defer seed.Close() //nolint:errcheck // teardown
	if _, err := seed.Exec(ctx, "FLUSHDB"); err != nil {
		t.Fatalf("Exec(FLUSHDB): %v", err)
	}

	const seeded = 300
	for i := 0; i < seeded; i++ {
		if _, err := seed.Exec(ctx, fmt.Sprintf("SET browse:key:%03d v", i)); err != nil {
			t.Fatalf("Exec(SET): %v", err)
		}
	}

	// The databases level first: one index, the Profile's own.
	databases := svc.ListDatabases(ctx, "keyspace")
	if databases.Status != service.SchemaOK {
		t.Fatalf("ListDatabases status = %q, want %q (message: %s)", databases.Status, service.SchemaOK, databases.Message)
	}
	if len(databases.Databases) != 1 || databases.Databases[0] != redisIndex {
		t.Errorf("databases = %v, want just the connected index %q", databases.Databases, redisIndex)
	}

	got := svc.ListTables(ctx, "keyspace", redisIndex)

	if got.Status != service.SchemaOK {
		t.Fatalf("ListTables status = %q, want %q (message: %s)", got.Status, service.SchemaOK, got.Message)
	}
	if len(got.Tables) != seeded {
		t.Fatalf("keys = %d, want the %d seeded — the scan is meant to page until the cursor comes home", len(got.Tables), seeded)
	}
	if got.Truncated {
		t.Error("Truncated = true on a keyspace well under the cap")
	}
	for i, table := range got.Tables {
		want := fmt.Sprintf("browse:key:%03d", i)
		if table.Name != want {
			t.Fatalf("keys[%d] = %q, want %q — the keys come back sorted", i, table.Name, want)
		}
	}

	// And the level below is refused rather than asked: a key has no columns.
	if columns := svc.ListColumns(ctx, "keyspace", redisIndex, got.Tables[0].Name); columns.Status != service.SchemaFailed {
		t.Errorf("ListColumns status = %q, want %q (message: %s)", columns.Status, service.SchemaFailed, columns.Message)
	}
}

func splitHostPort(t *testing.T, address string) (string, int) {
	t.Helper()

	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatalf("parsing container address %q: %v", address, err)
	}
	var port int
	if _, err := fmt.Sscanf(portText, "%d", &port); err != nil {
		t.Fatalf("parsing container port %q: %v", portText, err)
	}
	return host, port
}
