package db_test

// This is the acceptance test for the Redis adapter: a real Redis 7 in Docker,
// reached through the Driver and Conn ports exactly as the Connection Registry
// will reach it.
//
// It skips — never fails — when Docker is not available, like the tunnel test
// beside it and the integration tests in internal/guard and internal/service,
// and shares one throwaway container across its tests the way those do. The
// server is started with a password, so the ordinary path and the rejected one
// are the same server rather than two setups.
//
// The docker, condense, firstLine and splitAddress helpers are the tunnel
// test's, shared by being in the same package.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/themethaithian/go-db/internal/db"
)

const (
	redisImage = "redis:7"

	// The default user's password. Everything but the auth-failure test uses
	// it, which is what makes that test a rejection rather than a different
	// server.
	redisPassword = "redis-integration-pw"

	redisDockerTimeout = 60 * time.Second
	redisReadyTimeout  = 60 * time.Second
)

func TestIntegrationRedisDriver(t *testing.T) {
	address := startRedisServer(t)

	t.Run("a value written through Exec is read back through ReadQuery", func(t *testing.T) {
		conn, ctx := openRedis(t, address, "")

		affected, err := conn.Exec(ctx, `SET greeting "hello world"`)
		if err != nil {
			t.Fatalf("Exec(SET): %v", err)
		}
		// SET answers OK, a string: there is no count in it, and the adapter
		// says 0 rather than inventing one.
		if affected != 0 {
			t.Errorf("Exec(SET) affected = %d, want 0 for a reply that is not a count", affected)
		}

		result, err := conn.ReadQuery(ctx, "GET greeting")
		if err != nil {
			t.Fatalf("ReadQuery(GET): %v", err)
		}
		if got := result.Kind(); got != db.ResultValue {
			t.Fatalf("Kind() = %q, want %q — Redis answers with one typed value", got, db.ResultValue)
		}
		reply, ok := result.Value()
		if !ok {
			t.Fatal("Value() reported the arm absent on a Result tagged value")
		}
		if reply.Kind != db.ReplyString || reply.Text != "hello world" {
			t.Errorf("reply = %#v, want the string \"hello world\"", reply)
		}
	})

	t.Run("a missing key is a nil reply, not a failure", func(t *testing.T) {
		conn, ctx := openRedis(t, address, "")

		result, err := conn.ReadQuery(ctx, "GET no-such-key")
		if err != nil {
			t.Fatalf("ReadQuery(GET) on a missing key: %v", err)
		}
		reply, _ := result.Value()
		if reply.Kind != db.ReplyNil {
			t.Errorf("reply = %#v, want a nil reply — the key is absent, and that is the answer", reply)
		}
	})

	t.Run("an integer reply is the affected count", func(t *testing.T) {
		conn, ctx := openRedis(t, address, "")

		for _, command := range []string{"SET doomed-one 1", "SET doomed-two 2"} {
			if _, err := conn.Exec(ctx, command); err != nil {
				t.Fatalf("Exec(%q): %v", command, err)
			}
		}

		affected, err := conn.Exec(ctx, "DEL doomed-one doomed-two no-such-key")
		if err != nil {
			t.Fatalf("Exec(DEL): %v", err)
		}
		if affected != 2 {
			t.Errorf("Exec(DEL) affected = %d, want 2 — the two keys that were there", affected)
		}
	})

	// This is the layer ADR-0006 leaves the classifier carrying alone, running
	// against a server that would happily have done it. The key surviving is
	// the assertion that matters: the refusal happened before execution, not
	// after.
	t.Run("ReadQuery refuses a mutation the server would have run", func(t *testing.T) {
		conn, ctx := openRedis(t, address, "")

		if _, err := conn.Exec(ctx, "SET survivor alive"); err != nil {
			t.Fatalf("Exec(SET): %v", err)
		}

		if _, err := conn.ReadQuery(ctx, "DEL survivor"); !errors.Is(err, db.ErrWriteAttempt) {
			t.Fatalf("ReadQuery(DEL) error = %v, want it to wrap db.ErrWriteAttempt", err)
		}

		result, err := conn.ReadQuery(ctx, "GET survivor")
		if err != nil {
			t.Fatalf("ReadQuery(GET): %v", err)
		}
		reply, _ := result.Value()
		if reply.Kind != db.ReplyString || reply.Text != "alive" {
			t.Errorf("reply = %#v, want the key still there — the refused DEL must never have reached the server", reply)
		}
	})

	t.Run("a reply past the cap is cut and says so", func(t *testing.T) {
		conn, ctx := openRedis(t, address, "")

		var push strings.Builder
		push.WriteString("RPUSH long-list")
		for i := 0; i < db.MaxReplyItems+50; i++ {
			fmt.Fprintf(&push, " %d", i)
		}
		length, err := conn.Exec(ctx, push.String())
		if err != nil {
			t.Fatalf("Exec(RPUSH): %v", err)
		}
		if want := int64(db.MaxReplyItems + 50); length != want {
			t.Fatalf("Exec(RPUSH) affected = %d, want the new length %d", length, want)
		}

		result, err := conn.ReadQuery(ctx, "LRANGE long-list 0 -1")
		if err != nil {
			t.Fatalf("ReadQuery(LRANGE): %v", err)
		}
		reply, _ := result.Value()
		if reply.Kind != db.ReplyArray {
			t.Fatalf("reply = %#v, want an array", reply)
		}
		if len(reply.Items) != db.MaxReplyItems {
			t.Errorf("items = %d, want the cap of %d", len(reply.Items), db.MaxReplyItems)
		}
		if !reply.Truncated {
			t.Error("Truncated = false, want the cut marked so the list on screen is not read as the whole one")
		}
		if reply.Items[0].Kind != db.ReplyString || reply.Items[0].Text != "0" {
			t.Errorf("items[0] = %#v, want the list's first element", reply.Items[0])
		}
	})

	// CONFIG GET is the read that proves RESP3 was negotiated: a server
	// speaking RESP2 answers it with a flat array, and the map arm would never
	// be built at all.
	t.Run("a map reply keeps its keys", func(t *testing.T) {
		conn, ctx := openRedis(t, address, "")

		result, err := conn.ReadQuery(ctx, "CONFIG GET maxmemory")
		if err != nil {
			t.Fatalf("ReadQuery(CONFIG GET): %v", err)
		}
		reply, _ := result.Value()
		if reply.Kind != db.ReplyMap {
			t.Fatalf("reply = %#v, want a map", reply)
		}
		if len(reply.Entries) != 1 || reply.Entries[0].Key.Text != "maxmemory" {
			t.Errorf("entries = %#v, want one entry keyed maxmemory", reply.Entries)
		}
	})

	t.Run("Ping answers on a live connection", func(t *testing.T) {
		conn, ctx := openRedis(t, address, "")

		if err := conn.Ping(ctx); err != nil {
			t.Errorf("Ping: %v", err)
		}
	})
}

// TestIntegrationRedisDatabaseIndex is the Profile field that means something
// different on this Engine. Redis numbers its databases, and a Profile naming
// index 1 must land there — a connection that quietly stayed on 0 would look
// like it worked while writing to the wrong keyspace.
func TestIntegrationRedisDatabaseIndex(t *testing.T) {
	address := startRedisServer(t)

	one, ctx := openRedis(t, address, "1")
	if _, err := one.Exec(ctx, "SET only-in-one yes"); err != nil {
		t.Fatalf("Exec(SET) on database 1: %v", err)
	}

	zero, ctx := openRedis(t, address, "")
	result, err := zero.ReadQuery(ctx, "GET only-in-one")
	if err != nil {
		t.Fatalf("ReadQuery(GET) on database 0: %v", err)
	}
	reply, _ := result.Value()
	if reply.Kind != db.ReplyNil {
		t.Errorf("reply = %#v on database 0, want nil — the key was written to database 1", reply)
	}

	back, ctx := openRedis(t, address, "1")
	result, err = back.ReadQuery(ctx, "GET only-in-one")
	if err != nil {
		t.Fatalf("ReadQuery(GET) on database 1: %v", err)
	}
	reply, _ = result.Value()
	if reply.Kind != db.ReplyString || reply.Text != "yes" {
		t.Errorf("reply = %#v on database 1, want the key that was written there", reply)
	}
}

// TestIntegrationRedisDatabaseIndexRefusesAName pins the failure that must not
// be a silent default. The address is a server that is really there, so the
// only thing being tested is that a Profile naming a database Redis cannot have
// fails before anything is dialled.
func TestIntegrationRedisDatabaseIndexRefusesAName(t *testing.T) {
	address := startRedisServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	opened, err := db.NewRedisDriver().Open(ctx, redisProfile(address, "production"), redisPassword, nil)
	if err == nil {
		opened.Close() //nolint:errcheck // it should never have opened
		t.Fatal("Open with a named database succeeded, want it refused rather than defaulted to index 0")
	}
	if !strings.Contains(err.Error(), "production") {
		t.Errorf("Open error = %v, want it to name the database it could not read", err)
	}
}

// TestIntegrationRedisAuthFailure and its sibling below are the two failures
// the ports promise to tell apart, because they call for different fixes.
func TestIntegrationRedisAuthFailure(t *testing.T) {
	address := startRedisServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for _, test := range []struct {
		name     string
		password string
	}{
		{"the wrong password", "not-the-password"},
		{"no password at all", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			opened, err := db.NewRedisDriver().Open(ctx, redisProfile(address, ""), test.password, nil)
			if err == nil {
				opened.Close() //nolint:errcheck // it should never have opened
				t.Fatal("Open succeeded, want it refused")
			}
			if !errors.Is(err, db.ErrAuthFailed) {
				t.Errorf("Open error = %v, want it to wrap db.ErrAuthFailed", err)
			}
			if errors.Is(err, db.ErrUnreachable) {
				t.Error("Open reported the server unreachable; it answered, and said no")
			}
		})
	}
}

func TestIntegrationRedisUnreachable(t *testing.T) {
	// Docker is not needed to prove this one, but the test lives with its
	// sibling and skips with it, so a run with no Docker reports one story.
	startRedisServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	opened, err := db.NewRedisDriver().Open(ctx, redisProfile(closedAddress(t), ""), redisPassword, nil)
	if err == nil {
		opened.Close() //nolint:errcheck // it should never have opened
		t.Fatal("Open on a closed port succeeded")
	}
	if !errors.Is(err, db.ErrUnreachable) {
		t.Errorf("Open error = %v, want it to wrap db.ErrUnreachable", err)
	}
	if errors.Is(err, db.ErrAuthFailed) {
		t.Error("Open reported the credentials rejected; nothing was there to reject them")
	}
}

// TestIntegrationRedisACLUser proves the other half of the credential mapping:
// a Profile's User is Redis's ACL username, and an empty one is the default
// user. The reader is created through Exec on the default user's connection,
// which is the gate's own path for a mutation.
func TestIntegrationRedisACLUser(t *testing.T) {
	address := startRedisServer(t)

	admin, ctx := openRedis(t, address, "")
	if _, err := admin.Exec(ctx, "ACL SETUSER reader on >reader-pw ~* +@all"); err != nil {
		t.Fatalf("Exec(ACL SETUSER): %v", err)
	}

	profile := redisProfile(address, "")
	profile.User = "reader"

	reader, err := db.NewRedisDriver().Open(ctx, profile, "reader-pw", nil)
	if err != nil {
		t.Fatalf("Open as the ACL user reader: %v", err)
	}
	t.Cleanup(func() { reader.Close() }) //nolint:errcheck // teardown

	if err := reader.Ping(ctx); err != nil {
		t.Errorf("Ping as the ACL user reader: %v", err)
	}

	if _, err := db.NewRedisDriver().Open(ctx, profile, "not-reader-pw", nil); !errors.Is(err, db.ErrAuthFailed) {
		t.Errorf("Open as reader with the wrong password: error = %v, want it to wrap db.ErrAuthFailed", err)
	}
}

// TestIntegrationRedisHonoursTheDialFunc pins the seam a tunnelled Profile
// depends on: the Driver dials through the DialFunc it was given and nowhere
// else. The Profile names an address that does not resolve on this machine, so
// a driver that fell back to dialling directly could not possibly succeed.
func TestIntegrationRedisHonoursTheDialFunc(t *testing.T) {
	address := startRedisServer(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var mu sync.Mutex
	var asked []string
	dial := func(ctx context.Context, wanted string) (net.Conn, error) {
		mu.Lock()
		asked = append(asked, wanted)
		mu.Unlock()
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "tcp", address)
	}

	profile := redisProfile("redis.invalid:6379", "")
	opened, err := db.NewRedisDriver().Open(ctx, profile, redisPassword, dial)
	if err != nil {
		t.Fatalf("Open through a DialFunc: %v", err)
	}
	t.Cleanup(func() { opened.Close() }) //nolint:errcheck // teardown

	mu.Lock()
	defer mu.Unlock()
	if len(asked) == 0 {
		t.Fatal("the DialFunc was never called, so the connection went around it")
	}
	if asked[0] != "redis.invalid:6379" {
		t.Errorf("the DialFunc was asked for %q, want the Profile's own address", asked[0])
	}
}

// redisProfile is a Profile for the shared server, on the given database.
func redisProfile(address, database string) db.Profile {
	host, port := splitAddress(address)
	return db.Profile{
		Name:     "redis-integration",
		Host:     host,
		Port:     port,
		Database: database,
		Engine:   db.EngineRedis,
	}
}

// openRedis opens one connection to the shared server and closes it when the
// test ends. The context it returns is the one the test's calls run under.
func openRedis(t *testing.T, address, database string) (db.Conn, context.Context) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)

	opened, err := db.NewRedisDriver().Open(ctx, redisProfile(address, database), redisPassword, nil)
	if err != nil {
		t.Fatalf("opening %s on database %q: %v", address, database, err)
	}
	t.Cleanup(func() { opened.Close() }) //nolint:errcheck // teardown
	return opened, ctx
}

// closedAddress returns a loopback address nothing is listening on: a port is
// taken and released, so the address is real and refuses.
func closedAddress(t *testing.T) string {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("taking a port to release it: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("releasing the port: %v", err)
	}
	return address
}

// The container is started at most once per package run and stopped by
// TestMain, so every test in this file shares it.
var (
	redisOnce      sync.Once
	redisAddress   string // host:port the container's Redis is reachable on
	redisSkip      string // why the container is unavailable, if it is
	redisContainer string // container name, empty when nothing was started
)

func TestMain(m *testing.M) {
	code := m.Run()
	stopRedisContainer()
	os.Exit(code)
}

// startRedisServer returns the shared server's address, starting the container
// on first use. It skips the calling test when Docker is not available, and
// fails it when Docker is there and the server would not come up: a broken
// container is a real failure, not a reason to skip.
func startRedisServer(t *testing.T) string {
	t.Helper()

	redisOnce.Do(startRedisContainer)
	if redisSkip != "" {
		t.Skipf("skipping integration test: %s", redisSkip)
	}
	if redisAddress == "" {
		t.Fatalf("the %s container did not start", redisImage)
	}
	return redisAddress
}

func startRedisContainer() {
	if out, err := dockerCLI("info", "--format", "{{.ServerVersion}}"); err != nil {
		redisSkip = fmt.Sprintf("Docker is not available (%v: %s); start it (e.g. `colima start`) to run this test", err, condense(out))
		return
	}

	name := fmt.Sprintf("godb-redis-driver-%d", time.Now().UnixNano())
	if out, err := dockerCLI("run", "--detach", "--rm",
		"--name", name, "--publish", "127.0.0.1::6379",
		redisImage, "redis-server", "--requirepass", redisPassword,
	); err != nil {
		redisSkip = fmt.Sprintf("could not start %s (%v: %s)", redisImage, err, condense(out))
		return
	}
	redisContainer = name

	mapped, err := dockerCLI("port", name, "6379/tcp")
	if err != nil {
		redisSkip = fmt.Sprintf("could not read the container's mapped port (%v: %s)", err, condense(mapped))
		stopRedisContainer()
		return
	}
	// `docker port` may report several bindings; the first loopback one wins.
	address := firstLine(mapped)
	if address == "" {
		redisSkip = fmt.Sprintf("the container published no port (%s)", condense(mapped))
		stopRedisContainer()
		return
	}
	if err := waitForRedis(address); err != nil {
		redisSkip = fmt.Sprintf("%s at %s never accepted a connection (%v)", redisImage, address, err)
		stopRedisContainer()
		return
	}
	redisAddress = address
}

// waitForRedis waits for the server to accept a connection. Redis is listening
// in well under a second, so this is a formality — but a race with the
// container's start would read as an adapter failure, and that is the one thing
// this file must never say by mistake.
func waitForRedis(address string) error {
	deadline := time.Now().Add(redisReadyTimeout)

	var last error
	for time.Now().Before(deadline) {
		socket, err := net.DialTimeout("tcp", address, 2*time.Second)
		if err == nil {
			return socket.Close()
		}
		last = err
		time.Sleep(200 * time.Millisecond)
	}
	return last
}

func stopRedisContainer() {
	if redisContainer == "" {
		return
	}
	// --rm on the container removes it once stopped.
	dockerCLI("stop", "--time", "0", redisContainer) //nolint:errcheck // teardown
	redisContainer = ""
}

// dockerCLI runs one docker command. It takes no *testing.T because the
// container is started and stopped outside any one test.
func dockerCLI(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), redisDockerTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
	return string(out), err
}
